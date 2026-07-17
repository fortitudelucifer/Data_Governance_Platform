package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOObjectStore implements ObjectStore backed by an S3-compatible MinIO server.
// Objects are stored at: <bucket>/<dataset_id>/<sha256[:2]>/<sha256>
// Storage URIs have the form: minio://<bucket>/<dataset_id>/<sha256[:2]>/<sha256>
type MinIOObjectStore struct {
	client *minio.Client
	bucket string
}

// MinIOConfig captures wiring for the MinIO driver.
type MinIOConfig struct {
	Endpoint  string // host:port, e.g. "localhost:9000"
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// NewMinIOObjectStore creates a MinIOObjectStore and ensures the target bucket exists.
func NewMinIOObjectStore(ctx context.Context, cfg MinIOConfig) (*MinIOObjectStore, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("minio endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("minio bucket is required")
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	// Ensure bucket exists; create if absent.
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %q: %w", cfg.Bucket, err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket %q: %w", cfg.Bucket, err)
		}
	}

	return &MinIOObjectStore{client: client, bucket: cfg.Bucket}, nil
}

// Driver implements ObjectStore.
func (s *MinIOObjectStore) Driver() string { return "minio" }

// Put implements ObjectStore. Buffers the body to compute SHA256, then uploads.
func (s *MinIOObjectStore) Put(ctx context.Context, req PutRequest) (PutResult, error) {
	if req.Body == nil {
		return PutResult{}, errors.New("nil body")
	}
	if req.DatasetID == 0 {
		return PutResult{}, errors.New("dataset_id required")
	}

	// Buffer body to compute SHA256 (required to build content-addressable key).
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return PutResult{}, fmt.Errorf("read body: %w", err)
	}

	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, digest) {
		return PutResult{}, fmt.Errorf("sha256 mismatch: declared=%s actual=%s", req.SHA256, digest)
	}

	objectKey := fmt.Sprintf("%d/%s/%s", req.DatasetID, digest[:2], digest)

	// Check for existing object to support deduplication (idempotent).
	_, statErr := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if statErr != nil {
		// Upload only when not already present.
		opts := minio.PutObjectOptions{}
		if req.MIME != "" {
			opts.ContentType = req.MIME
		}
		_, err = s.client.PutObject(ctx, s.bucket, objectKey, bytes.NewReader(raw), int64(len(raw)), opts)
		if err != nil {
			return PutResult{}, fmt.Errorf("put object: %w", err)
		}
	}

	return PutResult{
		StorageURI: fmt.Sprintf("minio://%s/%s", s.bucket, objectKey),
		SHA256:     digest,
		Size:       int64(len(raw)),
		MIME:       req.MIME,
	}, nil
}

// PutAt implements ObjectStore. Stores body at an explicit object key and
// returns minio://<bucket>/<key>. size may be -1 (streaming/auto multipart).
func (s *MinIOObjectStore) PutAt(ctx context.Context, key string, body io.Reader, size int64, mime string) (PutResult, error) {
	if body == nil {
		return PutResult{}, errors.New("nil body")
	}
	if strings.TrimSpace(key) == "" || strings.Contains(key, "..") {
		return PutResult{}, errors.New("invalid key")
	}
	opts := minio.PutObjectOptions{}
	if mime != "" {
		opts.ContentType = mime
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, body, size, opts)
	if err != nil {
		return PutResult{}, fmt.Errorf("put object at %q: %w", key, err)
	}
	return PutResult{
		StorageURI: fmt.Sprintf("minio://%s/%s", s.bucket, key),
		Size:       info.Size,
		MIME:       mime,
	}, nil
}

// PresignGetURL implements ObjectStore：返回 MinIO 预签名直连 URL，让浏览器直接
// 从对象存储取字节（原生支持 Range / 视频 seek），不再穿过应用进程。
func (s *MinIOObjectStore) PresignGetURL(ctx context.Context, storageURI string, expiry time.Duration) (string, error) {
	objectKey, err := s.uriToKey(storageURI)
	if err != nil {
		return "", err
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, objectKey, expiry, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Get implements ObjectStore.
func (s *MinIOObjectStore) Get(ctx context.Context, storageURI string) (io.ReadCloser, error) {
	objectKey, err := s.uriToKey(storageURI)
	if err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	// Validate that the object actually exists by peeking at its stats.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("stat object: %w", err)
	}
	return obj, nil
}

// Stat implements ObjectStore.
func (s *MinIOObjectStore) Stat(ctx context.Context, storageURI string) (StatResult, error) {
	objectKey, err := s.uriToKey(storageURI)
	if err != nil {
		return StatResult{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return StatResult{}, ErrObjectNotFound
		}
		return StatResult{}, fmt.Errorf("stat object: %w", err)
	}
	return StatResult{
		StorageURI: storageURI,
		Size:       info.Size,
		MIME:       info.ContentType,
	}, nil
}

// Delete implements ObjectStore.
func (s *MinIOObjectStore) Delete(ctx context.Context, storageURI string) error {
	objectKey, err := s.uriToKey(storageURI)
	if err != nil {
		return err
	}
	return s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{})
}

// Exists implements ObjectStore.
func (s *MinIOObjectStore) Exists(ctx context.Context, storageURI string) (bool, error) {
	objectKey, err := s.uriToKey(storageURI)
	if err != nil {
		return false, err
	}
	_, err = s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat object: %w", err)
	}
	return true, nil
}

// uriToKey validates and extracts the object key from a minio:// URI.
func (s *MinIOObjectStore) uriToKey(storageURI string) (string, error) {
	prefix := "minio://" + s.bucket + "/"
	if !strings.HasPrefix(storageURI, prefix) {
		return "", fmt.Errorf("unsupported storage uri %q (expected minio://%s/...)", storageURI, s.bucket)
	}
	key := strings.TrimPrefix(storageURI, prefix)
	if key == "" {
		return "", errors.New("empty object key in storage uri")
	}
	if strings.Contains(key, "..") {
		return "", errors.New("invalid object key: traversal not allowed")
	}
	return key, nil
}

// isNotFound reports whether a MinIO error indicates a missing object.
func isNotFound(err error) bool {
	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		return errResp.Code == "NoSuchKey" || errResp.Code == "NoSuchBucket"
	}
	return false
}
