// Package store owns the database connection and the query helpers shared by
// every service package: pagination, transactions, and error translation.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/domain"
)

// Common persistence errors, translated from driver-specific failures so
// service packages never import GORM error types.
var (
	ErrNotFound  = errors.New("store: not found")
	ErrConflict  = errors.New("store: conflict")
	ErrDuplicate = errors.New("store: duplicate")
)

// Open connects to the database, applies pool settings and optionally runs
// AutoMigrate.
func Open(cfg config.DBConfig, log *slog.Logger) (*gorm.DB, error) {
	gcfg := &gorm.Config{
		Logger:                 newGormLogger(log),
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },

		// Optional relationships are modelled as empty-string ids rather than
		// nullable pointers, which keeps the models and their JSON readable
		// across roughly twenty optional foreign keys. Database-level FK
		// constraints cannot express that ("" is not NULL), so referential
		// integrity is enforced in the service layer instead — every write
		// path validates its references, and the indexes are still created.
		DisableForeignKeyConstraintWhenMigrating: true,
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), gcfg)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("store: sql handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return db, nil
}

// Migrate runs AutoMigrate for every model and then creates the FULLTEXT
// indexes GORM cannot express.
func Migrate(db *gorm.DB, log *slog.Logger) error {
	if err := db.AutoMigrate(domain.AllModels()...); err != nil {
		return fmt.Errorf("store: automigrate: %w", err)
	}
	if db.Dialector.Name() != "mysql" {
		return nil
	}
	for _, ix := range domain.FullTextIndexes {
		if db.Migrator().HasIndex(ix.Table, ix.Name) {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE `%s` ADD FULLTEXT INDEX `%s` (%s)", ix.Table, ix.Name, ix.Columns)
		if err := db.Exec(stmt).Error; err != nil {
			// A missing FULLTEXT index degrades search to LIKE rather than
			// breaking the app, so this is logged and not fatal.
			log.Warn("could not create fulltext index", "index", ix.Name, "error", err)
		}
	}
	return nil
}

// Tx runs fn inside a transaction, rolling back on error or panic.
func Tx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(fn)
}

// Translate converts a GORM/driver error into one of the package sentinels.
func Translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return ErrDuplicate
	}
	msg := err.Error()
	if strings.Contains(msg, "Duplicate entry") || strings.Contains(msg, "UNIQUE constraint failed") {
		return fmt.Errorf("%w: %s", ErrDuplicate, msg)
	}
	return err
}

// Page describes a paginated, sorted list request. Municipal volumes are
// small enough (tens of thousands of requests a year) that offset paging with
// an exact total is the right trade: the console wants page numbers and result
// counts, which keyset paging cannot give without a second query anyway.
type Page struct {
	Limit  int
	Offset int
	SortBy string
	Desc   bool
}

// DefaultLimit and MaxLimit bound page sizes.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Normalize clamps the page window into range.
func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

// Result carries one page of rows plus the totals the console needs.
type Result[T any] struct {
	Items   []T   `json:"items"`
	Total   int64 `json:"total"`
	Limit   int   `json:"limit"`
	Offset  int   `json:"offset"`
	HasMore bool  `json:"hasMore"`
}

// Paginate counts the matching rows, then fetches one page of them.
//
// sortable maps the API-visible sort keys to real column names. Sort keys are
// never interpolated from user input directly: an unmapped key falls back to
// the default rather than reaching the SQL string.
func Paginate[T any](q *gorm.DB, p Page, sortable map[string]string, defaultSort string, dest *[]T) (Result[T], error) {
	p = p.Normalize()
	res := Result[T]{Limit: p.Limit, Offset: p.Offset}

	if err := q.Session(&gorm.Session{}).Count(&res.Total).Error; err != nil {
		return res, Translate(err)
	}

	column := defaultSort
	if mapped, ok := sortable[p.SortBy]; ok && mapped != "" {
		column = mapped
	}
	dir := "ASC"
	if p.Desc {
		dir = "DESC"
	}

	err := q.Order(fmt.Sprintf("%s %s", column, dir)).
		Order("id " + dir).
		Limit(p.Limit).Offset(p.Offset).
		Find(dest).Error
	if err != nil {
		return res, Translate(err)
	}

	res.Items = *dest
	if res.Items == nil {
		res.Items = []T{}
	}
	res.HasMore = int64(p.Offset+len(res.Items)) < res.Total
	return res, nil
}

// Exists reports whether any row matches the given query.
func Exists(q *gorm.DB) (bool, error) {
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, Translate(err)
	}
	return n > 0, nil
}

// LikeEscape neutralises LIKE wildcards in user-supplied search terms so a
// query for "100%" does not match everything.
func LikeEscape(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return r.Replace(s)
}
