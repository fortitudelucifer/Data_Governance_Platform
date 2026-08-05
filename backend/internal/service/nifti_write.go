package service

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"math"
)

// NIfTI-1 label-map writer (C3.4). Mirrors ParseNIfTIMeta in volume_probe.go —
// the two must stay consistent: what we read back must be what we wrote.
//
// # Why this file is dangerous
//
// A label map whose geometry disagrees with the source image looks *completely
// normal* on its own. It only shows up when a radiologist overlays it on the
// original and the tumour sits in the wrong place — or, far worse, on the wrong
// SIDE. Up/down errors are obvious at a glance; **left/right errors are not**,
// and reporting a left-sided lesion as right-sided is the kind of mistake that
// reaches a patient. So this writer:
//
//   - copies the source affine verbatim (direction × spacing, plus origin)
//     rather than re-deriving orientation from anything;
//   - copies the source's orientation block through verbatim (see below);
//   - is verified against nibabel, not against our own parser (a writer and
//     reader that share a bug agree with each other perfectly).
//
// # Copy the source's orientation through; don't arbitrate
//
// NIfTI can carry orientation twice: sform (an explicit 3×4 matrix) and qform
// (a quaternion). Real files exist where the two DISAGREE — the MNI152 template
// is one: sform origin (-75.8,-110.8,-71.8) vs qform origin (0,0,0). And the
// ecosystem is split on which wins: **nibabel/FSL prefer sform, ITK/3D Slicer/
// ANTs prefer qform**.
//
// An earlier version of this writer emitted sform only, reasoning that one
// representation cannot contradict itself. That reasoning solved the wrong
// problem. Picking a single affine means silently taking a side, and a label
// map written that way lands **75mm off the source image** in whichever tool
// made the other choice — both files open fine, they just don't overlay.
// 3D Slicer acceptance caught exactly this; nibabel had not, because nibabel
// makes the same choice our parser does (a reader that shares your bias agrees
// with you no matter what).
//
// So: when the caller supplies the source's orientation block we copy BOTH
// representations through byte-for-byte. Whatever rule a downstream tool
// applies, it applies the same rule to the image and to the label map, so the
// two can never drift apart. We only synthesise an sform-only header when the
// source orientation is unavailable (pre-C3.4 derivatives).

const (
	niftiHeaderSize = 348
	niftiVoxOffset  = 352 // header + 4-byte extension flag
	niftiDTUint8    = 2
	niftiDTInt16    = 4
)

// NIfTIVolume is the minimal description needed to write a label map.
type NIfTIVolume struct {
	Dims [3]int
	// Affine is the voxel→world matrix in RAS+ (NIfTI convention), same layout
	// as the source's srow_*: affine[r][c] for c<3 is direction*spacing,
	// affine[r][3] is the world origin.
	Affine [3][4]float64
	// Labels is the voxel payload in Fortran order (i fastest, then j, then k) —
	// the same order NIfTI stores and the same order our mask volumes use.
	Labels []uint16
	// MaxLabel decides the on-disk datatype: ≤255 → uint8 (4× smaller and what
	// every tool expects for a segmentation); above that → int16.
	MaxLabel int
	// Descrip goes in the 80-byte descrip field. Informational only.
	Descrip string
	// Orient is the source header's orientation block, copied through verbatim.
	// When nil (or lacking any code) we fall back to writing sform only, derived
	// from Affine — correct in isolation, but it takes a side on ambiguous
	// sources. Prefer passing the real thing.
	Orient *NIfTIOrientation
	// LabelMapJSON,若非空,作为 NIfTI 扩展(ecode 6)嵌进文件——让 label→组织名 的映射
	// **随文件走**,而不是只在 HTTP 响应头里(文件被转发/离线打开后,"2=肿瘤"就没了,#21)。
	// 扩展写在 header 之后、体素之前,vox_offset 随之后移;不懂扩展的工具照常按 vox_offset
	// 读体素,几何/像素零影响(标准 NIfTI 扩展机制)。
	LabelMapJSON []byte
}

// AffineFromMeta rebuilds the voxel→world matrix from a VolumeMeta.
//
// VolumeMeta stores the affine decomposed (unit direction columns + spacing +
// origin); this recomposes it. The decomposition is exact — spacing is the
// column norm and direction the normalised column — so recomposition returns
// the original matrix up to float rounding. A round-trip test pins that down;
// if it ever stops holding, export geometry is wrong and nothing else will say so.
func AffineFromMeta(m *VolumeMeta) [3][4]float64 {
	var a [3][4]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			a[r][c] = m.Direction[r*3+c] * m.Spacing[c]
		}
		a[r][3] = m.Origin[r]
	}
	return a
}

// WriteNIfTIGz serialises a label volume as a gzip-compressed .nii.
func WriteNIfTIGz(v NIfTIVolume) ([]byte, error) {
	raw, err := writeNIfTI(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("nifti: gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("nifti: gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

func writeNIfTI(v NIfTIVolume) ([]byte, error) {
	nx, ny, nz := v.Dims[0], v.Dims[1], v.Dims[2]
	if nx <= 0 || ny <= 0 || nz <= 0 {
		return nil, fmt.Errorf("nifti: non-positive dims %dx%dx%d", nx, ny, nz)
	}
	want := nx * ny * nz
	if len(v.Labels) != want {
		return nil, fmt.Errorf("nifti: label count %d != %d voxels", len(v.Labels), want)
	}

	dt := int16(niftiDTUint8)
	bitpix := int16(8)
	if v.MaxLabel > 255 {
		dt, bitpix = niftiDTInt16, 16
	}

	hdr := make([]byte, niftiVoxOffset)
	le := binary.LittleEndian
	putI16 := func(off int, x int16) { le.PutUint16(hdr[off:], uint16(x)) }
	putF32 := func(off int, x float64) { le.PutUint32(hdr[off:], math.Float32bits(float32(x))) }

	le.PutUint32(hdr[0:], niftiHeaderSize) // sizeof_hdr

	// dim[0..7]: dim[0] is the count of used dimensions.
	putI16(40, 3)
	putI16(42, int16(nx))
	putI16(44, int16(ny))
	putI16(46, int16(nz))
	for i := 4; i <= 7; i++ { // dim[4..7] must be 1, not 0 — some readers multiply them
		putI16(40+i*2, 1)
	}

	putI16(70, dt)     // datatype
	putI16(72, bitpix) // bitpix

	// pixdim[0] = qfac (qform's handedness flag). Neutral default; overwritten
	// below with the source's value when an orientation block is supplied.
	// 0 is invalid in some readers, so never leave it zero.
	putF32(76, 1)
	// pixdim[1..3] = voxel size = the norm of each affine column. Readers that
	// ignore sform fall back to these, so they must agree with the matrix.
	//
	// ⚠️ pixdim[0] is at offset 76 and pixdim[1] at **80** — so the loop writes
	// 80/84/88, not 84/88/92. The off-by-one version left pixdim[1] at zero, and
	// our own parser never noticed because it reads sform and ignores pixdim
	// entirely: writer and reader agreed perfectly while the file was malformed.
	// nibabel caught it on the first run ("pixdim[1,2,3] should be non-zero").
	// That is exactly why the authority for this format is a third-party reader.
	for c := 0; c < 3; c++ {
		n := math.Sqrt(v.Affine[0][c]*v.Affine[0][c] + v.Affine[1][c]*v.Affine[1][c] + v.Affine[2][c]*v.Affine[2][c])
		putF32(80+c*4, n)
	}

	// #21 label→name 映射作为 NIfTI 扩展嵌入(header 后、体素前;esize 须为 16 的倍数)。
	var ext []byte
	if len(v.LabelMapJSON) > 0 {
		esize := 8 + len(v.LabelMapJSON)
		if r := esize % 16; r != 0 {
			esize += 16 - r
		}
		ext = make([]byte, esize)
		le.PutUint32(ext[0:], uint32(esize))
		le.PutUint32(ext[4:], 6) // ecode 6 = 纯文本/注释(容纳 JSON)
		copy(ext[8:], v.LabelMapJSON)
		hdr[348] = 1 // extension flag:有扩展
	}
	putF32(108, float64(niftiVoxOffset+len(ext))) // vox_offset(扩展把体素往后推)
	putF32(112, 1)                                // scl_slope — labels are labels, never scaled
	putF32(116, 0)              // scl_inter
	putF32(124, float64(v.MaxLabel))
	putF32(128, 0) // cal_min

	copy(hdr[148:228], v.Descrip) // descrip[80]

	if v.Orient != nil && v.Orient.HasOrientation() {
		// Verbatim copy — including the case where the two disagree. Preserving
		// the source's ambiguity is right; resolving it for the user is not.
		putI16(252, int16(v.Orient.QformCode))
		putI16(254, int16(v.Orient.SformCode))
		putF32(256, v.Orient.Quatern[0])
		putF32(260, v.Orient.Quatern[1])
		putF32(264, v.Orient.Quatern[2])
		putF32(268, v.Orient.QOffset[0])
		putF32(272, v.Orient.QOffset[1])
		putF32(276, v.Orient.QOffset[2])
		if v.Orient.QFac != 0 {
			putF32(76, v.Orient.QFac) // pixdim[0]; overrides the neutral 1 above
		}
		for r := 0; r < 3; r++ {
			for c := 0; c < 4; c++ {
				putF32(280+r*16+c*4, v.Orient.Srow[r*4+c])
			}
		}
	} else {
		// Fallback: sform only, from Affine. qform_code=0 means "no qform",
		// which is legal and unambiguous for a file we generated ourselves.
		putI16(252, 0)
		putI16(254, 1)
		for r := 0; r < 3; r++ {
			for c := 0; c < 4; c++ {
				putF32(280+r*16+c*4, v.Affine[r][c])
			}
		}
	}

	copy(hdr[344:348], []byte("n+1\x00"))
	// Bytes 348..351 are the extension flag; all-zero means "no extensions".

	out := make([]byte, 0, niftiVoxOffset+len(ext)+want*int(bitpix)/8)
	out = append(out, hdr...)
	out = append(out, ext...) // #21 扩展(label map)在 header 之后、体素之前
	if bitpix == 8 {
		for _, l := range v.Labels {
			out = append(out, byte(l))
		}
	} else {
		b := make([]byte, 2)
		for _, l := range v.Labels {
			le.PutUint16(b, l)
			out = append(out, b...)
		}
	}
	return out, nil
}
