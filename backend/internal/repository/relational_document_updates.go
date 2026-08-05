package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/gorm"
)

func (r *RelationalDocRepo) UpdateDocStage(ctx context.Context, datasetID *uint, docKey, stage string) error {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("doc_key = ? AND is_active = ?", docKey, true)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	res := query.
		Updates(map[string]interface{}{
			"annotation_stage": stage,
			"updated_at":       time.Now(),
		})
	return res.Error
}

// SetDocumentsDeadline 批量设置/清除活跃文档截止时间（存于 data.deadline；空串=清除）。
func (r *RelationalDocRepo) SetDocumentsDeadline(ctx context.Context, datasetID uint, docKeys []string, deadline string) error {
	if len(docKeys) == 0 {
		return nil
	}
	var docs []dbmodel.Document
	if err := r.DB.WithContext(ctx).
		Where("dataset_id = ? AND doc_key IN ? AND is_active = ?", datasetID, docKeys, true).
		Find(&docs).Error; err != nil {
		return err
	}
	for _, d := range docs {
		var data map[string]interface{}
		if len(d.Data) > 0 {
			_ = json.Unmarshal(d.Data, &data)
		}
		if data == nil {
			data = make(map[string]interface{})
		}
		if deadline == "" {
			delete(data, "deadline")
		} else {
			data["deadline"] = deadline
		}
		raw, _ := json.Marshal(data)
		if err := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
			Where("dataset_id = ? AND doc_key = ? AND is_active = ?", datasetID, d.DocKey, true).
			Updates(map[string]interface{}{"data": dbmodel.JSON(raw), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
	}
	return nil
}

// AssignDocuments 批量设置/清除活跃文档任务元数据，存于 data.* 载荷内,不提升为列。
func (r *RelationalDocRepo) AssignDocuments(ctx context.Context, datasetID uint, docKeys []string, assigneeID, reviewerID *uint, deadlineAt *string) error {
	if len(docKeys) == 0 {
		return nil
	}
	var docs []dbmodel.Document
	if err := r.DB.WithContext(ctx).
		Where("dataset_id = ? AND doc_key IN ? AND is_active = ?", datasetID, docKeys, true).
		Find(&docs).Error; err != nil {
		return err
	}
	for _, d := range docs {
		var data map[string]interface{}
		if len(d.Data) > 0 {
			_ = json.Unmarshal(d.Data, &data)
		}
		if data == nil {
			data = make(map[string]interface{})
		}
		if assigneeID != nil {
			if *assigneeID == 0 {
				delete(data, "assignee_id")
			} else {
				data["assignee_id"] = *assigneeID
			}
		}
		if reviewerID != nil {
			if *reviewerID == 0 {
				delete(data, "reviewer_id")
			} else {
				data["reviewer_id"] = *reviewerID
			}
		}
		if deadlineAt != nil {
			if *deadlineAt == "" {
				delete(data, "deadline")
			} else {
				data["deadline"] = *deadlineAt
			}
		}
		raw, _ := json.Marshal(data)
		if err := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
			Where("dataset_id = ? AND doc_key = ? AND is_active = ?", datasetID, d.DocKey, true).
			Updates(map[string]interface{}{"data": dbmodel.JSON(raw), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *RelationalDocRepo) UpdateDocumentQAPairsAndStage(ctx context.Context, datasetID *uint, docKey string, qaPairs []payload.QAPair, stage string) error {
	var sqlDoc dbmodel.Document
	query := r.DB.WithContext(ctx).Where("doc_key = ? AND is_active = ?", docKey, true)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	if err := query.First(&sqlDoc).Error; err != nil {
		return err
	}

	var data map[string]interface{}
	if len(sqlDoc.Data) > 0 {
		_ = json.Unmarshal(sqlDoc.Data, &data)
	} else {
		data = make(map[string]interface{})
	}

	data["qa_pairs"] = qaPairs
	rawJSON, _ := json.Marshal(data)

	res := query.Model(&dbmodel.Document{}).
		Updates(map[string]interface{}{
			"data":             dbmodel.JSON(rawJSON),
			"annotation_stage": stage,
			"updated_at":       time.Now(),
		})
	return res.Error
}

func (r *RelationalDocRepo) UpdateDocumentRefinement(ctx context.Context, datasetID *uint, docKey string, score int, reasoning string, version string, userID uint, stage string) error {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("doc_key = ? AND is_active = ?", docKey, true)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	res := query.
		Updates(map[string]interface{}{
			"llm_refinement_enabled":   true,
			"llm_refinement_score":     score,
			"llm_refinement_reasoning": reasoning,
			"llm_refinement_version":   version,
			"annotator_user_id":        fmt.Sprintf("%d", userID), // canonical numeric id (matches QA/candidate paths)
			"annotator_action_time":    time.Now(),
			"annotation_stage":         stage,
			"updated_at":               time.Now(),
		})
	return res.Error
}

func (r *RelationalDocRepo) RollbackDocumentRefinement(ctx context.Context, datasetID *uint, docKey string) error {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("doc_key = ? AND is_active = ?", docKey, true)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	res := query.
		Updates(map[string]interface{}{
			"llm_refinement_enabled":   false,
			"llm_refinement_score":     gorm.Expr("NULL"),
			"llm_refinement_reasoning": gorm.Expr("NULL"),
			"llm_refinement_version":   gorm.Expr("NULL"),
			"annotation_stage":         "refining",
			"updated_at":               time.Now(),
		})
	return res.Error
}

// UpdateDocumentRefinementCursor validates etag and updates cursor and optionally stage.

func (r *RelationalDocRepo) UpdateDocumentRefinementCursor(ctx context.Context, datasetID *uint, docKey string, etag string, cursor int, newEtag string, newStage string) error {
	buildQuery := func() *gorm.DB {
		query := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
			Where("doc_key = ? AND is_active = ?", docKey, true)
		if datasetID != nil {
			query = query.Where("dataset_id = ?", *datasetID)
		}
		if etag != "" {
			query = query.Where("etag = ?", etag)
		}
		return query
	}

	updates := map[string]interface{}{
		"refinement_cursor": cursor,
		"etag":              newEtag,
		"updated_at":        time.Now(),
	}
	if newStage != "" {
		var sqlDoc dbmodel.Document
		if err := buildQuery().First(&sqlDoc).Error; err != nil {
			return fmt.Errorf("document not found or version mismatch: %w", err)
		}
		var data map[string]interface{}
		if len(sqlDoc.Data) > 0 {
			_ = json.Unmarshal(sqlDoc.Data, &data)
		}
		if data == nil {
			data = make(map[string]interface{})
		}
		data["annotation_stage"] = newStage
		rawJSON, _ := json.Marshal(data)
		updates["data"] = dbmodel.JSON(rawJSON)
		updates["annotation_stage"] = newStage
	}

	res := buildQuery().Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("document not found or version mismatch")
	}
	return nil
}

// UpdateDocumentQAPairsAndCursor validates etag and updates arrays alongside the cursor and optionally the annotator.

func (r *RelationalDocRepo) UpdateDocumentQAPairsAndCursor(ctx context.Context, datasetID *uint, docKey string, etag string, qaPairs []payload.QAPair, cursor int, newEtag string, userID *uint, annotatorName string) error {
	// 每次都要新建 builder:GORM 的 builder 在 First 之后语句已被污染,复用它做
	// Updates 会生成重复表名(Postgres 直接报 42712;SQLite 时代侥幸没炸——
	// 夹具搬上真库当天现形的老 bug)。
	buildQuery := func() *gorm.DB {
		q := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
			Where("doc_key = ? AND is_active = ?", docKey, true).Where("etag = ?", etag)
		if datasetID != nil {
			q = q.Where("dataset_id = ?", *datasetID)
		}
		return q
	}
	var sqlDoc dbmodel.Document
	if err := buildQuery().First(&sqlDoc).Error; err != nil {
		return fmt.Errorf("document not found or version mismatch: %w", err)
	}

	var data map[string]interface{}
	if len(sqlDoc.Data) > 0 {
		_ = json.Unmarshal(sqlDoc.Data, &data)
	} else {
		data = make(map[string]interface{})
	}
	data["qa_pairs"] = qaPairs
	rawJSON, _ := json.Marshal(data)

	now := time.Now()
	updates := map[string]interface{}{
		"data":              dbmodel.JSON(rawJSON),
		"refinement_cursor": cursor,
		"etag":              newEtag,
		"updated_at":        now,
	}

	if userID != nil {
		updates["annotator_user_id"] = fmt.Sprintf("%d", *userID)
		updates["annotator_name"] = annotatorName
		updates["annotator_action_time"] = now
	}

	res := buildQuery().Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("document not found or version mismatch")
	}
	return nil
}
