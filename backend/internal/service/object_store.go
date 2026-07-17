package service

import (
	"context"
	"errors"
	"io"
	"time"
)

// ObjectStore is the multi-modal binary storage abstraction (plan_v1/03 §6 +
// ADR-05). P0 ships a local filesystem driver; production will gain a MinIO
// driver behind the same interface, switchable via configuration.
//
// All methods are context-aware so callers can apply timeouts. Implementations
// must accept and return URIs of the form returned by their own Put — the
// concrete scheme (local:// vs s3://) is an implementation detail.
type ObjectStore interface {
	// Put stores the object content. Implementations must compute or accept
	// the canonical key (e.g. SHA256-addressable path). Returns the
	// implementation's storage URI suitable for persistence in
	// asset.storage_uri.
	Put(ctx context.Context, req PutRequest) (PutResult, error)

	// PutAt stores body at an explicit key (NOT content-addressed). Used for
	// derived artifacts under "derived/..." (waveform peaks, frame index,
	// thumbnails). size may be -1 if unknown. Returns the storage URI for key
	// (consumable by Get / Stat / PresignGetURL / Delete). See plan_v2 T0.3.
	PutAt(ctx context.Context, key string, body io.Reader, size int64, mime string) (PutResult, error)

	// Get returns a reader for the object identified by storageURI.
	Get(ctx context.Context, storageURI string) (io.ReadCloser, error)

	// Stat returns object metadata (size, content-type) without fetching the
	// body.
	Stat(ctx context.Context, storageURI string) (StatResult, error)

	// Delete removes the object. Idempotent: deleting a missing key returns
	// nil.
	Delete(ctx context.Context, storageURI string) error

	// Exists reports whether the object is present.
	Exists(ctx context.Context, storageURI string) (bool, error)

	// Driver returns the implementation identifier ("local" / "minio").
	Driver() string

	// PresignGetURL returns a time-limited direct-download URL for the object,
	// offloading bytes from the app process（视频 seek / 大文件下载靠对象存储原生
	// 的 Range 支持）。驱动不支持预签名时返回 ""，调用方应退回流式 + ServeContent。
	PresignGetURL(ctx context.Context, storageURI string, expiry time.Duration) (string, error)
}

// PutRequest carries the body and metadata required to store an object.
// SHA256 / Size may be zero — the driver will compute them — but supplying
// them lets a caller short-circuit duplicate uploads (see asset_service).
type PutRequest struct {
	DatasetID  uint
	OriginalName string
	MIME       string
	SHA256     string // hex
	Size       int64
	Body       io.Reader
}

// PutResult is the object metadata after Put completes.
type PutResult struct {
	StorageURI string
	SHA256     string
	Size       int64
	MIME       string
}

// StatResult is a subset of object metadata for non-body queries.
type StatResult struct {
	StorageURI string
	Size       int64
	MIME       string
}

// ErrObjectNotFound is returned by Get / Stat when the URI does not exist.
var ErrObjectNotFound = errors.New("object not found")
