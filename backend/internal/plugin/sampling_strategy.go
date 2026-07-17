package plugin

// SegmentUnit represents a single segment (paragraph) for sampling.
type SegmentUnit struct {
	DocKey     string `json:"doc_key"`
	SegmentIdx int    `json:"segment_idx"`
	Text       string `json:"text"`
}

// StrategyInfo describes a registered sampling strategy.
type StrategyInfo struct {
	StrategyID  string `json:"strategy_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SamplingStrategy defines the interface for sampling strategy plugins.
type SamplingStrategy interface {
	// StrategyID returns the strategy identifier, e.g. "random", "rule".
	StrategyID() string
	// Name returns a human-readable name for this strategy.
	Name() string
	// ValidateParams validates the sampling parameters.
	ValidateParams(params map[string]interface{}) error
	// Sample selects a subset of segments according to the strategy.
	Sample(segments []SegmentUnit, params map[string]interface{}) ([]SegmentUnit, error)
}
