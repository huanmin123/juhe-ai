package accounthealth

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

	_ "modernc.org/sqlite"
)

// direct_input_reader_sqlite_test.go 覆盖 SQLiteDirectInputReader 的候选语义
// 与 sqlite store 闭环：全部场景只用 t.TempDir() 下的本地 SQLite 文件，不依赖
// 外部 PG。业务库最小 schema 是「直读候选列 ∪ 投影写列」的并集（与包内
// projectionBusinessSchema 的列集对齐），统计库只含 CheckContract 与配额花费
// 用到的 5 张表。时间文本一律固定毫秒 UTC（与 gateway 写侧同格式）。

var sqliteDirectFixtureNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

const sqliteDirectInputTestBusinessSchema = `
CREATE TABLE accounts (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
  provider_code TEXT NOT NULL DEFAULT 'openai',
  provider_protocol_profile_id TEXT NOT NULL DEFAULT 'profile_openai_openai_v1',
  protocol_code TEXT NOT NULL DEFAULT 'openai',
  protocol_version TEXT NOT NULL DEFAULT 'v1',
  type TEXT NOT NULL DEFAULT 'api_key',
  client_compatibility TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending_test',
  schedulable INTEGER NOT NULL DEFAULT 1,
  credentials_encrypted TEXT NOT NULL DEFAULT '',
  proxy_profile_id TEXT,
  health_check_model TEXT NOT NULL DEFAULT 'gpt-4o-mini',
  health_check_endpoint_mode TEXT NOT NULL DEFAULT 'chat_json',
  account_expires_at TEXT,
  cooldown_until TEXT,
  temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 0,
  cooldown_retest_observation_started_at TEXT,
  cooldown_retest_generation TEXT,
  cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
  cooldown_retest_last_at TEXT,
  cooldown_retest_last_status_code INTEGER,
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_trace_id TEXT,
  authorization_instance_source_account_id TEXT,
  authorization_instance_authorization_id TEXT,
  deleted_at TEXT,
  next_health_check_at TEXT,
  last_health_check_at TEXT,
  last_health_success_at TEXT,
  last_health_check_status_code INTEGER,
  last_health_check_error_code TEXT,
  last_health_check_error_message TEXT,
  last_health_check_trace_id TEXT,
  health_check_failure_count INTEGER NOT NULL DEFAULT 0,
  health_check_failure_started_at TEXT,
  config_revision INTEGER NOT NULL DEFAULT 5,
  dispatch_revision INTEGER NOT NULL DEFAULT 7,
  balance_query_enabled INTEGER NOT NULL DEFAULT 0,
  balance_query_config_json TEXT NOT NULL DEFAULT '{}',
  balance_query_next_refresh_at TEXT,
  availability_schedule_json TEXT,
  availability_schedule_next_check_at TEXT,
  created_at TEXT NOT NULL DEFAULT '2026-09-01T00:00:00.000Z',
  updated_at TEXT NOT NULL DEFAULT '2026-09-01T00:00:00.000Z'
);
CREATE TABLE account_health_jobs_input_versions (
  account_id TEXT NOT NULL,
  input_version INTEGER NOT NULL,
  current_version INTEGER NOT NULL,
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account_id, input_version)
);
CREATE TABLE group_accounts (
  system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
  group_id TEXT NOT NULL DEFAULT 'wsql-group',
  account_id TEXT NOT NULL,
  account_authorization_id TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT '2026-09-01T00:00:00.000Z',
  PRIMARY KEY (group_id, account_id)
);
CREATE TABLE group_account_stats_dirty (
  group_id TEXT PRIMARY KEY,
  reason TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE resource_authorizations (
  id TEXT PRIMARY KEY,
  resource_type TEXT NOT NULL DEFAULT 'account',
  resource_id TEXT NOT NULL DEFAULT '',
  resource_owner_system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
  grantee_system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
  status TEXT NOT NULL DEFAULT 'active',
  effective_source_team_id TEXT,
  limits_json TEXT,
  expires_at TEXT
);
CREATE TABLE resource_authorization_grants (
  id TEXT PRIMARY KEY,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  resource_owner_system_account_id TEXT NOT NULL,
  grantee_type TEXT NOT NULL,
  grantee_team_id TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  limits_json TEXT,
  expires_at TEXT,
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE proxy_profiles (
  id TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 1,
  type TEXT NOT NULL DEFAULT 'http',
  host TEXT NOT NULL DEFAULT '',
  port INTEGER NOT NULL DEFAULT 0,
  username TEXT,
  password_encrypted TEXT
);
CREATE TABLE account_model_mappings (
  account_id TEXT NOT NULL,
  provider_code TEXT NOT NULL DEFAULT 'openai',
  source_model TEXT NOT NULL,
  source_endpoint_family TEXT NOT NULL,
  upstream_model TEXT NOT NULL,
  upstream_endpoint_family TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT '2026-09-01T00:00:00.000Z',
  PRIMARY KEY (account_id, source_model, source_endpoint_family)
);
CREATE TABLE system_settings (
  system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
  key TEXT NOT NULL,
  value_json TEXT NOT NULL,
  PRIMARY KEY (system_account_id, key)
);
CREATE TABLE account_health_projection_receipts (
  outcome_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  input_version INTEGER NOT NULL CHECK (input_version >= 1),
  disposition TEXT NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
  reason TEXT,
  applied_at TEXT NOT NULL
);
CREATE TABLE account_health_projection_cursors (
  consumer_key TEXT PRIMARY KEY,
  observed_at TEXT,
  outcome_id TEXT,
  updated_at TEXT NOT NULL,
  CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
);
CREATE TABLE account_circuit_outbox (
  event_id TEXT PRIMARY KEY,
  projection_key TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  event_type TEXT NOT NULL,
  account_id TEXT NOT NULL,
  account_runtime_key TEXT NOT NULL,
  circuit_scope_key TEXT,
  incident_id TEXT,
  transition_id TEXT NOT NULL,
  dispatch_revision INTEGER NOT NULL,
  generation INTEGER,
  ledger_revision INTEGER,
  status TEXT NOT NULL DEFAULT 'pending',
  available_at_ms INTEGER NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  created_at_ms INTEGER NOT NULL,
  updated_at_ms INTEGER NOT NULL,
  UNIQUE (projection_key, dedupe_key)
);
`

const sqliteDirectInputTestStatsSchema = `
CREATE TABLE usage_stats_totals (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  total_cost_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (system_account_id, scope_type, scope_id)
);
CREATE TABLE usage_stats_daily (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  stat_date TEXT NOT NULL,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
);
CREATE TABLE usage_stats_weekly (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  stat_week TEXT NOT NULL,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_week)
);
CREATE TABLE usage_stats_monthly (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  stat_month TEXT NOT NULL,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_month)
);
CREATE TABLE usage_quota_hourly_windows (
  system_account_id TEXT NOT NULL,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  window_hours INTEGER NOT NULL,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours)
);
`

type sqliteDirectFixture struct {
	t        *testing.T
	dir      string
	secret   string
	business *sql.DB // 种子写入连接（读断言也走它）
	stats    *sql.DB
}

func newSQLiteDirectFixture(t *testing.T) *sqliteDirectFixture {
	t.Helper()
	dir := t.TempDir()
	business, err := sql.Open("sqlite", filepath.Join(dir, "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = business.Close() })
	if _, err := business.Exec(sqliteDirectInputTestBusinessSchema); err != nil {
		t.Fatalf("初始化业务库 schema: %v", err)
	}
	stats, err := sql.Open("sqlite", filepath.Join(dir, "stats.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stats.Close() })
	if _, err := stats.Exec(sqliteDirectInputTestStatsSchema); err != nil {
		t.Fatalf("初始化统计库 schema: %v", err)
	}
	fixture := &sqliteDirectFixture{t: t, dir: dir, secret: "wsql-direct-credential-secret", business: business, stats: stats}
	fixture.seedSettings(t)
	return fixture
}

// seedSettings 幂等写入六个 J1 调度设置（canonical 值）。
func (f *sqliteDirectFixture) seedSettings(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"accountHealthCheckIntervalHours":      "24",
		"accountHealthCheckJitterMinutes":      "10",
		"accountHealthCheckFailureThreshold":   "3",
		"defaultTemporaryUnschedulableMinutes": "60",
		"cooldownAccountRetestMaxBackoffHours": "24",
		"usageStatsTimezone":                   "\"Asia/Shanghai\"",
	} {
		if _, err := f.business.Exec(`INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', ?, ?)
			ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = excluded.value_json`, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func sqliteDirectFixtureTimeText(offset time.Duration) string {
	return sqliteDirectTimestamp(sqliteDirectFixtureNow.Add(offset))
}

// sqliteDirectCandidateSeed 是候选种子的默认值 + patch 模型：patch 直接改字段
// 或经 extraColumns 写任意列（nil 表示显式 NULL）。
type sqliteDirectCandidateSeed struct {
	id                string
	status            string
	schedulable       int
	apiKey            string
	baseURL           string
	provider          string
	profile           string
	endpointMode      string
	healthModel       string
	credentials       string // 非空直接覆盖凭据列（构造坏 envelope 用）
	withBinding       bool
	withVersion       bool
	accountExpiresAt  any
	cooldownUntil     any
	nextHealthCheckAt any
	lastHealthCheckAt any
	configRevision    int64
	dispatchRevision  int64
	inputVersion      int64
	extraColumns      map[string]any
}

func newSQLiteDirectCandidateSeed(id string) sqliteDirectCandidateSeed {
	return sqliteDirectCandidateSeed{
		id: id, status: "pending_test", schedulable: 1, apiKey: "sk-" + id,
		baseURL: "http://127.0.0.1:1", provider: "openai", profile: "profile_openai_openai_v1",
		endpointMode: "chat_json", healthModel: "gpt-4o-mini",
		withBinding: true, withVersion: true,
		configRevision: 5, dispatchRevision: 7, inputVersion: 1,
		extraColumns: map[string]any{},
	}
}

func (f *sqliteDirectFixture) seedCandidate(t *testing.T, seed sqliteDirectCandidateSeed) {
	t.Helper()
	credentials := seed.credentials
	if credentials == "" && seed.apiKey != "" {
		envelope, err := EncryptV1Envelope(f.secret, []byte(`{"api_key":`+mustJSON(seed.apiKey)+`,"base_url":`+mustJSON(seed.baseURL)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		credentials = envelope
	}
	columns := map[string]any{
		"id": seed.id, "provider_code": seed.provider, "provider_protocol_profile_id": seed.profile,
		"type": "api_key", "status": seed.status, "schedulable": seed.schedulable,
		"credentials_encrypted": credentials, "health_check_model": seed.healthModel,
		"health_check_endpoint_mode": seed.endpointMode,
		"config_revision":            seed.configRevision, "dispatch_revision": seed.dispatchRevision,
		"created_at": sqliteDirectFixtureTimeText(-120 * time.Hour), "updated_at": sqliteDirectFixtureTimeText(-time.Hour),
	}
	for name, value := range seed.extraColumns {
		columns[name] = value
	}
	if seed.accountExpiresAt != nil {
		columns["account_expires_at"] = seed.accountExpiresAt
	}
	if seed.cooldownUntil != nil {
		columns["cooldown_until"] = seed.cooldownUntil
	}
	if seed.nextHealthCheckAt != nil {
		columns["next_health_check_at"] = seed.nextHealthCheckAt
	}
	if seed.lastHealthCheckAt != nil {
		columns["last_health_check_at"] = seed.lastHealthCheckAt
	}
	names := make([]string, 0, len(columns))
	marks := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for name, value := range columns {
		names = append(names, name)
		marks = append(marks, "?")
		args = append(args, value)
	}
	if _, err := f.business.Exec(`INSERT INTO accounts (`+strings.Join(names, ",")+`) VALUES (`+strings.Join(marks, ",")+")", args...); err != nil {
		t.Fatal(err)
	}
	if seed.withVersion {
		if _, err := f.business.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, input_version, current_version, updated_at) VALUES (?, ?, ?, ?)`,
			seed.id, seed.inputVersion, seed.inputVersion, sqliteDirectFixtureTimeText(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if seed.withBinding {
		if _, err := f.business.Exec(`INSERT INTO group_accounts (account_id, enabled) VALUES (?, 1) ON CONFLICT (group_id, account_id) DO NOTHING`, seed.id); err != nil {
			t.Fatal(err)
		}
	}
}

// readOnlyReader 用与 main.go 相同的只读范式打开两个库并构造 reader。
func (f *sqliteDirectFixture) readOnlyReader(t *testing.T, now func() time.Time) *SQLiteDirectInputReader {
	t.Helper()
	business := openSQLiteReadOnlyTestDB(t, filepath.Join(f.dir, "business.sqlite3"))
	stats := openSQLiteReadOnlyTestDB(t, filepath.Join(f.dir, "stats.sqlite3"))
	reader, err := NewSQLiteDirectInputReader(business, stats, f.secret, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func openSQLiteReadOnlyTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteDirectInputReaderConstructorArms(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	business := openSQLiteReadOnlyTestDB(t, filepath.Join(fixture.dir, "business.sqlite3"))
	stats := openSQLiteReadOnlyTestDB(t, filepath.Join(fixture.dir, "stats.sqlite3"))
	if _, err := NewSQLiteDirectInputReader(nil, stats, "s", time.Hour, nil); err == nil || !strings.Contains(err.Error(), "配置无效") {
		t.Fatalf("nil business db: %v", err)
	}
	if _, err := NewSQLiteDirectInputReader(business, nil, "s", time.Hour, nil); err == nil {
		t.Fatal("nil stats db 必须报错")
	}
	if _, err := NewSQLiteDirectInputReader(business, stats, "  ", time.Hour, nil); err == nil {
		t.Fatal("空 secret 必须报错")
	}
	if _, err := NewSQLiteDirectInputReader(business, stats, "s", 30*time.Second, nil); err == nil {
		t.Fatal("TTL 下界必须报错")
	}
	if _, err := NewSQLiteDirectInputReader(business, stats, "s", 8*24*time.Hour, nil); err == nil {
		t.Fatal("TTL 上界必须报错")
	}
	reader, err := NewSQLiteDirectInputReader(business, stats, "s", time.Hour, nil)
	if err != nil || reader == nil {
		t.Fatalf("合法构造: %v", err)
	}
	// limit 越界臂（与 PG reader 同边界）。
	if _, err := reader.LoadDueWithFailures(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "limit 必须在 1..") {
		t.Fatalf("limit=0: %v", err)
	}
	if _, err := reader.LoadDueWithFailures(context.Background(), maxJ1Capacity+1); err == nil {
		t.Fatal("limit>maxJ1Capacity 必须报错")
	}
}

func TestSQLiteDirectInputReaderPendingTestFirstAndInputShape(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	// active 且到期的对照候选（pending_test 必须排它前面）。
	active := newSQLiteDirectCandidateSeed("wsql-active")
	active.status = "active"
	fixture.seedCandidate(t, active)
	pending := newSQLiteDirectCandidateSeed("wsql-pending")
	fixture.seedCandidate(t, pending)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("LoadDueWithFailures: %v", err)
	}
	if len(result.Inputs) != 2 || result.Inputs[0].AccountID != "wsql-pending" {
		t.Fatalf("pending_test 必须排最前: %+v", result)
	}
	input := result.Inputs[0]
	if input.InputVersion != 1 || input.ConfigRevision != 5 || input.DispatchRevision != 7 {
		t.Fatalf("fence 错误: %+v", input)
	}
	if input.EndpointMode != "chat_json" || input.HealthModel != "gpt-4o-mini" || input.Provider != "openai" {
		t.Fatalf("探活目标错误: %+v", input)
	}
	if input.TLSPolicyVersion != "j1-direct-upstream-v1" {
		t.Fatalf("TLS policy 错误: %+v", input)
	}
	if !input.IssuedAt.Equal(sqliteDirectFixtureNow) || !input.ExpiresAt.Equal(sqliteDirectFixtureNow.Add(time.Hour)) {
		t.Fatalf("有效期错误: issued=%v expires=%v", input.IssuedAt, input.ExpiresAt)
	}
	if input.Eligibility.AccountStatus != "pending_test" || !input.Eligibility.BoundGroup || !input.Eligibility.AuthorizationEligible {
		t.Fatalf("eligibility 错误: %+v", input.Eligibility)
	}
	if input.Schedule.HealthIntervalMS != int64(24*time.Hour/time.Millisecond) || input.Schedule.FailureThreshold != 3 || input.Schedule.MaxPauseMinutes != 60 {
		t.Fatalf("settings 调度快照错误: %+v", input.Schedule)
	}
	if len(input.APIKeys) != 1 || input.APIKeys[0].Credential.Kind != "api_key" {
		t.Fatalf("api key 输入错误: %+v", input.APIKeys)
	}
	plaintext, err := DecryptV1Envelope(fixture.secret, input.APIKeys[0].Credential.Ciphertext)
	if err != nil {
		t.Fatalf("解封凭据: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(plaintext, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["api_key"] != "sk-wsql-pending" {
		t.Fatalf("api key 解密错误: %s", plaintext)
	}
	if input.BaseURL != "http://127.0.0.1:1" {
		t.Fatalf("base_url 错误: %s", input.BaseURL)
	}
}

func TestSQLiteDirectInputReaderFilterArms(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	// 合法对照候选（active 且到期）。
	due := newSQLiteDirectCandidateSeed("wsql-due")
	due.status = "active"
	due.nextHealthCheckAt = nil
	fixture.seedCandidate(t, due)
	// 无分组绑定。
	noBinding := newSQLiteDirectCandidateSeed("wsql-nobind")
	noBinding.withBinding = false
	fixture.seedCandidate(t, noBinding)
	// 已到期。
	expired := newSQLiteDirectCandidateSeed("wsql-expired")
	expired.accountExpiresAt = sqliteDirectFixtureTimeText(-time.Hour)
	fixture.seedCandidate(t, expired)
	// 冷却未到。
	cooling := newSQLiteDirectCandidateSeed("wsql-cooling")
	cooling.cooldownUntil = sqliteDirectFixtureTimeText(time.Hour)
	fixture.seedCandidate(t, cooling)
	// active 但 schedulable=0。
	unschedulable := newSQLiteDirectCandidateSeed("wsql-unsched")
	unschedulable.status = "active"
	unschedulable.schedulable = 0
	fixture.seedCandidate(t, unschedulable)
	// active 但未到期（LoadDue 过滤、LoadAccount 放行）。
	notDue := newSQLiteDirectCandidateSeed("wsql-notdue")
	notDue.status = "active"
	notDue.nextHealthCheckAt = sqliteDirectFixtureTimeText(time.Hour)
	fixture.seedCandidate(t, notDue)

	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("LoadDueWithFailures: %v", err)
	}
	ids := map[string]bool{}
	for _, input := range result.Inputs {
		ids[input.AccountID] = true
	}
	for _, blocked := range []string{"wsql-nobind", "wsql-expired", "wsql-cooling", "wsql-unsched", "wsql-notdue"} {
		if ids[blocked] {
			t.Fatalf("%s 必须被过滤: %v", blocked, ids)
		}
	}
	if !ids["wsql-due"] {
		t.Fatalf("到期对照候选必须返回: %v", ids)
	}
	// ignoreSchedule=true 跳过到期谓词（LoadAccount 语义），其余资格守卫保留。
	explicit, err := reader.LoadAccountWithFailures(context.Background(), "wsql-notdue")
	if err != nil {
		t.Fatalf("LoadAccountWithFailures: %v", err)
	}
	if len(explicit.Inputs) != 1 || explicit.Inputs[0].AccountID != "wsql-notdue" {
		t.Fatalf("显式加载必须绕过到期谓词: %+v", explicit)
	}
	// 但到期/冷却守卫在 LoadAccount 下仍然生效（SQL 谓词不受 ignoreSchedule 影响）。
	stillBlocked, err := reader.LoadAccountWithFailures(context.Background(), "wsql-expired")
	if err != nil || len(stillBlocked.Inputs) != 0 {
		t.Fatalf("显式加载不得绕过到期守卫: %+v %v", stillBlocked, err)
	}
}

func TestSQLiteDirectInputReaderLoadAccountArms(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	fixture.seedCandidate(t, newSQLiteDirectCandidateSeed("wsql-only"))
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	if _, err := reader.LoadAccount(context.Background(), "   "); err == nil || !strings.Contains(err.Error(), "account ID 不能为空") {
		t.Fatalf("空白 ID: %v", err)
	}
	inputs, err := reader.LoadAccount(context.Background(), "wsql-only")
	if err != nil || len(inputs) != 1 || inputs[0].AccountID != "wsql-only" {
		t.Fatalf("按 ID 精确返回: %+v %v", inputs, err)
	}
	missing, err := reader.LoadAccount(context.Background(), "wsql-ghost")
	if err != nil || len(missing) != 0 {
		t.Fatalf("不存在必须返回空: %+v %v", missing, err)
	}
}

func TestSQLiteDirectInputReaderSuppressionFiltersBeforeLimit(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	// 两个 pending_test：稳定序 created_at 相同时按 id ASC，wsql-supp-a 在前。
	first := newSQLiteDirectCandidateSeed("wsql-supp-a")
	second := newSQLiteDirectCandidateSeed("wsql-supp-b")
	fixture.seedCandidate(t, first)
	fixture.seedCandidate(t, second)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return []DirectInputSuppression{{AccountID: "wsql-supp-a", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, NextDueAt: sqliteDirectFixtureNow.Add(time.Hour)}}, nil
	})
	suppressed, err := reader.LoadDueWithFailures(context.Background(), 1)
	if err != nil {
		t.Fatalf("抑制加载: %v", err)
	}
	if len(suppressed.Inputs) != 1 || suppressed.Inputs[0].AccountID != "wsql-supp-b" {
		t.Fatalf("被抑制候选不得占用 limit 窗口: %+v", suppressed)
	}
	// fence 不匹配（config_revision 漂移）→ 不抑制。
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return []DirectInputSuppression{{AccountID: "wsql-supp-a", InputVersion: 1, ConfigRevision: 4, DispatchRevision: 7, NextDueAt: sqliteDirectFixtureNow.Add(time.Hour)}}, nil
	})
	stale, err := reader.LoadDueWithFailures(context.Background(), 1)
	if err != nil || len(stale.Inputs) != 1 || stale.Inputs[0].AccountID != "wsql-supp-a" {
		t.Fatalf("漂移 fence 不得抑制: %+v %v", stale, err)
	}
	// 重试窗口已过 → 不抑制。
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return []DirectInputSuppression{{AccountID: "wsql-supp-a", InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, NextDueAt: sqliteDirectFixtureNow.Add(-time.Minute)}}, nil
	})
	expired, err := reader.LoadDueWithFailures(context.Background(), 1)
	if err != nil || len(expired.Inputs) != 1 || expired.Inputs[0].AccountID != "wsql-supp-a" {
		t.Fatalf("过期抑制必须失效: %+v %v", expired, err)
	}
	// 抑制快照失败 → 整读失败。
	reader.SetSuppressionProvider(func(ctx context.Context, now time.Time) ([]DirectInputSuppression, error) {
		return nil, context.DeadlineExceeded
	})
	if _, err := reader.LoadDueWithFailures(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "抑制快照失败") {
		t.Fatalf("抑制快照失败臂: %v", err)
	}
}

func TestSQLiteDirectInputReaderMalformedCandidateIsolated(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	fixture.seedCandidate(t, newSQLiteDirectCandidateSeed("wsql-good"))
	bad := newSQLiteDirectCandidateSeed("wsql-bad")
	bad.credentials = "not-an-envelope"
	fixture.seedCandidate(t, bad)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("坏候选必须被隔离而非整读失败: %v", err)
	}
	if len(result.Inputs) != 1 || result.Inputs[0].AccountID != "wsql-good" {
		t.Fatalf("合法候选必须返回: %+v", result)
	}
	if len(result.Failures) != 1 || result.Failures[0].AccountID != "wsql-bad" || result.Failures[0].InputVersion != 1 || result.Failures[0].ConfigRevision != 5 || result.Failures[0].DispatchRevision != 7 {
		t.Fatalf("隔离 failure 必须携带完整 fence: %+v", result.Failures)
	}
	// LoadDue 面对隔离 failure 必须显式报错引导调用方换用 WithFailures。
	if _, err := reader.LoadDue(context.Background(), 10); err == nil || !strings.Contains(err.Error(), "LoadDueWithFailures") {
		t.Fatalf("LoadDue 隔离失败臂: %v", err)
	}
}

func TestSQLiteDirectInputReaderCheckContractArms(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	if err := reader.CheckContract(context.Background()); err != nil {
		t.Fatalf("完整 fixture 契约必须通过: %v", err)
	}
	// stats 库缺表 → 统计契约失败。
	bareStatsPath := filepath.Join(t.TempDir(), "bare-stats.sqlite3")
	bareStats, err := sql.Open("sqlite", bareStatsPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bareStats.Close() })
	if _, err := bareStats.Exec(`CREATE TABLE placeholder (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	broken, err := NewSQLiteDirectInputReader(openSQLiteReadOnlyTestDB(t, filepath.Join(fixture.dir, "business.sqlite3")), openSQLiteReadOnlyTestDB(t, bareStatsPath), fixture.secret, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "统计只读契约") {
		t.Fatalf("缺统计表必须契约失败: %v", err)
	}
	// business 库缺表 → 业务契约失败。
	bareBusinessPath := filepath.Join(t.TempDir(), "bare-business.sqlite3")
	bareBusiness, err := sql.Open("sqlite", bareBusinessPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bareBusiness.Close() })
	if _, err := bareBusiness.Exec(`CREATE TABLE accounts (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	brokenBusiness, err := NewSQLiteDirectInputReader(openSQLiteReadOnlyTestDB(t, bareBusinessPath), openSQLiteReadOnlyTestDB(t, filepath.Join(fixture.dir, "stats.sqlite3")), fixture.secret, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := brokenBusiness.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "业务只读契约") {
		t.Fatalf("缺业务表必须契约失败: %v", err)
	}
}

// TestSQLiteDirectInputReaderRunnerCycleActivatesPendingAccount 是用户症状的
// 回归验收：sqlite store + SQLiteDirectInputReader + Runner 跑一个 cycle，
// 探活请求真实发生、outcome 落 jobs store、经现有投影路径把业务库
// accounts.status 从 pending_test 翻为 active。
func TestSQLiteDirectInputReaderRunnerCycleActivatesPendingAccount(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	var probeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected probe path: %s", request.URL.Path)
		}
		probeRequests++
		// 直读输入带协议 profile，探针校验完整 Chat Completions 语义：
		// finish_reason 非空且响应回显挑战值 "juhe"。
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	candidate := newSQLiteDirectCandidateSeed("wsql-cycle")
	candidate.baseURL = server.URL
	fixture.seedCandidate(t, candidate)

	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(fixture.dir, "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "wsql-cycle-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })

	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	inputDirectory := filepath.Join(fixture.dir, "inputs")
	if err := os.MkdirAll(inputDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewRunnerWithDirectInputReader(Config{
		CredentialSecret: fixture.secret,
		InputDirectory:   inputDirectory,
		ProbeTimeout:     2 * time.Second,
		MaxResponseBytes: 65536,
		MaxConcurrency:   1,
		DirectInputLimit: 8,
		Now:              func() time.Time { return sqliteDirectFixtureNow },
	}, store, nil, reader)
	if err := runner.runCycle(context.Background(), lease); err != nil {
		t.Fatalf("runCycle: %v", err)
	}
	if probeRequests != 1 {
		t.Fatalf("探活请求必须发生一次: %d", probeRequests)
	}
	state, found, err := store.LoadCurrentState(context.Background(), "wsql-cycle")
	if err != nil || !found {
		t.Fatalf("jobs store 状态必须落库: found=%t err=%v", found, err)
	}
	if state.Outcome != OutcomeSuccess || state.AccountStatus != "active" {
		t.Fatalf("outcome 错误: %#v", state)
	}
	// 经现有 OutcomeProjector 把 outcome 投影回业务库。
	businessHandle, err := NewProjectionBusinessDB(fixture.business, false)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{
		Business:         businessHandle,
		CredentialSecret: fixture.secret,
		PollInterval:     DefaultProjectionPollInterval,
		BatchSize:        DefaultProjectionBatchSize,
		Now:              func() time.Time { return sqliteDirectFixtureNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	drain, err := projector.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("投影 drain: %v", err)
	}
	if drain.Processed != 1 {
		t.Fatalf("必须投影 1 条 outcome: %+v", drain)
	}
	var status string
	if err := fixture.business.QueryRow(`SELECT status FROM accounts WHERE id = 'wsql-cycle'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("业务库 accounts.status 必须经投影翻为 active: %s", status)
	}
	var disposition string
	if err := fixture.business.QueryRow(`SELECT disposition FROM account_health_projection_receipts WHERE account_id = 'wsql-cycle'`).Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if disposition != "applied" {
		t.Fatalf("投影 receipt 必须为 applied: %s", disposition)
	}
}

// TestEnsureSQLiteDirectInputLayoutColdStart 是冷启动顺序修复的验收：从
// 「业务/统计两库文件都不存在」的空目录起点，ensure 兜底出完整 J1 契约布局
// （statsverify 布局 + J1 契约表 + settings 六键），随后只读 reader 的
// CheckContract 必须通过、LoadDue 必须可执行——即 jobs 单进程冷启动不再因
// 契约缺失 fail-fast；并验证 ensure 幂等（重复执行不报错、不覆盖种子值）。
func TestEnsureSQLiteDirectInputLayoutColdStart(t *testing.T) {
	dir := t.TempDir()
	businessPath := filepath.Join(dir, "business.sqlite3")
	statsPath := filepath.Join(dir, "stats.sqlite3")
	// 起点断言：两库文件都不存在。
	for _, path := range []string{businessPath, statsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("起点必须不存在 %s: %v", path, err)
		}
	}
	ctx := context.Background()
	if err := EnsureSQLiteDirectInputLayout(ctx, businessPath, statsPath); err != nil {
		t.Fatalf("冷启动布局兜底: %v", err)
	}
	// 幂等：重复执行必须无操作成功。
	if err := EnsureSQLiteDirectInputLayout(ctx, businessPath, statsPath); err != nil {
		t.Fatalf("重复 ensure 必须幂等: %v", err)
	}
	// 只读 reader 契约预检必须通过（main.go sqlite 分支的同一门禁）。
	reader := &SQLiteDirectInputReader{
		businessDB:       openSQLiteReadOnlyTestDB(t, businessPath),
		statsDB:          openSQLiteReadOnlyTestDB(t, statsPath),
		credentialSecret: "cold-start-secret",
		inputTTL:         time.Hour,
		now:              func() time.Time { return sqliteDirectFixtureNow },
		scheduleCache:    &directScheduleCache{ttl: directScheduleCacheTTL, now: func() time.Time { return sqliteDirectFixtureNow }},
	}
	if err := reader.CheckContract(ctx); err != nil {
		t.Fatalf("冷启动布局上 CheckContract 必须通过: %v", err)
	}
	if _, err := reader.LoadDue(ctx, 8); err != nil {
		t.Fatalf("冷启动布局上 LoadDue 必须可执行: %v", err)
	}
	// settings 六键已按 canonical 缺省播种且幂等不覆盖：手工改值后再 ensure，
	// 值必须保持手工值（INSERT OR IGNORE 语义）。
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = business.Close() })
	if _, err := business.Exec(`UPDATE system_settings SET value_json = '5' WHERE key = 'accountHealthCheckIntervalHours'`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSQLiteDirectInputLayout(ctx, businessPath, statsPath); err != nil {
		t.Fatal(err)
	}
	var interval int
	if err := business.QueryRow(`SELECT value_json FROM system_settings WHERE key = 'accountHealthCheckIntervalHours'`).Scan(&interval); err != nil {
		t.Fatal(err)
	}
	if interval != 5 {
		t.Fatalf("幂等 ensure 不得覆盖既有设置: %d", interval)
	}
}

// seedBinding 追加一条 enabled 分组绑定行（authID 为空串表示 NULL 授权绑定）。
func (f *sqliteDirectFixture) seedBinding(t *testing.T, groupID, accountID, authID string, updatedAt time.Time) {
	t.Helper()
	var auth any
	if authID != "" {
		auth = authID
	}
	if _, err := f.business.Exec(`INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled, updated_at) VALUES (?, ?, ?, 1, ?)`,
		groupID, accountID, auth, sqliteDirectTimestamp(updatedAt)); err != nil {
		t.Fatal(err)
	}
}

// loadSQLiteDirectCandidates 以与 reader 相同的实参执行冻结候选 SQL，返回经
// scanDirectCandidate 解码的候选行（可断言 binding.group_id /
// account_authorization_id 的选中结果）。
func loadSQLiteDirectCandidates(t *testing.T, reader *SQLiteDirectInputReader, limit int) []directCandidate {
	t.Helper()
	rows, err := reader.businessDB.QueryContext(context.Background(), sqliteDirectInputCandidatesSQL,
		sqliteDirectTimestamp(sqliteDirectFixtureNow), limit, 0, "", 0)
	if err != nil {
		t.Fatalf("执行冻结候选 SQL: %v", err)
	}
	defer rows.Close()
	var result []directCandidate
	for rows.Next() {
		candidate, err := scanDirectCandidate(rows)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// TestSQLiteDirectInputReaderBindingJoinSingleRow 验证 binding join 的两层分
// 区与 PG LATERAL 恒定单行语义逐字等价：NULL 授权账户取全部 enabled 绑定的
// 全局顶行（含 NULL 与非 NULL auth 混合），授权实例取其 auth 分区内顶行；
// 任何跨 auth 分区的绑定组合都不得让同一账户在候选集出现多行（多行会导致
// 同周期重复探活）。
func TestSQLiteDirectInputReaderBindingJoinSingleRow(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	t1 := sqliteDirectFixtureNow.Add(-3 * time.Hour)
	t2 := sqliteDirectFixtureNow.Add(-2 * time.Hour)
	t3 := sqliteDirectFixtureNow.Add(-time.Hour)

	// 场景一：NULL 授权 + 两个非 NULL auth 分区 → 全局顶 grpY（updated DESC）；
	// 变体补第三行（authA→grpZ updated 更新）：authA 分区顶变为 grpZ 且成为全
	// 局顶，authB 分区的 grpY 仍占分区 rank=1，不得干扰选择。
	fixture.seedCandidate(t, newSQLiteDirectCandidateSeed("wsql-bind1"))
	fixture.seedBinding(t, "wsql-grpX", "wsql-bind1", "wsql-authA", t1)
	fixture.seedBinding(t, "wsql-grpY", "wsql-bind1", "wsql-authB", t2)
	fixture.seedBinding(t, "wsql-grpZ", "wsql-bind1", "wsql-authA", t3)

	// 场景二：NULL 授权 + NULL auth 绑定与非 NULL auth 绑定混合 → 全局顶
	// grpM（NULL 分区与非 NULL 分区共同参与全局排序）。
	fixture.seedCandidate(t, newSQLiteDirectCandidateSeed("wsql-bind2"))
	fixture.seedBinding(t, "wsql-grpN", "wsql-bind2", "wsql-authC", t1)
	fixture.seedBinding(t, "wsql-grpM", "wsql-bind2", "", t2)

	// 场景三：授权实例（auth=wsql-authX）+ 另一 auth=wsql-authY 的更新绑定 →
	// 恰取 X 分区内顶行 grpP；authY 的新行既不干扰也不被选中（若被选中，
	// ToInput 的授权绑定 fence 校验会把候选变成隔离 failure，计数断言即失败）。
	source := newSQLiteDirectCandidateSeed("wsql-authsrc")
	source.status = "active"
	fixture.seedCandidate(t, source)
	instance := newSQLiteDirectCandidateSeed("wsql-authacc")
	instance.extraColumns["authorization_instance_source_account_id"] = "wsql-authsrc"
	instance.extraColumns["authorization_instance_authorization_id"] = "wsql-authX"
	fixture.seedCandidate(t, instance)
	if _, err := fixture.business.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status) VALUES ('wsql-authX', 'account', 'wsql-authsrc', 'sys_admin', 'sys_admin', 'active')`); err != nil {
		t.Fatal(err)
	}
	fixture.seedBinding(t, "wsql-grpP", "wsql-authacc", "wsql-authX", t1)
	fixture.seedBinding(t, "wsql-grpQ", "wsql-authacc", "wsql-authY", t2)

	// 候选行断言：每账户恰一行，且选中预期分组/授权绑定。
	candidates := loadSQLiteDirectCandidates(t, reader, 50)
	byAccount := map[string]directCandidate{}
	for _, candidate := range candidates {
		if _, duplicate := byAccount[candidate.account.ID]; duplicate {
			t.Fatalf("账户 %s 在候选集出现多行（重复探活漂移）: %+v", candidate.account.ID, candidates)
		}
		byAccount[candidate.account.ID] = candidate
	}
	// wsql-authsrc 是源账户自身的活跃候选（默认分组绑定），与三个场景账户
	// 共 4 行；关键不变量是每账户恰一行（重复行检查在上面）。
	if len(candidates) != 4 {
		t.Fatalf("候选行数必须为 4（含源账户自身候选）: %+v", candidates)
	}
	if got := byAccount["wsql-bind1"].binding; got.GroupID != "wsql-grpZ" || got.AuthorizationBindingID != "wsql-authA" {
		t.Fatalf("场景一必须取全局顶 grpZ/authA: %+v", got)
	}
	if got := byAccount["wsql-bind2"].binding; got.GroupID != "wsql-grpM" || got.AuthorizationBindingID != "" {
		t.Fatalf("场景二必须取全局顶 grpM/NULL: %+v", got)
	}
	if got := byAccount["wsql-authacc"].binding; got.GroupID != "wsql-grpP" || got.AuthorizationBindingID != "wsql-authX" {
		t.Fatalf("场景三必须取 authX 分区内顶行 grpP: %+v", got)
	}

	// LoadDue 语义断言：3 个输入、0 隔离失败（授权实例经 ToInput 完整校验）。
	result, err := reader.LoadDueWithFailures(context.Background(), 50)
	if err != nil {
		t.Fatalf("LoadDueWithFailures: %v", err)
	}
	if len(result.Inputs) != 4 || len(result.Failures) != 0 {
		t.Fatalf("inputs=%d failures=%+v", len(result.Inputs), result.Failures)
	}
	seen := map[string]int{}
	for _, input := range result.Inputs {
		seen[input.AccountID]++
	}
	for _, accountID := range []string{"wsql-bind1", "wsql-bind2", "wsql-authacc", "wsql-authsrc"} {
		if seen[accountID] != 1 {
			t.Fatalf("账户 %s 输入数必须为 1: %v", accountID, seen)
		}
	}
}

// TestSQLiteDirectInputReaderAuthorizationBindingPartitionMissingFilters
// 锁定授权实例账户的 binding 分区过滤：authorization_instance_authorization_id
// 非 NULL 且该 auth 分区在 group_accounts 中没有 enabled 行（唯一绑定行
// enabled=0）时，两层 binding CTE 选不出行 → binding.group_id 全 NULL →
// 外层 `binding.group_id IS NOT NULL` 把账户过滤出候选集。授权本身存在且
// active，过滤只能来自 binding 分区缺失，不得与授权资格谓词混淆。
func TestSQLiteDirectInputReaderAuthorizationBindingPartitionMissingFilters(t *testing.T) {
	fixture := newSQLiteDirectFixture(t)
	// 对照候选：NULL 授权 + 默认 enabled 绑定 → 必须出现在候选。
	due := newSQLiteDirectCandidateSeed("wsql-bindok")
	due.status = "active"
	fixture.seedCandidate(t, due)
	// 授权实例源账户（合格 source：active 且 schedulable=1）。
	source := newSQLiteDirectCandidateSeed("wsql-bindsrc")
	source.status = "active"
	fixture.seedCandidate(t, source)
	// 实验候选：授权非 NULL 指向 active 授权 wsql-bindAuth。
	instance := newSQLiteDirectCandidateSeed("wsql-bindmiss")
	instance.status = "active"
	instance.extraColumns["authorization_instance_source_account_id"] = "wsql-bindsrc"
	instance.extraColumns["authorization_instance_authorization_id"] = "wsql-bindAuth"
	fixture.seedCandidate(t, instance)
	if _, err := fixture.business.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status) VALUES ('wsql-bindAuth', 'account', 'wsql-bindsrc', 'sys_admin', 'sys_admin', 'active')`); err != nil {
		t.Fatal(err)
	}
	// 唯一绑定行 auth=wsql-bindAuth 但 enabled=0：该 auth 分区无 enabled 行。
	if _, err := fixture.business.Exec(`INSERT INTO group_accounts (group_id, account_id, account_authorization_id, enabled, updated_at) VALUES ('wsql-grpMiss', 'wsql-bindmiss', 'wsql-bindAuth', 0, '2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	reader := fixture.readOnlyReader(t, func() time.Time { return sqliteDirectFixtureNow })
	result, err := reader.LoadDueWithFailures(context.Background(), 10)
	if err != nil {
		t.Fatalf("LoadDueWithFailures: %v", err)
	}
	ids := map[string]bool{}
	for _, input := range result.Inputs {
		ids[input.AccountID] = true
	}
	if ids["wsql-bindmiss"] {
		t.Fatalf("auth 分区无 enabled 绑定的实例账户必须被 binding.group_id IS NOT NULL 过滤: %v", ids)
	}
	if !ids["wsql-bindok"] || !ids["wsql-bindsrc"] {
		t.Fatalf("对照候选必须保留: %v", ids)
	}
}
