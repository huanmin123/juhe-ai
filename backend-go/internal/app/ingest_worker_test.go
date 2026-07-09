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

func TestRunOperationLogRetentionCleanupWorkerRequiresPostgresURL(t *testing.T) {
	cfg := config.Config{
		ShutdownTimeout: time.Second,
	}

	err := RunOperationLogRetentionCleanupWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), OperationLogRetentionCleanupWorkerOptions{RunOnce: true})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("RunOperationLogRetentionCleanupWorker() error = %v, want missing postgres url", err)
	}
}

func TestRunOperationLogRetentionCleanupWorkerValidatesInterval(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunOperationLogRetentionCleanupWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), OperationLogRetentionCleanupWorkerOptions{
		Interval: -time.Second,
		RunOnce:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "清理间隔") {
		t.Fatalf("RunOperationLogRetentionCleanupWorker() error = %v, want invalid interval", err)
	}
}

func TestRunOperationLogRetentionCleanupWorkerValidatesInitialDelay(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunOperationLogRetentionCleanupWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), OperationLogRetentionCleanupWorkerOptions{
		InitialDelay: -time.Second,
		RunOnce:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "初始延迟") {
		t.Fatalf("RunOperationLogRetentionCleanupWorker() error = %v, want invalid initial delay", err)
	}
}

func TestRunAuthorizationUsageRangeWindowRefreshWorkerRequiresPostgresURL(t *testing.T) {
	cfg := config.Config{
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationUsageRangeWindowRefreshWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationUsageRangeWindowRefreshWorkerOptions{RunOnce: true})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("RunAuthorizationUsageRangeWindowRefreshWorker() error = %v, want missing postgres url", err)
	}
}

func TestRunAuthorizationUsageRangeWindowRefreshWorkerValidatesInterval(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationUsageRangeWindowRefreshWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationUsageRangeWindowRefreshWorkerOptions{
		Interval: -time.Second,
		RunOnce:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "刷新间隔") {
		t.Fatalf("RunAuthorizationUsageRangeWindowRefreshWorker() error = %v, want invalid interval", err)
	}
}

func TestRunAuthorizationUsageRangeWindowRefreshWorkerValidatesInitialDelay(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunAuthorizationUsageRangeWindowRefreshWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), AuthorizationUsageRangeWindowRefreshWorkerOptions{
		InitialDelay: -time.Second,
		RunOnce:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "初始延迟") {
		t.Fatalf("RunAuthorizationUsageRangeWindowRefreshWorker() error = %v, want invalid initial delay", err)
	}
}

func TestRunGatewayQuotaSnapshotBuildWorkerRequiresPostgresURL(t *testing.T) {
	cfg := config.Config{
		ShutdownTimeout: time.Second,
	}

	err := RunGatewayQuotaSnapshotBuildWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), GatewayQuotaSnapshotBuildWorkerOptions{RunOnce: true})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("RunGatewayQuotaSnapshotBuildWorker() error = %v, want missing postgres url", err)
	}
}

func TestRunGatewayQuotaSnapshotBuildWorkerRequiresRedisStateURLWhenPublishing(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		RedisNamespace:  "juhe-ai",
		ShutdownTimeout: time.Second,
	}

	err := RunGatewayQuotaSnapshotBuildWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), GatewayQuotaSnapshotBuildWorkerOptions{
		RunOnce:             true,
		PublishRuntimeState: true,
	})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("RunGatewayQuotaSnapshotBuildWorker() error = %v, want missing redis state url", err)
	}
}

func TestRunGatewayQuotaSnapshotBuildWorkerValidatesInterval(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunGatewayQuotaSnapshotBuildWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), GatewayQuotaSnapshotBuildWorkerOptions{
		Interval: -time.Second,
		RunOnce:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "构建间隔") {
		t.Fatalf("RunGatewayQuotaSnapshotBuildWorker() error = %v, want invalid interval", err)
	}
}

func TestRunGatewayQuotaSnapshotBuildWorkerValidatesInitialDelay(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		ShutdownTimeout: time.Second,
	}

	err := RunGatewayQuotaSnapshotBuildWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), GatewayQuotaSnapshotBuildWorkerOptions{
		InitialDelay: -time.Second,
		RunOnce:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "初始延迟") {
		t.Fatalf("RunGatewayQuotaSnapshotBuildWorker() error = %v, want invalid initial delay", err)
	}
}

func TestRunGatewayQuotaSnapshotBuildWorkerValidatesSnapshotTTLWhenPublishing(t *testing.T) {
	cfg := config.Config{
		PostgresURL:     "postgres://juhe_ai:password@127.0.0.1:5432/juhe_ai?sslmode=disable",
		RedisStateURL:   "redis://127.0.0.1:6379/1",
		RedisNamespace:  "juhe-ai",
		ShutdownTimeout: time.Second,
	}

	err := RunGatewayQuotaSnapshotBuildWorker(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), GatewayQuotaSnapshotBuildWorkerOptions{
		RunOnce:             true,
		PublishRuntimeState: true,
		SnapshotTTL:         -time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "Redis TTL") {
		t.Fatalf("RunGatewayQuotaSnapshotBuildWorker() error = %v, want invalid redis ttl", err)
	}
}
