package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
)

func TestRunIngestWorkerRequiresPostgresURL(t *testing.T) {
	cfg := config.Config{
		RedisQueueURL:   "redis://127.0.0.1:6379/2",
		ShutdownTimeout: time.Second,
	}

	err := RunIngestWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("RunIngestWorker() error = %v, want missing postgres url", err)
	}
}

func TestRunIngestWorkerRequiresRedisQueueURL(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunIngestWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_QUEUE_URL") {
		t.Fatalf("RunIngestWorker() error = %v, want missing redis queue url", err)
	}
}

func TestRunAuthorizationExpirySweepWorkerRequiresPostgresURL(t *testing.T) {
	cfg := config.Config{
		RedisStateURL:   "redis://127.0.0.1:6379/1",
		RedisCacheURL:   "redis://127.0.0.1:6379/0",
		RedisNamespace:  "juhe-ai",
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationExpirySweepWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationExpirySweepWorkerOptions{RunOnce: true})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("RunAuthorizationExpirySweepWorker() error = %v, want missing postgres url", err)
	}
}

func TestRunAuthorizationExpirySweepWorkerRequiresRedisStateURL(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		RedisCacheURL:   "redis://127.0.0.1:6379/0",
		RedisNamespace:  "juhe-ai",
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationExpirySweepWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationExpirySweepWorkerOptions{RunOnce: true})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("RunAuthorizationExpirySweepWorker() error = %v, want missing redis state url", err)
	}
}

func TestRunAuthorizationExpirySweepWorkerRequiresRedisCacheURL(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		RedisStateURL:   "redis://127.0.0.1:6379/1",
		RedisNamespace:  "juhe-ai",
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationExpirySweepWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationExpirySweepWorkerOptions{RunOnce: true})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("RunAuthorizationExpirySweepWorker() error = %v, want missing redis cache url", err)
	}
}

func TestRunAuthorizationExpirySweepWorkerValidatesInterval(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		RedisStateURL:   "redis://127.0.0.1:6379/1",
		RedisCacheURL:   "redis://127.0.0.1:6379/0",
		RedisNamespace:  "juhe-ai",
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationExpirySweepWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationExpirySweepWorkerOptions{
		Interval: -time.Second,
		RunOnce:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "扫描间隔") {
		t.Fatalf("RunAuthorizationExpirySweepWorker() error = %v, want invalid interval", err)
	}
}
