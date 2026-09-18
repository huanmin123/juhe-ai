package publicapilogs

// w14h 覆盖波次：capture 快照克隆/估算/JSON 辅助的剩余类型臂、pipeline 关闭
// 排空与重试打断路径、retention 收尾分支、store 的 SQLite/PG 事务错误臂。
//
// 已知不可达语句（登记备查）：
//   - capture.go boundedSnapshot 的 marshalCompact 错误臂（240-242）：克隆结果
//     只含 string/snapshotObject/map 等可序列化值，marshalCompact 不会失败。
//   - json.go appendJSONValue 的 *snapshotObject MarshalJSON 错误臂（146-148）
//     与 appendJSONString 的 json.Marshal 错误臂（203-205）：两者对自身输入
//     恒不返回错误。
//   - publicapilogs.go safeJSONObjectStringify 的 MarshalJSON 错误臂（212-214、
//     218-220）：同上，snapshotObject.MarshalJSON 恒成功。
//   - pipeline.go Enqueue 的 select default 分支（149-151）：queueLen 计数恒
//     不小于 channel 内条数，channel 满时容量检查（140）先命中。
//   - publicapilogs.go safeJSONObjectStringify 的 map marshalCompact 错误臂
//     （227-229）：map 分支的序列化缓冲以 "{" 开头，恒非 nil，不会报错。
//   - capture.go cloneSnapshotScalar 的 marshalCompact 错误臂（545-547）：标量
//     类型均恒可序列化。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var w14hPalBoom = errors.New("w14h pal boom")

func TestW14HCaptureHelperArms(t *testing.T) {
	// record：nil sink 直接返回 false 且只记录一次。
	capture := NewCapture(CaptureSpec{Method: "GET", Path: "/x", StartedAt: time.Now()}, nil)
	if capture.RecordFinish(200, nil) {
		t.Fatal("nil sink 必须返回 false")
	}
	if !capture.Recorded() {
		t.Fatal("record 后必须置位")
	}
	if capture.RecordClosed(200, nil) {
		t.Fatal("二次记录必须被拒绝")
	}
	// hasOwnEnumerableKey：非对象类型。
	if hasOwnEnumerableKey("w14h") {
		t.Fatal("标量必须无键")
	}
	// requestBodyRejectedReason：413 或 entity.too.large → too_large；其余 → parse_failed。
	tooLarge := &CaptureSpec{BodyRejected: &BodyRejection{StatusCode: 413, ErrorType: "entity.too.large"}}
	if got := requestBodyRejectedReason(tooLarge, 500); got != "request_body_too_large" {
		t.Fatalf("too large=%q", got)
	}
	parseFail := &CaptureSpec{BodyRejected: &BodyRejection{StatusCode: 400, ErrorType: "body.parse_failed"}}
	if got := requestBodyRejectedReason(parseFail, 413); got != "request_body_too_large" {
		t.Fatalf("413 rejected=%q", got)
	}
	if got := requestBodyRejectedReason(parseFail, 500); got != "request_body_parse_failed" {
		t.Fatalf("500 rejected=%q", got)
	}
	// contentLengthBytes：非法数字。
	if got := contentLengthBytes("w14h-not-a-number"); got != 0 {
		t.Fatalf("非法 content length=%d", got)
	}
	// objectGet：nil map / snapshotObject / 其他类型。
	if got := objectGet(map[string]any(nil), "k"); got != nil {
		t.Fatalf("nil map=%v", got)
	}
	object := newSnapshotObject().set("k", "v")
	if got := objectGet(object, "k"); got != "v" {
		t.Fatalf("snapshot get=%v", got)
	}
	if got := objectGet(42, "k"); got != nil {
		t.Fatalf("标量=%v", got)
	}
	// firstString：超长截断到上限。
	long := strings.Repeat("a", publicAPIErrorInfoMaxRunes+10)
	if got := firstString(long); len([]rune(got)) != publicAPIErrorInfoMaxRunes {
		t.Fatalf("firstString 截断长度=%d", len([]rune(got)))
	}
	// sliceUTF8 / maxInt64 / itoa。
	if got := sliceUTF8("abc", 0); got != "" {
		t.Fatalf("sliceUTF8 零上限=%q", got)
	}
	if got := maxInt64(1, 9); got != 9 {
		t.Fatalf("maxInt64=%d", got)
	}
	if got := maxInt64(9, 1); got != 9 {
		t.Fatalf("maxInt64 首臂=%d", got)
	}
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0)=%q", got)
	}
	// boundedSnapshot：负 size 钳制为 0（空快照路径直接返回钳制值）。
	snapshot := boundedSnapshot(newSnapshotObject().set("body", Undefined), -5)
	if snapshot.sizeBytes != 0 {
		t.Fatalf("负 size=%d", snapshot.sizeBytes)
	}
	// boundedSnapshotValue：maxBytes<1 钳制为 1。
	result := boundedSnapshotValue(newSnapshotObject().set("k", "vvvv"), 0)
	if !result.truncated {
		t.Fatalf("零预算必须截断：%v", result.value)
	}
}

func TestW14HCloneSnapshotValueArms(t *testing.T) {
	freshState := func(remaining int) *snapshotBudget {
		return &snapshotBudget{remainingBytes: remaining, seen: map[*snapshotObject]bool{}}
	}
	// 标量类型臂：int64 / json.Number / []byte / time.Time。
	state := freshState(1 << 20)
	for _, value := range []any{int64(7), float64(1.5), json.Number("8"), []byte("buf"), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)} {
		if cloneSnapshotValue(value, state, 0) == nil {
			t.Fatalf("标量克隆 %T 不能为 nil", value)
		}
	}
	// 预算耗尽后的入口截断。
	exhausted := freshState(0)
	if got := cloneSnapshotValue("x", exhausted, 0); got != "[truncated]" {
		t.Fatalf("耗尽入口=%v", got)
	}
	// 循环引用 → [Circular]。
	circular := newSnapshotObject()
	circular.set("self", circular)
	if got := cloneSnapshotValue(circular, freshState(1<<20), 0); got == circular {
		t.Fatal("循环引用必须被替换")
	}
	// 值类型 snapshotObject。
	if got := cloneSnapshotValue(*newSnapshotObject().set("a", 1), freshState(1<<20), 0); got == nil {
		t.Fatal("值类型克隆不能为 nil")
	}
	// 未知复合类型：可序列化 → JSON 文本；不可序列化 → [unavailable]。
	if got := cloneSnapshotValue(struct {
		A int `json:"a"`
	}{1}, freshState(1<<20), 0); got != `{"a":1}` {
		t.Fatalf("未知结构体=%v", got)
	}
	if got := cloneSnapshotValue(make(chan int), freshState(1<<20), 0); got != "[unavailable]" {
		t.Fatalf("不可序列化=%v", got)
	}
	// 数组超过条目上限 → 截断标记。
	big := make([]any, publicAPISnapshotMaxEntries+1)
	for index := range big {
		big[index] = 1
	}
	arrayResult := boundedSnapshotValue(big, 1<<20)
	encoded, err := json.Marshal(arrayResult.value)
	if err != nil {
		t.Fatal(err)
	}
	if !arrayResult.truncated || !strings.Contains(string(encoded), "[truncated]") {
		t.Fatalf("超 entries 数组=%s truncated=%v", encoded, arrayResult.truncated)
	}
	// 数组中途预算耗尽 → break。
	budgetBreak := cloneSnapshotValue([]any{"xxxxxxxx", "yyyyyyyy"}, freshState(4), 0)
	array, ok := budgetBreak.([]any)
	if !ok || len(array) != 2 || array[1] != "[truncated]" {
		t.Fatalf("预算中断数组=%v", budgetBreak)
	}
	// cloneSnapshotBuffer：超预览上限 / 负预算。
	if got := cloneSnapshotBuffer([]byte(strings.Repeat("b", publicAPISnapshotStringPreviewBytes+10)), freshState(1<<20)); got == nil {
		t.Fatal("buffer 克隆不能为 nil")
	}
	negativeBuffer := freshState(-3)
	cloneSnapshotBuffer([]byte("abc"), negativeBuffer)
	if got := cloneSnapshotString("value", freshState(-3)); !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("负预算字符串=%q", got)
	}
}

func TestW14HEstimateArms(t *testing.T) {
	base := Input{ID: "w14h-est", Method: "GET", Path: "/x", StatusCode: 200}
	// maxBytes / maxNodes 非正 → 取最大上限。
	if estimateInputBytes(base, 0, 10) <= 0 {
		t.Fatal("maxBytes<=0 仍需估算")
	}
	if estimateInputBytes(base, 100, 0) <= 0 {
		t.Fatal("maxNodes<=0 仍需估算")
	}
	// maxBytes 极小 → 入口即触顶。
	if got := estimateInputBytes(base, 1, 1<<20); got > 1 {
		t.Fatalf("触顶估算=%d", got)
	}
	newContext := func() *jsonLikeByteEstimateContext {
		return &jsonLikeByteEstimateContext{seen: map[*snapshotObject]bool{}, maxBytes: 1 << 20, maxNodes: 1 << 20}
	}
	// 标量与容器类型臂。
	context := newContext()
	for _, value := range []any{3.14, []byte("raw"), (*snapshotObject)(nil), map[string]any(nil), []any(nil), struct{ X int }{1}} {
		visitJSONLikeValue(value, context)
	}
	if context.total == 0 {
		t.Fatal("类型臂估算必须累计")
	}
	// 已访问 snapshotObject → 16 字节。
	seenContext := newContext()
	object := newSnapshotObject().set("k", 1)
	seenContext.seen[object] = true
	visitJSONLikeValue(object, seenContext)
	if seenContext.total != 16 {
		t.Fatalf("已见对象=%d", seenContext.total)
	}
	// snapshotObject 键循环中触顶 → return。
	limitObject := newSnapshotObject()
	for _, key := range []string{"k1", "k2", "k3"} {
		limitObject.set(key, strings.Repeat("v", 50))
	}
	objectLimit := &jsonLikeByteEstimateContext{seen: map[*snapshotObject]bool{}, maxBytes: 20, maxNodes: 1 << 20}
	visitJSONLikeValue(limitObject, objectLimit)
	if objectLimit.total != 20 {
		t.Fatalf("对象触顶=%d", objectLimit.total)
	}
	// []any 迭代中触顶 → return。
	sliceLimit := &jsonLikeByteEstimateContext{seen: map[*snapshotObject]bool{}, maxBytes: 12, maxNodes: 1 << 20}
	visitJSONLikeValue([]any{"aaaaaaaa", "bbbbbbbb"}, sliceLimit)
	if sliceLimit.total != 12 {
		t.Fatalf("数组触顶=%d", sliceLimit.total)
	}
	// addEstimatedBytes：负字节 / 触顶 / 越界钳制。
	byteContext := &jsonLikeByteEstimateContext{seen: map[*snapshotObject]bool{}, maxBytes: 100, total: 50}
	addEstimatedBytes(byteContext, -5)
	if byteContext.total != 50 {
		t.Fatalf("负字节=%d", byteContext.total)
	}
	byteContext.total = 100
	addEstimatedBytes(byteContext, 10)
	if byteContext.total != 100 {
		t.Fatalf("触顶后=%d", byteContext.total)
	}
	byteContext.total = 95
	addEstimatedBytes(byteContext, 10)
	if byteContext.total != 100 {
		t.Fatalf("越界钳制=%d", byteContext.total)
	}
	// utf16Length：增补平面字符计 2。
	if got := utf16Length("a😀"); got != 3 {
		t.Fatalf("utf16 长度=%d", got)
	}
	// estimateStringBytes：长字符串按 4 字节/字符估算。
	longContext := &jsonLikeByteEstimateContext{maxBytes: 1 << 20, maxNodes: 1 << 20}
	if got := estimateStringBytes(strings.Repeat("a", 17*1024), longContext); got != 17*1024*4 {
		t.Fatalf("长字符串估算=%d", got)
	}
}

func TestW14HJSONHelperArms(t *testing.T) {
	// nil 接收者的 get。
	var missing *snapshotObject
	if got := missing.get("k"); got != nil {
		t.Fatalf("nil get=%v", got)
	}
	if got, err := marshalCompact(nil); err != nil || got != "null" {
		t.Fatalf("marshal nil=%q", got)
	}
	if _, err := marshalCompact(make(chan int)); err == nil {
		t.Fatal("不可序列化必须报错")
	}
	if got := string(appendJSONValue(nil, float32(1.5))); got != "1.5" {
		t.Fatalf("float32=%q", got)
	}
	if got := string(appendJSONValue(nil, []byte("raw"))); !strings.Contains(got, `"type":"Buffer"`) {
		t.Fatalf("[]byte=%q", got)
	}
	if got, err := marshalCompact(map[string]any{"a": Undefined, "b": 1}); err != nil || got != `{"b":1}` {
		t.Fatalf("undefined 省略=%q", got)
	}
}

func TestW14HPublicapilogsHelperArms(t *testing.T) {
	// 值类型 snapshotObject 与 nil map。
	object := newSnapshotObject().set("a", 1)
	if got := safeJSONObjectStringify(*object); got != `{"a":1}` {
		t.Fatalf("值类型=%q", got)
	}
	if got := safeJSONObjectStringify(map[string]any(nil)); got != "{}" {
		t.Fatalf("nil map=%q", got)
	}
	// integerOrNull：json.Number 三分支。
	if got := integerOrNull(json.Number("12")); got != int64(12) {
		t.Fatalf("整数字符串=%v", got)
	}
	if got := integerOrNull(json.Number("1.75")); got != int64(1) {
		t.Fatalf("小数字符串=%v", got)
	}
	if got := integerOrNull(json.Number("w14h")); got != nil {
		t.Fatalf("非法字符串=%v", got)
	}
}

func TestW14HRetentionArms(t *testing.T) {
	// now/sleep 缺省回退。
	retention := NewRetention(&w14hCountingCleanupStore{}, func(context.Context) (map[string]any, error) {
		return map[string]any{"publicApiLogRetentionDays": 1}, nil
	}, nil, nil)
	if retention == nil {
		t.Fatal("缺省构造不能为 nil")
	}
	// sleep 错误终止循环（前一批删满触发 sleep）。
	sleepErr := errors.New("w14h sleep boom")
	countingStore := &w14hCountingCleanupStore{}
	errRetention := NewRetention(countingStore, func(context.Context) (map[string]any, error) {
		return map[string]any{"publicApiLogRetentionDays": 1}, nil
	}, nil, func(context.Context) error { return sleepErr })
	deleted, err := errRetention.RunOnce(context.Background())
	if !errors.Is(err, sleepErr) || deleted != RetentionBatchSize {
		t.Fatalf("sleep 错误=%d/%v", deleted, err)
	}
	// settingIntegerValue：int32 / int64 / 非法 json.Number。
	if value, err := SettingNumber(map[string]any{"k": int32(5)}, "k", 1, 10); err != nil || value != 5 {
		t.Fatalf("int32=%d/%v", value, err)
	}
	if value, err := SettingNumber(map[string]any{"k": int64(6)}, "k", 1, 10); err != nil || value != 6 {
		t.Fatalf("int64=%d/%v", value, err)
	}
	if _, err := SettingNumber(map[string]any{"k": json.Number("x")}, "k", 1, 10); err == nil {
		t.Fatal("非法 json.Number 必须报错")
	}
	if errNotInteger.Error() == "" {
		t.Fatal("错误消息不能为空")
	}
	// 默认 sleep 闭包：第一批删满触发默认 sleep，第二批不满结束。
	defaultSleepStore := &mockCleanupStore{deleted: []int{RetentionBatchSize, 5}}
	defaultSleep := NewRetention(defaultSleepStore, func(context.Context) (map[string]any, error) {
		return map[string]any{"publicApiLogRetentionDays": 1}, nil
	}, nil, nil)
	total, err := defaultSleep.RunOnce(context.Background())
	if err != nil || total != RetentionBatchSize+5 {
		t.Fatalf("默认 sleep=%d/%v", total, err)
	}
}

type w14hCountingCleanupStore struct{ calls int }

func (s *w14hCountingCleanupStore) CleanupBefore(context.Context, string, int) (int, error) {
	s.calls++
	return RetentionBatchSize, nil
}

func TestW14HSanitizeArms(t *testing.T) {
	// 不可解析 URL 原样返回。
	if got := sanitizeURLForLog("://w14h-invalid"); got != "://w14h-invalid" {
		t.Fatalf("不可解析=%q", got)
	}
	// 空路径按 "/" 处理后不命中 OAuth 路径。
	if got := sanitizeURLForLog("plain-value"); got != "plain-value" {
		t.Fatalf("纯文本=%q", got)
	}
	// 解析后路径为空按 "/" 处理。
	if got := sanitizeURLForLog("http://w14h.example"); got != "http://w14h.example" {
		t.Fatalf("空路径=%q", got)
	}
}

// ---------------------------------------------------------------------------
// store 事务错误臂：脚本化 driver
// ---------------------------------------------------------------------------

type w14hStoreStep struct {
	matcher       []string
	cols          []string
	rows          [][]driver.Value
	noRows        bool
	rowsErr       error
	eofErr        error
	execErr       error
	prepareErr    error
	affected      int64
	affectedErr   error
}

type w14hStoreScript struct {
	mu        sync.Mutex
	steps     []w14hStoreStep
	beginErr  error
	commitErr error
}

func (s *w14hStoreScript) take(query string) *w14hStoreStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.steps {
		step := &s.steps[index]
		matched := len(step.matcher) == 0
		for _, fragment := range step.matcher {
			if strings.Contains(query, fragment) {
				matched = true
				break
			}
		}
		if matched {
			return step
		}
	}
	return nil
}

type w14hStoreConn struct{ script *w14hStoreScript }

func (c *w14hStoreConn) Prepare(query string) (driver.Stmt, error) {
	if step := c.script.take(query); step != nil && step.prepareErr != nil {
		return nil, step.prepareErr
	}
	return &w14hStoreStmt{}, nil
}
func (c *w14hStoreConn) Close() error { return nil }

func (c *w14hStoreConn) Begin() (driver.Tx, error) {
	if err := c.script.beginErr; err != nil {
		return nil, err
	}
	return w14hStoreTx{script: c.script}, nil
}

func (c *w14hStoreConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if step := c.script.take(query); step != nil {
		if step.execErr != nil {
			return nil, step.execErr
		}
		if step.affectedErr != nil {
			return w14hStoreResult{affectedErr: step.affectedErr}, nil
		}
	}
	return w14hStoreResult{affected: 1}, nil
}

func (c *w14hStoreConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if step := c.script.take(query); step != nil {
		if step.rowsErr != nil {
			return nil, step.rowsErr
		}
		cols := step.cols
		if cols == nil {
			cols = []string{"id"}
		}
		return &w14hStoreRows{cols: cols, values: step.rows, eofErr: step.eofErr}, nil
	}
	return &w14hStoreRows{cols: []string{"id"}}, nil
}

type w14hStoreStmt struct{}

func (w14hStoreStmt) Close() error  { return nil }
func (w14hStoreStmt) NumInput() int { return -1 }
func (w14hStoreStmt) Exec([]driver.Value) (driver.Result, error) {
	return w14hStoreResult{affected: 1}, nil
}
func (w14hStoreStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, w14hPalBoom
}

type w14hStoreTx struct{ script *w14hStoreScript }

func (t w14hStoreTx) Commit() error   { return t.script.commitErr }
func (t w14hStoreTx) Rollback() error { return nil }

type w14hStoreResult struct {
	affected     int64
	affectedErr  error
}

func (r w14hStoreResult) LastInsertId() (int64, error) { return 0, nil }
func (r w14hStoreResult) RowsAffected() (int64, error) { return r.affected, r.affectedErr }

type w14hStoreRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w14hStoreRows) Columns() []string { return r.cols }
func (r *w14hStoreRows) Close() error      { return nil }
func (r *w14hStoreRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.eofErr != nil {
		return r.eofErr
	}
	return io.EOF
}
func (r *w14hStoreRows) Err() error { return nil }

var w14hStoreDriverSeq int64

type w14hStoreDriver struct{ script *w14hStoreScript }

func (d w14hStoreDriver) Open(string) (driver.Conn, error) {
	return &w14hStoreConn{script: d.script}, nil
}

func w14hStoreOpen(t *testing.T, script *w14hStoreScript) *sql.DB {
	t.Helper()
	name := "w14h-pal-store-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + strconv.FormatInt(atomic.AddInt64(&w14hStoreDriverSeq, 1), 10)
	sql.Register(name, w14hStoreDriver{script: script})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newW14HStore 构造挂接脚本 driver 的 Store。
func newW14HStore(t *testing.T, script *w14hStoreScript, postgres bool) *Store {
	t.Helper()
	store, err := NewStore(w14hStoreOpen(t, script), postgres, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW14HStoreFaultArms(t *testing.T) {
	ctx := context.Background()
	inputs := []Input{{ID: "w14h-store-1", Method: "GET", Path: "/x", StatusCode: 200}}
	// SQLite：begin 失败 / prepare 失败。
	if err := newW14HStore(t, &w14hStoreScript{beginErr: w14hPalBoom}, false).InsertBatch(ctx, inputs); err == nil {
		t.Fatal("sqlite begin 失败必须透传")
	}
	prepareFail := &w14hStoreScript{steps: []w14hStoreStep{{matcher: []string{"INSERT INTO"}, prepareErr: w14hPalBoom}}}
	if err := newW14HStore(t, prepareFail, false).InsertBatch(ctx, inputs); err == nil {
		t.Fatal("sqlite prepare 失败必须透传")
	}
	// PG：begin 失败。
	if err := newW14HStore(t, &w14hStoreScript{beginErr: w14hPalBoom}, true).InsertBatch(ctx, inputs); err == nil {
		t.Fatal("pg begin 失败必须透传")
	}
	// CleanupBefore 全链错误臂。
	cleanup := func(script *w14hStoreScript) (int, error) {
		deleted, err := newW14HStore(t, script, false).CleanupBefore(ctx, "2026-01-01", 5)
		return deleted, err
	}
	if _, err := cleanup(&w14hStoreScript{beginErr: w14hPalBoom}); err == nil {
		t.Fatal("cleanup begin 失败必须透传")
	}
	if _, err := cleanup(&w14hStoreScript{steps: []w14hStoreStep{{matcher: []string{"SELECT id FROM"}, rowsErr: w14hPalBoom}}}); err == nil {
		t.Fatal("cleanup 查询失败必须透传")
	}
	if _, err := cleanup(&w14hStoreScript{steps: []w14hStoreStep{{matcher: []string{"SELECT id FROM"}, cols: []string{"id"}, rows: [][]driver.Value{{"id-1"}, {nil}}}}}); err == nil {
		t.Fatal("cleanup 扫描失败必须透传")
	}
	if _, err := cleanup(&w14hStoreScript{steps: []w14hStoreStep{{matcher: []string{"SELECT id FROM"}, cols: []string{"id"}, rows: [][]driver.Value{{"id-1"}}, eofErr: w14hPalBoom}}}); err == nil {
		t.Fatal("cleanup 行迭代失败必须透传")
	}
	if _, err := cleanup(&w14hStoreScript{steps: []w14hStoreStep{
		{matcher: []string{"SELECT id FROM"}, cols: []string{"id"}, rows: [][]driver.Value{{"id-1"}}},
		{matcher: []string{"DELETE FROM"}, execErr: w14hPalBoom},
	}}); err == nil {
		t.Fatal("cleanup 删除失败必须透传")
	}
	if _, err := cleanup(&w14hStoreScript{
		commitErr: w14hPalBoom,
		steps:     []w14hStoreStep{{matcher: []string{"SELECT id FROM"}, cols: []string{"id"}, rows: [][]driver.Value{{"id-1"}}}},
	}); err == nil {
		t.Fatal("cleanup 提交失败必须透传")
	}
	deleted, err := cleanup(&w14hStoreScript{steps: []w14hStoreStep{
		{matcher: []string{"SELECT id FROM"}, cols: []string{"id"}, rows: [][]driver.Value{{"id-1"}}},
		{matcher: []string{"DELETE FROM"}, affectedErr: w14hPalBoom},
	}})
	if err != nil || deleted != 1 {
		t.Fatalf("RowsAffected 失败按 len(ids) 返回=%d/%v", deleted, err)
	}
	// ensureCtx：nil ctx 回退为 Background，limit<1 钳制。
	okScript := &w14hStoreScript{steps: []w14hStoreStep{{matcher: []string{"SELECT id FROM"}, noRows: true, cols: []string{"id"}}}}
	if _, err := newW14HStore(t, okScript, false).CleanupBefore(nil, "2026-01-01", 0); err != nil {
		t.Fatalf("nil ctx + limit<1=%v", err)
	}
}

// ---------------------------------------------------------------------------
// pipeline：默认配置、关闭排空与重试打断路径
// ---------------------------------------------------------------------------

// w14hPipelineWriter 支持"首次调用阻塞直至放行 + 前置失败"的批量写入。
type w14hPipelineWriter struct {
	mu        sync.Mutex
	calls     int
	failAll   bool
	failFirst int
	gate      chan struct{}
	batches   int
}

func newW14HPipelineWriter() *w14hPipelineWriter {
	return &w14hPipelineWriter{gate: make(chan struct{})}
}

func (w *w14hPipelineWriter) release() { close(w.gate) }

func (w *w14hPipelineWriter) blocked() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls > 0
}

func (w *w14hPipelineWriter) InsertBatch(ctx context.Context, inputs []Input) error {
	w.mu.Lock()
	w.calls++
	failAll, failFirst, call := w.failAll, w.failFirst, w.calls
	w.mu.Unlock()
	if failAll || call <= failFirst {
		return w14hPalBoom
	}
	select {
	case <-w.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches++
	return nil
}

func (w *w14hPipelineWriter) written() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.batches
}

func TestW14HPipelineArms(t *testing.T) {
	ctx := context.Background()
	// 默认 RetryDelay 回退与 nil writer 拒绝、nil 接收者 Close、nil ctx Close。
	clock := &manualClock{current: fixedTime(t, "2026-09-04T08:00:00Z")}
	defaults := NewPipeline(newW14HPipelineWriter(), Config{Now: clock.Now, QueueMaxItems: 8, QueueMaxBytes: 1 << 20, FlushBatchSize: 2, ShutdownMaxBatch: 4})
	defaults.Close(nil)
	var missing *Pipeline
	missing.Close(ctx)
	empty := NewPipeline(newW14HPipelineWriter(), Config{RetryDelay: 0, Now: clock.Now})
	if empty.cfg.RetryDelay != DefaultRetryDelay {
		t.Fatalf("RetryDelay 默认=%v", empty.cfg.RetryDelay)
	}
	empty.Close(ctx)
	// nil writer 的 Enqueue。
	if (&Pipeline{}).Enqueue(Input{ID: "w14h-nil-writer"}) {
		t.Fatal("nil writer 必须拒绝")
	}

	// 场景 A：写入失败 → 重试等待被 Close 打断 → 排空首写再失败返回。
	failing := newW14HPipelineWriter()
	failing.failAll = true
	failingPipeline := NewPipeline(failing, Config{QueueMaxItems: 8, QueueMaxBytes: 1 << 20, FlushBatchSize: 2, ShutdownMaxBatch: 4, RetryDelay: 30 * time.Second, Now: clock.Now})
	for index := 0; index < 3; index++ {
		if !failingPipeline.Enqueue(Input{ID: "w14h-fail-" + string(rune('a'+index)), Method: "GET", Path: "/x", StatusCode: 200}) {
			t.Fatal("入队必须成功")
		}
	}
	waitFor(t, 5*time.Second, failing.blocked, "首次批量写入")
	failingPipeline.Close(ctx)
	select {
	case <-failingPipeline.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("失败排空未结束")
	}
	if failingPipeline.Runtime().FlushFailureCount < 2 {
		t.Fatalf("失败计数=%d", failingPipeline.Runtime().FlushFailureCount)
	}

	// 场景 B：首写阻塞，Close 打断重试等待后排空全部残余。
	gated := newW14HPipelineWriter()
	gatedPipeline := NewPipeline(gated, Config{QueueMaxItems: 16, QueueMaxBytes: 1 << 20, FlushBatchSize: 2, ShutdownMaxBatch: 8, RetryDelay: 30 * time.Second, Now: clock.Now})
	for index := 0; index < 5; index++ {
		if !gatedPipeline.Enqueue(Input{ID: "w14h-gate-" + string(rune('a'+index)), Method: "GET", Path: "/x", StatusCode: 200}) {
			t.Fatal("入队必须成功")
		}
	}
	waitFor(t, 5*time.Second, gated.blocked, "首次批量写入阻塞")
	gated.release()
	gatedPipeline.Close(ctx)
	select {
	case <-gatedPipeline.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("排空未结束")
	}
	if written := gated.written(); written != 3 {
		t.Fatalf("排空批次数=%d（期望 3）", written)
	}
	if remaining := gatedPipeline.Runtime().QueueLength; remaining != 0 {
		t.Fatalf("排空后队列长度=%d", remaining)
	}

	// 场景 C：首写失败后进入长重试等待，Close 打断等待 → 排空写完剩余队列。
	retry := newW14HPipelineWriter()
	retry.failFirst = 1
	retryPipeline := NewPipeline(retry, Config{QueueMaxItems: 16, QueueMaxBytes: 1 << 20, FlushBatchSize: 2, ShutdownMaxBatch: 8, RetryDelay: 30 * time.Second, Now: clock.Now})
	for index := 0; index < 5; index++ {
		if !retryPipeline.Enqueue(Input{ID: "w14h-retry-" + string(rune('a'+index)), Method: "GET", Path: "/x", StatusCode: 200}) {
			t.Fatal("入队必须成功")
		}
	}
	waitFor(t, 5*time.Second, retry.blocked, "首次批量写入失败")
	// 首写失败不走 gate；Close 触发的排空写入需要 gate 放行，先释放避免死锁。
	retry.release()
	retryPipeline.Close(ctx)
	select {
	case <-retryPipeline.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("重试排空未结束")
	}
	if written := retry.written(); written != 3 {
		t.Fatalf("重试排空批次数=%d（期望 3）", written)
	}
	if remaining := retryPipeline.Runtime().QueueLength; remaining != 0 {
		t.Fatalf("重试排空后队列长度=%d", remaining)
	}
}
