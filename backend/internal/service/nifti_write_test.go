package service

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// C3.4 NIfTI 写入器。
//
// ⚠️ 这里的自证测试（写出去再用我们自己的 ParseNIfTIMeta 读回来）**只能证明自洽**。
// 一个自洽但非标的写入器往返完全成功，直到把文件拖进 3D Slicer 才发现全错——
// C3.3a 的 RLE 就是这么被 pycocotools 抓到的。所以权威验证在
// `testdata/nifti/verify_nifti.py`（nibabel），并把验过的向量冻进
// `testdata/nifti/golden.json`，CI 里对着夹具比。

// 一个刻意"不好看"的几何：各向异性体素 + 非零原点 + LAS 朝向（第一轴翻转，
// det<0 的左手系）。正交单位阵测不出任何朝向 bug——它对任何写法都成立。
func awkwardMeta() *VolumeMeta {
	return &VolumeMeta{
		Dims:    [3]int{4, 3, 2},
		Spacing: [3]float64{0.7, 1.25, 3.0},
		Origin:  [3]float64{-90.5, 12.25, -7.75},
		// 列向量：i→-x（左右翻转）, j→+y, k→+z
		Direction:  [9]float64{-1, 0, 0, 0, 1, 0, 0, 0, 1},
		Handedness: -1,
	}
}

func TestAffineFromMetaRoundTrip(t *testing.T) {
	// 分解（spacing = 列范数, direction = 归一化列）与重组必须严格互逆。
	// 这条不成立 = 导出的几何是错的，而**没有任何东西会报错**。
	m := awkwardMeta()
	aff := AffineFromMeta(m)

	want := [3][4]float64{
		{-0.7, 0, 0, -90.5},
		{0, 1.25, 0, 12.25},
		{0, 0, 3.0, -7.75},
	}
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			if math.Abs(aff[r][c]-want[r][c]) > 1e-12 {
				t.Fatalf("affine[%d][%d] = %v, want %v", r, c, aff[r][c], want[r][c])
			}
		}
	}
}

func TestWriteNIfTIReadBackByOwnParser(t *testing.T) {
	m := awkwardMeta()
	n := m.Dims[0] * m.Dims[1] * m.Dims[2]
	labels := make([]uint16, n)
	for i := range labels {
		labels[i] = uint16(i % 3) // 0/1/2
	}

	gz, err := WriteNIfTIGz(NIfTIVolume{
		Dims: m.Dims, Affine: AffineFromMeta(m), Labels: labels, MaxLabel: 2, Descrip: "dg-seg",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := gunzipT(t, gz)

	got, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Dims != m.Dims {
		t.Errorf("dims = %v, want %v", got.Dims, m.Dims)
	}
	for i := 0; i < 3; i++ {
		if math.Abs(got.Spacing[i]-m.Spacing[i]) > 1e-5 {
			t.Errorf("spacing[%d] = %v, want %v", i, got.Spacing[i], m.Spacing[i])
		}
		if math.Abs(got.Origin[i]-m.Origin[i]) > 1e-4 {
			t.Errorf("origin[%d] = %v, want %v", i, got.Origin[i], m.Origin[i])
		}
	}
	// **手性必须一起过来**：左手系写出去还是左手系。这一位翻了 = 左右反了，
	// 而左右反了在画面上看不出来（C-H3）。
	if got.Handedness != m.Handedness {
		t.Errorf("handedness = %d, want %d（左右可能被翻了）", got.Handedness, m.Handedness)
	}
	for i := 0; i < 9; i++ {
		if math.Abs(got.Direction[i]-m.Direction[i]) > 1e-5 {
			t.Fatalf("direction[%d] = %v, want %v", i, got.Direction[i], m.Direction[i])
		}
	}
}

// #21 label→组织名 作为 NIfTI 扩展嵌入文件,让含义随文件走(HTTP 头在转发/离线后丢失)。
// 关键:扩展加在 header 后、体素前,vox_offset 后移,几何/像素零影响——加了扩展仍能被正确解析。
func TestWriteNIfTI_EmbedsLabelMapExtension(t *testing.T) {
	m := awkwardMeta()
	n := m.Dims[0] * m.Dims[1] * m.Dims[2]
	labelJSON := []byte(`{"1":"肝","2":"肿瘤"}`)
	gz, err := WriteNIfTIGz(NIfTIVolume{
		Dims: m.Dims, Affine: AffineFromMeta(m), Labels: make([]uint16, n), MaxLabel: 2, LabelMapJSON: labelJSON,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := gunzipT(t, gz)

	if raw[348] != 1 {
		t.Fatalf("扩展标志(byte 348)= %d, want 1", raw[348])
	}
	voxOff := int(math.Float32frombits(binary.LittleEndian.Uint32(raw[108:112])))
	esize := int(binary.LittleEndian.Uint32(raw[352:356]))
	if esize%16 != 0 || esize < 8+len(labelJSON) {
		t.Fatalf("esize = %d(须是 16 的倍数且 ≥ %d)", esize, 8+len(labelJSON))
	}
	if voxOff != 352+esize {
		t.Fatalf("vox_offset = %d, want %d(352 + esize)", voxOff, 352+esize)
	}
	if code := binary.LittleEndian.Uint32(raw[356:360]); code != 6 {
		t.Errorf("ecode = %d, want 6", code)
	}
	if !bytes.Contains(raw[360:voxOff], labelJSON) {
		t.Errorf("label map JSON 未嵌入扩展")
	}
	// 最关键:加扩展后几何仍能被正确解析(体素在后移的 vox_offset 处)。
	got, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("加扩展后仍应可解析: %v", err)
	}
	if got.Dims != m.Dims {
		t.Errorf("dims = %v, want %v(扩展破坏了几何?)", got.Dims, m.Dims)
	}
}

func TestWriteNIfTIDatatypeSwitch(t *testing.T) {
	m := awkwardMeta()
	n := m.Dims[0] * m.Dims[1] * m.Dims[2]

	// ≤255 个标签 → uint8（bitpix 8）
	small, err := WriteNIfTIGz(NIfTIVolume{Dims: m.Dims, Affine: AffineFromMeta(m), Labels: make([]uint16, n), MaxLabel: 255})
	if err != nil {
		t.Fatal(err)
	}
	if bp := bitpixOf(t, small); bp != 8 {
		t.Errorf("MaxLabel=255 → bitpix %d, want 8", bp)
	}
	// >255 → int16。写成 uint8 会让第 256 段**静默截断成 0**（= 那一段消失）。
	big, err := WriteNIfTIGz(NIfTIVolume{Dims: m.Dims, Affine: AffineFromMeta(m), Labels: make([]uint16, n), MaxLabel: 256})
	if err != nil {
		t.Fatal(err)
	}
	if bp := bitpixOf(t, big); bp != 16 {
		t.Errorf("MaxLabel=256 → bitpix %d, want 16", bp)
	}
}

func TestWriteNIfTIRejectsMismatchedLabelCount(t *testing.T) {
	m := awkwardMeta()
	_, err := WriteNIfTIGz(NIfTIVolume{Dims: m.Dims, Affine: AffineFromMeta(m), Labels: make([]uint16, 3)})
	if err == nil {
		t.Fatal("体素数对不上必须报错，不能写出一个长度不对的文件")
	}
}

// TestNIfTIGoldenMatchesNibabelFixture 对着 **nibabel 验过**的夹具比。
// 夹具由 testdata/nifti/verify_nifti.py 生成并校验；这里只做纯 Go 的比对，
// 所以 CI 不需要 Python，而"权威性"来自夹具生成时那一次真实的 nibabel 比对。
func TestNIfTIGoldenMatchesNibabelFixture(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "testdata", "nifti")
	specRaw, err := os.ReadFile(filepath.Join(dir, "golden.json"))
	if err != nil {
		t.Skipf("夹具缺失(%v)——先跑 testdata/nifti/verify_nifti.py 生成", err)
	}
	var spec struct {
		Dims     [3]int      `json:"dims"`
		Spacing  [3]float64  `json:"spacing"`
		Origin   [3]float64  `json:"origin"`
		Affine   [][]float64 `json:"affine"`
		Labels   []uint16    `json:"labels"`
		MaxLabel int         `json:"max_label"`
		SHA256   string      `json:"sha256_nii"`
	}
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		t.Fatalf("golden.json: %v", err)
	}

	var aff [3][4]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			aff[r][c] = spec.Affine[r][c]
		}
	}
	gz, err := WriteNIfTIGz(NIfTIVolume{
		Dims: spec.Dims, Affine: aff, Labels: spec.Labels, MaxLabel: spec.MaxLabel, Descrip: "dg-seg",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := gunzipT(t, gz)

	// 与冻结的 .nii 逐字节比对（gzip 本身不比——压缩参数会变，而载荷才是契约）。
	wantRaw, err := os.ReadFile(filepath.Join(dir, "golden.nii"))
	if err != nil {
		t.Skipf("golden.nii 缺失: %v", err)
	}
	if !bytes.Equal(raw, wantRaw) {
		t.Fatalf("写出的 NIfTI 与 nibabel 验过的夹具不一致（%d vs %d 字节）——\n"+
			"这意味着几何或体素编码变了。别改夹具，先跑 verify_nifti.py 弄清哪边错了。",
			len(raw), len(wantRaw))
	}
}

func gunzipT(t *testing.T, gz []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip read: %v", err)
	}
	return raw
}

func bitpixOf(t *testing.T, gz []byte) int16 {
	t.Helper()
	raw := gunzipT(t, gz)
	return int16(raw[72]) | int16(raw[73])<<8
}

// TestWriteNIfTIPreservesBothOrientations 钉死 C3.4 验收在 3D Slicer 里逮到的
// 那个 bug：源文件的 sform 与 qform **互相矛盾**（真实存在：MNI152 模板的
// sform 原点 (-75.8,-110.8,-71.8)、qform 原点 (0,0,0)），而生态对"信哪个"的
// 选择相反——nibabel/FSL 认 sform，ITK/3D Slicer/ANTs 认 qform。
//
// 旧写法只写 sform，等于替用户选边：标签图在 Slicer 里与原图**错开 75mm**，
// 两个文件各自都合法、都能打开，只是叠不上。nibabel 验不出来，因为它和我们的
// 解析器口味相同（口味相同的读者永远同意你）。
//
// 正确行为：两份原样带过去。下游无论用哪条规则，对影像和标签用的都是同一条。
func TestWriteNIfTIPreservesBothOrientations(t *testing.T) {
	src := NIfTIOrientation{
		SformCode: 2,
		Srow: [12]float64{
			0.7374631, 0, 0, -75.762535,
			0, 0.7374631, 0, -110.762535,
			0, 0, 0.7374631, -71.762535,
		},
		QformCode: 2,
		Quatern:   [3]float64{0, 0, 0},
		QOffset:   [3]float64{0, 0, 0}, // 与 sform 刻意不一致——这正是要保留的
		QFac:      1,
	}
	dims := [3]int{3, 2, 2}
	gz, err := WriteNIfTIGz(NIfTIVolume{
		Dims: dims, Affine: AffineFromMeta(awkwardMeta()),
		Labels: make([]uint16, 12), MaxLabel: 1, Orient: &src,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := gunzipT(t, gz)

	rd16 := func(off int) int16 { return int16(raw[off]) | int16(raw[off+1])<<8 }
	rdf := func(off int) float64 {
		return float64(math.Float32frombits(uint32(raw[off]) | uint32(raw[off+1])<<8 |
			uint32(raw[off+2])<<16 | uint32(raw[off+3])<<24))
	}

	if got := rd16(254); int(got) != src.SformCode {
		t.Errorf("sform_code = %d, want %d", got, src.SformCode)
	}
	// **这一条是核心**：旧写法把 qform_code 写成 0，Slicer 就会退回 sform，
	// 而源文件在 Slicer 眼里用的是 qform —— 于是两者错开。
	if got := rd16(252); int(got) != src.QformCode {
		t.Errorf("qform_code = %d, want %d（写成 0 就等于替用户选边，Slicer 里会错位）", got, src.QformCode)
	}
	for i, off := range []int{268, 272, 276} {
		if math.Abs(rdf(off)-src.QOffset[i]) > 1e-5 {
			t.Errorf("qoffset[%d] = %v, want %v", i, rdf(off), src.QOffset[i])
		}
	}
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			want := src.Srow[r*4+c]
			if got := rdf(280 + r*16 + c*4); math.Abs(got-want) > 1e-4 {
				t.Errorf("srow[%d][%d] = %v, want %v", r, c, got, want)
			}
		}
	}
	// 源里两份原本就不一致，写出来后**也必须还不一致**——统一它们同样是替用户
	// 做决定，而且会让标签图与原图对不上。
	if math.Abs(rdf(268)-rdf(280+3*4)) < 1e-6 {
		t.Error("qoffset_x 与 srow_x[3] 被写成一致了——源文件的歧义被我们抹平了")
	}
}

// 没有源朝向时（C3.4 之前的旧派生物）退回 sform-only，且不能留下"朝向未知"。
func TestWriteNIfTIFallsBackWhenNoOrientation(t *testing.T) {
	gz, err := WriteNIfTIGz(NIfTIVolume{
		Dims: awkwardMeta().Dims, Affine: AffineFromMeta(awkwardMeta()),
		Labels: make([]uint16, 24), MaxLabel: 1, Orient: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := gunzipT(t, gz)
	if s := int16(raw[254]) | int16(raw[255])<<8; s != 1 {
		t.Errorf("回退时 sform_code = %d, want 1（两个 code 都为 0 = 朝向未知，工具会把体摆到原点）", s)
	}
	// 空的 Orient（全零）也要走回退，而不是写出 code 全 0 的头。
	empty := NIfTIOrientation{}
	if empty.HasOrientation() {
		t.Error("全零的朝向块不该被当作有效朝向")
	}
}
