package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
)

// QCConfig captures the upload-side QC limits. Defaults follow plan_v1/02
// §2.1 and 04 §1.3 (隐私三件 / 大小防御).
type QCConfig struct {
	MaxFileSizeBytes int64
	MaxPixelCount    int64
	LongImageRatio   float64 // height / width (or width / height) threshold
	AcceptedMIME     []string
}

// DefaultQCConfig returns the P0 defaults.
func DefaultQCConfig() QCConfig {
	return QCConfig{
		MaxFileSizeBytes: 200 << 20,  // 200 MiB（放宽以容纳音视频文件）
		MaxPixelCount:    50_000_000, // 50M pixels (~7000x7000)，仅图片校验
		LongImageRatio:   8.0,        // > 8:1 considered long image
		AcceptedMIME: []string{
			// 图片
			"image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp",
			// 音频
			"audio/mpeg", "audio/wav", "audio/x-wav", "audio/ogg", "audio/flac", "audio/mp4",
			// 视频
			"video/mp4", "video/webm", "video/quicktime", "video/x-matroska",
		},
	}
}

// QCReport is the structured output of QCService.Inspect, persisted as JSON in
// asset.qc_report.
type QCReport struct {
	Status      string                 `json:"status"`
	MIME        string                 `json:"mime"`
	Format      string                 `json:"format"`
	Width       int                    `json:"width"`
	Height      int                    `json:"height"`
	SizeBytes   int64                  `json:"size_bytes"`
	SHA256      string                 `json:"sha256"`
	IsLongImage bool                   `json:"is_long_image"`
	Reasons     []string               `json:"reasons,omitempty"`
	Features    map[string]interface{} `json:"features,omitempty"`
}

// QCService runs the upload-time quality control pipeline. It is intentionally
// dependency-free so it can be unit-tested without disk / DB.
type QCService struct {
	cfg QCConfig
}

// NewQCService creates a QC service. Pass an empty QCConfig{} to use defaults.
func NewQCService(cfg QCConfig) *QCService {
	if cfg.MaxFileSizeBytes <= 0 || cfg.MaxPixelCount <= 0 {
		cfg = DefaultQCConfig()
	}
	if cfg.LongImageRatio <= 1.0 {
		cfg.LongImageRatio = DefaultQCConfig().LongImageRatio
	}
	if len(cfg.AcceptedMIME) == 0 {
		cfg.AcceptedMIME = DefaultQCConfig().AcceptedMIME
	}
	return &QCService{cfg: cfg}
}

// Inspect runs the QC pipeline on an image upload. It returns:
//   - report: the QC verdict (always non-nil)
//   - cleanBytes: the body with EXIF stripped where applicable (JPEG)
//   - err: only set on infrastructure failures (read errors, etc.); validation
//     failures are encoded inside report.Status / report.Reasons so the caller
//     can persist a QC_FAILED Asset row instead of dropping the upload.
func (q *QCService) Inspect(body io.Reader, declaredMIME string) (*QCReport, []byte, error) {
	// Hard limit body size to MaxFileSizeBytes + 1 to differentiate truncation
	// from honest reads.
	limit := q.cfg.MaxFileSizeBytes
	limited := io.LimitReader(body, limit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("read upload body: %w", err)
	}

	report := &QCReport{Status: qcPassed}
	report.SizeBytes = int64(len(raw))

	if int64(len(raw)) > limit {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, fmt.Sprintf("file size %d exceeds limit %d", len(raw), limit))
		// Continue running other checks but cap body to avoid huge work.
		raw = raw[:limit]
		report.SizeBytes = int64(len(raw))
	}

	// magic bytes：先按文件签名识别，识别不出再回退到客户端声明的 MIME（音视频
	// 容器格式多，签名覆盖不全时用 declaredMIME 兜底）。
	mime, format, kind := sniffMedia(raw, declaredMIME)
	if mime == "" {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, "unknown or unsupported file type")
		report.SHA256 = sha256Hex(raw)
		return report, raw, nil
	}
	report.MIME = mime
	report.Format = format

	if !mimeAccepted(q.cfg.AcceptedMIME, mime) {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, fmt.Sprintf("mime %q not accepted", mime))
	}

	// 音视频：不做图片解码（无宽高/EXIF），仅记录 MIME/大小/SHA256 后入库。
	if kind != "image" {
		report.SHA256 = sha256Hex(raw)
		report.Features = map[string]interface{}{"kind": kind}
		return report, raw, nil
	}

	// TD-1：声明为 image/* 但文件签名不匹配任何已知图片格式（来自 sniffMedia 的
	// declaredMIME 兜底）→ 以 "magic bytes" 理由拒绝，而非走 decode 报 "image decode failed"。
	// 理由更准确，且避免 decode 失败掩盖其它问题。
	if _, _, ok := sniffImage(raw); !ok {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, fmt.Sprintf("magic bytes do not match a known image signature (declared %q)", declaredMIME))
		report.SHA256 = sha256Hex(raw)
		return report, raw, nil
	}

	// 以下为图片专属 QC（宽高 / 长图 / 像素上限 / EXIF 去除）。
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, fmt.Sprintf("image decode failed: %v", err))
		report.SHA256 = sha256Hex(raw)
		return report, raw, nil
	}
	report.Width = cfg.Width
	report.Height = cfg.Height
	if cfg.Width <= 0 || cfg.Height <= 0 {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, "image has zero dimension")
	}

	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels > q.cfg.MaxPixelCount {
		report.Status = qcFailed
		report.Reasons = append(report.Reasons, fmt.Sprintf("pixel count %d exceeds limit %d", pixels, q.cfg.MaxPixelCount))
	}

	if cfg.Width > 0 && cfg.Height > 0 {
		long := false
		if cfg.Width >= cfg.Height {
			if float64(cfg.Width)/float64(cfg.Height) >= q.cfg.LongImageRatio {
				long = true
			}
		} else {
			if float64(cfg.Height)/float64(cfg.Width) >= q.cfg.LongImageRatio {
				long = true
			}
		}
		report.IsLongImage = long
	}

	clean := raw
	if mime == "image/jpeg" {
		stripped, err := stripJPEGExif(raw)
		if err == nil {
			clean = stripped
			report.SizeBytes = int64(len(clean))
		} else {
			// Strip failure is not fatal; record and continue with original.
			report.Reasons = append(report.Reasons, fmt.Sprintf("exif strip skipped: %v", err))
		}
	}

	report.SHA256 = sha256Hex(clean)
	report.Features = map[string]interface{}{
		"width":         cfg.Width,
		"height":        cfg.Height,
		"aspect_ratio":  ratio(cfg.Width, cfg.Height),
		"is_long_image": report.IsLongImage,
	}
	return report, clean, nil
}

// ---------- helpers ----------

const (
	qcPassed = "passed"
	qcFailed = "failed"
)

func mimeAccepted(list []string, mime string) bool {
	for _, m := range list {
		if strings.EqualFold(m, mime) {
			return true
		}
	}
	return false
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func ratio(w, h int) float64 {
	if h == 0 {
		return 0
	}
	return float64(w) / float64(h)
}

// sniffImage returns (mime, format, ok). It checks the first bytes of raw for
// supported image signatures.
func sniffImage(raw []byte) (string, string, bool) {
	switch {
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "image/jpeg", "jpeg", true
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png", "png", true
	case len(raw) >= 6 && (bytes.Equal(raw[:6], []byte("GIF87a")) || bytes.Equal(raw[:6], []byte("GIF89a"))):
		return "image/gif", "gif", true
	case len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "image/webp", "webp", true
	case len(raw) >= 2 && raw[0] == 'B' && raw[1] == 'M':
		return "image/bmp", "bmp", true
	}
	return "", "", false
}

// sniffMedia 识别图片 / 音频 / 视频，返回 (mime, format, kind)。
// kind ∈ {"image","audio","video"}。优先用文件签名（精确），签名覆盖不到的
// 音视频容器再回退到客户端声明的 declaredMIME。mime 为空表示无法识别。
func sniffMedia(raw []byte, declaredMIME string) (mime, format, kind string) {
	// 图片签名最精确，先处理（也优先于 RIFF/WAVE，避免 webp 与 wav 混淆）。
	if m, f, ok := sniffImage(raw); ok {
		return m, f, "image"
	}
	switch {
	case len(raw) >= 12 && bytes.Equal(raw[4:8], []byte("ftyp")): // MP4 / MOV 容器
		if strings.HasPrefix(declaredMIME, "audio/") {
			return "audio/mp4", "mp4", "audio"
		}
		return "video/mp4", "mp4", "video"
	case len(raw) >= 4 && raw[0] == 0x1A && raw[1] == 0x45 && raw[2] == 0xDF && raw[3] == 0xA3:
		return "video/webm", "webm", "video" // EBML (webm / mkv)
	case len(raw) >= 3 && bytes.Equal(raw[:3], []byte("ID3")):
		return "audio/mpeg", "mp3", "audio"
	case len(raw) >= 2 && raw[0] == 0xFF && (raw[1]&0xE0) == 0xE0:
		return "audio/mpeg", "mp3", "audio" // MP3 帧同步
	case len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WAVE")):
		return "audio/wav", "wav", "audio"
	case len(raw) >= 4 && bytes.Equal(raw[:4], []byte("OggS")):
		return "audio/ogg", "ogg", "audio"
	case len(raw) >= 4 && bytes.Equal(raw[:4], []byte("fLaC")):
		return "audio/flac", "flac", "audio"
	}
	// 签名识别失败：用客户端声明的 MIME 兜底（仅音视频/图片大类）。
	switch {
	case strings.HasPrefix(declaredMIME, "audio/"):
		return declaredMIME, "", "audio"
	case strings.HasPrefix(declaredMIME, "video/"):
		return declaredMIME, "", "video"
	case strings.HasPrefix(declaredMIME, "image/"):
		return declaredMIME, "", "image"
	}
	return "", "", ""
}

// stripJPEGExif removes APP1 (EXIF) and APP2 (often ICC / EXIF MakerNote)
// segments from a JPEG byte stream. It rewrites a clean JPEG without modifying
// pixels. Returns an error if the input is not a parseable JPEG.
func stripJPEGExif(raw []byte) ([]byte, error) {
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return nil, errors.New("not a jpeg")
	}
	out := make([]byte, 0, len(raw))
	out = append(out, 0xFF, 0xD8) // SOI
	i := 2
	for i < len(raw) {
		// Find next marker.
		if raw[i] != 0xFF {
			return nil, fmt.Errorf("malformed jpeg at offset %d", i)
		}
		// Skip fill bytes 0xFF.
		for i < len(raw) && raw[i] == 0xFF {
			i++
		}
		if i >= len(raw) {
			return nil, errors.New("truncated jpeg")
		}
		marker := raw[i]
		i++
		// Standalone markers (no length / payload): SOI/EOI/RSTn/TEM
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			if marker == 0xD9 {
				out = append(out, 0xFF, 0xD9)
				return out, nil
			}
			out = append(out, 0xFF, marker)
			continue
		}
		if i+2 > len(raw) {
			return nil, errors.New("truncated segment length")
		}
		segLen := int(raw[i])<<8 | int(raw[i+1])
		if segLen < 2 || i+segLen > len(raw) {
			return nil, errors.New("invalid segment length")
		}
		segStart := i
		segEnd := i + segLen

		// Drop EXIF (APP1 with "Exif\0\0") and APP2 ICC/EXIF makernote.
		drop := false
		if marker == 0xE1 {
			payload := raw[segStart+2 : segEnd]
			if len(payload) >= 6 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
				drop = true
			}
		}
		if !drop {
			out = append(out, 0xFF, marker)
			out = append(out, raw[segStart:segEnd]...)
		}
		i = segEnd

		// On SOS (0xDA), the rest is entropy-coded data; copy until EOI.
		if marker == 0xDA {
			out = append(out, raw[i:]...)
			return out, nil
		}
	}
	return out, nil
}
