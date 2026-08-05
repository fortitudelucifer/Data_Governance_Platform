package model

// DashboardStats holds aggregated dashboard statistics.
type DashboardStats struct {
	DatasetCount       int            `json:"dataset_count"`
	DocCount           int            `json:"doc_count"`
	AutoAnnotatedCount int            `json:"auto_annotated_count"`
	RefinedCount       int            `json:"refined_count"`
	QATotal            int            `json:"qa_total"`
	StageDistribution  map[string]int `json:"stage_distribution"`
	ImageTasks         *ImageTaskStats `json:"image_tasks,omitempty"`
}

// ImageTaskStats holds aggregated image annotation task statistics.
type ImageTaskStats struct {
	Total             int            `json:"total"`
	FinalizedToday    int            `json:"finalized_today"`
	StateDistribution map[string]int `json:"state_distribution"`
}

// DailyTrend holds a single day's trend data.
type DailyTrend struct {
	Date         string `json:"date"`
	RefinedCount int    `json:"refined_count"`
}

// AnnotatorStats holds per-annotator performance metrics.
type AnnotatorStats struct {
	UserID         string  `json:"user_id"`
	Username       string  `json:"username"`
	DisplayName    string  `json:"display_name"`
	AssignedCount  int     `json:"assigned_count"`
	CompletedCount int     `json:"completed_count"`
	CompletionRate float64 `json:"completion_rate"`
}

// ImageAnnotatorStats holds per-annotator image task performance metrics.
// Sourced from annotation_tasks (relational DB); scoped to assignee_id.
type ImageAnnotatorStats struct {
	UserID          uint    `json:"user_id"`
	DisplayName     string  `json:"display_name"`
	AssignedCount   int     `json:"assigned_count"`    // all tasks with assignee_id = user
	InProgressCount int     `json:"in_progress_count"` // HUMAN_PENDING + HUMAN_IN_PROGRESS
	QAPendingCount  int     `json:"qa_pending_count"`  // QA_PENDING
	FinalizedCount  int     `json:"finalized_count"`   // FINALIZED + EXPORTED
	TodayFinalized  int     `json:"today_finalized"`
	CompletionRate  float64 `json:"completion_rate"` // finalized / assigned × 100
}
