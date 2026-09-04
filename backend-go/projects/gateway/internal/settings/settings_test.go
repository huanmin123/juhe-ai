package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) snapshot() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry(nil), s.entries...)
}

type recordingInvalidator struct {
	mu     sync.Mutex
	events [][2]string
}

func (i *recordingInvalidator) Invalidate(topic, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.events = append(i.events, [2]string{topic, reason})
}

func (i *recordingInvalidator) has(topic, reason string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, event := range i.events {
		if event[0] == topic && event[1] == reason {
			return true
		}
	}
	return false
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
}

// seedSystemSettings mirrors DEFAULT_SYSTEM_SETTINGS (schema-defaults.ts)
// with the timezone pinned to UTC (Node seeds the process timezone).
var seedSystemSettings = map[string]any{
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

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:settings-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`,
		`CREATE TABLE IF NOT EXISTS global_settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// The timezone guard probes the usage stats projections; the empty
	// tables mirror a fresh stats database.
	for _, tableName := range usageStatsDataTables {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + tableName + ` (probe TEXT)`); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, value := range seedSystemSettings {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, execErr := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, ?)`, key, string(raw), now); execErr != nil {
			t.Fatal(execErr)
		}
	}
	for _, seed := range []struct{ key, value string }{
		{"appName", "聚合 AI"},
		{"appIcon", "/__aisys__/brand-icon.svg"},
	} {
		raw, _ := json.Marshal(seed.value)
		if _, err := db.Exec(`INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?)`, seed.key, string(raw), now); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	sink := &recordingSink{}
	invalidator := &recordingInvalidator{}
	store, err := NewStore(db, false, nil, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	e.mu.Lock()
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
		}
	}
	e.mu.Unlock()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func (e *testEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	id := ""
	if existing, err := e.deps.Accounts.FindByUsername(context.Background(), username); err == nil {
		id = existing.ID
	}
	if id == "" {
		created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
			Username: username, DisplayName: username + "_name", Password: password, Role: role,
			MustChangePassword: boolPtr(false),
		})
		if err != nil {
			t.Fatal(err)
		}
		id = created.ID
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	return id
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) queryCell(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := e.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value.String
}

func boolPtr(v bool) *bool { return &v }

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

func message(t *testing.T, payload map[string]any) string {
	t.Helper()
	text, ok := payload["message"].(string)
	if !ok {
		t.Fatalf("missing message: %v", payload)
	}
	return text
}

// TestSettingsGetRequiresAdminAndFullWhitelist covers the auth surface and
// the 60-key full snapshot contract of GET /settings.
func TestSettingsGetRequiresAdminAndFullWhitelist(t *testing.T) {
	env := newTestEnv(t)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings", "")
	if code != http.StatusUnauthorized || message(t, payload) != "请先登录" {
		t.Fatalf("anonymous get: %d %v", code, payload)
	}

	env.login(t, "worker", "worker-pass", "user")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings", "")
	if code != http.StatusForbidden || message(t, payload) != "需要管理员权限" {
		t.Fatalf("user get: %d %v", code, payload)
	}

	env.login(t, "root", "root-pass", "super_admin")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings", "")
	if code != http.StatusOK {
		t.Fatalf("admin get: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if len(data) != len(SystemSettingKeys) {
		t.Fatalf("whitelist size: %d != %d", len(data), len(SystemSettingKeys))
	}
	for _, key := range SystemSettingKeys {
		if _, ok := data[key]; !ok {
			t.Fatalf("missing key: %s", key)
		}
	}
	if data["gatewayTextRawBodyLimitMegabytes"] != float64(16) {
		t.Fatalf("gatewayTextRawBodyLimitMegabytes: %v", data["gatewayTextRawBodyLimitMegabytes"])
	}
	if data["usageStatsTimezone"] != "UTC" {
		t.Fatalf("usageStatsTimezone: %v", data["usageStatsTimezone"])
	}
	if data["userAiAccountLimit"] != float64(100) {
		t.Fatalf("userAiAccountLimit: %v", data["userAiAccountLimit"])
	}
}

// TestSettingsPublicSubsetWithoutLogin covers the pre-auth brand endpoint:
// exactly the global appName/appIcon pair.
func TestSettingsPublicSubsetWithoutLogin(t *testing.T) {
	env := newTestEnv(t)
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/public", "")
	if code != http.StatusOK {
		t.Fatalf("public get: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if len(data) != 2 {
		t.Fatalf("public subset size: %d (%v)", len(data), data)
	}
	if data["appName"] != "聚合 AI" || data["appIcon"] != "/__aisys__/brand-icon.svg" {
		t.Fatalf("public subset values: %v", data)
	}
}

// TestSettingsPatchValidationContract mirrors the strict-key and per-key
// value validation of normalizeSystemSettingsInput.
func TestSettingsPatchValidationContract(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"empty", `{}`, "系统设置更新不能为空"},
		{"unknown key", `{"gatewayFooBar":1}`, "未知系统设置字段：gatewayFooBar"},
		{"string number", `{"gatewayTextRawBodyLimitMegabytes":"12"}`, "gatewayTextRawBodyLimitMegabytes 必须是整数"},
		{"fraction", `{"gatewayTextRawBodyLimitMegabytes":1.5}`, "gatewayTextRawBodyLimitMegabytes 必须是整数"},
		{"bool", `{"gatewayTextRawBodyLimitMegabytes":true}`, "gatewayTextRawBodyLimitMegabytes 必须是整数"},
		{"below min", `{"gatewayTextRawBodyLimitMegabytes":0}`, "gatewayTextRawBodyLimitMegabytes 必须在 1 到 64 之间"},
		{"above max", `{"gatewayTextRawBodyLimitMegabytes":65}`, "gatewayTextRawBodyLimitMegabytes 必须在 1 到 64 之间"},
		{"empty timezone", `{"usageStatsTimezone":""}`, "usageStatsTimezone 无效：统计时区必须是非空字符串"},
		{"unknown timezone", `{"usageStatsTimezone":"Mars/Phobos"}`, "usageStatsTimezone 无效：统计时区不存在：Mars/Phobos"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings", testCase.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", testCase.name, code, payload)
		}
		if got := message(t, payload); got != testCase.message {
			t.Fatalf("%s: message %q != %q", testCase.name, got, testCase.message)
		}
	}
	if got := env.queryCell(t, `SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key='gatewayTextRawBodyLimitMegabytes'`); got != "16" {
		t.Fatalf("rejected patch must not write: %v", got)
	}
	if len(env.sink.snapshot()) != 0 {
		t.Fatal("rejected patch must not append operation log")
	}
}

// TestSettingsPatchUpdatesSnapshotInvalidationAndLog covers the happy path:
// full-snapshot response, persisted rows, runtime invalidation event and the
// settings.update operation log (empty changes on no-op writes).
func TestSettingsPatchUpdatesSnapshotInvalidationAndLog(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	body := `{"gatewayTextRawBodyLimitMegabytes":32,"textStreamIdleTimeoutSeconds":30}`
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings", body)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if len(data) != len(SystemSettingKeys) {
		t.Fatalf("patch response must be the full snapshot: %d", len(data))
	}
	if data["gatewayTextRawBodyLimitMegabytes"] != float64(32) {
		t.Fatalf("updated value: %v", data["gatewayTextRawBodyLimitMegabytes"])
	}
	if data["textStreamIdleTimeoutSeconds"] != float64(30) {
		t.Fatalf("untouched value: %v", data["textStreamIdleTimeoutSeconds"])
	}
	if got := env.queryCell(t, `SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key='gatewayTextRawBodyLimitMegabytes'`); got != "32" {
		t.Fatalf("stored value_json: %v", got)
	}
	if !env.inval.has(TopicGatewayRuntime, settingsUpdatedReason) {
		t.Fatal("missing gateway runtime invalidation event")
	}

	// The update clears the snapshot cache: a follow-up GET reads the new
	// value from the database.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings", "")
	if code != http.StatusOK || dataMap(t, payload)["gatewayTextRawBodyLimitMegabytes"] != float64(32) {
		t.Fatalf("get after patch: %d %v", code, payload)
	}

	entries := env.sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("operation log entries: %d", len(entries))
	}
	entry := entries[0]
	if entry.Module != "settings" || entry.Action != "update_settings" || entry.OperationKey != "settings.update" {
		t.Fatalf("log identity: %+v", entry)
	}
	if entry.ResourceType != "system_settings" || entry.ResourceID != "system" ||
		entry.ResourceName != "系统运行设置" || entry.Summary != "更新系统运行设置" || entry.Mode != "admin" {
		t.Fatalf("log resource: %+v", entry)
	}
	if len(entry.Changes) != 1 {
		t.Fatalf("changes: %+v", entry.Changes)
	}
	change := entry.Changes[0]
	if change.Field != "gatewayTextRawBodyLimitMegabytes" || change.Label != "gatewayTextRawBodyLimitMegabytes" ||
		change.Before != "16" || change.After != "32" {
		t.Fatalf("change payload: %+v", change)
	}

	// A no-op write still succeeds, still logs, but reports no changes.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", body)
	if code != http.StatusOK {
		t.Fatalf("no-op patch: %d %v", code, payload)
	}
	entries = env.sink.snapshot()
	if len(entries) != 2 {
		t.Fatalf("no-op patch must still log: %d", len(entries))
	}
	if len(entries[1].Changes) != 0 {
		t.Fatalf("no-op changes: %+v", entries[1].Changes)
	}
}

// TestSettingsPatchUsageStatsTimezoneGuard mirrors
// assertUsageStatsTimezoneUpdateAllowed in SQLite mode: no stats data allows
// the change, existing stats data refuses it, same-value writes pass.
func TestSettingsPatchUsageStatsTimezoneGuard(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"usageStatsTimezone":"Asia/Shanghai"}`)
	if code != http.StatusOK {
		t.Fatalf("timezone change without stats data: %d %v", code, payload)
	}
	if dataMap(t, payload)["usageStatsTimezone"] != "Asia/Shanghai" {
		t.Fatalf("timezone response: %v", dataMap(t, payload)["usageStatsTimezone"])
	}
	if got := env.queryCell(t, `SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key='usageStatsTimezone'`); got != `"Asia/Shanghai"` {
		t.Fatalf("stored timezone: %v", got)
	}

	env.exec(t, `INSERT INTO usage_stats_totals (probe) VALUES ('x')`)

	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"usageStatsTimezone":"Europe/Paris"}`)
	if code != http.StatusBadRequest || message(t, payload) != "已有统计数据后不能直接修改统计时区，请先备份并重建统计缓存" {
		t.Fatalf("timezone change with stats data: %d %v", code, payload)
	}
	// The same timezone stays writable even with stats data present.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"usageStatsTimezone":"Asia/Shanghai"}`)
	if code != http.StatusOK {
		t.Fatalf("same timezone with stats data: %d %v", code, payload)
	}
	// Validation runs before the guard, so an invalid zone still reports the
	// validation message.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"usageStatsTimezone":"Mars/Phobos"}`)
	if code != http.StatusBadRequest || message(t, payload) != "usageStatsTimezone 无效：统计时区不存在：Mars/Phobos" {
		t.Fatalf("invalid timezone with stats data: %d %v", code, payload)
	}
}

// TestSettingsPostgresModeForbidsTimezoneOnlineChange mirrors the PG branch
// of the guard: any online timezone change is refused before touching SQL.
func TestSettingsPostgresModeForbidsTimezoneOnlineChange(t *testing.T) {
	db, err := sql.Open("sqlite", "file:settings-pg-guard-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), map[string]any{"usageStatsTimezone": "UTC"})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if validation.Message != "PostgreSQL 模式下暂不支持在线修改统计时区，请停机后通过离线迁移 / 重建流程调整" {
		t.Fatalf("pg guard message: %q", validation.Message)
	}
}

// TestSettingsSnapshotProviderFillsCompatibleDefaults covers the
// SettingsProvider contract and applyCompatibleSystemSettingDefaults on a
// legacy database missing one of the compatible rows.
func TestSettingsSnapshotProviderFillsCompatibleDefaults(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, `DELETE FROM system_settings WHERE system_account_id='sys_admin' AND key='userAiAccountLimit'`)

	store, err := NewStore(env.db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.SettingsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != len(SystemSettingKeys) {
		t.Fatalf("snapshot size: %d != %d", len(snapshot), len(SystemSettingKeys))
	}
	if snapshot["userAiAccountLimit"] != float64(100) {
		t.Fatalf("compatible default: %v", snapshot["userAiAccountLimit"])
	}
	var provider SettingsProvider = store
	if _, err := provider.SettingsSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
}
