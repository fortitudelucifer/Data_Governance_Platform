package relational

import "time"

// LLMProvider stores configuration for an LLM/capability service endpoint.
// Phase 2 fields (CapabilityType / ProviderKind / ExtraConfig / Priority) are
// added via ALTER TABLE; existing rows are backfilled by cmd/main.go at startup.
type LLMProvider struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	Name              string     `gorm:"size:100;not null" json:"name"`
	Type              string     `gorm:"size:30;not null" json:"type"`
	Endpoint          string     `gorm:"size:500;not null" json:"endpoint"`
	APIKey            string     `gorm:"size:500" json:"api_key,omitempty"`
	Model             string     `gorm:"size:100;not null" json:"model"`
	Enabled           bool       `gorm:"not null;default:true" json:"enabled"`
	EnabledSet        bool       `gorm:"-" json:"-"` // internal flag, not persisted
	TimeoutSeconds    int        `gorm:"not null;default:60" json:"timeout_seconds"`
	MaxRetries        int        `gorm:"not null;default:3" json:"max_retries"`
	LastTestSuccess   *bool      `json:"last_test_success"`
	LastTestAt        *time.Time `json:"last_test_at"`
	LastTestLatencyMs *int       `json:"last_test_latency_ms"`
	// Phase 2: capability center fields
	CapabilityType string `gorm:"size:64;default:'text.chat'" json:"capability_type"`
	ProviderKind   string `gorm:"size:32" json:"provider_kind"`
	ExtraConfig    string `gorm:"type:text" json:"extra_config,omitempty"`
	Priority       int    `gorm:"not null;default:0" json:"priority"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
