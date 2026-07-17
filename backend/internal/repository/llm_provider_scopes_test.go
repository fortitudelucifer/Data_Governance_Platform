package repository

import (
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/testutil"

	"gorm.io/gorm"
)

func newScopeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.DB(t, RunMigrations)
}

func TestEnabledTextChatProvidersScope_FiltersCorrectly(t *testing.T) {
	db := newScopeTestDB(t)

	// Seed:
	//   - kept: enabled + text.chat
	//   - kept: enabled + capability_type=""    (legacy)
	//   - skipped: disabled even though text.chat
	//   - skipped: enabled but vlm.structured
	// （capability_type=NULL 的 legacy 用例已删：列是 NOT NULL，NULL 在新 schema
	//   里是不可表示状态；scope 里的 IS NULL 子句只是无害的防御。）
	rows := []*dbmodel.LLMProvider{
		{Name: "a-textchat", Type: "openai", Endpoint: "http://a", Model: "m", Enabled: true, CapabilityType: "text.chat"},
		{Name: "b-legacy-empty", Type: "openai", Endpoint: "http://b", Model: "m", Enabled: true, CapabilityType: ""},
		{Name: "c-vlm-enabled", Type: "openai", Endpoint: "http://c", Model: "m", Enabled: true, CapabilityType: "vlm.structured"},
		{Name: "d-textchat-disabled", Type: "openai", Endpoint: "http://d", Model: "m", Enabled: false, CapabilityType: "text.chat"},
	}
	for _, r := range rows {
		wantEnabled := r.Enabled
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.Name, err)
		}
		// GORM's `default:true` tag makes it skip "enabled = false" on insert.
		// Patch it explicitly to honour what the test asked for.
		if !wantEnabled {
			if err := db.Model(&dbmodel.LLMProvider{}).
				Where("id = ?", r.ID).
				Update("enabled", false).Error; err != nil {
				t.Fatalf("force-disable %s: %v", r.Name, err)
			}
		}
	}

	var got []dbmodel.LLMProvider
	if err := db.Scopes(EnabledTextChatProvidersScope).Find(&got).Error; err != nil {
		t.Fatalf("query: %v", err)
	}

	kept := map[string]bool{}
	for _, p := range got {
		kept[p.Name] = true
	}

	for _, want := range []string{"a-textchat", "b-legacy-empty"} {
		if !kept[want] {
			t.Errorf("expected %q in result", want)
		}
	}
	for _, drop := range []string{"c-vlm-enabled", "d-textchat-disabled"} {
		if kept[drop] {
			t.Errorf("expected %q to be filtered out", drop)
		}
	}
}
