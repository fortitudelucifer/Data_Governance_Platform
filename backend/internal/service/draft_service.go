package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Server-side annotation draft (C0.6b).
//
// # What this is for — and what it is NOT
//
// The local IndexedDB draft (C0.6a) already makes the crash-loss window ≈0.
// This layer solves a different problem: **resuming on another machine**.
// Start segmenting at home, continue at the office, lose at most one flush
// interval (MM_DRAFT_FLUSH_INTERVAL, default 5m).
//
// ⚠️ A draft is NOT an annotation. It never feeds export, never feeds QA, and
// is never a source of truth: exports read `track_snapshots` only (see
// CLAUDE.md 数据模型要点). Treating a draft as data would quietly let unreviewed,
// possibly half-finished work leak into deliverables. The draft is a *recovery
// aid* whose only legitimate use is "restore what I was doing".
//
// # Where it lives
//
// The 00 contract puts drafts under a short-lived `uploads/` prefix in object
// storage, not in the payload tables. Two reasons that matter:
//   - drafts churn every few minutes per active annotator; that write rate has
//     no business touching the relational spine;
//   - drafts are disposable by definition, so they must be trivially GC-able by
//     prefix without reasoning about foreign keys.
//
// Keyed by (task, user): the edit lock already serialises editors, but keying
// by user as well means a handover can never hand someone else's half-done
// strokes to the next person.

// DraftMaxBytes caps a single draft document. The op log is tens of bytes per
// stroke, so this is generous for hours of work; the cap exists so a runaway
// client cannot fill the bucket. Over-limit is a 413, not a silent truncation —
// a truncated op log replays into a *wrong* mask, which is exactly the class of
// failure this project treats as unacceptable.
const DraftMaxBytes = 8 << 20 // 8 MiB

// DraftService stores and retrieves per-(task,user) annotation drafts.
type DraftService struct {
	store ObjectStore
	ttl   time.Duration
}

func NewDraftService(store ObjectStore, ttl time.Duration) *DraftService {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &DraftService{store: store, ttl: ttl}
}

// DraftEnvelope wraps the client payload with the metadata needed to decide
// whether a draft is still applicable when it is read back.
type DraftEnvelope struct {
	TaskID uint   `json:"task_id"`
	UserID uint   `json:"user_id"`
	// Baseline records which server-side track versions the ops were recorded
	// against. On restore the client compares; if the server moved on, the
	// draft must not be replayed silently — the result would be a mask nobody
	// authored.
	Baseline  json.RawMessage `json:"baseline"`
	Ops       json.RawMessage `json:"ops"`
	OpCount   int             `json:"op_count"`
	SavedAt   time.Time       `json:"saved_at"`
	ClientRev int             `json:"client_rev"`
}

func draftKey(taskID, userID uint) string {
	// `uploads/` = the short-lived prefix the contract designates for drafts.
	return fmt.Sprintf("uploads/drafts/task_%d/user_%d.json", taskID, userID)
}

// Put writes (overwrites) the draft for a task+user.
func (s *DraftService) Put(ctx context.Context, env DraftEnvelope) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("draft: object store 未配置")
	}
	env.SavedAt = time.Now().UTC()
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if len(body) > DraftMaxBytes {
		return &DraftTooLargeError{Size: len(body), Max: DraftMaxBytes}
	}
	_, err = s.store.PutAt(ctx, draftKey(env.TaskID, env.UserID),
		bytes.NewReader(body), int64(len(body)), "application/json")
	return err
}

// Get returns the stored draft, or (nil, nil) when there is none.
//
// A draft older than the TTL is treated as absent **and deleted**: resuming
// work from an unbounded past is not "resuming", and a months-old draft
// offered as "unsaved changes" is far more likely to confuse than to help.
func (s *DraftService) Get(ctx context.Context, taskID, userID uint) (*DraftEnvelope, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	uri := s.store.URIForKey(draftKey(taskID, userID))
	rc, err := s.store.Get(ctx, uri)
	if err != nil {
		return nil, nil // absent (or unreadable) — never block opening a task
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, DraftMaxBytes+1))
	if err != nil {
		return nil, nil
	}
	var env DraftEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil
	}
	if time.Since(env.SavedAt) > s.ttl {
		_ = s.store.Delete(ctx, uri)
		return nil, nil
	}
	return &env, nil
}

// Delete drops the draft. Called after a successful save — leaving it behind
// would make the next visit report "unsaved changes" that are in fact saved,
// and an alarm that cries wolf gets ignored when it matters.
func (s *DraftService) Delete(ctx context.Context, taskID, userID uint) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Delete(ctx, s.store.URIForKey(draftKey(taskID, userID)))
}

// DraftTooLargeError signals an over-limit draft (mapped to HTTP 413).
type DraftTooLargeError struct {
	Size int
	Max  int
}

func (e *DraftTooLargeError) Error() string {
	return fmt.Sprintf("草稿过大：%d 字节，上限 %d。请先保存当前进度（保存后草稿会清空）。", e.Size, e.Max)
}
