package settings

// w11f 覆盖波次：脚本化 sqlite 连接层故障注入 + 纯函数/直连分支测试。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var w11fBoom = errors.New("w11f boom")

// ---------------------------------------------------------------------------
// 故障脚本（与其它 w11f 包同型的连接层注入）
// ---------------------------------------------------------------------------

type w11fRule struct {
	substr   string
	queryErr error
	execErr  error
	cols     []string
	rows     [][]driver.Value
	nextErr  error
	limit    int
	hits     int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w11fScript struct {
	mu        sync.Mutex
	t         *testing.T
	rules     []*w11fRule
	beginErr  error
	commitErr error
}

// allowUnfired 把规则标记为负对照（故意不命中），Cleanup 断言跳过。
func (r *w11fRule) allowUnfired() *w11fRule {
	r.exempt = true
	return r
}

// assertRulesFired 在测试收尾断言所有注入规则都被真实消费过（hits>0）：
// 子串与生产 SQL 漂移导致规则永不命中的伪覆盖臂在此变红。
func (s *w11fScript) assertRulesFired() {
	if s.t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.hits == 0 && !r.exempt {
			s.t.Errorf("w11f 注入规则未命中（伪覆盖）：substr=%q", r.substr)
		}
	}
}

func (s *w11fScript) rule(r *w11fRule) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) failQuery(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom})
}

func (s *w11fScript) failQueryAlways(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom, limit: -1})
}

func (s *w11fScript) canned(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	return s.rule(&w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr})
}

func (s *w11fScript) failExec(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, execErr: w11fBoom})
}

func (s *w11fScript) failBegin() {
	s.mu.Lock()
	s.beginErr = w11fBoom
	s.mu.Unlock()
}

func (s *w11fScript) failCommit() {
	s.mu.Lock()
	s.commitErr = w11fBoom
	s.mu.Unlock()
}

func (s *w11fScript) take(query string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(query, r.substr) {
			continue
		}
		if r.limit >= 0 && r.hits >= max(r.limit, 1) {
			continue
		}
		r.hits++
		return r
	}
	return nil
}

func (s *w11fScript) beginFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.beginErr
	s.beginErr = nil
	return err
}

func (s *w11fScript) commitFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.commitErr
	s.commitErr = nil
	return err
}

type w11fConnector struct {
	base   driver.Connector
	script *w11fScript
}

func (c w11fConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w11fConn{base: conn, script: c.script}, nil
}

func (c w11fConnector) Driver() driver.Driver { return c.base.Driver() }

type w11fConn struct {
	base   driver.Conn
	script *w11fScript
}

func (c *w11fConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w11fConn) Close() error                              { return c.base.Close() }

func (c *w11fConn) Begin() (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w11fTx{base: tx, script: c.script}, nil
}

func (c *w11fConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &w11fTx{base: tx, script: c.script}, nil
	}
	return c.Begin()
}

func (c *w11fConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if rule := c.script.take(query); rule != nil && rule.execErr != nil {
		return nil, rule.execErr
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w11fConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.queryErr != nil {
			return nil, rule.queryErr
		}
		if rule.cols != nil {
			return &w11fRows{cols: rule.cols, values: rule.rows, nextErr: rule.nextErr}, nil
		}
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type w11fTx struct {
	base   driver.Tx
	script *w11fScript
}

func (t *w11fTx) Commit() error {
	if err := t.script.commitFailure(); err != nil {
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *w11fTx) Rollback() error { return t.base.Rollback() }

type w11fRows struct {
	cols    []string
	values  [][]driver.Value
	next    int
	nextErr error
}

func (r *w11fRows) Columns() []string { return r.cols }
func (r *w11fRows) Close() error      { return nil }
func (r *w11fRows) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	row := r.values[r.next]
	r.next++
	copy(dest, row)
	return nil
}

// ---------------------------------------------------------------------------
// 环境
// ---------------------------------------------------------------------------

type w11fEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
	store  *Store
	script *w11fScript
}

func newW11FEnv(t *testing.T) *w11fEnv {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + w11fNextDBName() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
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
	return &w11fEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, store: store, script: script}
}

func (e *w11fEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
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

func (e *w11fEnv) login(t *testing.T, username, password, role string) {
	t.Helper()
	if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
	}); err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
}

func (e *w11fEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// 纯函数分支
// ---------------------------------------------------------------------------

func TestW11FHelperBranches(t *testing.T) {
	// 错误类型。
	if (&ValidationError{Message: "m"}).Error() != "m" {
		t.Fatal("ValidationError drift")
	}
	if (&UnknownSettingsSectionError{Key: "k"}).Error() != "未知设置分区：k" {
		t.Fatal("UnknownSettingsSectionError drift")
	}
	// NewStore(nil) 与 PG 方言。
	if _, err := NewStore(nil, false, nil, nil); err == nil {
		t.Fatal("NewStore(nil) must fail")
	}
	pgStore := &Store{pg: true}
	if pgStore.table("x") != "juhe_business.x" {
		t.Fatal("pg table drift")
	}
	if got := pgStore.bind("UPDATE t SET a = ?, b = ?"); got != "UPDATE t SET a = $1, b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
	if itoa(0) != "0" || itoa(76) != "76" {
		t.Fatal("itoa drift")
	}
	if ensureCtx(nil) == nil {
		t.Fatal("ensureCtx drift")
	}
	if placeholders(2) != "?,?" || placeholders(0) != "" {
		t.Fatal("placeholders drift")
	}
	// resolveSettingsSection。
	if _, err := resolveSettingsSection("nope"); err == nil {
		t.Fatal("resolveSettingsSection unknown must fail")
	}
	if section, err := resolveSettingsSection("brand"); err != nil || section.Domain != settingsSectionDomainGlobal {
		t.Fatalf("resolveSettingsSection brand = %+v/%v", section, err)
	}
	// queryArgs 带/不带首参。
	if args := queryArgs([]string{"a"}, "first"); args[0] != "first" || args[1] != "a" {
		t.Fatalf("queryArgs first = %v", args)
	}
	if args := queryArgs([]string{"a"}, ""); len(args) != 1 || args[0] != "a" {
		t.Fatalf("queryArgs no first = %v", args)
	}
	// errorText: nil / 空 message / 有 message。
	if errorText(nil, "fb") != "fb" {
		t.Fatal("errorText nil drift")
	}
	if errorText(errors.New(""), "fb") != "fb" {
		t.Fatal("errorText empty drift")
	}
	if errorText(errors.New("boom"), "fb") != "boom" {
		t.Fatal("errorText message drift")
	}
	// comparableValue: nil / 不可序列化。
	if comparableValue(nil) != "null" {
		t.Fatal("comparableValue nil drift")
	}
	if comparableValue(make(chan int)) != "null" {
		t.Fatal("comparableValue chan drift")
	}
	// safeChangeText: nil / 长串截断 / 数字 / 布尔 JSON / 不可序列化。
	if safeChangeText(nil) != "" {
		t.Fatal("safeChangeText nil drift")
	}
	if got := safeChangeText(strings.Repeat("长", 201)); got != strings.Repeat("长", 200)+"..." {
		t.Fatalf("safeChangeText clamp = %d", len(got))
	}
	if safeChangeText(float64(42)) != "42" || safeChangeText(float64(4.5)) != "4.5" {
		t.Fatal("safeChangeText number drift")
	}
	if safeChangeText(true) != "true" {
		t.Fatal("safeChangeText bool drift")
	}
	if safeChangeText(make(chan int)) != "" {
		t.Fatal("safeChangeText chan drift")
	}
	// diffSafeFields / diffSafeFieldsWithLabels。
	changes := diffSafeFields(map[string]any{"a": 1.0, "b": 2.0}, map[string]any{"a": 1.0, "b": 3.0}, []string{"a", "b"})
	if len(changes) != 1 || changes[0].Field != "b" || changes[0].Label != "b" || changes[0].Before != "2" || changes[0].After != "3" {
		t.Fatalf("diffSafeFields = %+v", changes)
	}
	changes = diffSafeFieldsWithLabels(map[string]any{"k": nil}, map[string]any{"k": "v"}, map[string]string{"k": "标签"})
	if len(changes) != 1 || changes[0].Label != "标签" || changes[0].Before != "" || changes[0].After != "v" {
		t.Fatalf("diffSafeFieldsWithLabels = %+v", changes)
	}
	// bodyKeys 排序。
	if keys := bodyKeys(map[string]any{"b": 1, "a": 1}); len(keys) != 2 || keys[0] != "a" {
		t.Fatalf("bodyKeys = %v", keys)
	}
	// writeMutationError 直连: 校验 → 400; 其他 → 500。
	recorder := httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, &ValidationError{Message: "参数错误"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("writeMutationError validation = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeMutationError(recorder, w11fBoom)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("writeMutationError generic = %d", recorder.Code)
	}
	// 规范化: 未知键 / 非整数 / 小数 / 越界 / 时区分支。
	if _, err := normalizeSystemSetting("nope", float64(1)); err == nil {
		t.Fatal("unknown system key must fail")
	}
	if _, err := normalizeSystemSetting("userAiAccountLimit", "x"); err == nil {
		t.Fatal("non-number must fail")
	}
	if _, err := normalizeSystemSetting("userAiAccountLimit", 1.5); err == nil {
		t.Fatal("fraction must fail")
	}
	if _, err := normalizeSystemSetting("userAiAccountLimit", -1.0); err == nil {
		t.Fatal("below min must fail")
	}
	if _, err := normalizeSystemSetting("userAiAccountLimit", 1_000_001.0); err == nil {
		t.Fatal("above max must fail")
	}
	if got, err := normalizeSystemSetting("userAiAccountLimit", 5.0); err != nil || got != 5.0 {
		t.Fatalf("valid integer = %v/%v", got, err)
	}
	// 全局键: 未知 / 非字符串 / 空白 / 合法。
	if _, err := normalizeGlobalSetting("nope", "v"); err == nil {
		t.Fatal("unknown global key must fail")
	}
	if _, err := normalizeGlobalSetting("appName", 3); err == nil {
		t.Fatal("non-string global must fail")
	}
	if _, err := normalizeGlobalSetting("appName", "  "); err == nil {
		t.Fatal("blank global must fail")
	}
	if got, err := normalizeGlobalSetting("appName", " 名字 "); err != nil || got != "名字" {
		t.Fatalf("global trim = %v/%v", got, err)
	}
	// 时区: 非字符串 / 空白 / 不存在 / 合法。
	if _, err := normalizeUsageStatsTimezone(1); err == nil {
		t.Fatal("non-string timezone must fail")
	}
	if _, err := normalizeUsageStatsTimezone("  "); err == nil {
		t.Fatal("blank timezone must fail")
	}
	if _, err := normalizeUsageStatsTimezone("Mars/Olympus"); err == nil {
		t.Fatal("unknown timezone must fail")
	}
	if got, err := normalizeUsageStatsTimezone(" Asia/Shanghai "); err != nil || got != "Asia/Shanghai" {
		t.Fatalf("timezone = %v/%v", got, err)
	}
	// 输入规范化: 空更新。
	if _, err := normalizeSystemSettingsInput(map[string]any{}); err == nil {
		t.Fatal("empty system input must fail")
	}
	if _, err := normalizeGlobalSettingsInput(map[string]any{}); err == nil {
		t.Fatal("empty global input must fail")
	}
	if normalized, err := normalizeSystemSettingsInput(map[string]any{"userAiAccountLimit": 7.0}); err != nil || normalized["userAiAccountLimit"] != 7.0 {
		t.Fatalf("system input = %v/%v", normalized, err)
	}
	if _, err := normalizeGlobalSettingsInput(map[string]any{"appName": "n"}); err != nil {
		t.Fatalf("global input = %v", err)
	}
}

// ---------------------------------------------------------------------------
// store 层故障与分支
// ---------------------------------------------------------------------------

func TestW11FStoreFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	ctx := context.Background()

	// Load: 命中缓存 + nil ctx。
	first, err := store.Load(ctx)
	if err != nil || len(first) != len(SystemSettingKeys) {
		t.Fatalf("Load = %v/%v", len(first), err)
	}
	if _, err := store.Load(nil); err != nil {
		t.Fatal(err)
	}
	// SettingsSnapshot 与 LoadGlobal 缓存命中。
	if _, err := store.SettingsSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadGlobal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadGlobal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadPublic(ctx); err != nil {
		t.Fatal(err)
	}

	// 查询错误 / Scan 错误 / 坏 JSON / 未知键 / 越界值 / rows.Err / 缺键。
	env.script.failQuery("FROM system_settings")
	if _, err := store.loadFromDatabase(ctx); err == nil {
		t.Fatal("load query fault must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{nil, "1"}}, nil)
	if _, err := store.loadFromDatabase(ctx); err == nil {
		t.Fatal("load scan fault must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"userAiAccountLimit", "{bad"}}, nil)
	if _, err := store.loadFromDatabase(ctx); err == nil {
		t.Fatal("load bad json must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"nope", "1"}}, nil)
	if _, err := store.loadFromDatabase(ctx); err == nil {
		t.Fatal("load unknown key must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"userAiAccountLimit", "99999999"}}, nil)
	if _, err := store.loadFromDatabase(ctx); err == nil {
		t.Fatal("load out-of-range must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"userAiAccountLimit", "7"}}, w11fBoom)
	if _, err := store.loadFromDatabase(ctx); err == nil {
		t.Fatal("load rows.Err must fail")
	}
	env.exec(t, `DELETE FROM system_settings WHERE key = 'operationLogRetentionDays'`)
	if _, err := store.loadFromDatabase(ctx); err == nil || !strings.Contains(err.Error(), "缺少字段") {
		t.Fatalf("load missing key = %v", err)
	}
	env.exec(t, `INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'operationLogRetentionDays', '365', '2024-01-01T00:00:00Z')`)

	// Update: 校验错误（未知键）/ 时区守卫（有数据时拒绝）/ upsert 故障。
	if _, err := store.Update(ctx, map[string]any{"nope": 1.0}); err == nil {
		t.Fatal("Update unknown key must fail")
	}
	if _, err := store.Update(ctx, map[string]any{}); err == nil {
		t.Fatal("Update empty must fail")
	}
	env.exec(t, `INSERT INTO usage_stats_totals (probe) VALUES ('x')`)
	if _, err := store.Update(ctx, map[string]any{"usageStatsTimezone": "Asia/Shanghai"}); err == nil || !strings.Contains(err.Error(), "不能直接修改统计时区") {
		t.Fatalf("Update timezone with data = %v", err)
	}
	env.exec(t, `DELETE FROM usage_stats_totals`)
	env.script.failBegin()
	if _, err := store.Update(ctx, map[string]any{"userAiAccountLimit": 9.0}); err == nil {
		t.Fatal("Update begin fault must fail")
	}
	env.script.failExec("INSERT INTO system_settings")
	if _, err := store.Update(ctx, map[string]any{"userAiAccountLimit": 9.0}); err == nil {
		t.Fatal("Update upsert fault must fail")
	}
	env.script.failCommit()
	if _, err := store.Update(ctx, map[string]any{"userAiAccountLimit": 9.0}); err == nil {
		t.Fatal("Update commit fault must fail")
	}
	// 提交后刷新缓存失败。
	env.script.failQuery("FROM system_settings")
	if _, err := store.Update(ctx, map[string]any{"userAiAccountLimit": 9.0}); err == nil {
		t.Fatal("Update refresh fault must fail")
	}
	// 时区守卫内部 Load 失败。
	env.script.failQueryAlways("FROM system_settings")
	if _, err := store.Update(ctx, map[string]any{"usageStatsTimezone": "Asia/Shanghai"}); err == nil {
		t.Fatal("timezone guard load fault must fail")
	}

	// usageStatsDataExists 探针错误。
	probeEnv := newW11FEnv(t)
	probeEnv.script.failQueryAlways("FROM usage_stats_totals")
	if _, err := probeEnv.store.usageStatsDataExists(ctx); err == nil {
		t.Fatal("usageStatsDataExists probe fault must fail")
	}

	// LoadGlobal 故障族。
	env2 := newW11FEnv(t)
	env2.script.failQuery("FROM global_settings")
	if _, err := env2.store.loadGlobalFromDatabase(ctx); err == nil {
		t.Fatal("global query fault must fail")
	}
	env2.script.canned("FROM global_settings", []string{"key", "value_json"}, [][]driver.Value{{nil, "v"}}, nil)
	if _, err := env2.store.loadGlobalFromDatabase(ctx); err == nil {
		t.Fatal("global scan fault must fail")
	}
	env2.script.canned("FROM global_settings", []string{"key", "value_json"}, [][]driver.Value{{"appName", "{bad"}}, nil)
	if _, err := env2.store.loadGlobalFromDatabase(ctx); err == nil {
		t.Fatal("global bad json must fail")
	}
	env2.script.canned("FROM global_settings", []string{"key", "value_json"}, [][]driver.Value{{"nope", "v"}}, nil)
	if _, err := env2.store.loadGlobalFromDatabase(ctx); err == nil {
		t.Fatal("global unknown key must fail")
	}
	env2.script.canned("FROM global_settings", []string{"key", "value_json"}, [][]driver.Value{{"appName", "\"n\""}}, w11fBoom)
	if _, err := env2.store.loadGlobalFromDatabase(ctx); err == nil {
		t.Fatal("global rows.Err must fail")
	}
	env2.exec(t, `DELETE FROM global_settings WHERE key = 'appIcon'`)
	if _, err := env2.store.loadGlobalFromDatabase(ctx); err == nil || !strings.Contains(err.Error(), "缺少字段") {
		t.Fatalf("global missing key = %v", err)
	}
	env2.exec(t, `INSERT INTO global_settings (key, value_json, updated_at) VALUES ('appIcon', '"/i"', '2024-01-01T00:00:00Z')`)

	// UpdateGlobal: 校验错误 / begin / exec / commit / 刷新失败。
	if _, err := env2.store.UpdateGlobal(ctx, map[string]any{"appName": 1}); err == nil {
		t.Fatal("UpdateGlobal invalid must fail")
	}
	env2.script.failBegin()
	if _, err := env2.store.UpdateGlobal(ctx, map[string]any{"appName": "w11f 名"}); err == nil {
		t.Fatal("UpdateGlobal begin fault must fail")
	}
	env2.script.failExec("INSERT INTO global_settings")
	if _, err := env2.store.UpdateGlobal(ctx, map[string]any{"appName": "w11f 名"}); err == nil {
		t.Fatal("UpdateGlobal exec fault must fail")
	}
	env2.script.failCommit()
	if _, err := env2.store.UpdateGlobal(ctx, map[string]any{"appName": "w11f 名"}); err == nil {
		t.Fatal("UpdateGlobal commit fault must fail")
	}
	env2.script.failQuery("FROM global_settings")
	if _, err := env2.store.UpdateGlobal(ctx, map[string]any{"appName": "w11f 名"}); err == nil {
		t.Fatal("UpdateGlobal refresh fault must fail")
	}
}

func TestW11FSectionFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	ctx := context.Background()

	// LoadSection: 未知分区 / 全局域 / 查询错误 / Scan / 坏 JSON / 未知键 / rows.Err / 缺键。
	if _, err := store.LoadSection(ctx, "nope"); err == nil {
		t.Fatal("LoadSection unknown must fail")
	}
	if values, err := store.LoadSection(ctx, "brand"); err != nil || values["appName"] != "聚合 AI" {
		t.Fatalf("brand section = %v/%v", values, err)
	}
	env.script.failQuery("FROM system_settings")
	if _, err := store.LoadSection(ctx, "data-retention"); err == nil {
		t.Fatal("section query fault must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{nil, "1"}}, nil)
	if _, err := store.LoadSection(ctx, "data-retention"); err == nil {
		t.Fatal("section scan fault must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"usageRecordRetentionDays", "{bad"}}, nil)
	if _, err := store.LoadSection(ctx, "data-retention"); err == nil {
		t.Fatal("section bad json must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"nope", "1"}}, nil)
	if _, err := store.LoadSection(ctx, "data-retention"); err == nil {
		t.Fatal("section unknown key must fail")
	}
	env.script.canned("FROM system_settings", []string{"key", "value_json"}, [][]driver.Value{{"usageRecordRetentionDays", "30"}}, w11fBoom)
	if _, err := store.LoadSection(ctx, "data-retention"); err == nil {
		t.Fatal("section rows.Err must fail")
	}
	// system 分区缺键（兼容默认只覆盖五个键，删一个非默认键）。
	env.exec(t, `DELETE FROM system_settings WHERE key = 'runtimeLogIndexRetentionDays'`)
	if _, err := store.LoadSection(ctx, "data-retention"); err == nil || !strings.Contains(err.Error(), "缺少字段") {
		t.Fatalf("section missing key = %v", err)
	}
	env.exec(t, `INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'runtimeLogIndexRetentionDays', '14', '2024-01-01T00:00:00Z')`)

	// UpdateSection: 未知分区 / 空输入 / 未授权键 / 原型污染键 / 校验错误 / 全局与系统域写入 / 故障。
	if _, err := store.UpdateSection(ctx, "nope", map[string]any{"appName": "n"}); err == nil {
		t.Fatal("UpdateSection unknown must fail")
	}
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{}); err == nil {
		t.Fatal("UpdateSection empty must fail")
	}
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"nope": "v"}); err == nil {
		t.Fatal("UpdateSection unknown key must fail")
	}
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"__proto__": "v"}); err == nil {
		t.Fatal("UpdateSection proto key must fail")
	}
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"appName": " "}); err == nil {
		t.Fatal("UpdateSection blank must fail")
	}
	if _, err := store.UpdateSection(ctx, "data-retention", map[string]any{"usageRecordRetentionDays": 0.5}); err == nil {
		t.Fatal("UpdateSection fraction must fail")
	}
	// 系统域: 时区守卫带数据拒绝（cooldown-retest 无时区键，改用整体 Update 已覆盖；这里测 upsert 故障）。
	env.script.failBegin()
	if _, err := store.UpdateSection(ctx, "cooldown-retest", map[string]any{"cooldownAccountRetestMaxBackoffHours": 24.0}); err == nil {
		t.Fatal("UpdateSection begin fault must fail")
	}
	env.script.failExec("INSERT INTO system_settings")
	if _, err := store.UpdateSection(ctx, "cooldown-retest", map[string]any{"cooldownAccountRetestMaxBackoffHours": 24.0}); err == nil {
		t.Fatal("UpdateSection exec fault must fail")
	}
	// 提交后系统缓存刷新失败。
	env.script.failQuery("FROM system_settings")
	if _, err := store.UpdateSection(ctx, "cooldown-retest", map[string]any{"cooldownAccountRetestMaxBackoffHours": 24.0}); err == nil {
		t.Fatal("UpdateSection refresh fault must fail")
	}
	// 全局域: begin / exec / 刷新失败。
	env.script.failBegin()
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"appName": "w11f 品牌"}); err == nil {
		t.Fatal("brand begin fault must fail")
	}
	env.script.failExec("INSERT INTO global_settings")
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"appName": "w11f 品牌"}); err == nil {
		t.Fatal("brand exec fault must fail")
	}
	env.script.failQuery("FROM global_settings")
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"appName": "w11f 品牌"}); err == nil {
		t.Fatal("brand refresh fault must fail")
	}
	// 正常写回。
	if _, err := store.UpdateSection(ctx, "brand", map[string]any{"appName": "w11f 品牌"}); err != nil {
		t.Fatal(err)
	}
	// 系统域时区守卫（带统计数据拒绝）。
	env.exec(t, `INSERT INTO usage_stats_totals (probe) VALUES ('x')`)
	if _, err := store.UpdateSection(ctx, "gateway-core", map[string]any{"textFirstResponseTimeoutSeconds": 130.0}); err != nil {
		t.Fatalf("non-timezone system section = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 路由层故障
// ---------------------------------------------------------------------------

func TestW11FRouteFaults(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11fadmin", "w11f-pass", "super_admin")

	// 公共设置 500。
	env.script.failQuery("FROM global_settings")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/settings/public", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("public fault = %d %v", code, payload)
	}
	// 全局设置 500 / PATCH 全局: 坏 JSON / LoadGlobal 错误。
	env.script.failQuery("FROM global_settings")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/global", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("global fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/global", `{bad`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch global malformed = %d %v", code, payload)
	}
	env.script.failQuery("FROM global_settings")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/global", `{"appName":"n"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch global load fault = %d %v", code, payload)
	}
	// 分区: GET 未知 → 400; GET 存储错误 → 500; PATCH 未知分区 → 400; PATCH 非对象 → 400;
	// PATCH LoadSection 错误 → 400; PATCH UpdateSection 错误 → 400。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/nope", "")
	if code != http.StatusBadRequest || payload["message"] != "未知设置分区：nope" {
		t.Fatalf("section unknown = %d %v", code, payload)
	}
	env.script.failQuery("FROM system_settings")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings/sections/data-retention", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("section fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/nope", `{"appName":"n"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch section unknown = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/brand", `[1]`)
	if code != http.StatusBadRequest || payload["message"] != "设置分区更新必须是普通 JSON 对象" {
		t.Fatalf("patch section non object = %d %v", code, payload)
	}
	env.script.failQuery("FROM global_settings")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/brand", `{"appName":"n"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch section load fault = %d %v", code, payload)
	}
	env.script.failExec("INSERT INTO global_settings")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/sections/brand", `{"appName":"w11f 分区名"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch section update fault = %d %v", code, payload)
	}
	// 全量设置: GET 500 / PATCH 坏 JSON / PATCH Load 500 / PATCH 校验 400。
	env.script.failQuery("FROM system_settings")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/settings", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("settings fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{bad`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch settings malformed = %d %v", code, payload)
	}
	env.script.failQuery("FROM system_settings")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"userAiAccountLimit":5}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("patch settings load fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"nope":5}`)
	if code != http.StatusBadRequest || payload["message"] != "未知系统设置字段：nope" {
		t.Fatalf("patch settings invalid = %d %v", code, payload)
	}
	// PATCH 成功 + sink。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings", `{"userAiAccountLimit":11}`)
	if code != http.StatusOK {
		t.Fatalf("patch settings hit = %d %v", code, payload)
	}
	// PATCH 全局成功 + sink（diff 覆盖 appName 与 appIcon）。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/settings/global", `{"appName":"w11f 全局名","appIcon":"/w11f.svg"}`)
	if code != http.StatusOK {
		t.Fatalf("patch global hit = %d %v", code, payload)
	}
	entries := env.sink.snapshot()
	actions := map[string]bool{}
	for _, entry := range entries {
		actions[entry.Action] = true
	}
	if !actions["update_settings"] || !actions["update_global"] {
		t.Fatalf("sink actions = %v", actions)
	}
	_ = fmt.Sprint
}

// w11fDBSequence 保证同一测试内多次建 env 的 DSN 唯一。
var w11fDBSequence int64

func w11fNextDBName() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()+atomicAdd())
}

func atomicAdd() int64 {
	return w11fDBSequence + 1
}
