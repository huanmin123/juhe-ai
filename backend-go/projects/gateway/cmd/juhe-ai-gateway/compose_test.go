package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// composeTestConfig builds the sqlite-mode composition config over a temp
// directory: the business owner gates are proven programmatically (the same
// values the operator must provide through JUHE_AI_BUSINESS_* envs) and the
// six-database storage preflight paths point at isolated temp files.
func composeTestConfig(t *testing.T) runtimeConfig {
	t.Helper()
	root := t.TempDir()
	// The codex context shard directory must exist before the preflight
	// opens state-*.sqlite3 inside it.
	if err := os.MkdirAll(filepath.Join(root, "codex-context"), 0o755); err != nil {
		t.Fatalf("create codex context shard root: %v", err)
	}
	return runtimeConfig{
		RuntimeMode:                 "standalone",
		DatabaseDriver:              "sqlite",
		CacheDriver:                 "memory",
		RuntimeStateDriver:          "memory",
		QueueDriver:                 "memory",
		Secret:                      "compose-test-secret",
		BusinessDatabasePath:        filepath.Join(root, "business.sqlite3"),
		StatsDatabasePath:           filepath.Join(root, "stats.sqlite3"),
		ChatDatabasePath:            filepath.Join(root, "chat.sqlite3"),
		DatasetDatabasePath:         filepath.Join(root, "dataset.sqlite3"),
		RuntimeLogDatabasePath:      filepath.Join(root, "runtime-log.sqlite3"),
		TableMonitorDatabasePath:    filepath.Join(root, "table-monitor.sqlite3"),
		UsageCatalogDatabasePath:    filepath.Join(root, "usage-catalog.sqlite3"),
		CodexContextShardRoot:       filepath.Join(root, "codex-context"),
		CodexContextShardCount:      1,
		ChatAssetsRoot:              filepath.Join(root, "chat-assets"),
		BusinessOwner:               "gateway",
		BusinessHandoffConfirmed:    true,
		BusinessNodeWriterStopped:   true,
		BusinessSchemaReady:         true,
		BusinessOwnerEpoch:          "epoch-compose-test",
		BusinessCutoverEvidencePath: filepath.Join(root, "evidence.json"),
		SystemAPIEnabled:            true,
		CaptchaDisabled:             true,
	}
}

func openComposeOperationStore(t *testing.T) operationlog.Store {
	t.Helper()
	dir := t.TempDir()
	config := operationlog.Config{
		Enabled:              true,
		Mode:                 operationlog.ModeSQLite,
		InstanceID:           "compose-test",
		DatabasePath:         filepath.Join(dir, "operation-log.sqlite3"),
		BusinessSettingsPath: filepath.Join(dir, "business-settings.sqlite3"),
		OwnerLease:           30 * time.Second,
		RetentionInterval:    time.Minute,
		RetentionDays:        365,
		RetentionBatchSize:   100,
	}
	// The F4 read-only business settings mirror requires the existing file.
	if err := os.WriteFile(config.BusinessSettingsPath, nil, 0o644); err != nil {
		t.Fatalf("create business settings file: %v", err)
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
// config (dataset file, hot-search and payload-blob roots) plus a provisioned
// F3 schema, mirroring what main does before composeSystemAPI. The returned
// closer releases the F3 store (the composition itself only reads it).
func openComposeAuditSources(t *testing.T, root string) (auditlog.Config, func()) {
	t.Helper()
	config := auditlog.Config{
		Mode:                 auditlog.ModeSQLite,
		InstanceID:           "compose-test",
		AuditDatabasePath:    filepath.Join(root, "audit-dataset.sqlite3"),
		PayloadBlobDirectory: filepath.Join(root, "audit-blobs"),
		HotSearchDirectory:   filepath.Join(root, "audit-hot"),
		BusinessSettingsPath: filepath.Join(root, "audit-business-settings.sqlite3"),
	}
	if err := os.WriteFile(config.BusinessSettingsPath, nil, 0o644); err != nil {
		t.Fatalf("create audit business settings file: %v", err)
	}
	store, err := auditlog.OpenStore(config)
	if err != nil {
		t.Fatalf("open F3 audit store: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("ensure F3 audit schema: %v", err)
	}
	return config, func() { _ = store.Close() }
}

// createRuntimeLogDataset provisions the F1-jobs-owned runtime-log file with
// the dataset tables the logreads routes read (the gateway itself never
// creates another owner's schema; this mirrors a deployed F1 indexer).
func createRuntimeLogDataset(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE IF NOT EXISTS runtime_logs (id TEXT PRIMARY KEY, time TEXT NOT NULL, level TEXT NOT NULL, trace_id TEXT, event TEXT, message TEXT, error_message TEXT, raw_json TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_log_facet_summary (bucket_key TEXT PRIMARY KEY, earliest_time TEXT, latest_time TEXT, total_count INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS runtime_log_level_facets (bucket_key TEXT NOT NULL, level TEXT NOT NULL, count INTEGER NOT NULL, latest_time TEXT, PRIMARY KEY (bucket_key, level))`,
		`CREATE TABLE IF NOT EXISTS runtime_log_event_facets (bucket_key TEXT NOT NULL, event TEXT NOT NULL, count INTEGER NOT NULL, latest_time TEXT, PRIMARY KEY (bucket_key, event))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create runtime-log dataset table: %v", err)
		}
	}
	return db
}

func TestComposeSystemAPIMountsKernelContract(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
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

	// Unmatched API path keeps the Node 404 JSON contract (and the 405->404
	// conversion happens inside the kernel).
	response := get("/__aisys__/api/definitely-not-mounted")
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

// TestComposeSystemAPIWiresRedisRuntimeStateAuthDrivers proves the
// runtime-state driver switch (BUG-0171.4): with JUHE_AI_RUNTIME_STATE_DRIVER
// = redis and the captcha enabled, the captcha route serves challenges from
// the shared auth_captcha state store under the Node-compatible namespace.
func TestComposeSystemAPIWiresRedisRuntimeStateAuthDrivers(t *testing.T) {
	cfg := composeTestConfig(t)
	redisServer := miniredis.RunT(t)
	cfg.RuntimeStateDriver = "redis"
	cfg.RedisStateURL = "redis://" + redisServer.Addr()
	cfg.RedisNamespace = "dev"
	cfg.CaptchaDisabled = false

	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

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

// seedSystemSettings mirrors the Node system_settings seed (schema-defaults.ts
// DEFAULT_SYSTEM_SETTINGS): the composition reads global settings through the
// settings store, so a smoke environment needs the seeded rows to serve the
// rate-limit settings before the schema migration owner lands.
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
		"textStreamIdleTimeoutSeconds":               30,
		"textUncommittedAttemptMaxLifetimeSeconds":   1800,
		"imageFirstResponseTimeoutSeconds":           600,
		"imageStreamIdleTimeoutSeconds":              120,
		"imageUncommittedAttemptMaxLifetimeSeconds":  3600,
		"imageRequestWallTimeoutSeconds":             3600,
		"chatImageGenerationTotalTimeoutSeconds":     900,
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
		"systemMetricsSampleIntervalSeconds":         30,
		"tableMonitorMaxTablesPerRun":                4,
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
		"systemMetricsRetentionDays":                 7,
		"systemMetricsHourlyRetentionDays":           30,
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create system_settings: %v", err)
	}
	for key, value := range defaults {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, ?)`, key, string(encoded), "2026-09-04T00:00:00Z"); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
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

func TestLoadRuntimeConfigDriverTriState(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "juhe-ai.sqlite3")

	// Standalone defaults (no hints): sqlite + memory drivers, but sqlite
	// mode requires the explicit database path (no CWD-relative default).
	if _, err := loadRuntimeConfig(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_DATABASE_PATH") {
		t.Fatalf("sqlite without database path must fail, got %v", err)
	}
	cfg, err := loadRuntimeConfig(func(key string) string {
		if key == "JUHE_AI_DATABASE_PATH" {
			return databasePath
		}
		return ""
	})
	if err != nil {
		t.Fatalf("standalone defaults: %v", err)
	}
	if cfg.RuntimeMode != "standalone" || cfg.DatabaseDriver != "sqlite" || cfg.CacheDriver != "memory" || cfg.RuntimeStateDriver != "memory" || cfg.QueueDriver != "memory" {
		t.Fatalf("standalone defaults wrong: %#v", cfg)
	}

	// Performance hints flip every driver default; a redis driver without its
	// URL must fail fast like the Node runtime.ts validation.
	performance := map[string]string{
		"JUHE_AI_DATABASE_PATH":   databasePath,
		"JUHE_AI_POSTGRES_URL":    "postgres://127.0.0.1:5432/juhe",
		"JUHE_AI_REDIS_CACHE_URL": "redis://127.0.0.1:6379/0",
		"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/1",
		"JUHE_AI_REDIS_QUEUE_URL": "redis://127.0.0.1:6379/2",
		"JUHE_AI_DATABASE_DRIVER": "sqlite",
	}
	cfg, err = loadRuntimeConfig(func(key string) string { return performance[key] })
	if err != nil {
		t.Fatalf("performance hints: %v", err)
	}
	if cfg.RuntimeMode != "performance" || cfg.CacheDriver != "redis" || cfg.RuntimeStateDriver != "redis" || cfg.QueueDriver != "redis_stream" {
		t.Fatalf("performance defaults wrong: %#v", cfg)
	}
	performance["JUHE_AI_RUNTIME_STATE_DRIVER"] = "redis"
	delete(performance, "JUHE_AI_REDIS_STATE_URL")
	if _, err := loadRuntimeConfig(func(key string) string { return performance[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("redis state driver without URL must fail, got %v", err)
	}

	// none-cookie requires secure; OIDC requires issuer + secret.
	cookieEnv := map[string]string{"JUHE_AI_DATABASE_PATH": databasePath, "JUHE_AI_COOKIE_SAME_SITE": "none"}
	if _, err := loadRuntimeConfig(func(key string) string { return cookieEnv[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_COOKIE_SECURE") {
		t.Fatalf("none cookie without secure must fail, got %v", err)
	}
	oidcEnv := map[string]string{"JUHE_AI_DATABASE_PATH": databasePath, "JUHE_AI_OIDC_ENABLED": "true"}
	if _, err := loadRuntimeConfig(func(key string) string { return oidcEnv[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_OIDC_ISSUER") {
		t.Fatalf("oidc without issuer must fail, got %v", err)
	}
}

func TestBusinessOwnerGateFailsClosed(t *testing.T) {
	cfg := composeTestConfig(t)
	if err := cfg.businessOwnerGate(); err != nil {
		t.Fatalf("proven gates must pass: %v", err)
	}
	cfg.BusinessNodeWriterStopped = false
	if err := cfg.businessOwnerGate(); err == nil {
		t.Fatal("unstopped Node writer must fail closed")
	}
}

// TestComposeSystemAPIMountsLogReadFamilies probes the X04 three log read
// faces end to end: the audit-logs / runtime-logs (incl. grep) / public-api-
// logs route families answer the Node 200 contracts for an admin and deny
// anonymous callers, instead of falling through to the kernel 404.
func TestComposeSystemAPIMountsLogReadFamilies(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
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
	// leaves JUHE_AI_LOG_DIR unset, so grep reports the disabled contract).
	hotData := decodeData(get("/__aisys__/api/audit-logs/search-hot"))
	if hotData["available"] != true {
		t.Fatalf("search-hot available=%v", hotData["available"])
	}
	grepData := decodeData(get("/__aisys__/api/runtime-logs/grep?keywords=probe"))
	if grepData["available"] != false || grepData["message"] != "文件日志未启用，无法使用 grep 模式。" {
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
