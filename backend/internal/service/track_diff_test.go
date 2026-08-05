package service

import (
	"reflect"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

func kf(frame int, x float64) paymodel.Keyframe {
	return paymodel.Keyframe{Frame: frame, TsMs: float64(frame) * 1000 / 30, Bbox: []float64{x, 0, 10, 10}}
}

func trk(id int, label string, kfs ...paymodel.Keyframe) paymodel.Track {
	return paymodel.Track{TrackID: id, Label: label, Kind: "bbox", Color: "#f00", IsActive: true, Keyframes: kfs}
}

func TestDiffTrackRounds_NoChange(t *testing.T) {
	r := []paymodel.Track{trk(1, "car", kf(0, 0), kf(10, 100))}
	d := DiffTrackRounds(r, r, 1, 2)
	if !d.Empty() {
		t.Fatalf("相同两轮应无差异, got %+v", d)
	}
	if d.FromRound != 1 || d.ToRound != 2 {
		t.Errorf("轮次未透传: %+v", d)
	}
}

func TestDiffTrackRounds_AddedRemoved(t *testing.T) {
	prev := []paymodel.Track{trk(1, "car", kf(0, 0)), trk(2, "person", kf(0, 0))}
	cur := []paymodel.Track{trk(1, "car", kf(0, 0)), trk(3, "cow", kf(0, 0))}
	d := DiffTrackRounds(prev, cur, 1, 2)

	if !reflect.DeepEqual(d.Added, []int{3}) {
		t.Errorf("Added = %v, want [3]", d.Added)
	}
	if !reflect.DeepEqual(d.Removed, []int{2}) {
		t.Errorf("Removed = %v, want [2]", d.Removed)
	}
	if len(d.Changed) != 0 {
		t.Errorf("track 1 未动，不该出现在 Changed: %+v", d.Changed)
	}
}

// 归档的 track（采纳/删除会留下 is_active=false）不属于「标注员提交的内容」。
func TestDiffTrackRounds_IgnoresArchived(t *testing.T) {
	archived := trk(2, "person", kf(0, 0))
	archived.IsActive = false
	prev := []paymodel.Track{trk(1, "car", kf(0, 0))}
	cur := []paymodel.Track{trk(1, "car", kf(0, 0)), archived}
	if d := DiffTrackRounds(prev, cur, 1, 2); !d.Empty() {
		t.Fatalf("归档 track 不应算新增: %+v", d)
	}
}

func TestDiffTrackRounds_KeyframeAddedRemovedMoved(t *testing.T) {
	prev := []paymodel.Track{trk(1, "car", kf(0, 0), kf(10, 100), kf(20, 200))}
	cur := []paymodel.Track{trk(1, "car",
		kf(0, 0),    // 未动
		kf(10, 150), // 移动了
		kf(30, 300), // 新增
	)} // frame 20 被删

	d := DiffTrackRounds(prev, cur, 1, 2)
	if len(d.Changed) != 1 {
		t.Fatalf("expected 1 changed track, got %+v", d.Changed)
	}
	c := d.Changed[0]
	if !reflect.DeepEqual(c.Keyframes.Added, []int{30}) {
		t.Errorf("Added = %v, want [30]", c.Keyframes.Added)
	}
	if !reflect.DeepEqual(c.Keyframes.Removed, []int{20}) {
		t.Errorf("Removed = %v, want [20]", c.Keyframes.Removed)
	}
	if !reflect.DeepEqual(c.Keyframes.Moved, []int{10}) {
		t.Errorf("Moved = %v, want [10]", c.Keyframes.Moved)
	}
	if c.FirstFrame == nil || *c.FirstFrame != 10 {
		t.Errorf("FirstFrame 应指向最早被改动的帧 10, got %v", c.FirstFrame)
	}
	if len(c.Fields) != 0 {
		t.Errorf("只动了关键帧，Fields 应为空: %v", c.Fields)
	}
}

// float 往返 JSON 会有微小误差；亚像素抖动不是标注员的编辑。
func TestDiffTrackRounds_SubPixelJitterIsNotAChange(t *testing.T) {
	prev := []paymodel.Track{trk(1, "car", kf(0, 100))}
	cur := []paymodel.Track{trk(1, "car", kf(0, 100.0000001))}
	if d := DiffTrackRounds(prev, cur, 1, 2); !d.Empty() {
		t.Fatalf("亚像素抖动不应报告为改动: %+v", d)
	}
	// 但真实的拖动要报出来
	cur2 := []paymodel.Track{trk(1, "car", kf(0, 103))}
	if d := DiffTrackRounds(prev, cur2, 1, 2); d.Empty() {
		t.Fatal("3px 的移动必须被报告")
	}
}

func TestDiffTrackRounds_FieldChanges(t *testing.T) {
	prev := []paymodel.Track{trk(1, "car", kf(0, 0))}
	cur := []paymodel.Track{trk(1, "truck", kf(0, 0))}
	cur[0].Color = "#00f"

	d := DiffTrackRounds(prev, cur, 1, 2)
	if len(d.Changed) != 1 {
		t.Fatalf("expected 1 changed, got %+v", d.Changed)
	}
	if !reflect.DeepEqual(d.Changed[0].Fields, []string{"label", "color"}) {
		t.Errorf("Fields = %v, want [label color]", d.Changed[0].Fields)
	}
	if d.Changed[0].Label != "truck" {
		t.Errorf("Label 应显示新一轮的值, got %q", d.Changed[0].Label)
	}
	if d.Changed[0].FirstFrame != nil {
		t.Errorf("没有关键帧改动时 FirstFrame 应为空, got %v", *d.Changed[0].FirstFrame)
	}
}

// outside/occluded 是逐帧状态，改了必须算 Moved（几何可能一模一样）。
func TestDiffTrackRounds_FlagChangeCountsAsMoved(t *testing.T) {
	prev := []paymodel.Track{trk(1, "car", kf(5, 0))}
	cur := []paymodel.Track{trk(1, "car", kf(5, 0))}
	cur[0].Keyframes[0].Outside = true

	d := DiffTrackRounds(prev, cur, 1, 2)
	if len(d.Changed) != 1 || !reflect.DeepEqual(d.Changed[0].Keyframes.Moved, []int{5}) {
		t.Fatalf("outside 翻转应算 Moved, got %+v", d.Changed)
	}
}

func TestDiffTrackRounds_AttrsCompareNumerically(t *testing.T) {
	prev := []paymodel.Track{trk(1, "car", kf(0, 0))}
	cur := []paymodel.Track{trk(1, "car", kf(0, 0))}
	prev[0].Attrs = map[string]interface{}{"count": 1}         // Go int
	cur[0].Attrs = map[string]interface{}{"count": float64(1)} // 反序列化回来是 float64
	if d := DiffTrackRounds(prev, cur, 1, 2); !d.Empty() {
		t.Fatalf("int 1 与 float64 1.0 应视为相等: %+v", d)
	}

	cur[0].Attrs = map[string]interface{}{"count": float64(2)}
	d := DiffTrackRounds(prev, cur, 1, 2)
	if len(d.Changed) != 1 || !reflect.DeepEqual(d.Changed[0].Fields, []string{"attrs"}) {
		t.Fatalf("attrs 真变了要报出来: %+v", d.Changed)
	}
}

// 输出必须稳定：map 遍历顺序随机，审核员读的 diff 不能每次刷新都换顺序。
func TestDiffTrackRounds_StableOrder(t *testing.T) {
	prev := []paymodel.Track{trk(9, "a", kf(0, 0)), trk(3, "b", kf(0, 0))}
	cur := []paymodel.Track{
		trk(9, "a", kf(0, 50)), trk(3, "b", kf(0, 50)), // 都改了
		trk(7, "new", kf(0, 0)), trk(1, "new", kf(0, 0)), // 都新增
	}
	for i := 0; i < 20; i++ {
		d := DiffTrackRounds(prev, cur, 1, 2)
		if !reflect.DeepEqual(d.Added, []int{1, 7}) {
			t.Fatalf("Added 顺序不稳定: %v", d.Added)
		}
		ids := []int{d.Changed[0].TrackID, d.Changed[1].TrackID}
		if !reflect.DeepEqual(ids, []int{3, 9}) {
			t.Fatalf("Changed 顺序不稳定: %v", ids)
		}
	}
}
