package util

import (
	"testing"
	"time"
)

// TestAppLocation_Default verifies that AppLocation returns a non-nil
// Asia/Shanghai-equivalent location after package init.
func TestAppLocation_Default(t *testing.T) {
	loc := AppLocation()
	if loc == nil {
		t.Fatal("AppLocation returned nil after init")
	}
	// Asia/Shanghai is +08:00 year-round; verify offset matches.
	_, offset := time.Now().In(loc).Zone()
	if offset != 8*3600 {
		t.Errorf("default location offset = %d sec, want 28800 (UTC+8)", offset)
	}
}

func TestSetAppLocation_UTC(t *testing.T) {
	original := AppLocation()
	defer SetAppLocation(original.String())

	if err := SetAppLocation("UTC"); err != nil {
		t.Fatalf("SetAppLocation(UTC) failed: %v", err)
	}
	loc := AppLocation()
	_, offset := time.Now().In(loc).Zone()
	if offset != 0 {
		t.Errorf("UTC offset = %d, want 0", offset)
	}
}

func TestSetAppLocation_AsiaShanghai(t *testing.T) {
	original := AppLocation()
	defer SetAppLocation(original.String())

	// Switch to UTC first so we can detect a real change back to Shanghai.
	_ = SetAppLocation("UTC")
	if err := SetAppLocation("Asia/Shanghai"); err != nil {
		t.Fatalf("SetAppLocation(Asia/Shanghai) failed: %v", err)
	}
	loc := AppLocation()
	_, offset := time.Now().In(loc).Zone()
	if offset != 8*3600 {
		t.Errorf("Asia/Shanghai offset = %d, want 28800", offset)
	}
}

func TestSetAppLocation_EmptyIsNoOp(t *testing.T) {
	original := AppLocation()
	if err := SetAppLocation(""); err != nil {
		t.Errorf("empty name should be no-op, got error: %v", err)
	}
	if AppLocation() != original {
		t.Error("AppLocation changed after empty SetAppLocation")
	}
}

func TestSetAppLocation_Invalid(t *testing.T) {
	original := AppLocation()
	defer SetAppLocation(original.String())

	if err := SetAppLocation("Not/A_Real_Zone"); err == nil {
		t.Error("expected error for invalid timezone name")
	}
	// On error, the previous location must remain installed.
	if AppLocation() != original {
		t.Error("AppLocation was modified despite invalid name")
	}
}

func TestSetAppLocation_AffectsTimeFormatting(t *testing.T) {
	original := AppLocation()
	defer SetAppLocation(original.String())

	// Build a fixed UTC instant.
	moment := time.Date(2026, 5, 29, 0, 30, 0, 0, time.UTC)

	_ = SetAppLocation("UTC")
	utcFmt := moment.In(AppLocation()).Format("2006-01-02 15:04:05")

	_ = SetAppLocation("Asia/Shanghai")
	cstFmt := moment.In(AppLocation()).Format("2006-01-02 15:04:05")

	if utcFmt == cstFmt {
		t.Errorf("UTC vs Asia/Shanghai should differ; both gave %q", utcFmt)
	}
	if utcFmt != "2026-05-29 00:30:00" {
		t.Errorf("unexpected UTC format: %s", utcFmt)
	}
	if cstFmt != "2026-05-29 08:30:00" {
		t.Errorf("unexpected CST format: %s", cstFmt)
	}
}
