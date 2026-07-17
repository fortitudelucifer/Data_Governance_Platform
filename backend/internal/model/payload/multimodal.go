package payload

import "time"

// ModelProviderRef matches plan_v1/04 §4.1: provider × model × capability_type
// triplet plus endpoint and version. Embedded inside OCR/VLM/AI run results.
type ModelProviderRef struct {
	ProviderID     uint   `json:"provider_id"`
	ProviderName   string `json:"provider_name"`
	ModelID        string `json:"model_id"`
	CapabilityType string `json:"capability_type"`
	EndpointMode   string `json:"endpoint_mode"` // litellm | adapter
	Version        string `json:"version"`
}

// RoutingResult is the immutable output of the L1 router (and P1 L2). Multiple
// versions can coexist; the latest is the active one.
type RoutingResult struct {
	ID                string                 `json:"id"`
	AssetID           uint                   `json:"asset_id"`
	TaskID            uint                   `json:"task_id"`
	TraceID           string                 `json:"trace_id"`
	Version           int                    `json:"version"`
	NeedOCR           bool                   `json:"need_ocr"`
	NeedCaption       bool                   `json:"need_caption"`
	OCRPriority       float64                `json:"ocr_priority"`
	CaptionPriority   float64                `json:"caption_priority"`
	Strategy          string                 `json:"strategy"`
	Reasons           []string               `json:"reasons"`
	Features          map[string]interface{} `json:"features"`
	RecommendedModels []ModelProviderRef     `json:"recommended_models"`
	FallbackChain     []ModelProviderRef     `json:"fallback_chain"`
	CreatedAt         time.Time              `json:"created_at"`
}

// AIRun records a single AI capability invocation. Multiple AIRuns can attach
// to one task across retries; the (task_id, capability_type, run_id) triplet
// is the idempotency key (see plan_v1/05 §2.2 + ADR-08).
type AIRun struct {
	ID             string           `json:"id"`
	RunID          string           `json:"run_id"`
	TaskID         uint             `json:"task_id"`
	AssetID        uint             `json:"asset_id"`
	TraceID        string           `json:"trace_id"`
	CapabilityType string           `json:"capability_type"`
	Provider       ModelProviderRef `json:"provider"`
	Status         string           `json:"status"` // success | failed | timeout
	Error          string           `json:"error,omitempty"`
	LatencyMs      int64            `json:"latency_ms"`
	Cost           float64          `json:"cost"`
	EstimatedCost  float64          `json:"estimated_cost"`
	Attempt        int              `json:"attempt"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
}

// OCRResult holds the structured + raw response of a single OCR adapter call.
type OCRResult struct {
	ID             string                 `json:"id"`
	RunID          string                 `json:"run_id"`
	TaskID         uint                   `json:"task_id"`
	AssetID        uint                   `json:"asset_id"`
	TraceID        string                 `json:"trace_id"`
	Provider       ModelProviderRef       `json:"provider"`
	Boxes          []OCRBox               `json:"boxes"`
	StructuredJSON map[string]interface{} `json:"structured_json,omitempty"`
	StructuredMD   string                 `json:"structured_md,omitempty"`
	RawResponse    interface{}            `json:"raw_response,omitempty"`
	Cost           float64                `json:"cost"`
	LatencyMs      int64                  `json:"latency_ms"`
	Status         string                 `json:"status"`
	CreatedAt      time.Time              `json:"created_at"`
}

// OCRBox is the canonical OCR detection box. Coordinates are absolute pixels
// in the original image (plan_v1/01 §5.3 C-02).
type OCRBox struct {
	X          float64     `json:"x"`
	Y          float64     `json:"y"`
	Width      float64     `json:"w"`
	Height     float64     `json:"h"`
	Text       string      `json:"text"`
	Confidence float64     `json:"confidence"`
	Polygon    [][]float64 `json:"polygon,omitempty"`
}

// VLMResult holds the structured + raw response of a single VLM adapter call
// (LiteLLM main path).
type VLMResult struct {
	ID             string                 `json:"id"`
	RunID          string                 `json:"run_id"`
	TaskID         uint                   `json:"task_id"`
	AssetID        uint                   `json:"asset_id"`
	TraceID        string                 `json:"trace_id"`
	Provider       ModelProviderRef       `json:"provider"`
	Caption        string                 `json:"caption,omitempty"`
	Tags           []string               `json:"tags,omitempty"`
	StructuredJSON map[string]interface{} `json:"structured_json,omitempty"`
	GroundingBoxes []OCRBox               `json:"grounding_boxes,omitempty"`
	RawResponse    interface{}            `json:"raw_response,omitempty"`
	Cost           float64                `json:"cost"`
	LatencyMs      int64                  `json:"latency_ms"`
	Status         string                 `json:"status"`
	CreatedAt      time.Time              `json:"created_at"`
}

// ASRSegment is one transcription segment from the asr.transcribe capability
// (FunASR/Paraformer + CAM++ speaker). Times are ms from the audio start.
type ASRSegment struct {
	StartMs    int64   `json:"start_ms"`
	EndMs      int64   `json:"end_ms"`
	Text       string  `json:"text"`
	Speaker    string  `json:"speaker,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	// Emotion (emotion2vec / SenseVoice) — optional per-segment affect.
	Emotion       string             `json:"emotion,omitempty"`
	EmotionScores map[string]float64 `json:"emotion_scores,omitempty"`
}

// ASRResult holds the response of a single ASR adapter call (asr.transcribe).
// Parallels OCRResult / VLMResult / SegResult.
type ASRResult struct {
	ID          string           `json:"id"`
	RunID       string           `json:"run_id"`
	TaskID      uint             `json:"task_id"`
	AssetID     uint             `json:"asset_id"`
	TraceID     string           `json:"trace_id"`
	Provider    ModelProviderRef `json:"provider"`
	Language    string           `json:"language,omitempty"`
	DurationMs  int64            `json:"duration_ms,omitempty"`
	Segments    []ASRSegment     `json:"segments"`
	RawResponse interface{}      `json:"raw_response,omitempty"`
	LatencyMs   int64            `json:"latency_ms"`
	Status      string           `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
}

// SegPolygon is one instance-segmentation contour from the seg.instance
// capability (YOLOv8-seg). Points are absolute-pixel [x,y] vertices.
type SegPolygon struct {
	ClassName string      `json:"class_name"`
	ClassID   int         `json:"class_id"`
	Points    [][]float64 `json:"points"`
	BBox      []float64   `json:"bbox"` // [x,y,w,h]
	Score     float64     `json:"score"`
}

// SegResult holds the structured + raw response of a single segmentation
// adapter call (seg.instance). Parallels OCRResult / VLMResult.
type SegResult struct {
	ID          string           `json:"id"`
	RunID       string           `json:"run_id"`
	TaskID      uint             `json:"task_id"`
	AssetID     uint             `json:"asset_id"`
	TraceID     string           `json:"trace_id"`
	Provider    ModelProviderRef `json:"provider"`
	Polygons    []SegPolygon     `json:"polygons"`
	RawResponse interface{}      `json:"raw_response,omitempty"`
	LatencyMs   int64            `json:"latency_ms"`
	Status      string           `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
}

// Shape is the unit of human annotation on an asset. P0 only edits BBox; other
// kinds are reserved (plan_v1/03 §7.4).
type Shape struct {
	ID            string                 `json:"id"`
	Kind          string                 `json:"kind"` // bbox | polygon | polyline | point | keypoints | mask
	Label         string                 `json:"label,omitempty"`
	Points        [][]float64            `json:"points"`
	SelectorValue string                 `json:"selector_value,omitempty"` // P1 SVG
	Attrs         map[string]interface{} `json:"attrs,omitempty"`
	Confidence    float64                `json:"confidence,omitempty"`
	Source        string                 `json:"source,omitempty"` // ai | human
	Color         string                 `json:"color,omitempty"`   // 自定义选框颜色（hex）

	// Audio / video reservation per ADR C-04. P0 leaves these zero.
	TimeStartMs *int64 `json:"time_start_ms,omitempty"`
	TimeEndMs   *int64 `json:"time_end_ms,omitempty"`
}

// HumanAnnotation is the editable draft / submitted human annotation. There is
// at most one active HumanAnnotation per task; old versions stay for audit.
type HumanAnnotation struct {
	ID             string                 `json:"id"`
	TaskID         uint                   `json:"task_id"`
	AssetID        uint                   `json:"asset_id"`
	TraceID        string                 `json:"trace_id"`
	AnnotatorID    uint                   `json:"annotator_id"`
	BasedOnAIRuns  []string               `json:"based_on_ai_runs"`
	Shapes         []Shape                `json:"shapes"`
	Texts          map[string]string      `json:"texts,omitempty"`
	Fields         map[string]interface{} `json:"fields,omitempty"`
	Diff           map[string]interface{} `json:"diff,omitempty"`
	QAStatus       string                 `json:"qa_status"` // draft | submitted | passed | rejected
	ReviewerID     *uint                  `json:"reviewer_id,omitempty"`
	ReviewNote     string                 `json:"review_note,omitempty"`
	Version        int                    `json:"version"`
	LastModifiedBy uint                   `json:"last_modified_by"`
	IsActive       bool                   `json:"is_active"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// FinalAnnotation is the immutable platform output for a finalized task. P0
// emits a flat internal JSON; P1 will additionally emit strict W3C JSON-LD.
type FinalAnnotation struct {
	ID         string                 `json:"id"`
	TaskID     uint                   `json:"task_id"`
	AssetID    uint                   `json:"asset_id"`
	DatasetID  uint                   `json:"dataset_id"`
	TraceID    string                 `json:"trace_id"`
	Strategy   string                 `json:"strategy"`
	Shapes     []Shape                `json:"shapes"`
	Texts      map[string]string      `json:"texts,omitempty"`
	Fields     map[string]interface{} `json:"fields,omitempty"`
	Caption    string                 `json:"caption,omitempty"`
	Tags       []string               `json:"tags,omitempty"`
	Provenance map[string]interface{} `json:"provenance,omitempty"`
	Version    int                    `json:"version"`
	CreatedAt  time.Time              `json:"created_at"`
}

// TraceLog is the unified per-call observability record. One row per
// CapabilityService invocation, written by both the LiteLLM gateway and any
// non-generative adapter (plan_v1/05 §4.3).
type TraceLog struct {
	ID                  string    `json:"id"`
	TraceID             string    `json:"trace_id"`
	TaskID              uint      `json:"task_id"`
	RunID               string    `json:"run_id"`
	CapabilityType      string    `json:"capability_type"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	Version             string    `json:"version"`
	EndpointMode        string    `json:"endpoint_mode"`
	LatencyMs           int64     `json:"latency_ms"`
	CostOrEstimatedCost float64   `json:"cost_or_estimated_cost"`
	Status              string    `json:"status"`
	Error               string    `json:"error,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}
