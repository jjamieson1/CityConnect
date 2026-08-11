// Package storetest provides an in-memory database for service-layer tests.
// It lives in its own package so the production binary never links `testing`.
package storetest

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// New returns an isolated in-memory database with the full schema
// applied. SQLite is used deliberately: it is pure Go, so `go test ./...`
// needs no MariaDB container and CI stays fast. Anything that depends on
// MariaDB-specific behaviour — FULLTEXT search in particular — degrades to the
// LIKE fallback here, and is covered by integration tests instead.
func New(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Discard,
		SkipDefaultTransaction: true,
		// Matches production: optional relationships are empty-string ids, so
		// integrity lives in the service layer rather than in FK constraints.
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(domain.AllModels()...); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
