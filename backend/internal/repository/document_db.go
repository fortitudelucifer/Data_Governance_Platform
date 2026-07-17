package repository

import (
	"context"
	"time"

	"text-annotation-platform/internal/model"
	paymodel "text-annotation-platform/internal/model/payload"
)

// DocumentDB is the common interface for document persistence.
// 唯一实现是 RelationalDocRepo(Postgres documents 表,方案-07 起)。
type DocumentDB interface {
	EnsureIndexes(ctx context.Context) error
	InsertDocuments(ctx context.Context, docs []paymodel.Document) error
	InsertDocument(ctx context.Context, doc paymodel.Document) error
	FindActiveDocument(ctx context.Context, datasetID *uint, docKey string, userID uint) (*paymodel.Document, error)
	FindVersionHistory(ctx context.Context, datasetID *uint, docKey string, userID uint) ([]paymodel.Document, error)
	FindDocumentsByDatasetPaginated(ctx context.Context, datasetID uint, page, pageSize int, userID uint, query string) (*PaginatedResult, error)
	FindDocumentsByDataset(ctx context.Context, datasetID uint, filter map[string]interface{}, userID uint) ([]paymodel.Document, error)
	FindDocKeysByRange(ctx context.Context, datasetID uint, skip, limit int64) ([]string, error)
	CountActiveDocKeys(ctx context.Context, datasetID uint) (int, error)
	CountActiveDocKeysByDatasets(ctx context.Context, datasetIDs []uint) (map[uint]int, error)
	FindExistingDocKeys(ctx context.Context, datasetID uint, docKeys []string, userID uint) ([]string, error)
	DeactivateVersion(ctx context.Context, datasetID *uint, docKey string, version int) error
	DeleteDocumentByKey(ctx context.Context, datasetID uint, docKey string) (int64, error)
	DeleteDocumentsByDataset(ctx context.Context, datasetID uint) error
	DeleteDocumentsByKeys(ctx context.Context, datasetID uint, docKeys []string) error
	FindDocumentsSince(ctx context.Context, datasetID uint, since *time.Time, isActive bool, userID uint, docKeys []string) ([]paymodel.Document, error)

	// Stage and Content updates extracted from services
	UpdateDocStage(ctx context.Context, datasetID *uint, docKey, stage string) error
	UpdateDocumentQAPairsAndStage(ctx context.Context, datasetID *uint, docKey string, qaPairs []paymodel.QAPair, stage string) error
	UpdateDocumentRefinement(ctx context.Context, datasetID *uint, docKey string, score int, reasoning string, version string, userID uint, stage string) error
	RollbackDocumentRefinement(ctx context.Context, datasetID *uint, docKey string) error

	// SetDocumentsDeadline 批量设置/清除活跃文档截止时间（管理员分配任务用，存于 data.deadline；空串=清除）
	SetDocumentsDeadline(ctx context.Context, datasetID uint, docKeys []string, deadline string) error
	// AssignDocuments 批量设置/清除活跃文档任务元数据；uint=0 清空人员，deadline_at 空串清除截止。
	AssignDocuments(ctx context.Context, datasetID uint, docKeys []string, assigneeID, reviewerID *uint, deadlineAt *string) error

	// Refinement Service (ETag optimistic concurrency)
	UpdateDocumentRefinementCursor(ctx context.Context, datasetID *uint, docKey string, etag string, cursor int, newEtag string, newStage string) error
	UpdateDocumentQAPairsAndCursor(ctx context.Context, datasetID *uint, docKey string, etag string, qaPairs []paymodel.QAPair, cursor int, newEtag string, userID *uint, annotatorName string) error

	// Dashboard analytics
	GetDashboardStats(ctx context.Context, datasetID *uint) (*model.DashboardStats, error)
	GetDailyTrend(ctx context.Context, days int, datasetID *uint) ([]model.DailyTrend, error)
	GetAnnotatorStats(ctx context.Context, datasetID *uint) ([]model.AnnotatorStats, error)
}
