package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type CheckResult struct {
	Configured bool
	Status     string
	Error      string
}

func Check(ctx context.Context, url string) CheckResult {
	if url == "" {
		return CheckResult{Configured: false, Status: "skipped"}
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}

	return CheckResult{Configured: true, Status: "ok"}
}
