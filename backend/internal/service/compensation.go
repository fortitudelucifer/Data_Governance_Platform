package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// AuditLogger defines the interface for logging audit entries.
// A full implementation is provided by AuditService (Task 8.3).
type AuditLogger interface {
	Log(ctx context.Context, entry AuditEntry) error
}

// AuditEntry represents a single audit log record.
type AuditEntry struct {
	Action     string
	TargetType string
	TargetID   string
	UserID     uint
	Result     string
	Detail     string
}

// CompensationHandler manages cross-database operations across stores with
// compensation semantics(07 之后文档与关系行同库,仅剩对象存储是事务外的)。
type CompensationHandler struct {
	dbRepo    *repository.DB
	docDB  repository.DocumentDB
	logger    AuditLogger
	// assets（可选，主部署注入）：删数据集时逐资产清理 blob / 派生物 /
	// 载荷行。runner（纯文本）模式没有资产栈，保持 nil。
	assets *AssetService
}

// NewCompensationHandler creates a CompensationHandler with the given dependencies.
func NewCompensationHandler(
	dbRepo *repository.DB,
	docDB  repository.DocumentDB,
	logger AuditLogger,
) *CompensationHandler {
	return &CompensationHandler{
		dbRepo:    dbRepo,
		docDB: docDB,
		logger:    logger,
	}
}

// WithAssetService wires the asset stack so DeleteDatasetWithCompensation can
// clean blobs / derivatives / payload rows per asset（M7）。
func (h *CompensationHandler) WithAssetService(a *AssetService) *CompensationHandler {
	h.assets = a
	return h
}

// compensationETag generates a random etag for new documents.
func compensationETag() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate etag failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func resolveCompensationStage(data map[string]interface{}) string {
	if data != nil {
		if s, ok := data["annotation_stage"].(string); ok && s != "" {
			return s
		}
	}
	return StageNotAnnotated
}

// ImportWithCompensation imports document rows and then updates the dataset's
// doc_count, rolling the inserted rows back when the counter update fails.
// (07 迁移后两步已同库,保留补偿只是为了失败路径的审计日志形状不变。)
//
// Flow:
//  1. Convert ParsedDocument →paymodel.Document
//  2. Insert document rows
//  3. Update the dataset doc_count
//  4. On failure: delete the inserted document rows (compensation)
func (h *CompensationHandler) ImportWithCompensation(
	ctx context.Context,
	datasetID uint,
	docs []paymodel.ParsedDocument,
	userID uint,
) (*ImportReport, error) {
	if len(docs) == 0 {
		return &ImportReport{}, nil
	}

	// Convert ParsedDocument to paymodel.Document
	now := time.Now()
	payloadDocs := make([]paymodel.Document, 0, len(docs))
	docKeys := make([]string, 0, len(docs))
	for _, p := range docs {
		stage := resolveCompensationStage(p.Data)
		if p.Data == nil {
			p.Data = make(map[string]interface{})
		}
		p.Data["annotation_stage"] = stage

		etag, err := compensationETag()
		if err != nil {
			return nil, err
		}
		payloadDocs = append(payloadDocs, paymodel.Document{
			DatasetID:       datasetID,
			DocKey:          p.DocKey,
			Version:         1,
			IsActive:        true,
			UserID:          userID,
			AnnotationStage: stage,
			Data:            p.Data,
			CreatedBy:       userID,
			CreatedAt:       paymodel.NewJSONTime(now),
			UpdatedAt:       paymodel.NewJSONTime(now),
			ETag:            etag,
		})
		docKeys = append(docKeys, p.DocKey)
	}

	// Step 1: Insert documents
	if err := h.docDB.InsertDocuments(ctx, payloadDocs); err != nil {
		h.logEntry(ctx, AuditEntry{
			Action:     "import",
			TargetType: "dataset",
			TargetID:   fmt.Sprintf("%d", datasetID),
			UserID:     userID,
			Result:     "failure",
			Detail:     fmt.Sprintf("document insert failed: %v", err),
		})
		return nil, fmt.Errorf("文档写入失败: %w", err)
	}

	// Step 2: Update the relational DB doc_count by counting real active distinct doc_keys.
	count, err := h.docDB.CountActiveDocKeys(ctx, datasetID)
	if err != nil {
		if rbErr := h.docDB.DeleteDocumentsByKeys(ctx, datasetID, docKeys); rbErr != nil {
			slog.Error("compensation: rollback DeleteDocumentsByKeys failed", "dataset_id", datasetID, "error", rbErr)
		}
		h.logEntry(ctx, AuditEntry{
			Action:     "import",
			TargetType: "dataset",
			TargetID:   fmt.Sprintf("%d", datasetID),
			UserID:     userID,
			Result:     "compensated",
			Detail:     fmt.Sprintf("CountActiveDocKeys failed, documents rolled back: %v", err),
		})
		return nil, fmt.Errorf("数据集更新失败，已回滚: %w", err)
	}

	if err := h.dbRepo.UpdateDocCount(ctx, datasetID, count); err != nil {
		// Rollback documents
		if rbErr := h.docDB.DeleteDocumentsByKeys(ctx, datasetID, docKeys); rbErr != nil {
			slog.Error("compensation: rollback DeleteDocumentsByKeys failed", "dataset_id", datasetID, "error", rbErr)
		}
		h.logEntry(ctx, AuditEntry{
			Action:     "import",
			TargetType: "dataset",
			TargetID:   fmt.Sprintf("%d", datasetID),
			UserID:     userID,
			Result:     "compensated",
			Detail:     fmt.Sprintf("the relational DB UpdateDocCount failed, documents rolled back: %v", err),
		})
		return nil, fmt.Errorf("数据集更新失败，已回滚: %w", err)
	}

	// All succeeded
	h.logEntry(ctx, AuditEntry{
		Action:     "import",
		TargetType: "dataset",
		TargetID:   fmt.Sprintf("%d", datasetID),
		UserID:     userID,
		Result:     "success",
		Detail:     fmt.Sprintf("Imported %d documents", len(payloadDocs)),
	})

	return &ImportReport{
		ImportedCount: len(payloadDocs),
	}, nil
}

// DeleteDatasetWithCompensation deletes a dataset's documents first,
// then removes the relational DB record. If document deletion fails, the rest is left
// untouched. (07 之后同库,此补偿语义已退化为普通顺序删除;保留审计日志。)
func (h *CompensationHandler) DeleteDatasetWithCompensation(
	ctx context.Context,
	datasetID uint,
) error {
	// Step 1: Delete documents
	if err := h.docDB.DeleteDocumentsByDataset(ctx, datasetID); err != nil {
		h.logEntry(ctx, AuditEntry{
			Action:     "delete",
			TargetType: "dataset",
			TargetID:   fmt.Sprintf("%d", datasetID),
			UserID:     1,
			Result:     "failure",
			Detail:     fmt.Sprintf("document delete failed: %v", err),
		})
		return fmt.Errorf("delete documents failed: %w", err)
	}

	// Step 1.5（M7）: per-asset cleanup — 对象存储 blob（内容哈希被其它资产共享时
	// 跳过）、派生物 blob、载荷行(标注/track)。曾经这里什么都不做：删数据集只删
	// 文本文档 + 数据集行，资产行 / blob / 任务全部泄漏（CLAUDE.md《已知坏》）。
	// 顺序仍是「先载荷后关系行」（06 §6.1）：DeleteAsset 内部即如此。
	// 资产**行**本身随后由数据集行的 FK ON DELETE CASCADE 一并消失。
	if h.assets != nil {
		assetIDs, err := h.dbRepo.ListAssetIDsByDataset(ctx, datasetID)
		if err != nil {
			return fmt.Errorf("list assets for dataset %d: %w", datasetID, err)
		}
		for _, id := range assetIDs {
			if err := h.assets.DeleteAsset(ctx, id); err != nil && err != ErrAssetNotFound {
				h.logEntry(ctx, AuditEntry{
					Action: "delete", TargetType: "dataset", TargetID: fmt.Sprintf("%d", datasetID),
					UserID: 1, Result: "failure",
					Detail: fmt.Sprintf("asset %d cleanup failed: %v", id, err),
				})
				return fmt.Errorf("delete asset %d of dataset %d: %w", id, datasetID, err)
			}
		}
	}

	// Step 2: Delete the relational record. 剩余的关系行（annotation_tasks /
	// upload_sessions / batch_jobs / extraction_results / documents /
	// dataset_tags…）由 FK ON DELETE CASCADE 级联清掉——级联是 schema 的属性，
	// 不再依赖应用层记得（06 M7）。
	if err := h.dbRepo.DeleteDataset(ctx, datasetID); err != nil {
		h.logEntry(ctx, AuditEntry{
			Action:     "delete",
			TargetType: "dataset",
			TargetID:   fmt.Sprintf("%d", datasetID),
			UserID:     1,
			Result:     "compensation_failed",
			Detail:     fmt.Sprintf("dataset delete failed after document rows deleted: %v", err),
		})
		return fmt.Errorf("delete dataset failed (document rows already deleted): %w", err)
	}

	// All succeeded
	h.logEntry(ctx, AuditEntry{
		Action:     "delete",
		TargetType: "dataset",
		TargetID:   fmt.Sprintf("%d", datasetID),
		UserID:     1,
		Result:     "success",
		Detail:     fmt.Sprintf("Dataset %d deleted successfully", datasetID),
	})

	return nil
}

// logEntry is a helper that logs an audit entry, silently ignoring errors.
func (h *CompensationHandler) logEntry(ctx context.Context, entry AuditEntry) {
	if h.logger != nil {
		_ = h.logger.Log(ctx, entry)
	}
}
