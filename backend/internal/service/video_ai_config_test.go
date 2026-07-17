package service

import "testing"

// 坏行不能让工作台崩：空 / 非法 JSON / 越界值一律降级成「合理」，而不是「坏掉」。
func TestParseVideoAIConfig_DegradesToDefaults(t *testing.T) {
	d := DefaultVideoAIConfig()
	for _, raw := range []string{"", "  ", "{}", "not json", `{"trigger":`, `{"trigger":"bogus"}`} {
		got := ParseVideoAIConfig(raw)
		if got.Trigger != d.Trigger || got.MaxFrames != d.MaxFrames || got.Model != d.Model {
			t.Errorf("ParseVideoAIConfig(%q) = %+v, want defaults %+v", raw, got, d)
		}
	}
}

func TestParseVideoAIConfig_KeepsValidValues(t *testing.T) {
	got := ParseVideoAIConfig(`{"trigger":"auto","model":"rtdetr","tracker":"bytetrack","sample_step":10,"max_frames":900,"min_score":0.6,"min_keyframes":3}`)
	want := VideoAIConfig{Trigger: "auto", Model: "rtdetr", Tracker: "bytetrack", SampleStep: 10, MaxFrames: 900, MinScore: 0.6, MinKeyframes: 3}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// 全局天花板：数据集配得再高也不能突破，否则「成本闸门」形同虚设。
func TestNormalize_ClampsToGlobalCeilings(t *testing.T) {
	c := VideoAIConfig{Trigger: "manual", Model: "yolo", Tracker: "botsort",
		SampleStep: 99999, MaxFrames: 99999, MinScore: 0.5, MinKeyframes: 2}.Normalize()
	if c.MaxFrames != videoAIMaxFramesCeiling {
		t.Errorf("MaxFrames = %d, want 上限 %d", c.MaxFrames, videoAIMaxFramesCeiling)
	}
	if c.SampleStep != videoAISampleStepCeiling {
		t.Errorf("SampleStep = %d, want 上限 %d", c.SampleStep, videoAISampleStepCeiling)
	}
}

// 噪声阈值：0 表示「未设置」而非「不过滤」——与 NewDetTrackAdapter 的默认逻辑
// 一致。两种含义并存的话，一次 `PUT {}` 就会把噪声过滤悄悄关掉。
func TestNormalize_ZeroAndOutOfRangeScoreFallBackToDefault(t *testing.T) {
	d := DefaultVideoAIConfig()
	for _, bad := range []float64{-0.1, 0, 1.5} {
		c := VideoAIConfig{MinScore: bad}.Normalize()
		if c.MinScore != d.MinScore {
			t.Errorf("MinScore=%v 应回落到默认 %v, got %v", bad, d.MinScore, c.MinScore)
		}
	}
	if got := (VideoAIConfig{MinScore: 1}).Normalize().MinScore; got != 1 {
		t.Errorf("MinScore=1 合法（只留满分），却被改成 %v", got)
	}
	if got := (VideoAIConfig{MinKeyframes: 0}).Normalize().MinKeyframes; got != d.MinKeyframes {
		t.Errorf("MinKeyframes=0 应回落到默认 %d, got %d", d.MinKeyframes, got)
	}
}

// 空对象 PUT 必须落成完整默认值，而不是一堆 0。
func TestNormalize_EmptyYieldsFullDefaults(t *testing.T) {
	if got, want := (VideoAIConfig{}).Normalize(), DefaultVideoAIConfig(); got != want {
		t.Fatalf("空配置 → %+v, want %+v", got, want)
	}
}

// 闸门的核心语义：调用方可以挑模型/追踪器、可以采样得更稀疏，
// 但**永远不能抬高 max_frames**——天花板属于数据集所有者，不属于点按钮的人。
func TestApplyRequestOverrides_CannotRaiseMaxFrames(t *testing.T) {
	ds := VideoAIConfig{Trigger: "manual", Model: "yolo", Tracker: "botsort",
		SampleStep: 5, MaxFrames: 300, MinScore: 0.4, MinKeyframes: 2}

	got := ds.ApplyRequestOverrides(DetectTrackOpts{Model: "rtdetr", Tracker: "bytetrack", SampleStep: 20})
	if got.MaxFrames != 300 {
		t.Fatalf("MaxFrames 被请求改动了：%d（请求里根本没有这个字段，也不该有）", got.MaxFrames)
	}
	if got.Model != "rtdetr" || got.Tracker != "bytetrack" || got.SampleStep != 20 {
		t.Errorf("模型/追踪器/采样步长应可被调用方覆盖: %+v", got)
	}
	// 非法的模型/追踪器不覆盖数据集设置
	got2 := ds.ApplyRequestOverrides(DetectTrackOpts{Model: "evil", Tracker: "evil"})
	if got2.Model != "yolo" || got2.Tracker != "botsort" {
		t.Errorf("非法覆盖应被忽略: %+v", got2)
	}
	// 采样步长同样受全局天花板约束
	got3 := ds.ApplyRequestOverrides(DetectTrackOpts{SampleStep: 99999})
	if got3.SampleStep != videoAISampleStepCeiling {
		t.Errorf("SampleStep = %d, want 上限 %d", got3.SampleStep, videoAISampleStepCeiling)
	}
}

// Encode → Parse 往返后应稳定（Encode 已经 Normalize 过）。
func TestVideoAIConfig_EncodeParseRoundTrip(t *testing.T) {
	in := VideoAIConfig{Trigger: "off", Model: "rtdetr", Tracker: "bytetrack",
		SampleStep: 7, MaxFrames: 1200, MinScore: 0.55, MinKeyframes: 4}
	raw, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseVideoAIConfig(raw); got != in {
		t.Fatalf("往返后 %+v != %+v", got, in)
	}
}
