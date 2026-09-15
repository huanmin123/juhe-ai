package main

// w1_selector_tail_test.go：chain_accounts.go 剩余分支与
// chain_accounts_secret.go openAIAccountSecretFromRow 的补充单测。
// 与 w1_selector_arms_test.go / chain_accounts_hydration_test.go /
// chain_accounts_secret_oauth_test.go 不重叠：这里只覆盖方言转换、
// WithStats 构造器守卫、模型窗口空白早退、候选合并去重、派发排序臂
// （superRank / modelRank / 同桶质量）、坏库 QueryContext 错误臂、
// 空 ids 回落、代理解析契约、fresh quality 映射与凭据类型变体。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// 复用的小构造器（全部 w1n 前缀）
// ---------------------------------------------------------------------------

// w1nOwnerGroupAccess 构造 owner 组访问元数据（配合 PreResolvedGroupAccess
// 跳过 resolveGroupAccess 的数据库读取）。
func w1nOwnerGroupAccess() *gatewayruntimecache.GroupUsageAccessMetadata {
	return &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: "sys_owner",
		ProviderCode:              "openai",
		GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
	}
}

// w1nSecretRow 构造 openAIAccountSecretFromRow 直接调用的最小候选行。
func w1nSecretRow(id, accountType, providerCode, encrypted string) *chainCandidateRow {
	return &chainCandidateRow{
		AccountID:            id,
		ID:                   id,
		SystemAccountID:      "sys_owner",
		ProviderCode:         providerCode,
		ProtocolCode:         sql.NullString{String: "openai", Valid: true},
		ProtocolVersion:      sql.NullString{String: "v1", Valid: true},
		Name:                 "行-" + id,
		Type:                 accountType,
		Status:               "active",
		Schedulable:          1,
		CredentialsEncrypted: sql.NullString{String: encrypted, Valid: encrypted != ""},
		ModelRank:            -1,
	}
}

// w1nEligible 把候选行包装成派发排序入口的 eligible 切片。
func w1nEligible(rows ...*chainCandidateRow) []chainEligibleRow {
	out := make([]chainEligibleRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, chainEligibleRow{row: row})
	}
	return out
}

// w1nDropTables 删除测试库中的表以构造 QueryContext 错误臂。
func w1nDropTables(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + name); err != nil {
			t.Fatalf("drop table %s: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 1. 方言：table / statsTable postgres 分支 + bind 的 ? → $N 转换
// ---------------------------------------------------------------------------

func TestW1NChainAccountsSelectorDialect(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	cases := []struct {
		name           string
		postgres       bool
		wantTable      string
		wantStatsTable string
		bindIn         string
		wantBind       string
	}{
		{
			name:           "sqlite 方言原样返回",
			postgres:       false,
			wantTable:      "groups",
			wantStatsTable: "account_quality_scores",
			bindIn:         "WHERE a = ? AND b IN (?, ?)",
			wantBind:       "WHERE a = ? AND b IN (?, ?)",
		},
		{
			name:           "postgres 方言加 schema 前缀并转换占位符",
			postgres:       true,
			wantTable:      "juhe_business.groups",
			wantStatsTable: "juhe_stats.account_quality_scores",
			bindIn:         "WHERE a = ? AND b IN (?, ?) OR c = ?",
			wantBind:       "WHERE a = $1 AND b IN ($2, $3) OR c = $4",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			selector, err := newChainAccountsSelectorWithStats(db, db, testCase.postgres, "secret", time.Now, 20)
			if err != nil {
				t.Fatalf("create selector: %v", err)
			}
			if got := selector.table("groups"); got != testCase.wantTable {
				t.Errorf("table(groups) = %q, want %q", got, testCase.wantTable)
			}
			if got := selector.statsTable("account_quality_scores"); got != testCase.wantStatsTable {
				t.Errorf("statsTable(account_quality_scores) = %q, want %q", got, testCase.wantStatsTable)
			}
			if got := selector.bind(testCase.bindIn); got != testCase.wantBind {
				t.Errorf("bind(%q) = %q, want %q", testCase.bindIn, got, testCase.wantBind)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. newChainAccountsSelectorWithStats 守卫与回落
// ---------------------------------------------------------------------------

func TestW1NNewChainAccountsSelectorWithStatsGuards(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	cases := []struct {
		name        string
		businessDB  *sql.DB
		limit       int
		wantErrPart string
	}{
		{name: "nil 业务库拒绝", businessDB: nil, limit: 20, wantErrPart: "业务数据库"},
		{name: "limit 0 越界拒绝", businessDB: db, limit: 0, wantErrPart: "1-50000"},
		{name: "limit 50001 越界拒绝", businessDB: db, limit: 50001, wantErrPart: "1-50000"},
		{name: "limit 1 下界通过", businessDB: db, limit: 1},
		{name: "limit 50000 上界通过", businessDB: db, limit: 50000},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			selector, err := newChainAccountsSelectorWithStats(testCase.businessDB, nil, false, "secret", time.Now, testCase.limit)
			if testCase.wantErrPart != "" {
				if err == nil {
					t.Fatalf("limit=%d 必须构造失败", testCase.limit)
				}
				if !strings.Contains(err.Error(), testCase.wantErrPart) {
					t.Fatalf("错误 %q 必须包含 %q", err.Error(), testCase.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("limit=%d 必须构造成功: %v", testCase.limit, err)
			}
			if selector == nil || selector.db != testCase.businessDB {
				t.Fatalf("业务库句柄必须原样注入: %+v", selector)
			}
		})
	}

	// statsDB=nil 回落到业务库句柄；now=nil 回落到 time.Now。
	selector, err := newChainAccountsSelectorWithStats(db, nil, false, "secret", nil, 7)
	if err != nil {
		t.Fatalf("statsDB=nil 必须构造成功: %v", err)
	}
	if selector.statsDB != db {
		t.Fatal("statsDB=nil 必须回落到业务库句柄")
	}
	if selector.candidateFinalLimit != 7 || selector.candidateScanLimit != 14 {
		t.Fatalf("limit=%d scan=%d, want 7/14", selector.candidateFinalLimit, selector.candidateScanLimit)
	}
	if selector.now == nil {
		t.Fatal("now=nil 必须回落到 time.Now")
	}
}

// ---------------------------------------------------------------------------
// 3. listModelCandidateRows 空白模型早退 + mergeChainCandidateRows 去重
// ---------------------------------------------------------------------------

func TestW1NListModelCandidateRowsBlankModelEarlyExit(t *testing.T) {
	fixture := newChainFixture(t)
	rows, ranks, err := fixture.selector.listModelCandidateRows(context.Background(),
		fixture.groupID, w1nOwnerGroupAccess(), "2026-09-04T00:00:00.000Z", "   ", "chat_completions", false)
	if err != nil {
		t.Fatalf("空白模型必须早退且无错误: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, want 非 nil 空切片", rows)
	}
	if ranks == nil || len(ranks) != 0 {
		t.Fatalf("ranks = %+v, want 非 nil 空 map", ranks)
	}
}

func TestW1NMergeChainCandidateRowsDedup(t *testing.T) {
	preferred := []chainCandidateRow{
		{AccountID: "acc_a", ID: "row_a"},
		{AccountID: "", ID: "row_b"},      // AccountID 空回退 ID 作去重键
		{AccountID: "acc_a", ID: "row_c"}, // 与 row_a 同键，丢弃
	}
	fallback := []chainCandidateRow{
		{AccountID: "acc_a", ID: "row_d"}, // 与模型窗口重复，丢弃
		{AccountID: "", ID: "row_e"},
	}
	merged := mergeChainCandidateRows(preferred, fallback)
	wantIDs := []string{"row_a", "row_b", "row_e"}
	if len(merged) != len(wantIDs) {
		t.Fatalf("merged = %d 条, want %d: %+v", len(merged), len(wantIDs), merged)
	}
	for index, want := range wantIDs {
		if merged[index].ID != want {
			t.Errorf("merged[%d].ID = %q, want %q（模型窗口顺序保持，基础窗口去重）", index, merged[index].ID, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. orderChainCandidateRowsForDispatch 排序臂
// ---------------------------------------------------------------------------

func TestW1NOrderChainCandidateRowsSuperRank(t *testing.T) {
	superOn := &chainCandidateRow{ID: "w1n_sup_on", Name: "同名",
		LocalSuperPriority: sql.NullInt64{Int64: 1, Valid: true}}
	superOff := &chainCandidateRow{ID: "w1n_sup_off", Name: "同名"}
	ordered := orderChainCandidateRowsForDispatch(w1nEligible(superOff, superOn), nil)
	if len(ordered) != 2 || ordered[0].row.ID != "w1n_sup_on" || ordered[1].row.ID != "w1n_sup_off" {
		t.Fatalf("superRank=1 必须排在 superRank=0 之前: %v", []string{ordered[0].row.ID, ordered[1].row.ID})
	}
}

func TestW1NOrderChainCandidateRowsModelRank(t *testing.T) {
	rankedFirst := &chainCandidateRow{ID: "w1n_ranked", Name: "同名"}
	unranked := &chainCandidateRow{ID: "w1n_unranked", Name: "同名"}
	ordered := orderChainCandidateRowsForDispatch(w1nEligible(unranked, rankedFirst),
		map[string]int{"w1n_ranked": 0})
	if len(ordered) != 2 || ordered[0].row.ID != "w1n_ranked" || ordered[1].row.ID != "w1n_unranked" {
		t.Fatalf("modelRank=0 必须排在未命中(rank 3)之前: %v", []string{ordered[0].row.ID, ordered[1].row.ID})
	}
	// nil modelRanks：全部 rank 0，落到 name/id 尾（同名按 id 升序）。
	byID := orderChainCandidateRowsForDispatch(w1nEligible(
		&chainCandidateRow{ID: "w1n_b", Name: "同名"},
		&chainCandidateRow{ID: "w1n_a", Name: "同名"}), nil)
	if len(byID) != 2 || byID[0].row.ID != "w1n_a" || byID[1].row.ID != "w1n_b" {
		t.Fatalf("nil modelRanks 必须落到 id 尾: %v", []string{byID[0].row.ID, byID[1].row.ID})
	}
}

func TestW1NCompareChainQualityArms(t *testing.T) {
	collator := chainNameCollator()
	score := func(value float64) *float64 { return &value }
	cases := []struct {
		name  string
		left  *chainCandidateRow
		right *chainCandidateRow
		want  int
	}{
		{
			name:  "左有分右无分左在前",
			left:  &chainCandidateRow{Name: "alpha", ID: "id_l", QualityScore: score(3)},
			right: &chainCandidateRow{Name: "alpha", ID: "id_r"},
			want:  -1,
		},
		{
			name:  "左无分右有分右在前",
			left:  &chainCandidateRow{Name: "alpha", ID: "id_l"},
			right: &chainCandidateRow{Name: "alpha", ID: "id_r", QualityScore: score(3)},
			want:  1,
		},
		{
			name:  "低分在前",
			left:  &chainCandidateRow{Name: "alpha", ID: "id_l", QualityScore: score(2)},
			right: &chainCandidateRow{Name: "alpha", ID: "id_r", QualityScore: score(9)},
			want:  -1,
		},
		{
			name:  "高分在后",
			left:  &chainCandidateRow{Name: "alpha", ID: "id_l", QualityScore: score(9)},
			right: &chainCandidateRow{Name: "alpha", ID: "id_r", QualityScore: score(2)},
			want:  1,
		},
		{
			name:  "同分按名",
			left:  &chainCandidateRow{Name: "beta", ID: "id_l", QualityScore: score(5)},
			right: &chainCandidateRow{Name: "alpha", ID: "id_r", QualityScore: score(5)},
			want:  1,
		},
		{
			name:  "同分同名按 id",
			left:  &chainCandidateRow{Name: "同名", ID: "id_b", QualityScore: score(5)},
			right: &chainCandidateRow{Name: "同名", ID: "id_a", QualityScore: score(5)},
			want:  1,
		},
		{
			name:  "双无分按名",
			left:  &chainCandidateRow{Name: "alpha", ID: "id_l"},
			right: &chainCandidateRow{Name: "beta", ID: "id_r"},
			want:  -1,
		},
		{
			name:  "双无分同名按 id",
			left:  &chainCandidateRow{Name: "同名", ID: "id_a"},
			right: &chainCandidateRow{Name: "同名", ID: "id_b"},
			want:  -1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := compareChainQuality(testCase.left, testCase.right, collator); got != testCase.want {
				t.Errorf("compareChainQuality = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestW1NOrderChainCandidateRowsQualityBucketGate(t *testing.T) {
	score := func(value float64) *float64 { return &value }
	// 同桶两个成员（bucketSize >= 2）→ 质量分决定顺序。
	sameBucket := w1nEligible(
		&chainCandidateRow{ID: "w1n_q_high", Name: "质量桶", Priority: 5, QualityScore: score(99)},
		&chainCandidateRow{ID: "w1n_q_low", Name: "质量桶", Priority: 5, QualityScore: score(10)},
	)
	ordered := orderChainCandidateRowsForDispatch(sameBucket, nil)
	if ordered[0].row.ID != "w1n_q_low" {
		t.Fatalf("同桶低分必须在前: first=%s", ordered[0].row.ID)
	}
	// 不同桶（bucketSize == 1）→ 质量不参与，priority 决定。
	crossBucket := w1nEligible(
		&chainCandidateRow{ID: "w1n_p5", Name: "异桶", Priority: 5, QualityScore: score(99)},
		&chainCandidateRow{ID: "w1n_p9", Name: "异桶", Priority: 9, QualityScore: score(1)},
	)
	ordered = orderChainCandidateRowsForDispatch(crossBucket, nil)
	if ordered[0].row.ID != "w1n_p5" {
		t.Fatalf("异桶质量不参与，priority=5 必须在前: first=%s", ordered[0].row.ID)
	}
}

// ---------------------------------------------------------------------------
// 5. 坏库错误臂：一次坏库命中 ListOpenAIAccountsForGroupResult 各读取点
// ---------------------------------------------------------------------------

func TestW1NListAccountsForGroupQueryErrorArms(t *testing.T) {
	groupAccess := &gatewayruntimecache.GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID: "sys_admin",
		ProviderCode:              "openai",
		GroupAccessType:           gatewayruntimecache.GroupAccessTypeOwner,
	}
	cases := []struct {
		name          string
		model         string
		separateStats bool
		prepare       func(t *testing.T, db *sql.DB)
	}{
		{
			name: "基础窗口 group_accounts 缺失",
			prepare: func(t *testing.T, db *sql.DB) {
				w1nDropTables(t, db, "group_accounts")
			},
		},
		{
			name:  "模型窗口 account_supported_models 缺失",
			model: "gpt-test",
			prepare: func(t *testing.T, db *sql.DB) {
				w1nDropTables(t, db, "account_supported_models")
			},
		},
		{
			name:          "fresh quality stats 表缺失",
			separateStats: true,
		},
		{
			name: "hydration supported_models 缺失",
			prepare: func(t *testing.T, db *sql.DB) {
				w1nDropTables(t, db, "account_supported_models")
			},
		},
		{
			name: "hydration model_mappings 缺失",
			prepare: func(t *testing.T, db *sql.DB) {
				w1nDropTables(t, db, "account_model_mappings")
			},
		},
		{
			name: "hydration runtime_states 缺失",
			prepare: func(t *testing.T, db *sql.DB) {
				w1nDropTables(t, db, "account_api_key_runtime_states")
			},
		},
		{
			name: "hydration proxy_profiles 缺失",
			prepare: func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`UPDATE accounts SET proxy_profile_id = 'w1n_px_err'`); err != nil {
					t.Fatalf("bind proxy profile: %v", err)
				}
				w1nDropTables(t, db, "proxy_profiles")
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := seedChainSelectorDB(t)
			// 先于 TempDir RemoveAll 关闭句柄（Windows 文件锁），否则清理失败。
			t.Cleanup(func() { _ = db.Close() })
			if testCase.prepare != nil {
				testCase.prepare(t, db)
			}
			statsDB := db
			if testCase.separateStats {
				empty, err := sql.Open("sqlite", ":memory:")
				if err != nil {
					t.Fatalf("open empty stats db: %v", err)
				}
				t.Cleanup(func() { _ = empty.Close() })
				statsDB = empty
			}
			selector, err := newChainAccountsSelectorWithStats(db, statsDB, false, "chain-test-secret", time.Now, 20)
			if err != nil {
				t.Fatalf("create selector: %v", err)
			}
			result, err := selector.ListOpenAIAccountsForGroupResult(context.Background(), "grp-limit", "sys_admin",
				gatewayruntimecache.OpenAIAccountsForGroupOptions{
					RequestedModel:         testCase.model,
					PreResolvedGroupAccess: groupAccess,
				})
			if err == nil {
				t.Fatalf("坏库必须返回 QueryContext 错误，得到 result=%+v", result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. 空 ids 分支：四个 hydration 批量读传 nil / 空切片直接回落
// ---------------------------------------------------------------------------

func TestW1NHydrationLoadsEmptyIDs(t *testing.T) {
	fixture := newChainFixture(t)
	ctx := context.Background()
	for _, ids := range [][]string{nil, {}} {
		supported, err := fixture.selector.loadSupportedModelsByAccountIds(ctx, ids)
		if err != nil {
			t.Fatalf("loadSupportedModelsByAccountIds(%v): %v", ids, err)
		}
		if supported == nil || len(supported) != 0 {
			t.Fatalf("supported = %+v, want 非 nil 空 map", supported)
		}
		mappings, err := fixture.selector.loadModelMappingsByAccountIds(ctx, ids)
		if err != nil {
			t.Fatalf("loadModelMappingsByAccountIds(%v): %v", ids, err)
		}
		if mappings == nil || len(mappings) != 0 {
			t.Fatalf("mappings = %+v, want 非 nil 空 map", mappings)
		}
		states, err := fixture.selector.loadAPIKeyRuntimeStatesByAccountIds(ctx, ids)
		if err != nil {
			t.Fatalf("loadAPIKeyRuntimeStatesByAccountIds(%v): %v", ids, err)
		}
		if states == nil || len(states) != 0 {
			t.Fatalf("states = %+v, want 非 nil 空 map", states)
		}
		quality, err := fixture.selector.loadFreshQualityRows(ctx, ids, "2026-01-01T00:00:00.000Z")
		if err != nil {
			t.Fatalf("loadFreshQualityRows(%v): %v", ids, err)
		}
		if quality == nil || len(quality) != 0 {
			t.Fatalf("quality = %+v, want 非 nil 空 map", quality)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. resolveProxyURLsForProfiles 契约：缺失/停用/不可解密/成功
// ---------------------------------------------------------------------------

func TestW1NResolveProxyURLsForProfilesContracts(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed proxy: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ('w1n_px_disabled', '停用代理', 'http', '10.0.0.1', 8080, NULL, NULL, 0, ?, ?)`, now, now)
	seed(`INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ('w1n_px_badpass', '坏密码代理', 'http', '10.0.0.2', 8080, 'alice', 'w1n-not-an-envelope', 1, ?, ?)`, now, now)
	seed(`INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ('w1n_px_plain', '匿名代理', 'http', '10.0.0.3', 8888, NULL, NULL, 1, ?, ?)`, now, now)

	resolutions, err := fixture.selector.resolveProxyURLsForProfiles(context.Background(),
		[]string{"w1n_px_ghost", "w1n_px_disabled", "w1n_px_badpass", "w1n_px_plain", ""})
	if err != nil {
		t.Fatalf("resolve proxy urls: %v（契约错误绝不作为 error 返回）", err)
	}
	if len(resolutions) != 4 {
		t.Fatalf("resolutions = %d 条（空白 id 必须被过滤）, want 4: %+v", len(resolutions), resolutions)
	}
	cases := []struct {
		id              string
		wantURL         string
		wantUnavailable bool
		wantMessage     string
	}{
		{id: "w1n_px_ghost", wantUnavailable: true, wantMessage: "代理不存在或已停用，请选择一个已启用的代理"},
		{id: "w1n_px_disabled", wantUnavailable: true, wantMessage: "代理不存在或已停用，请选择一个已启用的代理"},
		{id: "w1n_px_badpass", wantUnavailable: true, wantMessage: "代理凭据不可解密，请检查代理配置"},
		{id: "w1n_px_plain", wantURL: "http://10.0.0.3:8888"},
	}
	for _, testCase := range cases {
		t.Run(testCase.id, func(t *testing.T) {
			resolution, ok := resolutions[testCase.id]
			if !ok {
				t.Fatalf("缺少 %s 的解析结果: %+v", testCase.id, resolutions)
			}
			if testCase.wantUnavailable {
				if resolution.unavailable == nil || !*resolution.unavailable {
					t.Fatalf("%s 必须标记 unavailable: %+v", testCase.id, resolution)
				}
				if resolution.errorMessage == nil || *resolution.errorMessage != testCase.wantMessage {
					t.Fatalf("%s errorMessage = %v, want %q", testCase.id, resolution.errorMessage, testCase.wantMessage)
				}
				if resolution.proxyURL != nil {
					t.Fatalf("%s 不可用时不得返回 proxyURL: %v", testCase.id, resolution.proxyURL)
				}
				return
			}
			if resolution.proxyURL == nil || *resolution.proxyURL != testCase.wantURL {
				t.Fatalf("%s proxyURL = %v, want %q", testCase.id, resolution.proxyURL, testCase.wantURL)
			}
			if resolution.unavailable != nil || resolution.errorMessage != nil {
				t.Fatalf("%s 可用时不得携带 unavailable/errorMessage: %+v", testCase.id, resolution)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 8. loadFreshQualityRows：按 account_id 映射（含 NULL 列与空白 id 过滤）
// ---------------------------------------------------------------------------

func TestW1NLoadFreshQualityRowsMapsByAccountID(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	seedStats := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.statsDB.Exec(query, args...); err != nil {
			t.Fatalf("seed stats row: %v: %v", query, err)
		}
	}
	seedStats(`INSERT INTO account_quality_scores (account_id, quality_score, quality_state, ewma_first_token_ms, last_sample_at, updated_at)
		VALUES ('w1n_q_a', 11.5, 'healthy', 320.25, '2999-01-01T00:00:00.000Z', ?)`, now)
	seedStats(`INSERT INTO account_quality_scores (account_id, quality_score, quality_state, ewma_first_token_ms, last_sample_at, updated_at)
		VALUES ('w1n_q_b', NULL, NULL, 88.0, '2999-01-01T00:00:00.000Z', ?)`, now)

	qualityByAccountID, err := fixture.selector.loadFreshQualityRows(context.Background(),
		[]string{"w1n_q_b", "w1n_q_a", "w1n_q_a", "   "}, "2026-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatalf("loadFreshQualityRows: %v", err)
	}
	if len(qualityByAccountID) != 2 {
		t.Fatalf("quality map = %d 条, want 2: %+v", len(qualityByAccountID), qualityByAccountID)
	}
	a := qualityByAccountID["w1n_q_a"]
	if a.score == nil || *a.score != 11.5 {
		t.Fatalf("w1n_q_a score = %v, want 11.5", a.score)
	}
	if a.state == nil || *a.state != "healthy" {
		t.Fatalf("w1n_q_a state = %v, want healthy", a.state)
	}
	if a.firstTokenMs == nil || *a.firstTokenMs != 320.25 {
		t.Fatalf("w1n_q_a firstTokenMs = %v, want 320.25", a.firstTokenMs)
	}
	b := qualityByAccountID["w1n_q_b"]
	if b.score != nil || b.state != nil {
		t.Fatalf("w1n_q_b NULL 列必须保持 nil: %+v", b)
	}
	if b.firstTokenMs == nil || *b.firstTokenMs != 88.0 {
		t.Fatalf("w1n_q_b firstTokenMs = %v, want 88.0", b.firstTokenMs)
	}
}

// ---------------------------------------------------------------------------
// 9. openAIAccountSecretFromRow：凭据类型变体 + endpoint modes + 池化开关
// ---------------------------------------------------------------------------

func TestW1NOpenAIAccountSecretFromRowCredentialVariants(t *testing.T) {
	fixture := newChainFixture(t)
	ctx := context.Background()
	ownerAccess := &chainAccountAccess{accountAccessType: chainAccountAccessOwner}
	fingerprint := chainFingerprintAPIKey("chain-test-secret", "sk-w1n-pool-b")
	runtimeStates := map[string][]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState{
		"w1n_row_pool": {{
			APIKeyID:      "w1n_state_1",
			Fingerprint:   fingerprint,
			Disabled:      true,
			CooldownUntil: strPtr("2026-09-04T01:00:00.000Z"),
		}},
		"w1n_row_scalar": {{
			APIKeyID:    "w1n_state_2",
			Fingerprint: "w1n_ignored",
		}},
		"w1n_row_unsupported": {{
			APIKeyID:    "w1n_state_3",
			Fingerprint: "w1n_unsupported",
		}},
	}
	poolRow := w1nSecretRow("w1n_row_pool", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{
			"api_keys":        []any{"sk-w1n-pool-a", "sk-w1n-pool-b", "sk-w1n-pool-a"},
			"api_key_weights": []any{2.0, 5.0},
		}))
	scalarRow := w1nSecretRow("w1n_row_scalar", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-w1n-solo"}))
	modesRow := w1nSecretRow("w1n_row_modes", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{
			"api_key":                  "sk-w1n-modes",
			"supported_endpoint_modes": []any{"responses_json", "bogus_mode", 42, "chat_sse"},
		}))
	unsupportedRow := w1nSecretRow("w1n_row_unsupported", "api_key", "mystery_provider",
		mustEncryptCredentials(t, map[string]any{"api_keys": []any{"k1", "k2"}}))
	oauthPoolRow := w1nSecretRow("w1n_row_oauth", "oauth", "openai",
		mustEncryptCredentials(t, map[string]any{"access_token": "w1n-oauth-token", "api_keys": []any{"k1", "k2"}}))
	gateOffRow := w1nSecretRow("w1n_row_gate_off", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-w1n-gate"}))
	gateOffRow.Status = "rate_limited"
	gateOffRow.LocalSuperPriority = sql.NullInt64{Int64: 1, Valid: true}
	gateOffRow.LocalFallback = sql.NullInt64{Int64: 1, Valid: true}
	gateOffRow.LocalPriority = sql.NullInt64{Int64: 3, Valid: true}
	gateOnRow := w1nSecretRow("w1n_row_gate_on", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-w1n-gate"}))
	gateOnRow.LocalSuperPriority = sql.NullInt64{Int64: 1, Valid: true}
	gateOnRow.LocalFallback = sql.NullInt64{Int64: 1, Valid: true}

	cases := []struct {
		name        string
		row         *chainCandidateRow
		options     chainSecretOptions
		wantDropped bool
		verify      func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret)
	}{
		{
			name:    "api_keys 数组池化投影",
			row:     poolRow,
			options: chainSecretOptions{apiKeyRuntimeState: runtimeStates},
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				if secret.APIKey != "sk-w1n-pool-a" {
					t.Fatalf("apiKey = %q, want sk-w1n-pool-a", secret.APIKey)
				}
				if len(secret.APIKeys) != 2 || secret.APIKeys[0] != "sk-w1n-pool-a" || secret.APIKeys[1] != "sk-w1n-pool-b" {
					t.Fatalf("apiKeys = %v（重复键必须去重）", secret.APIKeys)
				}
				if len(secret.APIKeyRuntimeStates) != 1 {
					t.Fatalf("池化开启必须附着 runtime states: %+v", secret.APIKeyRuntimeStates)
				}
				state := secret.APIKeyRuntimeStates[0]
				if state.Fingerprint != fingerprint || !state.Disabled {
					t.Fatalf("runtime state = %+v", state)
				}
				if state.CooldownUntil == nil || *state.CooldownUntil != "2026-09-04T01:00:00.000Z" {
					t.Fatalf("runtime state cooldownUntil = %v", state.CooldownUntil)
				}
			},
		},
		{
			name:    "api_key 标量单键不启用池化",
			row:     scalarRow,
			options: chainSecretOptions{apiKeyRuntimeState: runtimeStates},
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				if secret.APIKey != "sk-w1n-solo" {
					t.Fatalf("apiKey = %q", secret.APIKey)
				}
				if len(secret.APIKeys) != 1 || secret.APIKeys[0] != "sk-w1n-solo" {
					t.Fatalf("apiKeys = %v", secret.APIKeys)
				}
				if len(secret.APIKeyRuntimeStates) != 0 {
					t.Fatalf("单键必须关闭池化，runtime states = %+v", secret.APIKeyRuntimeStates)
				}
			},
		},
		{
			name:        "空凭据静默丢弃",
			row:         w1nSecretRow("w1n_row_empty", "api_key", "openai", ""),
			wantDropped: true,
		},
		{
			name:        "凭据不可解密切换为静默丢弃",
			row:         w1nSecretRow("w1n_row_badcreds", "api_key", "openai", "w1n-not-an-envelope"),
			wantDropped: true,
		},
		{
			name: "supported_endpoint_modes 覆盖过滤",
			row:  modesRow,
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				want := []string{"responses_json", "chat_sse"}
				if len(secret.SupportedEndpointModes) != len(want) {
					t.Fatalf("supportedEndpointModes = %v, want %v", secret.SupportedEndpointModes, want)
				}
				for index, mode := range want {
					if secret.SupportedEndpointModes[index] != mode {
						t.Fatalf("supportedEndpointModes = %v, want %v", secret.SupportedEndpointModes, want)
					}
				}
			},
		},
		{
			name:    "非池化 provider 隔离开关关闭",
			row:     unsupportedRow,
			options: chainSecretOptions{apiKeyRuntimeState: runtimeStates},
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				if secret.APIKey != "k1" {
					t.Fatalf("apiKey = %q", secret.APIKey)
				}
				if len(secret.APIKeys) != 2 {
					t.Fatalf("apiKeys = %v", secret.APIKeys)
				}
				if len(secret.APIKeyRuntimeStates) != 0 {
					t.Fatalf("不受支持 provider 必须关闭池化: %+v", secret.APIKeyRuntimeStates)
				}
			},
		},
		{
			name:    "oauth 类型不启用池化",
			row:     oauthPoolRow,
			options: chainSecretOptions{apiKeyRuntimeState: runtimeStates},
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				if secret.APIKey != "w1n-oauth-token" {
					t.Fatalf("apiKey = %q（oauth 必须解析 access_token）", secret.APIKey)
				}
				if secret.APIKeys != nil {
					t.Fatalf("oauth 类型 apiKeys 必须保持 nil: %v", secret.APIKeys)
				}
				if len(secret.APIKeyRuntimeStates) != 0 {
					t.Fatalf("oauth 类型必须关闭池化: %+v", secret.APIKeyRuntimeStates)
				}
			},
		},
		{
			name: "非 active 状态关闭超优先投影",
			row:  gateOffRow,
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				if secret.Status != "rate_limited" {
					t.Fatalf("status = %q", secret.Status)
				}
				if secret.SuperPriorityEnabled || secret.FallbackEnabled {
					t.Fatalf("非 active 必须关闭 super/fallback 投影: %v/%v",
						secret.SuperPriorityEnabled, secret.FallbackEnabled)
				}
				if secret.Priority != 3 {
					t.Fatalf("priority = %d, want 3（local 优先）", secret.Priority)
				}
			},
		},
		{
			name: "active 状态保留超优先投影",
			row:  gateOnRow,
			verify: func(t *testing.T, secret *gatewayruntimecache.OpenAIAccountSecret) {
				if !secret.SuperPriorityEnabled || !secret.FallbackEnabled {
					t.Fatalf("active 状态必须保留 super/fallback 投影: %v/%v",
						secret.SuperPriorityEnabled, secret.FallbackEnabled)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			secret, err := fixture.selector.openAIAccountSecretFromRow(ctx, testCase.row,
				w1nOwnerGroupAccess(), ownerAccess, testCase.options)
			if testCase.wantDropped {
				if err != nil {
					t.Fatalf("必须静默丢弃，得到错误: %v", err)
				}
				if secret != nil {
					t.Fatalf("必须返回 nil secret: %+v", secret)
				}
				return
			}
			if err != nil {
				t.Fatalf("openAIAccountSecretFromRow: %v", err)
			}
			if secret == nil {
				t.Fatal("secret 不能为 nil")
			}
			testCase.verify(t, secret)
		})
	}
}

// ---------------------------------------------------------------------------
// 9b. openAIAccountSecretFromRow：授权绑定投影与缺绑定错误臂
// ---------------------------------------------------------------------------

func TestW1NOpenAIAccountSecretFromRowAuthorizedBinding(t *testing.T) {
	fixture := newChainFixture(t)
	row := w1nSecretRow("w1n_row_authz", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-w1n-authz"}))
	row.GroupID = sql.NullString{String: "group_bound", Valid: true}
	row.AccountAuthorizationID = sql.NullString{String: "authz_b1", Valid: true}
	row.BindingSystemAccountID = sql.NullString{String: "sys_binding", Valid: true}
	row.AuthorizationInstanceSourceAccountID = sql.NullString{String: "src_owner_acc", Valid: true}
	row.ResourceAccountID = sql.NullString{String: "src_owner_acc", Valid: true}
	access := &chainAccountAccess{
		accountAccessType:      chainAccountAccessAuthorized,
		accountOwnerID:         strPtr("sys_real_owner"),
		accountAuthorizationID: strPtr("authz_b1"),
	}
	secret, err := fixture.selector.openAIAccountSecretFromRow(context.Background(), row,
		w1nOwnerGroupAccess(), access, chainSecretOptions{})
	if err != nil {
		t.Fatalf("openAIAccountSecretFromRow: %v", err)
	}
	if secret == nil {
		t.Fatal("secret 不能为 nil")
	}
	if secret.BindingSystemAccountID == nil || *secret.BindingSystemAccountID != "sys_binding" {
		t.Fatalf("bindingSystemAccountId = %v", secret.BindingSystemAccountID)
	}
	if secret.BoundGroupID == nil || *secret.BoundGroupID != "group_bound" {
		t.Fatalf("boundGroupId = %v", secret.BoundGroupID)
	}
	if secret.AccountAuthorizationID == nil || *secret.AccountAuthorizationID != "authz_b1" {
		t.Fatalf("accountAuthorizationId = %v", secret.AccountAuthorizationID)
	}
	if secret.AccountOwnerSystemAccountID != "sys_real_owner" {
		t.Fatalf("accountOwnerSystemAccountId = %q, want sys_real_owner（授权 owner 覆盖）", secret.AccountOwnerSystemAccountID)
	}
	if secret.CredentialSourceAccountID == nil || *secret.CredentialSourceAccountID != "src_owner_acc" {
		t.Fatalf("credentialSourceAccountId = %v", secret.CredentialSourceAccountID)
	}
	if secret.AccountAccessType != "account_authorized" {
		t.Fatalf("accountAccessType = %q", secret.AccountAccessType)
	}

	// 本地授权绑定缺失系统账户上下文 → 错误臂。
	row.BindingSystemAccountID = sql.NullString{}
	_, err = fixture.selector.openAIAccountSecretFromRow(context.Background(), row,
		w1nOwnerGroupAccess(), access, chainSecretOptions{})
	if err == nil {
		t.Fatal("授权账户绑定缺少系统账户上下文必须报错")
	}
	if !strings.Contains(err.Error(), "授权账户绑定缺少系统账户上下文") {
		t.Fatalf("错误 %q 必须包含绑定缺失提示", err.Error())
	}
}

// ---------------------------------------------------------------------------
// 9c. openAIAccountSecretFromRow：RFC3339 时间戳错误臂
// ---------------------------------------------------------------------------

func TestW1NOpenAIAccountSecretFromRowBadTimestamps(t *testing.T) {
	fixture := newChainFixture(t)
	plainRow := func() *chainCandidateRow {
		return w1nSecretRow("w1n_row_ts", "api_key", "openai",
			mustEncryptCredentials(t, map[string]any{"api_key": "sk-w1n-ts"}))
	}
	badCredsRow := w1nSecretRow("w1n_row_ts", "api_key", "openai",
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-w1n-ts", "expires_at": "bad-ts"}))
	authorizedAccess := func(expiresAt string) *chainAccountAccess {
		return &chainAccountAccess{
			accountAccessType:      chainAccountAccessAuthorized,
			accountAuthorizationID: strPtr("authz_ts"),
			expiresAt:              strPtr(expiresAt),
		}
	}
	badGroupAccess := w1nOwnerGroupAccess()
	badGroupAccess.GroupAuthorizationExpiresAt = strPtr("bad-ts")

	cases := []struct {
		name        string
		row         *chainCandidateRow
		groupAccess *gatewayruntimecache.GroupUsageAccessMetadata
		access      *chainAccountAccess
		wantErrPart string
	}{
		{
			name: "cooldownUntil 非法",
			row: func() *chainCandidateRow {
				row := plainRow()
				row.CooldownUntil = sql.NullString{String: "bad-ts", Valid: true}
				return row
			}(),
			wantErrPart: "cooldownUntil",
		},
		{
			name: "accountExpiresAt 非法",
			row: func() *chainCandidateRow {
				row := plainRow()
				row.AccountExpiresAt = sql.NullString{String: "bad-ts", Valid: true}
				return row
			}(),
			wantErrPart: "accountExpiresAt",
		},
		{
			name: "streamFailureWindowStartedAt 非法",
			row: func() *chainCandidateRow {
				row := plainRow()
				row.StreamFailureWindowStartedAt = sql.NullString{String: "bad-ts", Valid: true}
				return row
			}(),
			wantErrPart: "streamFailureWindowStartedAt",
		},
		{
			name:        "凭据 expires_at 非法",
			row:         badCredsRow,
			wantErrPart: "expires_at",
		},
		{
			name:        "账户授权 expiresAt 非法",
			row:         plainRow(),
			access:      authorizedAccess("bad-ts"),
			wantErrPart: "expiresAt",
		},
		{
			name:        "分组授权 expiresAt 非法",
			row:         plainRow(),
			groupAccess: badGroupAccess,
			wantErrPart: "expiresAt",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			groupAccess := testCase.groupAccess
			if groupAccess == nil {
				groupAccess = w1nOwnerGroupAccess()
			}
			access := testCase.access
			if access == nil {
				access = &chainAccountAccess{accountAccessType: chainAccountAccessOwner}
			}
			secret, err := fixture.selector.openAIAccountSecretFromRow(context.Background(),
				testCase.row, groupAccess, access, chainSecretOptions{})
			if err == nil {
				t.Fatalf("非法时间戳必须报错，得到 secret=%+v", secret)
			}
			if !strings.Contains(err.Error(), testCase.wantErrPart) {
				t.Fatalf("错误 %q 必须包含 %q", err.Error(), testCase.wantErrPart)
			}
		})
	}
}
