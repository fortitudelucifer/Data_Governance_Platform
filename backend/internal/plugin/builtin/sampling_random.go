package builtin

import (
	"fmt"
	"math/rand"
	"time"

	"text-annotation-platform/internal/plugin"
)

// RandomSamplingStrategy implements SamplingStrategy for random sampling.
type RandomSamplingStrategy struct{}

func (s *RandomSamplingStrategy) StrategyID() string {
	return "random"
}

func (s *RandomSamplingStrategy) Name() string {
	return "Random Sampling"
}

// ValidateParams checks that ratio is a float64 in (0, 1].
func (s *RandomSamplingStrategy) ValidateParams(params map[string]interface{}) error {
	ratioVal, ok := params["ratio"]
	if !ok {
		return fmt.Errorf("strategy 'random' parameter error: 'ratio' is required")
	}
	ratio, ok := ratioVal.(float64)
	if !ok {
		return fmt.Errorf("strategy 'random' parameter error: 'ratio' must be a number")
	}
	if ratio <= 0 || ratio > 1 {
		return fmt.Errorf("strategy 'random' parameter error: ratio must be in (0, 1], got %v", ratio)
	}
	return nil
}

// Sample randomly selects the given proportion of segments.
func (s *RandomSamplingStrategy) Sample(segments []plugin.SegmentUnit, params map[string]interface{}) ([]plugin.SegmentUnit, error) {
	if err := s.ValidateParams(params); err != nil {
		return nil, err
	}
	ratio := params["ratio"].(float64)

	if len(segments) == 0 {
		return nil, nil
	}

	count := int(float64(len(segments)) * ratio)
	if count == 0 {
		count = 1 // at least one
	}
	if count > len(segments) {
		count = len(segments)
	}

	// Shuffle and take first count elements
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	perm := rng.Perm(len(segments))
	result := make([]plugin.SegmentUnit, count)
	for i := 0; i < count; i++ {
		result[i] = segments[perm[i]]
	}
	return result, nil
}
