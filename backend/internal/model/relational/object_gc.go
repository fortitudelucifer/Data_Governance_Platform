package relational

import "time"

// ObjectGC is one queued object-store cleanup — a row in the durable GC outbox
// (migration 000005). Enqueued in the same DB transaction as the relational
// deletes that turned these objects into garbage, then drained by the object-GC
// janitor (Delete / DeletePrefix, with backoff retry).
type ObjectGC struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	StorageURI    string    `json:"storage_uri"`
	IsPrefix      bool      `json:"is_prefix"`
	Reason        string    `json:"reason"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error"`
	CreatedAt     time.Time `json:"created_at"`
}

// TableName pins the table (GORM would otherwise pluralise to object_gcs).
func (ObjectGC) TableName() string { return "object_gc_queue" }
