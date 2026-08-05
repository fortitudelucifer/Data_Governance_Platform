package service

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// newTestDB opens a real-Postgres fixture (独立 schema + 真 goose 迁移).
func newTestDB(t *testing.T) *repository.DB {
	t.Helper()
	return &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
}

// randomProviderType returns a random valid provider type.
func randomProviderType(rng *rand.Rand) string {
	types := []string{"ollama", "openai_compatible"}
	return types[rng.Intn(len(types))]
}

// randomNonEmptyString generates a random non-empty ASCII string of length 1..maxLen.
func randomNonEmptyString(rng *rand.Rand, maxLen int) string {
	n := rng.Intn(maxLen) + 1
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + rng.Intn(26))
	}
	return string(b)
}

// TestLLMProviderConfigRoundTrip_Property verifies that creating an LLM provider
// and then querying it by ID returns all fields unchanged, and that timeout/max_retries
// are independent per provider.
//
// Feature: annotation-workflow-v2, Property 1: LLM Provider 配置 round-trip
// **Validates: Requirements 1.1, 1.6, 9.7**
func TestLLMProviderConfigRoundTrip_Property(t *testing.T) {
	repo := newTestDB(t)
	llmSvc := NewLLMService(repo, nil, nil, LLMServiceConfig{})
	svc := NewCapabilityConfigService(repo, nil, llmSvc, nil, 0)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		timeout := rng.Intn(300) + 1
		maxRetries := rng.Intn(10) + 1
		providerType := randomProviderType(rng)
		apiKey := ""
		if providerType == "openai_compatible" {
			apiKey = randomNonEmptyString(rng, 30)
		}

		provider := &dbmodel.LLMProvider{
			Name:           fmt.Sprintf("provider-%d", i),
			Type:           providerType,
			ProviderKind:   providerType,
			CapabilityType: "text.chat",
			Endpoint:       fmt.Sprintf("http://host-%d:8080", i),
			APIKey:         apiKey,
			Model:          randomNonEmptyString(rng, 20),
			Enabled:        rng.Intn(2) == 1,
			TimeoutSeconds: timeout,
			MaxRetries:     maxRetries,
		}

		if err := svc.Create(ctx, provider); err != nil {
			t.Fatalf("iteration %d: Create failed: %v", i, err)
		}

		got, err := svc.GetByID(ctx, provider.ID)
		if err != nil {
			t.Fatalf("iteration %d: GetByID failed: %v", i, err)
		}

		if got.Name != provider.Name {
			t.Errorf("iteration %d: Name mismatch: got %q, want %q", i, got.Name, provider.Name)
		}
		if got.Type != provider.Type {
			t.Errorf("iteration %d: Type mismatch: got %q, want %q", i, got.Type, provider.Type)
		}
		if got.Endpoint != provider.Endpoint {
			t.Errorf("iteration %d: Endpoint mismatch: got %q, want %q", i, got.Endpoint, provider.Endpoint)
		}
		if got.APIKey != provider.APIKey {
			t.Errorf("iteration %d: APIKey mismatch: got %q, want %q", i, got.APIKey, provider.APIKey)
		}
		if got.Model != provider.Model {
			t.Errorf("iteration %d: Model mismatch: got %q, want %q", i, got.Model, provider.Model)
		}
		if got.Enabled != provider.Enabled {
			t.Errorf("iteration %d: Enabled mismatch: got %v, want %v", i, got.Enabled, provider.Enabled)
		}
		if got.TimeoutSeconds != provider.TimeoutSeconds {
			t.Errorf("iteration %d: TimeoutSeconds mismatch: got %d, want %d", i, got.TimeoutSeconds, provider.TimeoutSeconds)
		}
		if got.MaxRetries != provider.MaxRetries {
			t.Errorf("iteration %d: MaxRetries mismatch: got %d, want %d", i, got.MaxRetries, provider.MaxRetries)
		}
	}

	// Verify timeout and max_retries are independent per provider
	all, err := svc.List(ctx, "text.chat")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(all) != iterations {
		t.Fatalf("expected %d providers, got %d", iterations, len(all))
	}

	seenTimeouts := make(map[int]bool)
	seenRetries := make(map[int]bool)
	for _, p := range all {
		seenTimeouts[p.TimeoutSeconds] = true
		seenRetries[p.MaxRetries] = true
	}
	if len(seenTimeouts) < 2 {
		t.Error("expected providers to have diverse timeout values")
	}
	if len(seenRetries) < 2 {
		t.Error("expected providers to have diverse max_retries values")
	}
}

func TestCapabilityConfigListWithoutFilterIncludesTextChat(t *testing.T) {
	repo := newTestDB(t)
	svc := NewCapabilityConfigService(repo, nil, nil, nil, 0)
	ctx := context.Background()

	rows := []*dbmodel.LLMProvider{
		{
			Name:           "qwen3.7-max",
			Type:           "openai",
			ProviderKind:   "openai",
			CapabilityType: "text.chat",
			Endpoint:       "http://llm.example/v1",
			Model:          "qwen3.7-max",
			Enabled:        true,
		},
		{
			Name:           "ocr",
			Type:           "http",
			ProviderKind:   "http",
			CapabilityType: "ocr.structure",
			Endpoint:       "http://ocr.example",
			Model:          "ocr",
			Enabled:        true,
		},
	}
	for _, row := range rows {
		if err := svc.Create(ctx, row); err != nil {
			t.Fatalf("create %s: %v", row.Name, err)
		}
	}

	got, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List without filter failed: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.Name] = true
	}
	for _, want := range []string{"qwen3.7-max", "ocr"} {
		if !seen[want] {
			t.Fatalf("expected %q in unfiltered capability list, got %#v", want, seen)
		}
	}
}
