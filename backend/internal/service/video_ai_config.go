package service

import (
	"encoding/json"
	"strings"
)

// Trigger modes for dataset-level video pre-annotation.
const (
	VideoAITriggerManual = "manual" // an annotator/admin presses 「AI 预标注」
	VideoAITriggerAuto   = "auto"   // every uploaded video is queued for detect_track
	VideoAITriggerOff    = "off"    // detect_track disabled for this dataset
)

// Hard ceilings. A dataset may configure *below* these; nothing may go above.
// detect_track cost is roughly linear in the number of sampled frames, so
// max_frames is the single number that bounds what one click can spend on the
// GPU (执行方案-02 B2.8 «成本闸门»).
const (
	videoAIMaxFramesCeiling  = 3000
	videoAISampleStepCeiling = 300
)

// VideoAIConfig is the dataset-level cost gate for detect_track. Zero values
// mean "use the global default", which is why every field is applied through
// Merge rather than read directly.
type VideoAIConfig struct {
	Trigger      string  `json:"trigger"`       // manual | auto | off
	Model        string  `json:"model"`         // yolo | rtdetr
	Tracker      string  `json:"tracker"`       // botsort | bytetrack
	SampleStep   int     `json:"sample_step"`   // sample every Nth frame
	MaxFrames    int     `json:"max_frames"`    // hard cap on sampled frames per video
	MinScore     float64 `json:"min_score"`     // drop AI tracks below this avg confidence
	MinKeyframes int     `json:"min_keyframes"` // drop AI tracks shorter than this
}

// DefaultVideoAIConfig mirrors the adapter defaults. Kept here so the API can
// tell an admin what an unconfigured dataset will actually do.
func DefaultVideoAIConfig() VideoAIConfig {
	return VideoAIConfig{
		Trigger: VideoAITriggerManual, Model: "yolo", Tracker: "botsort",
		SampleStep: 5, MaxFrames: 600, MinScore: 0.4, MinKeyframes: 2,
	}
}

func validVideoTrigger(t string) bool {
	switch t {
	case VideoAITriggerManual, VideoAITriggerAuto, VideoAITriggerOff:
		return true
	}
	return false
}

func validVideoModel(m string) bool   { return m == "yolo" || m == "rtdetr" }
func validVideoTracker(t string) bool { return t == "botsort" || t == "bytetrack" }

// Normalize fills blanks from the defaults and clamps everything into range.
// Invalid enum values fall back to the default rather than erroring: this runs
// on the read path too, where a hand-edited row must not break the workbench.
func (c VideoAIConfig) Normalize() VideoAIConfig {
	d := DefaultVideoAIConfig()
	if !validVideoTrigger(c.Trigger) {
		c.Trigger = d.Trigger
	}
	if !validVideoModel(c.Model) {
		c.Model = d.Model
	}
	if !validVideoTracker(c.Tracker) {
		c.Tracker = d.Tracker
	}
	if c.SampleStep <= 0 {
		c.SampleStep = d.SampleStep
	}
	if c.SampleStep > videoAISampleStepCeiling {
		c.SampleStep = videoAISampleStepCeiling
	}
	if c.MaxFrames <= 0 {
		c.MaxFrames = d.MaxFrames
	}
	if c.MaxFrames > videoAIMaxFramesCeiling {
		c.MaxFrames = videoAIMaxFramesCeiling
	}
	// `<= 0` means "unset" here, not "filter nothing" — matching NewDetTrackAdapter,
	// which applies the same defaults. Two meanings for 0 would make a `{}` PUT
	// silently switch the noise filters off and drown annotators in weak boxes.
	if c.MinScore <= 0 || c.MinScore > 1 {
		c.MinScore = d.MinScore
	}
	if c.MinKeyframes <= 0 {
		c.MinKeyframes = d.MinKeyframes
	}
	return c
}

// --- datasets.ai_config（M11 收敛列）----------------------------------------
// 数据集级 AI 配置收敛为一列 jsonb，按 capability 分键：
//   {"video.detect_track": {...}, "seg.cell": {...}}
// 判据：加模态零改列（执行方案-06 M11）。原 video_ai_config 整列并入
// "video.detect_track" 键；后续 Phase C/D 的能力（seg.cell / det3d.cuboid…）
// 只是加键，不再开新列。下面两个函数是这列**唯一**的读写口。

// VideoAIConfigFromDataset extracts the detect_track gate from the
// capability-keyed ai_config column. Missing key / malformed JSON → defaults
// （坏行退化成「安全」，不是「坏掉」，与 ParseVideoAIConfig 同一哲学）。
func VideoAIConfigFromDataset(aiConfigRaw string) VideoAIConfig {
	raw, ok := decodeAIConfig(aiConfigRaw)[CapabilityVideoDetectTrack]
	if !ok {
		return DefaultVideoAIConfig()
	}
	return ParseVideoAIConfig(string(raw))
}

func decodeAIConfig(raw string) map[string]json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]json.RawMessage{}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// PatchAIConfig sets one capability's entry inside ai_config and returns the
// full column value to store. Read-modify-write：其它能力的键原样保留，
// 更新视频闸门不许顺手抹掉别的能力的配置。
func PatchAIConfig(aiConfigRaw, capability string, value any) (string, error) {
	entries := decodeAIConfig(aiConfigRaw)
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	entries[capability] = b
	out, err := json.Marshal(entries)
	return string(out), err
}

// ParseVideoAIConfig reads a dataset's stored JSON. An empty or malformed value
// yields the defaults — a bad row degrades to "sane", never to "broken".
func ParseVideoAIConfig(raw string) VideoAIConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return DefaultVideoAIConfig()
	}
	var c VideoAIConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return DefaultVideoAIConfig()
	}
	return c.Normalize()
}

// Encode serialises the normalised config for storage.
func (c VideoAIConfig) Encode() (string, error) {
	b, err := json.Marshal(c.Normalize())
	return string(b), err
}

// ApplyRequestOverrides layers a caller's per-run choices on top of the dataset
// config. The workbench may pick a model/tracker freely and may sample *more
// sparsely* than the dataset allows, but it can never raise max_frames — that
// is the whole point of the gate: the ceiling belongs to the dataset owner, not
// to whoever clicks the button.
func (c VideoAIConfig) ApplyRequestOverrides(opts DetectTrackOpts) VideoAIConfig {
	if validVideoModel(opts.Model) {
		c.Model = opts.Model
	}
	if validVideoTracker(opts.Tracker) {
		c.Tracker = opts.Tracker
	}
	if opts.SampleStep > 0 {
		// A larger step samples fewer frames → strictly cheaper. A smaller step
		// is more expensive, but still bounded by MaxFrames below, so allow it.
		c.SampleStep = opts.SampleStep
		if c.SampleStep > videoAISampleStepCeiling {
			c.SampleStep = videoAISampleStepCeiling
		}
	}
	return c
}
