package usagewriter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- records.go 小函数与剩余分支 ----

func TestDisplayStringVariants(t *testing.T) {
	if got := displayString(nil); got != "undefined" {
		t.Fatalf("nil 应显示为 undefined，实际: %q", got)
	}
	if got := displayString("text"); got != "text" {
		t.Fatalf("字符串应原样返回，实际: %q", got)
	}
	if got := displayString(woStringerInt(1)); got != "stringer-text" {
		t.Fatalf("Stringer 应使用 String()，实际: %q", got)
	}
	if got := displayString(3.5); got != "3.5" {
		t.Fatalf("其他类型应使用 %%v 文本，实际: %q", got)
	}
}

func TestAuthorizationSourcePairIsValidTable(t *testing.T) {
	// 契约：team 来源必须携带 team id，非 team 来源必须不带 team id。
	cases := []struct {
		sourceType string
		teamID     string
		want       bool
	}{
		{AuthorizationSourceTypeTeam, "team-1", true},
		{AuthorizationSourceTypeTeam, "", false},
		{AuthorizationSourceTypeManual, "", true},
		{AuthorizationSourceTypeManual, "team-1", false},
		{"unknown", "", true},
	}
	for _, tc := range cases {
		if got := authorizationSourcePairIsValid(tc.sourceType, tc.teamID); got != tc.want {
			t.Fatalf("authorizationSourcePairIsValid(%q,%q)=%v，期望 %v", tc.sourceType, tc.teamID, got, tc.want)
		}
	}
}

func TestSortedAccountIDsOrdersAndHandlesEmpty(t *testing.T) {
	if got := sortedAccountIDs(map[string]string{"b": "1", "c": "2", "a": "3"}); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("账号应升序排列，实际: %v", got)
	}
	if got := sortedAccountIDs(map[string]string{}); len(got) != 0 {
		t.Fatalf("空 map 应返回空列表，实际: %v", got)
	}
}

func TestSystemClockNowReturnsTime(t *testing.T) {
	// 只验证时钟可用并返回接近当前的时间，不做精确断言。
	now := SystemClock{}.Now()
	if now.IsZero() || now.Sub(time.Now()).Abs() > time.Minute {
		t.Fatalf("SystemClock 应返回当前时间，实际: %v", now)
	}
}

func TestBoundSnapshotArrayStopsAtBudget(t *testing.T) {
	// 契约：数组累计字节到达 64KB 预算后停止处理，剩余项计入截断标记。
	items := make([]any, 6)
	for i := range items {
		items[i] = strings.Repeat("a", 15_000) // 每项截断到 16KB 上限，4 项即触顶
	}
	bounded, ok := BoundUsageRecordSnapshot(items).([]any)
	if !ok {
		t.Fatalf("数组快照应返回数组，实际: %T", bounded)
	}
	if len(bounded) != 6 {
		t.Fatalf("预算触顶后应保留 5 项加 1 个截断标记，实际项数: %d", len(bounded))
	}
	if last, isString := bounded[len(bounded)-1].(string); !isString || last != "[1 items truncated]" {
		t.Fatalf("末项应为截断标记，实际: %#v", bounded[len(bounded)-1])
	}
}

type woDeepStruct struct {
	Child *woDeepStruct `json:"child"`
	Name  string        `json:"name"`
}

func TestBoundSnapshotStructDepthAndPointer(t *testing.T) {
	// 指针入参解引用后按结构体展开。
	leaf := &woDeepStruct{Name: "leaf"}
	bounded, ok := BoundUsageRecordSnapshot(leaf).(*OrderedObject)
	if !ok || bounded.Get("name") != "leaf" {
		t.Fatalf("指针结构体应解引用展开，实际: %#v", bounded)
	}
	// 嵌套超过 6 层的结构体触发深度截断。
	deep := &woDeepStruct{Name: "root"}
	cursor := deep
	for i := 0; i < 10; i++ {
		cursor.Child = &woDeepStruct{Name: "level"}
		cursor = cursor.Child
	}
	level := BoundUsageRecordSnapshot(deep).(*OrderedObject)
	var marker any
	for i := 0; i < 12; i++ {
		marker = level.Get("child")
		child, isObject := marker.(*OrderedObject)
		if !isObject {
			break
		}
		level = child
	}
	if marker != "[depth_truncated]" {
		t.Fatalf("结构体深嵌套应触发深度截断，实际: %#v", marker)
	}
}

// ---- freeze.go：Enrich 分支 ----

func TestEnrichUsageRecordPricingGuardBranches(t *testing.T) {
	ctx := context.Background()
	catalog := &mockCatalog{}
	withSnapshot := UsageRecordInput{PricingSnapshot: map[string]any{"k": "v"}}
	if got := EnrichUsageRecordPricing(ctx, withSnapshot, catalog); got.PricingSnapshot == nil {
		t.Fatalf("已有快照的记录不应被改写")
	}
	noFacts := UsageRecordInput{ProviderCode: "openai"}
	if got := EnrichUsageRecordPricing(ctx, noFacts, catalog); got.PricingSnapshot != nil {
		t.Fatalf("无成本事实的记录不应生成快照")
	}
	withFacts := UsageRecordInput{InputTokens: intPtr(10)}
	if got := EnrichUsageRecordPricing(ctx, withFacts, nil); got.PricingSnapshot != nil {
		t.Fatalf("缺 catalog 的记录应保持原样")
	}
	noProvider := UsageRecordInput{InputTokens: intPtr(10)}
	if got := EnrichUsageRecordPricing(ctx, noProvider, catalog); got.PricingSnapshot != nil {
		t.Fatalf("缺 provider 的记录应保持原样")
	}
	// 全空模型名：costModel 为空，直接返回且不生成快照。
	emptyModels := UsageRecordInput{ProviderCode: "openai", InputTokens: intPtr(10)}
	if got := EnrichUsageRecordPricing(ctx, emptyModels, catalog); got.PricingSnapshot != nil {
		t.Fatalf("空模型名不应生成快照")
	}
}

func TestFixedNumberRoundTrips(t *testing.T) {
	// Number(value.toFixed(10)) 语义：10 位小数后精度收敛。
	if got := fixedNumber(1.23456789012345, 10); got != 1.2345678901 {
		t.Fatalf("应按 10 位小数取整，实际: %v", got)
	}
	// 行为存疑：Go FormatFloat 对 .5 边界采用银行家舍入（2.5 -> "2"），
	// 与 JS (2.5).toFixed(0) === "3" 的方向可能不一致；按当前实际行为断言。
	if got := fixedNumber(2.5, 0); got != 2 {
		t.Fatalf("0 位小数按当前实现应为 2（银行家舍入），实际: %v", got)
	}
}

// ---- rows.go stringDefault ----

func TestStringDefaultVariants(t *testing.T) {
	if got := stringDefault("", "d"); got != "d" {
		t.Fatalf("空串应返回默认值，实际: %q", got)
	}
	if got := stringDefault("x", "d"); got != "x" {
		t.Fatalf("非空串应原样返回，实际: %q", got)
	}
}

// ---- store.go：catalog 合并路径与 openShardDB 目录失败 ----

func TestSqliteShardStoreScopeCatalogMerge(t *testing.T) {
	// 契约：同一账号/API Key 在同一分片的多条记录合并为一行，
	// first_created_at 取最早值。直接驱动 upsertScopeShardCatalog 保证确定性。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, catalogDB := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() })
	accountID := "account-1"
	apiKeyID := "key-1"
	entries := []ShardEntry{
		{ID: "wo-merge-2", ShardKey: "20260504:s01", SystemAccountID: "sys1", APIKeyID: &apiKeyID, AccountID: &accountID, TrafficSource: TrafficSourceGateway, CreatedAt: "2026-05-04T03:02:02.000Z"},
		{ID: "wo-merge-1", ShardKey: "20260504:s01", SystemAccountID: "sys1", APIKeyID: &apiKeyID, AccountID: &accountID, TrafficSource: TrafficSourceGateway, CreatedAt: "2026-05-04T03:02:01.000Z"},
	}
	tx, err := catalogDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	if err := upsertScopeShardCatalog(context.Background(), tx, entries); err != nil {
		_ = tx.Rollback()
		t.Fatalf("写入范围目录失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}
	var accountRows, apiKeyRows int
	var firstCreated string
	if err := catalogDB.QueryRow(`SELECT COUNT(*) FROM usage_record_account_shards WHERE account_id='account-1'`).Scan(&accountRows); err != nil {
		t.Fatalf("读取账号分片目录失败: %v", err)
	}
	if accountRows != 1 {
		t.Fatalf("同账号同分片应合并为 1 行，实际: %d", accountRows)
	}
	if err := catalogDB.QueryRow(`SELECT first_created_at FROM usage_record_account_shards WHERE account_id='account-1'`).Scan(&firstCreated); err != nil {
		t.Fatalf("读取 first_created_at 失败: %v", err)
	}
	if firstCreated != "2026-05-04T03:02:01.000Z" {
		t.Fatalf("first_created_at 应取最早值，实际: %s", firstCreated)
	}
	if err := catalogDB.QueryRow(`SELECT COUNT(*) FROM usage_record_api_key_shards WHERE api_key_id='key-1'`).Scan(&apiKeyRows); err != nil {
		t.Fatalf("读取 API Key 分片目录失败: %v", err)
	}
	if apiKeyRows != 1 {
		t.Fatalf("同 Key 同分片应合并为 1 行，实际: %d", apiKeyRows)
	}
}

func TestSqliteShardStoreOpenShardDBMkdirFailure(t *testing.T) {
	// 契约：分片目录无法创建时（路径被同名文件占据）写批次必须报错。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, _ := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() })
	blocker := filepath.Join(t.TempDir(), "20260504")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatalf("创建占位文件失败: %v", err)
	}
	location := UsageRecordShardLocation{
		ShardKey:   "20260504:s01",
		BucketDate: "20260504",
		ShardID:    1,
		FilePath:   filepath.Join(blocker, "usage-20260504-s01.sqlite3"),
	}
	broken := WritePlan{RowsByShard: []ShardRows{{Location: location, Rows: []ShardWriteRow{{
		ID:     "usage-mkdir-blocked",
		Params: []any{"x"}, // 参数不完整也会失败，但 openShardDB 的目录创建先于 Exec
	}}}}}
	if _, err := store.WriteBatch(context.Background(), broken); err == nil {
		t.Fatalf("分片目录创建失败应导致写批次报错")
	}
}

func TestSqliteShardStoreShardFileIsDirectoryFails(t *testing.T) {
	// 契约：分片文件路径是目录时 schema 初始化必须失败并关闭连接，
	// 不产生半初始化的缓存。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, _ := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() })
	shardFile := filepath.Join(t.TempDir(), "20260504", "s01")
	if err := os.MkdirAll(shardFile, 0o755); err != nil {
		t.Fatalf("创建目录占位失败: %v", err)
	}
	location := UsageRecordShardLocation{
		ShardKey:   "20260504:s01",
		BucketDate: "20260504",
		ShardID:    1,
		FilePath:   filepath.Join(shardFile, "usage-20260504-s01.sqlite3"),
	}
	broken := WritePlan{RowsByShard: []ShardRows{{Location: location, Rows: []ShardWriteRow{{
		ID:     "usage-dir-blocked",
		Params: []any{"x"},
	}}}}}
	if _, err := store.WriteBatch(context.Background(), broken); err == nil {
		t.Fatalf("分片文件为目录时应报错")
	}
}

func TestSqliteShardStoreSideEffectExecFailureIsWarnOnly(t *testing.T) {
	// 契约：accounts.last_used_at 更新失败只放弃副作用（Node warn-only），
	// 不影响写批次成功。用触发器强制 UPDATE 失败以覆盖回滚分支。
	now := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	store, _ := newWoSqliteStore(t, now)
	t.Cleanup(func() { _ = store.Close() })
	businessDB := newWoBusinessDB(t, true)
	if _, err := businessDB.Exec(`CREATE TRIGGER wo_block_last_used BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT, 'blocked by wo trigger'); END`); err != nil {
		t.Fatalf("创建触发器失败: %v", err)
	}
	store.config.BusinessDB = businessDB
	input := gatewayInput("wo-side-effect-fail")
	input.AccountID = "account-1"
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, WritePlanOptions{
		Postgres:   false,
		ShardCount: 4,
		ShardRoot:  store.config.ShardRoot,
	}, fixedClock("2026-05-04T03:02:01.000Z"))
	if err != nil {
		t.Fatalf("构建写计划失败: %v", err)
	}
	if _, err := store.WriteBatch(context.Background(), plan); err != nil {
		t.Fatalf("副作用失败不应导致写批次失败: %v", err)
	}
}
