package service

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

// stubAdapter is a minimal CapabilityAdapter for testing Register().
type stubAdapter struct{ cap string }

func (s *stubAdapter) Capability() string { return s.cap }
func (s *stubAdapter) Invoke(_ context.Context, _ CapabilityRequest) (CapabilityResponse, error) {
	return CapabilityResponse{}, nil
}

func TestRegister_OverwriteLogsWarning(t *testing.T) {
	svc := NewCapabilityService(nil)

	// Capture log output.
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	svc.Register(&stubAdapter{"ocr.structure"})
	svc.Register(&stubAdapter{"ocr.structure"}) // second registration → overwrite

	output := buf.String()
	if !strings.Contains(output, "overwriting") {
		t.Errorf("expected log to contain 'overwriting', got: %s", output)
	}
	if !strings.Contains(output, "ocr.structure") {
		t.Errorf("expected log to contain capability type 'ocr.structure', got: %s", output)
	}
}

func TestRegister_FirstRegistrationNoLog(t *testing.T) {
	svc := NewCapabilityService(nil)

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	svc.Register(&stubAdapter{"vlm.structured"})

	if strings.Contains(buf.String(), "overwriting") {
		t.Error("expected no 'overwriting' log on first registration")
	}
}

func TestRegister_NilAdapterIgnored(t *testing.T) {
	svc := NewCapabilityService(nil)
	// Must not panic.
	svc.Register(nil)
}
