// Package db opens a service's own PostgreSQL database through GORM.
package db

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"

	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/outbox"
)

// MustOpen connects to the database, retrying while it starts, and migrates models.
// Every service database also gets the transactional outbox table.
func MustOpen(cfg config.Config, models ...any) *gorm.DB {
	if cfg.DatabaseURL == "" {
		panic(fmt.Sprintf("%s: DATABASE_URL is required", cfg.Service))
	}
	var (
		gdb *gorm.DB
		err error
	)
	for attempt := 1; attempt <= 60; attempt++ {
		gdb, err = gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{
			Logger: logger.New(log.New(os.Stdout, "", 0), logger.Config{
				SlowThreshold: time.Second, LogLevel: logger.Warn, IgnoreRecordNotFoundError: true, Colorful: false,
			}),
			NowFunc:        func() time.Time { return time.Now().UTC() },
			TranslateError: true,
		})
		if err == nil {
			var sqlDB interface{ Ping() error }
			if sqlDB, err = gdb.DB(); err == nil {
				err = sqlDB.Ping()
			}
		}
		if err == nil {
			break
		}
		slog.Warn("database not ready, retrying", "attempt", attempt, "err", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		panic(fmt.Sprintf("%s: cannot connect to database: %v", cfg.Service, err))
	}
	if cfg.OTLPEndpoint != "" {
		if err := gdb.Use(tracing.NewPlugin(tracing.WithoutMetrics())); err != nil {
			slog.Warn("gorm tracing disabled", "err", err)
		}
	}
	sqlDB, _ := gdb.DB()
	sqlDB.SetMaxOpenConns(int(config.Int("DB_MAX_OPEN_CONNS", 10)))
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	all := append([]any{&outbox.Event{}}, models...)
	if err := gdb.AutoMigrate(all...); err != nil {
		panic(fmt.Sprintf("%s: migration failed: %v", cfg.Service, err))
	}
	slog.Info("database ready", "models", len(all))
	return gdb
}
