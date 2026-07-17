package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// helper: build a fresh LocalObjectStore under t.TempDir().
func newTestLocalStore(t *testing.T) *LocalObjectStore {
	t.Helper()
	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalObjectStore: %v", err)
	}
	return store
}

func TestLocalObjectStore_PutGetStat(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()

	body := []byte("hello, multimodal world")
	want := sha256.Sum256(body)
	wantHex := hex.EncodeToString(want[:])

	res, err := store.Put(ctx, PutRequest{
		DatasetID:    7,
		OriginalName: "hello.bin",
		MIME:         "application/octet-stream",
		Body:         bytes.NewReader(body),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if res.SHA256 != wantHex {
		t.Fatalf("sha256 mismatch: got %s want %s", res.SHA256, wantHex)
	}
	if res.Size != int64(len(body)) {
		t.Fatalf("size mismatch: got %d want %d", res.Size, len(body))
	}
	wantURI := "local://7/" + wantHex[:2] + "/" + wantHex
	if res.StorageURI != wantURI {
		t.Fatalf("uri mismatch: got %s want %s", res.StorageURI, wantURI)
	}

	// Stat
	st, err := store.Stat(ctx, res.StorageURI)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size != int64(len(body)) {
		t.Fatalf("Stat size mismatch: got %d want %d", st.Size, len(body))
	}

	// Get returns identical bytes
	rc, err := store.Get(ctx, res.StorageURI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}

	// Exists
	exists, err := store.Exists(ctx, res.StorageURI)
	if err != nil || !exists {
		t.Fatalf("Exists: %v %v", exists, err)
	}

	// AbsPath should resolve under root
	abs, err := store.AbsPath(res.StorageURI)
	if err != nil {
		t.Fatalf("AbsPath: %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(abs), filepath.Clean(store.root)) {
		t.Fatalf("AbsPath not under root: %s vs %s", abs, store.root)
	}
}

func TestLocalObjectStore_PutDeduplicates(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()
	body := []byte("dedup-me")

	first, err := store.Put(ctx, PutRequest{DatasetID: 1, Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second, err := store.Put(ctx, PutRequest{DatasetID: 1, Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if first.StorageURI != second.StorageURI {
		t.Fatalf("dedup failed: uris differ %s vs %s", first.StorageURI, second.StorageURI)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("dedup failed: sha differ")
	}
}

func TestLocalObjectStore_PutDeclaredSHAMismatch(t *testing.T) {
	store := newTestLocalStore(t)
	body := []byte("payload")
	_, err := store.Put(context.Background(), PutRequest{
		DatasetID: 2,
		SHA256:    "deadbeef", // declared mismatch
		Body:      bytes.NewReader(body),
	})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch error, got %v", err)
	}
}

func TestLocalObjectStore_PutRequiresDatasetAndBody(t *testing.T) {
	store := newTestLocalStore(t)
	if _, err := store.Put(context.Background(), PutRequest{Body: bytes.NewReader([]byte("x"))}); err == nil {
		t.Fatalf("expected error for missing dataset id")
	}
	if _, err := store.Put(context.Background(), PutRequest{DatasetID: 1, Body: nil}); err == nil {
		t.Fatalf("expected error for nil body")
	}
}

func TestLocalObjectStore_GetNotFound(t *testing.T) {
	store := newTestLocalStore(t)
	missing := "local://1/aa/" + strings.Repeat("a", 64)
	_, err := store.Get(context.Background(), missing)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("expected ErrObjectNotFound, got %v", err)
	}
	_, err = store.Stat(context.Background(), missing)
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Stat expected ErrObjectNotFound, got %v", err)
	}
	exists, err := store.Exists(context.Background(), missing)
	if err != nil || exists {
		t.Fatalf("Exists: %v %v", exists, err)
	}
}

func TestLocalObjectStore_DeleteIsIdempotent(t *testing.T) {
	store := newTestLocalStore(t)
	ctx := context.Background()
	res, err := store.Put(ctx, PutRequest{DatasetID: 3, Body: bytes.NewReader([]byte("byebye"))})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Delete(ctx, res.StorageURI); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	// Second delete should not error.
	if err := store.Delete(ctx, res.StorageURI); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
	exists, _ := store.Exists(ctx, res.StorageURI)
	if exists {
		t.Fatalf("object still exists after delete")
	}
}

func TestLocalObjectStore_RejectsBadURIs(t *testing.T) {
	store := newTestLocalStore(t)
	cases := []string{
		"s3://bucket/key",      // wrong scheme
		"local://",             // empty path
		"local://1/../escape",  // traversal
	}
	for _, uri := range cases {
		if _, err := store.Get(context.Background(), uri); err == nil {
			t.Errorf("expected error for uri %q", uri)
		}
	}
}

func TestLocalObjectStore_Driver(t *testing.T) {
	store := newTestLocalStore(t)
	if store.Driver() != "local" {
		t.Fatalf("driver = %q, want local", store.Driver())
	}
}
