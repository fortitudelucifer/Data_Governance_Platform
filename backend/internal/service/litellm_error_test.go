package service

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseLLMError_DashScopeAccessDenied(t *testing.T) {
	body := []byte(`{"error":{"message":"Access denied. For details, see: https://help.aliyun.com/...","type":"access_denied","param":null,"code":"access_denied"}}`)
	e := parseLLMError(403, body)
	if e.StatusCode != 403 {
		t.Errorf("status = %d, want 403", e.StatusCode)
	}
	if e.Type != "access_denied" {
		t.Errorf("type = %q, want access_denied", e.Type)
	}
	if !e.Permanent() {
		t.Error("403 access_denied should be Permanent")
	}
}

func TestParseLLMError_LiteLLMWrapped403InMessage(t *testing.T) {
	// LiteLLM sometimes returns a non-403 HTTP status but the real reason is
	// only in the message; classification must still flag it permanent.
	body := []byte(`{"error":{"message":"litellm.APIError: OpenAIException - Access denied. Received Model Group=qwen-vl","type":null,"param":null,"code":"403"}}`)
	e := parseLLMError(500, body)
	if !e.Permanent() {
		t.Error("wrapped Access denied (HTTP 500) should still be Permanent via message")
	}
}

func TestPermanentVsTransient(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{400, true}, {401, true}, {403, true}, {404, true}, {422, true},
		{429, false}, {500, false}, {502, false}, {503, false}, {504, false},
	}
	for _, c := range cases {
		e := &LLMAPIError{StatusCode: c.status}
		if got := e.Permanent(); got != c.want {
			t.Errorf("status %d Permanent()=%v, want %v", c.status, got, c.want)
		}
	}
}

func TestIsPermanentLLMError_WrappedAndNonLLM(t *testing.T) {
	// Non-LLM errors are transient (keep existing backoff behaviour).
	if isPermanentLLMError(fmt.Errorf("dial tcp: timeout")) {
		t.Error("non-LLM error should not be permanent")
	}
	// Wrapped LLMAPIError must still be detected via errors.As.
	wrapped := fmt.Errorf("vlm invoke: %w", &LLMAPIError{StatusCode: 403, Type: "access_denied"})
	if !isPermanentLLMError(wrapped) {
		t.Error("wrapped 403 should be permanent")
	}
}

func TestFriendlyLLMError(t *testing.T) {
	msg := FriendlyLLMError(&LLMAPIError{StatusCode: 403, Type: "access_denied", Message: "Access denied"})
	if !strings.Contains(msg, "未对当前账号开通") {
		t.Errorf("friendly access_denied message unexpected: %q", msg)
	}
	// Non-LLM error passes through unchanged.
	if got := FriendlyLLMError(fmt.Errorf("boom")); got != "boom" {
		t.Errorf("non-LLM passthrough = %q, want boom", got)
	}
}
