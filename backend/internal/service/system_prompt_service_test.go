package service

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

func newPromptTestRepo(t *testing.T) *repository.DB {
	t.Helper()
	return &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
}

// TestSystemPromptCRUDRoundTrip_Property verifies that creating a system prompt
// and querying by case_type returns matching content, and that updating content
// is reflected on subsequent queries.
//
// Feature: annotation-workflow-v2, Property 4: System Prompt CRUD round-trip
// **Validates: Requirements 2.2, 2.8, 2.9**
func TestSystemPromptCRUDRoundTrip_Property(t *testing.T) {
	repo := newPromptTestRepo(t)
	svc := NewSystemPromptService(repo)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		caseType := fmt.Sprintf("type_%d", i)
		name := fmt.Sprintf("name_%d", i)
		content := randomNonEmptyString(rng, 200)

		// Create
		created, err := svc.Create(ctx, caseType, name, content)
		if err != nil {
			t.Fatalf("iteration %d: Create failed: %v", i, err)
		}

		// Read back by case_type
		got, err := svc.GetByCaseType(ctx, caseType)
		if err != nil {
			t.Fatalf("iteration %d: GetByCaseType failed: %v", i, err)
		}
		if got.Content != content {
			t.Errorf("iteration %d: content mismatch after create: got %q, want %q", i, got.Content, content)
		}
		if got.CaseType != caseType {
			t.Errorf("iteration %d: case_type mismatch: got %q, want %q", i, got.CaseType, caseType)
		}
		if got.ID != created.ID {
			t.Errorf("iteration %d: ID mismatch: got %d, want %d", i, got.ID, created.ID)
		}

		// Update content
		newContent := randomNonEmptyString(rng, 200)
		if err := svc.Update(ctx, caseType, newContent); err != nil {
			t.Fatalf("iteration %d: Update failed: %v", i, err)
		}

		// Read back after update
		updated, err := svc.GetByCaseType(ctx, caseType)
		if err != nil {
			t.Fatalf("iteration %d: GetByCaseType after update failed: %v", i, err)
		}
		if updated.Content != newContent {
			t.Errorf("iteration %d: content mismatch after update: got %q, want %q", i, updated.Content, newContent)
		}
	}

	// Verify List returns all created prompts
	all, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(all) != iterations {
		t.Errorf("expected %d prompts, got %d", iterations, len(all))
	}
}
