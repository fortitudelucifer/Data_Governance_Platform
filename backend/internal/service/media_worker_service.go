package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// media_worker_service.go — derived-asset pipeline (plan_v2 T0.3). Polls
// audio/video assets that are QC-passed and pending preprocessing, leases them,
// and produces: probed metadata (duration/fps/sample_rate), audio waveform
// peaks, video frame index + a keyframe thumbnail. Mirrors AIWorker's
// poll + bounded-pool + lease + graceful-drain shape.

// MediaWorkerConfig tunes the worker.
type MediaWorkerConfig struct {
	Enabled        bool
	Interval       time.Duration
	BatchSize      int
	Concurrency    int
	LeaseTTL       time.Duration
	MaxRetries     int
	WaveformRate   int     // PCM decode rate for peaks
	WaveformBucket int     // samples per peak bucket
	ThumbnailWidth int     // px
	ThumbnailAtSec float64 // timestamp for the keyframe thumbnail
}

// DefaultMediaWorkerConfig returns sane defaults.
func DefaultMediaWorkerConfig() MediaWorkerConfig {
	return MediaWorkerConfig{
		Enabled:        true,
		Interval:       5 * time.Second,
		BatchSize:      4,
		Concurrency:    2,
		LeaseTTL:       5 * time.Minute,
		MaxRetries:     3,
		WaveformRate:   8000,
		WaveformBucket: 256,
		ThumbnailWidth: 320,
		ThumbnailAtSec: 1.0,
	}
}

// MediaWorker runs the derive pipeline.
type MediaWorker struct {
	cfg   MediaWorkerConfig
	db *repository.DB
	store ObjectStore
	tools MediaTools

	sem    chan struct{}
	stopCh chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
}

// NewMediaWorker wires the worker.
func NewMediaWorker(cfg MediaWorkerConfig, db *repository.DB, store ObjectStore, tools MediaTools) *MediaWorker {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = cfg.Concurrency
	}
	return &MediaWorker{
		cfg:    cfg,
		db:  db,
		store:  store,
		tools:  tools,
		sem:    make(chan struct{}, cfg.Concurrency),
		stopCh: make(chan struct{}),
	}
}

// Start launches the loop. Safe to call once.
func (w *MediaWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.loop(ctx)
}

// Stop signals shutdown and waits for in-flight derivations to drain (or ctx).
func (w *MediaWorker) Stop(ctx context.Context) error {
	w.once.Do(func() { close(w.stopCh) })
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *MediaWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	tick := time.NewTicker(w.cfg.Interval)
	defer tick.Stop()
	slog.Info("media_worker started", "interval", w.cfg.Interval, "batch", w.cfg.BatchSize, "concurrency", w.cfg.Concurrency, "ffmpeg", w.tools.FFmpeg)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-tick.C:
			w.tick(ctx)
		}
	}
}

func (w *MediaWorker) tick(ctx context.Context) {
	leaseUntil := time.Now().Add(w.cfg.LeaseTTL)
	assets, err := w.db.LeaseDuePreprocessAssets(ctx, leaseUntil, w.cfg.BatchSize)
	if err != nil {
		slog.Error("media_worker lease failed", "error", err)
		return
	}
	for i := range assets {
		a := assets[i]
		select {
		case <-w.stopCh:
			return
		case w.sem <- struct{}{}:
		}
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			defer func() { <-w.sem }()
			w.process(ctx, &a)
		}()
	}
}

// playableVideoCodecs is the QC whitelist of browser-reliable video codecs
// (执行方案-02 §编解码可播性). HEVC/AV1/MPEG-4 decode is uneven across
// browsers / low-end annotation machines → rejected this phase. Configurable
// later; a transcode-proxy derivative is the future escape hatch.
var playableVideoCodecs = map[string]bool{"h264": true, "vp8": true, "vp9": true}

// terminalPreprocessError marks a non-retryable preprocessing failure — the
// bytes won't change on retry (e.g. an unsupported codec), so fail immediately.
type terminalPreprocessError struct{ reason string }

func (e *terminalPreprocessError) Error() string { return e.reason }

func terminalf(format string, a ...interface{}) error {
	return &terminalPreprocessError{reason: fmt.Sprintf(format, a...)}
}

// process derives artifacts for one asset and updates its preprocess state.
func (w *MediaWorker) process(ctx context.Context, a *dbmodel.Asset) {
	if err := w.derive(ctx, a); err != nil {
		var term *terminalPreprocessError
		if errors.As(err, &term) {
			slog.Warn("media_worker rejected (terminal)", "asset_id", a.ID, "reason", term.reason)
			_ = w.db.MarkPreprocessRejected(ctx, a.ID, term.reason) // status=rejected, never re-claimed
			return
		}
		slog.Error("media_worker derive failed", "asset_id", a.ID, "attempt", a.PreprocessAttempts, "error", err)
		if a.PreprocessAttempts >= w.cfg.MaxRetries {
			_ = w.db.MarkPreprocessFailed(ctx, a.ID, err.Error(), nil) // terminal
			return
		}
		backoff := time.Duration(a.PreprocessAttempts) * 30 * time.Second
		retryAt := time.Now().Add(backoff)
		_ = w.db.MarkPreprocessFailed(ctx, a.ID, err.Error(), &retryAt)
		return
	}
	if err := w.db.MarkPreprocessReady(ctx, a.ID); err != nil {
		slog.Error("media_worker mark ready failed", "asset_id", a.ID, "error", err)
		return
	}
	slog.Info("media_worker derived", "asset_id", a.ID, "modality", a.Modality)
}

func (w *MediaWorker) derive(ctx context.Context, a *dbmodel.Asset) error {
	// Materialise the source to a temp file so ffmpeg/ffprobe can seek it.
	src, cleanup, err := w.fetchToTemp(ctx, a)
	if err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}
	defer cleanup()

	meta, err := w.tools.Probe(ctx, src)
	if err != nil {
		return err
	}
	_ = w.db.UpdateAssetMediaMeta(ctx, a.ID, meta.DurationMs, meta.FPS, meta.SampleRate, meta.Width, meta.Height)

	switch a.Modality {
	case dbmodel.ModalityAudio:
		peaks, err := w.tools.WaveformPeaks(ctx, src, w.cfg.WaveformRate, w.cfg.WaveformBucket)
		if err != nil {
			return err
		}
		ph := paramsHash(fmt.Sprintf("wf-%d-%d", w.cfg.WaveformRate, w.cfg.WaveformBucket))
		if err := w.putDerivative(ctx, a, dbmodel.DerivativeWaveform, ph, "application/json", peaks); err != nil {
			return err
		}
	case dbmodel.ModalityVideo:
		if !meta.HasVideo {
			return terminalf("文件不含视频流，无法作为视频标注")
		}
		// Codec playability. If the source codec isn't browser-reliable
		// (HEVC/AV1/MPEG-4…), transcode to H.264/AAC once and serve that
		// derivative to <video> — annotators never have to transcode by hand.
		// The frame index/thumbnail are then derived from the stream the
		// browser actually plays, so frame↔time stays 0-offset.
		playSrc := src
		playMeta := meta
		if !playableVideoCodecs[meta.VideoCodec] {
			tpath, tcleanup, terr := w.tools.Transcode(ctx, src)
			if terr != nil {
				return terminalf("视频编码 %q 无法转码为可播放格式（%v）", meta.VideoCodec, terr)
			}
			defer tcleanup()
			if err := w.putDerivativeFile(ctx, a, dbmodel.DerivativePlayback, paramsHash("pb-h264-v1"), "video/mp4", "mp4", tpath); err != nil {
				return err
			}
			playSrc = tpath
			// Re-probe the transcoded stream: rotation is baked in (→0), dims
			// may swap, and pts come from the new stream.
			if pm, perr := w.tools.Probe(ctx, tpath); perr == nil {
				playMeta = pm
				_ = w.db.UpdateAssetMediaMeta(ctx, a.ID, pm.DurationMs, pm.FPS, pm.SampleRate, pm.Width, pm.Height)
			}
			playMeta.Playback = true
			slog.Info("media_worker transcoded to playable", "asset_id", a.ID, "from_codec", meta.VideoCodec)
		}
		idx, err := w.tools.FrameIndex(ctx, playSrc, playMeta)
		if err != nil {
			return err
		}
		if err := w.putDerivative(ctx, a, dbmodel.DerivativeFrameIndex, paramsHash("fi-v1"), "application/json", idx); err != nil {
			return err
		}
		thumb, err := w.tools.Thumbnail(ctx, playSrc, w.cfg.ThumbnailAtSec, w.cfg.ThumbnailWidth)
		if err != nil {
			// Thumbnail is non-fatal — log and continue (frame index is the
			// critical artifact).
			slog.Warn("media_worker thumbnail failed", "asset_id", a.ID, "error", err)
		} else if err := w.putDerivative(ctx, a, dbmodel.DerivativeThumbnail, paramsHash(fmt.Sprintf("th-%d", w.cfg.ThumbnailWidth)), "image/jpeg", thumb); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported modality for preprocessing: %s", a.Modality)
	}
	return nil
}

// putDerivative stores bytes in the object store and upserts the DB row.
func (w *MediaWorker) putDerivative(ctx context.Context, a *dbmodel.Asset, kind, ph, mime string, body []byte) error {
	ext := "json"
	if mime == "image/jpeg" {
		ext = "jpg"
	}
	key := fmt.Sprintf("derived/%s/%s/v1/%s.%s", a.SHA256, kind, ph, ext)
	res, err := w.store.PutAt(ctx, key, bytes.NewReader(body), int64(len(body)), mime)
	if err != nil {
		return fmt.Errorf("put %s: %w", kind, err)
	}
	d := &dbmodel.AssetDerivative{
		AssetID:    a.ID,
		Kind:       kind,
		Version:    1,
		ParamsHash: ph,
		StorageURI: res.StorageURI,
		Status:     "ready",
		SizeBytes:  int64(len(body)),
	}
	return w.db.UpsertDerivative(ctx, d)
}

// putDerivativeFile streams a derived file (e.g. a transcoded MP4, too large to
// hold in memory) to the object store and upserts the DB row.
func (w *MediaWorker) putDerivativeFile(ctx context.Context, a *dbmodel.Asset, kind, ph, mime, ext, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", kind, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", kind, err)
	}
	key := fmt.Sprintf("derived/%s/%s/v1/%s.%s", a.SHA256, kind, ph, ext)
	res, err := w.store.PutAt(ctx, key, f, st.Size(), mime)
	if err != nil {
		return fmt.Errorf("put %s: %w", kind, err)
	}
	d := &dbmodel.AssetDerivative{
		AssetID:    a.ID,
		Kind:       kind,
		Version:    1,
		ParamsHash: ph,
		StorageURI: res.StorageURI,
		Status:     "ready",
		SizeBytes:  st.Size(),
	}
	return w.db.UpsertDerivative(ctx, d)
}

// fetchToTemp streams the asset bytes to a temp file and returns its path.
func (w *MediaWorker) fetchToTemp(ctx context.Context, a *dbmodel.Asset) (string, func(), error) {
	rc, err := w.store.Get(ctx, a.StorageURI)
	if err != nil {
		return "", func() {}, err
	}
	defer rc.Close()
	ext := filepath.Ext(a.OriginalName)
	tmp, err := os.CreateTemp("", "mediasrc-*"+ext)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp.Name(), cleanup, nil
}

// paramsHash returns a short stable hash for the derivation params (cache key).
func paramsHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}
