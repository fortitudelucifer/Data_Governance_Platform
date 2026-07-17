package service

import (
	"context"
	"errors"
	"sync"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// batch_annotate_service.go — list-level batch auto-annotation for assets
// (image/audio/video). Select tasks → pick capability+model → run them through
// the model-aware ad-hoc invoke at a bounded concurrency (the "流量节奏").
//
// Item 3 hardening: jobs are persisted to the relational DB (batch_jobs) so progress
// survives restarts, is visible across instances, and is cross-instance
// cancellable (the runner polls the DB status). The worker goroutine still runs
// on the instance that started it; on restart, in-flight jobs are reconciled to
// "interrupted".

// ErrBatchRunning is returned when a dataset already has a running batch.
var ErrBatchRunning = errors.New("a batch job is already running for this dataset")

// BatchAnnotateService runs batch auto-annotation jobs.
type BatchAnnotateService struct {
	adhoc *AdHocInvocationService
	db *repository.DB
	mu    sync.Mutex
	// local cancel funcs for goroutines started on this instance, by job id.
	cancels map[string]context.CancelFunc
}

// NewBatchAnnotateService wires the ad-hoc invoker + persistence.
func NewBatchAnnotateService(adhoc *AdHocInvocationService, db *repository.DB) *BatchAnnotateService {
	return &BatchAnnotateService{adhoc: adhoc, db: db, cancels: map[string]context.CancelFunc{}}
}

// ReconcileOnStartup marks orphaned "running" jobs as "interrupted".
func (s *BatchAnnotateService) ReconcileOnStartup(ctx context.Context) {
	if s.db != nil {
		_ = s.db.ReconcileRunningBatchJobs(ctx)
	}
}

// Start launches a batch job for the given tasks. concurrency clamps to [1,8].
func (s *BatchAnnotateService) Start(datasetID uint, taskIDs []uint, capability, model string, concurrency int) (*dbmodel.BatchJob, error) {
	if s.adhoc == nil || s.db == nil {
		return nil, errors.New("batch annotation not configured")
	}
	if capability == "" {
		return nil, errors.New("capability required")
	}
	if len(taskIDs) == 0 {
		return nil, errors.New("no tasks selected")
	}
	if concurrency < 1 {
		concurrency = 2
	}
	if concurrency > 8 {
		concurrency = 8
	}

	ctxBg := context.Background()
	if running, _ := s.db.HasRunningBatchJob(ctxBg, datasetID); running {
		return nil, ErrBatchRunning
	}
	id, err := randHex(16)
	if err != nil {
		return nil, err
	}
	job := &dbmodel.BatchJob{
		JobID: id, DatasetID: datasetID, Capability: capability, Model: model,
		Total: len(taskIDs), Status: dbmodel.BatchStatusRunning, StartedAt: time.Now(),
	}
	if err := s.db.CreateBatchJob(ctxBg, job); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()

	go s.run(ctx, job.JobID, taskIDs, capability, model, concurrency)
	return job, nil
}

func (s *BatchAnnotateService) run(ctx context.Context, jobID string, taskIDs []uint, capability, model string, concurrency int) {
	defer func() {
		s.mu.Lock()
		delete(s.cancels, jobID)
		s.mu.Unlock()
	}()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var cmu sync.Mutex
	done, failed := 0, 0
	cancelled := false

	for _, tid := range taskIDs {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		// cross-instance cancel: the DB status may have been flipped elsewhere.
		if st, err := s.db.GetBatchJobStatus(context.Background(), jobID); err == nil && st != dbmodel.BatchStatusRunning {
			cancelled = true
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(taskID uint) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := s.adhoc.InvokeForTask(ctx, taskID, capability, model)
			cmu.Lock()
			if err != nil || res == nil || res.Status != "success" {
				failed++
			} else {
				done++
			}
			d, f := done, failed
			cmu.Unlock()
			_ = s.db.UpdateBatchJobProgress(context.Background(), jobID, d, f)
		}(tid)
	}
	wg.Wait()
	cmu.Lock()
	_ = s.db.UpdateBatchJobProgress(context.Background(), jobID, done, failed)
	cmu.Unlock()

	// Only mark completed if we weren't cancelled (cancel sets the status).
	if !cancelled && ctx.Err() == nil {
		if st, _ := s.db.GetBatchJobStatus(context.Background(), jobID); st == dbmodel.BatchStatusRunning {
			_ = s.db.SetBatchJobStatus(context.Background(), jobID, dbmodel.BatchStatusCompleted)
		}
	}
}

// Status returns the latest job for a dataset (nil if none).
func (s *BatchAnnotateService) Status(datasetID uint) *dbmodel.BatchJob {
	if s.db == nil {
		return nil
	}
	j, err := s.db.FindLatestBatchJobByDataset(context.Background(), datasetID)
	if err != nil {
		return nil
	}
	return j
}

// Cancel stops the running batch for a dataset (cross-instance via DB status;
// in-flight invokes on this instance are also cancelled via ctx). Returns true
// if a running job was cancelled.
func (s *BatchAnnotateService) Cancel(datasetID uint) bool {
	if s.db == nil {
		return false
	}
	j, err := s.db.FindLatestBatchJobByDataset(context.Background(), datasetID)
	if err != nil || j.Status != dbmodel.BatchStatusRunning {
		return false
	}
	_ = s.db.SetBatchJobStatus(context.Background(), j.JobID, dbmodel.BatchStatusCancelled)
	s.mu.Lock()
	if cancel, ok := s.cancels[j.JobID]; ok {
		cancel()
	}
	s.mu.Unlock()
	return true
}
