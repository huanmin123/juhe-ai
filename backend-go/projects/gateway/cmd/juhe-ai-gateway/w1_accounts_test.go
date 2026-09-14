package main

// w1: chain_accounts.go 收割——选择器构造守卫、SQLite/PostgreSQL 方言
// 投影（table/bind）、候选行合并与派发排序（模型秩/回退/超优先/优先级/
// 桶内质量决胜/中文整理排序，Node orderGatewayDispatchCandidateRowsForDispatch）。

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func w1CandidateRow(id string) *chainCandidateRow {
	return &chainCandidateRow{ID: id, AccountID: "", Name: id, Priority: 5}
}

func TestW1AccountsSelectorConstruction(t *testing.T) {
	// nil 数据库：报错。
	if _, err := newChainAccountsSelectorWithStats(nil, nil, false, "secret", nil, 20); err == nil {
		t.Fatal("nil db 必须报错")
	}
	// 限额越界：报错。
	fixture := newChainFixture(t)
	if _, err := newChainAccountsSelectorWithStats(fixture.db, fixture.statsDB, false, "secret", nil, 0); err == nil {
		t.Fatal("限额 0 必须报错")
	}
	if _, err := newChainAccountsSelectorWithStats(fixture.db, fixture.statsDB, false, "secret", nil, 50001); err == nil {
		t.Fatal("限额超上界必须报错")
	}
	// 缺席 statsDB / now：回退业务库与系统时钟。
	selector, err := newChainAccountsSelectorWithStats(fixture.db, nil, false, "secret", nil, 20)
	if err != nil || selector.statsDB != fixture.db || selector.now == nil {
		t.Fatalf("selector = %+v, %v", selector, err)
	}
	// 扫描限额 = 终限 × 2。
	if got := dispatchCandidateScanLimitFor(20); got != 40 {
		t.Fatalf("scan limit = %d", got)
	}
	// 方言投影：SQLite 前缀名直用，PostgreSQL 加 schema 前缀。
	sqliteSelector, _ := newChainAccountsSelector(fixture.db, false, "secret", nil, 20)
	if got := sqliteSelector.table("api_keys"); got != "api_keys" {
		t.Fatalf("sqlite table = %q", got)
	}
	if got := sqliteSelector.statsTable("usage_daily"); got != "usage_daily" {
		t.Fatalf("sqlite stats table = %q", got)
	}
	plainQuery := "SELECT * FROM api_keys WHERE id = ?"
	if got := sqliteSelector.bind(plainQuery); got != plainQuery {
		t.Fatalf("sqlite bind = %q", got)
	}
	pgSelector, _ := newChainAccountsSelector(fixture.db, true, "secret", nil, 20)
	if got := pgSelector.table("api_keys"); got != "juhe_business.api_keys" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pgSelector.statsTable("usage_daily"); !strings.HasPrefix(got, "juhe_stats.") && !strings.HasSuffix(got, "usage_daily") {
		t.Fatalf("pg stats table = %q", got)
	}
	pgBound := pgSelector.bind("INSERT INTO t(a,b,c) VALUES (?,?,?)")
	if !strings.Contains(pgBound, "$1") || !strings.Contains(pgBound, "$3") || strings.Contains(pgBound, "?") {
		t.Fatalf("pg bind = %q", pgBound)
	}
}

func TestW1MergeChainCandidateRows(t *testing.T) {
	// 按账户去重：preferred 优先、fallback 补位、空 AccountID 回落 ID。
	preferred := []chainCandidateRow{
		{ID: "row_1", AccountID: "acc_1"},
		{ID: "row_2", AccountID: ""},
	}
	fallback := []chainCandidateRow{
		{ID: "row_1", AccountID: "acc_1"},
		{ID: "row_2", AccountID: ""},
		{ID: "row_3", AccountID: "acc_3"},
	}
	merged := mergeChainCandidateRows(preferred, fallback)
	if len(merged) != 3 {
		t.Fatalf("merged = %d", len(merged))
	}
	if merged[0].ID != "row_1" || merged[1].ID != "row_2" || merged[2].ID != "row_3" {
		t.Fatalf("order = %s/%s/%s", merged[0].ID, merged[1].ID, merged[2].ID)
	}
}

func TestW1OrderChainCandidateRowsForDispatch(t *testing.T) {
	// 优先级取值：本地覆盖优先于默认。
	localRow := &chainCandidateRow{ID: "local", LocalPriority: sql.NullInt64{Valid: true, Int64: 2}}
	defaultRow := &chainCandidateRow{ID: "default", Priority: 7}
	if got := chainPriorityRank(localRow); got != 2 {
		t.Fatalf("local priority = %d", got)
	}
	if got := chainPriorityRank(defaultRow); got != 7 {
		t.Fatalf("default priority = %d", got)
	}
	// 回退/超优先秩：NULL 安全。
	if got := chainFallbackRank(&chainCandidateRow{LocalFallback: sql.NullInt64{Valid: true, Int64: 1}}); got != 1 {
		t.Fatalf("fallback rank = %d", got)
	}
	if got := chainSuperRank(&chainCandidateRow{}); got != 0 {
		t.Fatalf("super rank = %d", got)
	}
	// 模型秩：nil 表 → 0；命中 ID/AccountID → 秩；未命中 → 3。
	if got := chainModelRank(w1CandidateRow("a"), nil); got != 0 {
		t.Fatalf("nil ranks = %d", got)
	}
	if got := chainModelRank(w1CandidateRow("a"), map[string]int{"a": 1}); got != 1 {
		t.Fatalf("id rank = %d", got)
	}
	if got := chainModelRank(&chainCandidateRow{ID: "r", AccountID: "acc_9"}, map[string]int{"acc_9": 2}); got != 2 {
		t.Fatalf("account rank = %d", got)
	}
	if got := chainModelRank(w1CandidateRow("a"), map[string]int{"b": 1}); got != 3 {
		t.Fatalf("miss rank = %d", got)
	}
	// 排序全链：模型秩 → 回退 → 超优先（降序）→ 优先级 → 名称/ID 决胜。
	rows := []chainEligibleRow{
		{row: &chainCandidateRow{ID: "p5", Name: "同乙", Priority: 5}},
		{row: &chainCandidateRow{ID: "rank3", Name: "甲", Priority: 1}},
		{row: &chainCandidateRow{ID: "fallback", Name: "乙", Priority: 1, LocalFallback: sql.NullInt64{Valid: true, Int64: 1}}},
		{row: &chainCandidateRow{ID: "p1", Name: "同甲", Priority: 1}},
	}
	ordered := orderChainCandidateRowsForDispatch(rows, map[string]int{"rank3": 1})
	ids := make([]string, 0, len(ordered))
	for _, item := range ordered {
		ids = append(ids, item.row.ID)
	}
	// 模型秩（升序）最优先；同秩组内回退账户殿后；p1/p5 同组按名称。
	if ids[0] != "rank3" {
		t.Fatalf("模型秩必须最前 = %v", ids)
	}
	if ids[1] != "p1" || ids[2] != "p5" {
		t.Fatalf("p1/p5 次序 = %v", ids)
	}
	if ids[3] != "fallback" {
		t.Fatalf("回退账户必须殿后 = %v", ids)
	}
	// 桶内质量决胜：有分者先、分低者先；同分按中文整理排序。
	qualityHigh := 9.0
	qualityLow := 1.5
	bucketRows := []chainEligibleRow{
		{row: &chainCandidateRow{ID: "noscore", Name: "无分", Priority: 1}},
		{row: &chainCandidateRow{ID: "score-high", Name: "高分", Priority: 1, QualityScore: &qualityHigh}},
		{row: &chainCandidateRow{ID: "score-low", Name: "低分", Priority: 1, QualityScore: &qualityLow}},
	}
	orderedBucket := orderChainCandidateRowsForDispatch(bucketRows, nil)
	if orderedBucket[0].row.ID != "score-low" || orderedBucket[1].row.ID != "score-high" || orderedBucket[2].row.ID != "noscore" {
		t.Fatalf("bucket order = %s/%s/%s", orderedBucket[0].row.ID, orderedBucket[1].row.ID, orderedBucket[2].row.ID)
	}
	// 质量比较纯函数：nil 分在后，同分按名称。
	collator := chainNameCollator()
	if delta := compareChainQuality(&chainCandidateRow{QualityScore: &qualityLow}, w1CandidateRow("x"), collator); delta != -1 {
		t.Fatalf("score vs nil = %d", delta)
	}
	left := 2.0
	if delta := compareChainQuality(&chainCandidateRow{QualityScore: &left, Name: "甲", ID: "a"}, &chainCandidateRow{QualityScore: &left, Name: "乙", ID: "b"}, collator); delta >= 0 {
		t.Fatalf("name delta = %d", delta)
	}
}

func TestW1ModelCandidateHelpers(t *testing.T) {
	// 链接中文整理器可用。
	if chainNameCollator() == nil {
		t.Fatal("collator 必须可用")
	}
	// 布尔转整数。
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt 语义错误")
	}
	_ = time.Now
	_ = gatewayruntimecache.OpenAIAccountsForGroupOptions{}
}
