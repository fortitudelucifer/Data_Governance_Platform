package service

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"text-annotation-platform/internal/cache"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/repository/iface"
)

const (
	categoryCacheTTL    = 60 * time.Minute
	tagCacheTTL         = 60 * time.Minute
	datasetPageTTL      = 5 * time.Minute
	datasetOptionsTTL   = 10 * time.Minute
	datasetByIDTTL      = 10 * time.Minute
)

// DatasetService handles business logic for datasets, categories, and tags.
type DatasetService struct {
	dbRepo iface.DBDatasetRepo
	docRepo repository.DocumentDB
	cache     *cache.Cache // nil = no Redis
}

// NewDatasetService creates a DatasetService with the given repositories.
func NewDatasetService(dbRepo iface.DBDatasetRepo, docRepo repository.DocumentDB) *DatasetService {
	return &DatasetService{
		dbRepo: dbRepo,
		docRepo: docRepo,
	}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *DatasetService) WithCache(c *cache.Cache) *DatasetService {
	s.cache = c
	return s
}

// datasetPageCacheKey builds a short, deterministic Redis key for a paginated
// dataset list request by hashing all filter/sort/page parameters.
func datasetPageCacheKey(categoryID *uint, tagIDs []uint, name string, sortOpt repository.DatasetListSort, page, pageSize int) string {
	cat := "nil"
	if categoryID != nil {
		cat = strconv.FormatUint(uint64(*categoryID), 10)
	}
	tagStrs := make([]string, len(tagIDs))
	for i, t := range tagIDs {
		tagStrs[i] = strconv.FormatUint(uint64(t), 10)
	}
	sort.Strings(tagStrs)
	// name 必须进 key：否则搜 "cat" 与搜 "dog" 命中同一条缓存，第二个搜索直接
	// 拿到第一个的结果。
	raw := fmt.Sprintf("cat=%s:tags=%s:q=%s:by=%s:ord=%s:p=%d:ps=%d",
		cat, strings.Join(tagStrs, ","), strings.ToLower(strings.TrimSpace(name)),
		sortOpt.By, sortOpt.Order, page, pageSize)
	return fmt.Sprintf("datasets:page:%x", md5.Sum([]byte(raw)))
}

// invalidateDatasetCaches clears all dataset-related Redis caches for a given
// dataset ID (use id=0 for create operations where ID is not yet known).
func (s *DatasetService) invalidateDatasetCaches(ctx context.Context, id uint) {
	if s.cache == nil {
		return
	}
	s.cache.ScanDelete(ctx, "datasets:page:*")
	s.cache.Delete(ctx, "datasets:options")
	if id > 0 {
		s.cache.Delete(ctx, "dataset:"+strconv.FormatUint(uint64(id), 10))
	}
}

// ---------------------------------------------------------------------------
// Dataset CRUD
// ---------------------------------------------------------------------------

// CreateDataset creates a dataset record in the relational DB and associates the given tags.
func (s *DatasetService) CreateDataset(ctx context.Context, name string, categoryID, ownerID uint, tagIDs []uint, industryTagIDs []uint, annotationType string, caseType string, datasetFunctionID *uint) (*dbmodel.Dataset, error) {
	return s.CreateDatasetWithModality(ctx, name, "", categoryID, ownerID, tagIDs, industryTagIDs, annotationType, caseType, datasetFunctionID)
}

// CreateDatasetWithModality is the modality-aware variant introduced in P0
// multi-modal. The legacy CreateDataset method delegates to this and passes
// modality="" so that the model layer falls back to its DB default ("text").
func (s *DatasetService) CreateDatasetWithModality(ctx context.Context, name, modality string, categoryID, ownerID uint, tagIDs []uint, industryTagIDs []uint, annotationType string, caseType string, datasetFunctionID *uint) (*dbmodel.Dataset, error) {
	if annotationType == "" {
		annotationType = "qa"
	}
	if caseType == "" {
		caseType = "criminal"
	}
	if modality == "" {
		modality = dbmodel.ModalityText
	}
	if !dbmodel.IsValidModality(modality) {
		return nil, fmt.Errorf("invalid modality %q", modality)
	}

	ds := &dbmodel.Dataset{
		Name:              name,
		OwnerID:           ownerID,
		UserID:            1, // single-user default
		Modality:          modality,
		AnnotationType:    annotationType,
		CaseType:          caseType,
		DatasetFunctionID: datasetFunctionID,
	}
	if categoryID > 0 {
		ds.CategoryID = &categoryID
	}

	if err := s.dbRepo.CreateDataset(ctx, ds); err != nil {
		return nil, fmt.Errorf("create dataset failed: %w", err)
	}

	if len(tagIDs) > 0 {
		if err := s.dbRepo.SetDatasetTags(ctx, ds.ID, tagIDs); err != nil {
			return nil, fmt.Errorf("set dataset tags failed: %w", err)
		}
	}

	if len(industryTagIDs) > 0 {
		if err := s.dbRepo.SetDatasetIndustryTags(ctx, ds.ID, industryTagIDs); err != nil {
			return nil, fmt.Errorf("set dataset industry tags failed: %w", err)
		}
	}

	// Reload with associations
	created, err := s.dbRepo.FindDatasetByID(ctx, ds.ID)
	if err != nil {
		return nil, err
	}
	s.invalidateDatasetCaches(ctx, 0) // clear list/options; new id's page slot is already empty
	return created, nil
}

func (s *DatasetService) hydrateDocCounts(ctx context.Context, datasets []dbmodel.Dataset) []dbmodel.Dataset {
	if len(datasets) == 0 {
		return []dbmodel.Dataset{}
	}

	datasetIDs := make([]uint, 0, len(datasets))
	for i := range datasets {
		datasetIDs = append(datasetIDs, datasets[i].ID)
	}

	actualCounts, err := s.docRepo.CountActiveDocKeysByDatasets(ctx, datasetIDs)
	if err != nil {
		return datasets
	}

	for i := range datasets {
		actual := actualCounts[datasets[i].ID]
		if datasets[i].DocCount != actual {
			if updErr := s.dbRepo.UpdateDocCount(ctx, datasets[i].ID, actual); updErr != nil {
				// non-fatal: stale doc_count is acceptable for read paths
				continue
			}
			datasets[i].DocCount = actual
		}
	}

	return datasets
}

func parseDatasetSequence(name string) (int, bool) {
	normalized := strings.TrimSpace(strings.ToLower(name))
	if !strings.HasPrefix(normalized, "dataset-") {
		return 0, false
	}

	seq, err := strconv.Atoi(strings.TrimPrefix(normalized, "dataset-"))
	if err != nil {
		return 0, false
	}

	return seq, true
}

func compareDatasetNames(a string, b string) int {
	aSeq, aOk := parseDatasetSequence(a)
	bSeq, bOk := parseDatasetSequence(b)

	if aOk && bOk {
		return aSeq - bSeq
	}
	if aOk {
		return -1
	}
	if bOk {
		return 1
	}

	return strings.Compare(strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b)))
}

// ListDatasetsPage returns datasets filtered by optional category and tag IDs with server-side pagination.
// Result is cached under "datasets:page:{hash}" for 5 minutes.
func (s *DatasetService) ListDatasetsPage(ctx context.Context, categoryID *uint, tagIDs []uint, name string, sortOpt repository.DatasetListSort, page int, pageSize int) (*repository.DatasetPage, error) {
	cacheKey := datasetPageCacheKey(categoryID, tagIDs, name, sortOpt, page, pageSize)
	if s.cache != nil {
		var v repository.DatasetPage
		if hit, _ := s.cache.GetJSON(ctx, cacheKey, &v); hit {
			return &v, nil
		}
	}

	result, err := s.fetchDatasetsPage(ctx, categoryID, tagIDs, name, sortOpt, page, pageSize)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, cacheKey, result, datasetPageTTL)
	}
	return result, nil
}

// fetchDatasetsPage performs the actual DB queries for ListDatasetsPage without any caching.
func (s *DatasetService) fetchDatasetsPage(ctx context.Context, categoryID *uint, tagIDs []uint, name string, sortOpt repository.DatasetListSort, page int, pageSize int) (*repository.DatasetPage, error) {
	filter := repository.DatasetFilter{
		CategoryID: categoryID,
		TagIDs:     tagIDs,
		Name:       name,
	}

	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	orderDescending := strings.EqualFold(sortOpt.Order, "descending") || strings.EqualFold(sortOpt.Order, "desc")

	if sortOpt.By == "" || sortOpt.By == "name" {
		items, err := s.dbRepo.ListDatasetListItems(ctx, filter)
		if err != nil {
			return nil, err
		}

		sort.SliceStable(items, func(i, j int) bool {
			cmp := compareDatasetNames(items[i].Name, items[j].Name)
			if orderDescending {
				return cmp > 0
			}
			return cmp < 0
		})

		total := int64(len(items))
		offset := (page - 1) * pageSize
		if offset >= len(items) {
			return &repository.DatasetPage{
				Items:    []dbmodel.Dataset{},
				Total:    total,
				Page:     page,
				PageSize: pageSize,
			}, nil
		}

		end := offset + pageSize
		if end > len(items) {
			end = len(items)
		}

		ids := make([]uint, 0, end-offset)
		for _, item := range items[offset:end] {
			ids = append(ids, item.ID)
		}

		datasets, err := s.dbRepo.FindDatasetsByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}

		return &repository.DatasetPage{
			Items:    s.hydrateDocCounts(ctx, datasets),
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		}, nil
	}

	datasets, total, err := s.dbRepo.ListDatasetsPage(ctx, filter, sortOpt, page, pageSize)
	if err != nil {
		return nil, err
	}

	return &repository.DatasetPage{
		Items:    s.hydrateDocCounts(ctx, datasets),
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *DatasetService) ListDatasetOptions(ctx context.Context) ([]repository.DatasetOption, error) {
	if s.cache != nil {
		var v []repository.DatasetOption
		if hit, _ := s.cache.GetJSON(ctx, "datasets:options", &v); hit {
			return v, nil
		}
	}

	options, err := s.dbRepo.ListDatasetOptions(ctx)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(options, func(i, j int) bool {
		return compareDatasetNames(options[i].Name, options[j].Name) < 0
	})

	if s.cache != nil {
		s.cache.SetJSON(ctx, "datasets:options", options, datasetOptionsTTL)
	}
	return options, nil
}

// GetByID returns a single dataset by primary key.
// Result is cached under "dataset:{id}" for 10 minutes.
func (s *DatasetService) GetByID(ctx context.Context, id uint) (*dbmodel.Dataset, error) {
	key := "dataset:" + strconv.FormatUint(uint64(id), 10)
	if s.cache != nil {
		var v dbmodel.Dataset
		if hit, _ := s.cache.GetJSON(ctx, key, &v); hit {
			return &v, nil
		}
	}
	ds, err := s.dbRepo.FindDatasetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, key, ds, datasetByIDTTL)
	}
	return ds, nil
}

// UpdateLabelConfig persists a raw JSON label-config string for a dataset.
func (s *DatasetService) UpdateLabelConfig(ctx context.Context, id uint, cfg string) error {
	if err := s.dbRepo.UpdateDataset(ctx, id, map[string]interface{}{"label_config": cfg}); err != nil {
		return err
	}
	s.invalidateDatasetCaches(ctx, id)
	return nil
}

// UpdateLabelOntology stores the audio/video label ontology JSON object for a
// dataset. See plan_v2 执行方案-00 T0.6 《标签本体 Schema》.
func (s *DatasetService) UpdateLabelOntology(ctx context.Context, id uint, ontology string) error {
	if err := s.dbRepo.UpdateDataset(ctx, id, map[string]interface{}{"label_ontology": ontology}); err != nil {
		return err
	}
	s.invalidateDatasetCaches(ctx, id)
	return nil
}

// GetVideoAIConfig returns the dataset's detect_track cost gate, filled in from
// the global defaults so the admin UI always shows what will actually happen
// (B2.8 成本闸门).
func (s *DatasetService) GetVideoAIConfig(ctx context.Context, id uint) (VideoAIConfig, error) {
	ds, err := s.dbRepo.FindDatasetByID(ctx, id)
	if err != nil {
		return VideoAIConfig{}, err
	}
	if ds == nil {
		return VideoAIConfig{}, fmt.Errorf("dataset %d not found", id)
	}
	return VideoAIConfigFromDataset(ds.AIConfig), nil
}

// UpdateVideoAIConfig stores a normalised (clamped) cost gate, and returns what
// was actually stored — the caller may have asked for max_frames=99999. Storing
// the normalised form means a later read never has to re-guess a stale row.
func (s *DatasetService) UpdateVideoAIConfig(ctx context.Context, id uint, cfg VideoAIConfig) (VideoAIConfig, error) {
	// 读-改-写整列 ai_config（M11）：只覆盖 video.detect_track 这一个键，
	// 其它能力的配置原样保留。
	ds, err := s.dbRepo.FindDatasetByID(ctx, id)
	if err != nil {
		return VideoAIConfig{}, err
	}
	if ds == nil {
		return VideoAIConfig{}, fmt.Errorf("dataset %d not found", id)
	}
	normalized := cfg.Normalize()
	raw, err := PatchAIConfig(ds.AIConfig, CapabilityVideoDetectTrack, normalized)
	if err != nil {
		return VideoAIConfig{}, err
	}
	if err := s.dbRepo.UpdateDataset(ctx, id, map[string]interface{}{"ai_config": raw}); err != nil {
		return VideoAIConfig{}, err
	}
	s.invalidateDatasetCaches(ctx, id)
	return normalized, nil
}

// UpdateExportMeta persists the export-envelope metadata (《通用元数据字段》规范)
// for a dataset. These constants are stamped onto every exported record's
// envelope. sourceDetail is a JSON object string (caller-validated).
func (s *DatasetService) UpdateExportMeta(ctx context.Context, id uint, authType, sourceType, sourceDetail, dataVersion string) error {
	if err := s.dbRepo.UpdateDataset(ctx, id, map[string]interface{}{
		"auth_type":     authType,
		"source_type":   sourceType,
		"source_detail": sourceDetail,
		"data_version":  dataVersion,
	}); err != nil {
		return err
	}
	s.invalidateDatasetCaches(ctx, id)
	return nil
}

// UpdateDataset updates a dataset's fields and re-associates tags.
func (s *DatasetService) UpdateDataset(ctx context.Context, id uint, name string, categoryID uint, tagIDs []uint, industryTagIDs []uint, annotationType string, caseType string, datasetFunctionID *uint) error {
	updates := map[string]interface{}{
		"name": name,
	}
	if categoryID > 0 {
		updates["category_id"] = categoryID
	} else {
		updates["category_id"] = nil
	}
	if annotationType != "" {
		updates["annotation_type"] = annotationType
	}
	if caseType != "" {
		updates["case_type"] = caseType
	}
	updates["dataset_function_id"] = datasetFunctionID
	if err := s.dbRepo.UpdateDataset(ctx, id, updates); err != nil {
		return fmt.Errorf("update dataset failed: %w", err)
	}

	if err := s.dbRepo.SetDatasetTags(ctx, id, tagIDs); err != nil {
		return fmt.Errorf("set dataset tags failed: %w", err)
	}
	if err := s.dbRepo.SetDatasetIndustryTags(ctx, id, industryTagIDs); err != nil {
		return fmt.Errorf("set dataset industry tags failed: %w", err)
	}
	s.invalidateDatasetCaches(ctx, id)
	return nil
}

// DeleteDataset removes a dataset. It first deletes the dataset's
// documents, then removes the relational record. If document deletion fails the
// relational record is kept intact.
func (s *DatasetService) DeleteDataset(ctx context.Context, id uint) error {
	// Delete document rows first
	if err := s.docRepo.DeleteDocumentsByDataset(ctx, id); err != nil {
		return fmt.Errorf("delete documents failed: %w", err)
	}

	// Then delete the relational record
	if err := s.dbRepo.DeleteDataset(ctx, id); err != nil {
		return fmt.Errorf("delete dataset from relational DB failed: %w", err)
	}
	s.invalidateDatasetCaches(ctx, id)
	return nil
}

// ---------------------------------------------------------------------------
// Category CRUD
// ---------------------------------------------------------------------------

// CreateCategory creates a new dataset category.
func (s *DatasetService) CreateCategory(ctx context.Context, cat *dbmodel.DatasetCategory) error {
	if err := s.dbRepo.CreateCategory(ctx, cat); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.Delete(ctx, "categories:all")
	}
	return nil
}

// ListCategories returns all categories with their associated dataset counts.
// Result is cached in Redis under "categories:all" for 60 minutes.
func (s *DatasetService) ListCategories(ctx context.Context) ([]repository.CategoryWithCount, error) {
	if s.cache != nil {
		var v []repository.CategoryWithCount
		if hit, _ := s.cache.GetJSON(ctx, "categories:all", &v); hit {
			return v, nil
		}
	}
	cats, err := s.dbRepo.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, "categories:all", cats, categoryCacheTTL)
	}
	return cats, nil
}

// UpdateCategory updates an existing category by ID.
func (s *DatasetService) UpdateCategory(ctx context.Context, id uint, updates map[string]interface{}) error {
	if err := s.dbRepo.UpdateCategory(ctx, id, updates); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.Delete(ctx, "categories:all")
	}
	return nil
}

// DeleteCategory removes a category by ID.
func (s *DatasetService) DeleteCategory(ctx context.Context, id uint) error {
	if err := s.dbRepo.DeleteCategory(ctx, id); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.Delete(ctx, "categories:all")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tag CRUD
// ---------------------------------------------------------------------------

// CreateTag creates a new tag after checking name uniqueness.
func (s *DatasetService) CreateTag(ctx context.Context, tag *dbmodel.Tag) error {
	if tag.Type == "" {
		tag.Type = "dataset"
	}
	existing, err := s.dbRepo.FindTagByName(ctx, tag.Name, tag.Type)
	if err == nil && existing != nil {
		return errors.New("tag name already exists")
	}
	if err := s.dbRepo.CreateTag(ctx, tag); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.Delete(ctx, "tags:"+tag.Type)
	}
	return nil
}

// ListTags returns tags by type (dataset by default).
// Result is cached in Redis under "tags:{type}" for 60 minutes.
func (s *DatasetService) ListTags(ctx context.Context, tagType string) ([]dbmodel.Tag, error) {
	if tagType == "" {
		tagType = "dataset"
	}
	if s.cache != nil {
		var v []dbmodel.Tag
		if hit, _ := s.cache.GetJSON(ctx, "tags:"+tagType, &v); hit {
			return v, nil
		}
	}
	tags, err := s.dbRepo.ListTags(ctx, &tagType)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, "tags:"+tagType, tags, tagCacheTTL)
	}
	return tags, nil
}

// UpdateTag updates an existing tag by ID.
func (s *DatasetService) UpdateTag(ctx context.Context, id uint, updates map[string]interface{}) error {
	if err := s.dbRepo.UpdateTag(ctx, id, updates); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.ScanDelete(ctx, "tags:*") // type unknown from id; clear all tag caches
	}
	return nil
}

// DeleteTag removes a tag by ID.
func (s *DatasetService) DeleteTag(ctx context.Context, id uint) error {
	if err := s.dbRepo.DeleteTag(ctx, id); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.ScanDelete(ctx, "tags:*") // type unknown from id; clear all tag caches
	}
	return nil
}
