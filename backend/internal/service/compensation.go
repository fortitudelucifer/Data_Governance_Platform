package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"gorm.io/gorm"
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

// gatherDatasetBlobs collects every object-store URI owned by a dataset's assets
// (source blobs + derivatives) for GC-outbox enqueue (#19). Source keys are
// dataset-scoped (KeyForContent embeds the dataset id) and deduped one-per-URI
// within a dataset, so a dataset's blobs are never referenced by another dataset
// — safe to enqueue all of them.
func (h *CompensationHandler) gatherDatasetBlobs(ctx context.Context, datasetID uint) ([]dbmodel.ObjectGC, error) {
	assetIDs, err := h.dbRepo.ListAssetIDsByDataset(ctx, datasetID)
	if err != nil {
		return nil, fmt.Errorf("list assets for dataset %d: %w", datasetID, err)
	}
	var items []dbmodel.ObjectGC
	for _, aid := range assetIDs {
		asset, err := h.dbRepo.FindAssetByID(ctx, aid)
		if err != nil {
			return nil, fmt.Errorf("load asset %d: %w", aid, err)
		}
		if asset != nil && asset.StorageURI != "" {
			items = append(items, dbmodel.ObjectGC{StorageURI: asset.StorageURI, Reason: "delete dataset source"})
		}
		derivs, err := h.dbRepo.ListDerivatives(ctx, aid)
		if err != nil {
			return nil, fmt.Errorf("list derivatives of asset %d: %w", aid, err)
		}
		for _, d := range derivs {
			if d.StorageURI == "" {
				continue
			}
			items = append(items, dbmodel.ObjectGC{
				StorageURI: d.StorageURI, IsPrefix: strings.HasSuffix(d.StorageURI, "/"),
				Reason: "delete dataset derivative",
			})
		}
	}
	return items, nil
}

// DeleteDatasetWithCompensation deletes a dataset **atomically** (#19).
//
// 旧实现是"先独立删 documents、再逐资产提交、最后删数据集行"——三段独立提交,任何
// 一段(第 3 个资产 / 最终 dataset DELETE)失败,前面已删的文档/资产不可恢复,数据集
// 却还在 → 半残数据集。改成:所有关系行由**数据集行的 FK ON DELETE CASCADE 一次原子
// 清掉**(assets/tasks/documents/payload 全挂在 datasets 上);对象存储 blob(不吃
// cascade)先在**同事务**登记进 GC outbox,由常驻 janitor 删除并重试到成功。于是要么
// 整个数据集连同 blob 都干净消失,要么什么都没动。
func (h *CompensationHandler) DeleteDatasetWithCompensation(
	ctx context.Context,
	datasetID uint,
) error {
	var gcItems []dbmodel.ObjectGC
	if h.assets != nil { // runner(纯文本)模式没有资产栈,只删关系行(文档随 cascade 走)
		items, err := h.gatherDatasetBlobs(ctx, datasetID)
		if err != nil {
			h.logEntry(ctx, AuditEntry{
				Action: "delete", TargetType: "dataset", TargetID: fmt.Sprintf("%d", datasetID),
				UserID: 1, Result: "failure", Detail: fmt.Sprintf("gather blobs failed: %v", err),
			})
			return err
		}
		gcItems = items
	}

	// 一个事务:登记待删 blob(outbox)+ 删数据集行(cascade 清所有关系行)。
	if err := h.dbRepo.DB.Transaction(func(tx *gorm.DB) error {
		if err := h.dbRepo.EnqueueObjectGCTx(ctx, tx, gcItems); err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec("DELETE FROM datasets WHERE id = ?", datasetID).Error
	}); err != nil {
		h.logEntry(ctx, AuditEntry{
			Action: "delete", TargetType: "dataset", TargetID: fmt.Sprintf("%d", datasetID),
			UserID: 1, Result: "failure", Detail: fmt.Sprintf("atomic dataset delete failed: %v", err),
		})
		return fmt.Errorf("delete dataset %d: %w", datasetID, err)
	}

	// 关系行已原子删除;blob 由 janitor 排空 outbox(重试到成功)。这里不做快路径,
	// janitor 一个周期内即清完,删除不是即时关键路径。
	h.logEntry(ctx, AuditEntry{
		Action:     "delete",
		TargetType: "dataset",
		TargetID:   fmt.Sprintf("%d", datasetID),
		UserID:     1,
		Result:     "success",
		Detail:     fmt.Sprintf("Dataset %d deleted atomically (%d blobs queued for GC)", datasetID, len(gcItems)),
	})

	return nil
}

// logEntry is a helper that logs an audit entry, silently ignoring errors.
func (h *CompensationHandler) logEntry(ctx context.Context, entry AuditEntry) {
	if h.logger != nil {
		_ = h.logger.Log(ctx, entry)
	}
}
