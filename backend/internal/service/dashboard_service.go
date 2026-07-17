package service

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"text-annotation-platform/internal/cache"
	"text-annotation-platform/internal/model"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"gorm.io/gorm"
)

// DashboardService provides aggregated statistics for the dashboard.
type DashboardService struct {
	dbRepo         *repository.DB
	docRepo         repository.DocumentDB
	demoMode          bool
	cacheTTL          time.Duration
	redisCache        *cache.Cache // nil = use in-process fallback below
	cacheMu           sync.RWMutex
	statsCache        map[string]dashboardStatsCacheEntry
	trendCache        map[string]dashboardTrendCacheEntry
	annotatorCache    map[string]dashboardAnnotatorCacheEntry
	imgAnnotatorCache map[string]dashboardImgAnnotatorCacheEntry
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *DashboardService) WithCache(c *cache.Cache) *DashboardService {
	s.redisCache = c
	return s
}

type dashboardImgAnnotatorCacheEntry struct {
	expiresAt time.Time
	value     []model.ImageAnnotatorStats
}

type dashboardStatsCacheEntry struct {
	expiresAt time.Time
	value     *model.DashboardStats
}

type dashboardTrendCacheEntry struct {
	expiresAt time.Time
	value     []model.DailyTrend
}

type dashboardAnnotatorCacheEntry struct {
	expiresAt time.Time
	value     []model.AnnotatorStats
}

type DashboardCacheInvalidator interface {
	InvalidateAll()
	InvalidateDataset(datasetID uint)
}

// NewDashboardService creates a new DashboardService.
func NewDashboardService(dbRepo *repository.DB, docRepo repository.DocumentDB, demoMode bool, cacheTTL time.Duration) *DashboardService {
	if cacheTTL <= 0 {
		cacheTTL = 5 * time.Minute
	}
	return &DashboardService{
		dbRepo:         dbRepo,
		docRepo:         docRepo,
		demoMode:          demoMode,
		cacheTTL:          cacheTTL,
		statsCache:        make(map[string]dashboardStatsCacheEntry),
		trendCache:        make(map[string]dashboardTrendCacheEntry),
		annotatorCache:    make(map[string]dashboardAnnotatorCacheEntry),
		imgAnnotatorCache: make(map[string]dashboardImgAnnotatorCacheEntry),
	}
}

// GetStats returns aggregated statistics, optionally filtered by dataset_id.
func (s *DashboardService) GetStats(ctx context.Context, datasetID *uint, forceRefresh bool) (*model.DashboardStats, error) {
	if s.demoMode {
		return s.demoStats(datasetID), nil
	}

	cacheKey := s.statsCacheKey(datasetID)
	if !forceRefresh {
		if cached, ok := s.getStatsCache(ctx, cacheKey); ok {
			return cached, nil
		}
	}

	var stats *model.DashboardStats
	var err error
	if datasetID == nil {
		stats, err = s.aggregateAllStats(ctx)
	} else {
		stats, err = s.getStatsForDataset(ctx, *datasetID)
	}
	if err != nil {
		return nil, err
	}

	s.setStatsCache(ctx, cacheKey, stats)
	return cloneDashboardStats(stats), nil
}

// RebuildAllCounters recomputes the counter columns on every dataset row from
// the document repository. Intended for admin repair / one-off backfills.
func (s *DashboardService) RebuildAllCounters(ctx context.Context) error {
	return s.dbRepo.SyncAllDatasetCounters(ctx, s.docRepo)
}

// getStatsForDataset reads pre-computed counters from the datasets row and
// augments them with image task statistics from annotation_tasks.
func (s *DashboardService) getStatsForDataset(ctx context.Context, datasetID uint) (*model.DashboardStats, error) {
	var ds dbmodel.Dataset
	if err := s.dbRepo.DB.WithContext(ctx).First(&ds, datasetID).Error; err != nil {
		return nil, fmt.Errorf("load dataset %d failed: %w", datasetID, err)
	}

	stats := countersFromDataset(&ds)

	// Image task stats from annotation_tasks (relational DB)
	stats.ImageTasks = imageTaskStatsForDataset(s.dbRepo.DB, datasetID)

	dsKey := s.statsCacheKey(&datasetID)
	s.setStatsCache(ctx, dsKey, stats)

	return stats, nil
}

// aggregateAllStats sums pre-computed counters across all datasets. This avoids
// any request-time aggregation over the documents table.
func (s *DashboardService) aggregateAllStats(ctx context.Context) (*model.DashboardStats, error) {
	type aggResult struct {
		DatasetCount         int
		DocCount             int
		NotAnnotatedCount    int
		AutoAnnotatingCount  int
		AutoAnnotatedCount   int
		AutoFailedCount      int
		RefiningCount        int
		RefinedCount         int
		ReviewedCount        int
		QATotal              int
	}
	var r aggResult
	err := s.dbRepo.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Select(`
		COUNT(*) AS dataset_count,
		COALESCE(SUM(doc_count), 0) AS doc_count,
		COALESCE(SUM(not_annotated_count), 0) AS not_annotated_count,
		COALESCE(SUM(auto_annotating_count), 0) AS auto_annotating_count,
		COALESCE(SUM(auto_annotated_count), 0) AS auto_annotated_count,
		COALESCE(SUM(auto_failed_count), 0) AS auto_failed_count,
		COALESCE(SUM(refining_count), 0) AS refining_count,
		COALESCE(SUM(refined_count), 0) AS refined_count,
		COALESCE(SUM(reviewed_count), 0) AS reviewed_count,
		COALESCE(SUM(qa_total), 0) AS qa_total
	`).Scan(&r).Error
	if err != nil {
		return nil, fmt.Errorf("aggregate dataset counters failed: %w", err)
	}

	all := &model.DashboardStats{
		DatasetCount: r.DatasetCount,
		DocCount:     r.DocCount,
		StageDistribution: map[string]int{
			"not_annotated":    r.NotAnnotatedCount,
			"auto_annotating":  r.AutoAnnotatingCount,
			"auto_annotated":   r.AutoAnnotatedCount,
			"auto_failed":      r.AutoFailedCount,
			"refining":         r.RefiningCount,
			"refined":          r.RefinedCount,
			"reviewed":         r.ReviewedCount,
		},
		AutoAnnotatedCount: r.AutoAnnotatedCount + r.RefiningCount + r.RefinedCount + r.ReviewedCount,
		RefinedCount:       r.RefinedCount + r.ReviewedCount,
		QATotal:            r.QATotal,
	}

	all.ImageTasks = imageTaskStatsForDataset(s.dbRepo.DB, 0)

	return all, nil
}

// countersFromDataset builds DashboardStats from the counter columns stored on
// the dataset row.
func countersFromDataset(ds *dbmodel.Dataset) *model.DashboardStats {
	stats := &model.DashboardStats{
		DatasetCount: 1,
		DocCount:     ds.DocCount,
		StageDistribution: map[string]int{
			"not_annotated":   ds.NotAnnotatedCount,
			"auto_annotating": ds.AutoAnnotatingCount,
			"auto_annotated":  ds.AutoAnnotatedCount,
			"auto_failed":     ds.AutoFailedCount,
			"refining":        ds.RefiningCount,
			"refined":         ds.RefinedCount,
			"reviewed":        ds.ReviewedCount,
		},
		AutoAnnotatedCount: ds.AutoAnnotatedCount + ds.RefiningCount + ds.RefinedCount + ds.ReviewedCount,
		RefinedCount:       ds.RefinedCount + ds.ReviewedCount,
		QATotal:            ds.QATotal,
	}
	return stats
}

// imageTaskStatsForDataset returns image annotation task stats scoped to a
// dataset, or across all datasets when datasetID == 0.
func imageTaskStatsForDataset(db *gorm.DB, datasetID uint) *model.ImageTaskStats {
	imageStats := &model.ImageTaskStats{StateDistribution: make(map[string]int)}
	type stateRow struct {
		State string
		Count int
	}
	var stateRows []stateRow
	atQ := db.Table("annotation_tasks").Select("state, count(*) as count").Group("state")
	if datasetID != 0 {
		atQ = atQ.Where("dataset_id = ?", datasetID)
	}
	if err := atQ.Scan(&stateRows).Error; err != nil {
		return nil
	}
	for _, r := range stateRows {
		imageStats.Total += r.Count
		imageStats.StateDistribution[r.State] = r.Count
	}
	today := time.Now().Format("2006-01-02")
	var ft int64
	ftQ := db.Table("annotation_tasks").
		Where("state = 'FINALIZED'").
		Where("DATE(updated_at) = ?", today)
	if datasetID != 0 {
		ftQ = ftQ.Where("dataset_id = ?", datasetID)
	}
	ftQ.Count(&ft)
	imageStats.FinalizedToday = int(ft)
	if imageStats.Total > 0 {
		return imageStats
	}
	return nil
}

// GetDailyTrend returns the daily refined document count for the last N days.
func (s *DashboardService) GetDailyTrend(ctx context.Context, days int, datasetID *uint, forceRefresh bool) ([]model.DailyTrend, error) {
	if s.demoMode {
		return s.demoDailyTrend(days), nil
	}

	if days <= 0 {
		days = 7
	}

	cacheKey := s.trendCacheKey(days, datasetID)
	if !forceRefresh {
		if cached, ok := s.getTrendCache(ctx, cacheKey); ok {
			return cached, nil
		}
	}

	trends, err := s.docRepo.GetDailyTrend(ctx, days, datasetID)
	if err != nil {
		return nil, err
	}

	s.setTrendCache(ctx, cacheKey, trends)
	return cloneDailyTrends(trends), nil
}

// GetAnnotatorStats returns per-annotator performance metrics with role-based data isolation.
func (s *DashboardService) GetAnnotatorStats(ctx context.Context, datasetID *uint, userID uint, role string, forceRefresh bool) ([]model.AnnotatorStats, error) {
	if s.demoMode {
		return s.demoAnnotatorStats(ctx, userID, role)
	}

	cacheKey := s.annotatorCacheKey(datasetID, userID, role)
	if !forceRefresh {
		if cached, ok := s.getAnnotatorCache(ctx, cacheKey); ok {
			return cached, nil
		}
	}

	stats, err := s.docRepo.GetAnnotatorStats(ctx, datasetID)
	if err != nil {
		return nil, err
	}

	if role != "admin" {
		target := strconv.FormatUint(uint64(userID), 10)
		filtered := make([]model.AnnotatorStats, 0, 1)
		for _, item := range stats {
			if item.UserID == target {
				filtered = append(filtered, item)
				break
			}
		}
		stats = filtered
	}

	if len(stats) == 0 {
		empty := []model.AnnotatorStats{}
		s.setAnnotatorCache(ctx, cacheKey, empty)
		return empty, nil
	}

	ids := make([]uint, 0, len(stats))
	for _, item := range stats {
		if uid, convErr := strconv.ParseUint(item.UserID, 10, 64); convErr == nil {
			ids = append(ids, uint(uid))
		}
	}

	if len(ids) > 0 {
		var users []dbmodel.User
		if err := s.dbRepo.DB.WithContext(ctx).
			Select("id", "username", "display_name").
			Where("id IN ?", ids).
			Find(&users).Error; err != nil {
			return nil, fmt.Errorf("query users for annotator stats failed: %w", err)
		}

		userByID := make(map[string]dbmodel.User, len(users))
		for _, u := range users {
			userByID[strconv.FormatUint(uint64(u.ID), 10)] = u
		}

		for i := range stats {
			if u, ok := userByID[stats[i].UserID]; ok {
				stats[i].Username = u.Username
				if u.DisplayName != "" {
					stats[i].DisplayName = u.DisplayName
				} else {
					stats[i].DisplayName = u.Username
				}
			}
			if stats[i].DisplayName == "" {
				stats[i].DisplayName = stats[i].Username
			}
		}
	}

	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].CompletedCount == stats[j].CompletedCount {
			return stats[i].CompletionRate > stats[j].CompletionRate
		}
		return stats[i].CompletedCount > stats[j].CompletedCount
	})

	s.setAnnotatorCache(ctx, cacheKey, stats)

	return cloneAnnotatorStats(stats), nil
}

// GetImageAnnotatorStats returns per-assignee image task counts from the relational DB.
// Non-admin users see only their own row. Admin sees all assignees.
func (s *DashboardService) GetImageAnnotatorStats(ctx context.Context, datasetID *uint, userID uint, role string, forceRefresh bool) ([]model.ImageAnnotatorStats, error) {
	if s.demoMode {
		return []model.ImageAnnotatorStats{}, nil
	}

	cacheKey := role + "|" + strconv.FormatUint(uint64(userID), 10) + "|" + s.statsCacheKey(datasetID)
	if !forceRefresh {
		if cached, ok := s.getImgAnnotatorCache(ctx, cacheKey); ok {
			return cached, nil
		}
	}

	today := time.Now().Format("2006-01-02")

	type row struct {
		UserID          uint    `gorm:"column:user_id"`
		DisplayName     string  `gorm:"column:display_name"`
		AssignedCount   int     `gorm:"column:assigned_count"`
		InProgressCount int     `gorm:"column:in_progress_count"`
		QAPendingCount  int     `gorm:"column:qa_pending_count"`
		FinalizedCount  int     `gorm:"column:finalized_count"`
		TodayFinalized  int     `gorm:"column:today_finalized"`
	}

	q := s.dbRepo.DB.WithContext(ctx).
		Table("annotation_tasks at").
		Select(`
			u.id AS user_id,
			COALESCE(NULLIF(u.display_name,''), u.username) AS display_name,
			COUNT(*) AS assigned_count,
			SUM(CASE WHEN at.state IN ('HUMAN_PENDING','HUMAN_IN_PROGRESS') THEN 1 ELSE 0 END) AS in_progress_count,
			SUM(CASE WHEN at.state = 'QA_PENDING' THEN 1 ELSE 0 END) AS qa_pending_count,
			SUM(CASE WHEN at.state IN ('FINALIZED','EXPORTED') THEN 1 ELSE 0 END) AS finalized_count,
			SUM(CASE WHEN at.state IN ('FINALIZED','EXPORTED') AND DATE(at.updated_at) = ? THEN 1 ELSE 0 END) AS today_finalized
		`, today).
		Joins("JOIN users u ON u.id = at.assignee_id").
		Where("at.assignee_id IS NOT NULL").
		Group("at.assignee_id, u.id, u.display_name, u.username").
		Order("finalized_count DESC, assigned_count DESC")

	if datasetID != nil {
		q = q.Where("at.dataset_id = ?", *datasetID)
	}
	if role != "admin" {
		q = q.Where("at.assignee_id = ?", userID)
	}

	var rows []row
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("image annotator stats: %w", err)
	}

	result := make([]model.ImageAnnotatorStats, len(rows))
	for i, r := range rows {
		rate := 0.0
		if r.AssignedCount > 0 {
			rate = float64(r.FinalizedCount) * 100 / float64(r.AssignedCount)
		}
		result[i] = model.ImageAnnotatorStats{
			UserID:          r.UserID,
			DisplayName:     r.DisplayName,
			AssignedCount:   r.AssignedCount,
			InProgressCount: r.InProgressCount,
			QAPendingCount:  r.QAPendingCount,
			FinalizedCount:  r.FinalizedCount,
			TodayFinalized:  r.TodayFinalized,
			CompletionRate:  rate,
		}
	}

	s.setImgAnnotatorCache(ctx, cacheKey, result)
	return result, nil
}

func (s *DashboardService) getImgAnnotatorCache(ctx context.Context, key string) ([]model.ImageAnnotatorStats, bool) {
	if s.redisCache != nil {
		var v []model.ImageAnnotatorStats
		if hit, _ := s.redisCache.GetJSON(ctx, "dashboard:img:"+key, &v); hit {
			return v, true
		}
		return nil, false
	}
	s.cacheMu.RLock()
	entry, ok := s.imgAnnotatorCache[key]
	s.cacheMu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	cloned := make([]model.ImageAnnotatorStats, len(entry.value))
	copy(cloned, entry.value)
	return cloned, true
}

func (s *DashboardService) setImgAnnotatorCache(ctx context.Context, key string, value []model.ImageAnnotatorStats) {
	if s.redisCache != nil {
		s.redisCache.SetJSON(ctx, "dashboard:img:"+key, value, s.cacheTTL)
		return
	}
	cloned := make([]model.ImageAnnotatorStats, len(value))
	copy(cloned, value)
	s.cacheMu.Lock()
	s.imgAnnotatorCache[key] = dashboardImgAnnotatorCacheEntry{
		expiresAt: time.Now().Add(s.cacheTTL),
		value:     cloned,
	}
	s.cacheMu.Unlock()
}

func (s *DashboardService) InvalidateAll() {
	if s.redisCache != nil {
		ctx := context.Background()
		s.redisCache.ScanDelete(ctx, "dashboard:*")
		return
	}
	s.cacheMu.Lock()
	s.statsCache = make(map[string]dashboardStatsCacheEntry)
	s.trendCache = make(map[string]dashboardTrendCacheEntry)
	s.annotatorCache = make(map[string]dashboardAnnotatorCacheEntry)
	s.imgAnnotatorCache = make(map[string]dashboardImgAnnotatorCacheEntry)
	s.cacheMu.Unlock()
}

func (s *DashboardService) InvalidateDataset(datasetID uint) {
	scope := "dataset:" + strconv.FormatUint(uint64(datasetID), 10)
	if s.redisCache != nil {
		ctx := context.Background()
		s.redisCache.ScanDelete(ctx, "dashboard:*:all")
		s.redisCache.ScanDelete(ctx, "dashboard:*:"+scope)
		return
	}
	s.cacheMu.Lock()
	delete(s.statsCache, "all")
	delete(s.statsCache, scope)
	for key := range s.trendCache {
		if strings.HasSuffix(key, "|all") || strings.HasSuffix(key, "|"+scope) {
			delete(s.trendCache, key)
		}
	}
	for key := range s.annotatorCache {
		if strings.HasSuffix(key, "|all") || strings.HasSuffix(key, "|"+scope) {
			delete(s.annotatorCache, key)
		}
	}
	for key := range s.imgAnnotatorCache {
		if strings.HasSuffix(key, "|all") || strings.HasSuffix(key, "|"+scope) {
			delete(s.imgAnnotatorCache, key)
		}
	}
	s.cacheMu.Unlock()
}

func (s *DashboardService) statsCacheKey(datasetID *uint) string {
	if datasetID == nil {
		return "all"
	}
	return "dataset:" + strconv.FormatUint(uint64(*datasetID), 10)
}

func (s *DashboardService) trendCacheKey(days int, datasetID *uint) string {
	return strconv.Itoa(days) + "|" + s.statsCacheKey(datasetID)
}

func (s *DashboardService) annotatorCacheKey(datasetID *uint, userID uint, role string) string {
	return role + "|" + strconv.FormatUint(uint64(userID), 10) + "|" + s.statsCacheKey(datasetID)
}

func (s *DashboardService) getStatsCache(ctx context.Context, key string) (*model.DashboardStats, bool) {
	if s.redisCache != nil {
		var v model.DashboardStats
		if hit, _ := s.redisCache.GetJSON(ctx, "dashboard:stats:"+key, &v); hit {
			return &v, true
		}
		return nil, false
	}
	s.cacheMu.RLock()
	entry, ok := s.statsCache[key]
	s.cacheMu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return cloneDashboardStats(entry.value), true
}

func (s *DashboardService) setStatsCache(ctx context.Context, key string, value *model.DashboardStats) {
	if s.redisCache != nil {
		s.redisCache.SetJSON(ctx, "dashboard:stats:"+key, value, s.cacheTTL)
		return
	}
	s.cacheMu.Lock()
	s.statsCache[key] = dashboardStatsCacheEntry{
		expiresAt: time.Now().Add(s.cacheTTL),
		value:     cloneDashboardStats(value),
	}
	s.cacheMu.Unlock()
}

func (s *DashboardService) getTrendCache(ctx context.Context, key string) ([]model.DailyTrend, bool) {
	if s.redisCache != nil {
		var v []model.DailyTrend
		if hit, _ := s.redisCache.GetJSON(ctx, "dashboard:trend:"+key, &v); hit {
			return v, true
		}
		return nil, false
	}
	s.cacheMu.RLock()
	entry, ok := s.trendCache[key]
	s.cacheMu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return cloneDailyTrends(entry.value), true
}

func (s *DashboardService) setTrendCache(ctx context.Context, key string, value []model.DailyTrend) {
	if s.redisCache != nil {
		s.redisCache.SetJSON(ctx, "dashboard:trend:"+key, value, s.cacheTTL)
		return
	}
	s.cacheMu.Lock()
	s.trendCache[key] = dashboardTrendCacheEntry{
		expiresAt: time.Now().Add(s.cacheTTL),
		value:     cloneDailyTrends(value),
	}
	s.cacheMu.Unlock()
}

func (s *DashboardService) getAnnotatorCache(ctx context.Context, key string) ([]model.AnnotatorStats, bool) {
	if s.redisCache != nil {
		var v []model.AnnotatorStats
		if hit, _ := s.redisCache.GetJSON(ctx, "dashboard:annotator:"+key, &v); hit {
			return v, true
		}
		return nil, false
	}
	s.cacheMu.RLock()
	entry, ok := s.annotatorCache[key]
	s.cacheMu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return cloneAnnotatorStats(entry.value), true
}

func (s *DashboardService) setAnnotatorCache(ctx context.Context, key string, value []model.AnnotatorStats) {
	if s.redisCache != nil {
		s.redisCache.SetJSON(ctx, "dashboard:annotator:"+key, value, s.cacheTTL)
		return
	}
	s.cacheMu.Lock()
	s.annotatorCache[key] = dashboardAnnotatorCacheEntry{
		expiresAt: time.Now().Add(s.cacheTTL),
		value:     cloneAnnotatorStats(value),
	}
	s.cacheMu.Unlock()
}

func cloneDashboardStats(value *model.DashboardStats) *model.DashboardStats {
	if value == nil {
		return nil
	}
	stageDistribution := make(map[string]int, len(value.StageDistribution))
	for k, v := range value.StageDistribution {
		stageDistribution[k] = v
	}
	clone := &model.DashboardStats{
		DatasetCount:       value.DatasetCount,
		DocCount:           value.DocCount,
		AutoAnnotatedCount: value.AutoAnnotatedCount,
		RefinedCount:       value.RefinedCount,
		QATotal:            value.QATotal,
		StageDistribution:  stageDistribution,
	}
	if value.ImageTasks != nil {
		sd := make(map[string]int, len(value.ImageTasks.StateDistribution))
		for k, v := range value.ImageTasks.StateDistribution {
			sd[k] = v
		}
		clone.ImageTasks = &model.ImageTaskStats{
			Total:             value.ImageTasks.Total,
			FinalizedToday:    value.ImageTasks.FinalizedToday,
			StateDistribution: sd,
		}
	}
	return clone
}

func cloneDailyTrends(value []model.DailyTrend) []model.DailyTrend {
	if value == nil {
		return nil
	}
	cloned := make([]model.DailyTrend, len(value))
	copy(cloned, value)
	return cloned
}

func cloneAnnotatorStats(value []model.AnnotatorStats) []model.AnnotatorStats {
	if value == nil {
		return nil
	}
	cloned := make([]model.AnnotatorStats, len(value))
	copy(cloned, value)
	return cloned
}

// Demo dashboard constants. These power the read-only DemoMode preview shown
// before a real dataset is imported (gated by cfg.DemoMode); production stats
// come from documents-table aggregates.
const (
	demoTotalDatasetCount  = 152
	demoTotalDocCount      = 1523847
	demoPerDatasetDocBase  = 9000 // min docs per demo dataset
	demoPerDatasetDocRange = 2000 // jitter range for visual variety
)

func (s *DashboardService) demoStats(datasetID *uint) *model.DashboardStats {
	datasetCount := demoTotalDatasetCount
	docCount := demoTotalDocCount
	if datasetID != nil {
		datasetCount = 1
		docCount = demoPerDatasetDocBase + int(*datasetID%uint(demoPerDatasetDocRange))
	}

	notAnnotated := int(float64(docCount) * 0.05)
	autoAnnotating := int(float64(docCount) * 0.03)
	autoAnnotated := int(float64(docCount) * 0.20)
	autoFailed := int(float64(docCount) * 0.02)
	refining := int(float64(docCount) * 0.10)
	refined := docCount - notAnnotated - autoAnnotating - autoAnnotated - autoFailed - refining

	stageDistribution := map[string]int{
		"not_annotated":   notAnnotated,
		"auto_annotating": autoAnnotating,
		"auto_annotated":  autoAnnotated,
		"auto_failed":     autoFailed,
		"refining":        refining,
		"refined":         refined,
	}

	return &model.DashboardStats{
		DatasetCount:       datasetCount,
		DocCount:           docCount,
		AutoAnnotatedCount: autoAnnotated + refining + refined,
		RefinedCount:       refined,
		QATotal:            docCount * 7,
		StageDistribution:  stageDistribution,
	}
}

func (s *DashboardService) demoDailyTrend(days int) []model.DailyTrend {
	if days <= 0 {
		days = 7
	}

	rng := rand.New(rand.NewSource(20260303))
	start := time.Now().AddDate(0, 0, -(days - 1))
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())

	trends := make([]model.DailyTrend, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i)
		trends = append(trends, model.DailyTrend{
			Date:         date.Format("2006-01-02"),
			RefinedCount: 4000 + rng.Intn(2001),
		})
	}

	return trends
}

func (s *DashboardService) demoAnnotatorStats(ctx context.Context, userID uint, role string) ([]model.AnnotatorStats, error) {
	var users []dbmodel.User
	if err := s.dbRepo.DB.WithContext(ctx).
		Select("id", "username", "display_name", "role", "status").
		Where("role = ? AND status = ?", "annotator", "active").
		Order("id ASC").
		Find(&users).Error; err != nil {
		return nil, fmt.Errorf("query demo annotator users failed: %w", err)
	}

	if len(users) == 0 {
		return []model.AnnotatorStats{}, nil
	}

	rng := rand.New(rand.NewSource(22002026))
	stats := make([]model.AnnotatorStats, 0, len(users))

	for _, u := range users {
		assigned := 400 + rng.Intn(501)
		minCompleted := int(float64(assigned) * 0.60)
		maxCompleted := int(float64(assigned) * 0.95)
		completed := minCompleted
		if maxCompleted > minCompleted {
			completed += rng.Intn(maxCompleted-minCompleted+1)
		}

		completionRate := 0.0
		if assigned > 0 {
			completionRate = float64(completed) * 100 / float64(assigned)
		}

		stats = append(stats, model.AnnotatorStats{
			UserID:         strconv.FormatUint(uint64(u.ID), 10),
			Username:       u.Username,
			DisplayName:    firstNonEmpty(u.DisplayName, u.Username),
			AssignedCount:  assigned,
			CompletedCount: completed,
			CompletionRate: completionRate,
		})
	}

	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].CompletedCount == stats[j].CompletedCount {
			return stats[i].CompletionRate > stats[j].CompletionRate
		}
		return stats[i].CompletedCount > stats[j].CompletedCount
	})

	if role == "admin" {
		return stats, nil
	}

	target := strconv.FormatUint(uint64(userID), 10)
	for _, item := range stats {
		if item.UserID == target {
			return []model.AnnotatorStats{item}, nil
		}
	}

	return []model.AnnotatorStats{}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
