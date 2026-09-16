package modelchecksource

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
)

// w12a_reader_fakes_test.go 用脚本化 database/sql 驱动驱动 PostgresReader 的
// 全部分支（契约检查、冻结、管理作用域、Resolve 回放），并以 w1cover 覆盖库
// 门禁验证管理作用域的真实 PG 臂。连接串永不进入日志或断言。

// ---- 脚本化驱动 ----

type w12aFakeState struct {
	mu          sync.Mutex
	execErr     error
	execErrByFrag map[string]error
	commitErr   error
	beginErr    error
	queryFn     func(query string, args []driver.NamedValue) (driver.Rows, error)
}

type w12aFakeConnector struct{ state *w12aFakeState }

func (c *w12aFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &w12aFakeConn{state: c.state}, nil
}
func (c *w12aFakeConnector) Driver() driver.Driver { return w12aFakeDriver{} }

type w12aFakeDriver struct{}

func (w12aFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w12a fake driver requires a connector")
}

type w12aFakeConn struct{ state *w12aFakeState }

func (c *w12aFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w12a fake driver handles queries directly")
}
func (c *w12aFakeConn) Close() error              { return nil }
func (c *w12aFakeConn) Begin() (driver.Tx, error) { return c.state.begin() }

// BeginTx 让驱动满足 database/sql 的只读与隔离级别要求。
func (c *w12aFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.state.begin()
}

func (s *w12aFakeState) begin() (driver.Tx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return w12aFakeTx{state: s}, nil
}

func (c *w12aFakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	for fragment, err := range c.state.execErrByFrag {
		if strings.Contains(query, fragment) {
			return driver.RowsAffected(0), err
		}
	}
	if c.state.execErr != nil {
		return driver.RowsAffected(0), c.state.execErr
	}
	return driver.RowsAffected(0), nil
}

func (c *w12aFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	queryFn := c.state.queryFn
	c.state.mu.Unlock()
	if queryFn != nil {
		return queryFn(query, args)
	}
	return &w12aFakeRows{}, nil
}

type w12aFakeTx struct{ state *w12aFakeState }

func (t w12aFakeTx) Commit() error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	return t.state.commitErr
}
func (t w12aFakeTx) Rollback() error { return nil }

type w12aFakeRows struct {
	columns  []string
	values   [][]driver.Value
	nextErr  error
	closeErr error
	i        int
}

func (r *w12aFakeRows) Columns() []string { return r.columns }
func (r *w12aFakeRows) Close() error      { return r.closeErr }
func (r *w12aFakeRows) Next(dest []driver.Value) error {
	if r.i < len(r.values) {
		copy(dest, r.values[r.i])
		r.i++
		return nil
	}
	if r.nextErr != nil {
		return r.nextErr
	}
	return io.EOF
}

func w12aOpenFakeDB(t *testing.T, state *w12aFakeState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&w12aFakeConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w12aErrRows(err error) driver.Rows { return &w12aFakeRows{nextErr: err} }

func w12aRows(values ...[]driver.Value) driver.Rows {
	return &w12aFakeRows{values: values}
}

// ---- 候选行构造 ----

const w12aCandidateColumns = 28

func w12aCandidateValues(t *testing.T, secret string) []driver.Value {
	t.Helper()
	envelope, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["w12a-key"],"base_url":"https://w12a.example/v1","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	return []driver.Value{
		"w12a-account", "w12a-sys", "w12a-target", "w12a-sys", int64(7), "active", true, "responses_json",
		"", "w12a-source", int64(3), "openai", "profile_openai_openai_v1", "openai", "v1", "api_key", envelope,
		true, "https://profile.example", "2026-08-27T00:00:00Z", "w12a-group",
		nil, nil, nil, nil, nil, nil, nil,
	}
}

func w12aScriptedReader(t *testing.T, secret, identity string, queryFn func(string, []driver.NamedValue) (driver.Rows, error)) (*PostgresReader, *w12aFakeState) {
	t.Helper()
	state := &w12aFakeState{queryFn: queryFn}
	db := w12aOpenFakeDB(t, state)
	reader, err := NewPostgresReader(db, secret, identity, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return reader, state
}

func w12aDispatch(secret string, candidate []driver.Value, extra func(query string) (driver.Rows, error)) func(string, []driver.NamedValue) (driver.Rows, error) {
	return func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if extra != nil {
			if rows, err := extra(query); rows != nil || err != nil {
				return rows, err
			}
		}
		if strings.Contains(query, "EXPLAIN") {
			return &w12aFakeRows{columns: []string{"QUERY PLAN"}}, nil
		}
		if strings.Contains(query, "account_supported_models") {
			return &w12aFakeRows{columns: []string{"model"}}, nil
		}
		if strings.Contains(query, "account_model_mappings") {
			return &w12aFakeRows{columns: []string{"source_model"}}, nil
		}
		if strings.Contains(query, "FROM juhe_business.accounts WHERE id=$1") {
			return &w12aFakeRows{columns: []string{"system_account_id"}, values: [][]driver.Value{{"w12a-sys"}}}, nil
		}
		if strings.Contains(query, "a.system_account_id=$1") {
			return &w12aFakeRows{columns: make([]string, w12aCandidateColumns), values: [][]driver.Value{candidate}}, nil
		}
		return &w12aFakeRows{}, nil
	}
}

// ---- CheckContract ----

func TestW12aPostgresCheckContractSuccess(t *testing.T) {
	reader, _ := w12aScriptedReader(t, "w12a-secret", "w12a-identity", w12aDispatch("w12a-secret", nil, nil))
	if err := reader.CheckContract(context.Background()); err != nil {
		t.Fatalf("契约检查不应报错: %v", err)
	}
}

func TestW12aPostgresCheckContractErrorBranches(t *testing.T) {
	if err := (*PostgresReader)(nil).CheckContract(context.Background()); err == nil {
		t.Fatalf("nil 读取器契约检查应报错")
	}
	db := w12aOpenFakeDB(t, &w12aFakeState{})
	if _, err := NewPostgresReader(db, "s", "i", time.Now); err != nil {
		t.Fatal(err)
	}

	reader, _ := w12aScriptedReader(t, "w12a-secret", "w12a-identity", func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "juhe_business.accounts a") && !strings.Contains(query, "EXPLAIN") {
			return nil, errors.New("w12a contract query failure")
		}
		return &w12aFakeRows{}, nil
	})
	if err := reader.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "verify model check reader contract") {
		t.Fatalf("契约查询失败应报错: %v", err)
	}

	reader, _ = w12aScriptedReader(t, "w12a-secret", "w12a-identity", func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "juhe_business.accounts a") {
			return &w12aFakeRows{closeErr: errors.New("w12a contract close failure")}, nil
		}
		return &w12aFakeRows{}, nil
	})
	if err := reader.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "close model check reader contract result") {
		t.Fatalf("契约结果关闭失败应报错: %v", err)
	}

	reader, _ = w12aScriptedReader(t, "w12a-secret", "w12a-identity", func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "EXPLAIN") {
			return nil, errors.New("w12a explain failure")
		}
		return &w12aFakeRows{}, nil
	})
	if err := reader.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "verify model check reader candidate query") {
		t.Fatalf("候选查询校验失败应报错: %v", err)
	}

	reader, _ = w12aScriptedReader(t, "w12a-secret", "w12a-identity", func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "EXPLAIN") {
			return &w12aFakeRows{closeErr: errors.New("w12a plan close failure")}, nil
		}
		return &w12aFakeRows{}, nil
	})
	if err := reader.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "close model check reader candidate plan") {
		t.Fatalf("候选计划关闭失败应报错: %v", err)
	}

	// 提交失败。
	commitState := &w12aFakeState{commitErr: errors.New("w12a commit failure"), queryFn: w12aDispatch("w12a-secret", nil, nil)}
	commitReader, err := NewPostgresReader(w12aOpenFakeDB(t, commitState), "w12a-secret", "w12a-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitReader.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "commit model check reader contract transaction") {
		t.Fatalf("契约提交失败应报错: %v", err)
	}

	// 已取消上下文：事务开启即失败。
	cancelState := &w12aFakeState{}
	cancelReader, err := NewPostgresReader(w12aOpenFakeDB(t, cancelState), "w12a-secret", "w12a-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cancelReader.CheckContract(ctx); err == nil {
		t.Fatalf("取消上下文应使契约检查失败")
	}
}

// ---- FreezeTarget 成功与失败分支 ----

func TestW12aPostgresFreezeTargetSuccessAndResolve(t *testing.T) {
	const secret = "w12a-freeze-secret"
	candidate := w12aCandidateValues(t, secret)
	reader, _ := w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, nil))
	request := Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"}
	frozen, err := reader.FreezeTarget(context.Background(), request)
	if err != nil {
		t.Fatalf("冻结不应报错: %v", err)
	}
	if frozen.TargetName != "w12a-target" || frozen.GroupID != "w12a-group" || frozen.TargetOwnerSystemID != "w12a-sys" {
		t.Fatalf("冻结元数据不符: %#v", frozen)
	}
	if frozen.DurableAccount.ID != "w12a-account" || frozen.DurableAccount.ConfigRevision != "7" {
		t.Fatalf("持久快照不符: %#v", frozen.DurableAccount)
	}
	if frozen.Execution.Endpoint != "https://w12a.example/v1" || frozen.Execution.Model != "gpt-5.6-sol" {
		t.Fatalf("执行快照不符: %#v", frozen.Execution)
	}
	// Resolve 完整回放。
	target, err := reader.Resolve(context.Background(), modelcheckexecutor.ResolutionRequest{
		Input:   modelcheckinput.IssuedInput{SystemAccountID: "w12a-sys", Model: "gpt-5.6-sol", Trigger: modelcheckinput.TriggerManual},
		Account: frozen.DurableAccount,
	})
	if err != nil {
		t.Fatalf("Resolve 不应报错: %v", err)
	}
	if target.Endpoint != "https://w12a.example/v1" || target.Headers.Get("Authorization") != "Bearer w12a-key" || target.Client == nil {
		t.Fatalf("Resolve 目标不符: %#v headers=%#v", target, target.Headers)
	}
	// 过期快照比较分支。
	stale := frozen.DurableAccount
	stale.ConfigRevision = "999"
	if _, err := reader.Resolve(context.Background(), modelcheckexecutor.ResolutionRequest{
		Input:   modelcheckinput.IssuedInput{SystemAccountID: "w12a-sys", Model: "gpt-5.6-sol", Trigger: modelcheckinput.TriggerManual},
		Account: stale,
	}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("过期快照应报错: %v", err)
	}
}

func TestW12aPostgresFreezeTargetValidationAndErrors(t *testing.T) {
	if _, err := (*PostgresReader)(nil).FreezeTarget(context.Background(), Request{}); err == nil {
		t.Fatalf("nil 读取器冻结应报错")
	}
	const secret = "w12a-freeze-validation-secret"
	reader, _ := w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, nil, nil))
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: " "}); err == nil {
		t.Fatalf("请求不完整应报错")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.FreezeTarget(ctx, Request{SystemAccountID: "s", AccountID: "a", Model: "m"}); err == nil {
		t.Fatalf("取消上下文应使冻结失败")
	}

	// 候选不存在（ErrNoRows）。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", func(string, []driver.NamedValue) (driver.Rows, error) {
		return &w12aFakeRows{}, nil
	})
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-ghost", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "does not exist or is not permitted") {
		t.Fatalf("缺失候选应报错: %v", err)
	}
	// 扫描失败（布尔列非法字符串）。
	badBool := w12aCandidateValues(t, secret)
	badBool[6] = "not-a-bool"
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, badBool, nil))
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "read model check account candidate") {
		t.Fatalf("扫描失败应报读取错误: %v", err)
	}
	// 凭据解密失败。
	badCredential := w12aCandidateValues(t, secret)
	badCredential[16] = "w12a-not-an-envelope"
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, badCredential, nil))
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "credentials are unavailable") {
		t.Fatalf("凭据不可用应报错: %v", err)
	}
	// 代理配置损坏：启用但端口缺失。
	withProxy := w12aCandidateValues(t, secret)
	withProxy[21] = "w12a-proxy"
	withProxy[22] = true
	withProxy[23] = "http"
	withProxy[24] = "w12a-host"
	withProxy[25] = int64(0)
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, withProxy, nil))
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "proxy profile is unavailable") {
		t.Fatalf("代理不可用应报错: %v", err)
	}
	// 提交失败。
	candidate := w12aCandidateValues(t, secret)
	state := &w12aFakeState{commitErr: errors.New("w12a freeze commit failure")}
	reader2, err := NewPostgresReader(w12aOpenFakeDB(t, state), secret, "w12a-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	state.queryFn = w12aDispatch(secret, candidate, nil)
	if _, err := reader2.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "commit model check reader transaction") {
		t.Fatalf("冻结提交失败应报错: %v", err)
	}
}

// ---- loadCandidateWithQuery / readSupportedModels / readModelMappings 错误分支 ----

func TestW12aReaderModelAndMappingErrorBranches(t *testing.T) {
	const secret = "w12a-tables-secret"
	candidate := w12aCandidateValues(t, secret)

	// supported models 查询失败。
	reader, _ := w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_supported_models") {
			return nil, errors.New("w12a models query failure")
		}
		return nil, nil
	}))
	_, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "read model check supported models") {
		t.Fatalf("受支持模型查询失败应报错: %v", err)
	}

	// supported models 扫描失败（NULL model）。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_supported_models") {
			return &w12aFakeRows{columns: []string{"model"}, values: [][]driver.Value{{nil}}}, nil
		}
		return nil, nil
	}))
	_, err = reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "scan model check supported model") {
		t.Fatalf("受支持模型扫描失败应报错: %v", err)
	}

	// supported models 迭代失败（rows.Err）。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_supported_models") {
			return &w12aFakeRows{columns: []string{"model"}, values: [][]driver.Value{{"gpt-5.6-sol"}, {"gpt-5.6-min"}}, nextErr: errors.New("w12a models iterate failure")}, nil
		}
		return nil, nil
	}))
	_, err = reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "iterate model check supported models") {
		t.Fatalf("受支持模型迭代失败应报错: %v", err)
	}

	// mappings 查询失败。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_model_mappings") {
			return nil, errors.New("w12a mappings query failure")
		}
		return nil, nil
	}))
	_, err = reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "read model check mappings") {
		t.Fatalf("映射查询失败应报错: %v", err)
	}

	// mappings 扫描失败。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_model_mappings") {
			return &w12aFakeRows{columns: []string{"source_model"}, values: [][]driver.Value{{nil}}}, nil
		}
		return nil, nil
	}))
	_, err = reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "scan model check mapping") {
		t.Fatalf("映射扫描失败应报错: %v", err)
	}

	// mappings 迭代失败。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_model_mappings") {
			return &w12aFakeRows{columns: []string{"a", "b", "c", "d", "e"}, values: [][]driver.Value{{"m", "responses", "up", "responses", true}}, nextErr: errors.New("w12a mappings iterate failure")}, nil
		}
		return nil, nil
	}))
	_, err = reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err == nil || !strings.Contains(err.Error(), "iterate model check mappings") {
		t.Fatalf("映射迭代失败应报错: %v", err)
	}

	// 完整映射行仍可成功冻结。
	reader, _ = w12aScriptedReader(t, secret, "w12a-identity", w12aDispatch(secret, candidate, func(query string) (driver.Rows, error) {
		if strings.Contains(query, "account_supported_models") {
			return &w12aFakeRows{columns: []string{"model"}, values: [][]driver.Value{{"gpt-5.6-sol"}, {"gpt-5.6-min"}}}, nil
		}
		if strings.Contains(query, "account_model_mappings") {
			return &w12aFakeRows{columns: []string{"a", "b", "c", "d", "e"}, values: [][]driver.Value{{"m", "responses", "up", "responses", true}}}, nil
		}
		return nil, nil
	}))
	frozen, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "w12a-sys", AccountID: "w12a-account", Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatalf("带模型与映射的冻结不应报错: %v", err)
	}
	if frozen.DurableAccount.MappedUpstreamModel != "gpt-5.6-sol" {
		t.Fatalf("受支持列表内的模型应原样映射: %#v", frozen.DurableAccount)
	}
}

// ---- ResolveManagementSystemAccount ----

func TestW12aPostgresManagementScopeBranches(t *testing.T) {
	reader, _ := w12aScriptedReader(t, "w12a-scope-secret", "w12a-identity", w12aDispatch("w12a-scope-secret", nil, nil))
	scope, err := reader.ResolveManagementSystemAccount(context.Background(), "w12a-account")
	if err != nil || scope != "w12a-sys" {
		t.Fatalf("管理作用域应为 w12a-sys: %q %v", scope, err)
	}
	if _, err := (*PostgresReader)(nil).ResolveManagementSystemAccount(context.Background(), "w12a-account"); err == nil {
		t.Fatalf("nil 读取器应报错")
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), " "); err == nil {
		t.Fatalf("空账号应报错")
	}
	// 读取失败（非 ErrNoRows）。
	broken, _ := w12aScriptedReader(t, "w12a-scope-secret", "w12a-identity", func(string, []driver.NamedValue) (driver.Rows, error) {
		return nil, errors.New("w12a scope read failure")
	})
	if _, err := broken.ResolveManagementSystemAccount(context.Background(), "w12a-account"); err == nil || !strings.Contains(err.Error(), "read model check management account scope") {
		t.Fatalf("作用域读取失败应报错: %v", err)
	}
	// ErrNoRows。
	missing, _ := w12aScriptedReader(t, "w12a-scope-secret", "w12a-identity", func(string, []driver.NamedValue) (driver.Rows, error) {
		return &w12aFakeRows{}, nil
	})
	if _, err := missing.ResolveManagementSystemAccount(context.Background(), "w12a-ghost"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("缺失账号应报错: %v", err)
	}
	// 空作用域。
	empty, _ := w12aScriptedReader(t, "w12a-scope-secret", "w12a-identity", func(string, []driver.NamedValue) (driver.Rows, error) {
		return &w12aFakeRows{columns: []string{"system_account_id"}, values: [][]driver.Value{{" "}}}, nil
	})
	if _, err := empty.ResolveManagementSystemAccount(context.Background(), "w12a-account"); err == nil || !strings.Contains(err.Error(), "system scope is empty") {
		t.Fatalf("空作用域应报错: %v", err)
	}
	// 提交失败。
	state := &w12aFakeState{commitErr: errors.New("w12a scope commit failure"), queryFn: func(string, []driver.NamedValue) (driver.Rows, error) {
		return &w12aFakeRows{columns: []string{"system_account_id"}, values: [][]driver.Value{{"w12a-sys"}}}, nil
	}}
	reader2, err := NewPostgresReader(w12aOpenFakeDB(t, state), "w12a-scope-secret", "w12a-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader2.ResolveManagementSystemAccount(context.Background(), "w12a-account"); err == nil || !strings.Contains(err.Error(), "commit model check management scope transaction") {
		t.Fatalf("作用域提交失败应报错: %v", err)
	}
	// 已取消上下文。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader2.ResolveManagementSystemAccount(ctx, "w12a-account"); err == nil {
		t.Fatalf("取消上下文应使作用域解析失败")
	}
}

// ---- 直接构造的读取器（覆盖 Resolve 内 resolver.New 失败）----

func TestW12aReadersResolveWithEmptyCredentialSecret(t *testing.T) {
	db := w12aOpenFakeDB(t, &w12aFakeState{})
	// 直接构造绕过构造函数校验，仅用于 Resolve 内 resolver.New 失败分支；
	// now 必须显式注入（生产构造函数已保证非空）。
	pg := &PostgresReader{db: db, identitySecret: "w12a-identity", now: time.Now}
	request := modelcheckexecutor.ResolutionRequest{
		Input:   modelcheckinput.IssuedInput{SystemAccountID: "w12a-sys", Model: "gpt-5.6-sol", Trigger: modelcheckinput.TriggerManual},
		Account: modelcheckinput.AccountSnapshot{ID: "w12a-account"},
	}
	if _, err := pg.Resolve(context.Background(), request); err == nil {
		t.Fatalf("空凭据密钥的 Resolve 应报错")
	}
	sqlite := &SQLiteReader{common: pg, db: db}
	if _, err := sqlite.Resolve(context.Background(), request); err == nil {
		t.Fatalf("空凭据密钥的 SQLite Resolve 应报错")
	}
}

func TestW12aSQLiteReaderTransactionErrorBranches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := w12aOpenFakeDB(t, &w12aFakeState{})
	reader, err := NewSQLiteReader(db, "w12a-secret", "w12a-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.CheckContract(ctx); err == nil {
		t.Fatalf("取消上下文应使 SQLite 契约检查失败")
	}
	if _, err := reader.FreezeTarget(ctx, Request{SystemAccountID: "s", AccountID: "a", Model: "m"}); err == nil {
		t.Fatalf("取消上下文应使 SQLite 冻结失败")
	}
	if _, err := reader.ResolveManagementSystemAccount(ctx, "a"); err == nil {
		t.Fatalf("取消上下文应使 SQLite 作用域解析失败")
	}
	// SQLite 提交失败分支。
	const secret = "w12a-sqlite-commit-secret"
	_ = secret
	state := &w12aFakeState{commitErr: errors.New("w12a sqlite commit failure"), queryFn: func(string, []driver.NamedValue) (driver.Rows, error) {
		return &w12aFakeRows{columns: []string{"system_account_id"}, values: [][]driver.Value{{"w12a-sys"}}}, nil
	}}
	db2 := w12aOpenFakeDB(t, state)
	reader2, err := NewSQLiteReader(db2, "w12a-secret", "w12a-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader2.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "commit model check SQLite reader contract transaction") {
		t.Fatalf("SQLite 契约提交失败应报错: %v", err)
	}
	if _, err := reader2.ResolveManagementSystemAccount(context.Background(), "w12a-account"); err == nil || !strings.Contains(err.Error(), "commit model check SQLite management scope transaction") {
		t.Fatalf("SQLite 作用域提交失败应报错: %v", err)
	}
}

// ---- w1cover 真实 PG 门控 ----

func w12aPgDSN(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("w12a PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w12a PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func TestW12aPostgresManagementScopeOnDevDatabase(t *testing.T) {
	dsn := w12aPgDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skip("w12a PG gated: 打开失败")
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skip("w12a PG gated: PG 不可达")
	}
	cleanup := func(t *testing.T) {
		_, _ = db.Exec(`DELETE FROM juhe_business.accounts WHERE id LIKE 'w12a-%'`)
	}
	cleanup(t)
	t.Cleanup(func() { cleanup(t) })
	fixture, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, deleted_at) VALUES ('w12a-account', 'w12a-sys', NULL) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Skipf("w12a PG gated: fixture 写入失败（schema 差异）: %v", err)
	}
	if rows, _ := fixture.RowsAffected(); rows == 0 {
		cleanup(t)
		if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, deleted_at) VALUES ('w12a-account', 'w12a-sys', NULL)`); err != nil {
			t.Skipf("w12a PG gated: fixture 重写失败: %v", err)
		}
	}
	reader, err := NewPostgresReader(db, "w12a-pg-secret", "w12a-pg-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := reader.ResolveManagementSystemAccount(context.Background(), "w12a-account")
	if err != nil || scope != "w12a-sys" {
		t.Fatalf("真实 PG 管理作用域应为 w12a-sys: %q %v", scope, err)
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), "w12a-ghost"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("真实 PG 缺失账号应报错: %v", err)
	}
}
