package service

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"text-annotation-platform/internal/cache"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/repository/iface"
)

const (
	taskMineTTL = 2 * time.Minute
	taskListTTL = 3 * time.Minute
)

// AnnotationTaskService is the entry point for creating and querying P0 image
// annotation tasks. It does NOT execute AI calls — that happens inside the
// AIWorker.
type AnnotationTaskService struct {
	db   iface.DBAnnotationTaskRepo
	cache   *cache.Cache // nil = no Redis
	videoAI VideoAIConfigLoader
}

// VideoAIConfigLoader resolves a dataset's video cost gate (B2.8). Injected
// rather than widened onto DBAnnotationTaskRepo: the task repo has no
// business knowing about datasets, and both of its test doubles would have to
// grow a method they never call.
type VideoAIConfigLoader func(ctx context.Context, datasetID uint) VideoAIConfig

// NewAnnotationTaskService composes the dependencies.
func NewAnnotationTaskService(dbRepo iface.DBAnnotationTaskRepo) *AnnotationTaskService {
	return &AnnotationTaskService{db: dbRepo}
}

// WithVideoAIConfig enables dataset-level auto pre-annotation. Without it, video
// tasks always land in HUMAN_PENDING (the safe default).
func (s *AnnotationTaskService) WithVideoAIConfig(loader VideoAIConfigLoader) *AnnotationTaskService {
	s.videoAI = loader
	return s
}

// videoAITrigger reports the dataset's trigger mode; manual when unwired.
func (s *AnnotationTaskService) videoAITrigger(ctx context.Context, datasetID uint) string {
	if s.videoAI == nil {
		return VideoAITriggerManual
	}
	return s.videoAI(ctx, datasetID).Trigger
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *AnnotationTaskService) WithCache(c *cache.Cache) *AnnotationTaskService {
	s.cache = c
	return s
}

// taskListResult bundles the paginated list result for JSON serialization.
type taskListResult struct {
	Items []dbmodel.AnnotationTask `json:"items"`
	Total int64                       `json:"total"`
}

// taskCacheKey builds a Redis key for a List call.
// Returns ("", false) for filter patterns we don't cache (e.g., asset-id lookups).
func taskCacheKey(filter repository.AnnotationTaskFilter, page, pageSize int) (string, bool) {
	state := ""
	if filter.State != nil {
		state = *filter.State
	}
	suffix := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("s=%s:p=%d:ps=%d", state, page, pageSize))))

	if filter.MineUserID != nil {
		uid := strconv.FormatUint(uint64(*filter.MineUserID), 10)
		return "tasks:mine:" + uid + ":" + suffix, true
	}
	if filter.DatasetID != nil && filter.AssetID == nil && len(filter.AssetIDs) == 0 {
		did := strconv.FormatUint(uint64(*filter.DatasetID), 10)
		asgn := ""
		if filter.AssigneeID != nil {
			asgn = strconv.FormatUint(uint64(*filter.AssigneeID), 10)
		}
		full := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("s=%s:a=%s:p=%d:ps=%d", state, asgn, page, pageSize))))
		return "tasks:list:" + did + ":" + full, true
	}
	return "", false
}

// invalidateTaskCaches clears all task list/mine caches after a write.
func (s *AnnotationTaskService) invalidateTaskCaches(ctx context.Context) {
	if s.cache == nil {
		return
	}
	s.cache.ScanDelete(ctx, "tasks:mine:*")
	s.cache.ScanDelete(ctx, "tasks:list:*")
}

// CreateTaskOptions captures the optional inputs to CreateForAsset.
type CreateTaskOptions struct {
	JobID    *uint
	Deadline *time.Time
}

// CreateForAsset creates an AnnotationTask for the given asset in CREATED
// state. If QC has already passed, the worker will pick the task up and run
// the L1 router on its next tick.
func (s *AnnotationTaskService) CreateForAsset(ctx context.Context, asset *dbmodel.Asset, opts CreateTaskOptions) (*dbmodel.AnnotationTask, error) {
	if asset == nil || asset.ID == 0 {
		return nil, errors.New("asset required")
	}
	traceID, err := newTraceID()
	if err != nil {
		return nil, fmt.Errorf("trace id: %w", err)
	}
	state := dbmodel.TaskStateCreated
	routeStrategy := ""
	if asset.QCStatus == dbmodel.QCStatusPassed {
		state = dbmodel.TaskStateRouting
		// Audio/Video must NOT enter the image-only L1 router (it derives
		// features from Width/Height). They go straight to HUMAN_PENDING; AI
		// prelabel (ASR for audio; detect+track for video) is run ON DEMAND from
		// the workspace via /tasks/:id/invoke — NOT auto-triggered on import, so
		// the annotator picks the model/capability and approves the call (mirrors
		// the image workspace). See plan_v2 执行方案-01 A2.
		switch asset.Modality {
		case dbmodel.ModalityAudio:
			state = dbmodel.TaskStateHumanPending
			routeStrategy = dbmodel.RouteHumanOnly
		case dbmodel.ModalityVideo:
			state = dbmodel.TaskStateHumanPending
			routeStrategy = dbmodel.RouteHumanOnly
			// …unless the dataset opted into auto pre-annotation (B2.8 成本闸门).
			// Then the task is queued for the AI worker, whose bounded GPU gate
			// decides how much of a 200-video import may run at once.
			if s.videoAITrigger(ctx, asset.DatasetID) == VideoAITriggerAuto {
				state = dbmodel.TaskStateAIPending
				routeStrategy = dbmodel.RouteVideoDetectFirst
			}
		}
	} else if asset.QCStatus == dbmodel.QCStatusFailed {
		state = dbmodel.TaskStateQCFailed
	}
	task := &dbmodel.AnnotationTask{
		AssetID:        asset.ID,
		DatasetID:      asset.DatasetID,
		ParentAssetID:  asset.ParentAssetID,
		JobID:          opts.JobID,
		RouteStrategy:  routeStrategy,
		StrategyOrigin: dbmodel.StrategyOriginAuto,
		State:          state,
		TraceID:        traceID,
		Version:        1,
		DeadlineAt:     opts.Deadline,
	}
	if err := s.db.CreateAnnotationTask(ctx, task); err != nil {
		return nil, fmt.Errorf("create annotation task: %w", err)
	}
	s.invalidateTaskCaches(ctx)
	return task, nil
}

// Get fetches a task by id.
func (s *AnnotationTaskService) Get(ctx context.Context, id uint) (*dbmodel.AnnotationTask, error) {
	return s.db.FindAnnotationTaskByID(ctx, id)
}

// List returns paginated tasks, serving from Redis cache when the filter
// matches a known cacheable pattern (mine / dataset list).
func (s *AnnotationTaskService) List(ctx context.Context, filter repository.AnnotationTaskFilter, page, pageSize int) ([]dbmodel.AnnotationTask, int64, error) {
	key, cacheable := taskCacheKey(filter, page, pageSize)
	if cacheable && s.cache != nil {
		var v taskListResult
		if hit, _ := s.cache.GetJSON(ctx, key, &v); hit {
			return v.Items, v.Total, nil
		}
	}

	items, total, err := s.db.ListAnnotationTasksPage(ctx, filter, page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	if cacheable && s.cache != nil {
		ttl := taskListTTL
		if filter.MineUserID != nil {
			ttl = taskMineTTL
		}
		s.cache.SetJSON(ctx, key, taskListResult{Items: items, Total: total}, ttl)
	}
	return items, total, nil
}

// Reprocess transitions a task into REPROCESS for re-routing/re-AI execution.
// It increments Version so downstream RoutingResult / OCRResult / VLMResult
// rows produce a fresh version.
func (s *AnnotationTaskService) Reprocess(ctx context.Context, taskID uint) error {
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if !task.IsTerminal() && task.State != dbmodel.TaskStateReprocess {
		return fmt.Errorf("task %d not in a terminal state (current: %s)", taskID, task.State)
	}
	traceID, err := newTraceID()
	if err != nil {
		return err
	}
	updates := map[string]interface{}{
		"state":               dbmodel.TaskStateRouting,
		"trace_id":            traceID,
		"version":             task.Version + 1,
		"retry_count":         0,
		"next_attempt_at":     nil,
		"lease_until":         nil,
		"human_only_fallback": false,
		"final_annotation_id": "",
		"updated_at":          time.Now(),
	}
	return s.db.UpdateAnnotationTask(ctx, taskID, updates)
}

// BatchAssign applies the same assignee/reviewer/deadline to multiple tasks
// atomically. Semantics mirror Assign: nil = leave unchanged, 0/zero = clear.
// Returns the number of rows actually updated.
func (s *AnnotationTaskService) BatchAssign(ctx context.Context, taskIDs []uint, assigneeID, reviewerID *uint, deadline *time.Time) (int64, error) {
	if len(taskIDs) == 0 {
		return 0, nil
	}
	updates := map[string]interface{}{"updated_at": time.Now()}
	if assigneeID != nil {
		if *assigneeID == 0 {
			updates["assignee_id"] = nil
		} else {
			updates["assignee_id"] = *assigneeID
		}
	}
	if reviewerID != nil {
		if *reviewerID == 0 {
			updates["reviewer_id"] = nil
		} else {
			updates["reviewer_id"] = *reviewerID
		}
	}
	if deadline != nil {
		if deadline.IsZero() {
			updates["deadline_at"] = nil
		} else {
			updates["deadline_at"] = *deadline
		}
	}
	if len(updates) == 1 {
		return 0, nil // only updated_at — nothing to do
	}
	n, err := s.db.BatchUpdateAnnotationTasks(ctx, taskIDs, updates)
	if err == nil && n > 0 {
		s.invalidateTaskCaches(ctx)
	}
	return n, err
}

// Assign sets assignee_id, reviewer_id, and/or deadline_at on a task.
// Any pointer being nil means leave unchanged.
// For uint fields, 0 = clear; for deadline, zero time = clear.
func (s *AnnotationTaskService) Assign(ctx context.Context, taskID uint, assigneeID, reviewerID *uint, deadline *time.Time) error {
	updates := map[string]interface{}{"updated_at": time.Now()}
	if assigneeID != nil {
		if *assigneeID == 0 {
			updates["assignee_id"] = nil
		} else {
			updates["assignee_id"] = *assigneeID
		}
	}
	if reviewerID != nil {
		if *reviewerID == 0 {
			updates["reviewer_id"] = nil
		} else {
			updates["reviewer_id"] = *reviewerID
		}
	}
	if deadline != nil {
		if deadline.IsZero() {
			updates["deadline_at"] = nil
		} else {
			updates["deadline_at"] = *deadline
		}
	}
	if len(updates) == 1 {
		return nil // only updated_at — nothing to do
	}
	if err := s.db.UpdateAnnotationTask(ctx, taskID, updates); err != nil {
		return err
	}
	s.invalidateTaskCaches(ctx)
	return nil
}

// AdjacentTaskIDs returns the task IDs immediately before and after taskID.
//
// When mineUserID is non-nil, adjacency is scoped to tasks where the given
// user is assignee or reviewer, ordered by task.id — suitable for navigating
// within "my tasks" queue. Otherwise adjacency spans the full dataset ordered
// by asset.id, which is the dataset-level navigation used from AssetListView.
func (s *AnnotationTaskService) AdjacentTaskIDs(ctx context.Context, taskID uint, mineUserID *uint) (prevTaskID, nextTaskID *uint, err error) {
	if mineUserID != nil {
		return s.db.FindAdjacentTaskIDsByUser(ctx, *mineUserID, taskID)
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	prevAsset, nextAsset, err := s.db.FindAdjacentAssetIDs(ctx, task.DatasetID, task.AssetID)
	if err != nil {
		return nil, nil, err
	}
	resolve := func(assetID *uint) *uint {
		if assetID == nil {
			return nil
		}
		tasks, _, e := s.db.ListAnnotationTasksPage(ctx, 
			repository.AnnotationTaskFilter{AssetID: assetID}, 1, 1)
		if e != nil || len(tasks) == 0 {
			return nil
		}
		id := tasks[0].ID
		return &id
	}
	return resolve(prevAsset), resolve(nextAsset), nil
}

// newTraceID returns a 16-byte hex string used as trace_id across the
// routing → ai → human → final chain (plan_v1/01 §8).
func newTraceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
