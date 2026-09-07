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

// TestSettingsPatchUsageStatsTimezoneSixDatabaseSplit is the X05 regression
// for the six-database SQLite split: the usage_stats_* projection tables live
// in the dedicated stats database, so the usageStatsDataExists probe must run
// against the stats handle — probing the business handle 500s every online
// timezone change ("no such table").
func TestSettingsPatchUsageStatsTimezoneSixDatabaseSplit(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Dedicated stats database: same tables the Node getStatsDatabase()
	// carries, empty (fresh stats store). The business database keeps none of
	// them — the exact fresh six-database shape.
	statsDB, err := sql.Open("sqlite", "file:settings-stats-split-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	statsDB.SetMaxOpenConns(1)
	t.Cleanup(func() { statsDB.Close() })
	for _, table := range usageStatsDataTables {
		if _, err := statsDB.Exec("CREATE TABLE " + table + " (probe TEXT)"); err != nil {
			t.Fatal(err)
		}
	}

	store, err := NewStore(env.db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.SetStatsDatabase(statsDB)

	// Empty stats database: the online timezone change must succeed and
	// persist through the business handle.
	updated, err := store.Update(context.Background(), map[string]any{"usageStatsTimezone": "Asia/Shanghai"})
	if err != nil {
		t.Fatalf("timezone change over six-database split: %v", err)
	}
	if updated["usageStatsTimezone"] != "Asia/Shanghai" {
		t.Fatalf("updated timezone: %v", updated["usageStatsTimezone"])
	}

	// Stats data present in the stats database: the guard must refuse.
	if _, err := statsDB.Exec(`INSERT INTO usage_stats_totals (probe) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), map[string]any{"usageStatsTimezone": "Europe/Paris"})
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if validation.Message != "已有统计数据后不能直接修改统计时区，请先备份并重建统计缓存" {
		t.Fatalf("guard message: %q", validation.Message)
	}
}

// TestSettingsGlobalAndSectionsRequireAdmin covers the requireAdmin gate of
// the four new endpoints (settings.routes.ts lines 16/24/55/68): anonymous
// callers get 401, non-admin sessions 403.
func TestSettingsGlobalAndSectionsRequireAdmin(t *testing.T) {
	env := newTestEnv(t)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/__aisys__/api/settings/global"},
		{http.MethodPatch, "/__aisys__/api/settings/global"},
		{http.MethodGet, "/__aisys__/api/settings/sections/brand"},
		{http.MethodPatch, "/__aisys__/api/settings/sections/brand"},
	}
	for _, route := range routes {
		code, payload := env.do(t, route.method, route.path, "")
		if code != http.StatusUnauthorized || message(t, payload) != "请先登录" {
			t.Fatalf("anonymous %s %s: %d %v", route.method, route.path, code, payload)
		}
	}
	env.login(t, "worker", "worker-pass", "user")
	for _, route := range routes {
		code, payload := env.do(t, route.method, route.path, `{}`)
		if code != http.StatusForbidden || message(t, payload) != "需要管理员权限" {
			t.Fatalf("user %s %s: %d %v", route.method, route.path, code, payload)
		}
	}
}

// TestSettingsSectionsGetCoversCatalogKeys covers GET /settings/sections/:key
// for all seven catalog sections: the {sectionKey, values} projection, the
// exact per-section key sets and seeded values across both domains.
func TestSettingsSectionsGetCoversCatalogKeys(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	for _, sectionKey := range ManagementSettingsSectionKeys {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/"+sectionKey, "")
		if code != http.StatusOK {
			t.Fatalf("get section %s: %d %v", sectionKey, code, payload)
		}
		data := dataMap(t, payload)
		if data["sectionKey"] != sectionKey {
			t.Fatalf("section %s echo: %v", sectionKey, data["sectionKey"])
		}
		values, ok := data["values"].(map[string]any)
		if !ok {
			t.Fatalf("section %s values shape: %v", sectionKey, data)
		}
		want := ManagementSettingsSectionCatalog[sectionKey].Keys
		if len(values) != len(want) {
			t.Fatalf("section %s key set: %d != %d (%v)", sectionKey, len(values), len(want), values)
		}
		for _, key := range want {
			if _, ok := values[key]; !ok {
				t.Fatalf("section %s missing key %s", sectionKey, key)
			}
		}
	}

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/gateway-core", "")
	if code != http.StatusOK {
		t.Fatalf("get gateway-core: %d %v", code, payload)
	}
	values := dataMap(t, payload)["values"].(map[string]any)
	if values["gatewayTextRawBodyLimitMegabytes"] != float64(16) || values["noAvailableAccountWaitTimeoutSeconds"] != float64(270) {
		t.Fatalf("gateway-core values: %v", values)
	}
	_, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/brand", "")
	values = dataMap(t, payload)["values"].(map[string]any)
	if values["appName"] != "聚合 AI" || values["appIcon"] != "/__aisys__/brand-icon.svg" {
		t.Fatalf("brand values: %v", values)
	}
}

// TestSettingsSectionCompatibleDefaults covers the section-level default
// merge: a legacy system_settings row missing userAiAccountLimit reads back
// as the compatible default inside user-request-limit.
func TestSettingsSectionCompatibleDefaults(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, `DELETE FROM system_settings WHERE system_account_id='sys_admin' AND key='userAiAccountLimit'`)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/user-request-limit", "")
	if code != http.StatusOK {
		t.Fatalf("get section: %d %v", code, payload)
	}
	values := dataMap(t, payload)["values"].(map[string]any)
	if len(values) != 5 {
		t.Fatalf("section key set: %d (%v)", len(values), values)
	}
	if values["userAiAccountLimit"] != float64(100) {
		t.Fatalf("compatible default: %v", values["userAiAccountLimit"])
	}
}

// TestSettingsSectionsUnknownKey mirrors parseSectionKey: both GET and PATCH
// render the verbatim 未知设置分区 message as 400 without logging.
func TestSettingsSectionsUnknownKey(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/nope", "")
	if code != http.StatusBadRequest || message(t, payload) != "未知设置分区：nope" {
		t.Fatalf("unknown get: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/nope", `{"appName":"x"}`)
	if code != http.StatusBadRequest || message(t, payload) != "未知设置分区：nope" {
		t.Fatalf("unknown patch: %d %v", code, payload)
	}
	if len(env.sink.snapshot()) != 0 {
		t.Fatal("unknown section must not append operation log")
	}
}

// TestSettingsSectionPatchValidation mirrors the PATCH /sections/:key
// rejections: empty updates, whitelist and prototype-polluting keys,
// non-object bodies and per-key value errors all render 400 with the
// verbatim message, without writing or logging.
func TestSettingsSectionPatchValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name    string
		section string
		body    string
		message string
	}{
		{"empty", "brand", `{}`, "设置更新不能为空"},
		{"unknown field", "brand", `{"appTagline":"x"}`, "brand 包含不允许的字段"},
		{"cross-section field", "brand", `{"gatewayTextRawBodyLimitMegabytes":16}`, "brand 包含不允许的字段"},
		{"proto key", "brand", `{"__proto__":"x"}`, "brand 包含不允许的字段"},
		{"array body", "brand", `["appName"]`, "设置分区更新必须是普通 JSON 对象"},
		{"null body", "gateway-core", `null`, "设置分区更新必须是普通 JSON 对象"},
		{"string body", "cooldown-retest", `"hours"`, "设置分区更新必须是普通 JSON 对象"},
		{"string number", "gateway-core", `{"gatewayTextRawBodyLimitMegabytes":"12"}`, "gatewayTextRawBodyLimitMegabytes 必须是整数"},
		{"out of range", "cooldown-retest", `{"cooldownAccountRetestMaxBackoffHours":721}`, "cooldownAccountRetestMaxBackoffHours 必须在 1 到 720 之间"},
		{"empty brand string", "brand", `{"appName":"   "}`, "appName 必须是非空字符串"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/"+testCase.section, testCase.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", testCase.name, code, payload)
		}
		if got := message(t, payload); got != testCase.message {
			t.Fatalf("%s: message %q != %q", testCase.name, got, testCase.message)
		}
	}
	if len(env.sink.snapshot()) != 0 {
		t.Fatal("rejected patch must not append operation log")
	}
	if got := env.queryCell(t, `SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key='gatewayTextRawBodyLimitMegabytes'`); got != "16" {
		t.Fatalf("rejected patch must not write: %v", got)
	}
	if got := env.queryCell(t, `SELECT value_json FROM global_settings WHERE key='appName'`); got != `"聚合 AI"` {
		t.Fatalf("rejected brand patch must not write: %v", got)
	}
}

// TestSettingsSectionPatchHappyPath covers a system section write: the
// {sectionKey, values} response projection, persisted row, gateway runtime
// invalidation, follow-up read and the settings.update operation log with
// resourceId=sectionKey.
func TestSettingsSectionPatchHappyPath(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/cooldown-retest",
		`{"cooldownAccountRetestMaxBackoffHours":24}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["sectionKey"] != "cooldown-retest" {
		t.Fatalf("sectionKey echo: %v", data["sectionKey"])
	}
	values := data["values"].(map[string]any)
	if len(values) != 1 || values["cooldownAccountRetestMaxBackoffHours"] != float64(24) {
		t.Fatalf("section values: %v", values)
	}
	if got := env.queryCell(t, `SELECT value_json FROM system_settings WHERE system_account_id='sys_admin' AND key='cooldownAccountRetestMaxBackoffHours'`); got != "24" {
		t.Fatalf("stored value_json: %v", got)
	}
	if !env.inval.has(TopicGatewayRuntime, settingsUpdatedReason) {
		t.Fatal("missing gateway runtime invalidation event")
	}

	// The section projection reads the persisted value back.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/cooldown-retest", "")
	if code != http.StatusOK || dataMap(t, payload)["values"].(map[string]any)["cooldownAccountRetestMaxBackoffHours"] != float64(24) {
		t.Fatalf("get after patch: %d %v", code, payload)
	}

	entries := env.sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("operation log entries: %d", len(entries))
	}
	entry := entries[0]
	if entry.Module != "settings" || entry.Action != "update_settings" || entry.OperationKey != "settings.update" || entry.Mode != "admin" {
		t.Fatalf("log identity: %+v", entry)
	}
	if entry.ResourceType != "system_settings" || entry.ResourceID != "cooldown-retest" ||
		entry.ResourceName != "设置分区 cooldown-retest" || entry.Summary != "更新设置分区 cooldown-retest" {
		t.Fatalf("log resource: %+v", entry)
	}
	if entry.VisibilityScope != "all_users" || entry.DetailLevel != "summary" {
		t.Fatalf("log visibility: %+v", entry)
	}
	if len(entry.Changes) != 1 {
		t.Fatalf("changes: %+v", entry.Changes)
	}
	change := entry.Changes[0]
	if change.Field != "cooldownAccountRetestMaxBackoffHours" || change.Label != "cooldownAccountRetestMaxBackoffHours" ||
		change.Before != "12" || change.After != "24" {
		t.Fatalf("change payload: %+v", change)
	}
}

// TestSettingsBrandSectionUpdatesGlobalAndPublic covers the brand slice: the
// global-domain write persists into global_settings, refreshes the global
// cache without a gateway runtime invalidation, logs as update_global with
// resourceId=brand, and GET /settings/public immediately sees the new brand.
func TestSettingsBrandSectionUpdatesGlobalAndPublic(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/brand",
		`{"appName":"新品牌","appIcon":"/__aisys__/new-icon.svg"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["sectionKey"] != "brand" {
		t.Fatalf("sectionKey echo: %v", data["sectionKey"])
	}
	values := data["values"].(map[string]any)
	if values["appName"] != "新品牌" || values["appIcon"] != "/__aisys__/new-icon.svg" {
		t.Fatalf("brand values: %v", values)
	}
	if got := env.queryCell(t, `SELECT value_json FROM global_settings WHERE key='appName'`); got != `"新品牌"` {
		t.Fatalf("stored appName: %v", got)
	}
	if env.inval.has(TopicGatewayRuntime, settingsUpdatedReason) {
		t.Fatal("global-domain section writes must not fire gateway runtime invalidation")
	}

	// The pre-auth public surface reflects the new brand immediately.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/public", "")
	publicData := dataMap(t, payload)
	if publicData["appName"] != "新品牌" || publicData["appIcon"] != "/__aisys__/new-icon.svg" {
		t.Fatalf("public after brand patch: %v", publicData)
	}

	entries := env.sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("operation log entries: %d", len(entries))
	}
	entry := entries[0]
	if entry.Action != "update_global" || entry.OperationKey != "settings.update_global" ||
		entry.ResourceType != "global_settings" || entry.ResourceID != "brand" ||
		entry.ResourceName != "设置分区 brand" || entry.Summary != "更新设置分区 brand" {
		t.Fatalf("log identity: %+v", entry)
	}
	if len(entry.Changes) != 2 {
		t.Fatalf("changes: %+v", entry.Changes)
	}
	// Change order follows the deterministic sorted field order (appIcon
	// sorts before appName).
	if entry.Changes[0].Field != "appIcon" || entry.Changes[0].Label != "appIcon" ||
		entry.Changes[0].Before != "/__aisys__/brand-icon.svg" || entry.Changes[0].After != "/__aisys__/new-icon.svg" {
		t.Fatalf("appIcon change: %+v", entry.Changes[0])
	}
	if entry.Changes[1].Field != "appName" || entry.Changes[1].Label != "appName" ||
		entry.Changes[1].Before != "聚合 AI" || entry.Changes[1].After != "新品牌" {
		t.Fatalf("appName change: %+v", entry.Changes[1])
	}
}

// TestSettingsGlobalEndpoints mirrors the /settings/global family: the
// admin-only read returns the brand pair, the patch validates strictly,
// trims strings, keeps untouched keys, logs with the 系统名称/系统图标 labels and
// feeds GET /settings/public.
func TestSettingsGlobalEndpoints(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/global", "")
	if code != http.StatusOK {
		t.Fatalf("global get: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if len(data) != 2 || data["appName"] != "聚合 AI" || data["appIcon"] != "/__aisys__/brand-icon.svg" {
		t.Fatalf("global get values: %v", data)
	}

	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/global", `{"appName":"  品牌X  "}`)
	if code != http.StatusOK {
		t.Fatalf("global patch: %d %v", code, payload)
	}
	data = dataMap(t, payload)
	if len(data) != 2 || data["appName"] != "品牌X" {
		t.Fatalf("global patch response: %v", data)
	}
	if data["appIcon"] != "/__aisys__/brand-icon.svg" {
		t.Fatalf("untouched key must persist: %v", data)
	}
	if got := env.queryCell(t, `SELECT value_json FROM global_settings WHERE key='appName'`); got != `"品牌X"` {
		t.Fatalf("stored appName: %v", got)
	}

	cases := []struct{ name, body, message string }{
		{"empty", `{}`, "全局设置更新不能为空"},
		{"unknown key", `{"appTagline":"x"}`, "未知全局设置字段：appTagline"},
		{"empty string", `{"appName":""}`, "appName 必须是非空字符串"},
		{"number", `{"appIcon":3}`, "appIcon 必须是非空字符串"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/settings/global", testCase.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: status %d %v", testCase.name, code, payload)
		}
		if got := message(t, payload); got != testCase.message {
			t.Fatalf("%s: message %q != %q", testCase.name, got, testCase.message)
		}
	}
	if len(env.sink.snapshot()) != 1 {
		t.Fatalf("rejected global patches must not log: %d", len(env.sink.snapshot()))
	}

	// The no-op write still logs but reports no changes (diffSafeFields).
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/global", `{"appName":"品牌X"}`)
	if code != http.StatusOK {
		t.Fatalf("no-op global patch: %d %v", code, payload)
	}

	// The public pre-auth surface reflects the global write immediately.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/public", "")
	if dataMap(t, payload)["appName"] != "品牌X" {
		t.Fatalf("public after global patch: %v", dataMap(t, payload))
	}

	entries := env.sink.snapshot()
	if len(entries) != 2 {
		t.Fatalf("operation log entries: %d", len(entries))
	}
	entry := entries[0]
	if entry.Action != "update_global" || entry.OperationKey != "settings.update_global" ||
		entry.ResourceType != "global_settings" || entry.ResourceID != "global" ||
		entry.ResourceName != "全局品牌设置" || entry.Summary != "更新全局品牌设置" {
		t.Fatalf("log identity: %+v", entry)
	}
	if len(entry.Changes) != 1 {
		t.Fatalf("changes: %+v", entry.Changes)
	}
	if entry.Changes[0].Field != "appName" || entry.Changes[0].Label != "系统名称" ||
		entry.Changes[0].Before != "聚合 AI" || entry.Changes[0].After != "品牌X" {
		t.Fatalf("appName change: %+v", entry.Changes[0])
	}
	if len(entries[1].Changes) != 0 {
		t.Fatalf("no-op changes: %+v", entries[1].Changes)
	}
}
