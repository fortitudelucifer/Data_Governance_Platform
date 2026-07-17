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
