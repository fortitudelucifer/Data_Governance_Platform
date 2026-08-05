package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// helper: encode an opaque RGBA image of size w x h as PNG.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// helper: encode a JPEG with synthetic APP1 EXIF segment so we can verify the
// stripper actually removes it.
func encodeJPEGWithEXIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: 50, G: 100, B: 150, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	raw := buf.Bytes()

	// Inject an APP1 EXIF segment right after SOI (0xFFD8).
	// Segment layout: 0xFF 0xE1 LEN_HI LEN_LO "Exif\0\0" + 8 bytes payload.
	exifPayload := []byte("Exif\x00\x00ABCDEFGH")
	segLen := 2 + len(exifPayload) // length field includes itself
	app1 := []byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen & 0xFF)}
	app1 = append(app1, exifPayload...)

	out := make([]byte, 0, len(raw)+len(app1))
	out = append(out, raw[:2]...) // SOI
	out = append(out, app1...)
	out = append(out, raw[2:]...)
	return out
}

func TestQCService_PassesValidPNG(t *testing.T) {
	body := encodePNG(t, 32, 24)
	q := NewQCService(QCConfig{})
	report, clean, err := q.Inspect(bytes.NewReader(body), "image/png")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcPassed {
		t.Fatalf("status = %s, want passed; reasons=%v", report.Status, report.Reasons)
	}
	if report.MIME != "image/png" || report.Format != "png" {
		t.Fatalf("mime/format = %q/%q", report.MIME, report.Format)
	}
	if report.Width != 32 || report.Height != 24 {
		t.Fatalf("dims = %dx%d", report.Width, report.Height)
	}
	if report.IsLongImage {
		t.Fatalf("32x24 should not be a long image")
	}
	if len(clean) == 0 {
		t.Fatalf("clean body empty")
	}
	if report.SHA256 == "" {
		t.Fatalf("missing sha256")
	}
}

func TestQCService_RejectsUnknownMagic(t *testing.T) {
	body := []byte("not-an-image-at-all")
	q := NewQCService(QCConfig{})
	report, _, err := q.Inspect(bytes.NewReader(body), "image/png")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcFailed {
		t.Fatalf("status = %s, want failed", report.Status)
	}
	joined := strings.Join(report.Reasons, "|")
	if !strings.Contains(joined, "magic bytes") {
		t.Fatalf("reasons should mention magic bytes; got %v", report.Reasons)
	}
}

func TestQCService_RejectsOversizedPixels(t *testing.T) {
	// 100x100 image but configure a tiny pixel cap.
	body := encodePNG(t, 100, 100)
	q := NewQCService(QCConfig{
		MaxFileSizeBytes: 50 << 20,
		MaxPixelCount:    1000,
		LongImageRatio:   8,
		AcceptedMIME:     []string{"image/png"},
	})
	report, _, err := q.Inspect(bytes.NewReader(body), "image/png")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcFailed {
		t.Fatalf("status = %s, want failed", report.Status)
	}
	joined := strings.Join(report.Reasons, "|")
	if !strings.Contains(joined, "pixel count") {
		t.Fatalf("reasons should mention pixel count; got %v", report.Reasons)
	}
}

func TestQCService_RejectsOversizedFile(t *testing.T) {
	// Build a payload bigger than the file-size cap. We don't need a real
	// image — magic bytes will fail too, but the size check fires first by
	// being recorded in Reasons.
	bigPayload := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF}, 10) // jpeg magic + filler
	bigPayload = append(bigPayload, bytes.Repeat([]byte{0x00}, 200)...)
	q := NewQCService(QCConfig{
		MaxFileSizeBytes: 50,
		MaxPixelCount:    1_000_000,
		LongImageRatio:   8,
		AcceptedMIME:     []string{"image/jpeg"},
	})
	report, _, err := q.Inspect(bytes.NewReader(bigPayload), "image/jpeg")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcFailed {
		t.Fatalf("status = %s, want failed", report.Status)
	}
	joined := strings.Join(report.Reasons, "|")
	if !strings.Contains(joined, "file size") {
		t.Fatalf("reasons should mention file size; got %v", report.Reasons)
	}
}

func TestQCService_DetectsLongImage(t *testing.T) {
	// 800x80 → 10:1 → long image at default ratio 8.
	body := encodePNG(t, 800, 80)
	q := NewQCService(QCConfig{})
	report, _, err := q.Inspect(bytes.NewReader(body), "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcPassed {
		t.Fatalf("status = %s, want passed", report.Status)
	}
	if !report.IsLongImage {
		t.Fatalf("expected IsLongImage=true for 800x80")
	}
}

func TestQCService_RejectsUnacceptedMIME(t *testing.T) {
	// Encode a PNG but configure only JPEG as accepted.
	body := encodePNG(t, 16, 16)
	q := NewQCService(QCConfig{
		MaxFileSizeBytes: 1 << 20,
		MaxPixelCount:    1_000_000,
		LongImageRatio:   8,
		AcceptedMIME:     []string{"image/jpeg"},
	})
	report, _, err := q.Inspect(bytes.NewReader(body), "image/png")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcFailed {
		t.Fatalf("status = %s, want failed", report.Status)
	}
	joined := strings.Join(report.Reasons, "|")
	if !strings.Contains(joined, "not accepted") {
		t.Fatalf("reasons should mention mime not accepted; got %v", report.Reasons)
	}
}

func TestQCService_StripsJPEGEXIF(t *testing.T) {
	dirty := encodeJPEGWithEXIF(t)
	if !bytes.Contains(dirty, []byte("Exif\x00\x00")) {
		t.Fatalf("setup error: dirty jpeg lacks exif segment")
	}
	q := NewQCService(QCConfig{})
	report, clean, err := q.Inspect(bytes.NewReader(dirty), "image/jpeg")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Status != qcPassed {
		t.Fatalf("status = %s, reasons=%v", report.Status, report.Reasons)
	}
	if bytes.Contains(clean, []byte("Exif\x00\x00")) {
		t.Fatalf("clean body still contains EXIF segment")
	}
	// Sanity: stripped body must remain a valid JPEG (starts with SOI).
	if len(clean) < 4 || clean[0] != 0xFF || clean[1] != 0xD8 {
		t.Fatalf("stripped body is not a JPEG")
	}
	if int64(len(clean)) >= int64(len(dirty)) {
		t.Fatalf("stripped body should be smaller than dirty: clean=%d dirty=%d", len(clean), len(dirty))
	}
}

func TestQCService_DefaultsApplied(t *testing.T) {
	q := NewQCService(QCConfig{}) // empty triggers defaults
	if q.cfg.MaxFileSizeBytes <= 0 || q.cfg.MaxPixelCount <= 0 {
		t.Fatalf("defaults not applied: %+v", q.cfg)
	}
	if q.cfg.LongImageRatio <= 1 {
		t.Fatalf("long image ratio default not applied: %v", q.cfg.LongImageRatio)
	}
	if len(q.cfg.AcceptedMIME) == 0 {
		t.Fatalf("accepted mime default not applied")
	}
}
