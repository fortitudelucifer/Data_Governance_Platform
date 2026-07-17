package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// Integration test — requires a live MinIO instance.
// Run with:
//
//	MM_MINIO_ENDPOINT=localhost:9000 go test -run TestMinIOObjectStore -v ./internal/service/...
//
// Skipped automatically when MM_MINIO_ENDPOINT is unset (CI-safe).
func newTestMinIOStore(t *testing.T) *MinIOObjectStore {
	t.Helper()
	endpoint := os.Getenv("MM_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("MM_MINIO_ENDPOINT not set; skipping MinIO integration tests")
	}
	accessKey := os.Getenv("MM_MINIO_ACCESS_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}
	secretKey := os.Getenv("MM_MINIO_SECRET_KEY")
	if secretKey == "" {
		secretKey = "minioadmin"
	}
	// S3/MinIO bucket names: lowercase, hyphens only, 3-63 chars.
	bucket := strings.ToLower(t.Name())
	for _, r := range []string{"/", "_", " ", "."} {
		bucket = strings.ReplaceAll(bucket, r, "-")
	}
	bucket = "ti-" + bucket
	if len(bucket) > 63 {
		bucket = bucket[:63]
	}

	ctx := context.Background()
	store, err := NewMinIOObjectStore(ctx, MinIOConfig{
		Endpoint:  endpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("NewMinIOObjectStore: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: delete the test bucket after the test.
		_ = store.client.RemoveBucket(context.Background(), bucket)
	})
	return store
}

func TestMinIOObjectStore_PutGetStat(t *testing.T) {
	store := newTestMinIOStore(t)
	ctx := context.Background()

	body := []byte("hello, minio world")
	h := sha256.Sum256(body)
	wantDigest := hex.EncodeToString(h[:])

	res, err := store.Put(ctx, PutRequest{
		Body:      bytes.NewReader(body),
		DatasetID: 1,
		MIME:      "image/jpeg",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if res.SHA256 != wantDigest {
		t.Errorf("SHA256: got %s, want %s", res.SHA256, wantDigest)
	}
	if res.Size != int64(len(body)) {
		t.Errorf("Size: got %d, want %d", res.Size, len(body))
	}
	if !strings.HasPrefix(res.StorageURI, "minio://") {
		t.Errorf("StorageURI: unexpected prefix %q", res.StorageURI)
	}

	// Get
	rc, err := store.Get(ctx, res.StorageURI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("Get body mismatch: got %q, want %q", got, body)
	}

	// Stat
	stat, err := store.Stat(ctx, res.StorageURI)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != int64(len(body)) {
		t.Errorf("Stat.Size: got %d, want %d", stat.Size, len(body))
	}
}

func TestMinIOObjectStore_Exists(t *testing.T) {
	store := newTestMinIOStore(t)
	ctx := context.Background()

	body := []byte("exists-test-payload")
	res, err := store.Put(ctx, PutRequest{Body: bytes.NewReader(body), DatasetID: 2})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	ok, err := store.Exists(ctx, res.StorageURI)
	if err != nil || !ok {
		t.Errorf("Exists after Put: got (%v, %v), want (true, nil)", ok, err)
	}
}

func TestMinIOObjectStore_Delete(t *testing.T) {
	store := newTestMinIOStore(t)
	ctx := context.Background()

	body := []byte("delete-test-payload")
	res, err := store.Put(ctx, PutRequest{Body: bytes.NewReader(body), DatasetID: 3})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.Delete(ctx, res.StorageURI); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Get(ctx, res.StorageURI)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrObjectNotFound", err)
	}
}

func TestMinIOObjectStore_Deduplication(t *testing.T) {
	store := newTestMinIOStore(t)
	ctx := context.Background()

	body := []byte("dedup-payload")
	r1, err := store.Put(ctx, PutRequest{Body: bytes.NewReader(body), DatasetID: 4})
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	r2, err := store.Put(ctx, PutRequest{Body: bytes.NewReader(body), DatasetID: 4})
	if err != nil {
		t.Fatalf("Put 2 (dedup): %v", err)
	}
	if r1.StorageURI != r2.StorageURI {
		t.Errorf("Dedup: URIs differ: %q vs %q", r1.StorageURI, r2.StorageURI)
	}
}

func TestMinIOObjectStore_SHA256Mismatch(t *testing.T) {
	store := newTestMinIOStore(t)
	ctx := context.Background()

	_, err := store.Put(ctx, PutRequest{
		Body:      bytes.NewReader([]byte("content")),
		DatasetID: 5,
		SHA256:    "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected sha256 mismatch error, got %v", err)
	}
}

func TestMinIOObjectStore_GetNotFound(t *testing.T) {
	store := newTestMinIOStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "minio://"+store.bucket+"/99/ab/abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Get non-existent: got %v, want ErrObjectNotFound", err)
	}
}

func TestMinIOObjectStore_Driver(t *testing.T) {
	store := newTestMinIOStore(t)
	if store.Driver() != "minio" {
		t.Errorf("Driver: got %q, want %q", store.Driver(), "minio")
	}
}
