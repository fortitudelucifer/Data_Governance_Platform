package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// media_probe.go — ffmpeg/ffprobe wrappers for the derived-asset pipeline
// (plan_v2 T0.3). audiowaveform is intentionally NOT used: waveform peaks are
// computed in-process from ffmpeg-decoded PCM (portable, one fewer binary).

// MediaTools holds the (configurable) binary paths. Empty falls back to PATH.
type MediaTools struct {
	FFmpeg  string
	FFprobe string
}

// NewMediaTools normalises empty paths to the PATH-resolved binaries.
func NewMediaTools(ffmpeg, ffprobe string) MediaTools {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	return MediaTools{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// MediaMeta is the subset of probe output we persist / expose. Rotation and the
// display W/H (rotation-applied) are the 坐标系 anchor (执行方案-02 §坐标系):
// every geometry consumer must work in this displayed-orientation pixel space.
type MediaMeta struct {
	DurationMs *int64
	FPS        *float64
	SampleRate *int
	HasVideo   bool
	HasAudio   bool
	// Video geometry (display / rotation-applied space).
	Rotation *int // CW display rotation degrees, normalized {0,90,180,270}
	Width    *int // display width  (coded W/H swapped when rotation is 90/270)
	Height   *int // display height
	// Codecs (for the QC playability whitelist).
	VideoCodec string
	AudioCodec string
	// Playback is set on the frame-index meta when the browser plays a
	// transcoded derivative rather than the original blob (HEVC/AV1/… source).
	Playback bool
}

type ffprobeOut struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		RFrameRate   string `json:"r_frame_rate"`
		SampleRate   string `json:"sample_rate"`
		Tags         struct {
			Rotate string `json:"rotate"`
		} `json:"tags"`
		SideDataList []struct {
			SideDataType string  `json:"side_data_type"`
			Rotation     float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
}

// Probe runs ffprobe and extracts duration / fps / sample-rate.
func (m MediaTools) Probe(ctx context.Context, path string) (*MediaMeta, error) {
	args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path}
	out, err := runCapture(ctx, m.FFprobe, args...)
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var p ffprobeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	meta := &MediaMeta{}
	if secs, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64); err == nil && secs > 0 {
		ms := int64(math.Round(secs * 1000))
		meta.DurationMs = &ms
	}
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			meta.HasVideo = true
			meta.VideoCodec = strings.ToLower(strings.TrimSpace(s.CodecName))
			if fps := parseRatio(s.AvgFrameRate); fps <= 0 {
				if fps2 := parseRatio(s.RFrameRate); fps2 > 0 {
					meta.FPS = &fps2
				}
			} else {
				meta.FPS = &fps
			}
			// Rotation: Display Matrix side-data is the negative of the CW
			// display angle; tags.rotate is already the CW display angle.
			var rot *int
			for _, sd := range s.SideDataList {
				if strings.Contains(strings.ToLower(sd.SideDataType), "display matrix") {
					r := normalizeRotation(-int(math.Round(sd.Rotation)))
					rot = &r
				}
			}
			if rot == nil {
				if v, err := strconv.Atoi(strings.TrimSpace(s.Tags.Rotate)); err == nil {
					r := normalizeRotation(v)
					rot = &r
				}
			}
			if rot == nil {
				zero := 0
				rot = &zero
			}
			meta.Rotation = rot
			// Display dims: swap coded W/H when rotated a quarter turn.
			if s.Width > 0 && s.Height > 0 {
				w, h := s.Width, s.Height
				if *rot == 90 || *rot == 270 {
					w, h = h, w
				}
				meta.Width, meta.Height = &w, &h
			}
		case "audio":
			meta.HasAudio = true
			meta.AudioCodec = strings.ToLower(strings.TrimSpace(s.CodecName))
			if sr, err := strconv.Atoi(strings.TrimSpace(s.SampleRate)); err == nil && sr > 0 {
				meta.SampleRate = &sr
			}
		}
	}
	return meta, nil
}

// normalizeRotation maps any integer degrees to a CW display angle in
// {0,90,180,270}.
func normalizeRotation(v int) int {
	r := ((v % 360) + 360) % 360
	// snap to the nearest quarter turn (guards odd metadata like 89/271).
	switch {
	case r >= 315 || r < 45:
		return 0
	case r < 135:
		return 90
	case r < 225:
		return 180
	default:
		return 270
	}
}

// WaveformPeaks decodes the audio to mono s16le PCM at sampleRate and computes
// per-bucket min/max, emitting an audiowaveform/Peaks.js-compatible JSON.
func (m MediaTools) WaveformPeaks(ctx context.Context, path string, sampleRate, samplesPerPixel int) ([]byte, error) {
	if sampleRate <= 0 {
		sampleRate = 8000
	}
	if samplesPerPixel <= 0 {
		samplesPerPixel = 256
	}
	args := []string{"-v", "error", "-i", path, "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "s16le", "-"}
	pcm, err := runCapture(ctx, m.FFmpeg, args...)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg pcm decode: %w", err)
	}
	n := len(pcm) / 2 // int16 samples
	buckets := (n + samplesPerPixel - 1) / samplesPerPixel
	data := make([]int16, 0, buckets*2)
	for b := 0; b < buckets; b++ {
		lo := int16(math.MaxInt16)
		hi := int16(math.MinInt16)
		start := b * samplesPerPixel
		end := start + samplesPerPixel
		if end > n {
			end = n
		}
		for i := start; i < end; i++ {
			s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
			if s < lo {
				lo = s
			}
			if s > hi {
				hi = s
			}
		}
		if start >= end { // empty bucket guard
			lo, hi = 0, 0
		}
		data = append(data, lo, hi)
	}
	payload := map[string]interface{}{
		"version":           2,
		"channels":          1,
		"sample_rate":       sampleRate,
		"samples_per_pixel": samplesPerPixel,
		"bits":              16,
		"length":            buckets,
		"data":              data,
	}
	return json.Marshal(payload)
}

// FrameIndex lists each video frame's presentation timestamp (ms), giving a
// frame→time map that survives variable frame rate (Gate 3). It also carries the
// 坐标系 anchor (rotation + display W/H) so the frontend canvas / exporters work
// in the displayed-orientation pixel space. Output JSON:
// {"fps":F,"duration_ms":D,"count":N,"pts_ms":[...],"rotation":R,"width":W,"height":H}.
func (m MediaTools) FrameIndex(ctx context.Context, path string, meta *MediaMeta) ([]byte, error) {
	args := []string{"-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=best_effort_timestamp_time",
		"-of", "csv=p=0", path}
	out, err := runCapture(ctx, m.FFprobe, args...)
	if err != nil {
		return nil, fmt.Errorf("ffprobe frames: %w", err)
	}
	ptsMs := make([]int64, 0, 1024)
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, ","))
		if line == "" || line == "N/A" {
			continue
		}
		if secs, err := strconv.ParseFloat(line, 64); err == nil {
			ptsMs = append(ptsMs, int64(math.Round(secs*1000)))
		}
	}
	payload := map[string]interface{}{
		"count":  len(ptsMs),
		"pts_ms": ptsMs,
	}
	if meta != nil {
		if meta.FPS != nil {
			payload["fps"] = *meta.FPS
		}
		if meta.DurationMs != nil {
			payload["duration_ms"] = *meta.DurationMs
		}
		if meta.Rotation != nil {
			payload["rotation"] = *meta.Rotation
		}
		if meta.Width != nil && meta.Height != nil {
			payload["width"] = *meta.Width
			payload["height"] = *meta.Height
		}
		if meta.Playback {
			// Signals the frontend to load the transcoded playback_mp4
			// derivative for <video> instead of the (unplayable) original.
			payload["playback"] = true
		}
	}
	return json.Marshal(payload)
}

// Transcode re-encodes a source video to a browser-reliable H.264(high)+AAC MP4
// with faststart, applying any rotation metadata so the output is upright
// (rotation=0). Returns the temp output path + a cleanup func. Used to make
// HEVC/AV1/MPEG-4 sources playable in the annotation workspace without asking
// annotators to transcode by hand (执行方案-02 §编解码可播性 escape hatch).
func (m MediaTools) Transcode(ctx context.Context, srcPath string) (string, func(), error) {
	out, err := os.CreateTemp("", "mediaplay-*.mp4")
	if err != nil {
		return "", func() {}, err
	}
	outPath := out.Name()
	_ = out.Close() // ffmpeg -y writes the file itself
	cleanup := func() { _ = os.Remove(outPath) }
	args := []string{
		"-v", "error", "-y", "-i", srcPath,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-pix_fmt", "yuv420p", "-profile:v", "high",
		"-movflags", "+faststart",
		"-c:a", "aac", "-b:a", "128k",
		outPath,
	}
	if _, err := runCapture(ctx, m.FFmpeg, args...); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("ffmpeg transcode: %w", err)
	}
	return outPath, cleanup, nil
}

// Thumbnail extracts a single JPEG frame at atSec, scaled to width (height
// auto). Used as the video board thumbnail.
func (m MediaTools) Thumbnail(ctx context.Context, path string, atSec float64, width int) ([]byte, error) {
	if width <= 0 {
		width = 320
	}
	args := []string{"-v", "error", "-ss", strconv.FormatFloat(atSec, 'f', 3, 64),
		"-i", path, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-1", width),
		"-f", "image2", "-c:v", "mjpeg", "-"}
	jpg, err := runCapture(ctx, m.FFmpeg, args...)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg thumbnail: %w", err)
	}
	if len(jpg) == 0 {
		return nil, fmt.Errorf("ffmpeg thumbnail: empty output")
	}
	return jpg, nil
}

// runCapture runs a command and returns stdout; stderr is folded into the error.
func runCapture(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, fmt.Errorf("%v: %s", err, msg)
	}
	return stdout.Bytes(), nil
}

// parseRatio parses ffprobe ratios like "30000/1001" or "25/1" into a float.
func parseRatio(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0/0" {
		return 0
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 {
		num, e1 := strconv.ParseFloat(parts[0], 64)
		den, e2 := strconv.ParseFloat(parts[1], 64)
		if e1 == nil && e2 == nil && den != 0 {
			return num / den
		}
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
