// Package repository — LLM provider GORM scopes.
//
// These scopes centralise the SQL filters that were duplicated across
// LLMService methods (see TD-28). Use them with GORM's .Scopes(...) chain:
//
//	var providers []db.LLMProvider
//	db.Scopes(repository.EnabledTextChatProvidersScope).Find(&providers)
//
// Any future column addition to the "text.chat" predicate (e.g. soft-delete,
// tenant filter) is made in one place.
package repository

import "gorm.io/gorm"

// EnabledTextChatProvidersScope filters llm_providers to:
//
//   - enabled = true, AND
//   - capability_type = 'text.chat', OR capability_type IS NULL, OR capability_type = ''
//
// The NULL / empty-string clauses keep Phase 1 legacy rows (which were
// inserted before capability_type existed) compatible with the Phase 2
// adapter routing logic.
func EnabledTextChatProvidersScope(db *gorm.DB) *gorm.DB {
	return db.Where(
		"enabled = ? AND (capability_type = ? OR capability_type IS NULL OR capability_type = ?)",
		true, "text.chat", "",
	)
}
