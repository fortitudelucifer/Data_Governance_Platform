package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
)

func TestASRAdapter_Invoke(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart, got %s", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		if r.FormValue("task_id") != "1" {
			t.Errorf("task_id=%s", r.FormValue("task_id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"paraformer-large","model_version":"2026-06","language":"zh","duration_ms":9587,
			"segments":[{"start_ms":1200,"end_ms":3400,"text":"你好 ","speaker":"spk0","confidence":0.9}]}`)
	}))
	defer srv.Close()

	a := NewASRHTTPAdapter(ASRAdapterConfig{
		Endpoint: srv.URL,
		Reader:   func(_ context.Context, _ string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("FAKEAUDIO")), nil },
	})
	resp, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: 1, AssetID: 2, RunID: "r1", AssetURI: "local://x", MIME: "audio/mpeg"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Status != "success" || resp.ASR == nil {
		t.Fatalf("resp=%+v", resp)
	}
	if len(resp.ASR.Segments) != 1 {
		t.Fatalf("want 1 segment, got %d", len(resp.ASR.Segments))
	}
	s := resp.ASR.Segments[0]
	if s.StartMs != 1200 || s.EndMs != 3400 || s.Text != "你好" || s.Speaker != "spk0" {
		t.Fatalf("segment mismatch: %+v", s)
	}
	if resp.Provider.ModelID != "paraformer-large" || resp.Provider.Version != "2026-06" {
		t.Fatalf("provider mismatch: %+v", resp.Provider)
	}
	if resp.ASR.Language != "zh" || resp.ASR.DurationMs != 9587 {
		t.Fatalf("meta mismatch: %+v", resp.ASR)
	}
}

func TestCapabilityForStrategy_ASR(t *testing.T) {
	if got := capabilityForStrategy(dbmodel.RouteASRFirst); got != CapabilityASRTranscribe {
		t.Fatalf("RouteASRFirst → %q, want %q", got, CapabilityASRTranscribe)
	}
}
