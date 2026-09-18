package modelcheckowner

// w14m 覆盖率补强：business_source 的 supportedModels/fence 查询错误臂、
// 授权目标解析数据变体（空 upstream 映射、非法凭据类型、端点模式校验、
// 凭据 BaseURL 覆盖、代理配置错误）与 BuildRequest 冻结重读错误臂。
// 复用 w13g2 的 SQLite 失败注入思路，新增按匹配次序生效的阈值驱动，
// 用以命中"冻结期间变化"这类二次读取臂。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	sqlite "modernc.org/sqlite"
)

var errW14mInjected = errors.New("w14m: injected failure")

// w14mFailpoint 记录各 pattern 的已匹配次数与生效阈值；threshold<=0 表示立即生效。
type w14mFailpoint struct {
	mu        sync.Mutex
	threshold map[string]int
	counts    map[string]int
	armed     map[string]bool
	mode      map[string]string // "query" | "scan" | "nexterr"
	failCommitsFrom int // 从第 N 次 Commit 开始失败；0 表示不失败
	commits   int
	failBeginsFrom  int // 从第 N 次 BeginTx 开始失败；0 表示不失败
	begins    int
}

func (fp *w14mFailpoint) arm(pattern string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.armed[pattern] = true
	fp.mode[pattern] = "query"
	delete(fp.threshold, pattern)
}

func (fp *w14mFailpoint) armAfter(pattern string, matches int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.armed[pattern] = true
	fp.mode[pattern] = "query"
	fp.threshold[pattern] = matches
}

func (fp *w14mFailpoint) armScan(pattern string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.armed[pattern] = true
	fp.mode[pattern] = "scan"
}

// armScan1 用单列假行替换结果集，使 QueryRow(...).Scan(&v) 能成功扫描到
// 固定值 "w14m-model"，用于命中"返回值与预期不一致"的比较分支。
func (fp *w14mFailpoint) armScan1(pattern string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.armed[pattern] = true
	fp.mode[pattern] = "scan1"
}

// armNextErr 让匹配查询放行真实数据但在迭代结束时报错，命中 rows.Err() 臂。
func (fp *w14mFailpoint) armNextErr(pattern string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.armed[pattern] = true
	fp.mode[pattern] = "nexterr"
}

func (fp *w14mFailpoint) disarm() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.armed = map[string]bool{}
	fp.threshold = map[string]int{}
	fp.mode = map[string]string{}
	fp.counts = map[string]int{}
	fp.failCommitsFrom = 0
	fp.failBeginsFrom = 0
	fp.commits = 0
	fp.begins = 0
}

func (fp *w14mFailpoint) hit(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	matched := fp.matchLocked(query)
	if matched == "" || fp.mode[matched] != "query" {
		return false
	}
	fp.counts[matched]++
	threshold := fp.threshold[matched]
	return threshold <= 0 || fp.counts[matched] > threshold
}

func (fp *w14mFailpoint) matchLocked(query string) string {
	for pattern := range fp.armed {
		if strings.Contains(query, pattern) {
			return pattern
		}
	}
	return ""
}

func (fp *w14mFailpoint) scanHit(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	matched := fp.matchLocked(query)
	return matched != "" && fp.mode[matched] == "scan"
}

func (fp *w14mFailpoint) modeHit(query, mode string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	matched := fp.matchLocked(query)
	return matched != "" && fp.mode[matched] == mode
}

func (fp *w14mFailpoint) nextErrHit(query string) bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	matched := fp.matchLocked(query)
	return matched != "" && fp.mode[matched] == "nexterr"
}

// newW14mFailpoint 返回初始化完成的 failpoint。
func newW14mFailpoint() *w14mFailpoint {
	return &w14mFailpoint{
		threshold: map[string]int{}, counts: map[string]int{},
		armed: map[string]bool{}, mode: map[string]string{},
	}
}

func (fp *w14mFailpoint) beginGate() error {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.begins++
	if fp.failBeginsFrom > 0 && fp.begins >= fp.failBeginsFrom {
		return errW14mInjected
	}
	return nil
}

func (fp *w14mFailpoint) commitGate() error {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.commits++
	if fp.failCommitsFrom > 0 && fp.commits >= fp.failCommitsFrom {
		return errW14mInjected
	}
	return nil
}

type w14mFakeRows struct{ sent bool }

func (r *w14mFakeRows) Columns() []string { return []string{"c0", "ghost"} }

func (r *w14mFakeRows) Close() error { return nil }

func (r *w14mFakeRows) Next(dest []driver.Value) error {
	if r.sent {
		return context.Canceled
	}
	r.sent = true
	if len(dest) > 0 {
		dest[0] = "w14m-model"
	}
	return nil
}

// w14mFakeRows1 提供单列假行，专门喂给单目的 Scan。
type w14mFakeRows1 struct{ sent bool }

func (r *w14mFakeRows1) Columns() []string { return []string{"name"} }

func (r *w14mFakeRows1) Close() error { return nil }

func (r *w14mFakeRows1) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	if len(dest) > 0 {
		dest[0] = "w14m-model"
	}
	return nil
}

type w14mFailDriver struct {
	fp    *w14mFailpoint
	inner driver.Driver // 为空时回退到 SQLite 驱动
}

func (d *w14mFailDriver) Open(name string) (driver.Conn, error) {
	inner := d.inner
	if inner == nil {
		inner = &sqlite.Driver{}
	}
	raw, err := inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &w14mFailConn{Conn: raw, fp: d.fp}, nil
}

type w14mFailConn struct {
	driver.Conn
	fp *w14mFailpoint
}

func (c *w14mFailConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.fp.beginGate(); err != nil {
		return nil, err
	}
	beginTx, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	tx, err := beginTx.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &w14mFailTx{Tx: tx, fp: c.fp}, nil
}

func (c *w14mFailConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.fp.nextErrHit(query) {
		queryer, ok := c.Conn.(driver.QueryerContext)
		if !ok {
			return nil, driver.ErrSkip
		}
		rows, err := queryer.QueryContext(ctx, query, args)
		if err != nil {
			return nil, err
		}
		return &w14mErrOnEOFRows{Rows: rows}, nil
	}
	if c.fp.modeHit(query, "scan") {
		return &w14mFakeRows{}, nil
	}
	if c.fp.modeHit(query, "scan1") {
		return &w14mFakeRows1{}, nil
	}
	if c.fp.hit(query) {
		return nil, errW14mInjected
	}
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

// w14mErrOnEOFRows 放行真实数据，把正常 EOF 替换为错误以命中 rows.Err() 臂。
type w14mErrOnEOFRows struct{ driver.Rows }

func (r *w14mErrOnEOFRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == io.EOF {
		return errW14mInjected
	}
	return err
}

func (c *w14mFailConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.fp.hit(query) {
		return nil, errW14mInjected
	}
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

type w14mFailTx struct {
	driver.Tx
	fp *w14mFailpoint
}

func (t *w14mFailTx) Commit() error {
	if err := t.fp.commitGate(); err != nil {
		_ = t.Tx.Rollback()
		return err
	}
	return t.Tx.Commit()
}

var w14mSharedFP *w14mFailpoint
var w14mFailDriverOnce sync.Once
var w14mDBSequence int64

func w14mSharedFailpoint() *w14mFailpoint {
	w14mFailDriverOnce.Do(func() {
		w14mSharedFP = newW14mFailpoint()
		sql.Register("w14mfail", &w14mFailDriver{fp: w14mSharedFP})
		w14mPgFP = newW14mFailpoint()
		sql.Register("w14mfailpg", &w14mFailDriver{inner: stdlib.GetDefaultDriver(), fp: w14mPgFP})
	})
	return w14mSharedFP
}

// w14mPgFP 挂在 pgx stdlib 驱动之上的失败注入点（PostgreSQL 门控测试用）。
var w14mPgFP *w14mFailpoint

func w14mFailDB(t *testing.T, ddl []string) (*sql.DB, *w14mFailpoint) {
	t.Helper()
	fp := w14mSharedFailpoint()
	w14mDBSequence++
	name := fmt.Sprintf("%s-%d", strings.ReplaceAll(t.Name(), "/", "-"), w14mDBSequence)
	db, err := sql.Open("w14mfail", "file:w14mfail-"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	// 不限连接数：ListAccountOptions 在外层 rows 未关闭时会发起内层查询，
	// 单连接会造成自等待死锁。
	t.Cleanup(func() { _ = db.Close(); fp.disarm() })
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 DDL %q 失败: %v", statement, err)
		}
	}
	return db, fp
}

func w14mBusinessDB(t *testing.T) (*sql.DB, *w14mFailpoint) {
	t.Helper()
	return w14mFailDB(t, businessSourceContractDDL())
}

func w14mSeedPlainAccount(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	w11eSeedAccount(t, db, id)
}

func TestW14MSupportedModelsErrorArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	request := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}

	// supportedModels 查询失败（首个 ORDER BY model 匹配即 supportedModels）。
	fp.arm("WHERE account_id=? ORDER BY model")
	if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "read J3b Business account supported models") {
		t.Fatalf("supported models 查询失败应传播: %v", err)
	}
	fp.disarm()

	// supportedModels 行扫描列数不匹配。
	fp.armScan("WHERE account_id=? ORDER BY model")
	if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "scan J3b Business account supported model") {
		t.Fatalf("supported models 扫描失败应传播: %v", err)
	}
	fp.disarm()
}

func TestW14MReadTargetFenceQueryErrorArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	command := RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}

	cases := []struct {
		name    string
		pattern string
		need    string
	}{
		{"account", "a LEFT JOIN", "read J3b Business target fence account"},
		{"groups", "ON ra.resource_type", "read J3b Business target fence groups"},
		{"authorizations", "OR (grantee_system_account_id=", "read J3b Business target fence authorizations"},
		{"mappings", "ORDER BY source_model", "read J3b Business target fence mappings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp.arm(tc.pattern)
			defer fp.disarm()
			_, err := source.BuildRequest(ctx, "sys-1", command)
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
		})
	}

	// fence 内 supported-models 查询：第 2 次匹配（fence）失败，第 1 次（resolve）放行。
	fp.armAfter("ORDER BY model", 1)
	if _, err := source.BuildRequest(ctx, "sys-1", command); err == nil || !strings.Contains(err.Error(), "fence supported models") {
		t.Fatalf("fence supported models 查询失败应传播: %v", err)
	}
	fp.disarm()
}

func TestW14MBuildRequestFreezeRecheckErrorArms(t *testing.T) {
	ctx := context.Background()
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	command := RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}

	// 冻结重读（第二次 Resolve）中 supported-models 查询失败 → target changed。
	fp.armAfter("ORDER BY model", 2)
	if _, err := source.BuildRequest(ctx, "sys-1", command); err == nil || !strings.Contains(err.Error(), "target changed while freezing") {
		t.Fatalf("冻结重读失败应报 target changed: %v", err)
	}
	fp.disarm()

	// 冻结后第二次 fence 读取失败 → fence changed。
	fp.armAfter("ORDER BY model", 3)
	if _, err := source.BuildRequest(ctx, "sys-1", command); err == nil || !strings.Contains(err.Error(), "target fence changed while freezing") {
		t.Fatalf("二次 fence 失败应报 fence changed: %v", err)
	}
	fp.disarm()
}

func TestW14MBuildRequestPolicyErrorArms(t *testing.T) {
	ctx := context.Background()
	command := RunCommand{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"}
	cases := []struct {
		name       string
		commits    int
		begins     int
		need       string
	}{
		{"fenceCommit", 2, 0, "commit J3b Business target fence"},
		{"policyCommit", 3, 0, "commit J3b Business policy read"},
		{"policyBegin", 0, 3, "open J3b Business policy transaction"},
		{"fenceBegin", 0, 2, "open J3b Business target fence transaction"},
		{"policyChanged", 6, 0, "policy changed while freezing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, fp := w14mBusinessDB(t)
			w14mSeedPlainAccount(t, db, "acct-1")
			source := w11eNewSource(t, db)
			fp.failCommitsFrom = tc.commits
			fp.failBeginsFrom = tc.begins
			_, err := source.BuildRequest(ctx, "sys-1", command)
			if err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err = %v, want 包含 %q", err, tc.need)
			}
			fp.disarm()
		})
	}
}

func TestW14MBuildRequestEmptyActorArm(t *testing.T) {
	db, _ := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	source := w11eNewSource(t, db)
	if _, err := source.BuildRequest(context.Background(), "  ", RunCommand{TargetID: "acct-1", Model: "m"}); err == nil || !strings.Contains(err.Error(), "actor is required") {
		t.Fatalf("空 actor 必须拒绝: %v", err)
	}
}

func TestW14MSameTargetFenceCompareArms(t *testing.T) {
	base := Target{Endpoint: "https://a", SupportedModels: []string{"m1", "m2"}}
	// 集合差异：left 有 right 没有的值。
	left := base
	right := base
	right.SupportedModels = []string{"m1"}
	if sameTargetFence(left, right) || sameTargetFence(Target{}, Target{Headers: map[string][]string{"A": {"1"}}}) {
		t.Fatal("fence 差异必须检出")
	}
	// header 键缺失与值不同。
	a := map[string][]string{"A": {"1"}, "B": {"2"}}
	if sameHeaderValues(a, map[string][]string{"A": {"1"}}) {
		t.Fatal("缺失键必须检出")
	}
	if sameHeaderValues(a, map[string][]string{"A": {"1"}, "B": {"3"}}) {
		t.Fatal("不同值必须检出")
	}
	// 空白值不计入集合。
	if !sameStringSet([]string{" m1 ", ""}, []string{"m1"}) {
		t.Fatal("空白值应忽略")
	}
}

// w14mSeedAuthorizedFixture 在故障注入库上复刻授权实例最小数据集。
func w14mSeedAuthorizedFixture(t *testing.T, db *sql.DB, sourceEnvelope string) {
	t.Helper()
	statements := []string{
		`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1,'https://example.invalid/v1')`,
		`INSERT INTO groups VALUES ('group-1','sys-1',1)`,
		`INSERT INTO resource_authorizations VALUES ('grant-1','account','acct-source','sys-1','sys-1','use','active',NULL)`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted) VALUES ('acct-source','sys-1','openai','profile_openai_openai_v1','openai','api_key',3,8,'active',1,'responses_sse','` + sourceEnvelope + `')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,enabled) VALUES ('acct-source','sys-1','group-1',1)`,
		`INSERT INTO accounts(id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,type,config_revision,dispatch_revision,status,schedulable,health_check_endpoint_mode,credentials_encrypted,authorization_instance_authorization_id,authorization_instance_source_account_id) VALUES ('acct-virtual','sys-1','openai','profile_openai_openai_v1','openai','api_key',5,6,'active',1,'responses_sse','` + sourceEnvelope + `','grant-1','acct-source')`,
		`INSERT INTO group_accounts(account_id,system_account_id,group_id,account_authorization_id,enabled) VALUES ('acct-virtual','sys-1','group-1','grant-1',1)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 %q 失败: %v", statement, err)
		}
	}
}

func TestW14MAuthorizedTargetErrorArms(t *testing.T) {
	ctx := context.Background()
	request := RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-virtual", Model: "gpt-5.6-sol"}

	t.Run("mappingQueryError", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`))
		source := w11eNewSource(t, db)
		fp.arm("WHERE account_id=? AND source_model=?")
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "read J3b account model mapping") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("modelRestriction", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`))
		if _, err := db.Exec(`INSERT INTO account_supported_models VALUES ('acct-source','other-model')`); err != nil {
			t.Fatal(err)
		}
		source := w11eNewSource(t, db)
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "model restriction does not allow model") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("supportedModelsError", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`))
		source := w11eNewSource(t, db)
		fp.arm("WHERE account_id=? ORDER BY model")
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "read J3b Business account supported models") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unsupportedCredentialType", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`))
		if _, err := db.Exec(`UPDATE accounts SET type='sso' WHERE id='acct-source'`); err != nil {
			t.Fatal(err)
		}
		source := w11eNewSource(t, db)
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "credential type is unsupported") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("endpointModeMismatch", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["chat_json"]}`))
		source := w11eNewSource(t, db)
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "endpoint_mode") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("credentialBaseURL", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"],"base_url":"https://cred.example.invalid/v1"}`))
		source := w11eNewSource(t, db)
		defer fp.disarm()
		target, err := source.Resolve(ctx, request)
		if err != nil || target.Endpoint != "https://cred.example.invalid/v1" {
			t.Fatalf("target = (%+v, %v)", target.Endpoint, err)
		}
	})

	t.Run("proxyUnavailable", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`))
		if _, err := db.Exec(`INSERT INTO proxy_profiles VALUES ('proxy-broken',0,'http','127.0.0.1',1,'','')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id='proxy-broken' WHERE id='acct-source'`); err != nil {
			t.Fatal(err)
		}
		source := w11eNewSource(t, db)
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "proxy profile is unavailable") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("proxyUnsupportedProtocol", func(t *testing.T) {
		db, fp := w14mBusinessDB(t)
		w14mSeedAuthorizedFixture(t, db, testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"]}`))
		if _, err := db.Exec(`INSERT INTO proxy_profiles VALUES ('proxy-socks6',1,'socks6','127.0.0.1',1,'','')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id='proxy-socks6' WHERE id='acct-source'`); err != nil {
			t.Fatal(err)
		}
		source := w11eNewSource(t, db)
		defer fp.disarm()
		if _, err := source.Resolve(ctx, request); err == nil || !strings.Contains(err.Error(), "proxy protocol is unsupported") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestW14MPlainCredentialBaseURLOverride(t *testing.T) {
	db, fp := w14mBusinessDB(t)
	w14mSeedPlainAccount(t, db, "acct-1")
	if _, err := db.Exec(`UPDATE accounts SET credentials_encrypted=? WHERE id='acct-1'`,
		testCredentialEnvelope(t, "secret", `{"api_key":"key","supported_endpoint_modes":["responses_sse"],"base_url":"https://plain.example.invalid/v1"}`)); err != nil {
		t.Fatal(err)
	}
	source := w11eNewSource(t, db)
	defer fp.disarm()
	target, err := source.Resolve(context.Background(), RunRequest{SystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6-sol"})
	if err != nil || target.Endpoint != "https://plain.example.invalid/v1" {
		t.Fatalf("target = (%+v, %v)", target.Endpoint, err)
	}
}
