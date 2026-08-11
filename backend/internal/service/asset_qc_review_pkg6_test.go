package service

// 包6(资产/QC/multipart/生命周期)对抗式 review 的回归锁——纯函数部分,不需 DB。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// #5 内容种类必须与数据集模态匹配(含医学模态)。
func TestCheckModalityMIME_KindMatch(t *testing.T) {
	// 匹配 → nil。
	for _, c := range []struct{ modality, mime string }{
		{"image", "image/png"}, {"video", "video/mp4"}, {"audio", "audio/wav"},
		{"volume", "application/x-nifti"}, {"wsi", "application/x-wsi"},
	} {
		if err := checkModalityMIME(c.modality, c.mime); err != nil {
			t.Fatalf("checkModalityMIME(%q,%q) 应通过,得到 %v", c.modality, c.mime, err)
		}
	}
	// 不匹配 → 报错(PNG 传进 video 集、NIfTI 传进 image 集)。
	for _, c := range []struct{ modality, mime string }{
		{"video", "image/png"}, {"image", "application/x-nifti"}, {"volume", "application/x-wsi"},
	} {
		if err := checkModalityMIME(c.modality, c.mime); err == nil {
			t.Fatalf("#5 回归:checkModalityMIME(%q,%q) 应拒绝(路由错误),却通过", c.modality, c.mime)
		}
	}
}

// #9 NIfTI 只认单文件 n+1,双文件 ni1 一律不识别(拒)。
func TestNiftiMagic_RejectsNI1(t *testing.T) {
	head := make([]byte, 352)
	copy(head[344:], "n+1\x00")
	if !niftiMagicAt344(head) {
		t.Fatalf("n+1 应识别为 NIfTI")
	}
	copy(head[344:], "ni1\x00")
	if niftiMagicAt344(head) {
		t.Fatalf("#9 回归:ni1(双文件)不应被识别为可单对象上传的 NIfTI")
	}
}

// #7 webp/bmp 无 decoder,sniffImage 有意不识别。
func TestSniffImage_NoWebpBmp(t *testing.T) {
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 4)...)
	if _, _, ok := sniffImage(webp); ok {
		t.Fatalf("#7 回归:webp 不该被 sniffImage 接受(没有 decoder)")
	}
	bmp := append([]byte("BM"), make([]byte, 20)...)
	if _, _, ok := sniffImage(bmp); ok {
		t.Fatalf("#7 回归:bmp 不该被 sniffImage 接受")
	}
	// jpeg/png/gif 仍识别。
	if _, _, ok := sniffImage([]byte{0xFF, 0xD8, 0xFF, 0, 0, 0}); !ok {
		t.Fatalf("jpeg 应仍被识别")
	}
}

// #6 DecodeConfig 能读头的截断 PNG(有 IHDR、无 IDAT/IEND)必须被完整解码挡下。
func TestQCInspect_RejectsTruncatedPNG(t *testing.T) {
	// 编码一张真 PNG,再截断到只剩 签名 + IHDR(DecodeConfig 能读出宽高,Decode 会失败)。
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	full := buf.Bytes()
	// 签名 8 + IHDR(4 len + 4 tag + 13 data + 4 crc = 25)= 33 字节:含尺寸、无像素数据。
	truncated := full[:33]

	// 先确认 DecodeConfig 仍能读出尺寸(证明这是"头合法、体截断"的那类)。
	if _, _, err := image.DecodeConfig(bytes.NewReader(truncated)); err != nil {
		t.Skipf("构造的截断 PNG 连头都读不出(%v),换构造方式;不影响被测逻辑", err)
	}
	q := NewQCService(QCConfig{})
	report, _, err := q.Inspect(bytes.NewReader(truncated), "image/png")
	if err != nil {
		t.Fatalf("Inspect 基础错误: %v", err)
	}
	if report.Status != qcFailed {
		t.Fatalf("#6 回归:截断 PNG(仅有文件头)应 QC 失败,得到 %q reasons=%v", report.Status, report.Reasons)
	}
}
