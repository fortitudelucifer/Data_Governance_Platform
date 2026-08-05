// Package testutil provides the shared Postgres test fixture.
//
// 单测不用任何轻量库当 test double——**测试踩到的就是生产 schema 本身**：
// 每个夹具在一次性测试库（TEST_DATABASE_URL，默认本地 data_governance_test）里
// 建一个独立 schema，跑真正的 goose 迁移（唯一约束 / 外键 / jsonb 全部生效），
// 测试结束 DROP SCHEMA CASCADE。schema 隔离让 `go test ./...` 的包级并行安全。
//
// Postgres 不可达时 t.Skip（与 MinIO 依赖测试的既有约定一致），但会在日志
// 里大声说明——本仓库的后端单测把 Postgres 容器当硬依赖，CI 由 postgres:16
// service 提供，本地起容器：
//
//	docker run -d --name data_governance_postgres -e POSTGRES_PASSWORD=postgres \
//	  -e POSTGRES_DB=data_governance -p 5432:5432 postgres:16
//	docker exec data_governance_postgres psql -U postgres -c "CREATE DATABASE data_governance_test"
package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DefaultTestDSN is the disposable local test database (see package comment).
const DefaultTestDSN = "postgres://postgres:postgres@localhost:5432/data_governance_test?sslmode=disable"

// DB opens a fresh, isolated schema in the disposable Postgres test database
// and applies the given migration function (pass repository.RunMigrations —
// 参数化是为了避免 testutil→repository→testutil 的 import 环)。
// The schema is dropped when the test finishes.
func DB(t *testing.T, migrate func(*gorm.DB) error) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = DefaultTestDSN
	}

	quiet := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}

	admin, err := gorm.Open(postgres.Open(dsn), quiet)
	if err == nil {
		err = admin.Exec("SELECT 1").Error
	}
	if err != nil {
		t.Skipf("Postgres 测试库不可达，跳过（这不是绿：起 data_governance_postgres 容器并建 data_governance_test 库，或设 TEST_DATABASE_URL）：%v", err)
	}

	// pg_trgm 装进 pg_catalog——它隐式存在于**每个** search_path 里，于是各测试
	// schema 都能解析 gin_trgm_ops，且集成测试 DROP SCHEMA public 也删不掉它。
	// （fixture 的 search_path 刻意不含 public：否则 goose 会看见集成测试留在
	// public 的 goose_db_version，以为已迁移而**静默跳过建表**——所有 fixture
	// 落到共享的 public，互相污染、序列不从 1 起。踩过。）
	// 并发首次安装/搬家会竞态，用事务级 advisory lock 串行化。
	if err := admin.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('pg_trgm_setup'))").Error; err != nil {
			return err
		}
		return tx.Exec(`DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    BEGIN
      ALTER EXTENSION pg_trgm SET SCHEMA pg_catalog;
    EXCEPTION WHEN OTHERS THEN NULL; -- 已在 pg_catalog
    END;
  ELSE
    CREATE EXTENSION pg_trgm SCHEMA pg_catalog;
  END IF;
END $$`).Error
	}); err != nil {
		t.Fatalf("ensure pg_trgm: %v", err)
	}

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand: %v", err)
	}
	schema := "t_" + hex.EncodeToString(suffix)

	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}

	// search_path 必须进 DSN（连接池的每条连接都要带上），且**只含**测试 schema
	// （不带 public，理由见上）；pg_catalog 隐式殿后，pg_trgm 由此可见。
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	db, err := gorm.Open(postgres.Open(dsn+sep+"search_path="+schema), quiet)
	if err != nil {
		t.Fatalf("connect with schema %s: %v", schema, err)
	}

	// search_path 必须真的生效——若被驱动吞掉，所有测试会静默共享 public schema
	// （序列不从 1 起、互相污染），这正是本项目最怕的假绿。宁可硬失败。
	var current string
	if err := db.Raw("SELECT current_schema()").Scan(&current).Error; err != nil || current != schema {
		t.Fatalf("search_path 未生效：current_schema=%q want %q (err=%v)", current, schema, err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate schema %s: %v", schema, err)
	}

	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		if sqlDB, e := admin.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}
