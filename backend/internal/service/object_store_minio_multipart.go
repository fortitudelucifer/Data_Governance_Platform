package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
)

// object_store_minio_multipart.go — S3 multipart upload primitives (plan_v2
// T0.2). Only the MinIO driver implements MultipartObjectStore; the local
// driver returns false on the capability assertion and callers fall back to the
// simple upload path (dev / small files).

// MultipartPart identifies one uploaded part for the complete step.
type MultipartPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// MultipartObjectStore is the optional capability for direct-to-store chunked
// uploads with presigned part URLs. Browsers PUT parts straight to the object
// store; the app only handles the control plane (init / complete / abort) and
// the post-complete finalization (stream hash + server-side copy).
type MultipartObjectStore interface {
	// InitMultipart starts a multipart upload at key, returning the upload id.
	InitMultipart(ctx context.Context, key, mime string) (string, error)
	// PresignPart returns a presigned PUT URL the browser uploads one part to.
	PresignPart(ctx context.Context, key, uploadID string, partNumber int, expiry time.Duration) (string, error)
	// CompleteMultipart assembles the parts into the final object at key.
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) error
	// AbortMultipart cancels an in-progress upload and frees its parts.
	AbortMultipart(ctx context.Context, key, uploadID string) error
	// ListParts returns the already-uploaded parts (resume support).
	ListParts(ctx context.Context, key, uploadID string) ([]MultipartPart, error)
	// StreamSHA256 streams the object to compute its sha256 + size and returns
	// the first <=512 bytes (for MIME sniffing) — constant memory, no full
	// buffering in the app process.
	StreamSHA256(ctx context.Context, key string) (sha string, size int64, head []byte, err error)
	// CopyTo server-side copies srcKey to dstKey (no app-process transfer) and
	// returns the destination storage URI.
	CopyTo(ctx context.Context, srcKey, dstKey string) (string, error)
	// KeyForContent returns the canonical content-addressed key for an asset.
	KeyForContent(datasetID uint, sha string) string
	// DeleteKey removes an object by raw key (temp cleanup).
	DeleteKey(ctx context.Context, key string) error
}

func (s *MinIOObjectStore) core() minio.Core { return minio.Core{Client: s.client} }

// InitMultipart implements MultipartObjectStore.
func (s *MinIOObjectStore) InitMultipart(ctx context.Context, key, mime string) (string, error) {
	opts := minio.PutObjectOptions{}
	if mime != "" {
		opts.ContentType = mime
	}
	return s.core().NewMultipartUpload(ctx, s.bucket, key, opts)
}

// PresignPart implements MultipartObjectStore.
func (s *MinIOObjectStore) PresignPart(ctx context.Context, key, uploadID string, partNumber int, expiry time.Duration) (string, error) {
	reqParams := url.Values{}
	reqParams.Set("uploadId", uploadID)
	reqParams.Set("partNumber", strconv.Itoa(partNumber))
	u, err := s.client.Presign(ctx, http.MethodPut, s.bucket, key, expiry, reqParams)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// CompleteMultipart implements MultipartObjectStore.
func (s *MinIOObjectStore) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) error {
	cps := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		cps[i] = minio.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	_, err := s.core().CompleteMultipartUpload(ctx, s.bucket, key, uploadID, cps, minio.PutObjectOptions{})
	return err
}

// AbortMultipart implements MultipartObjectStore.
func (s *MinIOObjectStore) AbortMultipart(ctx context.Context, key, uploadID string) error {
	return s.core().AbortMultipartUpload(ctx, s.bucket, key, uploadID)
}

// ListParts implements MultipartObjectStore.
func (s *MinIOObjectStore) ListParts(ctx context.Context, key, uploadID string) ([]MultipartPart, error) {
	res, err := s.core().ListObjectParts(ctx, s.bucket, key, uploadID, 0, 10000)
	if err != nil {
		return nil, err
	}
	parts := make([]MultipartPart, 0, len(res.ObjectParts))
	for _, p := range res.ObjectParts {
		parts = append(parts, MultipartPart{PartNumber: p.PartNumber, ETag: p.ETag})
	}
	return parts, nil
}

// StreamSHA256 implements MultipartObjectStore.
func (s *MinIOObjectStore) StreamSHA256(ctx context.Context, key string) (string, int64, []byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", 0, nil, err
	}
	defer obj.Close()
	h := sha256.New()
	head := make([]byte, 0, 512)
	buf := make([]byte, 64*1024)
	var size int64
	for {
		n, rerr := obj.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			size += int64(n)
			if len(head) < 512 {
				need := 512 - len(head)
				if need > n {
					need = n
				}
				head = append(head, buf[:need]...)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, nil, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, head, nil
}

// CopyTo implements MultipartObjectStore.
func (s *MinIOObjectStore) CopyTo(ctx context.Context, srcKey, dstKey string) (string, error) {
	if _, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: s.bucket, Object: srcKey}); err != nil {
		return "", err
	}
	return fmt.Sprintf("minio://%s/%s", s.bucket, dstKey), nil
}

// KeyForContent implements MultipartObjectStore.
func (s *MinIOObjectStore) KeyForContent(datasetID uint, sha string) string {
	return fmt.Sprintf("%d/%s/%s", datasetID, sha[:2], sha)
}

// DeleteKey implements MultipartObjectStore.
func (s *MinIOObjectStore) DeleteKey(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
