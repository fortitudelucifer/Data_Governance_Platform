package repository

// 执行方案-06 P1/P2/P3 验收的机器可执行版：迁移在真 Postgres 上跑一遍，
// 然后逐条断言「schema 的属性」——部分唯一索引、外键级联、并发去重恰一行。
//
// 需要一个**可丢弃**的库（测试会 DROP SCHEMA public CASCADE）：
//
//	TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/data_governance_test?sslmode=disable
//
// 未设置时整组跳过——本地 `go test ./...` 不强制要求 Docker。CI 的后端 job
// 起 postgres:16 service 并设置该变量：**迁移文件在 CI 里真的被执行**，
// 不会腐烂成「只在某台 dev 机器上验证过」。
//
// 变异验证（P2）：把 000001_init.sql 里 idx_assets_dataset_sha 的 UNIQUE 去掉，
// TestPostgres_ConcurrentDuplicateUpload_ExactlyOneRow 必须失败。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// pgTestRepo resets the disposable test database and runs the goose migrations.
func pgTestRepo(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration tests")
	}
	raw, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := raw.Exec("DROP SCHEMA public CASCADE").Error; err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := raw.Exec("CREATE SCHEMA public").Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if sqlDB, e := raw.DB(); e == nil {
		_ = sqlDB.Close()
	}

	repo, err := NewDB(dsn) // 连接 + 跑全部 goose 迁移
	if err != nil {
		t.Fatalf("NewDB (migrations): %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := repo.DB.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return repo
}

// P1：`\d assets` 的自动化等价——唯一索引带谓词、外键指向 datasets 且 CASCADE。
func TestPostgres_SchemaConstraints(t *testing.T) {
	repo := pgTestRepo(t)

	var indexdef string
	if err := repo.DB.Raw(
		`SELECT indexdef FROM pg_indexes WHERE tablename = 'assets' AND indexname = 'idx_assets_dataset_sha'`,
	).Scan(&indexdef).Error; err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	if !strings.Contains(indexdef, "UNIQUE") {
		t.Errorf("idx_assets_dataset_sha 不是 UNIQUE：%q", indexdef)
	}
	if !strings.Contains(indexdef, "qc_status") {
		t.Errorf("idx_assets_dataset_sha 缺 qc_status='passed' 谓词：%q", indexdef)
	}

	// 数据脊柱上的级联外键（confdeltype 'c' = ON DELETE CASCADE）。
	type fk struct{ child, parent string }
	for _, f := range []fk{
		{"assets", "datasets"},
		{"annotation_tasks", "assets"},
		{"annotation_tasks", "datasets"},
		{"asset_derivatives", "assets"},
		{"upload_sessions", "datasets"},
		{"documents", "datasets"},
		{"batch_jobs", "datasets"},
	} {
		var deltype string
		q := fmt.Sprintf(
			`SELECT string_agg(confdeltype, ',') FROM pg_constraint
			 WHERE contype = 'f' AND conrelid = '%s'::regclass AND confrelid = '%s'::regclass`,
			f.child, f.parent)
		if err := repo.DB.Raw(q).Scan(&deltype).Error; err != nil {
			t.Fatalf("query pg_constraint %s→%s: %v", f.child, f.parent, err)
		}
		if !strings.Contains(deltype, "c") {
			t.Errorf("%s → %s 缺 ON DELETE CASCADE 外键（confdeltype=%q）", f.child, f.parent, deltype)
		}
	}
}

// P3-1：并发 10 个相同文件的「插入」→ assets 表恰好 1 行。
// 唯一性必须是数据库的属性：这里刻意绕过 FindAssetBySHA256 的快路径，
// 直接并发打 CreateAssetDedup——先查后写时代这正是插出两行的竞态窗口。
func TestPostgres_ConcurrentDuplicateUpload_ExactlyOneRow(t *testing.T) {
	repo := pgTestRepo(t)
	ctx := context.Background()

	ds := &dbmodel.Dataset{Name: "pg-dedup", Modality: dbmodel.ModalityImage}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	sha := strings.Repeat("ab", 32)

	var inserted int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := &dbmodel.Asset{
				DatasetID: ds.ID, Modality: "image", StorageURI: "sha256/" + sha,
				SHA256: sha, QCStatus: dbmodel.QCStatusPassed,
			}
			ok, err := repo.CreateAssetDedup(ctx, a)
			if err != nil {
				t.Errorf("CreateAssetDedup: %v", err)
				return
			}
			if ok {
				atomic.AddInt32(&inserted, 1)
			}
		}()
	}
	wg.Wait()

	var n int64
	repo.DB.Model(&dbmodel.Asset{}).Where("dataset_id = ? AND sha256 = ?", ds.ID, sha).Count(&n)
	if n != 1 {
		t.Fatalf("并发去重后 assets 行数 = %d，want 恰好 1", n)
	}
	if inserted != 1 {
		t.Fatalf("赢家应恰好 1 个，got %d", inserted)
	}

	// 谓词只盖 passed：同一个坏文件（QC 失败行）反复上传不受唯一键限制——
	// 拒收记录是给操作员看的，不该 500。
	for i := 0; i < 2; i++ {
		f := &dbmodel.Asset{DatasetID: ds.ID, Modality: "image", SHA256: sha, QCStatus: dbmodel.QCStatusFailed}
		ok, err := repo.CreateAssetDedup(ctx, f)
		if err != nil || !ok {
			t.Fatalf("QC 失败行第 %d 次插入应成功（ok=%v err=%v）", i+1, ok, err)
		}
	}
}

// P3-2：删数据集 → 关系行全部级联消失（blob / 载荷行的清理在 service 层，
// 见 CompensationHandler.DeleteDatasetWithCompensation；这里锁死 schema 半边）。
func TestPostgres_DeleteDatasetCascades(t *testing.T) {
	repo := pgTestRepo(t)
	ctx := context.Background()

	ds := &dbmodel.Dataset{Name: "pg-cascade", Modality: dbmodel.ModalityImage}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	a := &dbmodel.Asset{DatasetID: ds.ID, Modality: "image", SHA256: strings.Repeat("cd", 32), QCStatus: dbmodel.QCStatusPassed}
	if err := repo.DB.Create(a).Error; err != nil {
		t.Fatalf("create asset: %v", err)
	}
	task := &dbmodel.AnnotationTask{AssetID: a.ID, DatasetID: ds.ID, State: dbmodel.TaskStateCreated}
	if err := repo.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	deriv := &dbmodel.AssetDerivative{AssetID: a.ID, Kind: "thumbnail"}
	if err := repo.DB.Create(deriv).Error; err != nil {
		t.Fatalf("create derivative: %v", err)
	}

	if err := repo.DeleteDataset(ctx, ds.ID); err != nil {
		t.Fatalf("delete dataset: %v", err)
	}

	for name, model := range map[string]interface{}{
		"assets":            &dbmodel.Asset{},
		"annotation_tasks":  &dbmodel.AnnotationTask{},
		"asset_derivatives": &dbmodel.AssetDerivative{},
	} {
		var n int64
		repo.DB.Model(model).Count(&n)
		if n != 0 {
			t.Errorf("删数据集后 %s 残留 %d 行（应级联清零）", name, n)
		}
	}
}

// P1-2：「我的任务」不得全表扫描。
//
// 谓词是 `assignee_id = ? OR reviewer_id = ?`，这是标注员最高频的查询（干完一条
// 就回来刷列表）。这两列一旦没索引，它就是 Seq Scan——**而开发库只有几行，所以
// 永远很快**，正是《净环境自检》说的「开发库上永远绿」的形态：等任务表长到十万
// 行、几十人同时刷，这里第一个融化，症状只是「越来越慢」，不报任何错。
//
// 所以这条测试**必须自己造够数据量**：行数太少时 Postgres 无论有没有索引都选
// Seq Scan（扫 4 行比走索引便宜），空表上断言等于没断言。
//
// 变异验证：把 000001_init.sql 里 idx_annotation_tasks_assignee /
// _reviewer 两行删掉，本测试必须失败。
func TestPostgres_MyTasksQueryUsesIndex(t *testing.T) {
	repo := pgTestRepo(t)

	ds := &dbmodel.Dataset{Name: "idx-plan", Modality: dbmodel.ModalityImage}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	a := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityImage,
		SHA256: strings.Repeat("b1", 32), QCStatus: dbmodel.QCStatusPassed}
	if err := repo.DB.Create(a).Error; err != nil {
		t.Fatalf("create asset: %v", err)
	}

	// 5000 条任务、200 个 assignee —— 每人 ~25 条，谓词足够选择性，
	// 有索引时规划器必然弃用 Seq Scan。
	if err := repo.DB.Exec(`
		INSERT INTO annotation_tasks (asset_id, dataset_id, assignee_id, reviewer_id, state)
		SELECT ?, ?, (g % 200) + 1, (g % 197) + 1, 'HUMAN_PENDING'
		FROM generate_series(1, 5000) g`, a.ID, ds.ID).Error; err != nil {
		t.Fatalf("bulk insert tasks: %v", err)
	}
	// 没有 ANALYZE，规划器没有统计信息，可能仍旧瞎选。
	if err := repo.DB.Exec(`ANALYZE annotation_tasks`).Error; err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// 断言挑在 COUNT 上，而不是 `ORDER BY id DESC LIMIT 20` 那条——这一点是变异
	// 测试逼出来的：带 ORDER BY+LIMIT 时，规划器可以**主键索引倒扫 + 过滤**提前
	// 收工（`Index Scan Backward using annotation_tasks_pkey`），于是「不是 Seq Scan」
	// 和「计划里有 Index」这两条断言**在索引被删掉时照样成立**——空断言。
	// COUNT 没有 ORDER BY 这条逃生通道，只能在 Seq Scan 与位图索引扫描之间二选一，
	// 才是真正能区分对错的那个查询。而它正是列表页每次都要跑的（ListTasks 先
	// Count(&total) 再取页）。
	var lines []string
	if err := repo.DB.Raw(
		`EXPLAIN SELECT count(*) FROM annotation_tasks
		 WHERE (assignee_id = 7 OR reviewer_id = 7)`,
	).Scan(&lines).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}
	plan := strings.Join(lines, "\n")

	if strings.Contains(plan, "Seq Scan on annotation_tasks") {
		t.Errorf("「我的任务」的总数查询走了全表扫描——assignee_id / reviewer_id 的索引没了。\n计划：\n%s", plan)
	}
	if !strings.Contains(plan, "Index") {
		t.Errorf("查询计划里没有任何 Index 节点，说明没用上索引。\n计划：\n%s", plan)
	}
}
