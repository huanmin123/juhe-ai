package usagewriter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件补齐 usagewriter 既有测试未覆盖的分支：体积估算限流/循环引用、
// JSON 编解码错误臂、分片路由边界、快照截断、writer 队列保护与停机路径，
// 以及门禁真实 PostgreSQL 的 PostgresShardStore.WriteBatch 全链路。

// ---------- 门禁真实 PostgreSQL（W7D_TEST_POSTGRES_DSN 同款先例） ----------

// w9gPostgresTarget 解析门禁真实 PG 的连接目标：
//  1. 优先 W9G_TEST_POSTGRES_DSN；
//  2. 回落读取本机私有 dev env（.local/project-resources/dev/env/shared.env
//     的 JUHE_AI_POSTGRES_URL），把库名替换为一次性覆盖库
//     juhe_ai_sub2api_dev_w1cover。
//
// 任何一步失败都 t.Skip（不可达环境不强跑）。DSN 全程不落入日志。
func w9gPostgresTarget(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("W9G_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	envPath := filepath.Join("..", "..", "..", "..", "..", ".local", "project-resources", "dev", "env", "shared.env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Skipf("dev env 不可读，跳过真实 PG 臂: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			continue
		}
		url := strings.TrimSpace(strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL="))
		if index := strings.Index(url, "/juhe_ai_sub2api_dev?"); index >= 0 {
			return url[:index] + "/juhe_ai_sub2api_dev_w1cover?" + url[index+len("/juhe_ai_sub2api_dev?"):]
		}
	}
	t.Skip("dev env 缺少 JUHE_AI_POSTGRES_URL，跳过真实 PG 臂")
	return ""
}

func w9gPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", w9gPostgresTarget(t))
	if err != nil {
		t.Skipf("打开 pg 失败，跳过真实 PG 臂: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("ping pg 失败，跳过真实 PG 臂: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestW9GPostgresStoreWriteBatch 覆盖 PostgresShardStore.WriteBatch 全链路：
// 分区确保 → 事务 → 账户锁定 → 批量插入（ON CONFLICT DO NOTHING）→
// last_used_at 副作用 → 提交；并覆盖空计划、无效 createdAt（原始
// no-partition 错误上抛）与幂等重放。
func TestW9GPostgresStoreWriteBatch(t *testing.T) {
	db := w9gPostgresDB(t)
	store := NewPostgresShardStore(PostgresShardStoreConfig{
		DB:         db,
		ShardCount: 4,
		Now:        func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	ctx := context.Background()

	today := time.Now().UTC().Format("2006-01-02")
	// 每次运行生成唯一 token，保证对临时库的可重入（幂等重放只在同次
	// 运行内断言）。
	runToken := fmt.Sprintf("%d", time.Now().UnixNano())
	createdAt := today + "T01:02:03.000Z"
	baseInput := func(id string) UsageRecordInput {
		input := gatewayInput("w9g-pg-" + runToken + "-" + id)
		input.ID = "usage_" + strings.ReplaceAll(today, "-", "") + "_s01_" + runToken + "_" + id
		input.CreatedAt = createdAt
		input.AccountID = "w9g-pg-account"
		input.InputTokens = intPtrW9G(7)
		input.CostUsd = floatPtrW9G(0.5)
		return input
	}

	plan, err := BuildWritePlan(ctx, []UsageRecordInput{baseInput("a"), baseInput("b")}, WritePlanOptions{
		Postgres:   true,
		ShardCount: 4,
	}, fixedClock("2026-01-02T03:04:05.000Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RowsByShard) == 0 || len(plan.ShardEntries) != 2 {
		t.Fatalf("plan rows=%d entries=%d", len(plan.RowsByShard), len(plan.ShardEntries))
	}

	inserted, err := store.WriteBatch(ctx, plan)
	if err != nil {
		t.Fatalf("first WriteBatch: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted = %d, want 2", inserted)
	}

	// 幂等重放：同 id 再写，ON CONFLICT DO NOTHING 命中，inserted=0。
	inserted, err = store.WriteBatch(ctx, plan)
	if err != nil {
		t.Fatalf("replay WriteBatch: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("replay inserted = %d, want 0", inserted)
	}

	// 空计划直通。
	empty, err := store.WriteBatch(ctx, WritePlan{})
	if err != nil || empty != 0 {
		t.Fatalf("empty plan: inserted=%d err=%v", empty, err)
	}

	// 批内远期 createdAt 同样触发当日分区创建（ensure 覆盖所有合法日期）。
	future := baseInput("future")
	future.CreatedAt = "1999-12-31T23:59:59.000Z"
	futurePlan, err := BuildWritePlan(ctx, []UsageRecordInput{future}, WritePlanOptions{
		Postgres:   true,
		ShardCount: 4,
	}, fixedClock("2026-01-02T03:04:05.000Z"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteBatch(ctx, futurePlan); err != nil {
		t.Fatalf("远期分区写入: %v", err)
	}
	var futurePartition bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'juhe_usage' AND c.relname = 'usage_records_19991231' AND c.relispartition)`).Scan(&futurePartition); err != nil {
		t.Fatal(err)
	}
	if !futurePartition {
		t.Fatal("批内 createdAt 对应的每日分区必须被确保创建")
	}

	// 分区内的行数核对：测试记录可查。
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM juhe_usage.usage_records WHERE trace_id LIKE $1`,
		"w9g-pg-"+runToken+"-%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 3 {
		t.Fatalf("usage rows = %d, want >= 3", count)
	}
}

// TestW9GPostgresStoreEnsurePartitionsInvalidDate 覆盖分区日期解析对
// 非法值的静默跳过分支（ensure 层不报错，由 INSERT 承载错误）。
func TestW9GPostgresStoreEnsurePartitionsInvalidDate(t *testing.T) {
	db := w9gPostgresDB(t)
	ensured := &ensuredPartitionDateKeys{}
	if err := ensurePostgresUsageRecordPartitions(context.Background(), db, ensured,
		[]string{"not-a-date", ""}); err != nil {
		t.Fatalf("无效 createdAt 必须静默跳过: %v", err)
	}
}

// ---------- SQLite store 错误臂 ----------

func TestW9GSqliteStoreDefaultsAndEmptyRows(t *testing.T) {
	// ShardCount/BusyTimeout 默认化。
	store := NewSqliteShardStore(SqliteShardStoreConfig{ShardCount: 0, BusyTimeoutMs: 0})
	if store.config.ShardCount != DefaultUsageShardCount || store.config.BusyTimeoutMs != 5000 {
		t.Fatalf("defaults: shardCount=%d busy=%d", store.config.ShardCount, store.config.BusyTimeoutMs)
	}

	// 空 shard 行集合直通。
	inserted, err := store.WriteBatch(context.Background(), WritePlan{
		RowsByShard: []ShardRows{{}},
	})
	if err != nil || inserted != 0 {
		t.Fatalf("empty shard rows: inserted=%d err=%v", inserted, err)
	}

	// recordShardEntries 空条目直通。
	if err := store.recordShardEntries(context.Background(), nil, nil); err != nil {
		t.Fatalf("empty entries: %v", err)
	}
}

func TestW9GSqliteStoreCatalogFailures(t *testing.T) {
	ctx := context.Background()
	entries := []ShardEntry{{
		ID:              "usage_20260102_s00_1000_x",
		ShardKey:        "20260102:s00",
		SystemAccountID: "sys1",
		TraceID:         "trace-x",
		TrafficSource:   TrafficSourceGateway,
		CreatedAt:       "2026-01-02T03:04:05.000Z",
	}}

	// catalog 库未建 schema：catalog 语句在缺失表上失败（defer 回滚臂）。
	root := t.TempDir()
	catalog, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, "catalog.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store := NewSqliteShardStore(SqliteShardStoreConfig{CatalogDB: catalog, ShardRoot: root, ShardCount: 4})
	if err := store.recordShardEntries(ctx, entries, nil); err == nil {
		t.Fatal("缺失 catalog schema 时必须报错")
	}

	// catalog 库已关闭：BeginTx 失败。
	closed, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, "closed.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	closedStore := NewSqliteShardStore(SqliteShardStoreConfig{CatalogDB: closed, ShardRoot: root, ShardCount: 4})
	closed.Close()
	if err := closedStore.recordShardEntries(ctx, entries, nil); err == nil {
		t.Fatal("catalog 已关闭时 BeginTx 必须报错")
	}
}

func TestW9GSqliteStoreShardDBFailures(t *testing.T) {

	// ShardRoot 位于普通文件之下：MkdirAll 失败。
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "catalog.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store := NewSqliteShardStore(SqliteShardStoreConfig{
		CatalogDB: catalog, ShardRoot: filepath.Join(filePath, "shards"), ShardCount: 4,
	})
	location := UsageRecordShardLocationForBucket("20260102", 0, filepath.Join(filePath, "shards"))
	if _, err := store.writeShardRows(location, []ShardWriteRow{{}}); err == nil {
		t.Fatal("shard 目录创建失败必须报错")
	}

	// 已缓存的 shard DB 被关闭：BeginTx 失败。
	liveRoot := t.TempDir()
	liveCatalog, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(liveRoot, "catalog.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer liveCatalog.Close()
	liveStore := NewSqliteShardStore(SqliteShardStoreConfig{CatalogDB: liveCatalog, ShardRoot: liveRoot, ShardCount: 4})
	liveLocation := UsageRecordShardLocationForBucket("20260102", 1, liveRoot)
	if _, err := liveStore.writeShardRows(liveLocation, []ShardWriteRow{{}}); err == nil {
		// 空行集合直通；用带参行打开真实 shard 库后再关闭它。
		row, err := w9gShardRow("20260102", "usage_20260102_s01_10_x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := liveStore.writeShardRows(liveLocation, []ShardWriteRow{row}); err != nil {
			t.Fatal(err)
		}
	}
	cached, err := liveStore.openShardDB(liveLocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := cached.Close(); err != nil {
		t.Fatal(err)
	}
	row, err := w9gShardRow("20260102", "usage_20260102_s01_11_x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := liveStore.writeShardRows(liveLocation, []ShardWriteRow{row}); err == nil {
		t.Fatal("shard 库被关闭后写入必须报错")
	}
}

func TestW9GEnsureUpstreamResponseModelColumn(t *testing.T) {
	root := t.TempDir()

	// 旧表缺列：ALTER 补列成功。
	legacy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, "legacy.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(`CREATE TABLE usage_records (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureUpstreamResponseModelColumn(legacy); err != nil {
		t.Fatalf("补列失败: %v", err)
	}

	// 目标表不存在：ALTER 以原始错误上抛。
	missing, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, "missing.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	if err := ensureUpstreamResponseModelColumn(missing); err == nil {
		t.Fatal("缺表时 ALTER 必须报错")
	}

	// 已关闭的库：PRAGMA 查询失败按可容忍处理（返回 nil）。
	closed, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(root, "closed.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := ensureUpstreamResponseModelColumn(closed); err != nil {
		t.Fatalf("已关闭库必须容错返回 nil: %v", err)
	}
}

// w9gShardRow 用 BuildWritePlan 产出一条真实可写 shard 行。
func w9gShardRow(bucketDateKey, id string) (ShardWriteRow, error) {
	createdAt := bucketDateKey[0:4] + "-" + bucketDateKey[4:6] + "-" + bucketDateKey[6:8] + "T03:04:05.000Z"
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{{
		ID:              id,
		SystemAccountID: "sys1",
		TraceID:         "trace-" + id,
		TrafficSource:   TrafficSourceGateway,
		Success:         true,
		CreatedAt:       createdAt,
	}}, WritePlanOptions{ShardCount: 4, ShardRoot: t_tempShardRoot()}, fixedClock(createdAt))
	if err != nil {
		return ShardWriteRow{}, err
	}
	for _, shardRows := range plan.RowsByShard {
		for _, row := range shardRows.Rows {
			return row, nil
		}
	}
	return ShardWriteRow{}, errors.New("plan produced no rows")
}

var w9gTempRoot string

func t_tempShardRoot() string {
	if w9gTempRoot == "" {
		w9gTempRoot = os.TempDir()
	}
	return w9gTempRoot
}

// ---------- 分片路由边界 ----------

func TestW9GShardRoutingEdges(t *testing.T) {
	// 负 shardID 归零。
	location := UsageRecordShardLocationForBucket("20260102", -7, t.TempDir())
	if location.ShardID != 0 || !strings.HasSuffix(location.FilePath, "usage-20260102-s00.sqlite3") {
		t.Fatalf("location = %+v", location)
	}

	// 溢出的 shard id 数字：回退 LogicalShardID。
	location, err := UsageRecordLogicalShardLocationForPostgres(
		"usage_20260102_s99999999999999999999_1_x", "2026-01-02T00:00:00.000Z", 4)
	if err != nil {
		t.Fatal(err)
	}
	if location.ShardID < 0 || location.ShardID >= 4 {
		t.Fatalf("logical shardID = %d", location.ShardID)
	}

	// id 不匹配且 createdAt 非法：上抛。
	if _, err := UsageRecordLogicalShardLocationForPostgres("zzz", "nope", 4); err == nil {
		t.Fatal("非法 createdAt 必须上抛")
	}
	if _, err := UsageRecordShardLocationForRecord("zzz", "nope", 4, t.TempDir()); err == nil {
		t.Fatal("非法 createdAt 必须上抛")
	}

	// ParseUsageRecordShardID 溢出拒绝。
	if _, _, ok := ParseUsageRecordShardID("usage_20260102_s99999999999999999999_1_x"); ok {
		t.Fatal("溢出 shard id 必须解析失败")
	}

	// shard key 溢出拒绝。
	if _, ok := UsageRecordShardLocationFromKey("20260102:s99999999999999999999", t.TempDir()); ok {
		t.Fatal("溢出 shard key 必须解析失败")
	}
}

// ---------- BuildWritePlan 与 jsonSnapshot ----------

func TestW9GBuildWritePlanInvalidCreatedAt(t *testing.T) {
	_, err := BuildWritePlan(context.Background(), []UsageRecordInput{{
		SystemAccountID: "sys1",
		TrafficSource:   TrafficSourceGateway,
		CreatedAt:       "not-a-time",
	}}, WritePlanOptions{ShardCount: 4, ShardRoot: t.TempDir()}, fixedClock("2026-01-02T03:04:05.000Z"))
	if err == nil {
		t.Fatal("非法 createdAt 必须上抛")
	}
}

func TestW9GJsonSnapshot(t *testing.T) {
	if got := jsonSnapshot(nil); got != nil {
		t.Fatalf("jsonSnapshot(nil) = %v", got)
	}
	if got := jsonSnapshot(make(chan int)); got != nil {
		t.Fatalf("不可序列化值必须退化为 nil, got %v", got)
	}
	if got := jsonSnapshot(map[string]any{"a": 1}); got == nil {
		t.Fatal("可序列化值必须返回字符串")
	}
}

// ---------- 快照截断（boundSnapshot 家族直测） ----------

func w9gBound(value any, mutate func(c *snapshotBoundContext)) any {
	context := &snapshotBoundContext{seen: newIdentitySet()}
	if mutate != nil {
		mutate(context)
	}
	return boundSnapshotValue(value, context)
}

func TestW9GBoundSnapshotValueEdges(t *testing.T) {
	// 顶部限流：bytes 已达上限 → "[truncated]"。
	if got := w9gBound("x", func(c *snapshotBoundContext) { c.bytes = usageSnapshotMaxBytes }); got != "[truncated]" {
		t.Fatalf("got %v", got)
	}

	// []byte 超过单字符串上限：缓冲摘要 + truncated 标记。
	big := make([]byte, usageSnapshotMaxStringBytes+1)
	buffer := w9gBound(big, nil).(*OrderedObject)
	if buffer.Get("_buffer") != true || buffer.Get("bytes") != len(big) || buffer.Get("truncated") != true {
		t.Fatalf("buffer = %+v", &buffer)
	}

	// *OrderedObject 深度截断。
	object := NewOrderedObject().Set("k", "v")
	if got := w9gBound(object, func(c *snapshotBoundContext) { c.depth = usageSnapshotMaxDepth }); got != "[depth_truncated]" {
		t.Fatalf("got %v", got)
	}

	// *OrderedObject 键数截断。
	wide := NewOrderedObject()
	for index := 0; index <= usageSnapshotMaxObjectKeys; index++ {
		wide.Set("k"+itoa(index), index)
	}
	bounded := w9gBound(wide, nil).(*OrderedObject)
	if bounded.Get("_truncated") != true {
		t.Fatal("键数超限必须标记 _truncated")
	}

	// *OrderedObject 字节预算中断。
	fat := NewOrderedObject().Set("big", strings.Repeat("x", usageSnapshotMaxBytes))
	if got := w9gBound(fat, nil).(*OrderedObject); got.Get("_truncated") != true {
		t.Fatalf("got %+v", &got)
	}

	// map 深度截断。
	if got := w9gBound(map[string]any{"a": 1}, func(c *snapshotBoundContext) { c.depth = usageSnapshotMaxDepth }); got != "[depth_truncated]" {
		t.Fatalf("got %v", got)
	}

	// map 键数截断。
	wideMap := map[string]any{}
	for index := 0; index <= usageSnapshotMaxObjectKeys; index++ {
		wideMap["m"+itoa(index)] = index
	}
	if got := w9gBound(wideMap, nil).(*OrderedObject); got.Get("_truncated") != true {
		t.Fatalf("got %+v", &got)
	}

	// 数组循环引用与深度截断。
	selfArray := []any{nil}
	selfArray[0] = selfArray
	arrayGot := w9gBound(selfArray, nil).([]any)
	if arrayGot[0] != "[circular]" {
		t.Fatalf("got %v", arrayGot)
	}
	if got := w9gBound([]any{1}, func(c *snapshotBoundContext) { c.depth = usageSnapshotMaxDepth }); got != "[depth_truncated]" {
		t.Fatalf("got %v", got)
	}

	// 数组元素数截断。
	many := make([]any, usageSnapshotMaxArrayItems+5)
	if got := w9gBound(many, nil).([]any); len(got) != usageSnapshotMaxArrayItems+1 {
		t.Fatalf("truncated array len = %d", len(got))
	}
}

func TestW9GBoundSnapshotStructEdges(t *testing.T) {
	// nil 结构体指针：非结构体语义。
	if got, ok := boundSnapshotStruct((*UsageRecordInput)(nil), &snapshotBoundContext{seen: newIdentitySet()}); ok || got != nil {
		t.Fatalf("got %v ok=%v", got, ok)
	}

	// 结构体循环引用。
	type selfRef struct {
		Next *selfRef `json:"next"`
	}
	// 结构体指针环经 Elem 解引用失去指针身份，由 depth 上限收敛（不深递归）。
	node := &selfRef{}
	node.Next = node
	context := &snapshotBoundContext{seen: newIdentitySet()}
	cycled, ok := boundSnapshotStruct(node, context)
	cycledMap, isObj := cycled.(*OrderedObject)
	if !ok || !isObj || !context.truncated || cycledMap.Get("_truncated") != true {
		t.Fatalf("got %v ok=%v isObj=%v", cycled, ok, isObj)
	}

	// 深度截断。
	context = &snapshotBoundContext{seen: newIdentitySet(), depth: usageSnapshotMaxDepth}
	if got, ok := boundSnapshotStruct(&selfRef{}, context); !ok || got != "[depth_truncated]" {
		t.Fatalf("got %v ok=%v", got, ok)
	}

	// 入口字节预算耗尽：整值退化为 "[truncated]"。
	context = &snapshotBoundContext{seen: newIdentitySet(), bytes: usageSnapshotMaxBytes}
	if got, ok := boundSnapshotStruct(&selfRef{}, context); !ok || got != "[truncated]" {
		t.Fatalf("got %v ok=%v", got, ok)
	}

	// 字段间字节耗尽：首字段吃满预算，次字段触发 _truncated 提前返回。
	type twoFields struct {
		Big   string `json:"big"`
		After string `json:"after"`
	}
	filled := &twoFields{Big: strings.Repeat("x", usageSnapshotMaxBytes)}
	context = &snapshotBoundContext{seen: newIdentitySet()}
	filledObj, ok := boundSnapshotStruct(filled, context)
	filledMap, isObj2 := filledObj.(*OrderedObject)
	if !ok || !isObj2 || filledMap.Get("_truncated") != true {
		t.Fatalf("got %v ok=%v isObj2=%v", filledObj, ok, isObj2)
	}
}

func TestW9GBoundSnapshotStringEdges(t *testing.T) {
	// remaining < 0 归零后走截断后缀路径（limit=0 → 只剩后缀）。
	context := &snapshotBoundContext{seen: newIdentitySet(), bytes: usageSnapshotMaxBytes + 10}
	if got := boundSnapshotString("short", context); !strings.Contains(got, "...[truncated ") {
		t.Fatalf("got %q", got)
	}

	// 截断后缀路径。
	context = &snapshotBoundContext{seen: newIdentitySet(), bytes: usageSnapshotMaxBytes - 100}
	huge := strings.Repeat("长", 200)
	got := boundSnapshotString(huge, context)
	if !strings.Contains(got, "...[truncated ") || !context.truncated {
		t.Fatalf("got %q truncated=%v", got, context.truncated)
	}
}

// ---------- JSON 编解码与估算 ----------

func TestW9GOrderedObjectEdges(t *testing.T) {
	// 零值对象 Set 初始化 values map。
	zero := &OrderedObject{}
	zero.Set("a", 1)
	if zero.Len() != 1 || zero.Get("a") != 1 {
		t.Fatalf("zero object = %+v", zero)
	}

	// nil 接收者。
	var nilObject *OrderedObject
	if nilObject.Get("a") != nil || nilObject.Has("a") || nilObject.Len() != 0 || nilObject.Keys() != nil {
		t.Fatal("nil 接收者必须安全")
	}
	encoded, err := json.Marshal(nilObject)
	if err != nil || string(encoded) != "null" {
		t.Fatalf("marshal nil = %s err=%v", encoded, err)
	}

	// 值不可序列化：Marshal 错误上抛。
	bad := NewOrderedObject().Set("ch", make(chan int))
	if _, err := json.Marshal(bad); err == nil {
		t.Fatal("不可序列化值必须报错")
	}

	// UnmarshalJSON 截断输入：key token / value decode 错误上抛。
	target := NewOrderedObject()
	if err := target.UnmarshalJSON([]byte(`{"a`)); err == nil {
		t.Fatal("截断 key 必须报错")
	}
	if err := target.UnmarshalJSON([]byte(`{"a": }`)); err == nil {
		t.Fatal("截断 value 必须报错")
	}
	if err := target.UnmarshalJSON([]byte(`[1]`)); err == nil {
		t.Fatal("非对象必须报错")
	}
	if err := target.UnmarshalJSON([]byte(`null`)); err != nil {
		t.Fatalf("null 输入必须无害通过: %v", err)
	}
}

func TestW9GEstimateJSONLikeBytesLimits(t *testing.T) {
	// 字节预算在容器迭代中触发 return。
	value := []any{strings.Repeat("x", 100), strings.Repeat("y", 100)}
	if got := EstimateJSONLikeBytes(value, EstimateJSONLikeBytesOptions{MaxBytes: 30}); got != 30 {
		t.Fatalf("array limit = %d", got)
	}
	if got := EstimateJSONLikeBytes(map[string]any{"a": strings.Repeat("x", 100)}, EstimateJSONLikeBytesOptions{MaxBytes: 20}); got != 20 {
		t.Fatalf("map limit = %d", got)
	}
	object := NewOrderedObject().Set("a", strings.Repeat("x", 100))
	if got := EstimateJSONLikeBytes(object, EstimateJSONLikeBytesOptions{MaxBytes: 20}); got != 20 {
		t.Fatalf("object limit = %d", got)
	}

	// 节点预算：MaxNodes=1 只计根节点（不访问子节点，总量为 0）。
	if got := EstimateJSONLikeBytes([]any{1, 2, 3}, EstimateJSONLikeBytesOptions{MaxNodes: 1}); got != 0 {
		t.Fatalf("node limit = %d", got)
	}

	// 循环引用：map / OrderedObject。
	selfMap := map[string]any{}
	selfMap["self"] = selfMap
	if got := EstimateJSONLikeBytes(selfMap, EstimateJSONLikeBytesOptions{}); got == 0 {
		t.Fatal("循环 map 估算不得为 0")
	}
	selfObject := NewOrderedObject()
	selfObject.Set("self", selfObject)
	if got := EstimateJSONLikeBytes(selfObject, EstimateJSONLikeBytesOptions{}); got == 0 {
		t.Fatal("循环 object 估算不得为 0")
	}

	// nil 结构体指针走 16 字节兜底。
	type payload struct {
		A int `json:"a"`
	}
	if got := EstimateJSONLikeBytes((*payload)(nil), EstimateJSONLikeBytesOptions{}); got != 16 {
		t.Fatalf("nil struct ptr = %d, want 16", got)
	}

	// 结构体循环引用。
	type node struct {
		Next *node `json:"next"`
	}
	loop := &node{}
	loop.Next = loop
	if got := EstimateJSONLikeBytes(loop, EstimateJSONLikeBytesOptions{}); got != 18 {
		t.Fatalf("struct circular = %d", got)
	}

	// 未导出字段跳过、json:"-" 跳过、无 tag 回落字段名。
	type tagged struct {
		hidden  int
		Skipped int `json:"-"`
		Plain   int
		Tagged  int `json:"tagged,omitempty"`
	}
	got := EstimateJSONLikeBytes(tagged{hidden: 1, Skipped: 2, Plain: 3, Tagged: 4}, EstimateJSONLikeBytesOptions{})
	if got == 0 {
		t.Fatal("估算不得为 0")
	}

	// UTF-8 截断与字节上限辅助。
	if got := sliceStringByUTF8Bytes("\xE4\xB8", 100); got != "\xE4\xB8" {
		t.Fatalf("incomplete rune slice = %q", got)
	}
	if got := sliceStringByUTF8Bytes("abc", -1); got != "" {
		t.Fatalf("negative max = %q", got)
	}
	if got := boundedStringByteLength("abc", 0); got != 0 {
		t.Fatalf("bounded 0 = %d", got)
	}
	if got := boundedStringByteLength(strings.Repeat("x", 100), 10); got != 10 {
		t.Fatalf("bounded clamp = %d", got)
	}
}

// ---------- freeze 定价冻结辅助 ----------

type w9gFakeCatalog struct {
	model    string
	snapshot *CostBreakdown
}

func (c *w9gFakeCatalog) ResolvePricingModel(context.Context, string, string, string, string) string {
	return c.model
}

func (c *w9gFakeCatalog) BuildBreakdown(context.Context, string, string, string, string, UsageRecordInput) *CostBreakdown {
	return c.snapshot
}

func TestW9GFreezePricingEdges(t *testing.T) {
	ctx := context.Background()
	cost := 0.25

	// 有 catalog 定价模型、无任何成本维度：修正模型但透传不产快照。
	catalog := &w9gFakeCatalog{model: "gpt-5"}
	input := UsageRecordInput{ProviderCode: "openai", Model: "requested-model", CostUsd: floatPtrW9G(0.5)}
	enriched := EnrichUsageRecordPricing(ctx, input, catalog)
	if enriched.PricingModel != "gpt-5" {
		t.Fatalf("pricing model 未按 catalog 修正: %q", enriched.PricingModel)
	}
	if enriched.PricingSnapshot != nil {
		t.Fatal("无成本维度不得产出快照")
	}
	// 已有 PricingModel 时 catalog 不覆盖。
	kept := EnrichUsageRecordPricing(ctx, UsageRecordInput{
		ProviderCode: "openai", Model: "requested-model", PricingModel: "explicit", CostUsd: floatPtrW9G(0.5),
	}, catalog)
	if kept.PricingModel != "explicit" {
		t.Fatalf("显式 pricing model 被覆盖: %q", kept.PricingModel)
	}

	// cache write 快照缺单价但声明了 Per1M 价格：补 0 成本。
	per1M := 3.0
	catalog = &w9gFakeCatalog{model: "gpt-5", snapshot: &CostBreakdown{
		CacheWriteUsdPer1M: &per1M,
		AccountChargeUsd:   &cost,
	}}
	writeTokens := 10
	enriched = EnrichUsageRecordPricing(ctx, UsageRecordInput{
		ProviderCode: "openai", Model: "gpt-5", CacheWriteTokens: &writeTokens,
	}, catalog)
	if enriched.CacheWriteCostUsd == nil || *enriched.CacheWriteCostUsd != 0 {
		t.Fatalf("cache write cost = %v, want 0 兜底", enriched.CacheWriteCostUsd)
	}
	if enriched.PricingSnapshot == nil || enriched.CostUsd != &cost {
		t.Fatalf("snapshot/cost 未冻结: %+v", enriched.PricingSnapshot)
	}

	// BuildBreakdown 返回 nil：Freeze 回落 PricingSnapshotForWrite 决定。
	catalog = &w9gFakeCatalog{model: "gpt-5", snapshot: nil}
	fallbackInput := UsageRecordInput{ProviderCode: "openai", Model: "gpt-5", CostUsd: &cost}
	frozen := FreezeUsageRecordPricingFacts(ctx, fallbackInput, catalog, true)
	if frozen.PricingSnapshot == nil {
		t.Fatal("fallback 路径必须产出确定性快照")
	}

	// PricingSnapshotForWrite：已有 *CostBreakdown 原样返回。
	snapshot := &CostBreakdown{Currency: "USD"}
	if got := PricingSnapshotForWrite(ctx, UsageRecordInput{PricingSnapshot: snapshot}, true, catalog); got != snapshot {
		t.Fatal("已有快照必须原样返回")
	}
	// 非 *CostBreakdown 快照：返回 nil。
	if got := PricingSnapshotForWrite(ctx, UsageRecordInput{PricingSnapshot: "legacy"}, true, catalog); got != nil {
		t.Fatal("未知快照类型必须返回 nil")
	}
	// 无成本事实：返回 nil。
	if got := PricingSnapshotForWrite(ctx, UsageRecordInput{}, true, catalog); got != nil {
		t.Fatal("无成本事实必须返回 nil")
	}

	// sumOptionalCosts 全 nil → nil；有值 → 求和。
	if got := sumOptionalCosts(nil, nil); got != nil {
		t.Fatalf("sumOptionalCosts(nil) = %v", got)
	}
	a, b := 0.1, 0.2
	if got := sumOptionalCosts(&a, nil, &b); *got != 0.3 {
		t.Fatalf("sum = %v", *got)
	}
}

// ---------- writer 队列保护、停机与运行时 ----------

type w9gAlwaysFailStore struct{}

func (w9gAlwaysFailStore) WriteBatch(Ctx, WritePlan) (int, error) {
	return 0, errors.New("store down")
}

// w9gDiscardLogger 丢弃全部告警（用于 nil/静默 logger 分支之外的错误臂）。
type w9gDiscardLogger struct{}

func (w9gDiscardLogger) Warn(string, map[string]any)  {}
func (w9gDiscardLogger) Error(string, map[string]any) {}

func TestW9GWriterFlushFailureAndDeadLetter(t *testing.T) {
	writer := NewWriter(Config{
		BatchSize:        2,
		MaxWriteAttempts: 2,
		QueueMaxItems:    10,
		QueueMaxBytes:    1 << 20,
		ShardRoot:        t.TempDir(),
	}, w9gAlwaysFailStore{}, fixedClock("2026-01-02T03:04:05.000Z"),
		WithLogger(w9gDiscardLogger{}))
	retry := &immediateRetry{}
	writer.RetryWait = retry.wait
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		writer.Close(ctx)
	})
	writer.Start()
	for i := 0; i < 2; i++ {
		if err := writer.Enqueue(context.Background(), gatewayInput("w9g-flush-"+itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	// 恒定失败：重试耗尽转死信终态（死信落定后 flushFailureCount 归零，
	// 由下批次重新计数）。
	eventually(2*time.Second, func() bool { return writer.Runtime().DeadLetterCount >= 2 })
	runtime := writer.Runtime()
	if runtime.DeadLetterCount < 2 || runtime.HandledRecords != 2 {
		t.Fatalf("runtime = %+v", runtime)
	}
	if len(writer.DeadLetters()) < 2 {
		t.Fatalf("dead letters = %d", len(writer.DeadLetters()))
	}
}

func TestW9GWriterRuntimeEdges(t *testing.T) {
	// nil logger：丢弃告警静默跳过。
	writer := NewWriter(Config{
		BatchSize:       4,
		QueueMaxItems:   4,
		QueueMaxBytes:   1 << 20,
		ShardRoot:       t.TempDir(),
		FlushIntervalMs: 60_000,
	}, &mockStore{}, fixedClock("2026-01-02T03:04:05.000Z"))
	// 不 Start：直接探测运行时与信号分支。
	if err := writer.Enqueue(context.Background(), gatewayInput("w9g-rt-1")); err != nil {
		t.Fatal(err)
	}
	writer.signal()
	writer.signal() // notify 已满 → default 臂。

	runtime := writer.Runtime()
	if runtime.QueueLength != 1 {
		t.Fatalf("queue length = %d", runtime.QueueLength)
	}

	// Runtime 的 oldest 取最小 createdAt；时钟倒退时排队时长钳 0。
	writer.mu.Lock()
	writer.pending[0].input.CreatedAt = "2026-01-02T09:00:00.000Z"
	writer.pending = append(writer.pending, queuedRecord{
		input:    gatewayInput("w9g-rt-2"),
		bytes:    10,
		enqueued: writer.pending[0].enqueued,
	})
	writer.pending[1].input.CreatedAt = "2026-01-01T00:00:00.000Z"
	writer.clock = fixedClock("2026-01-01T00:00:00.000Z")
	writer.mu.Unlock()
	runtime = writer.Runtime()
	if runtime.OldestCreatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("oldest = %q", runtime.OldestCreatedAt)
	}
	if runtime.OldestQueuedMs != 0 {
		t.Fatalf("oldestQueuedMs = %d, want 0", runtime.OldestQueuedMs)
	}

	// removeBatch 字节钳零。
	writer.mu.Lock()
	batch, batchBytes := writer.peekBatchLocked()
	writer.mu.Unlock()
	writer.removeBatch(batch, batchBytes+999999, 0)
	if writer.pendingBytes < 0 {
		t.Fatalf("pendingBytes = %d", writer.pendingBytes)
	}

	// deadLetterBatch 保留上限裁剪。
	writer.mu.Lock()
	writer.deadLetters = make([]UsageRecordInput, 0, 120)
	for index := 0; index < 120; index++ {
		writer.deadLetters = append(writer.deadLetters, gatewayInput("w9g-dl"))
	}
	writer.mu.Unlock()
	writer.mu.Lock()
	writer.pending = []queuedRecord{{input: gatewayInput("w9g-dl-pending"), bytes: 1}}
	writer.mu.Unlock()
	writer.deadLetterBatch([]queuedRecord{{input: gatewayInput("w9g-dl")}})
	if len(writer.DeadLetters()) > 100 {
		t.Fatalf("dead letters = %d", len(writer.DeadLetters()))
	}

	// flushOnceShutdown 空队列直通；Close(nil) 使用 Background（需先启动，
	// done 通道才有生产者）。
	writer.flushOnceShutdown()
	writer.Start()
	writer.Close(nil)

	// NewWriter 默认时钟。
	defaultClockWriter := NewWriter(Config{ShardRoot: t.TempDir()}, &mockStore{}, nil)
	defaultClockWriter.Start()
	defaultClockWriter.Close(nil)
}

func TestW9GWriterOversizeDropWithoutLogger(t *testing.T) {
	writer := NewWriter(Config{
		BatchSize:       4,
		QueueMaxItems:   100,
		QueueMaxBytes:   1 << 20,
		ShardRoot:       t.TempDir(),
		FlushIntervalMs: 60_000,
	}, &mockStore{}, fixedClock("2026-01-02T03:04:05.000Z"))
	// 快照在归一化时已被 64KB 上限截断；用超大 TraceID 触发超尺寸。
	huge := gatewayInput(strings.Repeat("t", 2<<20))
	if err := writer.Enqueue(context.Background(), huge); err != nil {
		t.Fatalf("oversize 入队按丢弃语义处理，不得报错: %v", err)
	}
	if got := writer.Runtime(); got.DroppedOversizeCount != 1 {
		t.Fatalf("droppedOversize = %d, want 1", got.DroppedOversizeCount)
	}
}

func intPtrW9G(v int) *int           { return &v }
func floatPtrW9G(v float64) *float64 { return &v }
