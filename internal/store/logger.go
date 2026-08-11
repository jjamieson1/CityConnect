package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// slowQuery is the threshold above which a statement is logged at warn level.
const slowQuery = 300 * time.Millisecond

// gormLogger bridges GORM's logger interface onto slog so database output
// joins the same structured stream as everything else, complete with the
// request id carried on the context.
type gormLogger struct {
	log   *slog.Logger
	level logger.LogLevel
}

func newGormLogger(log *slog.Logger) logger.Interface {
	return &gormLogger{log: log.With("component", "db"), level: logger.Warn}
}

func (l *gormLogger) LogMode(level logger.LogLevel) logger.Interface {
	clone := *l
	clone.level = level
	return &clone
}

func (l *gormLogger) Info(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Info {
		l.log.InfoContext(ctx, msg, "args", args)
	}
}

func (l *gormLogger) Warn(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Warn {
		l.log.WarnContext(ctx, msg, "args", args)
	}
}

func (l *gormLogger) Error(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Error {
		l.log.ErrorContext(ctx, msg, "args", args)
	}
}

func (l *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		l.log.ErrorContext(ctx, "query failed",
			"error", err, "elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql)
	case elapsed > slowQuery:
		sql, rows := fc()
		l.log.WarnContext(ctx, "slow query",
			"elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql)
	case l.level >= logger.Info:
		sql, rows := fc()
		l.log.DebugContext(ctx, "query",
			"elapsed_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql)
	}
}
