package service

import (
	"bytes"
	"compress/gzip"
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
			// 图片。⚠️ #7 不含 webp/bmp:仓库没有注册它们的 decoder,image.DecodeConfig
			// 对二者恒报 unknown → 完整 QC 必失败。既然解不了就别放进白名单假装支持
			// (原来放了 → magic 嗅探接受、随后 decode 失败;>16MiB 走 multipart 还会被
			// 标 passed,不一致)。要支持得先引入并注册 x/image 的 webp/bmp decoder + 同步 UI。
			"image/jpeg", "image/png", "image/gif",
			// 音频
			"audio/mpeg", "audio/wav", "audio/x-wav", "audio/ogg", "audio/flac", "audio/mp4",
			// 视频
			"video/mp4", "video/webm", "video/quicktime", "video/x-matroska",
			// 医学体数据(Phase C):NIfTI(.nii / .nii.gz)
			"application/x-nifti",
			// 病理全切片(Phase C1):金字塔 TIFF(.svs / .ndpi / .tif)
			"application/x-wsi",
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

	// #6 DecodeConfig 只读文件头(PNG 的 IHDR / JPEG 的 SOF),**证明不了图片完整可解码**:
	// 合法头 + 缺 IDAT/IEND 的 PNG、只有 SOF 的截断 JPEG 都能读出宽高、标 passed,但浏览器
	// 和 AI 都解不开——一张"通过 QC 却打不开"的图,是最典型的静默错。像素数上面已卡过上限
	// (= 限住了这次完整解码的资源),这里做一次**完整解码**验证结构,解不出即 fail-closed。
	if report.Status == qcPassed {
		if _, _, derr := image.Decode(bytes.NewReader(raw)); derr != nil {
			report.Status = qcFailed
			report.Reasons = append(report.Reasons, fmt.Sprintf("image full decode failed (截断/损坏,仅有文件头): %v", derr))
		}
	}

	clean := raw
	if mime == "image/jpeg" {
		stripped, err := stripJPEGExif(raw)
		if err != nil {
			// #8 strip 失败 **fail-closed**。旧代码"记一笔、继续用原图"——含 EXIF/GPS/XMP/
			// IPTC 的 PHI 就随原图 passed 出去了,"净化失败却仍通过"正是最危险的静默错。
			// 宁可拒绝让上传方知道,也不放行一张没洗干净的图。
			report.Status = qcFailed
			report.Reasons = append(report.Reasons, fmt.Sprintf("JPEG 元数据净化失败(拒绝而非放行含 EXIF/XMP/IPTC 的原图): %v", err))
		} else {
			clean = stripped
			report.SizeBytes = int64(len(clean))
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
	}
	// #7 webp/bmp 有意不识别:没有 decoder,识别了也只会在完整 QC 时 decode 失败。
	// 不识别 → sniffMedia 走 declaredMIME,而白名单已移除二者 → 干净地拒("不支持的类型")。
	return "", "", false
}

// sniffNIfTI 识别 NIfTI-1 体数据(.nii 与 gzip 压缩的 .nii.gz),返回 (format, ok)。
// NIfTI 的魔数("n+1\0" / "ni1\0")在**偏移 344**,不在文件头——这是它和其它格式
// 最不一样的地方,也是"按文件头嗅探"会漏掉它的原因。
// .nii.gz 需要先解开头部若干字节才能看到 344 处;只解一小段,不整包解压。
// niftiMagicAt344 只认 **"n+1"(单文件 .nii/.nii.gz)**,**不认 "ni1"**(#9)。
// ni1 是 ANALYZE 衍生的**双文件**格式:头在 .hdr、体素在独立的 .img。单对象上传只能
// 拿到其中一个文件,而 worker 又要求 vox_offset>=352(同文件内有体素)→ ni1 必然 terminal
// reject。与其让它在 QC 蒙混过关、到 worker 才死,不如在准入处就当"不识别"拒掉(fail-closed)。
// 要支持双文件 ni1 需上传 canonical manifest,按各文件 path/size/SHA 算资产 SHA(欠账)。
func niftiMagicAt344(head []byte) bool {
	if len(head) < 348 {
		return false
	}
	return string(head[344:347]) == "n+1"
}

func sniffNIfTI(raw []byte) (string, bool) {
	if niftiMagicAt344(raw) {
		return "nii", true
	}
	// gzip 包装(.nii.gz):只解到能看见偏移 344 为止。
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", false
		}
		defer zr.Close()
		head := make([]byte, 348)
		if _, err := io.ReadFull(zr, head); err != nil {
			return "", false
		}
		if niftiMagicAt344(head) {
			return "nii.gz", true
		}
	}
	return "", false
}

// sniffMedia 识别图片 / 音频 / 视频 / 医学体数据，返回 (mime, format, kind)。
// kind ∈ {"image","audio","video"}。优先用文件签名（精确），签名覆盖不到的
// 音视频容器再回退到客户端声明的 declaredMIME。mime 为空表示无法识别。
func sniffMedia(raw []byte, declaredMIME string) (mime, format, kind string) {
	// 图片签名最精确，先处理（也优先于 RIFF/WAVE，避免 webp 与 wav 混淆）。
	if m, f, ok := sniffImage(raw); ok {
		return m, f, "image"
	}
	// 医学体数据（Phase C）：NIfTI 的魔数在偏移 344，不在文件头，所以必须显式识别
	// ——否则 .nii/.nii.gz 会掉进"unknown or unsupported file type"被 QC 拒收
	// （E2E 实测踩到:单测直接调 RegisterAsset 绕过了 QC，把这个洞盖住了）。
	if f, ok := sniffNIfTI(raw); ok {
		return "application/x-nifti", f, "volume"
	}
	// 病理全切片(C1.1b):svs/ndpi 本质是**金字塔 TIFF**。
	// ⚠️ 这里必须与"普通 TIFF 图片"区分开:一张 10 万×10 万的切片若被当成普通
	// 图片,下游会试图整幅解码进内存。判据是**有没有瓦片金字塔**(见 sniffWSI),
	// 而不是文件头——两者的文件头完全一样。
	if f, ok := sniffWSI(raw); ok {
		return "application/x-wsi", f, "wsi"
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

		// #8 丢弃所有可能藏 PHI 的元数据段,不只 EXIF。旧代码只认 APP1/Exif,XMP、IPTC、
		// COM 原样留下 —— 它们照样能带姓名/GPS/病历号。逐类清:
		drop := false
		switch marker {
		case 0xE1: // APP1:EXIF(含 GPS/MakerNote)或 XMP
			payload := raw[segStart+2 : segEnd]
			if bytes.HasPrefix(payload, []byte("Exif\x00\x00")) ||
				bytes.HasPrefix(payload, []byte("http://ns.adobe.com/xap/")) {
				drop = true
			}
		case 0xED: // APP13:IPTC / Photoshop 资源(常含版权/作者/说明)
			drop = true
		case 0xFE: // COM:注释段
			drop = true
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

// sniffWSI 识别病理全切片(svs / ndpi / 金字塔 tif),返回 (format, ok)。
//
// ⚠️ **判据是"有没有瓦片金字塔",不是文件头**。svs/ndpi 与普通 TIFF 图片的
// 文件头**完全一样**(II/MM + 42/43),只靠魔数区分不开。而混淆的代价是不对称的:
// 一张 10 万×10 万的切片若被当成普通图片,下游会试图把整幅解码进内存——
// 那不是"显示得不好看",是把进程打死。
//
// 所以这里复用 C1.1a 的探针:能解出**带瓦片的金字塔层**才算 WSI,否则交还给
// 后面的分支(普通 TIFF 图片目前不在受支持之列,会被 QC 正常拒收)。
// 探针只读几 KB 的 IFD 头,不解像素,对大文件同样廉价。
func sniffWSI(raw []byte) (string, bool) {
	if len(raw) < 8 {
		return "", false
	}
	if !((raw[0] == 'I' && raw[1] == 'I') || (raw[0] == 'M' && raw[1] == 'M')) {
		return "", false
	}
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil || len(m.Levels) == 0 {
		return "", false
	}
	switch m.Vendor {
	case "aperio":
		return "svs", true
	default:
		return "pyramidal-tiff", true
	}
}
