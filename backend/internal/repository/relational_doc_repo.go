package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text-annotation-platform/internal/model"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"time"

	"gorm.io/gorm"
)

// RelationalDocRepo implements the document repository on PostgreSQL.
// 07 迁移后它是 DocumentDB 的唯一实现:主部署与 runner 模式共用。
type RelationalDocRepo struct {
	DB *gorm.DB
}

// documentListOrder sorts newest import batch first, then by import order.
//   - _import_batch_id 是字符串（"{ds}_{ts}"），文本序即时间序；
//   - _import_order 是整数，但 `->>` 永远返回 text（'10' < '9'），必须 CAST 成数值；
//   - 没有 batch/order 键的旧文档排最后 / 最前，显式写死 NULLS 方向。
const documentListOrder = "(data->>'_import_batch_id') DESC NULLS LAST, CAST(data->>'_import_order' AS NUMERIC) ASC NULLS FIRST, doc_key ASC"

// PaginatedResult holds a page of documents plus the total count.
type PaginatedResult struct {
	Items []paymodel.Document `json:"items"`
	Total int64                 `json:"total"`
}

func NewRelationalDocRepo(db *gorm.DB) *RelationalDocRepo {
	return &RelationalDocRepo{DB: db}
}

// Convert between the relational row and the payload document model.
func toDocModel(doc dbmodel.Document) paymodel.Document {
	m := paymodel.Document{
		DatasetID:              doc.DatasetID,
		DocKey:                 doc.DocKey,
		Version:                doc.Version,
		IsActive:               doc.IsActive,
		UserID:                 doc.UserID,
		AnnotationStage:        doc.AnnotationStage,
		RefinementCursor:       doc.RefinementCursor,
		CreatedBy:              doc.CreatedBy,
		CreatedAt:              paymodel.NewJSONTime(doc.CreatedAt),
		UpdatedAt:              paymodel.NewJSONTime(doc.UpdatedAt),
		ETag:                   doc.ETag,
		LLMRefinementEnabled:   doc.LLMRefinementEnabled,
		LLMRefinementScore:     doc.LLMRefinementScore,
		LLMRefinementReasoning: doc.LLMRefinementReasoning,
		LLMRefinementVersion:   doc.LLMRefinementVersion,
		AnnotatorUserID:        doc.AnnotatorUserID,
		AnnotatorName:          doc.AnnotatorName,
		AnnotatorActionTime:    paymodel.JSONTimePtr(doc.AnnotatorActionTime),
	}

	// Unmarshal json.RawMessage into map[string]interface{}
	// Note: We're taking a shortcut here by decoding the JSON directly into the payload document map
	// since the frontend expects the structure to remain consistent.
	if len(doc.Data) > 0 {
		var data map[string]interface{}
		_ = json.Unmarshal(doc.Data, &data) // ignoring error for simplicity in fallback
		m.Data = data
	}

	return m
}

func fromDocModel(m paymodel.Document) dbmodel.Document {
	doc := dbmodel.Document{
		DatasetID:              m.DatasetID,
		DocKey:                 m.DocKey,
		Version:                m.Version,
		IsActive:               m.IsActive,
		UserID:                 m.UserID,
		AnnotationStage:        m.AnnotationStage,
		RefinementCursor:       m.RefinementCursor,
		CreatedBy:              m.CreatedBy,
		CreatedAt:              m.CreatedAt.Time,
		UpdatedAt:              m.UpdatedAt.Time,
		ETag:                   m.ETag,
		LLMRefinementEnabled:   m.LLMRefinementEnabled,
		LLMRefinementScore:     m.LLMRefinementScore,
		LLMRefinementReasoning: m.LLMRefinementReasoning,
		LLMRefinementVersion:   m.LLMRefinementVersion,
		AnnotatorUserID:        m.AnnotatorUserID,
		AnnotatorName:          m.AnnotatorName,
	}

	if m.AnnotatorActionTime != nil {
		t := m.AnnotatorActionTime.Time
		doc.AnnotatorActionTime = &t
	}

	rawJSON, _ := json.Marshal(m.Data)
	doc.Data = dbmodel.JSON(rawJSON)
	return doc
}

// EnsureIndexes is a no-op: documents 表（含 pg_trgm GIN 检索索引）由 goose 迁移
// 建出——schema 只有那一份真源（M3/P-H3），这里不再 AutoMigrate 出第二份。
// 单测夹具（testutil.DB）跑的也是同一份迁移。
func (r *RelationalDocRepo) EnsureIndexes(ctx context.Context) error { return nil }

func (r *RelationalDocRepo) InsertDocuments(ctx context.Context, docs []paymodel.Document) error {
	if len(docs) == 0 {
		return nil
	}
	var sqlDocs []dbmodel.Document
	for _, d := range docs {
		sqlDocs = append(sqlDocs, fromDocModel(d))
	}
	return r.DB.WithContext(ctx).CreateInBatches(sqlDocs, 500).Error
}

func (r *RelationalDocRepo) InsertDocument(ctx context.Context, doc paymodel.Document) error {
	sqlDoc := fromDocModel(doc)
	return r.DB.WithContext(ctx).Create(&sqlDoc).Error
}

func (r *RelationalDocRepo) FindActiveDocument(ctx context.Context, datasetID *uint, docKey string, userID uint) (*paymodel.Document, error) {
	var sqlDoc dbmodel.Document
	query := r.DB.WithContext(ctx).Where("doc_key = ? AND is_active = ?", docKey, true)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	err := query.First(&sqlDoc).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	m := toDocModel(sqlDoc)
	return &m, nil
}

func (r *RelationalDocRepo) FindVersionHistory(ctx context.Context, datasetID *uint, docKey string, userID uint) ([]paymodel.Document, error) {
	var sqlDocs []dbmodel.Document
	query := r.DB.WithContext(ctx).Where("doc_key = ?", docKey)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	err := query.Order("version desc").Find(&sqlDocs).Error
	if err != nil {
		return nil, err
	}

	var docs []paymodel.Document
	for _, d := range sqlDocs {
		docs = append(docs, toDocModel(d))
	}
	return docs, nil
}

func (r *RelationalDocRepo) FindDocumentsByDatasetPaginated(ctx context.Context, datasetID uint, page, pageSize int, userID uint, query string) (*PaginatedResult, error) {
	var total int64
	q := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).Where("dataset_id = ? AND is_active = ?", datasetID, true)
	if search := strings.TrimSpace(query); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q = q.Where("(LOWER(doc_key) LIKE ? OR LOWER(CAST(data AS TEXT)) LIKE ?)", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, err
	}

	var sqlDocs []dbmodel.Document
	err := q.Order(documentListOrder).Offset((page - 1) * pageSize).Limit(pageSize).Find(&sqlDocs).Error
	if err != nil {
		return nil, err
	}

	var docs []paymodel.Document
	for _, d := range sqlDocs {
		docs = append(docs, toDocModel(d))
	}

	return &PaginatedResult{Items: docs, Total: total}, nil
}

func (r *RelationalDocRepo) FindDocumentsByDataset(ctx context.Context, datasetID uint, _ map[string]interface{}, _ uint) ([]paymodel.Document, error) {
	// ignoring filter here since it's only used for basic finds
	var sqlDocs []dbmodel.Document
	err := r.DB.WithContext(ctx).Where("dataset_id = ? AND is_active = ?", datasetID, true).Order(documentListOrder).Find(&sqlDocs).Error
	if err != nil {
		return nil, err
	}

	var docs []paymodel.Document
	for _, d := range sqlDocs {
		docs = append(docs, toDocModel(d))
	}
	return docs, nil
}

func (r *RelationalDocRepo) FindDocKeysByRange(ctx context.Context, datasetID uint, skip, limit int64) ([]string, error) {
	var keys []string
	err := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).Select("doc_key").
		Where("dataset_id = ? AND is_active = ?", datasetID, true).
		Order(documentListOrder).Offset(int(skip)).Limit(int(limit)).Pluck("doc_key", &keys).Error
	return keys, err
}

func (r *RelationalDocRepo) CountActiveDocKeys(ctx context.Context, datasetID uint) (int, error) {
	var count int64
	err := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("dataset_id = ? AND is_active = ?", datasetID, true).
		Distinct("doc_key").
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (r *RelationalDocRepo) CountActiveDocKeysByDatasets(ctx context.Context, datasetIDs []uint) (map[uint]int, error) {
	result := make(map[uint]int, len(datasetIDs))
	if len(datasetIDs) == 0 {
		return result, nil
	}

	for _, datasetID := range datasetIDs {
		result[datasetID] = 0
	}

	type countRow struct {
		DatasetID uint  `gorm:"column:dataset_id"`
		Count     int64 `gorm:"column:count"`
	}

	var rows []countRow
	err := r.DB.WithContext(ctx).
		Model(&dbmodel.Document{}).
		Select("dataset_id, COUNT(DISTINCT doc_key) AS count").
		Where("dataset_id IN ? AND is_active = ?", datasetIDs, true).
		Group("dataset_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		result[row.DatasetID] = int(row.Count)
	}

	return result, nil
}

func (r *RelationalDocRepo) FindExistingDocKeys(ctx context.Context, datasetID uint, docKeys []string, userID uint) ([]string, error) {
	var keys []string
	err := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).Select("doc_key").
		Where("dataset_id = ? AND doc_key IN ?", datasetID, docKeys).
		Pluck("doc_key", &keys).Error
	return keys, err
}

func (r *RelationalDocRepo) DeactivateVersion(ctx context.Context, datasetID *uint, docKey string, version int) error {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("doc_key = ? AND version = ?", docKey, version)
	if datasetID != nil {
		query = query.Where("dataset_id = ?", *datasetID)
	}
	res := query.
		Updates(map[string]interface{}{"is_active": false, "updated_at": time.Now()})

	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("document not found: %s v%d", docKey, version)
	}
	return nil
}

func (r *RelationalDocRepo) DeleteDocumentByKey(ctx context.Context, datasetID uint, docKey string) (int64, error) {
	res := r.DB.WithContext(ctx).Where("dataset_id = ? AND doc_key = ?", datasetID, docKey).Delete(&dbmodel.Document{})
	return res.RowsAffected, res.Error
}

func (r *RelationalDocRepo) DeleteDocumentsByDataset(ctx context.Context, datasetID uint) error {
	return r.DB.WithContext(ctx).Where("dataset_id = ?", datasetID).Delete(&dbmodel.Document{}).Error
}

func (r *RelationalDocRepo) DeleteDocumentsByKeys(ctx context.Context, datasetID uint, docKeys []string) error {
	return r.DB.WithContext(ctx).Where("dataset_id = ? AND doc_key IN ?", datasetID, docKeys).Delete(&dbmodel.Document{}).Error
}

func (r *RelationalDocRepo) FindDocumentsSince(ctx context.Context, datasetID uint, since *time.Time, isActive bool, userID uint, docKeys []string) ([]paymodel.Document, error) {
	q := r.DB.WithContext(ctx).Where("dataset_id = ? AND is_active = ?", datasetID, isActive)
	if since != nil {
		q = q.Where("updated_at > ?", *since)
	}
	if len(docKeys) > 0 {
		q = q.Where("doc_key IN ?", docKeys)
	}

	var sqlDocs []dbmodel.Document
	if err := q.Order("updated_at asc").Find(&sqlDocs).Error; err != nil {
		return nil, err
	}

	var docs []paymodel.Document
	for _, d := range sqlDocs {
		docs = append(docs, toDocModel(d))
	}
	return docs, nil
}

// Dashboard and Aggregate stubs

func (r *RelationalDocRepo) GetDashboardStats(ctx context.Context, datasetID *uint) (*model.DashboardStats, error) {
	stats := &model.DashboardStats{StageDistribution: make(map[string]int)}

	q := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).Where("is_active = ?", true)
	if datasetID != nil {
		q = q.Where("dataset_id = ?", *datasetID)
	}

	var count int64
	if err := q.Count(&count).Error; err != nil {
		return nil, err
	}
	stats.DocCount = int(count)

	type stageResult struct {
		Stage string
		Count int
	}
	var results []stageResult

	// Use COALESCE to handle empty strings
	if err := q.Select("COALESCE(NULLIF(annotation_stage, ''), 'not_annotated') as stage, count(*) as count").Group("stage").Scan(&results).Error; err != nil {
		return nil, err
	}

	for _, res := range results {
		stats.StageDistribution[res.Stage] = res.Count
	}

	stats.AutoAnnotatedCount = stats.StageDistribution["auto_annotated"] +
		stats.StageDistribution["refining"] +
		stats.StageDistribution["refined"] +
		stats.StageDistribution["reviewed"]

	stats.RefinedCount = stats.StageDistribution["refined"] + stats.StageDistribution["reviewed"]

	// QA pairs 总数在 Go 侧统计（拉回 data 数一遍）：本仓储只服务
	// runner/standalone 模式，量级允许；统计逻辑因此被单测直接覆盖，
	// 不藏在一条难以断言的聚合 SQL 里。
	qaQ := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).Where("is_active = ?", true)
	if datasetID != nil {
		qaQ = qaQ.Where("dataset_id = ?", *datasetID)
	}
	var dataRows []dbmodel.Document
	if err := qaQ.Select("data").Find(&dataRows).Error; err != nil {
		return nil, err
	}
	qaTotal := 0
	for _, row := range dataRows {
		if len(row.Data) == 0 {
			continue
		}
		var payload struct {
			QAPairs []json.RawMessage `json:"qa_pairs"`
		}
		if err := json.Unmarshal(row.Data, &payload); err != nil {
			continue // 非法 JSON 行不计入，与旧 SQL 的 NULL 行为一致
		}
		qaTotal += len(payload.QAPairs)
	}
	stats.QATotal = qaTotal

	return stats, nil
}

func (r *RelationalDocRepo) GetDailyTrend(ctx context.Context, days int, datasetID *uint) ([]model.DailyTrend, error) {
	now := time.Now()
	startDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))

	q := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("is_active = ? AND annotation_stage = ? AND updated_at >= ?", true, "refined", startDate)
	if datasetID != nil {
		q = q.Where("dataset_id = ?", *datasetID)
	}

	// 日期分桶在 Go 侧做：SQL 的 DATE() 经驱动扫出来是带时间的 date 值，
	// 直接拿去当 'YYYY-MM-DD' 的 map 键会静默全部对不上（趋势恒为 0，不报错，
	// 踩过）。拉回 updated_at 自己按 startDate 的时区分桶，行为唯一且被单测覆盖。
	var times []time.Time
	if err := q.Pluck("updated_at", &times).Error; err != nil {
		return nil, err
	}

	dateMap := make(map[string]int)
	for _, ts := range times {
		dateMap[ts.In(startDate.Location()).Format("2006-01-02")]++
	}

	trends := make([]model.DailyTrend, days)
	for i := 0; i < days; i++ {
		date := startDate.AddDate(0, 0, i)
		dateStr := date.Format("2006-01-02")
		trends[i] = model.DailyTrend{
			Date:         dateStr,
			RefinedCount: dateMap[dateStr],
		}
	}

	return trends, nil
}

func (r *RelationalDocRepo) GetAnnotatorStats(ctx context.Context, datasetID *uint) ([]model.AnnotatorStats, error) {
	q := r.DB.WithContext(ctx).Model(&dbmodel.Document{}).
		Where("is_active = ?", true).
		Where("annotation_stage IN ?", []string{"refining", "refined"}).
		Where("annotator_user_id IS NOT NULL AND annotator_user_id != ''")

	if datasetID != nil {
		q = q.Where("dataset_id = ?", *datasetID)
	}

	type row struct {
		UserID         string  `gorm:"column:user_id"`
		Username       string  `gorm:"column:username"`
		AssignedCount  int     `gorm:"column:assigned_count"`
		CompletedCount int     `gorm:"column:completed_count"`
		CompletionRate float64 `gorm:"column:completion_rate"`
	}

	var rows []row
	if err := q.Select(
		"annotator_user_id AS user_id, " +
			"MAX(annotator_name) AS username, " +
			"COUNT(*) AS assigned_count, " +
			"SUM(CASE WHEN annotation_stage = 'refined' THEN 1 ELSE 0 END) AS completed_count, " +
			"CASE WHEN COUNT(*) = 0 THEN 0 ELSE CAST(SUM(CASE WHEN annotation_stage = 'refined' THEN 1 ELSE 0 END) AS REAL) * 100.0 / COUNT(*) END AS completion_rate",
	).Group("annotator_user_id").Scan(&rows).Error; err != nil {
		return nil, err
	}

	stats := make([]model.AnnotatorStats, 0, len(rows))
	for _, r := range rows {
		stats = append(stats, model.AnnotatorStats{
			UserID:         r.UserID,
			Username:       r.Username,
			DisplayName:    r.Username,
			AssignedCount:  r.AssignedCount,
			CompletedCount: r.CompletedCount,
			CompletionRate: r.CompletionRate,
		})
	}

	return stats, nil
}
