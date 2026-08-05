package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	paymodel "text-annotation-platform/internal/model/payload"
)

// maxVolumeExportBytes 是导出峰值内存的可预估预算(#12):每段展一张整卷 dense byte mask
// (n) + 合并的 owner(4n) + labels(2n)。超过就 fail-closed 拒,别让合法任务拖死进程。
// 1.5GB 覆盖 512³ 十几段的正常情形,又挡住 512³×几十段 / 巨卷的失控展开。
const maxVolumeExportBytes = int64(3) << 29 // 1.5 GB

// C3.4 — export voxel_mask tracks as NIfTI label maps.
//
// # Two outputs, because one of them necessarily loses data
//
// A single label map is what you drop into 3D Slicer to see everything at once,
// but a voxel can only carry ONE label — and in medicine overlap is the normal
// case, not the exception ("tumour inside liver"). So we also offer one binary
// volume per segment (lossless), and whenever the single map has to drop
// voxels we say so explicitly in the manifest. Silently losing the overlap
// would violate the first rule of 00《稠密几何存储契约》: never lose human work.

// VolumeSegment is one voxel_mask track flattened for export.
type VolumeSegment struct {
	Label    string `json:"label"`
	Color    string `json:"color,omitempty"`
	TrackID  int    `json:"track_id"`
	Value    int    `json:"value"` // label value in the combined map (1-based)
	Voxels   int    `json:"voxels"`
	Slices   int    `json:"slices"`
	mask     []byte // dims-sized, 1 where the segment is present
}

// OverlapPair records that two segments claim the same voxels. `LostVoxels` is
// how many voxels of `Loser` are hidden in the single-label map.
type OverlapPair struct {
	A          string `json:"a"`
	B          string `json:"b"`
	Voxels     int    `json:"voxels"`
	Loser      string `json:"loser"`
	LostVoxels int    `json:"lost_voxels"`
}

// VolumeExportManifest is the labels.json shipped alongside the NIfTI.
type VolumeExportManifest struct {
	AssetID   uint            `json:"asset_id"`
	TaskID    uint            `json:"task_id"`
	Dims      [3]int          `json:"dims"`
	Spacing   [3]float64      `json:"spacing"`
	Segments  []VolumeSegment `json:"segments"`
	Overlaps  []OverlapPair   `json:"overlaps,omitempty"`
	// OverlapNote is human-readable and deliberately blunt. It ends up in the
	// zip and in a response header so nobody has to read JSON to learn that the
	// single-label file is lossy for their data.
	OverlapNote string `json:"overlap_note,omitempty"`
	Convention  string `json:"convention"`
}

// BuildVolumeSegments decodes voxel_mask tracks into dims-sized binary masks.
//
// Label values are assigned 1..N in track order (0 = background). Track order
// is by TrackID so the mapping is stable across exports — an unstable mapping
// would mean "label 2" means something different every time you export, which
// silently corrupts any downstream training set.
func BuildVolumeSegments(tracks []paymodel.Track, dims [3]int) ([]VolumeSegment, error) {
	nx, ny, nz := dims[0], dims[1], dims[2]
	if nx <= 0 || ny <= 0 || nz <= 0 {
		return nil, fmt.Errorf("volume export: bad dims %v", dims)
	}
	plane := nx * ny

	vm := make([]paymodel.Track, 0, len(tracks))
	for _, t := range tracks {
		if t.Kind == paymodel.TrackKindVoxelMask {
			vm = append(vm, t)
		}
	}
	sort.Slice(vm, func(i, j int) bool { return vm[i].TrackID < vm[j].TrackID })

	// #12 导出给每段展一张整卷 dense mask,合并时再加 owner(4n)+labels(2n)——512³×20 段
	// 就是 1GB+,能把请求拖死进程。合法任务即可触发。先按可预估预算挡住(fail-closed),
	// 超限返回明确错误指路(减少段数 / 分批导出)。真正的稀疏/流式导出是后续欠账。
	if n64 := int64(nx) * int64(ny) * int64(nz); n64*int64(len(vm)+6) > maxVolumeExportBytes {
		return nil, fmt.Errorf("导出预计占用 %.1f GB（%d 段 × %d 体素,每段整卷展开）,超过上限 %d GB:请减少段数或分批导出",
			float64(n64*int64(len(vm)+6))/(1<<30), len(vm), n64, maxVolumeExportBytes>>30)
	}

	out := make([]VolumeSegment, 0, len(vm))
	for i, t := range vm {
		mask := make([]byte, nx*ny*nz)
		voxels, slices := 0, 0
		for _, kf := range t.Keyframes {
			if kf.RLE == nil || kf.Outside {
				continue
			}
			z := kf.Frame
			if z < 0 || z >= nz {
				// A keyframe outside the volume means the label map would be
				// wrong in a way nothing downstream can detect. Refuse loudly.
				return nil, fmt.Errorf("volume export: track %d has keyframe at z=%d, volume has %d slices", t.TrackID, z, nz)
			}
			if len(kf.Bbox) < 4 {
				return nil, fmt.Errorf("volume export: track %d keyframe z=%d has RLE but no bbox (RLE is bbox-local)", t.TrackID, z)
			}
			sub, bh, bw, err := DecodeCOCORLE(kf.RLE)
			if err != nil {
				return nil, fmt.Errorf("volume export: track %d z=%d: %w", t.TrackID, z, err)
			}
			bx, by := int(kf.Bbox[0]), int(kf.Bbox[1])
			before := voxels
			for r := 0; r < bh; r++ {
				y := by + r
				if y < 0 || y >= ny {
					continue
				}
				for c := 0; c < bw; c++ {
					if sub[r*bw+c] == 0 {
						continue
					}
					x := bx + c
					if x < 0 || x >= nx {
						continue
					}
					idx := z*plane + y*nx + x
					if mask[idx] == 0 {
						mask[idx] = 1
						voxels++
					}
				}
			}
			if voxels > before {
				slices++
			}
		}
		out = append(out, VolumeSegment{
			Label:   t.Label,
			Color:   t.Color,
			TrackID: t.TrackID,
			Value:   i + 1,
			Voxels:  voxels,
			Slices:  slices,
			mask:    mask,
		})
	}
	return out, nil
}

// CombineLabelVolume flattens segments into a single label map and reports what
// that flattening cost.
//
// Later segments win on overlap (panel order = what the annotator sees on top).
// The returned overlaps say exactly which segment lost how many voxels, so the
// caller can tell the user instead of quietly shipping a lossy file.
func CombineLabelVolume(segs []VolumeSegment, dims [3]int) ([]uint16, []OverlapPair) {
	n := dims[0] * dims[1] * dims[2]
	labels := make([]uint16, n)
	// owner[i] is the index of the segment currently occupying voxel i, -1 none.
	owner := make([]int32, n)
	for i := range owner {
		owner[i] = -1
	}
	type key struct{ a, b int }
	counts := map[key]int{}

	for si, s := range segs {
		for i := 0; i < n; i++ {
			if s.mask[i] == 0 {
				continue
			}
			if prev := owner[i]; prev >= 0 {
				k := key{int(prev), si}
				counts[k]++
			}
			owner[i] = int32(si)
			labels[i] = uint16(s.Value)
		}
	}

	pairs := make([]OverlapPair, 0, len(counts))
	for k, cnt := range counts {
		pairs = append(pairs, OverlapPair{
			A: segs[k.a].Label, B: segs[k.b].Label,
			Voxels: cnt,
			// The earlier segment is the one overwritten (later wins).
			Loser: segs[k.a].Label, LostVoxels: cnt,
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Voxels != pairs[j].Voxels {
			return pairs[i].Voxels > pairs[j].Voxels
		}
		return pairs[i].A < pairs[j].A
	})
	return labels, pairs
}

// MaxLabelValue is the largest label value present (drives the on-disk dtype).
func MaxLabelValue(segs []VolumeSegment) int {
	m := 0
	for _, s := range segs {
		if s.Value > m {
			m = s.Value
		}
	}
	return m
}

// SegmentBinaryLabels returns a 0/1 label payload for one segment.
func SegmentBinaryLabels(s VolumeSegment) []uint16 {
	out := make([]uint16, len(s.mask))
	for i, v := range s.mask {
		if v != 0 {
			out[i] = 1
		}
	}
	return out
}

// BuildOverlapNote turns overlap pairs into a sentence a human will actually read.
func BuildOverlapNote(pairs []OverlapPair) string {
	if len(pairs) == 0 {
		return ""
	}
	total := 0
	for _, p := range pairs {
		total += p.LostVoxels
	}
	return fmt.Sprintf(
		"警告：%d 对分割存在重叠，单张标签图中共 %d 个体素被后一段覆盖（不可见）。"+
			"需要无损结果请用 ?split=1 取逐段二值文件。",
		len(pairs), total)
}

// WriteVolumeExportZip packs per-segment binary NIfTIs plus labels.json.
func WriteVolumeExportZip(segs []VolumeSegment, man VolumeExportManifest, dims [3]int, affine [3][4]float64, orient *NIfTIOrientation) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, s := range segs {
		gz, err := WriteNIfTIGz(NIfTIVolume{
			Dims: dims, Affine: affine, Labels: SegmentBinaryLabels(s), MaxLabel: 1,
			Descrip: "dg-seg " + s.Label,
			Orient:  orient, // 同上：逐段文件也必须与原图同朝向
		})
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("seg_%02d_%s.nii.gz", s.Value, safeFileLabel(s.Label))
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(gz); err != nil {
			return nil, err
		}
	}

	w, err := zw.Create("labels.json")
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// safeFileLabel keeps segment names usable as filenames without mangling CJK —
// annotators name segments 肝/肿瘤, and stripping those to "seg_01_" would make
// the zip unreadable. Only characters that actually break filesystems go.
func safeFileLabel(s string) string {
	if s == "" {
		return "unnamed"
	}
	bad := map[rune]bool{'/': true, '\\': true, ':': true, '*': true, '?': true,
		'"': true, '<': true, '>': true, '|': true, 0: true}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if bad[r] || r < 0x20 {
			out = append(out, '_')
			continue
		}
		out = append(out, r)
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return string(out)
}
