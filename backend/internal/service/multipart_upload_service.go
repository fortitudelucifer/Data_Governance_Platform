package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// multipart_upload_service.go — resumable, direct-to-store chunked uploads
// (plan_v2 T0.2). The app only handles the control plane (init/complete/abort/
// status); bytes go browser→object store via presigned part URLs. Requires the
// MinIO driver; the local driver returns ErrMultipartUnsupported and callers
// fall back to the simple upload path.

// ErrMultipartUnsupported is returned when the object store can't do multipart.
var ErrMultipartUnsupported = errors.New("multipart upload requires the minio object store driver")

// ErrHashMismatch is returned when client_sha256 != server-computed sha256.
var ErrHashMismatch = errors.New("hash_mismatch")

const minPartSize = 5 << 20 // S3 floor for non-final parts

// MultipartUploadConfig tunes the upload lifecycle.
type MultipartUploadConfig struct {
	PartSize   int64
	MaxSize    int64
	SessionTTL time.Duration
	PresignTTL time.Duration
}

// DefaultMultipartUploadConfig returns sane defaults (16 MiB parts, 12h
// session, 2h presign, 50 GiB cap).
func DefaultMultipartUploadConfig() MultipartUploadConfig {
	return MultipartUploadConfig{
		PartSize:   16 << 20,
		MaxSize:    50 << 30,
		SessionTTL: 12 * time.Hour,
		PresignTTL: 2 * time.Hour,
	}
}

// MultipartUploadService orchestrates sessions. store is nil when the driver
// lacks multipart support.
type MultipartUploadService struct {
	db  *repository.DB
	store  MultipartObjectStore
	assets *AssetService
	cfg    MultipartUploadConfig
}

// NewMultipartUploadService wires the service. Pass a nil store to disable.
func NewMultipartUploadService(db *repository.DB, store MultipartObjectStore, assets *AssetService, cfg MultipartUploadConfig) *MultipartUploadService {
	if cfg.PartSize < minPartSize {
		cfg.PartSize = DefaultMultipartUploadConfig().PartSize
	}
	return &MultipartUploadService{db: db, store: store, assets: assets, cfg: cfg}
}

// Supported reports whether multipart uploads are available.
func (s *MultipartUploadService) Supported() bool { return s.store != nil }

// MultipartInitResult is returned by Init.
type MultipartInitResult struct {
	SessionID string    `json:"session_id"`
	UploadID  string    `json:"upload_id"`
	PartSize  int64     `json:"part_size"`
	PartCount int       `json:"part_count"`
	PartURLs  []string  `json:"part_urls"` // index i → part number i+1
	ExpiresAt time.Time `json:"expires_at"`
}

// Init validates the request, starts a multipart upload and presigns all part
// URLs. The caller (handler) supplies the authenticated user id.
func (s *MultipartUploadService) Init(ctx context.Context, userID, datasetID uint, filename, contentType string, sizeBytes int64, clientSHA string) (*MultipartInitResult, error) {
	if s.store == nil {
		return nil, ErrMultipartUnsupported
	}
	if sizeBytes <= 0 {
		return nil, errors.New("size_bytes must be > 0")
	}
	if sizeBytes > s.cfg.MaxSize {
		return nil, fmt.Errorf("file exceeds max size %d", s.cfg.MaxSize)
	}
	ds, err := s.db.FindDatasetByID(ctx, datasetID)
	if err != nil {
		return nil, fmt.Errorf("dataset lookup: %w", err)
	}
	if ds.Modality == dbmodel.ModalityText || ds.Modality == "" {
		return nil, ErrDatasetNotImage
	}
	// Medical (wsi/volume): fail-closed gate BEFORE issuing upload URLs, so
	// clinical data without a deid pipeline never gets a place to land (C-H1:
	// 去标识必须发生在写对象存储之前). RegisterAsset gates again at Complete.
	if err := s.assets.AdmitMedicalIngest(ctx, ds, userID); err != nil {
		return nil, err
	}

	s.cleanupExpired(ctx) // opportunistic janitor

	sessionID, err := randHex(16)
	if err != nil {
		return nil, err
	}
	tempKey := fmt.Sprintf("uploads/%d/%s/raw", userID, sessionID)
	uploadID, err := s.store.InitMultipart(ctx, tempKey, contentType)
	if err != nil {
		return nil, fmt.Errorf("init multipart: %w", err)
	}
	partCount := int((sizeBytes + s.cfg.PartSize - 1) / s.cfg.PartSize)
	urls := make([]string, 0, partCount)
	for i := 1; i <= partCount; i++ {
		u, perr := s.store.PresignPart(ctx, tempKey, uploadID, i, s.cfg.PresignTTL)
		if perr != nil {
			_ = s.store.AbortMultipart(ctx, tempKey, uploadID)
			return nil, fmt.Errorf("presign part %d: %w", i, perr)
		}
		urls = append(urls, u)
	}
	expiresAt := time.Now().Add(s.cfg.SessionTTL)
	sess := &dbmodel.UploadSession{
		SessionID:     sessionID,
		UploadID:      uploadID,
		UserID:        userID,
		DatasetID:     datasetID,
		Modality:      ds.Modality,
		Filename:      filename,
		ContentType:   contentType,
		SizeBytes:     sizeBytes,
		PartSize:      s.cfg.PartSize,
		TempObjectKey: tempKey,
		ClientSHA256:  clientSHA,
		Status:        dbmodel.UploadPending,
		ExpiresAt:     expiresAt,
	}
	if err := s.db.CreateUploadSession(ctx, sess); err != nil {
		_ = s.store.AbortMultipart(ctx, tempKey, uploadID)
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &MultipartInitResult{
		SessionID: sessionID, UploadID: uploadID, PartSize: s.cfg.PartSize,
		PartCount: partCount, PartURLs: urls, ExpiresAt: expiresAt,
	}, nil
}

// MultipartStatusResult reports session progress for resume.
type MultipartStatusResult struct {
	Status        string    `json:"status"`
	PartSize      int64     `json:"part_size"`
	SizeBytes     int64     `json:"size_bytes"`
	ExpiresAt     time.Time `json:"expires_at"`
	UploadedParts []int     `json:"uploaded_parts"`
}

// Status returns the session + already-uploaded part numbers (resume).
func (s *MultipartUploadService) Status(ctx context.Context, userID uint, sessionID string) (*MultipartStatusResult, error) {
	sess, err := s.ownedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	out := &MultipartStatusResult{
		Status: sess.Status, PartSize: sess.PartSize, SizeBytes: sess.SizeBytes, ExpiresAt: sess.ExpiresAt,
	}
	if s.store != nil && sess.Status == dbmodel.UploadPending {
		if parts, perr := s.store.ListParts(ctx, sess.TempObjectKey, sess.UploadID); perr == nil {
			for _, p := range parts {
				out.UploadedParts = append(out.UploadedParts, p.PartNumber)
			}
		}
	}
	return out, nil
}

// Complete assembles parts, verifies the hash, copies to the content-addressed
// key and registers the asset.
func (s *MultipartUploadService) Complete(ctx context.Context, userID uint, sessionID, uploadID string, parts []MultipartPart, clientSHA string) (*UploadResult, error) {
	if s.store == nil {
		return nil, ErrMultipartUnsupported
	}
	sess, err := s.ownedSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status != dbmodel.UploadPending {
		return nil, fmt.Errorf("session not pending (status=%s)", sess.Status)
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	if uploadID != "" && uploadID != sess.UploadID {
		return nil, errors.New("upload_id mismatch")
	}
	if len(parts) == 0 {
		return nil, errors.New("no parts")
	}
	// #3 原子认领 pending→completing。并发两个 Complete 里只有一个能翻成 completing,
	// 另一个 claimed=false 直接退出——不会两个都读到 pending 各跑一遍、其中一个注册成功
	// 另一个 NoSuchUpload 后又把会话反写成 failed。此后所有分支都在 completing 态里跑。
	claimed, cerr := s.db.CASUploadSessionStatus(ctx, sessionID, dbmodel.UploadPending, dbmodel.UploadCompleting, nil)
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, errors.New("session not pending or a completion is already in progress")
	}

	if err := s.store.CompleteMultipart(ctx, sess.TempObjectKey, sess.UploadID, parts); err != nil {
		s.fail(ctx, sess, "complete multipart: "+err.Error())
		return nil, fmt.Errorf("complete multipart: %w", err)
	}
	sha, size, head, err := s.store.StreamSHA256(ctx, sess.TempObjectKey)
	if err != nil {
		s.fail(ctx, sess, "hash stream: "+err.Error())
		return nil, fmt.Errorf("hash stream: %w", err)
	}
	// #2 服务端**实测大小**必须等于 Init 声明、且不超上限。旧代码拿到真实 size 却从不
	// 比对 → 声明 100MiB 只交一个 16MiB part(截断文件)照样通过;声明小文件却上传超大
	// part 可绕过 50GiB 上限。声明大小是 Init 算分片数的依据,实测不符即数据不完整/在钻空子。
	if size != sess.SizeBytes {
		_ = s.store.DeleteKey(ctx, sess.TempObjectKey)
		s.fail(ctx, sess, fmt.Sprintf("size mismatch: assembled %d != declared %d", size, sess.SizeBytes))
		return nil, fmt.Errorf("分片组装后大小 %d 与 Init 声明的 %d 不符(截断或不完整)", size, sess.SizeBytes)
	}
	if s.cfg.MaxSize > 0 && size > s.cfg.MaxSize {
		_ = s.store.DeleteKey(ctx, sess.TempObjectKey)
		s.fail(ctx, sess, fmt.Sprintf("size %d exceeds max %d", size, s.cfg.MaxSize))
		return nil, fmt.Errorf("文件大小 %d 超过上限 %d", size, s.cfg.MaxSize)
	}
	if clientSHA == "" {
		clientSHA = sess.ClientSHA256
	}
	if clientSHA != "" && !strings.EqualFold(clientSHA, sha) {
		_ = s.store.DeleteKey(ctx, sess.TempObjectKey)
		s.fail(ctx, sess, ErrHashMismatch.Error())
		return nil, ErrHashMismatch
	}

	mime := http.DetectContentType(head)
	if err := checkModalityMIME(sess.Modality, mime); err != nil {
		_ = s.store.DeleteKey(ctx, sess.TempObjectKey)
		s.fail(ctx, sess, err.Error())
		return nil, err
	}

	finalKey := s.store.KeyForContent(sess.DatasetID, sha)
	finalURI, err := s.store.CopyTo(ctx, sess.TempObjectKey, finalKey)
	if err != nil {
		s.fail(ctx, sess, "copy to final: "+err.Error())
		return nil, fmt.Errorf("copy to final: %w", err)
	}
	_ = s.store.DeleteKey(ctx, sess.TempObjectKey) // best-effort temp cleanup

	res, err := s.assets.RegisterAsset(ctx, RegisterAssetOpts{
		DatasetID:    sess.DatasetID,
		Modality:     sess.Modality,
		StorageURI:   finalURI,
		OriginalName: sess.Filename,
		MIME:         mime,
		SHA256:       sha,
		SizeBytes:    size,
		UploaderID:   userID,
	})
	if err != nil {
		s.fail(ctx, sess, "register asset: "+err.Error())
		return nil, err
	}
	now := time.Now()
	// #3 completing→completed(CAS,不无条件反写);写失败不再静默吞:资产已注册(SHA
	// 去重幂等),会话状态只是元数据,记日志即可,但绝不谎报失败。
	if _, uerr := s.db.CASUploadSessionStatus(ctx, sessionID, dbmodel.UploadCompleting, dbmodel.UploadCompleted, map[string]interface{}{
		"server_sha256":    sha,
		"final_object_key": finalKey,
		"asset_id":         res.Asset.ID,
		"completed_at":     &now,
	}); uerr != nil {
		slog.Error("multipart: mark session completed failed (asset already registered)", "session", sessionID, "error", uerr)
	}
	return res, nil
}

// Abort cancels a session and frees its parts + temp object.
func (s *MultipartUploadService) Abort(ctx context.Context, userID uint, sessionID string) error {
	sess, err := s.ownedSession(ctx, userID, sessionID)
	if err != nil {
		return err
	}
	if s.store != nil {
		_ = s.store.AbortMultipart(ctx, sess.TempObjectKey, sess.UploadID)
		if err := s.store.DeleteKey(ctx, sess.TempObjectKey); err != nil {
			// #4 温对象没删掉 → 不落 aborted(终态、不再回收);标 failed + expires_at=now,
			// janitor 下一轮立刻重试清理。用户视角会话已取消(不会完成),清理在后台兜底。
			_ = s.db.UpdateUploadSession(ctx, sessionID, map[string]interface{}{
				"status": dbmodel.UploadFailed, "error": "abort temp cleanup failed: " + err.Error(),
				"expires_at": time.Now(),
			})
			return nil
		}
	}
	return s.db.UpdateUploadSession(ctx, sessionID, map[string]interface{}{"status": dbmodel.UploadAborted})
}

// ownedSession loads a session and enforces ownership.
func (s *MultipartUploadService) ownedSession(ctx context.Context, userID uint, sessionID string) (*dbmodel.UploadSession, error) {
	sess, err := s.db.FindUploadSession(ctx, sessionID)
	if err != nil {
		return nil, errors.New("session not found")
	}
	if sess.UserID != userID {
		return nil, errors.New("forbidden: not session owner")
	}
	return sess, nil
}

func (s *MultipartUploadService) fail(ctx context.Context, sess *dbmodel.UploadSession, msg string) {
	// #3 只从 completing 反写 failed(CAS),绝不无条件覆盖——否则一次晚到的失败会把已
	// completed 的会话反写成 failed。所有 fail() 调用点都在认领 completing 之后。
	_, _ = s.db.CASUploadSessionStatus(ctx, sess.SessionID, dbmodel.UploadCompleting, dbmodel.UploadFailed, map[string]interface{}{"error": msg})
}

// StartJanitor runs a **resident** cleanup loop (#4). The old cleanup was
// opportunistic — only fired from Init, at most 5 rows — so if uploads stop, every
// overdue session's parts + temp object leak forever. This drains on a timer,
// independent of request traffic. Stop via ctx cancellation.
func (s *MultipartUploadService) StartJanitor(ctx context.Context, interval time.Duration) {
	if s.store == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.cleanupExpired(ctx)
			}
		}
	}()
}

// cleanupExpired aborts + removes overdue sessions. **只有对象真的清干净了才把会话标
// expired**(#4):旧代码 AbortMultipart/DeleteKey 失败也照标 expired,而 expired 不在
// 可回收查询集里 → parts/temp 从此永久失联。现在清理失败就保持 pending/failed(可回收),
// 下一轮再试;确认成功才落终态。
func (s *MultipartUploadService) cleanupExpired(ctx context.Context) {
	if s.store == nil {
		return
	}
	sessions, err := s.db.ListReclaimableUploadSessions(ctx, time.Now(), 50)
	if err != nil {
		return
	}
	for i := range sessions {
		ss := sessions[i]
		_ = s.store.AbortMultipart(ctx, ss.TempObjectKey, ss.UploadID) // best-effort:parts 可能已释放
		if err := s.store.DeleteKey(ctx, ss.TempObjectKey); err != nil {
			continue // 温对象没删掉 → 不标 expired,保持可回收,下轮重试(不再永久失联)
		}
		_ = s.db.UpdateUploadSession(ctx, ss.SessionID, map[string]interface{}{"status": dbmodel.UploadExpired})
	}
}

// checkModalityMIME rejects a clear cross-category mismatch among image/audio/
// video. Inconclusive sniffs (octet-stream) trust the dataset's modality.
func checkModalityMIME(modality, mime string) error {
	kind := mime
	if i := strings.IndexByte(kind, '/'); i > 0 {
		kind = kind[:i]
	}
	// #5 医学模态也纳入判定:application/x-nifti→volume、application/x-wsi→wsi。
	switch mime {
	case "application/x-nifti":
		kind = "volume"
	case "application/x-wsi":
		kind = "wsi"
	}
	switch kind {
	case "image", "audio", "video", "volume", "wsi":
		if kind != modality {
			return fmt.Errorf("content kind %q does not match dataset modality %q（PNG 传进 video 集这类路由错误）", kind, modality)
		}
		return nil
	}
	return nil // inconclusive — trust dataset modality
}

// randHex returns n random bytes hex-encoded.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
