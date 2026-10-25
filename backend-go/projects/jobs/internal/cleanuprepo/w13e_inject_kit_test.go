package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

)

// w13e_inject_kit_test.go 提供 w13e 批次的 PG 语句级注入装饰器（sqlite 半区随臂退役）。
//   - SQLite 装饰器：在 kitFailingSQLite 语义之上补齐 Begin 失败、Commit
//     失败、RowsAffected 失败与「脚本化行集」（可注入 Scan 失败值与迭代
//     中段错误，覆盖 rows.Scan / rows.Err 错误臂）；
//   - PG 装饰器：在 kitFailingPGConn 之上补齐 Commit 失败与 RowsAffected
//     失败（覆盖 changes / tx.Commit 错误臂）。
// 断言只覆盖「该失败必须向外传播」；不改变生产语义。
//
// fired 记账：open 助手为每个句柄集合建立 w13eFiredArms，注入命中时打点，
// 测试收尾断言所有注册的子串/布尔注入都真实命中过——子串与生产 SQL 漂移
// 导致规则永不命中的伪覆盖臂在此变红（allowUnfired 显式豁免负对照臂）。

// w13eFiredArms 记录注入臂的真实命中情况。
type w13eFiredArms struct {
	mu    sync.Mutex
	names map[string]bool
}

func (a *w13eFiredArms) mark(name string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.names == nil {
		a.names = map[string]bool{}
	}
	a.names[name] = true
}

func (a *w13eFiredArms) fired(name string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.names[name]
}

// w13eArm 一条注入臂：名称 + 命中子串（布尔注入用说明文本）。
type w13eArm struct {
	name   string
	needle string
}

// w13eAssertArmsFired 断言所有注册的注入臂都命中过；allowUnfired 显式豁免。
func w13eAssertArmsFired(t *testing.T, fired *w13eFiredArms, allowUnfired []string, arms []w13eArm) {
	t.Helper()
	for _, arm := range arms {
		if arm.needle == "" || fired.fired(arm.name) {
			continue
		}
		allowed := false
		for _, name := range allowUnfired {
			if name == arm.name {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}
		t.Errorf("w13e 注入规则未命中（伪覆盖）：%s=%q", arm.name, arm.needle)
	}
}

// ---- SQLite 装饰器 ----

// w13eRowsScript 按子串匹配返回脚本化行集。errAfterRows >= 0 时在产出
// errAfterRows 行后令 Next 返回错误（rows.Err 臂）；values 中的字符串喂给
// 数值扫描目标时自然产生 Scan 错误。
type w13eRowsScript struct {
	match        string
	columns      []string
	values       [][]driver.Value
	errAfterRows int // -1 表示不注入迭代错误
}

type w13eScriptedRows struct {
	columns []string
	values  [][]driver.Value
	pos     int
	errAt   int // 产出第 errAt 行后返回错误；<0 表示无
}

func (r *w13eScriptedRows) Columns() []string { return r.columns }
func (r *w13eScriptedRows) Close() error      { return nil }
func (r *w13eScriptedRows) Next(dest []driver.Value) error {
	if r.errAt >= 0 && r.pos == r.errAt {
		return errors.New("w13eScriptedRows: 迭代中段注入错误")
	}
	if r.pos >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

type w13ePGOptions struct {
	failOn             []string
	failBegin          bool
	failCommit         bool
	rowsAffectedFailOn string
	// fired 由 open 助手初始化（同一 opts 传给多个 open 时共享一份）；
	// allowUnfired 列出豁免 fired 断言的臂名。
	fired        *w13eFiredArms
	allowUnfired []string
}

func (o *w13ePGOptions) arms() []w13eArm {
	arms := make([]w13eArm, 0, len(o.failOn)+3)
	for _, needle := range o.failOn {
		arms = append(arms, w13eArm{name: "failOn:" + needle, needle: needle})
	}
	if o.rowsAffectedFailOn != "" {
		arms = append(arms, w13eArm{name: "rowsAffectedFailOn", needle: o.rowsAffectedFailOn})
	}
	if o.failBegin {
		arms = append(arms, w13eArm{name: "failBegin", needle: "BeginTx 注入失败"})
	}
	if o.failCommit {
		arms = append(arms, w13eArm{name: "failCommit", needle: "Commit 注入失败"})
	}
	return arms
}

type w13eDecoratedPGConn struct {
	*kitFailingPGConn
	opts w13ePGOptions
}

// markPGFired 记录本语句命中了哪些 failOn 注入（与 kitFailingPGConn.shouldFail
// 同一条件）。
func (c *w13eDecoratedPGConn) markPGFired(query string) {
	for _, needle := range c.opts.failOn {
		if strings.Contains(query, needle) {
			c.opts.fired.mark("failOn:" + needle)
		}
	}
}

func (c *w13eDecoratedPGConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.opts.rowsAffectedFailOn != "" && strings.Contains(query, c.opts.rowsAffectedFailOn) {
		c.opts.fired.mark("rowsAffectedFailOn")
		c.rec.capture(query, args)
		return kitFailingResult{}, nil
	}
	c.markPGFired(query)
	return c.kitFailingPGConn.ExecContext(ctx, query, args)
}

func (c *w13eDecoratedPGConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.markPGFired(query)
	return c.kitFailingPGConn.QueryContext(ctx, query, args)
}

func (c *w13eDecoratedPGConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.opts.failBegin {
		c.opts.fired.mark("failBegin")
		return nil, errors.New("w13eDecoratedPGConn: BeginTx 注入失败")
	}
	c.rec.mu.Lock()
	c.rec.txDepth++
	c.rec.begins++
	c.rec.mu.Unlock()
	return w13ePGTx{rec: c.rec, failCommit: c.opts.failCommit, fired: c.opts.fired}, nil
}

type w13ePGTx struct {
	rec        *pgRecorder
	failCommit bool
	fired      *w13eFiredArms
}

func (t w13ePGTx) Commit() error {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	t.rec.txDepth--
	t.rec.commits++
	if t.failCommit {
		t.fired.mark("failCommit")
		return errors.New("w13ePGTx: Commit 注入失败")
	}
	return nil
}

func (t w13ePGTx) Rollback() error {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	t.rec.txDepth--
	t.rec.rollbacks++
	return nil
}

type w13ePGConnector struct {
	rec  *pgRecorder
	opts w13ePGOptions
}

func (c w13ePGConnector) Connect(context.Context) (driver.Conn, error) {
	inner := &kitFailingPGConn{recorderConn: &recorderConn{rec: c.rec}, failOn: c.opts.failOn}
	return &w13eDecoratedPGConn{kitFailingPGConn: inner, opts: c.opts}, nil
}

func (c w13ePGConnector) Driver() driver.Driver { return recorderDriver{rec: c.rec} }

// w13eOpenDecoratedPG 打开带 Commit/RowsAffected 注入的 PG 录制句柄。

func w13eOpenDecoratedPG(t *testing.T, rec *pgRecorder, opts w13ePGOptions) *DB {
	t.Helper()
	if opts.fired == nil {
		opts.fired = &w13eFiredArms{}
	}
	t.Cleanup(func() { w13eAssertArmsFired(t, opts.fired, opts.allowUnfired, opts.arms()) })
	return &DB{DB: sql.OpenDB(w13ePGConnector{rec: rec, opts: opts}), Postgres: true}
}
