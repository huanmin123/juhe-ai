package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// composeTestPostgres provisions the isolated PostgreSQL test database for
// composition tests (PLAN-20261024T051925945Z 测试基建唯一存储设施): fresh
// six-schema DDL + seed via the maintenance bootstrap surface, one database
// per test, dropped afterwards.
func composeTestPostgres(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t,
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.EnsurePostgres(ctx, db)
			return err
		},
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: time.Now, Secret: "compose-test-secret"})
			return err
		})
}

// composeTestConfig builds the PostgreSQL-mode composition config over an
// isolated pgtestkit database + a private miniredis for the cache/state
// drivers (PostgreSQL + Redis 是唯一存储形态): the business owner gates are
// proven programmatically (the same values the operator must provide through
// JUHE_AI_BUSINESS_* envs).
func composeTestConfig(t *testing.T) runtimeConfig {
	t.Helper()
	tdb := composeTestPostgres(t)
	return composeTestConfigForDSN(t, tdb.DSN)
}

// composeTestConfigForDSN builds the PG-mode composition config over an
// existing test database DSN (shared by subprocess-scenario fixtures that
// provision the database themselves).
func composeTestConfigForDSN(t *testing.T, dsn string) runtimeConfig {
	t.Helper()
	root := t.TempDir()
	redisServer := miniredis.RunT(t)
	return runtimeConfig{
		PostgresURL:         dsn,
		BusinessPostgresURL: dsn,
		RedisCacheURL:       "redis://" + redisServer.Addr(),
		RedisStateURL:       "redis://" + redisServer.Addr(),
		RedisNamespace:      "compose-test",
		Secret:              "compose-test-secret",
		// Node runtime.ts:770: dispatchAccountCandidateLimit =
		// integerConfig(..., globalConcurrencyMax, 1, 50_000) — the load path
		// always provides the default (globalMax 5000); the hand-built config
		// mirrors it.
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
		UsageSpoolDirectory:           filepath.Join(root, "usage-record-spool"),
		ChatAssetsRoot:                filepath.Join(root, "chat-assets"),
		ChatMaxTurnsPerConversation:   50,
		ChatRetentionDays:             3,
		ChatToolEnvironment:           "development",
		BusinessOwner:                 "gateway",
		BusinessHandoffConfirmed:      true,
		BusinessNodeWriterStopped:     true,
		BusinessSchemaReady:           true,
		BusinessOwnerEpoch:            "epoch-compose-test",
		SystemAPIEnabled:              true,
		ChainEnabled:                  true,
		CaptchaDisabled:               true,
		AuditLogEnabled:               true,
	}
}

func openComposeOperationStore(t *testing.T, dsn string) operationlog.Store {
	t.Helper()
	config := operationlog.Config{
		Enabled:              true,
		InstanceID:           "compose-test",
		PostgresURL:          dsn,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
		OwnerLease:           30 * time.Second,
		RetentionInterval:    time.Minute,
		RetentionDays:        365,
		RetentionBatchSize:   100,
	}
	store, err := operationlog.OpenStore(config)
	if err != nil {
		t.Fatalf("open operation log store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure operation log schema: %v", err)
	}
	return store
}

// openComposeOperationLease acquires the process-shared F4 lease keeper over
// the compose test store (the same wiring main performs before composing).
func openComposeOperationLease(t *testing.T, store operationlog.Store) *operationlog.LeaseKeeper {
	t.Helper()
	keeper, ok, err := operationlog.StartLeaseKeeper(context.Background(), store, "compose-test", 30*time.Second, nil)
	if err != nil {
		t.Fatalf("start F4 lease keeper: %v", err)
	}
	if !ok {
		t.Fatal("F4 lease keeper refused the unclaimed compose lease")
	}
	t.Cleanup(keeper.Close)
	return keeper
}

// openComposeAuditSources prepares the X04 logreads audit inputs: the F3
// config (PostgreSQL pool handle, hot-search and payload-blob roots) plus a
// provisioned F3 schema, mirroring what main does before composeSystemAPI. It
// also starts the shared F3 lease keeper and the in-process audit producer
// the composition requires (去跨进程战役第四刀). The returned closer releases
// the producer lease and the F3 store.
func openComposeAuditSources(t *testing.T, dsn string) (auditlog.Config, *auditlog.Producer, func()) {
	t.Helper()
	pools := pgpool.NewRegistry()
	t.Cleanup(func() { _ = pools.Close() })
	pooled, err := pools.Acquire(dsn, "gateway-store", 4, 2)
	if err != nil {
		t.Fatalf("open F3 audit shared pool: %v", err)
	}
	config := auditlog.Config{
		InstanceID:               "compose-test",
		PostgresURL:              dsn,
		PostgresPool:             pooled,
		PayloadBlobDirectory:     filepath.Join(t.TempDir(), "audit-blobs"),
		HotSearchDirectory:       filepath.Join(t.TempDir(), "audit-hot"),
		BusinessSettingsURL:      dsn,
		OwnerLease:               30 * time.Second,
		RetentionInterval:        time.Minute,
		RetentionBatchSize:       100,
		SuccessHotRetentionHours: 1,
		SuccessSampleRate:        1.0,
		SuccessRetentionDays:     3,
		ProblemRetentionDays:     7,
	}
	store, err := auditlog.OpenStore(config)
	if err != nil {
		t.Fatalf("open F3 audit store: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("ensure F3 audit schema: %v", err)
	}
	keeper, ok, err := auditlog.StartLeaseKeeper(context.Background(), store, config.InstanceID, 30*time.Second, nil)
	if err != nil || !ok {
		_ = store.Close()
		t.Fatalf("start F3 audit lease keeper: ok=%v err=%v", ok, err)
	}
	producer := auditlog.NewProducer(store, keeper, config, producerLogger{})
	return config, producer, func() {
		keeper.Close()
		_ = store.Close()
	}
}

// createRuntimeLogDataset provisions the F1-jobs-owned runtime-log dataset
// tables in the juhe_dataset schema (the gateway itself never creates another
// owner's schema; this mirrors a deployed F1 indexer).
func createRuntimeLogDataset(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS juhe_dataset`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs (id TEXT PRIMARY KEY, time TIMESTAMPTZ NOT NULL, level TEXT NOT NULL, trace_id TEXT, event TEXT, message TEXT, error_message TEXT, raw_json TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary (bucket_key TEXT PRIMARY KEY, earliest_time TIMESTAMPTZ, latest_time TIMESTAMPTZ, total_count INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets (bucket_key TEXT NOT NULL, level TEXT NOT NULL, count INTEGER NOT NULL, PRIMARY KEY (bucket_key, level))`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets (bucket_key TEXT NOT NULL, event TEXT NOT NULL, count INTEGER NOT NULL, latest_time TIMESTAMPTZ, PRIMARY KEY (bucket_key, event))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create runtime-log dataset table: %v", err)
		}
	}
}

func TestComposeSystemAPIMountsKernelContract(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t, cfg.PostgresURL)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), mustComposeAuditProducer(t, cfg.PostgresURL), composeTestAuditConfig(t, cfg.PostgresURL), composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	get := func(path string) *http.Response {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	decode := func(response *http.Response) map[string]any {
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("decode %s body: %v", response.Request.URL.Path, err)
		}
		return payload
	}

	// Health endpoint: no auth, no rate limit, Node db-service shape.
	health := get("/__aisys__/api/health")
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", health.StatusCode)
	}
	if payload := decode(health); payload["service"] != "juhe-ai-db-service" {
		t.Fatalf("health payload=%#v", payload)
	}

	// Auth surface mounted unauthenticated (captcha disabled contract). The
	// kernel WriteOK envelope mirrors the Node ok() helper ({data, message}).
	captcha := get("/__aisys__/api/auth/captcha")
	if captcha.StatusCode != http.StatusOK {
		t.Fatalf("captcha status=%d", captcha.StatusCode)
	}
	captchaPayload := decode(captcha)
	captchaData, _ := captchaPayload["data"].(map[string]any)
	if captchaData == nil || captchaData["required"] != false {
		t.Fatalf("captcha payload=%#v", captchaPayload)
	}

	// Protected management surface: requireAuth contract without a session.
	for _, path := range []string{
		"/__aisys__/api/groups",
		"/__aisys__/api/api-keys",
		"/__aisys__/api/accounts",
		"/__aisys__/api/my-operation-logs",
		"/__aisys__/api/system-teams",
	} {
		response := get(path)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s status=%d want 401", path, response.StatusCode)
		}
		if payload := decode(response); payload["message"] != "请先登录" {
			t.Fatalf("GET %s payload=%#v", path, payload)
		}
	}

	// J3a's jobs-owned replacement has no Compose ingress yet. Keep the
	// gateway endpoint mounted until that listener is deployed, so the
	// management action cannot silently regress to a 404.
	request, err := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/proxies/missing/test", nil)
	if err != nil {
		t.Fatalf("create proxy test request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST proxy test: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("proxy test endpoint must stay mounted before J3a ingress cutover: status=%d", response.StatusCode)
	}

	// Unmatched API path keeps the Node 404 JSON contract (and the 405->404
	// conversion happens inside the kernel).
	response = get("/__aisys__/api/definitely-not-mounted")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unmatched api status=%d", response.StatusCode)
	}
	if payload := decode(response); payload["message"] != "资源不存在" {
		t.Fatalf("unmatched api payload=%#v", payload)
	}

	// The F4 producer sink must be wired with the shared lease (management
	// mutations persist through it after cutover).
	if composed.producer == nil {
		t.Fatal("composition F4 producer missing")
	}
}

// TestComposeSystemAPIWiresRedisRuntimeStateAuthDrivers proves the shared
// runtime-state auth drivers: the captcha route serves challenges from the
// shared auth_captcha state store under the Node-compatible namespace
// (Redis 是唯一运行态形态 since the storage-shape retirement).
func TestComposeSystemAPIWiresRedisRuntimeStateAuthDrivers(t *testing.T) {
	cfg := composeTestConfig(t)
	redisServer := miniredis.RunT(t)
	cfg.RedisStateURL = "redis://" + redisServer.Addr()
	cfg.RedisNamespace = "dev"
	cfg.CaptchaDisabled = false

	store := openComposeOperationStore(t, cfg.PostgresURL)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), mustComposeAuditProducer(t, cfg.PostgresURL), composeTestAuditConfig(t, cfg.PostgresURL), composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	response, err := http.Get(server.URL + "/__aisys__/api/auth/captcha")
	if err != nil {
		t.Fatalf("GET captcha: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("captcha status=%d", response.StatusCode)
	}
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data == nil || payload.Data["required"] != true || payload.Data["captchaId"] == nil {
		t.Fatalf("captcha payload=%#v", payload.Data)
	}

	// The challenge lives in the namespaced shared store, not in process
	// memory: the Go key equals redisNamespacedKey(`juhe-ai:state:auth_captcha:`).
	found := false
	for _, key := range redisServer.Keys() {
		if strings.HasPrefix(key, "juhe-ai:dev:state:auth_captcha:challenge:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("challenge key missing in shared store; keys=%v", redisServer.Keys())
	}
}

// seedSystemSettings re-applies the Node system_settings seed defaults
// (schema-defaults.ts DEFAULT_SYSTEM_SETTINGS) on the test database: the
// composition reads global settings through the settings store, so a smoke
// environment needs the seeded rows to serve the rate-limit settings. The
// rows are already present from bootstrap.SeedPostgres — this re-seed keeps
// the fixture explicit and idempotent.
func seedSystemSettings(t *testing.T, db *sql.DB) {
	t.Helper()
	defaults := map[string]any{
		"gatewayTextRawBodyLimitMegabytes":           16,
		"accountCircuitConfirmationFailuresRequired": 2,
		"gatewayUserRequestLimitPerMinute":           0,
		"gatewayUserRequestLimitPerDay":              0,
		"gatewayUserRequestLimitPerWeek":             0,
		"gatewayUserRequestLimitPerMonth":            0,
		"userAiAccountLimit":                         100,
		"systemApiRateLimitIpReadPerMinute":          600,
		"systemApiRateLimitIpReadBurstPer10Seconds":  120,
		"systemApiRateLimitIpWritePerMinute":         180,
		"systemApiRateLimitIpWriteBurstPer10Seconds": 40,
		"systemApiRateLimitUserReadPerMinute":        300,
		"systemApiRateLimitUserWritePerMinute":       120,
		"defaultTemporaryUnschedulableMinutes":       2,
		"temporaryUnschedulableRetryIntervalSeconds": 3,
		"temporaryUnschedulableRetryAttempts":        2,
		"textFirstResponseTimeoutSeconds":            120,
		"textNonStreamFirstResponseTimeoutSeconds":   600,
		"textStreamIdleTimeoutSeconds":               30,
		"textUncommittedAttemptMaxLifetimeSeconds":   1800,
		"imageFirstResponseTimeoutSeconds":           600,
		"imageStreamIdleTimeoutSeconds":              120,
		"imageUncommittedAttemptMaxLifetimeSeconds":  3600,
		"imageRequestWallTimeoutSeconds":             3600,
		"chatImageGenerationTotalTimeoutSeconds":     900,
		"audioFirstResponseTimeoutSeconds":           120,
		"videoCreateTimeoutSeconds":                  60,
		"noAvailableAccountWaitTimeoutSeconds":       270,
		"streamFailureThresholdCount":                3,
		"streamFailureThresholdWindowMinutes":        5,
		"operationLogRetentionDays":                  365,
		"operationLogMaxChangesPerRecord":            100,
		"statsAggregationIntervalSeconds":            60,
		"statsAggregationBatchSize":                  2000,
		"statsAggregationMaxBatchesPerRun":           5,
		"usageHotWindowRefreshIntervalSeconds":       600,
		"groupAccountStatsRefreshIntervalSeconds":    60,
		"accountQualityRefreshIntervalSeconds":       600,
		"accountQualityWindowMinutes":                10,
		"accountHealthCheckIntervalHours":            1,
		"accountHealthCheckJitterMinutes":            10,
		"accountHealthCheckFailureThreshold":         3,
		"cooldownAccountRetestIntervalSeconds":       3,
		"cooldownAccountRetestMaxBackoffHours":       12,
		"oauthAccessTokenRefreshIntervalSeconds":     60,
		"oauthAccessTokenRefreshLeadSeconds":         300,
		"oauthAccessTokenRefreshBatchSize":           20,
		"oauthAccessTokenRefreshRetryBackoffSeconds": 300,
		"modelCheckRetentionDays":                    30,
		"runtimeLogIndexRetentionDays":               14,
		"publicApiLogRetentionDays":                  30,
		"usageRecordRetentionDays":                   30,
		"usageStatsTimezone":                         "UTC",
		"usageStatsMinuteRetentionHours":             48,
		"usageStatsHourlyRetentionDays":              60,
		"usageStatsDailyRetentionDays":               400,
		"usageStatsWeeklyRetentionWeeks":             104,
		"usageStatsMonthlyRetentionMonths":           24,
		"usageRankSnapshotRetentionDays":             30,
	}
	// 方言自适应：PG 组合根句柄（pgx）走 juhe_business 限定 + $n 占位符；
	// sqlite 夹具句柄（chain fixture 家族，B5 迁移对象）走裸表 + `?` 占位符。
	// modernc sqlite 驱动具体类型含 "sqlite"；pgx stdlib 驱动类型为 stdlib.Driver。
	sqliteDialect := strings.Contains(fmt.Sprintf("%T", db.Driver()), "sqlite")
	ctx := context.Background()
	for key, value := range defaults {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if !sqliteDialect {
			if _, err := db.ExecContext(ctx, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
				VALUES ('sys_admin', $1, $2, '2026-09-04T00:00:00Z') ON CONFLICT (system_account_id, key) DO NOTHING`, key, string(encoded)); err != nil {
				t.Fatalf("seed %s: %v", key, err)
			}
		} else {
			if _, err := db.ExecContext(ctx, `INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
				VALUES ('sys_admin', ?, ?, '2026-09-04T00:00:00Z')`, key, string(encoded)); err != nil {
				t.Fatalf("seed %s: %v", key, err)
			}
		}
	}
}

// composeTestAuditConfig builds the F3 PG audit config used by composition
// tests that only need the config value (the producer comes from
// mustComposeAuditProducer)。组合根要求 F3 数据集连接池在场，这里同样注入
// 独立共享池句柄（registry 生命周期随测试结束释放）。
func composeTestAuditConfig(t *testing.T, dsn string) auditlog.Config {
	t.Helper()
	pools := pgpool.NewRegistry()
	t.Cleanup(func() { _ = pools.Close() })
	pooled, err := pools.Acquire(dsn, "gateway-store", 4, 2)
	if err != nil {
		t.Fatalf("open F3 audit shared pool: %v", err)
	}
	return auditlog.Config{
		InstanceID:               "compose-test",
		PostgresURL:              dsn,
		PostgresPool:             pooled,
		BusinessSettingsURL:      dsn,
		PayloadBlobDirectory:     filepath.Join(t.TempDir(), "audit-blobs"),
		HotSearchDirectory:       filepath.Join(t.TempDir(), "audit-hot"),
		OwnerLease:               30 * time.Second,
		RetentionInterval:        time.Minute,
		RetentionBatchSize:       100,
		SuccessHotRetentionHours: 1,
		SuccessSampleRate:        1.0,
		SuccessRetentionDays:     3,
		ProblemRetentionDays:     7,
	}
}

// mustComposeAuditProducer starts the shared F3 store + lease + producer for
// a composition test; the closer is registered on t.Cleanup.
func mustComposeAuditProducer(t *testing.T, dsn string) *auditlog.Producer {
	t.Helper()
	_, producer, closeAudit := openComposeAuditSources(t, dsn)
	t.Cleanup(closeAudit)
	return producer
}

func TestGateGatewayChainPassesAfterPhase2(t *testing.T) {
	// Phase 2 authored every frozen-port adapter (see gatewaychain.go), so
	// both gate states pass and the composition assembles for real.
	if err := gateGatewayChain(false); err != nil {
		t.Fatalf("disabled chain must pass: %v", err)
	}
	if err := gateGatewayChain(true); err != nil {
		t.Fatalf("enabled chain must pass after the phase-2 adapters landed: %v", err)
	}
}

// TestLoadRuntimeConfigRequiresStorageEndpoints pins the 2026-10-24
// storage-shape contract: PostgreSQL + Redis 是唯一存储形态，三个连接串 env
// 无条件必填（缺失启动即失败）；普通文件目录派生（usage spool / codex-context
// segments / logs / chat-assets）保留。
func TestLoadRuntimeConfigRequiresStorageEndpoints(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://127.0.0.1:5432/juhe",
		"JUHE_AI_REDIS_CACHE_URL": "redis://127.0.0.1:6379/0",
		"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/1",
	}
	cfg, err := loadRuntimeConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("full storage env must load: %v", err)
	}
	if cfg.PostgresURL != env["JUHE_AI_POSTGRES_URL"] || cfg.RedisCacheURL != env["JUHE_AI_REDIS_CACHE_URL"] || cfg.RedisStateURL != env["JUHE_AI_REDIS_STATE_URL"] {
		t.Fatalf("storage endpoints wrong: %#v", cfg)
	}
	// Spool derivation lands under <DATA_DIR>/usage-record-spool (D1).
	if cfg.UsageSpoolDirectory != filepath.Join("data", "usage-record-spool") {
		t.Fatalf("derived spool directory = %q", cfg.UsageSpoolDirectory)
	}
	if cfg.CodexContextRoot != filepath.Join("data", "codex-context") {
		t.Fatalf("derived codex context root = %q", cfg.CodexContextRoot)
	}
	// Missing任一连接串启动即失败（错误信息可定位）。
	for _, missing := range []string{"JUHE_AI_POSTGRES_URL", "JUHE_AI_REDIS_CACHE_URL", "JUHE_AI_REDIS_STATE_URL"} {
		partial := map[string]string{}
		for key, value := range env {
			if key != missing {
				partial[key] = value
			}
		}
		if _, err := loadRuntimeConfig(func(key string) string { return partial[key] }); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing %s must fail with a locatable error, got %v", missing, err)
		}
	}
	// 旧模式开关 env 不再被读取：显式配置不产生任何效果也不报错。
	legacy := map[string]string{
		"JUHE_AI_RUNTIME_MODE":         "standalone",
		"JUHE_AI_DATABASE_DRIVER":      "sqlite",
		"JUHE_AI_CACHE_DRIVER":         "memory",
		"JUHE_AI_RUNTIME_STATE_DRIVER": "memory",
	}
	for key, value := range env {
		legacy[key] = value
	}
	if _, err := loadRuntimeConfig(func(key string) string { return legacy[key] }); err != nil {
		t.Fatalf("legacy driver env must be ignored (not read), got %v", err)
	}

	// none-cookie requires secure.
	cookieEnv := map[string]string{"JUHE_AI_COOKIE_SAME_SITE": "none"}
	for key, value := range env {
		cookieEnv[key] = value
	}
	if _, err := loadRuntimeConfig(func(key string) string { return cookieEnv[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_COOKIE_SECURE") {
		t.Fatalf("none cookie without secure must fail, got %v", err)
	}
}

// TestLoadRuntimeConfigConcurrencyGlobalMaxBound pins the D2 fix: the Node
// integerConfig('JUHE_AI_CONCURRENCY_GLOBAL_MAX', 5_000, 1, 50_000) bound —
// values above 50000 must fail at startup (runtime.ts:410).
func TestLoadRuntimeConfigConcurrencyGlobalMaxBound(t *testing.T) {
	storage := map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://127.0.0.1:5432/juhe",
		"JUHE_AI_REDIS_CACHE_URL": "redis://127.0.0.1:6379/0",
		"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/1",
	}
	loaded := func(value string) func(string) string {
		return func(key string) string {
			if key == "JUHE_AI_CONCURRENCY_GLOBAL_MAX" {
				return value
			}
			return storage[key]
		}
	}
	cfg, err := loadRuntimeConfig(loaded("50000"))
	if err != nil {
		t.Fatalf("upper bound value must parse: %v", err)
	}
	if cfg.ConcurrencyGlobalMax != 50000 {
		t.Fatalf("ConcurrencyGlobalMax = %d want 50000", cfg.ConcurrencyGlobalMax)
	}
	if _, err := loadRuntimeConfig(loaded("50001")); err == nil || !strings.Contains(err.Error(), "JUHE_AI_CONCURRENCY_GLOBAL_MAX 必须在 1-50000 范围内") {
		t.Fatalf("value above 50000 must fail with the integerConfig copy, got %v", err)
	}
	if _, err := loadRuntimeConfig(loaded("0")); err == nil || !strings.Contains(err.Error(), "JUHE_AI_CONCURRENCY_GLOBAL_MAX 必须在 1-50000 范围内") {
		t.Fatalf("value below 1 must fail with the integerConfig copy, got %v", err)
	}
	if _, err := loadRuntimeConfig(loaded("abc")); err == nil || !strings.Contains(err.Error(), "JUHE_AI_CONCURRENCY_GLOBAL_MAX 必须配置为整数") {
		t.Fatalf("non-integer must fail with the integerConfig copy, got %v", err)
	}
}

func TestBusinessOwnerGateKeepsConnectionPreconditions(t *testing.T) {
	// 清理批次 C1（2026-09-30）：切流五证/cutover evidence 门禁随 Node 清零
	// 整体退役，owner 事实恒 auto-claim——handoff 语义字段不再是门禁输入；
	// businessOwnerGate 只保留连接性前置（缺业务连接串仍 fail closed）。
	tdb := composeTestPostgres(t)
	cfg := composeTestConfigForDSN(t, tdb.DSN)
	if err := cfg.businessOwnerGate(); err != nil {
		t.Fatalf("auto-claimed owner gate must pass: %v", err)
	}
	cfg.BusinessNodeWriterStopped = false
	if err := cfg.businessOwnerGate(); err != nil {
		t.Fatalf("retired cutover-era fields must no longer gate startup: %v", err)
	}
	cfg.BusinessPostgresURL = ""
	if err := cfg.businessOwnerGate(); err == nil {
		t.Fatal("missing business URL must fail closed")
	}
}

// TestComposeSystemAPIMountsLogReadFamilies probes the X04 three log read
// faces end to end: the audit-logs / runtime-logs (incl. grep) / public-api-
// logs route families answer the Node 200 contracts for an admin and deny
// anonymous callers, instead of falling through to the kernel 404.
func TestComposeSystemAPIMountsLogReadFamilies(t *testing.T) {
	tdb := composeTestPostgres(t)
	createRuntimeLogDataset(t, tdb.DB)
	cfg := composeTestConfigForDSN(t, tdb.DSN)
	store := openComposeOperationStore(t, tdb.DSN)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), mustComposeAuditProducer(t, tdb.DSN), composeTestAuditConfig(t, tdb.DSN), composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

	// Admin session (captcha disabled contract, same as the auth surface).
	mustChangePasswordFlag := false
	if _, err := composed.authDeps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: "log-admin", DisplayName: "log-admin_name", Password: "log-admin-password-123", Role: "admin",
		MustChangePassword: &mustChangePasswordFlag,
	}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	loginServer := httptest.NewServer(composed.Kernel)
	defer loginServer.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	login, err := client.Post(loginServer.URL+"/__aisys__/api/auth/login", "application/json",
		strings.NewReader(`{"username":"log-admin","password":"log-admin-password-123"}`))
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	cookies := login.Cookies()
	_ = login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("admin login status=%d", login.StatusCode)
	}

	get := func(path string) *http.Response {
		request, err := http.NewRequest(http.MethodGet, loginServer.URL+path, nil)
		if err != nil {
			t.Fatalf("build GET %s: %v", path, err)
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	decodeData := func(response *http.Response) map[string]any {
		var payload struct {
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("decode %s body: %v", response.Request.URL.Path, err)
		}
		if payload.Data == nil {
			t.Fatalf("GET %s data envelope missing", response.Request.URL.Path)
		}
		return payload.Data
	}

	// Every mounted family answers 200 with the {data} envelope.
	for _, path := range []string{
		"/__aisys__/api/audit-logs",
		"/__aisys__/api/audit-logs/runtime",
		"/__aisys__/api/audit-logs/search-hot",
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/runtime-logs",
		"/__aisys__/api/runtime-logs/facets",
		"/__aisys__/api/runtime-logs/grep-options",
		"/__aisys__/api/runtime-logs/grep",
		"/__aisys__/api/public-api-logs",
	} {
		response := get(path)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, response.StatusCode)
		}
		decodeData(response)
	}

	// The audit search-hot empty-keyword hint and the runtime grep
	// file-logging-disabled degradation both stay 200 (composeTestConfig
	// hand-builds the config with an empty LogDir — the equivalent of the
	// explicit JUHE_AI_LOG_DIR=disabled opt-out since the 2026-09-28
	// default-on derivation — so grep reports the disabled contract).
	hotData := decodeData(get("/__aisys__/api/audit-logs/search-hot"))
	if hotData["available"] != true {
		t.Fatalf("search-hot available=%v", hotData["available"])
	}
	grepData := decodeData(get("/__aisys__/api/runtime-logs/grep?keywords=probe"))
	if grepData["available"] != false || grepData["message"] != "文件日志已显式关闭（JUHE_AI_LOG_DIR=disabled），无法使用 grep 模式。" {
		t.Fatalf("grep payload=%v", grepData)
	}

	// Anonymous callers hit requireAdmin, not the 404 fallthrough.
	anonymous, err := client.Get(loginServer.URL + "/__aisys__/api/audit-logs")
	if err != nil {
		t.Fatalf("anonymous audit-logs: %v", err)
	}
	_ = anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous audit-logs status=%d want 401", anonymous.StatusCode)
	}
}
