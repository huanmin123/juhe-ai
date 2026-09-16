package publicapilogs

// w9b JSON 快照与 capture 预算补充：类型矩阵、HTML 反转义、Buffer 快照、
// 预算扣减、构造器守卫。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestW9BAppendJSONValueMatrix(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "null"},
		{"undefined", undefinedValue{}, "null"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", int(3), "3"},
		{"int8", int8(-3), "-3"},
		{"int16", int16(300), "300"},
		{"int32", int32(-70000), "-70000"},
		{"int64", int64(1 << 40), "1099511627776"},
		{"uint", uint(5), "5"},
		{"uint32", uint32(7), "7"},
		{"uint64", uint64(1 << 41), "2199023255552"},
		{"json.Number", json.Number("1.25"), "1.25"},
		{"float int", float64(4), "4"},
		{"float frac", 1.5, "1.5"},
		{"float NaN", math.NaN(), "null"},
		{"float Inf", math.Inf(1), "null"},
		{"array", []any{1, "a", nil}, `[1,"a",null]`},
		{"map sorted", map[string]any{"b": 2, "a": 1}, `{"a":1,"b":2}`},
		{"map nil", map[string]any(nil), "null"},
		{"snapshot nil", (*snapshotObject)(nil), "null"},
		{"fallback struct", struct {
			X int `json:"x"`
		}{1}, `{"x":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(appendJSONValue(nil, tc.value))
			if got != tc.want {
				t.Fatalf("%s=%q want %q", tc.name, got, tc.want)
			}
		})
	}
	// 不支持类型走 fallback：json.Marshal 失败时返回原 buf。
	if got := appendJSONValue([]byte("ok"), make(chan int)); string(got) != "ok" {
		t.Fatalf("不可序列化=%q", got)
	}
}

func TestW9BHTMLUnescape(t *testing.T) {
	encoded, err := json.Marshal("<a>&\"b\"")
	if err != nil {
		t.Fatal(err)
	}
	// 手工构造的 \u003c/\u003e/\u0026 反转义（< > & 是 encoding/json 的 HTML 转义集）。
	manual := []byte("\"\\u003c\\u003e\\u0026\\\"b\\\"\"")
	if string(htmlUnescape(manual)) != `"<>&\"b\""` {
		t.Fatalf("manual=%q", htmlUnescape(manual))
	}
	_ = encoded
	// 无 \u00 前缀直接原样。
	if got := htmlUnescape([]byte(`"plain"`)); string(got) != `"plain"` {
		t.Fatalf("plain=%q", got)
	}
	// 部分转义序列保留。
	if got := htmlUnescape([]byte(`"\u0041"`)); string(got) != `"\u0041"` {
		t.Fatalf("非目标转义=%q", got)
	}
}

func TestW9BAppendBufferSnapshotShape(t *testing.T) {
	got := string(appendBufferSnapshot(nil, []byte("hello")))
	if !strings.Contains(got, `"type":"Buffer"`) || !strings.Contains(got, `"byteLength":5`) || !strings.Contains(got, `"preview":"hello"`) || !strings.Contains(got, `"truncated":false`) {
		t.Fatalf("buffer snapshot=%s", got)
	}
	long := strings.Repeat("y", publicAPISnapshotStringPreviewBytes+10)
	got = string(appendBufferSnapshot(nil, []byte(long)))
	if !strings.Contains(got, `"truncated":true`) {
		t.Fatalf("超长 buffer 应截断")
	}
}

func TestW9BCloneSnapshotBufferAndCharge(t *testing.T) {
	state := &snapshotBudget{remainingBytes: 100000}
	clone := cloneSnapshotBuffer([]byte("data"), state)
	if clone == nil {
		t.Fatal("clone 不能为 nil")
	}
	if state.remainingBytes >= 100000 {
		t.Fatalf("预算必须扣减：%d", state.remainingBytes)
	}
	// 预算极小 → 截断。
	small := &snapshotBudget{remainingBytes: 1}
	smallClone := cloneSnapshotBuffer([]byte("abcdef"), small)
	encoded, err := json.Marshal(smallClone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"truncated":true`) {
		t.Fatalf("小预算=%s", encoded)
	}
	// 负数扣减钳制。
	negative := &snapshotBudget{remainingBytes: 5}
	chargeSnapshotBytes(negative, 100)
	if negative.remainingBytes != 0 || !negative.truncated {
		t.Fatalf("负预算=%+v", negative)
	}
	neg := &snapshotBudget{remainingBytes: 10}
	chargeSnapshotBytes(neg, -5)
	if neg.remainingBytes != 10 {
		t.Fatalf("负字节=%d", neg.remainingBytes)
	}
}

func TestW9BSafeJSONObjectStringifyArms(t *testing.T) {
	if got := safeJSONObjectStringify(nil); got != "{}" {
		t.Fatalf("nil=%q", got)
	}
	if got := safeJSONObjectStringify((*snapshotObject)(nil)); got != "{}" {
		t.Fatalf("nil snapshot=%q", got)
	}
	object := newSnapshotObject().set("b", 2).set("a", 1)
	if got := safeJSONObjectStringify(object); got != `{"b":2,"a":1}` {
		t.Fatalf("插入序=%q", got)
	}
	// 非 snapshot/普通 map[string]any 之外的类型折叠为 {}。
	if got := safeJSONObjectStringify(map[string]int{"x": 1}); got != "{}" {
		t.Fatalf("未知类型=%q", got)
	}
	if got := safeJSONObjectStringify(map[string]any{"x": 1}); got != `{"x":1}` {
		t.Fatalf("map=%q", got)
	}
}

func TestW9BIntegerOrNullMatrix(t *testing.T) {
	cases := []struct {
		value any
		want  any
	}{
		{nil, nil},
		{int(3), int64(3)},
		{int32(4), int64(4)},
		{int64(5), int64(5)},
		{float64(6), int64(6)},
		{math.NaN(), nil},
		{math.Inf(-1), nil},
		{"7", nil},
	}
	for _, tc := range cases {
		if got := integerOrNull(tc.value); got != tc.want {
			t.Fatalf("integerOrNull(%#v)=%v want %v", tc.value, got, tc.want)
		}
	}
}

func TestW9BJSONUnsupportedErrorMessage(t *testing.T) {
	if errJSONUnsupported.Error() == "" {
		t.Fatal("错误消息不能为空")
	}
}

func TestW9BPipelineDoneAndDrainShutdown(t *testing.T) {
	env := newW9BPipelineEnv(t)
	// 正常 drain：关闭后残余队列写完，Done 关闭。
	item := env.capture("w9b-drain")
	if !item {
		t.Fatal("capture 失败")
	}
	env.pipeline.Close(context.Background())
	select {
	case <-env.pipeline.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done 未关闭")
	}
	if env.writer.batches == 0 {
		t.Fatal("关闭前应有批次写出")
	}
}

type w9bWriterError struct{}

func (w9bWriterError) Error() string { return "w9b writer failure" }

var errW9BWriter = w9bWriterError{}

type w9bWriter struct {
	batches int
	fail    bool
}

func (w *w9bWriter) InsertBatch(ctx context.Context, inputs []Input) error {
	if w.fail {
		return errW9BWriter
	}
	w.batches++
	return nil
}

type w9bPipelineEnv struct {
	pipeline *Pipeline
	writer   *w9bWriter
}

func (e *w9bPipelineEnv) capture(id string) bool {
	return e.pipeline.Enqueue(Input{ID: id, Method: "POST", Path: "/v1/x", StatusCode: 200})
}

func newW9BPipelineEnv(t *testing.T) *w9bPipelineEnv {
	t.Helper()
	writer := &w9bWriter{}
	cfg := Config{QueueMaxItems: 64, QueueMaxBytes: 1 << 20, FlushBatchSize: 2, RetryDelay: 10 * time.Millisecond, ShutdownMaxBatch: 8}
	pipeline := NewPipeline(writer, cfg)
	t.Cleanup(func() { pipeline.Close(context.Background()) })
	return &w9bPipelineEnv{pipeline: pipeline, writer: writer}
}

func TestW9BSettingNumberArms(t *testing.T) {
	if _, err := SettingNumber(map[string]any{}, "k", 1, 10); err == nil {
		t.Fatal("缺键必须报错")
	}
	if _, err := SettingNumber(map[string]any{"k": "x"}, "k", 1, 10); err == nil {
		t.Fatal("非数字必须报错")
	}
	if _, err := SettingNumber(map[string]any{"k": 11}, "k", 1, 10); err == nil {
		t.Fatal("越界必须报错")
	}
	if value, err := SettingNumber(map[string]any{"k": json.Number("7")}, "k", 1, 10); err != nil || value != 7 {
		t.Fatalf("json.Number=%d err=%v", value, err)
	}
	if value, err := SettingNumber(map[string]any{"k": 7.5}, "k", 1, 10); err == nil {
		t.Fatalf("小数=%d", value)
	}
}

func TestW9BVisitJSONLikeValueArms(t *testing.T) {
	// 对象/数组/标量的估算遍历。
	nested := map[string]any{
		"a": []any{1, "two", map[string]any{"c": true}},
		"d": json.Number("3.14"),
	}
	context := &jsonLikeByteEstimateContext{seen: map[*snapshotObject]bool{}, maxBytes: 1 << 20, maxNodes: 10000}
	visitJSONLikeValue(nested, context)
	if context.total == 0 {
		t.Fatal("估算字节必须为正")
	}
	// 节点上限触顶。
	tiny := &jsonLikeByteEstimateContext{seen: map[*snapshotObject]bool{}, maxBytes: 1 << 20, maxNodes: 2}
	visitJSONLikeValue(nested, tiny)
	if tiny.nodeCount > 2 {
		t.Fatalf("节点上限=%d", tiny.nodeCount)
	}
}

func TestW9BStorePostgresInsertPath(t *testing.T) {
	// pg 标志驱动方言分支：nil db 的构造守卫 + postgres bind 查询文本。
	if _, err := NewStore(nil, true, nil, nil); err == nil {
		t.Fatal("nil db 必须拒绝")
	}
}

func TestW9BStorePostgresInsertBatchArms(t *testing.T) {
	// 脚本化 database/sql driver 驱动 PG 方言 INSERT 分支。
	script := &w9bScript{}
	store, err := NewStore(w9bOpen(t, script), true, func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []Input{{ID: "pal-1", Method: "GET", Path: "/v1/models", StatusCode: 200}, {ID: "pal-2", Method: "POST", Path: "/v1/chat", StatusCode: 500}}
	if err := store.InsertBatch(context.Background(), inputs); err != nil {
		t.Fatalf("PG 批量插入=%v", err)
	}
	// 失败注入：BEGIN 后第一条 INSERT 失败。
	failScript := &w9bScript{steps: []w9bScriptStep{{matcher: []string{"INSERT INTO"}, execErr: errW9BWriter}}}
	failing, err := NewStore(w9bOpen(t, failScript), true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.InsertBatch(context.Background(), inputs); err == nil {
		t.Fatal("插入失败必须透传")
	}
	// 空批次 no-op。
	if err := store.InsertBatch(context.Background(), nil); err != nil {
		t.Fatalf("空批次=%v", err)
	}
}

// w9b 脚本化 database/sql driver：按查询子串匹配注入失败/行数据（PG 分支）。
type w9bScriptStep struct {
	matcher  []string
	execErr  error
	affected int64
}

var w9bScriptSeq int64

type w9bScript struct {
	steps []w9bScriptStep
}

func (s *w9bScript) take(kind, query string) *w9bScriptStep {
	for index := range s.steps {
		step := &s.steps[index]
		matched := true
		for _, fragment := range step.matcher {
			if !strings.Contains(query, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return step
		}
	}
	return nil
}

type w9bScriptConn struct{ script *w9bScript }

func (c *w9bScriptConn) Prepare(string) (driver.Stmt, error) {
	return &w9bScriptStmt{}, nil
}
func (c *w9bScriptConn) Close() error { return nil }
func (c *w9bScriptConn) Begin() (driver.Tx, error) {
	return w9bScriptTx{}, nil
}

func (c *w9bScriptConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if step := c.script.take("Exec", query); step != nil && step.execErr != nil {
		return nil, step.execErr
	}
	return driver.RowsAffected(1), nil
}

type w9bScriptStmt struct{}

func (s *w9bScriptStmt) Close() error  { return nil }
func (s *w9bScriptStmt) NumInput() int { return -1 }
func (s *w9bScriptStmt) Exec(args []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (s *w9bScriptStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("w9b: query unsupported")
}

type w9bScriptTx struct{}

func (w9bScriptTx) Commit() error   { return nil }
func (w9bScriptTx) Rollback() error { return nil }

type w9bScriptDriver struct{ script *w9bScript }

func (d w9bScriptDriver) Open(string) (driver.Conn, error) {
	return &w9bScriptConn{script: d.script}, nil
}

func w9bOpen(t *testing.T, script *w9bScript) *sql.DB {
	t.Helper()
	name := "w9b-pal-" + strconv.FormatInt(atomic.AddInt64(&w9bScriptSeq, 1), 10)
	sql.Register(name, w9bScriptDriver{script: script})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
