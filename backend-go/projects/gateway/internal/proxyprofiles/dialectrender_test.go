package proxyprofiles

// 方言渲染级测试（X05 盲区修复）：列型断裂（enabled boolean / timestamptz）
// 无法靠行为黑盒暴露，本文件用录制驱动捕获 Store 实际发出的
// SQL 文本与绑定参数，按 Node 归档 PG 路径（proxy.repository.ts 的
// createProxyAsync / patchProxyForManagementAsync / listProxyOptionsAsync /
// buildProxyKeywordFilterAsync / proxySummarySelectColumns）逐片段断言。
// 做法与 jobs/internal/cleanuprepo/statssubtractpostgres_test.go 一致：
// 录制驱动 + 测试内独立 bindTestPG 重写占位符，避免与 Store.bind 循环论证。
// 方言坍缩后 Store 恒发 PG 形态，SQLite 对偶断言随开关退役。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 录制驱动 ----

type proxyRecordedStatement struct {
	query string
	args  []driver.Value
}

type proxyScriptedResult struct {
	match   string
	columns []string
	rows    [][]driver.Value
}

type proxyRecorder struct {
	mu         sync.Mutex
	statements []proxyRecordedStatement
	scripted   []proxyScriptedResult
}

func newProxyRecorder() *proxyRecorder { return &proxyRecorder{} }

func (r *proxyRecorder) script(match string, columns []string, rows [][]driver.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripted = append(r.scripted, proxyScriptedResult{match: match, columns: columns, rows: rows})
}

func (r *proxyRecorder) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, proxyRecordedStatement{query: query, args: values})
}

func (r *proxyRecorder) all() []proxyRecordedStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proxyRecordedStatement{}, r.statements...)
}

func (r *proxyRecorder) popScripted(query string) (proxyScriptedResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, item := range r.scripted {
		if strings.Contains(query, item.match) {
			r.scripted = append(r.scripted[:i], r.scripted[i+1:]...)
			return item, true
		}
	}
	return proxyScriptedResult{}, false
}

type proxyRecorderDriver struct{ rec *proxyRecorder }

func (d proxyRecorderDriver) Open(string) (driver.Conn, error) {
	return &proxyRecorderConn{rec: d.rec}, nil
}

type proxyRecorderConnector struct{ rec *proxyRecorder }

func (c proxyRecorderConnector) Connect(context.Context) (driver.Conn, error) {
	return &proxyRecorderConn{rec: c.rec}, nil
}

func (c proxyRecorderConnector) Driver() driver.Driver { return proxyRecorderDriver{rec: c.rec} }

type proxyRecorderConn struct{ rec *proxyRecorder }

func (c *proxyRecorderConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recorder: Prepare 不应被调用（走 QueryerContext/ExecerContext）")
}

func (c *proxyRecorderConn) Close() error { return nil }

func (c *proxyRecorderConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recorder: Begin 不应被调用")
}

func (c *proxyRecorderConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.capture(query, args)
	return driver.RowsAffected(1), nil
}

func (c *proxyRecorderConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	if scripted, ok := c.rec.popScripted(query); ok {
		return &proxyRecordedRows{columns: scripted.columns, values: scripted.rows}, nil
	}
	return &proxyRecordedRows{columns: []string{"value"}}, nil
}

func (c *proxyRecorderConn) CheckNamedValue(value *driver.NamedValue) error {
	switch value.Value.(type) {
	case nil, int64, float64, bool, string, []byte, time.Time:
		return nil
	case int:
		value.Value = int64(value.Value.(int))
		return nil
	}
	return fmt.Errorf("recorder: 不支持的参数类型 %T", value.Value)
}

type proxyRecordedRows struct {
	columns []string
	values  [][]driver.Value
	pos     int
}

func (r *proxyRecordedRows) Columns() []string { return r.columns }

func (r *proxyRecordedRows) Close() error { return nil }

func (r *proxyRecordedRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

// ---- 固定值与装配 ----

// bindTestPG 是测试内独立的 ?→$n 重写器（不复用 Store.bind，避免渲染断言
// 循环论证；与 statssubtractpostgres_test.go 的 bindTestPG 同型）。
func bindTestPG(query string) string {
	var out strings.Builder
	index := 0
	for _, ch := range query {
		if ch == '?' {
			index++
			out.WriteString("$")
			out.WriteString(fmt.Sprintf("%d", index))
			continue
		}
		out.WriteRune(ch)
	}
	return out.String()
}

type proxyRenderFixture struct {
	store *Store
	rec   *proxyRecorder
}

func newProxyRenderFixture(t *testing.T) *proxyRenderFixture {
	t.Helper()
	rec := newProxyRecorder()
	db := sql.OpenDB(proxyRecorderConnector{rec})
	t.Cleanup(func() { _ = db.Close() })
	fixed := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	store, err := NewStore(Deps{
		DB:     db,
		Secret: "test-secret",
		Now:    func() time.Time { return fixed },
		NewID:  func(prefix string) string { return prefix + "_render_1" },
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return &proxyRenderFixture{store: store, rec: rec}
}

// ---- PG 渲染断言 ----

// TestPostgresListOptionsRendersBooleanFilterAndKeyword 照 Node
// listProxyOptionsAsync（proxy.repository.ts:260-277）断言 window 与 selected
// 查询：enabled = true、COLLATE "C" + starts_with 三参窗口、updated_at 经
// to_char 文本化。
func TestPostgresListOptionsRendersBooleanFilterAndKeyword(t *testing.T) {
	fixture := newProxyRenderFixture(t)
	optionColumns := []string{"id", "name", "type", "enabled", "updated_at"}
	fixture.rec.script("LIMIT", optionColumns, [][]driver.Value{
		{"p-1", "Alpha", "http", true, "2026-09-01T00:00:00.000000Z"},
	})
	fixture.rec.script("id IN", optionColumns, [][]driver.Value{
		{"p-9", "Zeta", "http", true, "2026-09-02T00:00:00.000000Z"},
	})
	options, err := fixture.store.ListOptions(context.Background(), "Alpha", 20, []string{"p-9"})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	if len(options) != 2 || options[0].ID != "p-1" || options[1].ID != "p-9" {
		t.Fatalf("options wrong: %#v", options)
	}

	statements := fixture.rec.all()
	if len(statements) != 2 {
		t.Fatalf("语句数 = %d, 期望 2：\n%v", len(statements), statements)
	}
	wantWindow := bindTestPG(`
		SELECT id, name, type, enabled,
			to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at
		FROM juhe_business.proxy_profiles
		WHERE enabled = true AND (name COLLATE "C" >= ? AND name COLLATE "C" < ? AND starts_with(name, ?))
		ORDER BY name ASC, updated_at DESC, id ASC
		LIMIT ?`)
	if statements[0].query != wantWindow {
		t.Fatalf("window 查询文本不匹配：\n%s\n期望：\n%s", statements[0].query, wantWindow)
	}
	if strings.Contains(statements[0].query, "enabled = 1") {
		t.Fatalf("PG window 查询不得出现整数比较：%s", statements[0].query)
	}
	wantWindowArgs := []any{"Alpha", "Alphb", "Alpha", 20}
	if len(statements[0].args) != len(wantWindowArgs) {
		t.Fatalf("window 参数数 = %d, 期望 %d", len(statements[0].args), len(wantWindowArgs))
	}
	for i, want := range wantWindowArgs {
		if fmt.Sprintf("%v", statements[0].args[i]) != fmt.Sprintf("%v", want) {
			t.Fatalf("window 参数[%d] = %v, 期望 %v", i, statements[0].args[i], want)
		}
	}
	wantSelected := bindTestPG(`
			SELECT id, name, type, enabled,
			to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at
			FROM juhe_business.proxy_profiles
			WHERE enabled = true AND id IN (?)
			ORDER BY name ASC, updated_at DESC, id ASC`)
	if statements[1].query != wantSelected {
		t.Fatalf("selected 查询文本不匹配：\n%s\n期望：\n%s", statements[1].query, wantSelected)
	}
	if len(statements[1].args) != 1 || fmt.Sprintf("%v", statements[1].args[0]) != "p-9" {
		t.Fatalf("selected 参数 = %v", statements[1].args)
	}
}

// TestPostgresListPageRendersTimeTextAndKeyword 断言分页列表（Node
// queryProxiesAsync proxy.repository.ts:347-370）：to_char updated_at（US）与
// last_tested_at（MS，镜像 normalizePostgresRows 的 Date.toISOString 毫秒形）
// 文本列可被 string 扫描；keyword 窗口为 COLLATE "C" + starts_with。
func TestPostgresListPageRendersTimeTextAndKeyword(t *testing.T) {
	fixture := newProxyRenderFixture(t)
	summaryColumns := []string{
		"id", "name", "description", "type", "host", "port", "username", "enabled",
		"test_status", "latency_ms", "outbound_ip", "outbound_region", "last_test_message",
		"last_tested_at", "updated_at",
	}
	fixture.rec.script("ORDER BY updated_at DESC", summaryColumns, [][]driver.Value{
		{"p-1", "Alpha", nil, "http", "h", int64(8080), nil, true, "unknown", nil, nil, nil, nil,
			"2026-09-01T00:00:00.000Z", "2026-09-01T00:00:00.000000Z"},
	})
	result, err := fixture.store.ListPage(context.Background(), 1, 20, "Alpha")
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Enabled || result.Items[0].UpdatedAt != "2026-09-01T00:00:00.000000Z" {
		t.Fatalf("list wrong: %#v", result)
	}
	if result.Items[0].LastTestedAt == nil || *result.Items[0].LastTestedAt != "2026-09-01T00:00:00.000Z" {
		t.Fatalf("lastTestedAt wrong: %#v", result.Items[0].LastTestedAt)
	}

	statement := fixture.rec.all()[0]
	want := bindTestPG(`
		SELECT id, name, description, type, host, port, username, enabled, test_status,
			latency_ms, outbound_ip, outbound_region, last_test_message,
			to_char(last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS last_tested_at,
			to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at
		FROM juhe_business.proxy_profiles WHERE (name COLLATE "C" >= ? AND name COLLATE "C" < ? AND starts_with(name, ?))
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?`)
	if statement.query != want {
		t.Fatalf("分页查询文本不匹配：\n%s\n期望：\n%s", statement.query, want)
	}
	wantArgs := []any{"Alpha", "Alphb", "Alpha", int64(21), int64(0)}
	if len(statement.args) != len(wantArgs) {
		t.Fatalf("分页参数数 = %d, 期望 %d", len(statement.args), len(wantArgs))
	}
	for i, wantArg := range wantArgs {
		if fmt.Sprintf("%v", statement.args[i]) != fmt.Sprintf("%v", wantArg) {
			t.Fatalf("分页参数[%d] = %v, 期望 %v", i, statement.args[i], wantArg)
		}
	}
}

// TestPostgresCreateBindsBoolean 断言插入把 enabled 绑定为 bool（Node
// createProxyAsync proxy.repository.ts:558 直接绑定 proxy.enabled）。
func TestPostgresCreateBindsBoolean(t *testing.T) {
	for _, tc := range []struct {
		enabled   bool
		wantValue string
	}{
		{enabled: true, wantValue: "true"},
		{enabled: false, wantValue: "false"},
	} {
		fixture := newProxyRenderFixture(t)
		name := "代理-" + tc.wantValue
		proxyType := "http"
		host := "h"
		port := 8080
		enabled := tc.enabled
		_, err := fixture.store.Create(context.Background(), proxyInput{
			Name: &name, Type: &proxyType, Host: &host, Port: &port, Enabled: &enabled,
		}, "sa-1")
		if err != nil {
			t.Fatalf("Create(enabled=%v): %v", tc.enabled, err)
		}
		statement := fixture.rec.all()[0]
		if !strings.Contains(statement.query, "INSERT INTO juhe_business.proxy_profiles ") {
			t.Fatalf("INSERT 表名不对：\n%s", statement.query)
		}
		var enabledArg driver.Value
		if len(statement.args) > 9 {
			// enabled 位于第 10 个绑定参数（index 9）。
			enabledArg = statement.args[9]
		}
		if fmt.Sprintf("%v", enabledArg) != tc.wantValue {
			t.Fatalf("enabled=%v: enabled 绑定 = %v (%T), 期望 %s",
				tc.enabled, enabledArg, enabledArg, tc.wantValue)
		}
		if strings.Contains(statement.query, "COLLATE") {
			t.Fatalf("INSERT 不应携带 keyword 片段")
		}
	}
}

// TestPostgresPatchBindsBooleanAndTimestampCast 断言管理面 PATCH（Node
// patchProxyForManagementAsync proxy.repository.ts:849-863）：enabled 绑定
// bool（buildProxyManagementPatchPlan 行 978 的 PG 分支）、连接变更重置项、
// updated_at = GREATEST(... CAST(? AS timestamptz))、CAS WHERE 的显式 CAST。
func TestPostgresPatchBindsBooleanAndTimestampCast(t *testing.T) {
	fixture := newProxyRenderFixture(t)
	rowColumns := []string{
		"id", "name", "description", "type", "host", "port", "username",
		"password_encrypted", "enabled", "test_status", "updated_at",
	}
	currentRow := []driver.Value{"p-1", "代理-p-1", nil, "http", "old.host", int64(8080), nil, nil, true, "passed", "2026-09-01T00:00:00.000000Z"}
	updatedRow := append([]driver.Value{}, currentRow...)
	updatedRow[4] = "new.host"
	updatedRow[8] = false
	updatedRow[9] = "unknown"
	updatedRow[10] = "2026-09-04T12:00:00.000000Z"
	fixture.rec.script("password_encrypted", rowColumns, [][]driver.Value{currentRow})
	fixture.rec.script("password_encrypted", rowColumns, [][]driver.Value{updatedRow})

	newHost := "new.host"
	disabled := false
	outcome, err := fixture.store.Patch(context.Background(), "p-1", proxyInput{
		Host:              &newHost,
		Enabled:           &disabled,
		ExpectedUpdatedAt: "2026-09-01T00:00:00.000000Z",
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !outcome.Mutation.Changed || !outcome.RuntimeChanged || outcome.Mutation.UpdatedAt != "2026-09-04T12:00:00.000000Z" {
		t.Fatalf("patch outcome wrong: %#v", outcome.Mutation)
	}

	var update *proxyRecordedStatement
	for i := range fixture.rec.all() {
		statement := fixture.rec.all()[i]
		if strings.Contains(statement.query, "UPDATE juhe_business.proxy_profiles") {
			update = &fixture.rec.all()[i]
			break
		}
	}
	if update == nil {
		t.Fatalf("未捕获 UPDATE 语句：\n%v", fixture.rec.all())
	}
	if !strings.Contains(update.query, "enabled = $2") {
		t.Fatalf("UPDATE 应绑定 enabled 参数：\n%s", update.query)
	}
	if !strings.Contains(update.query, "updated_at = GREATEST(updated_at + INTERVAL '1 millisecond', CAST($9 AS timestamptz))") {
		t.Fatalf("UPDATE updated_at 表达式不匹配：\n%s", update.query)
	}
	if !strings.Contains(update.query, "WHERE id = $10 AND updated_at = CAST($11 AS timestamptz)") {
		t.Fatalf("UPDATE CAS WHERE 不匹配：\n%s", update.query)
	}
	if strings.Contains(update.query, "strftime") || strings.Contains(update.query, "enabled = 1") {
		t.Fatalf("PG UPDATE 不得携带 SQLite 片段：\n%s", update.query)
	}
	wantArgs := []string{
		"new.host",                    // host
		"false",                       // enabled（bool 绑定）
		"unknown",                     // test_status 重置
		"<nil>",                       // latency_ms
		"<nil>",                       // outbound_ip
		"<nil>",                       // outbound_region
		"<nil>",                       // last_test_message
		"<nil>",                       // last_tested_at
		"2026-09-04T12:00:00.000000Z", // updated_at candidate
		"p-1",                         // id
		"2026-09-01T00:00:00.000000Z", // CAS revision
	}
	if len(update.args) != len(wantArgs) {
		t.Fatalf("UPDATE 参数数 = %d, 期望 %d（%v）", len(update.args), len(wantArgs), update.args)
	}
	for i, want := range wantArgs {
		if fmt.Sprintf("%v", update.args[i]) != want {
			t.Fatalf("UPDATE 参数[%d] = %v (%T), 期望 %s", i, update.args[i], update.args[i], want)
		}
	}
	// 读取路径：current row 的 updated_at 经 to_char 文本化（Node
	// proxyManagementPatchSelectColumns proxy.repository.ts:887-889）。
	var load *proxyRecordedStatement
	for i := range fixture.rec.all() {
		statement := fixture.rec.all()[i]
		if strings.Contains(statement.query, "SELECT id, name, description") {
			load = &fixture.rec.all()[i]
			break
		}
	}
	if load == nil {
		t.Fatalf("未捕获行加载查询")
	}
	if !strings.Contains(load.query, `to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at`) {
		t.Fatalf("行加载应 to_char 文本化 updated_at：\n%s", load.query)
	}
}

// ---- limit 查询解析对齐（Node shared/query-values.ts:21-30 +
// proxies.routes.ts:123-125）----

// TestOptionLimitQueryParsing 断言非整数 limit 回落默认 50（修复前 Go 把
// "abc"/"1.8"/"1e2" 解析为 0 再钳成 1）。
func TestOptionLimitQueryParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 50},         // absent（未传参时 hasLimit=false，同形回落）
		{"abc", 50},      // NaN -> undefined -> 50
		{"1.8", 50},      // 非整数 -> undefined -> 50
		{"1e2", 50},      // Number("1e2")=100 -> clamp 50
		{"0x10", 16},     // 16 在 1..50 内原样保留
		{"0b11", 3},      // 3
		{"0o17", 15},     // 15
		{"Infinity", 50}, // 非整数 -> undefined -> 50
		{"1e400", 50},    // 溢出 -> Infinity -> undefined -> 50
		{"NaN", 50},
		{"inf", 50},   // 小写不识别 -> NaN
		{"0x", 50},    // 空十六进制 -> NaN
		{"-0x10", 50}, // 带符号十六进制 -> NaN
		{"1_0", 50},
		{".5", 50}, // 0.5 非整数 -> 50
		{"0", 1},
		{"-5", 1},
		{"1", 1},
		{"3", 3},
		{"49", 49},
		{"50", 50},
		{"51", 50},
		{" 7 ", 7},
		{"+9", 9},
		{"5.", 5},     // Number("5.")=5
		{"1e-400", 1}, // 下溢 -> 0 -> clamp 1
	}
	for _, tc := range cases {
		value, present := integerQueryValue(tc.raw)
		got := optionLimitValue(value, present)
		if got != tc.want {
			t.Fatalf("limit=%q -> %d, 期望 %d", tc.raw, got, tc.want)
		}
	}
	// 完全缺省（空 URL）：hasLimit=false -> 50。
	if value, present := integerQueryValue(""); present {
		t.Fatalf("空串不应解析成功：%v", value)
	}
}

// TestIntegerQueryValuePagePageSize 断言 page/pageSize 路径（Node
// parseProxyListOptions proxies.routes.ts:52-58）：非整数回落由 intFromQuery
// 承担（repository normalizeProxyListOptions proxy.repository.ts:372-383 的
// 非整数默认 20 / 1 语义由 ListPage clamp 保持）。
func TestIntegerQueryValuePagePageSize(t *testing.T) {
	if value, present := integerQueryValue("2.5"); present {
		t.Fatalf("2.5 不应解析为整数：%v", value)
	}
	if value, present := integerQueryValue("1e2"); !present || value != 100 {
		t.Fatalf("1e2 应为 100：%v %v", value, present)
	}
	if value, present := integerQueryValue("-0"); !present || value != 0 {
		t.Fatalf("-0 应为 0：%v %v", value, present)
	}
	if got := intFromQuery(1e20, true, 7); got != math.MaxInt {
		t.Fatalf("超大整数应饱和到 MaxInt：%d", got)
	}
	if got := intFromQuery(0, false, 7); got != 7 {
		t.Fatalf("缺省应回落：%d", got)
	}
}
