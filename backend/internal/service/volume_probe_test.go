package service

// volume_probe(C3.1)的语义锁。最要命的一条是 C-H3 手性:affine 的 det 符号
// 决定坐标系左右手,单轴镜像(det<0)就是"左肺病灶标到右肺"那种会出人命且
// 不报错的翻转。这些测试把它钉死,并覆盖 CT/MRI 推断、scl 重标定、qform 回退、
// 以及一堆必须"响亮拒绝而非静默误读"的坏头。末尾用真实 MNI152 跨数据验证。

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

// niftiBuilder assembles a minimal valid NIfTI-1 (.nii) byte stream.
type niftiBuilder struct {
	dims     [3]int
	datatype int16
	bitpix   int16
	pixdim   [3]float32
	sform    *[3][4]float32 // set → sform_code=1
	qform    *qformParams   // set → qform_code=1
	sclSlope float32
	sclInter float32
	voxels   []byte
}

type qformParams struct {
	b, c, d          float32
	ox, oy, oz       float32
	qfac             float32
}

func (nb niftiBuilder) build() []byte {
	const voxOffset = 352
	h := make([]byte, voxOffset)
	binary.LittleEndian.PutUint32(h[0:], 348)
	pi16 := func(off int, v int16) { binary.LittleEndian.PutUint16(h[off:], uint16(v)) }
	pf := func(off int, v float32) { binary.LittleEndian.PutUint32(h[off:], math.Float32bits(v)) }
	pi16(40, 3)
	pi16(42, int16(nb.dims[0]))
	pi16(44, int16(nb.dims[1]))
	pi16(46, int16(nb.dims[2]))
	pi16(70, nb.datatype)
	pi16(72, nb.bitpix)
	qfac := float32(1)
	if nb.qform != nil && nb.qform.qfac != 0 {
		qfac = nb.qform.qfac
	}
	pf(76, qfac)
	pf(80, nb.pixdim[0])
	pf(84, nb.pixdim[1])
	pf(88, nb.pixdim[2])
	pf(108, voxOffset)
	pf(112, nb.sclSlope)
	pf(116, nb.sclInter)
	if nb.sform != nil {
		pi16(254, 1)
		for r := 0; r < 3; r++ {
			for c := 0; c < 4; c++ {
				pf(280+r*16+c*4, nb.sform[r][c])
			}
		}
	}
	if nb.qform != nil {
		pi16(252, 1)
		pf(256, nb.qform.b)
		pf(260, nb.qform.c)
		pf(264, nb.qform.d)
		pf(268, nb.qform.ox)
		pf(272, nb.qform.oy)
		pf(276, nb.qform.oz)
	}
	copy(h[344:], "n+1\x00")
	return append(h, nb.voxels...)
}

// int16Voxels builds a filled int16 volume with a given constant plus a max spike.
func int16Voxels(n int, base, spike int16) []byte {
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := base
		if i == n/2 {
			v = spike
		}
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func uint16Voxels(n int, base, spike uint16) []byte {
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := base
		if i == n/2 {
			v = spike
		}
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return b
}

func TestParseNIfTI_SformGeometry(t *testing.T) {
	n := 4 * 4 * 3
	// RAS affine: diag(0.7, 0.8, 2.0) with origin (-10, -20, 5).
	aff := [3][4]float32{{0.7, 0, 0, -10}, {0, 0.8, 0, -20}, {0, 0, 2.0, 5}}
	raw := niftiBuilder{
		dims: [3]int{4, 4, 3}, datatype: 4, bitpix: 16, pixdim: [3]float32{0.7, 0.8, 2.0},
		sform: &aff, sclSlope: 1, sclInter: 0, voxels: int16Voxels(n, 100, 500),
	}.build()

	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Dims != [3]int{4, 4, 3} {
		t.Errorf("dims = %v", m.Dims)
	}
	for i, want := range []float64{0.7, 0.8, 2.0} {
		if math.Abs(m.Spacing[i]-want) > 1e-6 {
			t.Errorf("spacing[%d] = %v want %v", i, m.Spacing[i], want)
		}
	}
	if m.Origin != [3]float64{-10, -20, 5} {
		t.Errorf("origin = %v", m.Origin)
	}
	// Diagonal positive affine → right-handed.
	if m.Handedness != 1 {
		t.Errorf("handedness = %d want +1 (right-handed RAS)", m.Handedness)
	}
}

// C-H3 的葬礼:单轴镜像(det<0)必须被判为左手系。这是"左右翻转"的机器警报。
// 变异验证:把 fillGeometryFromAffine 里的 det<0 判据去掉,本测试必须红。
func TestParseNIfTI_MirroredAffineIsLeftHanded(t *testing.T) {
	n := 2 * 2 * 2
	// Single-axis flip on x → mirror → left-handed.
	aff := [3][4]float32{{-1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	raw := niftiBuilder{
		dims: [3]int{2, 2, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{1, 1, 1},
		sform: &aff, sclSlope: 1, voxels: int16Voxels(n, 0, 10),
	}.build()
	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Handedness != -1 {
		t.Fatalf("single-axis mirror must be left-handed(-1), got %d — L/R flip would ship silently", m.Handedness)
	}
	// Double flip diag(-1,-1,1) is DICOM-LPS→RAS: det +1, NOT a mirror.
	aff2 := [3][4]float32{{-1, 0, 0, 0}, {0, -1, 0, 0}, {0, 0, 1, 0}}
	raw2 := niftiBuilder{dims: [3]int{2, 2, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{1, 1, 1}, sform: &aff2, sclSlope: 1, voxels: int16Voxels(n, 0, 10)}.build()
	m2, _ := ParseNIfTIMeta(raw2)
	if m2.Handedness != 1 {
		t.Fatalf("LPS→RAS double-flip must stay right-handed(+1), got %d", m2.Handedness)
	}
}

// CT:int16 + HU 量程(有负值,骨强正)+ scl 重标定 → 推断 ct,给标准窗预设。
func TestParseNIfTI_CTModalityAndWindows(t *testing.T) {
	n := 8 * 8 * 4
	aff := [3][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	// stored 0..2000, scl slope 1 inter -1024 → HU -1024..976.
	raw := niftiBuilder{
		dims: [3]int{8, 8, 4}, datatype: 4, bitpix: 16, pixdim: [3]float32{1, 1, 1},
		sform: &aff, sclSlope: 1, sclInter: -1024, voxels: int16Voxels(n, 0, 2000),
	}.build()
	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Modality != "ct" {
		t.Errorf("modality = %q want ct (HU range %.0f..%.0f)", m.Modality, m.IntensityLo, m.IntensityHi)
	}
	if math.Abs(m.IntensityLo-(-1024)) > 1e-6 || math.Abs(m.IntensityHi-976) > 1e-6 {
		t.Errorf("HU range = %.0f..%.0f want -1024..976 (scl applied)", m.IntensityLo, m.IntensityHi)
	}
	names := map[string]bool{}
	for _, w := range m.Windows {
		names[w.Name] = true
	}
	for _, want := range []string{"soft_tissue", "lung", "bone", "brain"} {
		if !names[want] {
			t.Errorf("CT windows missing %q (got %v)", want, names)
		}
	}
}

// MRI:uint16 非负 → 推断 mri,窗从自身强度量程导出(无绝对刻度)。
func TestParseNIfTI_MRIModalityAndWindow(t *testing.T) {
	n := 6 * 6 * 3
	aff := [3][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	raw := niftiBuilder{
		dims: [3]int{6, 6, 3}, datatype: 512, bitpix: 16, pixdim: [3]float32{1, 1, 1},
		sform: &aff, sclSlope: 1, voxels: uint16Voxels(n, 200, 4000),
	}.build()
	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Modality != "mri" {
		t.Errorf("modality = %q want mri", m.Modality)
	}
	if len(m.Windows) != 1 || m.Windows[0].Name != "full_range" {
		t.Errorf("MRI window = %+v want single full_range", m.Windows)
	}
	// full-range center on midpoint of 200..4000.
	if math.Abs(m.Windows[0].Center-2100) > 1 || math.Abs(m.Windows[0].Width-3800) > 1 {
		t.Errorf("full_range = W%.0f C%.0f want W3800 C2100", m.Windows[0].Width, m.Windows[0].Center)
	}
}

// qform 回退:无 sform 时用四元数重建。单位四元数 + qfac=1 → 对角 spacing affine。
func TestParseNIfTI_QformFallback(t *testing.T) {
	n := 3 * 3 * 2
	raw := niftiBuilder{
		dims: [3]int{3, 3, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{1.5, 1.5, 3.0},
		qform:    &qformParams{b: 0, c: 0, d: 0, ox: 1, oy: 2, oz: 3, qfac: 1},
		sclSlope: 1, voxels: int16Voxels(n, 10, 20),
	}.build()
	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i, want := range []float64{1.5, 1.5, 3.0} {
		if math.Abs(m.Spacing[i]-want) > 1e-5 {
			t.Errorf("qform spacing[%d] = %v want %v", i, m.Spacing[i], want)
		}
	}
	if m.Origin != [3]float64{1, 2, 3} {
		t.Errorf("qform origin = %v want [1 2 3]", m.Origin)
	}
	if m.Handedness != 1 {
		t.Errorf("identity quaternion handedness = %d want +1", m.Handedness)
	}
}

// #4 scl_slope=0 表示"无缩放":inter 必须一并忽略,否则整卷偏移 inter(常见 CT 的 -1024)
// 且不报错——强度、CT 推断、窗位、PNG 映射全被带偏。变异:去掉规范化里 inter=0,本测试红。
func TestParseNIfTI_SlopeZeroIgnoresInter(t *testing.T) {
	n := 4 * 4 * 2
	aff := [3][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	raw := niftiBuilder{
		dims: [3]int{4, 4, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{1, 1, 1},
		sform: &aff, sclSlope: 0, sclInter: -1024, voxels: int16Voxels(n, 100, 200),
	}.build()
	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.SclSlope != 1 || m.SclInter != 0 {
		t.Fatalf("slope=0 应规范成 (1,0),得到 (%.1f,%.1f)——inter 被错误应用会让整卷偏移", m.SclSlope, m.SclInter)
	}
	// stored 100..200,slope=0(无缩放)→ real 100..200,不该被 -1024 偏移成负。
	if m.IntensityLo < 0 {
		t.Fatalf("强度被 inter 偏移了(lo=%.0f<0),slope=0 时不该应用 inter", m.IntensityLo)
	}
}

// #3 空间单位:xyzt_units 声明米/微米时,spacing 必须换算到 mm(否则测量/体积错 1000×);
// 未声明时按 mm 兜底但标 SpacingUnitKnown=false。#5:Orient.Pixdim 原样保存源 pixdim。
func TestParseNIfTI_SpatialUnitsConvertedToMM(t *testing.T) {
	aff := [3][4]float32{{2, 0, 0, 0}, {0, 2, 0, 0}, {0, 0, 3, 0}} // 文件单位下 spacing 2,2,3
	raw := niftiBuilder{
		dims: [3]int{2, 2, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{2, 2, 3},
		sform: &aff, sclSlope: 1, voxels: int16Voxels(8, 0, 1),
	}.build()

	// 默认 xyzt_units=0(未知):spacing 保文件值,SpacingUnitKnown=false。
	m0, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m0.SpacingUnitKnown {
		t.Error("未声明单位时 SpacingUnitKnown 应为 false")
	}
	if math.Abs(m0.Spacing[2]-3) > 1e-5 {
		t.Errorf("未知单位应按文件值兜底 mm, spacing=%v want 3", m0.Spacing[2])
	}

	// 声明"米"(xyzt_units 低 3 位 = 1):spacing 应 ×1000 换成 mm。
	rawM := clone(raw)
	rawM[123] = (rawM[123] &^ 0x07) | 1
	mM, err := ParseNIfTIMeta(rawM)
	if err != nil {
		t.Fatal(err)
	}
	if !mM.SpacingUnitKnown {
		t.Error("声明米时 SpacingUnitKnown 应为 true")
	}
	if math.Abs(mM.Spacing[2]-3000) > 1e-3 {
		t.Errorf("米→毫米应 ×1000, spacing=%v want 3000(否则测量/体积错 1000×)", mM.Spacing[2])
	}
	if math.Abs(mM.Orient.Pixdim[2]-3) > 1e-5 {
		t.Errorf("#5 Orient.Pixdim 应保存源 pixdim, got %v want 3", mM.Orient.Pixdim[2])
	}
}

// 坏头必须响亮拒绝,不是静默误读(那是本项目最贵的一类事故)。
func TestParseNIfTI_RejectsBadHeaders(t *testing.T) {
	good := niftiBuilder{dims: [3]int{2, 2, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{1, 1, 1},
		sform: &[3][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}, sclSlope: 1, voxels: int16Voxels(8, 0, 1)}.build()

	cases := map[string]func([]byte) []byte{
		"too short":          func(b []byte) []byte { return b[:100] },
		"bad sizeof_hdr":     func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint32(c[0:], 999); return c },
		"bad magic":          func(b []byte) []byte { c := clone(b); copy(c[344:], "xxx\x00"); return c },
		"unsupported dtype":  func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint16(c[70:], 1024); return c },
		"voxel data short":   func(b []byte) []byte { return b[:352+4] },
		// #2 4D 静默截断:ndim=4 且 dim[4]>1 必须拒(否则后续时间点无声丢失)。
		"4D trailing dim":    func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint16(c[40:], 4); binary.LittleEndian.PutUint16(c[48:], 10); return c },
		// #6 vox_offset 未校验:负 / 超文件长度都会让 raw[voxOffset:] 越界 panic。
		"negative vox_offset": func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint32(c[108:], math.Float32bits(-1)); return c },
		"vox_offset past EOF": func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint32(c[108:], math.Float32bits(1e9)); return c },
		// #7 资源护栏:切片数超上限即拒(挡 1×1×N 资源风暴)。
		"implausible slices": func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint16(c[46:], 5000); return c },
		// #4 非有限 scl_slope 必须拒(不能拿 Inf/NaN 去缩放整卷)。
		"non-finite slope": func(b []byte) []byte { c := clone(b); binary.LittleEndian.PutUint32(c[112:], math.Float32bits(float32(math.Inf(1)))); return c },
	}
	for name, mangle := range cases {
		if _, err := ParseNIfTIMeta(mangle(good)); err == nil {
			t.Errorf("%s: expected error, got nil (silent mis-read is the dangerous case)", name)
		}
	}
}

func clone(b []byte) []byte { c := make([]byte, len(b)); copy(c, b); return c }

// 跨数据验证:真实 MNI152 模板(下过一次)。文件不在就跳过(不是绿)。
func TestParseNIfTI_RealMNI152(t *testing.T) {
	path := os.Getenv("MM_TEST_NIFTI")
	if path == "" {
		t.Skip("set MM_TEST_NIFTI to a decompressed .nii to run the cross-data check")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("read %s: %v", path, err)
	}
	m, err := ParseNIfTIMeta(raw)
	if err != nil {
		t.Fatalf("parse real nifti: %v", err)
	}
	if m.Dims[0] <= 0 || m.Dims[1] <= 0 || m.Dims[2] <= 0 {
		t.Fatalf("degenerate dims %v", m.Dims)
	}
	if m.Spacing[0] <= 0 || m.Spacing[1] <= 0 || m.Spacing[2] <= 0 {
		t.Fatalf("non-positive spacing %v", m.Spacing)
	}
	if m.IntensityHi <= m.IntensityLo {
		t.Fatalf("flat intensity range %.1f..%.1f", m.IntensityLo, m.IntensityHi)
	}
	t.Logf("MNI152: dims=%v spacing=%v modality=%s HU/int=%.0f..%.0f handed=%+d",
		m.Dims, m.Spacing, m.Modality, m.IntensityLo, m.IntensityHi, m.Handedness)
}
