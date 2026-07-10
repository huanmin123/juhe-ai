package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"

	"juhe-ai/backend-go/internal/platform/postgres"
)

func Check(ctx context.Context, rawURL string) postgres.CheckResult {
	if rawURL == "" {
		return postgres.CheckResult{Configured: false, Status: "skipped"}
	}

	opts, err := goredis.ParseURL(rawURL)
	if err != nil {
		return postgres.CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}

	client := goredis.NewClient(opts)
	defer func() {
		_ = client.Close()
	}()

	if err := client.Ping(ctx).Err(); err != nil {
		return postgres.CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}

	return postgres.CheckResult{Configured: true, Status: "ok"}
}
