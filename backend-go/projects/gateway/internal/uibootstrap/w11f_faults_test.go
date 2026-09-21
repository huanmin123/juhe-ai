package uibootstrap

// w11f 覆盖波次：补 query 校验、self/admin scope、行解析与错误分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

var w11fBoom = errors.New("w11f boom")

// ---------------------------------------------------------------------------
// 故障脚本（同型连接层注入）
// ---------------------------------------------------------------------------

type w11fRule struct {
	substr   string
	queryErr error
	cols     []string
	rows     [][]driver.Value
	nextErr  error
	limit    int
	hits     int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w11fScript struct {
	mu    sync.Mutex
	t     *testing.T
	rules []*w11fRule
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

func (s *w11fScript) failQuery(substr string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w11fRule{substr: substr, queryErr: w11fBoom}
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) canned(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr}
	s.rules = append(s.rules, r)
	return r
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
func (c *w11fConn) Begin() (driver.Tx, error)                 { return c.base.Begin() }

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

func (c *w11fConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

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

// w11fFaultFixture 在故障脚本连接上重建 fixture。
func w11fFaultFixture(t *testing.T) (*Deps, *w11fScript) {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(bootstrapSchema); err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('w11f-sa', 'w11f-user', 'W11F')`,
		`INSERT INTO groups (id, name, system_account_id, provider_code, enabled, is_default) VALUES
			('w11f-g1', 'w11f-openai组', 'w11f-sa', 'openai', 1, 1),
			('w11f-g2', 'w11f-gpt组', 'w11f-sa', 'gpt', 1, 1)`,
		`INSERT INTO route_strategies (id, name, system_account_id, mode, status, is_default, created_at) VALUES
			('w11f-rs1', 'w11f-策略一', 'w11f-sa', 'normal', 'active', 1, '2026-01-01')`,
		`INSERT INTO route_strategy_groups (route_strategy_id, system_account_id, group_id, status) VALUES
			('w11f-rs1', 'w11f-sa', 'w11f-g2', 'active')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return &Deps{DB: db, PGDialect: false, Auth: nil}, script
}

func TestW11FBootstrapScopesAndErrors(t *testing.T) {
	deps, script := w11fFaultFixture(t)

	// 空 systemAccountId → 400。
	recorder := invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=%20", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "系统账号 ID 不能为空") {
		t.Fatalf("blank scope = %d %s", recorder.Code, recorder.Body.String())
	}
	// systemAccountId=all 等价无 scope → admin 仍 400。
	recorder = invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=all", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "请选择目标系统账户") {
		t.Fatalf("all scope = %d %s", recorder.Code, recorder.Body.String())
	}
	// gpt 供应商 + enabled 组 + active 绑定 → preferred 命中。
	recorder = invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=w11f-sa", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("scoped read = %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"preferredDefaultRouteStrategy"`) || !strings.Contains(body, "w11f-rs1") {
		t.Fatalf("preferred missing: %s", body)
	}
	// self 面: 普通 user 读取自身。
	userAuth := &authsys.AuthContext{SystemAccountID: "w11f-sa", Username: "w11f-user", Role: "user"}
	recorder = invokeBootstrap(t, deps, true, "/__aisys__/api/my-ui-bootstrap/options", userAuth)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self read = %d %s", recorder.Code, recorder.Body.String())
	}
	// 不存在的账户 → 404。
	recorder = invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=ghost", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing account = %d %s", recorder.Code, recorder.Body.String())
	}
	// 存储错误 → 500。
	script.failQuery("FROM system_accounts")
	recorder = invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=w11f-sa", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("query fault = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW11FBootstrapRowBranches(t *testing.T) {
	deps, script := w11fFaultFixture(t)

	// 空 owner → nil。
	if reference, err := deps.findUserReferenceData(context.Background(), "  "); err != nil || reference != nil {
		t.Fatalf("blank owner = %v/%v", reference, err)
	}
	// 查询错误 / Scan 错误 / rows.Err。
	script.failQuery("FROM system_accounts")
	if _, err := deps.findUserReferenceData(context.Background(), "w11f-sa"); err == nil {
		t.Fatal("query fault must fail")
	}
	cols := []string{"system_account_id", "provider_code", "group_id", "group_name", "group_enabled",
		"route_strategy_id", "route_strategy_name", "route_strategy_mode", "route_strategy_status", "route_binding_status"}
	script.canned("FROM system_accounts", cols, [][]driver.Value{{"sa", "p", "g", "n", "notabool", nil, nil, nil, nil, nil}}, nil)
	if _, err := deps.findUserReferenceData(context.Background(), "w11f-sa"); err == nil {
		t.Fatal("scan fault must fail")
	}
	script.canned("FROM system_accounts", cols, [][]driver.Value{{"sa", nil, nil, nil, nil, nil, nil, nil, nil, nil}}, w11fBoom)
	if _, err := deps.findUserReferenceData(context.Background(), "w11f-sa"); err == nil {
		t.Fatal("rows.Err fault must fail")
	}
	// 空行（无 provider）→ reference nil（sawRow=true 但跳过；此处罐装行主键存在）。
	script.canned("FROM system_accounts", cols, [][]driver.Value{{"sa", nil, nil, nil, nil, nil, nil, nil, nil, nil}}, nil)
	reference, err := deps.findUserReferenceData(context.Background(), "w11f-sa")
	if err != nil || reference == nil || reference.SystemAccountID != "w11f-sa" {
		t.Fatalf("null provider row = %v/%v", reference, err)
	}
	if len(reference.ProviderDefaults) != 0 {
		t.Fatalf("provider defaults = %+v", reference.ProviderDefaults)
	}

	// routeStrategyRef 分支：缺 name / 缺 mode / 缺 status / 完整。
	if routeStrategyRef(sqlNull("rs"), sqlNull(""), sqlNull("normal"), sqlNull("active")) != nil {
		t.Fatal("blank name must be nil")
	}
	if routeStrategyRef(sqlNull("rs"), sqlNull("n"), sqlNullString(false), sqlNull("active")) != nil {
		t.Fatal("invalid mode must be nil")
	}
	if routeStrategyRef(sqlNull("rs"), sqlNull("n"), sqlNull("normal"), sqlNullString(false)) != nil {
		t.Fatal("invalid status must be nil")
	}
	if ref := routeStrategyRef(sqlNull("rs"), sqlNull("n"), sqlNull("normal"), sqlNull("active")); ref == nil || ref.ID != "rs" || ref.Mode != "normal" {
		t.Fatalf("valid ref = %+v", ref)
	}
	// table 的 PG 方言 arm。
	if got := (&Deps{PGDialect: true}).table("x"); got != "juhe_business.x" {
		t.Fatalf("pg table = %q", got)
	}
	// requestScope / selfScope 无 auth 上下文。
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if scope := requestScope(request); scope.IsAdmin || scope.ViewerID != "" {
		t.Fatal("requestScope anonymous drift")
	}
	if scope := selfScope(request); scope.IsAdmin || scope.ViewerID != "" {
		t.Fatal("selfScope anonymous drift")
	}
}

func sqlNull(v string) sql.NullString         { return sql.NullString{String: v, Valid: true} }
func sqlNullString(valid bool) sql.NullString { return sql.NullString{Valid: valid} }
