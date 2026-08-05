package service

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	paymodel "text-annotation-platform/internal/model/payload"
)

// randomQAPair generates a random QAPair, sometimes with empty/short fields to
// exercise filter edge cases.
func randomQAPair(rng *rand.Rand) paymodel.QAPair {
	q := randomQAString(rng)
	a := randomQAString(rng)
	return paymodel.QAPair{
		Question:  q,
		Answer:    a,
		Source:    "llm",
		Confirmed: false,
	}
}

// randomQAString generates a string that is sometimes empty, sometimes short,
// sometimes whitespace-only, and sometimes a normal string with multibyte chars.
func randomQAString(rng *rand.Rand) string {
	switch rng.Intn(6) {
	case 0:
		return "" // empty
	case 1:
		return "   " // whitespace only
	case 2:
		// Short string (1-4 runes) — may include multibyte
		n := rng.Intn(4) + 1
		return randomUnicodeString(rng, n)
	default:
		// Normal string (5-30 runes)
		n := rng.Intn(26) + 5
		return randomUnicodeString(rng, n)
	}
}

// randomUnicodeString generates a string of exactly n runes, mixing ASCII and CJK.
func randomUnicodeString(rng *rand.Rand, n int) string {
	runes := make([]rune, n)
	for i := range runes {
		if rng.Intn(3) == 0 {
			// CJK character range
			runes[i] = rune(0x4E00 + rng.Intn(0x9FFF-0x4E00))
		} else {
			runes[i] = rune('a' + rng.Intn(26))
		}
	}
	return string(runes)
}

// TestParseQAPairsInvariants_Property verifies that ParseQAPairs satisfies:
// (a) no empty Q/A in result
// (b) no question < 5 rune chars
// (c) no duplicate questions
// (d) result is subset of input
//
// Feature: annotation-workflow-v2, Property 10: QA 质量过滤不变量
// **Validates: Requirements 3.8**
func TestParseQAPairsInvariants_Property(t *testing.T) {
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		// Generate random slice of QA pairs (0-20 items)
		n := rng.Intn(21)
		input := make([]paymodel.QAPair, n)
		for j := 0; j < n; j++ {
			input[j] = randomQAPair(rng)
		}

		// Sometimes inject duplicates
		if n > 2 && rng.Intn(3) == 0 {
			input[n-1].Question = input[0].Question
		}

		result := paymodel.ParseQAPairs(input)

		// (a) No empty Q/A
		for _, p := range result {
			if strings.TrimSpace(p.Question) == "" {
				t.Errorf("iteration %d: result contains empty question", i)
			}
			if strings.TrimSpace(p.Answer) == "" {
				t.Errorf("iteration %d: result contains empty answer", i)
			}
		}

		// (b) No question < 5 rune chars
		for _, p := range result {
			if utf8.RuneCountInString(strings.TrimSpace(p.Question)) < 5 {
				t.Errorf("iteration %d: result contains short question %q (rune count %d)",
					i, p.Question, utf8.RuneCountInString(strings.TrimSpace(p.Question)))
			}
		}

		// (c) No duplicate questions
		seen := make(map[string]bool)
		for _, p := range result {
			q := strings.TrimSpace(p.Question)
			if seen[q] {
				t.Errorf("iteration %d: result contains duplicate question %q", i, q)
			}
			seen[q] = true
		}

		// (d) Result is subset of input
		inputSet := make(map[string]map[string]bool) // question -> set of answers
		for _, p := range input {
			q := strings.TrimSpace(p.Question)
			a := strings.TrimSpace(p.Answer)
			if inputSet[q] == nil {
				inputSet[q] = make(map[string]bool)
			}
			inputSet[q][a] = true
		}
		for _, p := range result {
			q := strings.TrimSpace(p.Question)
			a := strings.TrimSpace(p.Answer)
			if !inputSet[q][a] {
				t.Errorf("iteration %d: result pair (%q, %q) not found in input", i, q, a)
			}
		}
	}
}
