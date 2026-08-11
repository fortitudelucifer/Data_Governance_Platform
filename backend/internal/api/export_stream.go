package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
)

// serveGeneratedArtifact resolves the streaming-vs-integrity tension for exports
// (#21). Streaming an export straight to the response is memory-safe but leaves no
// way to fail cleanly: a mid-stream error has already sent a 200 + a truncated /
// error-record-poisoned body, and a downstream tool can't tell.
//
// Instead we stream the generation into a **temp file** (still constant memory —
// disk, not RAM, so huge MOT/COCO exports don't OOM), compute its sha256, and only
// **after** it fully succeeds send the headers + stream the file back with an
// X-Content-SHA256 the client can verify. If generate() fails, **zero bytes** have
// been sent, so the caller returns a clean error status.
//
// generate writes the artifact to the provided io.Writer. Any error it returns is
// propagated with nothing sent to the client.
func serveGeneratedArtifact(c *gin.Context, filename, contentType string, generate func(io.Writer) error) error {
	tmp, err := os.CreateTemp("", "dg-export-*.tmp")
	if err != nil {
		return fmt.Errorf("export temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()

	h := sha256.New()
	if err := generate(io.MultiWriter(tmp, h)); err != nil {
		return err // nothing sent to the client yet — caller sends a clean error status
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("export flush: %w", err)
	}
	st, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("export stat: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("export rewind: %w", err)
	}

	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Length", strconv.FormatInt(st.Size(), 10))
	c.Header("X-Content-SHA256", hex.EncodeToString(h.Sum(nil)))
	c.Status(http.StatusOK)
	// From here bytes flow; the artifact is already complete + checksummed, so a
	// copy error is only a client-side disconnect, not a truncated deliverable.
	_, _ = io.Copy(c.Writer, tmp)
	return nil
}
