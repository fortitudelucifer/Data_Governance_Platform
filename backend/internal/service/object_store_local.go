package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalObjectStore is the dev / single-node ObjectStore driver. It writes
// objects under a content-addressable layout:
//
//	<root>/<dataset_id>/<sha256[:2]>/<sha256>
//
// and returns storageURIs of the form `local://<dataset_id>/<sha256[:2]>/<sha256>`.
// Production deployments should switch to a MinIO-backed driver behind the
// same interface (ADR-05).
type LocalObjectStore struct {
	root string
}

// NewLocalObjectStore creates a LocalObjectStore rooted at root. The directory
// is created on demand.
func NewLocalObjectStore(root string) (*LocalObjectStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("local object store root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	return &LocalObjectStore{root: abs}, nil
}

// Driver implements ObjectStore.
func (s *LocalObjectStore) Driver() string { return "local" }

// Put implements ObjectStore. It writes the body via a temp file + rename to
// avoid partial writes.
func (s *LocalObjectStore) Put(ctx context.Context, req PutRequest) (PutResult, error) {
	if req.Body == nil {
		return PutResult{}, errors.New("nil body")
	}
	if req.DatasetID == 0 {
		return PutResult{}, errors.New("dataset_id required")
	}

	// Buffer to a temp file while streaming, hash on the way through.
	tmpDir := filepath.Join(s.root, "_tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return PutResult{}, fmt.Errorf("mkdir tmp: %w", err)
	}
	tmp, err := os.CreateTemp(tmpDir, "asset-*.bin")
	if err != nil {
		return PutResult{}, fmt.Errorf("create tmp: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	hasher := sha256.New()
	mw := io.MultiWriter(tmp, hasher)
	written, err := io.Copy(mw, req.Body)
	if err != nil {
		return PutResult{}, fmt.Errorf("copy body: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return PutResult{}, fmt.Errorf("sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return PutResult{}, fmt.Errorf("close tmp: %w", err)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, digest) {
		return PutResult{}, fmt.Errorf("sha256 mismatch: declared=%s actual=%s", req.SHA256, digest)
	}

	// Compose the canonical relative path and absolute target.
	rel := filepath.Join(fmt.Sprintf("%d", req.DatasetID), digest[:2], digest)
	absDir := filepath.Join(s.root, filepath.Dir(rel))
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return PutResult{}, fmt.Errorf("mkdir target: %w", err)
	}
	abs := filepath.Join(s.root, rel)

	// If a file already exists with the same SHA256, treat as deduplicated.
	if _, err := os.Stat(abs); err == nil {
		// Drop the temp; the existing canonical file is authoritative.
		_ = os.Remove(tmp.Name())
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(tmp.Name(), abs); err != nil {
			return PutResult{}, fmt.Errorf("rename to canonical: %w", err)
		}
	} else {
		return PutResult{}, fmt.Errorf("stat canonical: %w", err)
	}

	uri := fmt.Sprintf("local://%d/%s/%s", req.DatasetID, digest[:2], digest)
	return PutResult{
		StorageURI: uri,
		SHA256:     digest,
		Size:       written,
		MIME:       req.MIME,
	}, nil
}

// PutAt implements ObjectStore. Writes body at an explicit relative key via
// temp file + rename. Returns local://<key>.
func (s *LocalObjectStore) PutAt(_ context.Context, key string, body io.Reader, _ int64, mime string) (PutResult, error) {
	if body == nil {
		return PutResult{}, errors.New("nil body")
	}
	cleaned := filepath.Clean(strings.TrimPrefix(key, "/"))
	if cleaned == "" || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "..") {
		return PutResult{}, errors.New("invalid key")
	}
	tmpDir := filepath.Join(s.root, "_tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return PutResult{}, fmt.Errorf("mkdir tmp: %w", err)
	}
	tmp, err := os.CreateTemp(tmpDir, "derived-*.bin")
	if err != nil {
		return PutResult{}, fmt.Errorf("create tmp: %w", err)
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()

	written, err := io.Copy(tmp, body)
	if err != nil {
		return PutResult{}, fmt.Errorf("copy body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return PutResult{}, fmt.Errorf("close tmp: %w", err)
	}
	abs := filepath.Join(s.root, cleaned)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return PutResult{}, fmt.Errorf("mkdir target: %w", err)
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		return PutResult{}, fmt.Errorf("rename: %w", err)
	}
	return PutResult{
		StorageURI: "local://" + filepath.ToSlash(cleaned),
		Size:       written,
		MIME:       mime,
	}, nil
}

// PresignGetURL implements ObjectStore. 本地驱动不支持预签名——返回 "" 让调用方
// 退回 ServeContent 流式（文件在本地磁盘可 seek，仍支持 Range / 缓存头）。
func (s *LocalObjectStore) PresignGetURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}

// Get implements ObjectStore.
func (s *LocalObjectStore) Get(ctx context.Context, storageURI string) (io.ReadCloser, error) {
	abs, err := s.uriToAbs(storageURI)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	return f, nil
}

// Stat implements ObjectStore.
func (s *LocalObjectStore) Stat(ctx context.Context, storageURI string) (StatResult, error) {
	abs, err := s.uriToAbs(storageURI)
	if err != nil {
		return StatResult{}, err
	}
	st, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return StatResult{}, ErrObjectNotFound
	}
	if err != nil {
		return StatResult{}, fmt.Errorf("stat object: %w", err)
	}
	return StatResult{StorageURI: storageURI, Size: st.Size()}, nil
}

// Delete implements ObjectStore.
func (s *LocalObjectStore) Delete(ctx context.Context, storageURI string) error {
	abs, err := s.uriToAbs(storageURI)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// Exists implements ObjectStore.
func (s *LocalObjectStore) Exists(ctx context.Context, storageURI string) (bool, error) {
	abs, err := s.uriToAbs(storageURI)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// AbsPath returns the resolved filesystem path for a stored object. Exposed
// so HTTP handlers can stream files directly via http.ServeFile when there is
// no need to involve another reader layer.
func (s *LocalObjectStore) AbsPath(storageURI string) (string, error) {
	return s.uriToAbs(storageURI)
}

// uriToAbs validates and resolves a `local://...` URI to an absolute path under
// the configured root. It guards against `..` traversal.
func (s *LocalObjectStore) uriToAbs(storageURI string) (string, error) {
	u, err := url.Parse(storageURI)
	if err != nil {
		return "", fmt.Errorf("parse storage uri: %w", err)
	}
	if u.Scheme != "local" {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	rel := strings.TrimPrefix(u.Host+u.Path, "/")
	if rel == "" {
		return "", errors.New("empty storage path")
	}
	cleaned := filepath.Clean(rel)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "..") {
		return "", errors.New("invalid storage path: traversal not allowed")
	}
	abs := filepath.Join(s.root, cleaned)
	// Defence in depth: verify the absolute path stays under root.
	if !strings.HasPrefix(abs, s.root) {
		return "", errors.New("invalid storage path: outside root")
	}
	return abs, nil
}
