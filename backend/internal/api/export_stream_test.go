package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// #21 生成成功:整包送出 + X-Content-SHA256 与内容一致。
func TestServeGeneratedArtifact_SuccessCarriesChecksum(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := "hello ndjson\n{\"a\":1}\n"
	err := serveGeneratedArtifact(c, "x.jsonl", "application/x-ndjson", func(w io.Writer) error {
		_, e := io.WriteString(w, body)
		return e
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != body {
		t.Fatalf("body mismatch:\n got %q\nwant %q", rec.Body.String(), body)
	}
	sum := sha256.Sum256([]byte(body))
	if got := rec.Header().Get("X-Content-SHA256"); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("checksum header = %q, want %q", got, hex.EncodeToString(sum[:]))
	}
}

// #21 生成中途失败:**零字节**送出 + 返回错误(调用方据此发干净的错误状态,而不是
// 200 + 截断/被污染的包)。
func TestServeGeneratedArtifact_FailureSendsNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	want := errors.New("stream blew up mid-way")
	err := serveGeneratedArtifact(c, "x.zip", "application/zip", func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial bytes before failure") // 写进临时文件,不该到客户端
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("#21 回归:生成失败时不该向客户端送出任何字节,实际送了 %d 字节", rec.Body.Len())
	}
	if rec.Header().Get("X-Content-SHA256") != "" {
		t.Fatalf("#21 回归:失败时不该发 checksum 头")
	}
}
