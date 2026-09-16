package usagewriter

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- 快照边界（boundSnapshot* 家族）----

type woSnapshotStruct struct {
	Visible  string            `json:"visible"`
	Ignored  string            `json:"-"`
	hidden   string            // 未导出字段跳过
	NilPtr   *int              `json:"nilPtr"`
	SetPtr   *string           `json:"setPtr"`
	Mapping  map[string]any    `json:"mapping"`
	Moment   time.Time         `json:"moment"`
	AnyNil   any               `json:"anyNil"`
	AnyValue any               `json:"anyValue"`
	Nested   *woNestedSnapshot `json:"nested"`
}

type woNestedSnapshot struct {
	Name string `json:"name"`
}

type woStringerInt int

func (woStringerInt) String() string { return "stringer-text" }

type woPlainInt int

func TestBoundUsageRecordSnapshotStructFields(t *testing.T) {
	// 契约：结构体快照按 JSON 名和声明序展开；nil 指针/接口字段与
	// 未导出、`json:"-"` 字段像 Node undefined 一样跳过。
	set := "set"
	input := woSnapshotStruct{
		Visible:  "v",
		Ignored:  "i",
		hidden:   "h",
		SetPtr:   &set,
		Mapping:  map[string]any{"b": 1, "a": 2},
		Moment:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		AnyValue: 42,
		Nested:   &woNestedSnapshot{Name: "n"},
	}
	bounded, ok := BoundUsageRecordSnapshot(input).(*OrderedObject)
	if !ok {
		t.Fatalf("结构体快照应绑定为 OrderedObject，实际: %T", BoundUsageRecordSnapshot(input))
	}
	if got := strings.Join(bounded.Keys(), ","); got != "visible,setPtr,mapping,moment,anyValue,nested" {
		t.Fatalf("字段键序/过滤不符，实际: %s", got)
	}
	mapping, ok := bounded.Get("mapping").(*OrderedObject)
	if !ok {
		t.Fatalf("map 字段应绑定为 OrderedObject，实际: %T", bounded.Get("mapping"))
	}
	if got := strings.Join(mapping.Keys(), ","); got != "a,b" {
		t.Fatalf("map 键应排序保证确定性，实际: %s", got)
	}
	if bounded.Get("moment") != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("时间字段应格式化为毫秒 ISO，实际: %v", bounded.Get("moment"))
	}
	nested, ok := bounded.Get("nested").(*OrderedObject)
	if !ok || nested.Get("name") != "n" {
		t.Fatalf("嵌套结构体应递归展开，实际: %#v", bounded.Get("nested"))
	}
}

func TestBoundUsageRecordSnapshotScalarAndFallbacks(t *testing.T) {
	if BoundUsageRecordSnapshot(nil) != nil {
		t.Fatalf("nil 快照应返回 nil")
	}
	// []byte 走缓冲描述对象而不是字符串。
	buffer, ok := BoundUsageRecordSnapshot([]byte("xyz")).(*OrderedObject)
	if !ok || buffer.Get("_buffer") != true || buffer.Get("bytes") != 3 || buffer.Get("truncated") != false {
		t.Fatalf("[]byte 应绑定为缓冲描述对象，实际: %#v", buffer)
	}
	// 非 JSON 基元的非结构体类型走 displayString 兜底。
	if got := BoundUsageRecordSnapshot(woStringerInt(7)); got != "stringer-text" {
		t.Fatalf("Stringer 应使用 String() 文本，实际: %v", got)
	}
	if got := BoundUsageRecordSnapshot(woPlainInt(42)); got != "42" {
		t.Fatalf("非 Stringer 应使用 %%v 文本，实际: %v", got)
	}
}

func TestBoundUsageRecordSnapshotCircularAndDepth(t *testing.T) {
	// map/slice 循环引用：identitySet 命中后返回 [circular] 标记，不死循环。
	circularMap := map[string]any{}
	circularMap["self"] = circularMap
	if got := BoundUsageRecordSnapshot(circularMap).(*OrderedObject).Get("self"); got != "[circular]" {
		t.Fatalf("map 循环引用应标记 [circular]，实际: %#v", got)
	}
	// identitySet 修复后追踪 *OrderedObject 指针身份（对齐 Node WeakSet），
	// 对象循环引用直接收敛为 [circular]；此前靠深度上限终止属行为缺陷。
	circular := NewOrderedObject()
	circular.Set("self", circular)
	root := BoundUsageRecordSnapshot(circular).(*OrderedObject)
	if got := root.Get("self"); got != "[circular]" {
		t.Fatalf("对象循环引用应标记 [circular]，实际: %#v", got)
	}
	// 深度超过 6 层触发 [depth_truncated]：沿 child 链下钻，直到遇到字符串标记。
	deep := NewOrderedObject()
	cursor := deep
	for i := 0; i < 10; i++ {
		next := NewOrderedObject()
		cursor.Set("child", next)
		cursor = next
	}
	level := BoundUsageRecordSnapshot(deep).(*OrderedObject)
	var marker any
	for i := 0; i < 10; i++ {
		marker = level.Get("child")
		child, isObject := marker.(*OrderedObject)
		if !isObject {
			break
		}
		level = child
	}
	if marker != "[depth_truncated]" {
		t.Fatalf("10 层内应触发深度截断，实际: %#v", marker)
	}
}

func TestBoundUsageRecordSnapshotStringAndArrayLimits(t *testing.T) {
	// 超过 16KB 的字符串截断并带明确后缀。
	huge := strings.Repeat("中", 10_000) // 30000 字节
	boundedText, ok := BoundUsageRecordSnapshot(huge).(string)
	if !ok {
		t.Fatalf("字符串快照应返回字符串，实际: %T", boundedText)
	}
	if !strings.HasSuffix(boundedText, " bytes]") || !strings.Contains(boundedText, "...[truncated ") {
		t.Fatalf("超长字符串应带截断后缀，实际长度: %d 结尾: %q", len(boundedText), boundedText[max(0, len(boundedText)-40):])
	}
	// 数组超过 50 项时保留前 50 项并追加计数标记。
	items := make([]any, 60)
	for i := range items {
		items[i] = i
	}
	array, ok := BoundUsageRecordSnapshot(items).([]any)
	if !ok {
		t.Fatalf("数组快照应返回数组，实际: %T", array)
	}
	if len(array) != 51 {
		t.Fatalf("应保留 50 项加 1 个截断标记，实际: %d", len(array))
	}
	if array[50] != "[10 items truncated]" {
		t.Fatalf("截断标记应注明丢弃数量，实际: %v", array[50])
	}
}

// ---- SqliteShardStore：业务侧副作用与错误路径 ----

func newWoSqliteStore(t *testing.T, now time.Time) (*SqliteShardStore, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	catalogDB := openStoreDB(t, filepath.Join(dir, "catalog.sqlite3"))
	store := NewSqliteShardStore(SqliteShardStoreConfig{
		CatalogDB:  catalogDB,
		ShardRoot:  dir,
		ShardCount: 4,
		Now:        func() time.Time { return now },
	})
	if err := store.EnsureCatalogSchema(); err != nil {
		t.Fatalf("初始化 catalog schema 失败: %v", err)
	}
	return store, catalogDB
}

func newWoBusinessDB(t *testing.T, withAccountsTable bool) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db := openStoreDB(t, filepath.Join(dir, "business.sqlite3"))
	if withAccountsTable {
		if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, last_used_at TEXT, updated_at TEXT, deleted_at TEXT)`); err != nil {
			t.Fatalf("建 accounts 表失败: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO accounts (id) VALUES ('account-1')`); err != nil {
			t.Fatalf("插入 accounts 失败: %v", err)
		}
	}
	return db
}

func TestSqliteShardStoreBusinessSideEffect(t *testing.T) {
	// 契约：WriteBatch 成功后把 accounts.last_used_at 推进到记录时间；
	// 业务库缺表时副作用只静默跳过，不影响写批次成功。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, _ := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() }) // Windows 下必须先关闭分片句柄再删临时目录
	businessDB := newWoBusinessDB(t, true)
	store.config.BusinessDB = businessDB
	input := gatewayInput("wo-side-effect")
	input.AccountID = "account-1"
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, WritePlanOptions{
		Postgres:   false,
		ShardCount: 4,
		ShardRoot:  store.config.ShardRoot,
	}, fixedClock("2026-05-04T03:02:01.000Z"))
	if err != nil {
		t.Fatalf("构建写计划失败: %v", err)
	}
	inserted, err := store.WriteBatch(context.Background(), plan)
	if err != nil {
		t.Fatalf("写批次不应报错: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("首次写入应插入 1 行，实际: %d", inserted)
	}
	var lastUsed sql.NullString
	if err := businessDB.QueryRow(`SELECT last_used_at FROM accounts WHERE id='account-1'`).Scan(&lastUsed); err != nil {
		t.Fatalf("读取 last_used_at 失败: %v", err)
	}
	if !lastUsed.Valid || lastUsed.String != "2026-05-04T03:02:01.000Z" {
		t.Fatalf("last_used_at 应推进到记录时间，实际: %v", lastUsed)
	}
	// 重复写同一批：ON CONFLICT(id) DO NOTHING 不重复计数。
	insertedAgain, err := store.WriteBatch(context.Background(), plan)
	if err != nil {
		t.Fatalf("重复写批次不应报错: %v", err)
	}
	if insertedAgain != 0 {
		t.Fatalf("重复写入应计 0 行，实际: %d", insertedAgain)
	}
}

func TestSqliteShardStoreSideEffectMissingTableIsWarnOnly(t *testing.T) {
	// 契约：业务库没有 accounts 表时副作用跳过（Node queryOnly 语义），
	// 写批次仍然成功。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, _ := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() }) // Windows 下必须先关闭分片句柄再删临时目录
	store.config.BusinessDB = newWoBusinessDB(t, false)
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{gatewayInput("wo-warn-only")}, WritePlanOptions{
		Postgres:   false,
		ShardCount: 4,
		ShardRoot:  store.config.ShardRoot,
	}, fixedClock("2026-05-04T03:02:01.000Z"))
	if err != nil {
		t.Fatalf("构建写计划失败: %v", err)
	}
	if _, err := store.WriteBatch(context.Background(), plan); err != nil {
		t.Fatalf("副作用缺表不应导致写批次失败: %v", err)
	}
}

func TestSqliteShardStoreWriteBatchRejectsBrokenRow(t *testing.T) {
	// 契约：参数个数损坏的行必须报错并回滚分片事务，不能静默丢数据。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, _ := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() }) // Windows 下必须先关闭分片句柄再删临时目录
	location := UsageRecordShardLocationForBucket("20260504", 1, store.config.ShardRoot)
	broken := WritePlan{
		RowsByShard: []ShardRows{{Location: location, Rows: []ShardWriteRow{{
			ID:        "usage_broken",
			Params:    []any{"only-one-param"}, // 与列数不符
			CreatedAt: "2026-05-04T03:02:01.000Z",
		}}}},
	}
	if _, err := store.WriteBatch(context.Background(), broken); err == nil {
		t.Fatalf("损坏的行参数应导致写批次失败")
	}
}

func TestSqliteShardStoreHelpers(t *testing.T) {
	// mergeFirstLast 只向更早的 createdAt 收敛。
	existing := ShardEntry{CreatedAt: "2026-01-02T00:00:00.000Z"}
	mergeFirstLast(&existing, ShardEntry{CreatedAt: "2026-01-01T00:00:00.000Z"})
	if existing.CreatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("更早的 createdAt 应被采纳，实际: %s", existing.CreatedAt)
	}
	mergeFirstLast(&existing, ShardEntry{CreatedAt: "2026-01-03T00:00:00.000Z"})
	if existing.CreatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("更晚的 createdAt 不应回退，实际: %s", existing.CreatedAt)
	}
	// sortedAccountIDList 升序去重输入 map。
	ids := sortedAccountIDList(map[string]bool{"b": true, "a": true, "c": true})
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("账号列表应升序，实际: %v", ids)
	}
	// trimValue 处理 nil 与空白。
	empty := ""
	if got := trimValue(nil); got != "" {
		t.Fatalf("nil 指针应得空串，实际: %q", got)
	}
	blank := "  "
	_ = empty
	if got := trimValue(&blank); got != "" {
		t.Fatalf("空白应被裁剪，实际: %q", got)
	}
}

// ---- PostgresShardStore：无需真实 PG 的构造与空批路径 ----

func TestPostgresShardStoreDefaultsAndEmptyBatch(t *testing.T) {
	store := NewPostgresShardStore(PostgresShardStoreConfig{})
	if store == nil {
		t.Fatalf("构造器应返回实例")
	}
	// 空批次直接返回 0，不触碰数据库连接。
	inserted, err := store.WriteBatch(context.Background(), WritePlan{})
	if err != nil || inserted != 0 {
		t.Fatalf("空批次应返回 (0,nil)，实际: (%d,%v)", inserted, err)
	}
}

// ---- Writer：ID 工厂注入 / Drain / 固定退避 / Start 守卫 ----

func TestWriterWithIDFactoryOption(t *testing.T) {
	// 契约：WithIDFactory 替换默认 ID 工厂，写入行的 ID 来自注入工厂。
	store := &mockStore{}
	writer, _ := newTestWriter(t, Config{BatchSize: 10, QueueMaxItems: 10, QueueMaxBytes: 1 << 20}, store,
		WithIDFactory(IDFactoryFunc(func(createdAt string) string { return "usage_wo_fixed" })))
	if err := writer.Enqueue(context.Background(), gatewayInput("wo-id-factory")); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	writer.Drain()
	if store.totalRows() != 1 {
		t.Fatalf("应写入 1 行，实际: %d", store.totalRows())
	}
	traceIDs := store.recordedTraceIDs()
	if len(traceIDs) != 1 || traceIDs[0] != "wo-id-factory" {
		t.Fatalf("记录应抵达 mock store，实际: %v", traceIDs)
	}
	plan := store.snapshotPlan()
	if plan == nil || len(plan.ShardEntries) != 1 || plan.ShardEntries[0].ID != "usage_wo_fixed" {
		t.Fatalf("注入工厂的 ID 应出现在目录条目中，实际: %#v", plan)
	}
}

func TestWriterDrainWithoutStartFlushesSynchronously(t *testing.T) {
	// 契约：Drain 同步清空队列但不停止 writer（区别于 Close）。
	store := &mockStore{}
	writer := NewWriter(Config{BatchSize: 10}, store, fixedClock("2026-01-02T03:04:05.000Z"),
		WithIDFactory(IDFactoryFunc(func(string) string { return "usage_wo_drain" })))
	// 未 Start 的 writer 没有后台 goroutine，用已取消的 context 让 Close 立即返回。
	closedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(func() { writer.Close(closedCtx) })
	if err := writer.Enqueue(context.Background(), gatewayInput("wo-drain-1")); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if writer.PendingCount() != 1 {
		t.Fatalf("Start 之前记录应留在队列，实际: %d", writer.PendingCount())
	}
	writer.Drain()
	if writer.PendingCount() != 0 {
		t.Fatalf("Drain 后队列应为空，实际: %d", writer.PendingCount())
	}
	writer.Drain() // 空队列直接返回
	if store.totalRows() != 1 {
		t.Fatalf("应恰好写 1 行，实际: %d", store.totalRows())
	}
}

func TestWriterDrainStopsOnPersistentFailure(t *testing.T) {
	// 契约：Drain 遇到持续失败时保留批次并停止 drain（retryOnFailure=false）。
	store := &mockStore{failures: 5, overflowErr: errors.New("wo 持续失败")}
	writer := NewWriter(Config{BatchSize: 10}, store, fixedClock("2026-01-02T03:04:05.000Z"),
		WithIDFactory(IDFactoryFunc(func(string) string { return "usage_wo_drain_fail" })), WithLogger(&captureLogger{}))
	// 未 Start 的 writer 没有后台 goroutine，用已取消的 context 让 Close 立即返回。
	closedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(func() { writer.Close(closedCtx) })
	if err := writer.Enqueue(context.Background(), gatewayInput("wo-drain-fail")); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	writer.Drain()
	if writer.PendingCount() != 1 {
		t.Fatalf("失败批次应保留在队列，实际: %d", writer.PendingCount())
	}
	if writer.Runtime().FlushFailureCount != 1 {
		t.Fatalf("Drain 应恰好尝试一次，实际失败计数: %d", writer.Runtime().FlushFailureCount)
	}
}

func TestWriterFixedRetryDelayRetriesThenRecovers(t *testing.T) {
	// 契约：未注入 RetryWait 时使用固定退避（RetryDelayMs），恢复后批次落库。
	store := &mockStore{failures: 1, overflowErr: errors.New("wo 首次失败")}
	writer := NewWriter(Config{BatchSize: 10, RetryDelayMs: 10}, store, fixedClock("2026-01-02T03:04:05.000Z"),
		WithIDFactory(IDFactoryFunc(func(string) string { return "usage_wo_retry" })), WithLogger(&captureLogger{}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		writer.Close(ctx)
	})
	writer.Start()
	if err := writer.Enqueue(context.Background(), gatewayInput("wo-retry")); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if !eventuallyTrue(2*time.Second, func() bool { return store.totalRows() == 1 }) {
		t.Fatalf("固定退避后批次应成功落库，实际行数: %d", store.totalRows())
	}
	if writer.Runtime().FlushFailureCount != 0 {
		t.Fatalf("成功后失败计数应清零，实际: %d", writer.Runtime().FlushFailureCount)
	}
}

func TestWriterWaitRetryAbortsOnStop(t *testing.T) {
	// 契约：固定退避等待期间 Close 必须中止等待并结束 flush goroutine。
	store := &mockStore{failures: 1000, overflowErr: errors.New("wo 一直失败")}
	writer := NewWriter(Config{BatchSize: 10, RetryDelayMs: 60_000}, store, fixedClock("2026-01-02T03:04:05.000Z"))
	writer.Start()
	if err := writer.Enqueue(context.Background(), gatewayInput("wo-stop-retry")); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if !eventuallyTrue(2*time.Second, func() bool { return writer.Runtime().FlushFailureCount == 1 }) {
		t.Fatalf("批次应进入一次失败等待，实际: %d", writer.Runtime().FlushFailureCount)
	}
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		writer.Close(ctx)
		close(done)
	}()
	select {
	case <-done:
		// Close 必须在退避等待中止后返回，而不是等满 60 秒。
	case <-time.After(5 * time.Second):
		t.Fatalf("Close 应中止退避等待及时返回")
	}
}

func TestWriterStartIsIdempotentAndRespectsStop(t *testing.T) {
	store := &mockStore{}
	writer := NewWriter(Config{}, store, fixedClock("2026-01-02T03:04:05.000Z"))
	writer.Start()
	writer.Start() // 重复 Start 不应再启动第二个 flush goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	writer.Close(ctx)
	writer.Start() // 停止后 Start 必须是 no-op
	if err := writer.Enqueue(context.Background(), gatewayInput("wo-after-stop")); err == nil {
		t.Fatalf("停止后的 Enqueue 应被拒绝")
	}
}

// ---- 测试辅助 ----

func eventuallyTrue(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return condition()
}

// snapshotPlan 返回 mock store 记录的首个写计划。
func (m *mockStore) snapshotPlan() *WritePlan {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.batches) == 0 {
		return nil
	}
	plan := m.batches[0]
	return &plan
}
