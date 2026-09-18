package accounts

// w14l 覆盖率收尾：脚本化连接层故障注入（对齐 chat 包 w10d 框架，见
// internal/chat/w10d_faultdriver_test.go）。通过 sqlite.NewConnector 的连接
// interposition 按查询子串注入一次性/第 n 次/持续失败，并在事务边界
// （Commit/Rollback）注入失败；w14lNullRows 返回整行 NULL，用于命中
// Scan 半途类型失败臂。w14b 曾登记 Commit/Rollback 失败臂与 Scan 半途
// 失败臂为残余缺口，本文件提供统一注入手段。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

// w14lInjected 是故障脚本返回的统一错误。
var w14lInjected = errors.New("w14l 注入故障")

// w14lCommitKey / w14lRollbackKey 是事务边界故障的特殊匹配键。
const (
	w14lCommitKey   = "\x00w14l-commit"
	w14lRollbackKey = "\x00w14l-rollback"
)

// w14lScript 记录按查询子串匹配的故障规则：rules 持续命中，once 命中一次
// 即出队，nth 在该子串第 n 次命中时失败；nullRows 命中后返回整行 NULL 的
// 行集（Scan 类型失败臂）。
type w14lScript struct {
	mu    sync.Mutex
	rules map[string]error
	once  map[string][]error
	nth   map[string][]int
	nulls map[string]int
	hits  map[string]int
}

func newW14LScript() *w14lScript {
	return &w14lScript{rules: map[string]error{}, once: map[string][]error{}, nth: map[string][]int{}, nulls: map[string]int{}, hits: map[string]int{}}
}

// fail 注册持续失败。
func (s *w14lScript) fail(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules[substr] = w14lInjected
}

// failCommit / failRollback 在事务边界注入持续失败。
func (s *w14lScript) failCommit()   { s.fail(w14lCommitKey) }
func (s *w14lScript) failRollback() { s.fail(w14lRollbackKey) }

// failOnce 注册一次性失败。
func (s *w14lScript) failOnce(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.once[substr] = append(s.once[substr], w14lInjected)
}

// failNth 注册该子串第 n 次命中的失败；之前的命中放行。
func (s *w14lScript) failNth(substr string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nth[substr] = append(s.nth[substr], n)
}

// nullRowOnce 注册一次性返回 NULL 行集。
func (s *w14lScript) nullRowOnce(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nulls[substr]++
}

// nullRow 注册持续返回 NULL 行集。
func (s *w14lScript) nullRow(substr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nulls[substr] = -1
}

// take 返回该查询命中的故障。
func (s *w14lScript) take(query string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for substr, queue := range s.once {
		if strings.Contains(query, substr) && len(queue) > 0 {
			err := queue[0]
			s.once[substr] = queue[1:]
			if len(s.once[substr]) == 0 {
				delete(s.once, substr)
			}
			return err
		}
	}
	for substr, err := range s.rules {
		if strings.Contains(query, substr) {
			return err
		}
	}
	for substr, targets := range s.nth {
		if strings.Contains(query, substr) {
			s.hits[substr]++
			for i, n := range targets {
				if n == s.hits[substr] {
					s.nth[substr] = append(targets[:i], targets[i+1:]...)
					return w14lInjected
				}
			}
		}
	}
	return nil
}

// wantNullRows 返回该查询是否应返回 NULL 行集。
func (s *w14lScript) wantNullRows(query string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for substr, remaining := range s.nulls {
		if strings.Contains(query, substr) && (remaining < 0 || remaining > 0) {
			if remaining > 0 {
				s.nulls[substr] = remaining - 1
				if s.nulls[substr] == 0 {
					delete(s.nulls, substr)
				}
			}
			return true
		}
	}
	return false
}

// w14lNullRows 返回一行全 NULL 再 EOF；非可空目标的 Scan 必然失败。
type w14lNullRows struct {
	cols  []string
	sent  bool
}

func (r *w14lNullRows) Columns() []string { return r.cols }
func (r *w14lNullRows) Close() error      { return nil }
func (r *w14lNullRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	for i := range dest {
		dest[i] = nil
	}
	return nil
}

// w14lConnector 包装 sqlite connector，在物理连接上叠加故障脚本。
type w14lConnector struct {
	base   driver.Connector
	script *w14lScript
}

func (c w14lConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w14lConn{base: conn, script: c.script}, nil
}

func (c w14lConnector) Driver() driver.Driver { return c.base.Driver() }

// w14lConn 只在 ExecContext/QueryContext 上注入；Prepare/Begin 直通，
// 事务边界交给 w14lTx。
type w14lConn struct {
	base   driver.Conn
	script *w14lScript
}

func (c *w14lConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w14lConn) Close() error                              { return c.base.Close() }

func (c *w14lConn) Begin() (driver.Tx, error) {
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w14lTx{base: tx, script: c.script}, nil
}

func (c *w14lConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var (
		tx  driver.Tx
		err error
	)
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err = bt.BeginTx(ctx, opts)
	} else {
		tx, err = c.base.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &w14lTx{base: tx, script: c.script}, nil
}

func (c *w14lConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.script.take(query); err != nil {
		return nil, err
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w14lConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.script.take(query); err != nil {
		return nil, err
	}
	if c.script.wantNullRows(query) {
		return &w14lNullRows{cols: []string{"w14l_c1"}}, nil
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// w14lTx 在 Commit/Rollback 上叠加故障脚本。
type w14lTx struct {
	base   driver.Tx
	script *w14lScript
}

func (t *w14lTx) Commit() error {
	if err := t.script.take(w14lCommitKey); err != nil {
		// 注入的提交失败不会到达底层：回滚底层事务，保持共享连接干净。
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *w14lTx) Rollback() error {
	if err := t.script.take(w14lRollbackKey); err != nil {
		return err
	}
	return t.base.Rollback()
}

// w14lFixture 是仅 store 级的故障注入 fixture：物理连接经过故障脚本，
// 建库与 seed（脚本尚未注册规则）不受影响。
type w14lFixture struct {
	t       *testing.T
	db      *sql.DB
	store   *Store
	script  *w14lScript
	owner   string
	created []string
}

func newW14LFaultFixture(t *testing.T) *w14lFixture {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	base, err := sqlite.NewConnector("file:accounts-w14l-" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := newW14LScript()
	db := sql.OpenDB(w14lConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range schemaStatements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range testFamilySchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &w14lFixture{t: t, db: db, store: store, script: script, owner: "w14l-owner"}
	fixture.seedBase()
	return fixture
}

func (f *w14lFixture) exec(statement string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(statement, args...); err != nil {
		f.t.Fatal(err)
	}
}

// seedBase 写入 gpt 供应商 + 协议档案 + 属主默认分组，并直接落一行账户。
func (f *w14lFixture) seedBase() {
	f.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	f.exec(`INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-openai-compat', 'openai', 'OpenAI 兼容', 1, '["gpt-4o-mini"]', ?, ?)`, now, now)
	f.exec(`INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_openai_openai_v1', 'openai', 'OpenAI 兼容协议', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'gpt-4o-mini', '["api_key","oauth"]', '[]', ?, ?)`, now, now)
	f.exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-gpt-w14l', 'gpt', 'gpt', 1, ?, ?)`, now, now)
	f.exec(`INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-gpt', 'gpt', 'gpt 协议', 1, 'openai', 'v1', 'https://api.openai.com/v1',
		'gpt-4o-mini', '["api_key","oauth"]', '[]', ?, ?)`, now, now)
	f.exec(`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-w14l-default', ?, '默认分组', 'gpt', 1, 1, 'personal', ?, ?)`, f.owner, now, now)
}

// seedRowAccount 直接插入一行账户（等价 env.seedAccount）。
func (f *w14lFixture) seedRowAccount(id, name, status string) {
	f.t.Helper()
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-seeded-" + id})
	if err != nil {
		f.t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	f.exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', ?, ?, 'sk-see***', 'gpt-4o-mini', ?, ?)`,
		id, f.owner, name, status, sealed, now, now)
}

// createViaStore 用真实 Create 流程建账户（脚本此时无规则，必然成功）。
func (f *w14lFixture) createViaStore(name string) *CreateResult {
	f.t.Helper()
	result, err := f.store.Create(context.Background(), w14lCreateInput(name), f.scope())
	if err != nil {
		f.t.Fatal(err)
	}
	f.created = append(f.created, result.ID)
	return result
}

// accountIDs 返回 setup 阶段经 Create 建立的账户 ID。
func (f *w14lFixture) accountIDs() []string { return f.created }

// firstID 返回第一个建立账户的 ID。
func (f *w14lFixture) firstID() string {
	if len(f.created) == 0 {
		f.t.Fatal("fixture 没有经 Create 建立的账户")
	}
	return f.created[0]
}

// secondID 返回第二个建立账户的 ID。
func (f *w14lFixture) secondID() string {
	if len(f.created) < 2 {
		f.t.Fatal("fixture 建立账户不足两个")
	}
	return f.created[1]
}

func (f *w14lFixture) scope() AccessScope {
	return AccessScope{ViewerID: f.owner, IsAdmin: true}
}

// w14lCreateInput 构造合法的 Create 输入（对齐 catalogWiringCreateInput）。
func w14lCreateInput(name string) CreateInput {
	return CreateInput{
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "prof-gpt",
		Name:                      name,
		AccountType:               "api_key",
		Credentials: Credentials{
			"api_key":  "sk-w14l-secret-0000000001",
			"base_url": "https://api.openai.com/v1",
		},
		SupportedModels: []string{"gpt-4o-mini"},
		Status:          CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
	}
}
