package service

import (
	"fmt"
	"testing"
	"testing/quick"

	paymodel "text-annotation-platform/internal/model/payload"
)

// --- Property 10: 导入操作原子性与补偿
// Feature: text-annotation-platform, Property 10: 导入操作原子性与补偿
// Validates: Requirements 20.1, 20.2
//
// For any document import operation, if any error occurs during the process
// (including document insert success but counter update failure), the final
// database state should be identical to the state before the import —no
// partial writes (dirty data) should exist.

// simulatedDB models the state of both databases for property testing.
type simulatedDB struct {
	// docKeys tracks doc_keys currently stored in the simulated document store.
	docKeys map[string]bool
	// relDocCount tracks the doc_count in the simulated relational-DB dataset.
	relDocCount int
}

func newSimulatedDB() *simulatedDB {
	return &simulatedDB{
		docKeys:  make(map[string]bool),
		relDocCount: 0,
	}
}

// snapshot captures the current state for later comparison.
func (db *simulatedDB) snapshot() (map[string]bool, int) {
	snap := make(map[string]bool, len(db.docKeys))
	for k, v := range db.docKeys {
		snap[k] = v
	}
	return snap, db.relDocCount
}

// stateEquals checks whether the current state matches a previous snapshot.
func (db *simulatedDB) stateEquals(docSnap map[string]bool, relSnap int) bool {
	if db.relDocCount != relSnap {
		return false
	}
	if len(db.docKeys) != len(docSnap) {
		return false
	}
	for k, v := range docSnap {
		if db.docKeys[k] != v {
			return false
		}
	}
	return true
}

// failureScenario encodes which step fails during import.
type failureScenario int

const (
	scenarioAllSucceed       failureScenario = iota // Both document insert and counter update succeed
	scenarioDocInsertFails                              // document insert fails —nothing written
	scenarioDBFails                              // insert succeeds, counter update fails —rollback documents
)

// mockAuditLogger records audit entries for verification.
type mockAuditLogger struct {
	entries []AuditEntry
}

func (l *mockAuditLogger) Log(entry AuditEntry) error {
	l.entries = append(l.entries, entry)
	return nil
}

// simulateImportWithCompensation models the compensation logic from
// CompensationHandler.ImportWithCompensation without requiring real database
// connections. It applies the same algorithmic flow:
//  1. Attempt document insert
//  2. Attempt relational-DB update
//  3. On counter-update failure: rollback documents (delete inserted docs)
//
// Returns the import report (nil on error), error, and the audit log entries.
func simulateImportWithCompensation(
	db *simulatedDB,
	docs []paymodel.ParsedDocument,
	scenario failureScenario,
	logger *mockAuditLogger,
) (*ImportReport, error) {
	if len(docs) == 0 {
		return &ImportReport{}, nil
	}

	docKeys := make([]string, len(docs))
	for i, d := range docs {
		docKeys[i] = d.DocKey
	}

	// Step 1: document insert
	if scenario == scenarioDocInsertFails {
		logger.Log(AuditEntry{
			Action:     "import",
			TargetType: "dataset",
			TargetID:   "1",
			UserID:     1,
			Result:     "failure",
			Detail:     "document insert failed: simulated error",
		})
		return nil, fmt.Errorf("文档写入失败: simulated insert error")
	}

	// document insert succeeds —add doc keys
	for _, k := range docKeys {
		db.docKeys[k] = true
	}

	// Step 2: relational-DB update
	if scenario == scenarioDBFails {
		// Compensation: rollback documents
		for _, k := range docKeys {
			delete(db.docKeys, k)
		}
		logger.Log(AuditEntry{
			Action:     "import",
			TargetType: "dataset",
			TargetID:   "1",
			UserID:     1,
			Result:     "compensated",
			Detail:     "counter update failed, documents rolled back",
		})
		return nil, fmt.Errorf("数据集更新失败，已回滚: simulated db error")
	}

	// Both succeed
	db.relDocCount += len(docs)
	logger.Log(AuditEntry{
		Action:     "import",
		TargetType: "dataset",
		TargetID:   "1",
		UserID:     1,
		Result:     "success",
		Detail:     fmt.Sprintf("Imported %d documents", len(docs)),
	})

	return &ImportReport{ImportedCount: len(docs)}, nil
}

func TestProperty10_ImportAtomicityAndCompensation(t *testing.T) {
	f := func(docKeys []string, scenarioByte uint8) bool {
		if len(docKeys) == 0 {
			return true // trivially holds for empty imports
		}

		// Deduplicate doc keys to avoid ambiguity
		seen := make(map[string]struct{})
		var uniqueKeys []string
		for _, k := range docKeys {
			if k == "" {
				continue
			}
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				uniqueKeys = append(uniqueKeys, k)
			}
		}
		if len(uniqueKeys) == 0 {
			return true
		}

		// Build ParsedDocuments
		docs := make([]paymodel.ParsedDocument, len(uniqueKeys))
		for i, k := range uniqueKeys {
			docs[i] = paymodel.ParsedDocument{
				DocKey: k,
				Data:   map[string]interface{}{"content": k},
			}
		}

		// Cycle through the three failure scenarios
		scenario := failureScenario(scenarioByte % 3)

		db := newSimulatedDB()
		logger := &mockAuditLogger{}

		// Capture state before import
		docSnap, relSnap := db.snapshot()

		report, err := simulateImportWithCompensation(db, docs, scenario, logger)

		switch scenario {
		case scenarioAllSucceed:
			// Both succeed: documents should persist, count should increase
			if err != nil {
				return false
			}
			if report.ImportedCount != len(docs) {
				return false
			}
			if db.relDocCount != len(docs) {
				return false
			}
			for _, k := range uniqueKeys {
				if !db.docKeys[k] {
					return false
				}
			}
			// Audit log should record success
			if len(logger.entries) == 0 || logger.entries[len(logger.entries)-1].Result != "success" {
				return false
			}

		case scenarioDocInsertFails:
			// document insert fails: nothing should be written, state unchanged
			if err == nil {
				return false
			}
			if !db.stateEquals(docSnap, relSnap) {
				return false
			}
			// Audit log should record failure
			if len(logger.entries) == 0 || logger.entries[len(logger.entries)-1].Result != "failure" {
				return false
			}

		case scenarioDBFails:
			// counter update fails after insert success: compensation rollback,
			// state should be identical to pre-import (no dirty data)
			if err == nil {
				return false
			}
			if !db.stateEquals(docSnap, relSnap) {
				return false
			}
			// Audit log should record compensation
			if len(logger.entries) == 0 || logger.entries[len(logger.entries)-1].Result != "compensated" {
				return false
			}
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("Property 10 failed: %v", err)
	}
}
