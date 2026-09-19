package accounts

// w14b list.go 与 m11_balance.go 臂补齐：列表投影（代理绑定/标签/锁状态/
// 分组绑定异常、排序去重、选项页归一化）与余额详情（快照字段回显、配置
// 匹配判定、端口访问器、掩码、强制激活查询）。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestW14BListPageProjectionArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	now := "2026-09-17T00:00:00.000Z"
	seedOpenAICompatibleProvider(t, env)

	// 代理可用 + 代理停用（绑定仍指向停用代理）两组。
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-w14b-l1', ?, 'w14b-l1-proxy', 'http', '127.0.0.1', 7890, 1, ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-w14b-l2', ?, 'w14b-l2-proxy', 'http', '127.0.0.1', 7891, 0, ?, ?)`, adminID, now, now)
	for _, item := range []struct{ id, name string }{{"acc-w14b-l1", "w14b-l1"}, {"acc-w14b-l2", "w14b-l2"}, {"acc-w14b-l3", "w14b-l3"}} {
		env.seedAccount(t, item.id, adminID, item.name, "active")
	}
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w14b-l1' WHERE id = 'acc-w14b-l1'`)
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w14b-l2' WHERE id = 'acc-w14b-l2'`)
	// 标签 + 锁状态 + 分组绑定（跨 owner 绑定异常臂）。
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('tag-w14b-l', ?, 'w14b-l-tag', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO account_tag_bindings (account_id, tag_id, system_account_id, created_at)
		VALUES ('acc-w14b-l1', 'tag-w14b-l', ?, ?)`, adminID, now)
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, updated_at) VALUES ('acc-w14b-l1', 1, 'ENGAGED', '2026-09-17T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, created_at, updated_at)
		VALUES (?, 'grp-w14b-l', 'acc-w14b-l1', ?, ?)`, "other-owner-w14b", now, now)
	// 损坏的时间计划 JSON（parseScheduleOrNull 失败臂）。
	env.exec(t, `UPDATE accounts SET availability_schedule_json = '{bad' WHERE id = 'acc-w14b-l2'`)

	page, err := env.store.ListPage(context.Background(), scope, ListOptions{
		Page: 1, PageSize: 10,
		Sorts: []ListSort{{Field: "name", Order: "asc"}, {Field: "name", Order: "desc"}},
	})
	if err != nil {
		t.Fatalf("列表查询应成功（重复排序字段被去重）：%v", err)
	}
	byID := map[string]ListItem{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	l1 := byID["acc-w14b-l1"]
	if l1.ID == "" {
		t.Fatal("l1 应在列表中")
	}
	if len(l1.Tags) != 1 || l1.Tags[0].Name != "w14b-l-tag" {
		t.Fatalf("标签投影不符：%+v", l1.Tags)
	}
	if l1.ProxyProfileName == nil || *l1.ProxyProfileName != "w14b-l1-proxy" || l1.ProxyProfileEnabled == nil || !*l1.ProxyProfileEnabled {
		t.Fatalf("可用代理投影不符：%+v", l1.ProxyProfileName)
	}
	if l1.LockState == nil || *l1.LockState != "ENGAGED" {
		t.Fatalf("锁状态投影不符：%+v", l1.LockState)
	}
	l2 := byID["acc-w14b-l2"]
	if l2.ID != "" && l2.AvailabilitySchedule != nil {
		t.Fatalf("损坏时间计划应忽略：%+v", l2.AvailabilitySchedule)
	}

	// 归一化与纯函数臂。
	if got := normalizeLockRange(10, 300, 30, 3600); got != 300 {
		t.Fatalf("越界锁超时应回退：%d", got)
	}
	if got := normalizeLockRange(600, 300, 30, 3600); got != 600 {
		t.Fatalf("界内锁超时应保留：%d", got)
	}
	if got := placeholders(0); got != "?" {
		t.Fatalf("零占位符应回退：%q", got)
	}
	if got := listItemColumns(""); got[0] != "accounts.id" && !strings.Contains(strings.Join(got, ","), "accounts.id") {
		t.Fatalf("空别名应回退 accounts 前缀：%v", got)
	}
	pgStore := &Store{pg: true}
	cte, joins := pgStore.listJoins()
	if !strings.Contains(joins, "LEFT JOIN LATERAL") || cte != "" {
		t.Fatal("pg 列表连接应使用 LATERAL")
	}
	sqliteCte, sqliteJoins := env.store.listJoins()
	if sqliteCte == "" || !strings.Contains(sqliteJoins, "ranked_group_bindings") {
		t.Fatal("sqlite 列表连接应使用 CTE")
	}
	if groupBindStatus(listRow{}) != "bound" {
		t.Fatal("无绑定 owner 差异应视为 bound")
	}
	// 选项摘要：空 scope 拒绝 + 分页归一化 + 关键字过滤。
	if _, err := env.store.ListOptionSummaries(context.Background(), AccessScope{}, ListOptions{}); err == nil {
		t.Fatal("空 scope 选项应拒绝")
	}
	summaries, err := env.store.ListOptionSummaries(context.Background(), scope, ListOptions{PageSize: 999, Keyword: "w14b-l1"})
	if err != nil {
		t.Fatalf("选项摘要应成功：%v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != "acc-w14b-l1" {
		t.Fatalf("关键字选项摘要不符：%+v", summaries)
	}
}

type w14bBalanceRefresherFake struct{ called bool }

func (f *w14bBalanceRefresherFake) RefreshManual(context.Context, BalanceRefreshCandidate) (BalanceManualRefreshOutcome, error) {
	f.called = true
	return BalanceManualRefreshOutcome{}, nil
}
func (f *w14bBalanceRefresherFake) TestDraft(context.Context, BalanceDraftProbeInput) (map[string]any, error) {
	f.called = true
	return map[string]any{}, nil
}

type w14bCatalogRefresherFake struct{ called bool }

func (f *w14bCatalogRefresherFake) RefreshDraftModelCatalog(context.Context, ModelCatalogDiscoveryInput) (map[string]any, error) {
	f.called = true
	return map[string]any{}, nil
}

func TestW14BBalanceDetailsAndSnapshotArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	seedOpenAICompatibleProvider(t, env)
	ctx := context.Background()

	// 端口访问器（零覆盖函数）。
	if env.store.BalanceRefresherPort() != nil || env.store.ModelCatalogRefresherPort() != nil {
		t.Fatal("未接线端口应为 nil")
	}
	refresher := &w14bBalanceRefresherFake{}
	catalog := &w14bCatalogRefresherFake{}
	env.store.SetManualBalanceRefresher(refresher)
	env.store.SetModelCatalogRefresher(catalog)
	if env.store.BalanceRefresherPort() == nil || env.store.ModelCatalogRefresherPort() == nil {
		t.Fatal("接线后端口应可用")
	}
	// statsTable 分支。
	if (&Store{pg: true}).statsTable("account_usage_snapshots") != "juhe_stats.account_usage_snapshots" {
		t.Fatal("pg stats 表应加前缀")
	}
	if env.store.statsTable("account_usage_snapshots") != "account_usage_snapshots" {
		t.Fatal("sqlite stats 表不应有前缀")
	}

	// 空 id / 缺失账户。
	if got, err := env.store.FindBalanceDetails(ctx, "  ", scope); err != nil || got != nil {
		t.Fatalf("空 id 应返回 nil：%v %v", got, err)
	}
	if got, err := env.store.FindBalanceDetails(ctx, "acc-w14b-none", scope); err != nil || got != nil {
		t.Fatalf("缺失账户应返回 nil：%v %v", got, err)
	}

	// 快照字段全量回显 + 详情 happy path。
	env.seedAccount(t, "acc-w14b-bal", adminID, "w14b-bal", "active")
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w14b-bal-12345678", "base_url": "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ?, balance_query_enabled = 1,
		balance_query_next_refresh_at = '2026-09-17T00:10:00.000Z' WHERE id = 'acc-w14b-bal'`, sealed)
	// keyBalances 条目以 api_key 的 HMAC 指纹为键（FindBalanceDetails 匹配臂）。
	fingerprint := env.store.balanceAPIKeyFingerprint("sk-w14b-bal-12345678")
	snapshotJSON := `{"configRevision":1,"queriedKeyCount":1,"scope":"all","aggregation":"sum",
		"keyBalances":[{"keyFingerprint":"` + fingerprint + `","maskedKey":"sk-…TZ","status":"ok",
		"remainingUsd":"12.5","rawUnit":"USD","scope":"all","basis":"granted",
		"errorMessage":"none","lastAttemptAt":"2026-09-17T00:00:00Z","lastSuccessAt":"2026-09-17T00:01:00Z"}]}`
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json, refresh_status, next_refresh_after, updated_at, created_at)
		VALUES (?, 'acc-w14b-bal', 'relay_balance', 'openai_codex', ?, 'ok', '2026-09-17T00:10:00.000Z', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z')`,
		adminID, snapshotJSON)
	details, err := env.store.FindBalanceDetails(ctx, "acc-w14b-bal", scope)
	if err != nil {
		t.Fatalf("余额详情应成功：%v", err)
	}
	if details == nil || len(details.KeyBalances) != 1 {
		t.Fatalf("余额详情应含按键快照：%+v", details)
	}
	snap := details.KeyBalances[0]
	if snap.Status != "ok" || snap.MaskedKey != "sk-…TZ" {
		t.Fatalf("按键快照回显不符：%+v", snap)
	}
	if snap.RawUnit == nil || *snap.RawUnit != "USD" || snap.Scope == nil || *snap.Scope != "all" {
		t.Fatalf("快照单位/作用域不符：%+v", snap)
	}
	if snap.ErrorMessage == nil || *snap.ErrorMessage != "none" || snap.LastAttemptAt == nil || snap.LastSuccessAt == nil {
		t.Fatalf("快照时间/错误字段不符：%+v", snap)
	}
	if details.QueriedKeyCount != 1 || details.Scope != "all" || details.Aggregation != "sum" {
		t.Fatalf("详情聚合字段不符：%+v", details)
	}
	// 掩码分支。
	if got := maskBalanceAPIKey("ab"); got != "ab…ab" && got != "a…b" {
		t.Fatalf("短键掩码不符：%q", got)
	}
	if got := maskBalanceAPIKey("sk-1234567890abcdef"); !strings.Contains(got, "…") || len(got) > 12 {
		t.Fatalf("长键掩码不符：%q", got)
	}
	// 配置匹配判定臂。
	if balanceSnapshotMatchesConfiguration("x", 1, nil) {
		t.Fatal("nil 记录应不匹配")
	}
	if balanceSnapshotMatchesConfiguration("", 1, &balanceSnapshotRecord{}) {
		t.Fatal("空快照应不匹配")
	}
	mismatch := &balanceSnapshotRecord{Snapshot: map[string]any{"configRevision": float64(2)}}
	if balanceSnapshotMatchesConfiguration("2026-09-17T00:10:00.000Z", 1, mismatch) {
		t.Fatal("版本不一致应不匹配")
	}
	nonNumber := &balanceSnapshotRecord{Snapshot: map[string]any{"configRevision": "1"}}
	if balanceSnapshotMatchesConfiguration("2026-09-17T00:10:00.000Z", 1, nonNumber) {
		t.Fatal("非数字版本应不匹配")
	}
	match := &balanceSnapshotRecord{Snapshot: map[string]any{"configRevision": float64(1)}, NextRefreshAfter: sql.NullString{String: "2026-09-17T00:10:00.000Z", Valid: true}}
	if !balanceSnapshotMatchesConfiguration("2026-09-17T00:10:00.000Z", 1, match) {
		t.Fatal("版本与时刻一致应匹配")
	}
	// 双方无效时刻且均未持久化 → 视为从未调度，视为匹配。
	bothInvalid := &balanceSnapshotRecord{Snapshot: map[string]any{"configRevision": float64(1)}}
	if !balanceSnapshotMatchesConfiguration("", 1, bothInvalid) {
		t.Fatal("双方无效时刻且无持久值应视为匹配")
	}
	// 单侧无效 → 不匹配。
	oneSided := &balanceSnapshotRecord{Snapshot: map[string]any{"configRevision": float64(1)}, NextRefreshAfter: sql.NullString{String: "bogus", Valid: true}}
	if balanceSnapshotMatchesConfiguration("", 1, oneSided) {
		t.Fatal("单侧无效时刻应不匹配")
	}
	// 损坏密文详情报错臂。
	env.seedAccount(t, "acc-w14b-bal2", adminID, "w14b-bal2", "active")
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'corrupted' WHERE id = 'acc-w14b-bal2'`)
	if _, err := env.store.FindBalanceDetails(ctx, "acc-w14b-bal2", scope); err == nil {
		t.Fatal("损坏密文应报错")
	}
}

func TestW14BForceActivateSummaryArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	// 不存在的账户 → nil, nil。
	if got, err := env.store.ForceActivatePending(context.Background(), "acc-w14b-nope", scope); err != nil || got != nil {
		t.Fatalf("缺失账户强制激活应返回 nil：%v %v", got, err)
	}
	if got, err := env.store.ForceActivatePending(context.Background(), "  ", scope); err != nil || got != nil {
		t.Fatalf("空 id 强制激活应返回 nil：%v %v", got, err)
	}
	// pending_test 账户：激活成功并返回摘要（顺带推进 dispatch revision 家族）。
	env.seedAccount(t, "acc-w14b-force", adminID, "w14b-force", "pending_test")
	result, err := env.store.ForceActivatePending(context.Background(), "acc-w14b-force", scope)
	if err != nil {
		t.Fatalf("强制激活应成功：%v", err)
	}
	if result == nil {
		t.Fatal("应返回激活结果")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-w14b-force' AND status = 'active' AND schedulable = 1`) != 1 {
		t.Fatal("强制激活应恢复 active 且可调度")
	}
	if result.Changed != true {
		t.Fatalf("应标记变更：%+v", result)
	}
	// 非 pending_test 账户：changed=false 分支。
	env.seedAccount(t, "acc-w14b-force2", adminID, "w14b-force2", "active")
	unchanged, err := env.store.ForceActivatePending(context.Background(), "acc-w14b-force2", scope)
	if err != nil || unchanged == nil || unchanged.Changed {
		t.Fatalf("非 pending 账户应无变更：%+v %v", unchanged, err)
	}
}
