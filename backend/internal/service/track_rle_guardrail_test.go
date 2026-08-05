package service

// 稠密掩膜护栏(00《稠密几何存储契约》规则 1)的语义锁。
//
// voxel_mask 站在"稀疏内联"这一侧,但它会滑向稠密。撞线时必须**明确拒绝并
// 指路**(改走 voxel_label 外置工作流),而不是:
//   · 静默转换 —— 他以为存下的是逐切片 RLE,实际变成了另一种东西;
//   · 干巴巴地报个错 —— 他会以为自己画错了,擦掉重画一遍,再撞一次。
// 所以这里连**文案里必须出现 voxel_label** 都断言了。

import (
	"strings"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

func kfWithRLE(frame int, countsLen int) paymodel.Keyframe {
	return paymodel.Keyframe{
		Frame: frame,
		TsMs:  float64(frame),
		Bbox:  []float64{0, 0, 10, 10},
		RLE:   &paymodel.MaskRLE{Size: [2]int{10, 10}, Counts: strings.Repeat("a", countsLen)},
	}
}

func newLimitSvc() *TrackService {
	return &TrackService{limits: DefaultTrackLimits()}
}

// 单关键帧 RLE 超 64KB → 拒绝,且文案指路 voxel_label。
// 变异验证:把 MaxKeyframeRLEBytes 的判断去掉,本测试必须红。
func TestGuardrail_SingleKeyframeRLETooBig(t *testing.T) {
	svc := newLimitSvc()
	req := TrackUpsertRequest{Keyframes: []paymodel.Keyframe{kfWithRLE(7, MaxKeyframeRLEBytes+1)}}
	err := svc.validate(&req)
	if err == nil {
		t.Fatal("单帧掩膜超限必须被拒")
	}
	if !strings.Contains(err.Error(), "voxel_label") {
		t.Fatalf("拒绝文案必须指路 voxel_label 外置工作流,got: %v", err)
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("拒绝文案应指明是哪个关键帧,got: %v", err)
	}
}

// #13:kind 与几何字段必须一致——kind=polygon 却只带 bbox(或反之)会让页面按几何存在性
// 照样显示、导出按 kind 静默跳过。写入处就拒。变异:删掉 validate 里那段 kind↔几何 switch,
// 前两条子断言必红。
func TestGuardrail_KindGeometryMismatchRejected(t *testing.T) {
	svc := newLimitSvc()
	polyWithBbox := TrackUpsertRequest{Kind: paymodel.TrackKindPolygon,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{1, 2, 3, 4}}}}
	if err := svc.validate(&polyWithBbox); err == nil {
		t.Fatal("kind=polygon 只带 bbox 必须被拒(否则导出静默丢)")
	}
	bboxWithPoints := TrackUpsertRequest{Kind: paymodel.TrackKindBBox,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Points: []float64{0, 0, 10, 0, 10, 10}}}}
	if err := svc.validate(&bboxWithPoints); err == nil {
		t.Fatal("kind=bbox 只带 points 必须被拒")
	}
	// 一致的两种应放行,不误伤。
	okPoly := TrackUpsertRequest{Kind: paymodel.TrackKindPolygon,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Points: []float64{0, 0, 10, 0, 10, 10}}}}
	if err := svc.validate(&okPoly); err != nil {
		t.Fatalf("kind=polygon + points 应放行, got %v", err)
	}
	okBbox := TrackUpsertRequest{Kind: paymodel.TrackKindBBox,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{1, 2, 3, 4}}}}
	if err := svc.validate(&okBbox); err != nil {
		t.Fatalf("kind=bbox + bbox 应放行, got %v", err)
	}
}

// #10 voxel_mask 几何专项校验:非 outside 帧必须有合法 bbox-local RLE(尺寸与 bbox 一致、
// 能解码、非全零),否则导出会跳过/截断/裁掉体外部分甚至产全零 segment,validate 却放行。
// 变异:删掉 validate 里的 voxel_mask case,坏几何断言变红。
func TestGuardrail_VoxelMaskGeometryValidated(t *testing.T) {
	svc := newLimitSvc()
	rle, err := EncodeCOCORLE([]byte{1, 1, 1, 1}, 2, 2, false) // 2×2 全 1
	if err != nil {
		t.Fatal(err)
	}
	ok := TrackUpsertRequest{Kind: paymodel.TrackKindVoxelMask,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{10, 20, 2, 2}, RLE: rle}}}
	if err := svc.validate(&ok); err != nil {
		t.Fatalf("合法 voxel_mask(RLE 尺寸=bbox)应放行, got %v", err)
	}
	bad := TrackUpsertRequest{Kind: paymodel.TrackKindVoxelMask,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{10, 20, 5, 5}, RLE: rle}}}
	if err := svc.validate(&bad); err == nil {
		t.Fatal("voxel_mask RLE 尺寸(2×2)与 bbox(5×5)不符必须被拒")
	}
	noRLE := TrackUpsertRequest{Kind: paymodel.TrackKindVoxelMask,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{10, 20, 2, 2}}}}
	if err := svc.validate(&noRLE); err == nil {
		t.Fatal("voxel_mask 非 outside 帧缺 RLE 必须被拒")
	}
}

// 刚好在线上(=上限)应当放行:护栏是">"不是">="——边界值不该无故拒人。
func TestGuardrail_ExactlyAtLimitPasses(t *testing.T) {
	svc := newLimitSvc()
	req := TrackUpsertRequest{Keyframes: []paymodel.Keyframe{kfWithRLE(1, MaxKeyframeRLEBytes)}}
	if err := svc.validate(&req); err != nil {
		t.Fatalf("恰好等于上限应当放行, got %v", err)
	}
}

// 整条 track 几何总量超 4MB → 拒绝(每帧都不超,但加起来超)。
func TestGuardrail_WholeTrackGeomTooBig(t *testing.T) {
	svc := newLimitSvc()
	// 每帧 32KB(不触发单帧护栏),128 帧 = 4MB,再多一帧就过线。
	per := 32 * 1024
	n := MaxTrackGeomBytes/per + 1
	kfs := make([]paymodel.Keyframe, 0, n)
	for i := 0; i < n; i++ {
		kfs = append(kfs, kfWithRLE(i, per))
	}
	err := svc.validate(&TrackUpsertRequest{Keyframes: kfs})
	if err == nil {
		t.Fatal("整条 track 几何总量超限必须被拒")
	}
	if !strings.Contains(err.Error(), "voxel_label") {
		t.Fatalf("拒绝文案必须指路 voxel_label,got: %v", err)
	}
}

// C0.3 实测量级必须安全通过:真实脑分割单切片最大 3.4KB、209 片、整卷 369KB。
// 这条把"护栏不会误伤真实工作量"钉住——护栏定太紧比没护栏更糟(天天误报)。
func TestGuardrail_RealWorldScaleFromC03Passes(t *testing.T) {
	svc := newLimitSvc()
	kfs := make([]paymodel.Keyframe, 0, 209)
	for i := 0; i < 209; i++ {
		kfs = append(kfs, kfWithRLE(i, 3446)) // C0.3 实测的单切片最大值
	}
	if err := svc.validate(&TrackUpsertRequest{Keyframes: kfs}); err != nil {
		t.Fatalf("C0.3 实测量级(真实脑分割)不该撞护栏, got %v", err)
	}
}

// kfWithCells 造一个 cells 关键帧:nCells 个实例,每个带 rleLen 字节的 bbox-local RLE。
func kfWithCells(frame, nCells, rleLen int) paymodel.Keyframe {
	insts := make([]paymodel.CellInstance, nCells)
	for i := range insts {
		insts[i] = paymodel.CellInstance{
			Bbox:  []float64{0, 0, 8, 8},
			RLE:   &paymodel.MaskRLE{Size: [2]int{8, 8}, Counts: strings.Repeat("a", rleLen)},
			Class: "tumor",
			Score: 0.9,
		}
	}
	return paymodel.Keyframe{Frame: frame, TsMs: float64(frame), Instances: insts}
}

// C-Q4 实测量级必须安全通过:一个 ROI ~418 个细胞、每核 bbox-local RLE ~42B ≈ 17KB,
// 单关键帧。把"护栏不误伤真实 ROI"钉住(护栏定太紧比没护栏更糟)。
func TestGuardrail_CellsRealWorldROIPasses(t *testing.T) {
	svc := newLimitSvc()
	req := TrackUpsertRequest{Keyframes: []paymodel.Keyframe{kfWithCells(0, 418, 42)}}
	if err := svc.validate(&req); err != nil {
		t.Fatalf("C-Q4 实测量级(单 ROI ~418 细胞 ~17KB)不该撞护栏, got %v", err)
	}
}

// 把整片细胞(> MaxCellsPerTrack)塞进一条轨迹 → 拒绝,文案指路 ROI 粒度、
// 且**不能**抄 voxel_label 的话(cells 不是体标签,那会把人导向无关工作流)。
// 变异验证:去掉 validate 里 totalCells 上限那支,本测试必须红。
func TestGuardrail_CellsWholeSlideRejected(t *testing.T) {
	svc := newLimitSvc()
	// 每个实例只 4B RLE(远不触发字节护栏),靠**数量**触发——因为整片 6.7万×42B≈2.8MB
	// 仍在 4MB 内,4MB 字节护栏兜不住,必须靠细胞数上限。
	req := TrackUpsertRequest{Keyframes: []paymodel.Keyframe{kfWithCells(0, MaxCellsPerTrack+1, 4)}}
	err := svc.validate(&req)
	if err == nil {
		t.Fatal("整片细胞塞进一条轨迹必须被拒(超单轨迹细胞数上限)")
	}
	if !strings.Contains(err.Error(), "ROI") {
		t.Fatalf("cells 护栏文案必须指路 ROI 粒度, got: %v", err)
	}
	if strings.Contains(err.Error(), "voxel_label") {
		t.Fatalf("cells 护栏文案不该抄 voxel_label(把人导向无关工作流), got: %v", err)
	}
}

// 恰好等于细胞数上限应放行(护栏是 ">" 不是 ">=")。
func TestGuardrail_CellsExactlyAtCountLimitPasses(t *testing.T) {
	svc := newLimitSvc()
	req := TrackUpsertRequest{Keyframes: []paymodel.Keyframe{kfWithCells(0, MaxCellsPerTrack, 4)}}
	if err := svc.validate(&req); err != nil {
		t.Fatalf("恰好等于细胞数上限应放行, got %v", err)
	}
}

// cells 的每实例 RLE 字节必须计入 4MB 整-track 护栏:100 个细胞、每个 50KB RLE = 5MB,
// 细胞数(100)远低于上限,只能靠字节护栏拦。变异验证:去掉 validate 里把
// inst.RLE.Counts 计入 totalGeom 那行,本测试必须红(整条 cells 静默绕过 4MB)。
func TestGuardrail_CellsInstanceBytesCountTowardTotal(t *testing.T) {
	svc := newLimitSvc()
	req := TrackUpsertRequest{Keyframes: []paymodel.Keyframe{kfWithCells(0, 100, 50*1024)}}
	if err := svc.validate(&req); err == nil {
		t.Fatal("cells 的 instance RLE 字节必须计入 4MB 总量护栏(否则整片细胞可静默绕过)")
	}
}

// 没有 RLE 的普通轨迹不受影响。
func TestGuardrail_SparseTracksUnaffected(t *testing.T) {
	svc := newLimitSvc()
	kfs := []paymodel.Keyframe{
		{Frame: 0, TsMs: 0, Bbox: []float64{0, 0, 10, 10}},
		{Frame: 5, TsMs: 5, Points: []float64{1, 2, 3, 4, 5, 6}},
	}
	if err := svc.validate(&TrackUpsertRequest{Keyframes: kfs}); err != nil {
		t.Fatalf("稀疏轨迹不该被稠密护栏影响, got %v", err)
	}
}
