package service

import (
	"context"
	"errors"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// AuditService implements the AuditLogger interface and provides query
// capabilities for audit logs stored in the relational DB.
type AuditService struct {
	dbRepo *repository.DB
}

// NewAuditService creates an AuditService backed by the given relational repository.
func NewAuditService(dbRepo *repository.DB) *AuditService {
	return &AuditService{dbRepo: dbRepo}
}

// Log writes an audit entry to the audit_logs table.
// It validates that action, target_id, and result are non-empty before persisting.
// The created_at timestamp is set automatically to the current time.
func (s *AuditService) Log(ctx context.Context, entry AuditEntry) error {
	if entry.Action == "" {
		return errors.New("audit entry: action must not be empty")
	}
	if entry.TargetID == "" {
		return errors.New("audit entry: target_id must not be empty")
	}
	if entry.Result == "" {
		return errors.New("audit entry: result must not be empty")
	}

	log := &dbmodel.AuditLog{
		Action:     entry.Action,
		TargetType: entry.TargetType,
		TargetID:   entry.TargetID,
		UserID:     entry.UserID,
		Result:     entry.Result,
		Detail:     entry.Detail,
		CreatedAt:  time.Now(),
	}

	return s.dbRepo.CreateAuditLog(ctx, log)
}

// Query retrieves audit logs matching the given filter with pagination support.
// It delegates to the repository's QueryAuditLogs method.
func (s *AuditService) Query(ctx context.Context, filter repository.AuditLogFilter) (*repository.AuditLogResult, error) {
	return s.dbRepo.QueryAuditLogs(ctx, filter)
}
