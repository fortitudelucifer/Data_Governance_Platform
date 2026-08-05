package service

// volume_probe.go — 执行方案-04 · C3.1:3D 体数据(CT/MRI)导入的元数据提取。
//
// NIfTI-1 解析 → VolumeMeta。**这里承载 C-H3「坐标系纪律」最硬的一条**:
// 几何存体素索引 (i,j,k),世界坐标变换靠 affine/direction——而 NIfTI 是 RAS+、
// DICOM 是 LPS,方向约定不同,混淆会出现左右翻转。左右翻转在医学影像里是会
// 出人命的那种错(把左肺病灶标到右肺),且**不会报任何错**。所以 direction
// 矩阵与其手性(det 符号)在这里被显式算出、显式暴露,而不是埋在渲染代码里。

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Window is one window/level (窗宽/窗位) preset for display. CT presets are in
// Hounsfield units; MRI has no absolute scale so its default is derived from the
// volume's own intensity distribution.
type Window struct {
	Name   string  `json:"name"`
	Width  float64 `json:"width"`  // 窗宽
	Center float64 `json:"center"` // 窗位
}

// VolumeMeta is the persisted metadata for a 3D volume asset (derivative kind
// volume_meta). The viewer (C3.2) reads it to render slices and map to world
// coordinates; geometry is stored in voxel index space and only meets world
// space here.
type VolumeMeta struct {
	Dims     [3]int     `json:"dims"`     // (nx, ny, nz) voxel counts
	Spacing  [3]float64 `json:"spacing"`  // mm per voxel along i/j/k(已按 xyzt_units 换算)
	// SpacingUnitKnown 为 false 表示源文件没声明空间单位(xyzt_units=0):spacing 按 mm
	// 兜底,但物理测量(RECIST/体积)可能错整个扫描倍率,前端应据此禁用或标注不确定(#3)。
	SpacingUnitKnown bool       `json:"spacing_unit_known"`
	Origin           [3]float64 `json:"origin"` // world coords of voxel (0,0,0)(mm)
	// Direction is the 3×3 voxel→world rotation (unit columns), row-major.
	// Its determinant sign is the handedness: negative = left-handed frame
	// (a mismatch waiting to flip L/R on export). Explicitly surfaced so C3.4
	// export can assert LPS/RAS intent instead of guessing.
	Direction   [9]float64 `json:"direction"`
	Handedness  int        `json:"handedness"`   // +1 right-handed, -1 left-handed
	Datatype    string     `json:"datatype"`     // int16 / uint16 / …
	Modality    string     `json:"modality"`     // ct | mri | unknown (inferred)
	SclSlope    float64    `json:"scl_slope"`    // stored*slope+inter = real value (HU for CT)
	SclInter    float64    `json:"scl_inter"`
	IntensityLo float64    `json:"intensity_lo"` // real-value min (post scl)
	IntensityHi float64    `json:"intensity_hi"` // real-value max
	Windows     []Window   `json:"windows"`      // display presets
	// SliceScl* map a 16-bit slice PNG pixel directly to the real value:
	//   real = png_pixel_u16 * SliceSclSlope + SliceSclInter
	// This folds the unsigned-shift used to fit signed data into PNG16 into the
	// linear map, so the viewer applies one affine to the raw pixel — no separate
	// offset field, no double-mapping. Set by the slice derivation (C3.1b).
	SliceSclSlope float64 `json:"slice_scl_slope"`
	SliceSclInter float64 `json:"slice_scl_inter"`

	// Orient preserves the source header's orientation fields **verbatim** so an
	// export can write them back unchanged.
	//
	// ⚠️ Why this is not redundant with Direction/Spacing/Origin above:
	// NIfTI can carry orientation twice (sform matrix + qform quaternion), and
	// real files exist where the two DISAGREE — the MNI152 template is one:
	// sform origin (-75.8,-110.8,-71.8) vs qform origin (0,0,0). Worse, the
	// ecosystem is split on which wins: nibabel/FSL prefer sform, ITK/3D Slicer/
	// ANTs prefer qform. Decompose to a single affine and you have silently
	// picked a side — and a label map exported that way lands 75mm off the
	// source image in whichever tool made the other choice. Both files open
	// fine; they just don't overlay. (Caught in C3.4 Slicer acceptance.)
	//
	// So we do NOT arbitrate: we copy both representations through unchanged.
	// Whatever rule a downstream tool applies, it applies the same rule to the
	// image and to the label map, so they can never drift apart.
	Orient NIfTIOrientation `json:"orient"`
}

// NIfTIOrientation is the raw orientation block of a NIfTI-1 header.
type NIfTIOrientation struct {
	SformCode int         `json:"sform_code"`
	Srow      [12]float64 `json:"srow"` // srow_x/y/z flattened, row-major 3x4
	QformCode int         `json:"qform_code"`
	Quatern   [3]float64  `json:"quatern"` // b, c, d
	QOffset   [3]float64  `json:"qoffset"`
	QFac      float64     `json:"qfac"` // pixdim[0]: +1 or -1
	// Pixdim 是 pixdim[1..3](源单位下的体素尺寸)。qform 的 affine 由 quaternion×qfac×pixdim
	// 决定,少了它就没法忠实重建 qform——writer 拿 sform 列范数顶替会在 sform/qform 尺度
	// 不一致时写错(#5)。原样保存,导出时用它复原 qform。
	Pixdim [3]float64 `json:"pixdim"`
}

// HasOrientation reports whether either representation is present. Derivatives
// produced before C3.4 lack this block; callers must fall back rather than
// writing a header with both codes zero (= "orientation unknown", which makes
// tools place the volume at the origin with pixdim spacing).
func (o NIfTIOrientation) HasOrientation() bool { return o.SformCode > 0 || o.QformCode > 0 }

// niftiDatatypeNames maps NIfTI datatype codes to names + byte widths.
var niftiDatatypeNames = map[int16]struct {
	name  string
	bytes int
}{
	2:   {"uint8", 1},
	4:   {"int16", 2},
	8:   {"int32", 4},
	16:  {"float32", 4},
	512: {"uint16", 2},
	256: {"int8", 1},
	64:  {"float64", 8},
}

// 体数据资源护栏(#7):切片数 = 逐片 PNG 编码/对象写次数;未压缩字节 = 内存/解压预算。
// 挡住 1×1×32767 这类"小文件、大资源"以及巨卷 OOM。
const (
	maxVolumeSlices = 4096
	maxVolumeBytes  = int64(2) << 30 // 2 GB
)

// ParseNIfTIMeta parses a NIfTI-1 (.nii, decompressed) byte stream into
// VolumeMeta. It reads the 348-byte header for geometry and scans the voxel data
// for the intensity range that drives default windowing. The caller is
// responsible for gunzip of .nii.gz before calling.
func ParseNIfTIMeta(raw []byte) (*VolumeMeta, error) {
	if len(raw) < 352 {
		return nil, fmt.Errorf("nifti: too short (%d bytes)", len(raw))
	}
	if sz := int32(binary.LittleEndian.Uint32(raw[0:4])); sz != 348 {
		// Big-endian NIfTI exists but is rare; we don't silently mis-read it.
		return nil, fmt.Errorf("nifti: unexpected sizeof_hdr %d (big-endian or not NIfTI-1?)", sz)
	}
	magic := string(raw[344:347])
	if magic != "n+1" && magic != "ni1" {
		return nil, fmt.Errorf("nifti: bad magic %q", magic)
	}

	rd16 := func(off int) int16 { return int16(binary.LittleEndian.Uint16(raw[off : off+2])) }
	rdf := func(off int) float64 {
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4])))
	}

	ndim := int(rd16(40))
	if ndim < 3 {
		return nil, fmt.Errorf("nifti: need ≥3 dims, got %d", ndim)
	}
	nx, ny, nz := int(rd16(42)), int(rd16(44)), int(rd16(46))
	if nx <= 0 || ny <= 0 || nz <= 0 {
		return nil, fmt.Errorf("nifti: non-positive dims %d×%d×%d", nx, ny, nz)
	}
	// #2 只支持 3D:4D(fMRI/DWI 的时间/通道维)当前会被静默截成第一卷、后续时间点无声
	// 丢失。明确拒绝任何尾随维度 > 1 的文件并指路拆分(dim[d] 在偏移 40+d*2)。
	for d := 4; d <= ndim && d <= 7; d++ {
		if n := int(rd16(40 + d*2)); n > 1 {
			return nil, fmt.Errorf("nifti: 只支持 3D 体数据,该文件是 %dD(dim[%d]=%d);请先按时间点/通道拆成多个 3D 文件再导入", ndim, d, n)
		}
	}
	datatype := rd16(70)
	dt, ok := niftiDatatypeNames[datatype]
	if !ok {
		return nil, fmt.Errorf("nifti: unsupported datatype code %d", datatype)
	}
	// #7 资源护栏:切片数与未压缩体素字节都设上限(挡 1×1×32767 之类资源风暴 / 巨卷 OOM)。
	if nz > maxVolumeSlices {
		return nil, fmt.Errorf("nifti: 切片数 %d 超过上限 %d(疑似资源攻击或非常规体积,请拆分)", nz, maxVolumeSlices)
	}
	if vb := int64(nx) * int64(ny) * int64(nz) * int64(dt.bytes); vb > maxVolumeBytes {
		return nil, fmt.Errorf("nifti: 体素数据 %.1f GB 超过上限 %d GB", float64(vb)/(1<<30), maxVolumeBytes>>30)
	}

	sclSlope, sclInter := rdf(112), rdf(116)
	if math.IsNaN(sclSlope) || math.IsInf(sclSlope, 0) || math.IsNaN(sclInter) || math.IsInf(sclInter, 0) {
		return nil, fmt.Errorf("nifti: non-finite scl_slope/scl_inter (%g/%g)", sclSlope, sclInter)
	}
	if sclSlope == 0 {
		// #4 slope 0 = "no scaling":inter 也必须一并忽略,否则整卷偏移 inter(常见 CT 的
		// -1024)且不报错——强度、CT 推断、窗位、PNG 映射全被带偏。
		sclSlope, sclInter = 1, 0
	}
	// #6 vox_offset 未校验会让下面 raw[voxOffset:] 越界 panic(或 <352 时把 header 当体素)。
	voxOffsetF := rdf(108)
	if math.IsNaN(voxOffsetF) || math.IsInf(voxOffsetF, 0) || voxOffsetF != math.Trunc(voxOffsetF) ||
		voxOffsetF < 352 || voxOffsetF > float64(len(raw)) {
		return nil, fmt.Errorf("nifti: bad vox_offset %g(须是 [352, 文件长度] 内的整数)", voxOffsetF)
	}
	voxOffset := int(voxOffsetF)

	meta := &VolumeMeta{
		Dims:     [3]int{nx, ny, nz},
		Datatype: dt.name,
		SclSlope: sclSlope,
		SclInter: sclInter,
	}

	// Affine: prefer sform (srow_*), fall back to qform (quaternion). Both are
	// voxel→world in RAS+ (NIfTI convention).
	sformCode := rd16(254)
	qformCode := rd16(252)
	var aff [3][4]float64
	switch {
	case sformCode > 0:
		for r := 0; r < 3; r++ {
			base := 280 + r*16
			for c := 0; c < 4; c++ {
				aff[r][c] = rdf(base + c*4)
			}
		}
	case qformCode > 0:
		aff = qformAffine(raw, rdf)
	default:
		// No orientation info: fall back to pixdim scaling at the origin. This
		// is a degenerate volume; direction is identity, which we flag as such.
		px, py, pz := rdf(80), rdf(84), rdf(88)
		aff = [3][4]float64{{px, 0, 0, 0}, {0, py, 0, 0}, {0, 0, pz, 0}}
	}
	// Preserve the raw orientation block verbatim (see NIfTIOrientation docs).
	meta.Orient = NIfTIOrientation{
		SformCode: int(sformCode),
		QformCode: int(qformCode),
		Quatern:   [3]float64{rdf(256), rdf(260), rdf(264)},
		QOffset:   [3]float64{rdf(268), rdf(272), rdf(276)},
		QFac:      rdf(76),
		Pixdim:    [3]float64{rdf(80), rdf(84), rdf(88)}, // #5 保存 pixdim[1..3] 供导出忠实复原 qform
	}
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			meta.Orient.Srow[r*4+c] = rdf(280 + r*16 + c*4)
		}
	}
	if meta.Orient.QFac == 0 {
		meta.Orient.QFac = 1 // 0 is invalid; the spec's neutral value is 1
	}

	fillGeometryFromAffine(meta, aff)

	// #3 空间单位:NIfTI 用 xyzt_units(byte 123)低 3 位声明单位——1=米 2=毫米 3=微米。
	// 以前从不读、一律当毫米 → 声明米/微米的文件 spacing/测量/体积最多错 1000×且不报错。
	// 换算到 mm(direction 是单位向量不受影响;Orient.Srow 原样保留供导出、不动)。单位未知
	// (xyzt_units=0)时按 mm 兜底并置 SpacingUnitKnown=false,让前端能禁用/标注物理测量。
	if f, ok := map[byte]float64{1: 1000, 2: 1, 3: 0.001}[raw[123]&0x07]; ok {
		meta.SpacingUnitKnown = true
		if f != 1 {
			for i := range meta.Spacing {
				meta.Spacing[i] *= f
				meta.Origin[i] *= f
			}
		}
	}

	// Intensity scan for default windowing (real values, post scl).
	lo, hi, err := scanIntensity(raw[voxOffset:], datatype, dt.bytes, nx*ny*nz, sclSlope, sclInter)
	if err != nil {
		return nil, err
	}
	meta.IntensityLo, meta.IntensityHi = lo, hi
	meta.Modality = inferModality(dt.name, lo, hi)
	meta.Windows = defaultWindows(meta.Modality, lo, hi)
	return meta, nil
}

// fillGeometryFromAffine decomposes the voxel→world affine into spacing (column
// norms), unit direction columns, origin (translation), and handedness.
func fillGeometryFromAffine(m *VolumeMeta, aff [3][4]float64) {
	for c := 0; c < 3; c++ {
		norm := math.Sqrt(aff[0][c]*aff[0][c] + aff[1][c]*aff[1][c] + aff[2][c]*aff[2][c])
		m.Spacing[c] = norm
		if norm == 0 {
			norm = 1
		}
		for r := 0; r < 3; r++ {
			m.Direction[r*3+c] = aff[r][c] / norm
		}
	}
	m.Origin = [3]float64{aff[0][3], aff[1][3], aff[2][3]}
	// det of the 3×3 direction → handedness.
	d := m.Direction
	det := d[0]*(d[4]*d[8]-d[5]*d[7]) - d[1]*(d[3]*d[8]-d[5]*d[6]) + d[2]*(d[3]*d[7]-d[4]*d[6])
	if det < 0 {
		m.Handedness = -1
	} else {
		m.Handedness = 1
	}
}

// qformAffine reconstructs the voxel→world affine from the NIfTI qform
// quaternion (used when sform is absent). Standard NIfTI-1 nifti_quatern_to_mat.
func qformAffine(raw []byte, rdf func(int) float64) [3][4]float64 {
	b, c, d := rdf(256), rdf(260), rdf(264)
	a := 1.0 - (b*b + c*c + d*d)
	if a < 1e-7 {
		// Normalize the (b,c,d) part when a would be imaginary.
		n := math.Sqrt(b*b + c*c + d*d)
		b, c, d = b/n, c/n, d/n
		a = 0
	} else {
		a = math.Sqrt(a)
	}
	qx, qy, qz := rdf(268), rdf(272), rdf(276)
	dx, dy, dz := rdf(80), rdf(84), rdf(88) // pixdim[1],[2],[3] (voxel sizes)
	qfac := rdf(76)                         // pixdim[0]: +1 or -1 (handedness fix)
	if qfac == 0 {
		qfac = 1
	}
	// Rotation matrix from quaternion.
	R := [3][3]float64{
		{a*a + b*b - c*c - d*d, 2*(b*c - a*d), 2*(b*d + a*c)},
		{2*(b*c + a*d), a*a + c*c - b*b - d*d, 2*(c*d - a*b)},
		{2*(b*d - a*c), 2*(c*d + a*b), a*a + d*d - b*b - c*c},
	}
	sp := [3]float64{dx, dy, dz * qfac}
	var aff [3][4]float64
	for r := 0; r < 3; r++ {
		aff[r][0] = R[r][0] * sp[0]
		aff[r][1] = R[r][1] * sp[1]
		aff[r][2] = R[r][2] * sp[2]
	}
	aff[0][3], aff[1][3], aff[2][3] = qx, qy, qz
	return aff
}

// scanIntensity walks the voxel data once, returning real-value (post scl) min
// and max. It samples with a stride for large volumes (the window default only
// needs the distribution's shape, not every voxel).
func scanIntensity(data []byte, datatype int16, byteW, n int, slope, inter float64) (float64, float64, error) {
	need := n * byteW
	if len(data) < need {
		return 0, 0, fmt.Errorf("nifti: voxel data short (have %d need %d)", len(data), need)
	}
	stride := 1
	if n > 4_000_000 {
		stride = n / 4_000_000 // cap the scan at ~4M samples
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	readOne := func(i int) float64 {
		off := i * byteW
		switch datatype {
		case 2: // uint8
			return float64(data[off])
		case 256: // int8
			return float64(int8(data[off]))
		case 4: // int16
			return float64(int16(binary.LittleEndian.Uint16(data[off:])))
		case 512: // uint16
			return float64(binary.LittleEndian.Uint16(data[off:]))
		case 8: // int32
			return float64(int32(binary.LittleEndian.Uint32(data[off:])))
		case 16: // float32
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(data[off:])))
		case 64: // float64
			return math.Float64frombits(binary.LittleEndian.Uint64(data[off:]))
		}
		return 0
	}
	for i := 0; i < n; i += stride {
		v := readOne(i)*slope + inter
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if math.IsInf(lo, 1) {
		return 0, 0, fmt.Errorf("nifti: empty intensity scan")
	}
	return lo, hi, nil
}

// inferModality guesses ct vs mri from datatype and intensity range. CT is
// int16 in Hounsfield units (negative values for air/lung, ~-1000..+3000); MRI
// is typically unsigned with a non-negative range and no absolute scale.
func inferModality(dtName string, lo, hi float64) string {
	if dtName == "int16" && lo < -300 && hi > 200 {
		return "ct" // HU signature: air/lung negative, bone strongly positive
	}
	if (dtName == "uint16" || dtName == "uint8") && lo >= 0 {
		return "mri"
	}
	return "unknown"
}

// defaultWindows returns display presets. CT gets the standard radiology set
// (HU-based); MRI/unknown gets a single full-range window derived from the
// volume's own intensities.
func defaultWindows(modality string, lo, hi float64) []Window {
	if modality == "ct" {
		return []Window{
			{Name: "soft_tissue", Width: 400, Center: 40},
			{Name: "lung", Width: 1500, Center: -600},
			{Name: "bone", Width: 1800, Center: 400},
			{Name: "brain", Width: 80, Center: 40},
		}
	}
	// Full-range window centered on the intensity midpoint.
	width := hi - lo
	if width <= 0 {
		width = 1
	}
	return []Window{{Name: "full_range", Width: width, Center: lo + width/2}}
}
