package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

	if err := s.store.CompleteMultipart(ctx, sess.TempObjectKey, sess.UploadID, parts); err != nil {
		s.fail(ctx, sess, "complete multipart: "+err.Error())
		return nil, fmt.Errorf("complete multipart: %w", err)
	}
	sha, size, head, err := s.store.StreamSHA256(ctx, sess.TempObjectKey)
	if err != nil {
		s.fail(ctx, sess, "hash stream: "+err.Error())
		return nil, fmt.Errorf("hash stream: %w", err)
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
	_ = s.db.UpdateUploadSession(ctx, sessionID, map[string]interface{}{
		"status":           dbmodel.UploadCompleted,
		"server_sha256":    sha,
		"final_object_key": finalKey,
		"asset_id":         res.Asset.ID,
		"completed_at":     &now,
	})
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
		_ = s.store.DeleteKey(ctx, sess.TempObjectKey)
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
	_ = s.db.UpdateUploadSession(ctx, sess.SessionID, map[string]interface{}{
		"status": dbmodel.UploadFailed, "error": msg,
	})
}

// cleanupExpired aborts + removes a few overdue sessions (opportunistic janitor).
func (s *MultipartUploadService) cleanupExpired(ctx context.Context) {
	if s.store == nil {
		return
	}
	sessions, err := s.db.ListReclaimableUploadSessions(ctx, time.Now(), 5)
	if err != nil {
		return
	}
	for i := range sessions {
		ss := sessions[i]
		_ = s.store.AbortMultipart(ctx, ss.TempObjectKey, ss.UploadID)
		_ = s.store.DeleteKey(ctx, ss.TempObjectKey)
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
	if kind != "image" && kind != "audio" && kind != "video" {
		return nil // inconclusive — trust dataset modality
	}
	if kind != modality {
		return fmt.Errorf("content kind %q does not match dataset modality %q", kind, modality)
	}
	return nil
}

// randHex returns n random bytes hex-encoded.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
