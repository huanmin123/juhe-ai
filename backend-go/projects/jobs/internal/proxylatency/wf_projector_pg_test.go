package proxylatency

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件用录制驱动（参照 cleanuprepo 的 pgRecorder 模式）锁定 ResultProjector
// 的 PostgreSQL SQL 文本契约与 PG 专属分支：契约预检语句、receipt/cursor 的
// FOR UPDATE 与 $n 占位、proxy_profiles CAS 的 ::timestamptz 围栏，以及
// manual no-targets/outbound 的 PG 路径。不依赖真实 PostgreSQL。

// ---- 录制驱动 ----

type wfStatement struct {
	query string
	args  []driver.Value
	tx    int
}

type wfScript struct {
	match    string
	columns  []string
	rows     [][]driver.Value
	execRows *int64
	closeErr error
	nextErr  error
}

type wfFail struct {
	match string
	err   error
}

type wfRecorder struct {
	mu         sync.Mutex
	statements []wfStatement
	txDepth    int
	begins     int
	commits    int
	rollbacks  int
	scripts    []wfScript
	queryFails []wfFail
	execFails  []wfFail
	beginErr   error
	commitErr  error
}

func newWFRecorder() *wfRecorder { return &wfRecorder{} }

func (r *wfRecorder) script(match string, columns []string, rows [][]driver.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, wfScript{match: match, columns: columns, rows: rows})
}

// scriptRowsErr 登记一个迭代到中途报错的查询结果。
func (r *wfRecorder) scriptRowsErr(match string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, wfScript{match: match, nextErr: err})
}

func (r *wfRecorder) scriptExec(match string, rows int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, wfScript{match: match, execRows: &rows})
}

func (r *wfRecorder) failQuery(match string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queryFails = append(r.queryFails, wfFail{match: match, err: err})
}

func (r *wfRecorder) failExec(match string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execFails = append(r.execFails, wfFail{match: match, err: err})
}

// popExecScript 只消费登记为“影响行数”的脚本（与查询脚本分开）。
func (r *wfRecorder) popExecScript(query string) (wfScript, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for position, item := range r.scripts {
		if item.execRows != nil && strings.Contains(query, item.match) {
			r.scripts = append(r.scripts[:position], r.scripts[position+1:]...)
			return item, true
		}
	}
	return wfScript{}, false
}

func (r *wfRecorder) popScript(query string) (wfScript, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for position, item := range r.scripts {
		if strings.Contains(query, item.match) {
			r.scripts = append(r.scripts[:position], r.scripts[position+1:]...)
			return item, true
		}
	}
	return wfScript{}, false
}

func (r *wfRecorder) popFail(kind string, query string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var list []wfFail
	if kind == "exec" {
		list = r.execFails
	} else {
		list = r.queryFails
	}
	for index, item := range list {
		if strings.Contains(query, item.match) {
			if kind == "exec" {
				r.execFails = append(r.execFails[:index], r.execFails[index+1:]...)
			} else {
				r.queryFails = append(r.queryFails[:index], r.queryFails[index+1:]...)
			}
			return item.err
		}
	}
	return nil
}

func (r *wfRecorder) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, wfStatement{query: query, args: values, tx: r.txDepth})
}

func (r *wfRecorder) all() []wfStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wfStatement{}, r.statements...)
}

// wfHasText 是 strings.Contains 的局部别名（needle 是否出现在 haystack）。
func wfHasText(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

type wfRecorderConnector struct{ rec *wfRecorder }

func (c wfRecorderConnector) Connect(context.Context) (driver.Conn, error) {
	return &wfRecorderConn{rec: c.rec}, nil
}

func (c wfRecorderConnector) Driver() driver.Driver { return wfRecorderConnector{rec: c.rec} }

func (c wfRecorderConnector) Open(string) (driver.Conn, error) {
	return &wfRecorderConn{rec: c.rec}, nil
}

type wfRecorderConn struct{ rec *wfRecorder }

func (c *wfRecorderConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recorder: Prepare 不应被调用（走 QueryerContext/ExecerContext）")
}

func (c *wfRecorderConn) Close() error { return nil }

func (c *wfRecorderConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recorder: Begin 不应被调用（走 BeginTx）")
}

func (c *wfRecorderConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.rec.mu.Lock()
	c.rec.txDepth++
	c.rec.begins++
	err := c.rec.beginErr
	c.rec.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &wfRecorderTx{rec: c.rec}, nil
}

func (c *wfRecorderConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.capture(query, args)
	if err := c.rec.popFail("exec", query); err != nil {
		return nil, err
	}
	if scripted, ok := c.rec.popExecScript(query); ok {
		return driver.RowsAffected(*scripted.execRows), nil
	}
	return driver.RowsAffected(1), nil
}

func (c *wfRecorderConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	if err := c.rec.popFail("query", query); err != nil {
		return nil, err
	}
	if scripted, ok := c.rec.popScript(query); ok {
		return &wfRecorderRows{columns: scripted.columns, values: scripted.rows, closeErr: scripted.closeErr, nextErr: scripted.nextErr}, nil
	}
	return &wfRecorderRows{columns: []string{"value"}}, nil
}

func (c *wfRecorderConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, int64, float64, bool, []byte, string, time.Time, []string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	}
	// 底层类型为 string 的自定义类型（如 ProjectionDisposition）按字符串绑定。
	converted, err := driver.DefaultParameterConverter.ConvertValue(value.Value)
	if err != nil {
		return fmt.Errorf("recorder: 不支持的参数类型 %T", value.Value)
	}
	value.Value = converted
	return nil
}

type wfRecorderTx struct{ rec *wfRecorder }

func (t *wfRecorderTx) Commit() error {
	t.rec.mu.Lock()
	t.rec.txDepth--
	t.rec.commits++
	err := t.rec.commitErr
	t.rec.mu.Unlock()
	return err
}

func (t *wfRecorderTx) Rollback() error {
	t.rec.mu.Lock()
	t.rec.txDepth--
	t.rec.rollbacks++
	t.rec.mu.Unlock()
	return nil
}

type wfRecorderRows struct {
	columns  []string
	values   [][]driver.Value
	pos      int
	closeErr error
	nextErr  error
}

func (r *wfRecorderRows) Columns() []string { return r.columns }

func (r *wfRecorderRows) Close() error { return r.closeErr }

func (r *wfRecorderRows) Next(dest []driver.Value) error {
	if r.nextErr != nil {
		return r.nextErr
	}
	if r.pos >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

func wfOpenRecorderDB(t *testing.T, rec *wfRecorder) *sql.DB {
	t.Helper()
	db := sql.OpenDB(wfRecorderConnector{rec: rec})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭录制库失败: %v", err)
		}
	})
	return db
}

// wfNewPGProjector 直接构造 PG 模式 projector（绕过 OpenStore 的池语义）。
func wfNewPGProjector(db *sql.DB) *ResultProjector {
	return &ResultProjector{
		store:    &Store{db: db, mode: StorePostgres},
		business: db,
		mode:     StorePostgres,
		cfg:      ResultProjectorConfig{ConsumerKey: "wf-pg", PollInterval: time.Minute, BatchSize: 10, Now: func() time.Time { return wfProjBase }},
		logger:   nil,
	}
}

func TestWFProjectorPostgresContract(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	projector := wfNewPGProjector(db)
	if err := projector.CheckContract(context.Background()); err != nil {
		t.Fatalf("PG 契约预检必须通过: %v", err)
	}
	statements := rec.all()
	if len(statements) != len(projector.contractStatements()) {
		t.Fatalf("契约语句数=%d want %d", len(statements), len(projector.contractStatements()))
	}
	joined := ""
	for _, statement := range statements {
		joined += statement.query + "\n"
	}
	for _, required := range []string{
		"FROM juhe_business.proxy_profiles LIMIT 0",
		"FROM juhe_business.proxy_latency_projection_receipts LIMIT 0",
		"FROM juhe_business.proxy_latency_projection_cursors LIMIT 0",
		"INSERT INTO juhe_business.account_list_availability_dirty",
	} {
		if !wfHasText(joined, required) {
			t.Fatalf("契约缺少 %q", required)
		}
	}
	// commit 失败必须传播。
	rec.commitErr = errors.New("提交失败")
	if err := projector.CheckContract(context.Background()); err == nil || !wfHasText(err.Error(), "提交 J3a Go result projector 契约事务失败") {
		t.Fatalf("commit 失败 err=%v", err)
	}
	rec.commitErr = nil
	rec.beginErr = errors.New("开始失败")
	if err := projector.CheckContract(context.Background()); err == nil || !wfHasText(err.Error(), "开始 J3a Go result projector 契约事务失败") {
		t.Fatalf("begin 失败 err=%v", err)
	}
}

func TestWFProjectorPostgresDrainEmpty(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	projector := wfNewPGProjector(db)
	count, err := projector.Drain(context.Background())
	if err != nil || count != 0 {
		t.Fatalf("空库 Drain count=%d err=%v", count, err)
	}
	joined := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
	}
	for _, required := range []string{
		"FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1",
		"FROM juhe_jobs.proxy_latency_outcomes WHERE committed=TRUE",
		"ORDER BY stored_at ASC,outcome_id ASC LIMIT $1",
	} {
		if !wfHasText(joined, required) {
			t.Fatalf("PG Drain 查询缺少 %q", required)
		}
	}
}

func TestWFProjectorPostgresProjectOutcomeFlow(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	projector := wfNewPGProjector(db)
	ctx := context.Background()

	revision := wfProjRevision
	observedAt := wfProjBase.Add(time.Second)
	outcome := Outcome{
		OutcomeID: "outcome-pg-1", RequestID: "request-pg-1", ProxyID: "p-pg",
		ObservedAt: observedAt, InputVersion: 1, ConfigRevision: revision, Trigger: TriggerPeriodic,
		OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
	}
	payload, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("编码 outcome 失败: %v", err)
	}
	digest, err := canonicalJSONDigest(outcome)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	rec.script("FROM juhe_jobs.proxy_latency_outcomes WHERE outcome_id=$1 AND committed=TRUE",
		[]string{"outcome_id", "request_id", "proxy_id", "input_version", "config_revision", "trigger", "owner_fence_token", "proxy_fence_token", "observed_at", "stored_at", "payload", "payload_digest"},
		[][]driver.Value{{outcome.OutcomeID, outcome.RequestID, outcome.ProxyID, int64(1), revision, string(TriggerPeriodic), int64(1), int64(1), observedAt, wfProjBase, payload, digest}})
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{outcome.ProxyID, revision, nil}})

	result, err := projector.ProjectOutcome(ctx, outcome)
	if err != nil || result.Disposition != ProjectionApplied || !result.Changed {
		t.Fatalf("PG 投影结果=%+v err=%v", result, err)
	}
	joined := ""
	argsText := ""
	for _, statement := range rec.all() {
		joined += statement.query + "\n"
		for _, arg := range statement.args {
			argsText += fmt.Sprintf("%v;", arg) + "\n"
		}
	}
	for _, required := range []string{
		"FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1 FOR UPDATE",
		"INSERT INTO juhe_business.proxy_latency_projection_receipts(outcome_id,proxy_id,input_version,disposition,reason,applied_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(outcome_id) DO NOTHING",
		"UPDATE juhe_business.proxy_profiles SET test_status=$1,latency_ms=$2,last_test_message=$3,last_tested_at=$4 WHERE id=$5 AND updated_at=$6::timestamptz AND (last_tested_at IS NULL OR last_tested_at<=$4)",
	} {
		// ProjectOutcome 走 advance=false 路径：只写 receipt 与 CAS，不推进 cursor。
		if !wfHasText(joined, required) {
			t.Fatalf("PG 投影缺少 %q", required)
		}
	}
	if !wfHasText(argsText, outcome.OutcomeID) || !wfHasText(argsText, outcome.ProxyID) || !wfHasText(argsText, string(ProjectionApplied)) {
		t.Fatal("PG 投影参数必须携带 outcome 身份与 applied disposition")
	}
	// payload 与摘要不出现在绑定参数中，但必须与持久化行一致（find 查询脚本已校验）。
	if !wfHasText(string(payload), outcome.OutcomeID) || len(digest) != 64 {
		t.Fatal("payload/摘要准备错误")
	}
}

func TestWFProjectorPostgresNoTargetsAndOutbound(t *testing.T) {
	rec := newWFRecorder()
	db := wfOpenRecorderDB(t, rec)
	projector := &ResultProjector{business: db, mode: StorePostgres, cfg: ResultProjectorConfig{Now: func() time.Time { return wfProjBase }}}
	ctx := context.Background()
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "p-pg", ProxyName: "PG代理", ConfigRevision: wfProjRevision,
		ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
	}
	observedAt := wfProjBase

	// no-targets：围栏命中（last_tested NULL）+ CAS 命中 → applied。
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{"p-pg", wfProjRevision, nil}})
	result, err := projector.ProjectManualNoTargets(ctx, request, observedAt)
	if err != nil || result.Disposition != ProjectionApplied || !result.Changed {
		t.Fatalf("PG no-targets 结果=%+v err=%v", result, err)
	}

	// outbound：双字段 CAS 命中。
	outcome := Outcome{ProxyID: "p-pg", ConfigRevision: wfProjRevision, ObservedAt: observedAt}
	rec.scriptExec("SET outbound_ip=$1,outbound_region=$2", 1)
	if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "JP"); err != nil {
		t.Fatalf("PG outbound 失败: %v", err)
	}
	found := false
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "SET outbound_ip=$1,outbound_region=$2") {
			found = true
			if len(statement.args) != 5 || statement.args[0] != "1.2.3.4" || statement.args[1] != "JP" || statement.args[2] != "p-pg" {
				t.Fatalf("outbound 参数=%v", statement.args)
			}
		}
	}
	if !found {
		t.Fatal("缺少 PG outbound 更新语句")
	}

	// outbound：CAS 未命中 + 围栏缺失 → ErrManualProxyMissing。
	rec.scriptExec("SET outbound_ip=$1,outbound_region=$2", 0)
	if err := projector.ProjectManualOutbound(ctx, outcome, "9.9.9.9", "US"); !errors.Is(err, ErrManualProxyMissing) {
		t.Fatalf("缺失代理 err=%v", err)
	}

	// outbound：CAS 未命中 + 围栏 revision 不匹配 → ErrManualProjectionStale。
	rec.scriptExec("SET outbound_ip=$1,outbound_region=$2", 0)
	rec.script("FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE",
		[]string{"id", "updated_at", "last_tested_at"},
		[][]driver.Value{{"p-pg", wfProjBase.Add(-time.Hour).UTC().Format(time.RFC3339Nano), nil}})
	if err := projector.ProjectManualOutbound(ctx, outcome, "9.9.9.9", "US"); !errors.Is(err, ErrManualProjectionStale) {
		t.Fatalf("stale err=%v", err)
	}
}

func TestWFProjectorCursorGuardsSQLite(t *testing.T) {
	business := wfOpenBusinessDB(t)
	projector := &ResultProjector{business: business, mode: StoreSQLite, cfg: ResultProjectorConfig{ConsumerKey: "wf-consumer", Now: func() time.Time { return wfProjBase }}}
	ctx := context.Background()

	// currentCursor：缺失/全空/单列/坏时间/正常。
	cursor, err := projector.currentCursor(ctx)
	if err != nil || cursor != nil {
		t.Fatalf("缺失 cursor=%v err=%v", cursor, err)
	}
	if _, err := business.Exec(`INSERT INTO proxy_latency_projection_cursors(consumer_key, stored_at, outcome_id, updated_at) VALUES('wf-consumer', NULL, NULL, '')`); err != nil {
		t.Fatalf("播种空 cursor 失败: %v", err)
	}
	cursor, err = projector.currentCursor(ctx)
	if err != nil || cursor != nil {
		t.Fatalf("全空 cursor=%v err=%v", cursor, err)
	}
	if _, err := business.Exec(`UPDATE proxy_latency_projection_cursors SET outcome_id='outcome-1' WHERE consumer_key='wf-consumer'`); err != nil {
		t.Fatalf("改写 cursor 失败: %v", err)
	}
	if _, err := projector.currentCursor(ctx); err == nil || !wfHasText(err.Error(), "存储损坏") {
		t.Fatalf("单列 cursor err=%v", err)
	}
	if _, err := business.Exec(`UPDATE proxy_latency_projection_cursors SET stored_at='not-a-time' WHERE consumer_key='wf-consumer'`); err != nil {
		t.Fatalf("改写 cursor 失败: %v", err)
	}
	if _, err := projector.currentCursor(ctx); err == nil {
		t.Fatal("坏时间 cursor 必须报错")
	}
	want := wfProjBase.Add(-time.Minute)
	if _, err := business.Exec(`UPDATE proxy_latency_projection_cursors SET stored_at=? WHERE consumer_key='wf-consumer'`, want.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("改写 cursor 失败: %v", err)
	}
	cursor, err = projector.currentCursor(ctx)
	if err != nil || cursor == nil || !cursor.StoredAt.Equal(want) || cursor.OutcomeID != "outcome-1" {
		t.Fatalf("cursor=%+v err=%v", cursor, err)
	}

	// advanceCursorTx：参数非法。
	tx, err := business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{}); err == nil {
		t.Fatal("零时间 cursor 必须拒绝")
	}
	if err := projector.advanceCursorTx(ctx, tx, OutcomeCursor{StoredAt: wfProjBase, OutcomeID: " "}); err == nil {
		t.Fatal("空 outcome id 必须拒绝")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// 无行 → 插入。
	next := OutcomeCursor{StoredAt: wfProjBase, OutcomeID: "outcome-next"}
	tx, err = business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	if err := projector.advanceCursorTx(ctx, tx, next); err != nil {
		t.Fatalf("插入 cursor 失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// 相等 → no-op；更新 → 前进；倒退 → 报错。
	tx, _ = business.BeginTx(ctx, nil)
	if err := projector.advanceCursorTx(ctx, tx, next); err != nil {
		t.Fatalf("相等 cursor 必须成功: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}
	older := OutcomeCursor{StoredAt: wfProjBase.Add(-time.Hour), OutcomeID: "outcome-old"}
	tx, _ = business.BeginTx(ctx, nil)
	if err := projector.advanceCursorTx(ctx, tx, older); err == nil || !wfHasText(err.Error(), "不允许倒退") {
		t.Fatalf("倒退 cursor err=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}
	newer := OutcomeCursor{StoredAt: wfProjBase.Add(time.Hour), OutcomeID: "outcome-new"}
	tx, _ = business.BeginTx(ctx, nil)
	if err := projector.advanceCursorTx(ctx, tx, newer); err != nil {
		t.Fatalf("前进 cursor 失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// 损坏行（stored_at/outcome_id 任一为 NULL）。
	if _, err := business.Exec(`UPDATE proxy_latency_projection_cursors SET outcome_id=NULL WHERE consumer_key='wf-consumer'`); err != nil {
		t.Fatalf("破坏 cursor 失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	if err := projector.advanceCursorTx(ctx, tx, newer); err == nil || !wfHasText(err.Error(), "存储损坏") {
		t.Fatalf("损坏 cursor err=%v", err)
	}
	_ = tx.Rollback()
}

func TestWFProjectorReceiptGuards(t *testing.T) {
	business := wfOpenBusinessDB(t)
	projector := &ResultProjector{business: business, mode: StoreSQLite, cfg: ResultProjectorConfig{ConsumerKey: "wf-consumer", Now: func() time.Time { return wfProjBase }}}
	ctx := context.Background()
	base := ProjectionResult{OutcomeID: "outcome-1", ProxyID: "p-1", InputVersion: 1}

	// 非法 disposition 参数。
	tx, err := business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	if err := projector.insertReceiptTx(ctx, tx, base, ProjectionDisposition("bogus"), ""); err == nil {
		t.Fatal("非法 disposition 必须拒绝")
	}
	_ = tx.Rollback()

	// 幂等冲突：已有 receipt 与本次写入不一致。
	if _, err := business.Exec(`INSERT INTO proxy_latency_projection_receipts(outcome_id,proxy_id,input_version,disposition,reason,applied_at) VALUES('outcome-1','p-1',1,'applied',NULL,'')`); err != nil {
		t.Fatalf("播种 receipt 失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	if err := projector.insertReceiptTx(ctx, tx, base, ProjectionStale, "config_revision_stale"); err == nil || !wfHasText(err.Error(), "幂等性冲突") {
		t.Fatalf("幂等冲突 err=%v", err)
	}
	_ = tx.Rollback()

	// 幂等重放：完全一致的 receipt 允许静默通过。
	tx, _ = business.BeginTx(ctx, nil)
	if err := projector.insertReceiptTx(ctx, tx, base, ProjectionApplied, ""); err != nil {
		t.Fatalf("一致重放 err=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// receiptTx 读到非法 disposition 必须报错。
	if _, err := business.Exec(`UPDATE proxy_latency_projection_receipts SET disposition='bogus' WHERE outcome_id='outcome-1'`); err != nil {
		t.Fatalf("破坏 receipt 失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	if _, _, err := projector.receiptTx(ctx, tx, "outcome-1"); err == nil || !wfHasText(err.Error(), "disposition 无效") {
		t.Fatalf("非法 disposition 读取 err=%v", err)
	}
	_ = tx.Rollback()
}

func TestWFProjectorProxyFenceSQLite(t *testing.T) {
	business := wfOpenBusinessDB(t)
	projector := &ResultProjector{business: business, mode: StoreSQLite}
	ctx := context.Background()

	// 缺失行。
	tx, err := business.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("开始事务失败: %v", err)
	}
	found, revision, lastTestedAt, err := projector.proxyFenceTx(ctx, tx, "p-missing")
	if err != nil || found || revision != "" || lastTestedAt != nil {
		t.Fatalf("缺失围栏 found=%v revision=%q last=%v err=%v", found, revision, lastTestedAt, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// 非法 updated_at。
	if _, err := business.Exec(`INSERT INTO proxy_profiles(id, updated_at, test_status) VALUES('p-bad', 'not-a-time', '')`); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	if _, _, _, err := projector.proxyFenceTx(ctx, tx, "p-bad"); err == nil || !wfHasText(err.Error(), "fence 无效") {
		t.Fatalf("非法 revision err=%v", err)
	}
	_ = tx.Rollback()

	// NULL last_tested_at。
	if _, err := business.Exec(`INSERT INTO proxy_profiles(id, updated_at, test_status) VALUES('p-null', ?, '')`, wfProjRevision); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	found, revision, lastTestedAt, err = projector.proxyFenceTx(ctx, tx, "p-null")
	if err != nil || !found || revision != wfProjRevision || lastTestedAt != nil {
		t.Fatalf("NULL last 围栏 found=%v revision=%q last=%v err=%v", found, revision, lastTestedAt, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// 有效 last_tested_at。
	testedAt := wfProjBase.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := business.Exec(`INSERT INTO proxy_profiles(id, updated_at, last_tested_at, test_status) VALUES('p-tested', ?, ?, '')`, wfProjRevision, testedAt); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	found, _, lastTestedAt, err = projector.proxyFenceTx(ctx, tx, "p-tested")
	if err != nil || !found || lastTestedAt == nil || !lastTestedAt.Equal(wfProjBase.Add(-time.Minute)) {
		t.Fatalf("有效围栏 found=%v last=%v err=%v", found, lastTestedAt, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	// last_tested_at 非 RFC3339 UTC。
	if _, err := business.Exec(`INSERT INTO proxy_profiles(id, updated_at, last_tested_at, test_status) VALUES('p-badtime', ?, ?, '')`, wfProjRevision, "2026-09-01 12:00:00"); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	tx, _ = business.BeginTx(ctx, nil)
	if _, _, _, err := projector.proxyFenceTx(ctx, tx, "p-badtime"); err == nil {
		t.Fatal("非法 last_tested_at 必须报错")
	}
	_ = tx.Rollback()
}

func TestWFProjectorPureHelpers(t *testing.T) {
	earlier := OutcomeCursor{StoredAt: wfProjBase, OutcomeID: "a"}
	later := OutcomeCursor{StoredAt: wfProjBase.Add(time.Second), OutcomeID: "b"}
	sameIDLater := OutcomeCursor{StoredAt: wfProjBase, OutcomeID: "z"}
	if compareOutcomeCursor(earlier, later) != -1 || compareOutcomeCursor(later, earlier) != 1 {
		t.Fatal("cursor 时间比较错误")
	}
	if compareOutcomeCursor(earlier, sameIDLater) != -1 || compareOutcomeCursor(sameIDLater, earlier) != 1 || compareOutcomeCursor(earlier, earlier) != 0 {
		t.Fatal("cursor id 比较错误")
	}
	if nullableString("") != nil || nullableString("x") != "x" {
		t.Fatal("nullableString 错误")
	}
	same := wfProjRevision
	granular := wfProjRevisionGranular
	if !sameProjectionInstant(same, granular) {
		t.Fatal("同时刻不同文本必须视为相同")
	}
	if sameProjectionInstant(same, wfProjBase.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano)) {
		t.Fatal("不同时刻不得视为相同")
	}
	if sameProjectionInstant(same, "bad") || sameProjectionInstant("bad", same) {
		t.Fatal("非法时间不得视为相同")
	}
	if !validProjectionInstant(wfProjRevision) || validProjectionInstant("nope") {
		t.Fatal("validProjectionInstant 错误")
	}
	for _, status := range []string{"passed", "warning", "failed", "unknown"} {
		if !validProjectionStatus(status) {
			t.Fatalf("状态 %s 必须合法", status)
		}
	}
	if validProjectionStatus("green") {
		t.Fatal("未知状态必须非法")
	}
	for _, disposition := range []string{"applied", "stale", "ignored", "rejected"} {
		if !validProjectionDisposition(disposition) {
			t.Fatalf("disposition %s 必须合法", disposition)
		}
	}
	if validProjectionDisposition("maybe") {
		t.Fatal("未知 disposition 必须非法")
	}

	// summarizeProjectionItems 消息分支。
	passed := summarizeProjectionItems([]ItemResult{{Status: ItemPassed, LatencyMS: 8}})
	if passed.Status != string(OverallPassed) || passed.Message != "代理质量检测通过" || passed.LatencyMS == nil || *passed.LatencyMS != 8 {
		t.Fatalf("passed 汇总=%+v", passed)
	}
	warning := summarizeProjectionItems([]ItemResult{{Status: ItemWarning, LatencyMS: 5}, {Status: ItemWarning, LatencyMS: 7}})
	if warning.Status != string(OverallWarning) || warning.Message != "代理可用，存在 2 项告警" || warning.LatencyMS == nil || *warning.LatencyMS != 6 {
		t.Fatalf("warning 汇总=%+v", warning)
	}
	failed := summarizeProjectionItems([]ItemResult{{Status: ItemFailed}, {Status: ItemFailed}})
	if failed.Status != string(OverallFailed) || failed.Message != "代理检测存在 3 项失败" || failed.LatencyMS != nil {
		t.Fatalf("failed 汇总=%+v", failed)
	}
	// 空列表时 base 为 passed（projectionBase 对空集返回 ItemPassed）。
	empty := summarizeProjectionItems(nil)
	if empty.Status != string(OverallPassed) || empty.LatencyMS != nil {
		t.Fatalf("空列表汇总=%+v", empty)
	}
	unknown := summarizeProjectionItems([]ItemResult{{Status: ItemUnknown}})
	if unknown.Status != string(OverallUnknown) || unknown.Message != "代理检测未形成有效传输尝试" || unknown.LatencyMS != nil {
		t.Fatalf("unknown 汇总=%+v", unknown)
	}
	mixed := summarizeProjectionItems([]ItemResult{{Status: ItemPassed, LatencyMS: 10}, {Status: ItemUnknown}})
	if mixed.Status != string(OverallWarning) {
		t.Fatalf("passed+unknown 汇总=%+v", mixed)
	}
}
