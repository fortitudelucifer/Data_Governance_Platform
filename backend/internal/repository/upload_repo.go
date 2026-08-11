package repository

import (
	"context"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// upload_repo.go — persistence for resumable multipart upload sessions (T0.2).

// CreateUploadSession inserts a new session row.
func (r *DB) CreateUploadSession(ctx context.Context, s *dbmodel.UploadSession) error {
	return r.DB.WithContext(ctx).Create(s).Error
}

// FindUploadSession looks up a session by its public session id.
func (r *DB) FindUploadSession(ctx context.Context, sessionID string) (*dbmodel.UploadSession, error) {
	var s dbmodel.UploadSession
	if err := r.DB.WithContext(ctx).Where("session_id = ?", sessionID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateUploadSession applies partial updates to a session.
func (r *DB) UpdateUploadSession(ctx context.Context, sessionID string, updates map[string]interface{}) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.UploadSession{}).
		Where("session_id = ?", sessionID).Updates(updates).Error
}

// CASUploadSessionStatus atomically flips a session's status from expected→next
// (plus optional extra updates), returning whether the row matched (#3). This
// serialises concurrent Complete calls: only the caller that wins
// pending→completing proceeds; the loser gets applied=false instead of both
// racing on the same pending session and one poisoning the other's result.
func (r *DB) CASUploadSessionStatus(ctx context.Context, sessionID, expected, next string, extra map[string]interface{}) (bool, error) {
	updates := map[string]interface{}{"status": next}
	for k, v := range extra {
		updates[k] = v
	}
	res := r.DB.WithContext(ctx).Model(&dbmodel.UploadSession{}).
		Where("session_id = ? AND status = ?", sessionID, expected).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ListReclaimableUploadSessions returns pending sessions whose lease/expiry has
// elapsed — candidates for abort + temp cleanup by the janitor.
func (r *DB) ListReclaimableUploadSessions(ctx context.Context, now time.Time, limit int) ([]dbmodel.UploadSession, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []dbmodel.UploadSession
	err := r.DB.WithContext(ctx).
		Where("status IN ?", []string{dbmodel.UploadPending, dbmodel.UploadFailed}).
		Where("expires_at <= ?", now).
		Order("expires_at asc").Limit(limit).Find(&out).Error
	return out, err
}
