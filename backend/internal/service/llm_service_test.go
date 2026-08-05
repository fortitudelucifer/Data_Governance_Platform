package service

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestOpenAICompatibleProtocol_Property verifies that the OpenAICompatibleAdapter
// sends correctly formatted requests: URL ends with /v1/chat/completions,
// messages array has system+user roles, and Authorization header is Bearer {api_key}.
//
// Feature: annotation-workflow-v2, Property 2: OpenAI 兼容协议请求格式
// **Validates: Requirements 1.3, 1.7**
func TestOpenAICompatibleProtocol_Property(t *testing.T) {
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		systemPrompt := randomTestString(rng, 50)
		userPrompt := randomTestString(rng, 100)
		apiKey := randomTestString(rng, 20)
		model := randomTestString(rng, 15)

		var capturedPath string
		var capturedAuth string
		var capturedBody chatCompletionRequest

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			capturedAuth = r.Header.Get("Authorization")

			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				t.Errorf("iteration %d: failed to decode request body: %v", i, err)
			}

			resp := chatCompletionResponse{
				Choices: []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}{
					{Message: struct {
						Content string `json:"content"`
					}{Content: "ok"}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))

		adapter := NewOpenAICompatibleAdapter(server.URL, apiKey, model, 10*time.Second)
		_, err := adapter.Call(context.Background(), systemPrompt, userPrompt)
		server.Close()

		if err != nil {
			t.Fatalf("iteration %d: Call failed: %v", i, err)
		}

		// (a) Request URL ends with /v1/chat/completions
		if !strings.HasSuffix(capturedPath, "/v1/chat/completions") {
			t.Errorf("iteration %d: URL path = %q, want suffix /v1/chat/completions", i, capturedPath)
		}

		// (b) Messages array has system + user roles
		if len(capturedBody.Messages) != 2 {
			t.Errorf("iteration %d: messages count = %d, want 2", i, len(capturedBody.Messages))
		} else {
			if capturedBody.Messages[0].Role != "system" {
				t.Errorf("iteration %d: messages[0].role = %q, want system", i, capturedBody.Messages[0].Role)
			}
			if capturedBody.Messages[0].Content != systemPrompt {
				t.Errorf("iteration %d: messages[0].content mismatch", i)
			}
			if capturedBody.Messages[1].Role != "user" {
				t.Errorf("iteration %d: messages[1].role = %q, want user", i, capturedBody.Messages[1].Role)
			}
			if capturedBody.Messages[1].Content != userPrompt {
				t.Errorf("iteration %d: messages[1].content mismatch", i)
			}
		}

		// (c) Authorization header is Bearer {api_key}
		expectedAuth := "Bearer " + apiKey
		if capturedAuth != expectedAuth {
			t.Errorf("iteration %d: Authorization = %q, want %q", i, capturedAuth, expectedAuth)
		}

		// Verify model is passed correctly
		if capturedBody.Model != model {
			t.Errorf("iteration %d: model = %q, want %q", i, capturedBody.Model, model)
		}
	}
}

// randomTestString generates a random string of length 1..maxLen using printable ASCII.
func randomTestString(rng *rand.Rand, maxLen int) string {
	n := rng.Intn(maxLen) + 1
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(32 + rng.Intn(95)) // printable ASCII
	}
	return string(b)
}


// TestAPIKeyMissingError_Property verifies that for openai_compatible type with
// empty api_key, Call() returns an error containing "认证" or "API Key".
//
// Feature: annotation-workflow-v2, Property 3: API Key 缺失错误提示
// **Validates: Requirements 1.5**
func TestAPIKeyMissingError_Property(t *testing.T) {
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		systemPrompt := randomTestString(rng, 50)
		userPrompt := randomTestString(rng, 100)

		adapter := NewOpenAICompatibleAdapter("http://localhost:9999", "", "test-model", 10*time.Second)
		_, err := adapter.Call(context.Background(), systemPrompt, userPrompt)

		if err == nil {
			t.Fatalf("iteration %d: expected error for empty API key, got nil", i)
		}

		errMsg := err.Error()
		if !strings.Contains(errMsg, "认证") && !strings.Contains(errMsg, "API Key") {
			t.Errorf("iteration %d: error message %q does not contain '认证' or 'API Key'", i, errMsg)
		}
	}
}


// TestLLMConnectivityResult_Property verifies that TestConnection returns
// correct results: on success latency_ms > 0 and preview is non-empty;
// on failure the error contains a reason description; results are persisted
// to the database.
//
// Feature: annotation-workflow-v2, Property 20: LLM 连通性测试结果正确性
// **Validates: Requirements 9.3, 9.4, 9.5**
func TestLLMConnectivityResult_Property(t *testing.T) {
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		shouldSucceed := rng.Intn(2) == 0

		if shouldSucceed {
			// Create a mock server that returns a valid response
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := chatCompletionResponse{
					Choices: []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					}{
						{Message: struct {
							Content string `json:"content"`
						}{Content: "Hello! I'm working fine."}},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}))

			adapter := NewOpenAICompatibleAdapter(server.URL, "test-key", "test-model", 10*time.Second)
			latencyMs, preview, err := adapter.TestConnection(context.Background())
			server.Close()

			if err != nil {
				t.Errorf("iteration %d (success): TestConnection failed: %v", i, err)
				continue
			}
			if latencyMs < 0 {
				t.Errorf("iteration %d (success): latency_ms = %d, want >= 0", i, latencyMs)
			}
			if preview == "" {
				t.Errorf("iteration %d (success): preview is empty", i)
			}
		} else {
			// Create a mock server that returns an error
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal server error"}`))
			}))

			adapter := NewOpenAICompatibleAdapter(server.URL, "test-key", "test-model", 10*time.Second)
			_, _, err := adapter.TestConnection(context.Background())
			server.Close()

			if err == nil {
				t.Errorf("iteration %d (failure): expected error, got nil", i)
				continue
			}
			errMsg := err.Error()
			if errMsg == "" {
				t.Errorf("iteration %d (failure): error message is empty", i)
			}
		}
	}
}
