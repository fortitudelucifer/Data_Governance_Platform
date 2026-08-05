package relational

import (
	"time"
)

// AnnotationTask state machine constants for the multi-modal P0 backbone.
// Matches plan_v1/01 §7.1 P0 state machine. P1 extensions (ROUTING_REVIEW,
// GRAY_DUAL_RUN, OCR_THEN_VLM, VLM_THEN_OCR) are reserved as enum positions
// only and not driven by the worker in P0.
const (
	TaskStateCreated         = "CREATED"
	TaskStateQCRunning       = "QC_RUNNING"
	TaskStateQCFailed        = "QC_FAILED"
	TaskStateRouting         = "ROUTING"
	TaskStateRoutingDone     = "ROUTING_DONE"
	TaskStateRoutingReview   = "ROUTING_REVIEW" // P1 reserved
	TaskStateAIPending       = "AI_PENDING"
	TaskStateAIRunning       = "AI_RUNNING"
	TaskStateAIFailed        = "AI_FAILED"
	TaskStateAIDone          = "AI_DONE"
	TaskStateHumanPending    = "HUMAN_PENDING"
	TaskStateHumanInProgress = "HUMAN_IN_PROGRESS"
	TaskStateHumanSubmitted  = "HUMAN_SUBMITTED"
	TaskStateQAPending       = "QA_PENDING"
	TaskStateQARejected      = "QA_REJECTED"
	TaskStateQAPassed        = "QA_PASSED"
	TaskStateFinalized       = "FINALIZED"
	TaskStateExported        = "EXPORTED"
	TaskStateReprocess       = "REPROCESS"
)

// Routing strategies. P0 only emits OCR_FIRST / VLM_FIRST / HUMAN_ONLY.
// Everything else is reserved for P1.
const (
	RouteOCRFirst    = "OCR_FIRST"
	RouteVLMFirst    = "VLM_FIRST"
	RouteHumanOnly   = "HUMAN_ONLY"
	RouteGrayDualRun = "GRAY_DUAL_RUN" // P1
	RouteOCRThenVLM  = "OCR_THEN_VLM"  // P1
	RouteVLMThenOCR  = "VLM_THEN_OCR"  // P1
	RouteTraditional = "TRADITIONAL_CV"
	RouteReprocess   = "REPROCESS"

	// A/V routes. Reserved in M0; the AI wiring (capabilityForStrategy +
	// adapters) lands in Phase A2 (audio) / B2 (video). See plan_v2
	// 执行方案-00-共用基座 T0.1 and 01/02.
	RouteASRFirst         = "ASR_FIRST"          // audio: ASR prelabel → human
	RouteVideoDetectFirst = "VIDEO_DETECT_FIRST" // video: detect+track prelabel → human
)

const (
	StrategyOriginAuto          = "AUTO"
	StrategyOriginHumanOverride = "HUMAN_OVERRIDE" // P1
)

// AnnotationTask is the relational primary state record for a multi-modal annotation
// task. Immutable AI raw / human / final results live in the payload tables; this row
// drives the state machine, retries, deadlines, assignment and trace linkage.
//
// See plan_v1/01 §7, plan_v1/03 §7.3.
type AnnotationTask struct {
	ID            uint  `gorm:"primaryKey" json:"id"`
	AssetID       uint  `gorm:"index;not null" json:"asset_id"`
	DatasetID     uint  `gorm:"index;not null" json:"dataset_id"`
	JobID         *uint `gorm:"index" json:"job_id"`
	ParentAssetID *uint `gorm:"index" json:"parent_asset_id"`

	RouteStrategy      string `gorm:"size:32;index" json:"route_strategy"`
	StrategyOrigin     string `gorm:"size:16;not null;default:'AUTO'" json:"strategy_origin"`
	StrategyReviewNote string `gorm:"type:text" json:"strategy_review_note"`

	State string `gorm:"size:32;not null;default:'CREATED';index" json:"state"`

	AssigneeID *uint `gorm:"index" json:"assignee_id"`
	ReviewerID *uint `gorm:"index" json:"reviewer_id"`

	TraceID string `gorm:"size:64;index;not null" json:"trace_id"`

	RetryCount int        `gorm:"not null;default:0" json:"retry_count"`
	Version    int        `gorm:"not null;default:1" json:"version"`
	DeadlineAt *time.Time `json:"deadline_at"`

	Cost      float64 `gorm:"type:decimal(12,6)" json:"cost"`
	LatencyMs int64   `json:"latency_ms"`

	AIRunIDs          JSON   `gorm:"type:jsonb" json:"ai_run_ids"`
	HumanAnnotationID string `gorm:"size:64" json:"human_annotation_id"`
	FinalAnnotationID string `gorm:"size:64" json:"final_annotation_id"`
	Error             JSON   `gorm:"type:jsonb" json:"error"`
	HumanOnlyFallback bool   `gorm:"not null;default:false" json:"human_only_fallback"`

	RoutingReviewDeadline *time.Time `json:"routing_review_deadline"` // P1

	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at"` // worker scheduling
	LeaseUntil    *time.Time `gorm:"index" json:"lease_until"`     // worker lease

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName overrides the default plural to avoid clash with anything called
// `annotation_tasks` elsewhere.
func (AnnotationTask) TableName() string { return "annotation_tasks" }

// IsTerminal reports whether the task is in a terminal state from which only
// REPROCESS can transition out.
func (t *AnnotationTask) IsTerminal() bool {
	switch t.State {
	case TaskStateFinalized, TaskStateExported, TaskStateQCFailed:
		return true
	}
	return false
}
