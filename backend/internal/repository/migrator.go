package repository

import (
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies all pending goose SQL migrations (PostgreSQL).
// schema 只有这一份真源：没有 AutoMigrate、没有第二方言、没有 legacy
// bootstrap——空库直跑迁移即可（执行方案-06 M3/P-H3）。
func RunMigrations(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("migrations: get sql.DB: %w", err)
	}

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migrations: set dialect: %w", err)
	}

	return goose.Up(sqlDB, "migrations")
}
