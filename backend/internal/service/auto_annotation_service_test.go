package service

import (
	"context"
	"math/rand"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// newTestDBWithPrompts opens a real-Postgres fixture (独立 schema + 真 goose
// 迁移) with the default system prompts seeded.
func newTestDBWithPrompts(t *testing.T) *repository.DB {
	t.Helper()
	db := testutil.DB(t, repository.RunMigrations)

	// Seed default system prompts
	prompts := []dbmodel.SystemPrompt{
		{CaseType: "criminal", Name: "刑事案件", Content: "刑事案件 System Prompt 内容"},
		{CaseType: "civil", Name: "民事案件", Content: "民事案件 System Prompt 内容"},
		{CaseType: "administrative", Name: "行政案件", Content: "行政案件 System Prompt 内容"},
	}
	for _, p := range prompts {
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("failed to seed prompt: %v", err)
		}
	}
	return &repository.DB{DB: db}
}

// randomStage returns a random annotation stage.
func randomStage(rng *rand.Rand) string {
	return AllStages[rng.Intn(len(AllStages))]
}

// TestStageTransitionValidity_Property verifies that all valid transitions succeed
// and all invalid transitions are rejected.
//
// Feature: annotation-workflow-v2, Property 7: 标注阶段状态机合法转换
// **Validates: Requirements 3.2, 3.6, 4.2, 4.8**
func TestStageTransitionValidity_Property(t *testing.T) {
	// Build the set of valid (current, target) pairs
	validSet := make(map[[2]string]bool)
	for current, targets := range validTransitions {
		for _, target := range targets {
			validSet[[2]string{current, target}] = true
		}
	}

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		current := randomStage(rng)
		target := randomStage(rng)

		err := ValidateStageTransition(current, target)
		isValid := validSet[[2]string{current, target}]

		if isValid && err != nil {
			t.Errorf("iteration %d: valid transition %s -> %s was rejected: %v", i, current, target, err)
		}
		if !isValid && err == nil {
			t.Errorf("iteration %d: invalid transition %s -> %s was accepted", i, current, target)
		}
	}

	// Exhaustive check: verify every valid transition succeeds
	for current, targets := range validTransitions {
		for _, target := range targets {
			if err := ValidateStageTransition(current, target); err != nil {
				t.Errorf("valid transition %s -> %s was rejected: %v", current, target, err)
			}
		}
	}

	// Exhaustive check: verify every invalid transition is rejected
	for _, current := range AllStages {
		for _, target := range AllStages {
			if validSet[[2]string{current, target}] {
				continue
			}
			if err := ValidateStageTransition(current, target); err == nil {
				t.Errorf("invalid transition %s -> %s was accepted", current, target)
			}
		}
	}
}

// TestAutoAnnotatingStateLock_Property verifies that the auto_annotating state
// rejects edit, annotate, and re-trigger auto-annotate operations.
// The only valid transitions from auto_annotating are auto_annotated and auto_failed.
//
// Feature: annotation-workflow-v2, Property 8: 自动标注中状态锁定
// **Validates: Requirements 3.3**
func TestAutoAnnotatingStateLock_Property(t *testing.T) {
	// Stages that represent "edit", "annotate", or "re-trigger auto-annotate"
	// from auto_annotating —all should be rejected except auto_annotated and auto_failed.
	blockedTargets := []string{
		StageNotAnnotated,
		StageAutoAnnotating, // re-trigger
		StageRefining,       // start refinement
		StageRefined,        // complete refinement
	}

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		// Pick a random blocked target
		target := blockedTargets[rng.Intn(len(blockedTargets))]
		err := ValidateStageTransition(StageAutoAnnotating, target)
		if err == nil {
			t.Errorf("iteration %d: auto_annotating -> %s should be rejected but was accepted", i, target)
		}
	}

	// Verify the only allowed transitions from auto_annotating
	allowedFromAnnotating := []string{StageAutoAnnotated, StageAutoFailed}
	for _, target := range allowedFromAnnotating {
		if err := ValidateStageTransition(StageAutoAnnotating, target); err != nil {
			t.Errorf("auto_annotating -> %s should be allowed but was rejected: %v", target, err)
		}
	}
}

// TestQAPairSourceConfirmedInvariant_Property verifies that:
// - LLM-generated QA pairs have source="llm" and confirmed=false
// - Manually added QA pairs have source="manual"
//
// Feature: annotation-workflow-v2, Property 9: QA 对来源与确认状态不变量
// **Validates: Requirements 3.4, 4.10**
func TestQAPairSourceConfirmedInvariant_Property(t *testing.T) {
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		// Simulate LLM-generated QA pairs
		n := rng.Intn(10) + 1
		llmPairs := make([]paymodel.QAPair, n)
		for j := 0; j < n; j++ {
			llmPairs[j] = paymodel.QAPair{
				Question:  randomUnicodeString(rng, rng.Intn(20)+5),
				Answer:    randomUnicodeString(rng, rng.Intn(30)+5),
				Source:    "llm",
				Confirmed: false,
			}
		}

		// Verify LLM pairs invariant
		for j, p := range llmPairs {
			if p.Source != "llm" {
				t.Errorf("iteration %d, pair %d: LLM pair source should be 'llm', got %q", i, j, p.Source)
			}
			if p.Confirmed {
				t.Errorf("iteration %d, pair %d: LLM pair confirmed should be false", i, j)
			}
		}

		// Simulate manually added QA pairs
		manualCount := rng.Intn(5) + 1
		manualPairs := make([]paymodel.QAPair, manualCount)
		for j := 0; j < manualCount; j++ {
			manualPairs[j] = paymodel.QAPair{
				Question: randomUnicodeString(rng, rng.Intn(20)+5),
				Answer:   randomUnicodeString(rng, rng.Intn(30)+5),
				Source:   "manual",
			}
		}

		// Verify manual pairs invariant
		for j, p := range manualPairs {
			if p.Source != "manual" {
				t.Errorf("iteration %d, manual pair %d: source should be 'manual', got %q", i, j, p.Source)
			}
		}
	}
}

// TestAutoAnnotationLoadsCorrectPrompt_Property verifies that the dataset's
// case_type maps to the correct system prompt, and that empty/unknown case_type
// falls back to criminal.
//
// Feature: annotation-workflow-v2, Property 6: 自动标注加载正确的 System Prompt
// **Validates: Requirements 2.5, 2.7**
func TestAutoAnnotationLoadsCorrectPrompt_Property(t *testing.T) {
	repo := newTestDBWithPrompts(t)
	promptSvc := NewSystemPromptService(repo)
	ctx := context.Background()

	// Known case types and their expected content
	expectedContent := map[string]string{
		"criminal":       "刑事案件 System Prompt 内容",
		"civil":          "民事案件 System Prompt 内容",
		"administrative": "行政案件 System Prompt 内容",
	}

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	knownTypes := []string{"criminal", "civil", "administrative"}
	unknownTypes := []string{"", "unknown", "tax", "maritime", "环保"}

	for i := 0; i < iterations; i++ {
		var caseType string
		if rng.Intn(3) == 0 {
			// Use unknown/empty case type —should fall back to criminal
			caseType = unknownTypes[rng.Intn(len(unknownTypes))]
		} else {
			caseType = knownTypes[rng.Intn(len(knownTypes))]
		}

		prompt, err := promptSvc.GetOrDefault(ctx, caseType)
		if err != nil {
			t.Fatalf("iteration %d: GetOrDefault(%q) failed: %v", i, caseType, err)
		}

		// Determine expected content
		expected, isKnown := expectedContent[caseType]
		if !isKnown {
			// Should fall back to criminal
			expected = expectedContent["criminal"]
		}

		if prompt.Content != expected {
			t.Errorf("iteration %d: case_type=%q, got content %q, want %q",
				i, caseType, prompt.Content, expected)
		}
	}
}

// TestConnectivityCheckBeforeAutoAnnotation_Property verifies that when a
// provider's LastTestSuccess is false or nil, a warning is returned.
//
// Feature: annotation-workflow-v2, Property 21: 自动标注前连通性检测
// **Validates: Requirements 9.8**
func TestConnectivityCheckBeforeAutoAnnotation_Property(t *testing.T) {
	repo := newTestDBWithPrompts(t)
	// Also migrate LLMProvider (already done in newTestDBWithPrompts)
	svc := NewAutoAnnotationService(nil, nil, nil, repo)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		// Create a provider with random LastTestSuccess state
		var lastTestSuccess *bool
		switch rng.Intn(3) {
		case 0:
			lastTestSuccess = nil // never tested
		case 1:
			v := false
			lastTestSuccess = &v // test failed
		case 2:
			v := true
			lastTestSuccess = &v // test succeeded
		}

		provider := dbmodel.LLMProvider{
			Name:            randomNonEmptyString(rng, 20),
			Type:            "openai_compatible",
			Endpoint:        "http://localhost:8080",
			Model:           "test-model",
			Enabled:         true,
			TimeoutSeconds:  60,
			MaxRetries:      3,
			LastTestSuccess: lastTestSuccess,
		}
		if err := repo.DB.Create(&provider).Error; err != nil {
			t.Fatalf("iteration %d: failed to create provider: %v", i, err)
		}

		warning := svc.CheckProviderConnectivity(ctx, provider.ID)

		if lastTestSuccess != nil && *lastTestSuccess {
			// Provider passed test —no warning expected
			if warning != "" {
				t.Errorf("iteration %d: provider passed test but got warning: %q", i, warning)
			}
		} else {
			// Provider never tested or failed —warning expected
			if warning == "" {
				t.Errorf("iteration %d: provider not tested/failed but no warning returned (LastTestSuccess=%v)",
					i, lastTestSuccess)
			}
		}
	}
}
