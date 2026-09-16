package policyreads

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// ---------------------------------------------------------------------------
// w11g 脚本化 database/sql driver：直测三个 store 的 SQL 错误臂与行解码
// 分支，不依赖真实数据库。步骤按消费顺序排列，可用 contains 匹配 SQL。
// ---------------------------------------------------------------------------

type w11gPStep struct {
	contains    string
	cols        []string
	rows        [][]driver.Value
	rowsErr     error
	nextErr     error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
}

var w11gPDriverSeq int64

type w11gPScript struct{ steps []w11gPStep }

func (s *w11gPScript) next(kind, query string) (*w11gPStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w11gP script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w11gP step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w11gPConn struct{ script *w11gPScript }

func (c *w11gPConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w11gP: prepare unsupported")
}
func (c *w11gPConn) Close() error { return nil }
func (c *w11gPConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w11gPTx{script: c.script}, nil
}
func (c *w11gPConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"v"}
	}
	return &w11gPRows{cols: cols, values: step.rows, nextErr: step.nextErr}, nil
}
func (c *w11gPConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w11gPResult{affected: step.affected, err: step.affectedErr}, nil
}

type w11gPTx struct{ script *w11gPScript }

func (t *w11gPTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w11gPTx) Rollback() error { return nil }

type w11gPResult struct {
	affected int64
	err      error
}

func (r w11gPResult) LastInsertId() (int64, error) { return 0, nil }
func (r w11gPResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w11gPRows struct {
	cols    []string
	values  [][]driver.Value
	nextErr error
	index   int
}

func (r *w11gPRows) Columns() []string { return r.cols }
func (r *w11gPRows) Close() error      { return nil }
func (r *w11gPRows) Next(dest []driver.Value) error {
	if r.nextErr != nil && r.index >= len(r.values) {
		return r.nextErr
	}
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}
func (r *w11gPRows) Err() error { return r.nextErr }

type w11gPDriver struct{ script *w11gPScript }

func (d w11gPDriver) Open(string) (driver.Conn, error) { return &w11gPConn{script: d.script}, nil }

var w11gClock = time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC)

func w11gOpenScripted(t *testing.T, steps []w11gPStep) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w11g-policyreads-%d", atomic.AddInt64(&w11gPDriverSeq, 1))
	sql.Register(name, w11gPDriver{script: &w11gPScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w11gExternalStore(t *testing.T, steps []w11gPStep) *ExternalStore {
	t.Helper()
	store, err := NewExternalStore(w11gOpenScripted(t, steps), false, func() time.Time { return w11gClock }, nil, nil, "w11g-crypto-secret")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW11GExternalListPageErrorArms(t *testing.T) {
	ctx := context.Background()
	s := w11gExternalStore(t, []w11gPStep{{rowsErr: errors.New("w11g list failed")}})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("list query error must propagate")
	}
	// 行扫描错误：map 值无法扫描进 string 目标。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{makeRowBad(10)}},
	})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("scan error must propagate")
	}
	// rows.Next 中途失败 → rows.Err()。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{makeRowStrings(10)}, nextErr: errors.New("w11g rows failed")},
	})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("rows error must propagate")
	}
	// 有行时追加 primary tokens 查询错误。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{makeRowStrings(10)}},
		{rowsErr: errors.New("w11g primary tokens failed")},
	})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("primary tokens query error must propagate")
	}
	// 存储行 scopes_json 损坏。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{makeRowStrings(10)}},
		{},
	})
	row := makeRowStrings(10)
	row[3] = "not-json"
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{row}},
		{},
	})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("bad scopes_json must fail list")
	}
	// rate_limits_json 损坏。
	row = makeRowStrings(10)
	row[3] = "[]"
	row[4] = "not-json"
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{row}},
		{},
	})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("bad rate_limits_json must fail list")
	}
	// pageSize 钳制：<1 与 >100（keyword 空白与 status=all 不加子句）。
	clampStore := w11gExternalStore(t, []w11gPStep{{}, {}})
	zero := 0
	result, err := clampStore.ListPage(ctx, nil, &zero, "  ", "all")
	if err != nil || result.PageSize != 1 {
		t.Fatalf("pageSize clamp low: %+v err=%v", result, err)
	}
	clampStore = w11gExternalStore(t, []w11gPStep{{}, {}})
	big := 101
	result, err = clampStore.ListPage(ctx, nil, &big, "  ", "all")
	if err != nil || result.PageSize != externalMaxPageSize {
		t.Fatalf("pageSize clamp high: %+v err=%v", result, err)
	}
}

func makeCols(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("c%d", i)
	}
	return out
}

func makeRowStrings(n int) []driver.Value {
	out := make([]driver.Value, n)
	for i := range out {
		out[i] = "v"
	}
	out[3] = "[]"
	out[4] = ""
	return out
}

func makeRowBad(n int) []driver.Value {
	out := make([]driver.Value, n)
	for i := range out {
		out[i] = map[string]any{}
	}
	return out
}

func TestW11GExternalFindSourceAndSecretArms(t *testing.T) {
	ctx := context.Background()
	// loadTokens 查询错误。
	s := w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(12), rows: [][]driver.Value{makeRowStrings(12)}},
		{rowsErr: errors.New("w11g tokens failed")},
	})
	if _, err := s.FindSource(ctx, "w11g-src"); err == nil {
		t.Fatal("token load error must propagate")
	}
	// 无 token：summary 返回空列表并统计 active 数。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(12), rows: [][]driver.Value{makeRowStrings(12)}},
		{},
	})
	summary, err := s.FindSource(ctx, "w11g-src")
	if err != nil || summary.TokenCount != 0 || len(summary.Tokens) != 0 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	// token 行扫描错误（12 列 token）。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(12), rows: [][]driver.Value{makeRowStrings(12)}},
		{cols: makeCols(12), rows: [][]driver.Value{makeRowBad(12)}},
	})
	if _, err := s.FindSource(ctx, "w11g-src"); err == nil {
		t.Fatal("token scan error must propagate")
	}
	// token scopes_json 损坏 → mapTokenSummary 回退空 scopes。
	tokenRow := makeRowStrings(12)
	tokenRow[6] = "bad-json"
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(12), rows: [][]driver.Value{makeRowStrings(12)}},
		{cols: makeCols(12), rows: [][]driver.Value{tokenRow}},
	})
	summary, err = s.FindSource(ctx, "w11g-src")
	if err != nil || len(summary.Tokens) != 1 || len(summary.Tokens[0].Scopes) != 0 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	// mapSourceRecord：rate_limits_json 损坏。
	sourceRow := makeRowStrings(10)
	sourceRow[4] = "bad-json"
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(12), rows: [][]driver.Value{append(sourceRow, int64(0), int64(0))}},
	})
	if _, err := s.FindSource(ctx, "w11g-src"); err == nil {
		t.Fatal("bad rate limits in detail must fail")
	}
	// FindTokenSecret：查询错误。
	s = w11gExternalStore(t, []w11gPStep{{rowsErr: errors.New("w11g secret failed")}})
	if _, err := s.FindTokenSecret(ctx, "w11g-src", "w11g-tok"); err == nil {
		t.Fatal("secret query error must propagate")
	}
	// 解密成功但 token 为空字符串 → ValidationError。
	emptyToken, err := encryptTestEnvelope("w11g-crypto-secret", map[string]string{"token": ""})
	if err != nil {
		t.Fatal(err)
	}
	s = w11gExternalStore(t, []w11gPStep{{cols: []string{"ciphertext"}, rows: [][]driver.Value{{emptyToken}}}})
	if _, err := s.FindTokenSecret(ctx, "w11g-src", "w11g-tok"); err == nil {
		t.Fatal("empty token payload must fail")
	}
}

func TestW11GExternalCreateAuthorizationArms(t *testing.T) {
	ctx := context.Background()
	input := externalSourceInput{
		Name:       "w11g 来源",
		Status:     "active",
		Scopes:     []any{"juhe_ai_public:api_key_list:read"},
		RateLimits: []any{},
	}
	s := w11gExternalStore(t, []w11gPStep{{beginErr: errors.New("w11g begin failed")}})
	if _, _, err := s.CreateAuthorization(ctx, input); err == nil {
		t.Fatal("begin error must propagate")
	}
	// 名称可用性检查查询失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{rowsErr: errors.New("w11g ensure failed")},
	})
	if _, _, err := s.CreateAuthorization(ctx, input); err == nil {
		t.Fatal("ensure-name error must propagate")
	}
	// 名称已被其他来源占用。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id"}, rows: [][]driver.Value{{"w11g-other"}}},
	})
	if _, _, err := s.CreateAuthorization(ctx, input); err == nil {
		t.Fatal("duplicate name must fail")
	}
	// insertSource 失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{},
		{execErr: errors.New("w11g insert source failed")},
	})
	if _, _, err := s.CreateAuthorization(ctx, input); err == nil {
		t.Fatal("insert source error must propagate")
	}
	// insertToken 失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{},
		{},
		{execErr: errors.New("w11g insert token failed")},
	})
	if _, _, err := s.CreateAuthorization(ctx, input); err == nil {
		t.Fatal("insert token error must propagate")
	}
	// Commit 失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{},
		{},
		{},
		{commitErr: errors.New("w11g commit failed")},
	})
	if _, _, err := s.CreateAuthorization(ctx, input); err == nil {
		t.Fatal("commit error must propagate")
	}
}

func encryptTestEnvelope(secret string, value map[string]string) (string, error) {
	return apikeys.EncryptJSON(secret, value)
}
