package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	"juhe-ai/backend-go/internal/store/port"
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

func TestRunAccountTestWorkerRequiresRedisQueueURL(t *testing.T) {
	err := RunAccountTestWorker(context.Background(), config.Config{
		NodeInternalBaseURL:        "http://127.0.0.1:3000",
		NodeInternalRequestTimeout: time.Second,
		Secret:                     "account-test-worker-secret",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_QUEUE_URL") {
		t.Fatalf("RunAccountTestWorker() error = %v, want missing redis queue url", err)
	}
}

func TestRunAccountTestWorkerRequiresNodeInternalBaseURL(t *testing.T) {
	err := RunAccountTestWorker(context.Background(), config.Config{
		RedisQueueURL:              "redis://127.0.0.1:6379/2",
		NodeInternalRequestTimeout: time.Second,
		Secret:                     "account-test-worker-secret",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_NODE_INTERNAL_BASE_URL") {
		t.Fatalf("RunAccountTestWorker() error = %v, want missing node internal base URL", err)
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

func TestNewAuthorizationExpirySweepServiceWiresPageDataDependencies(t *testing.T) {
	store := &authorizationExpiryWorkerStoreStub{
		expiryResult: port.ManagementResourceAuthorizationExpirySweepResult{
			Expired: 1,
			Authorizations: []port.ManagementResourceAuthorizationExpiryFanout{{
				AuthorizationID:              "rauthgrant_team",
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceOwnerSystemAccountID: "owner",
				GranteeType:                  "team",
				GranteeTeamID:                "team_main",
			}},
		},
		teamFound: true,
		teamResult: port.ManagementSystemTeamDetail{Members: []port.ManagementSystemTeamMemberSummary{
			{SystemAccountID: "member_active", Status: "active"},
			{SystemAccountID: "member_disabled", Status: "disabled"},
		}},
	}
	publisher := &authorizationExpiryWorkerPublisherStub{err: errors.New("redis unavailable")}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	service := newAuthorizationExpirySweepService(store, nil, publisher, store, logger)

	result, err := service.ExpireDue(context.Background(), managementauthorizations.ExpirySweepInput{Limit: 1})

	if err != nil || result.Expired != 1 {
		t.Fatalf("ExpireDue() result=%+v error=%v", result, err)
	}
	if store.teamCalls != 1 || store.teamID != "team_main" {
		t.Fatalf("team lookup calls=%d teamID=%q", store.teamCalls, store.teamID)
	}
	if publisher.calls != 1 || !reflect.DeepEqual(publisher.owners, []string{"member_active", "owner"}) || publisher.allScopes {
		t.Fatalf("publisher calls=%d owners=%#v allScopes=%v", publisher.calls, publisher.owners, publisher.allScopes)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "redis unavailable") {
		t.Fatalf("warning log = %q", logs.String())
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

type authorizationExpiryWorkerStoreStub struct {
	expiryResult port.ManagementResourceAuthorizationExpirySweepResult
	expiryErr    error
	teamCalls    int
	teamID       string
	teamResult   port.ManagementSystemTeamDetail
	teamFound    bool
	teamErr      error
}

func (s *authorizationExpiryWorkerStoreStub) ExpireDueManagementResourceAuthorizations(_ context.Context, _ port.ManagementResourceAuthorizationExpirySweepInput) (port.ManagementResourceAuthorizationExpirySweepResult, error) {
	return s.expiryResult, s.expiryErr
}

func (s *authorizationExpiryWorkerStoreStub) FindManagementSystemTeam(_ context.Context, teamID string, _ string) (port.ManagementSystemTeamDetail, bool, error) {
	s.teamCalls++
	s.teamID = teamID
	return s.teamResult, s.teamFound, s.teamErr
}

type authorizationExpiryWorkerPublisherStub struct {
	calls     int
	owners    []string
	allScopes bool
	err       error
}

func (s *authorizationExpiryWorkerPublisherStub) PublishAccountsStaticReset(_ context.Context, owners []string, allScopes bool) error {
	s.calls++
	s.owners = append([]string(nil), owners...)
	s.allScopes = allScopes
	return s.err
}
