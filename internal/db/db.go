// Package db wires up the GORM connection for either SQLite or PostgreSQL.
package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"instagrampanel/internal/models"
)

type stdLogWriter struct{}

func (stdLogWriter) Printf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

func Open(driver, dsn string) (*gorm.DB, error) {
	var dialector gorm.Dialector

	switch driver {
	case "postgres":
		dialector = postgres.Open(dsn)
	case "sqlite":
		if dir := filepath.Dir(dsn); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create sqlite data dir: %w", err)
			}
		}
		dialector = sqlite.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported DB_DRIVER %q (use sqlite or postgres)", driver)
	}

	gdb, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.New(stdLogWriter{}, logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := models.Migrate(gdb); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return gdb, nil
}
