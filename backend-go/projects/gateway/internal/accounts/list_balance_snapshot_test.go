package accounts

// 账户列表余额字段叠加回归（Node account-status-snapshot.service.ts 列表
// 余额投影的 Node→Go 移植缺口：/accounts 与 /my-accounts 列表项缺失
// balanceQueryEnabled / balanceQueryNextRefreshAt / balanceSnapshot，前端
// AccountUsageCell.vue 以 balanceQueryEnabled 渲染余额行导致整行不显示）：
// owner 行投影 enabled/nextRefreshAt；balanceSnapshot 仅在启用且 stats
// kind='relay_balance' 快照与当前配置匹配时叠加，形状剥 keyBalances；
// authorized 行三字段皆省略；stats 读空/读失败降级不失败。

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// 列表余额投影用的固定代次（账户 config_revision 默认 1）。
const balanceListNextRefreshAt = "2026-09-27T00:00:00.000Z"

// balanceListSnapshotJSON 模拟 jobs 写入的 relay_balance snapshot_json：携带
// keyBalances 明细（列表形状必须剥除）与全部白名单字段。
const balanceListSnapshotJSON = `{"status":"ok","configRevision":1,"remainingUsd":"12.50","rawUnit":"usd",` +
	`"scope":"account","aggregation":"sum","keyCount":2,"queriedKeyCount":2,` +
	`"lastAttemptAt":"2026-09-27T00:00:00.000Z","lastSuccessAt":"2026-09-27T00:00:00.000Z",` +
	`"consecutiveTransientFailures":0,` +
	`"keyBalances":[{"keyFingerprint":"fp-1","maskedKey":"sk-1…ab","status":"ok","remainingUsd":"10.00"}]}`

// setAccountBalanceQuery flips the accounts row's balance query columns.
func setAccountBalanceQuery(t *testing.T, env *testEnv, id string, enabled bool, nextRefreshAt any) {
	t.Helper()
	enabledValue := 0
	if enabled {
		enabledValue = 1
	}
	if nextRefreshAt == nil {
		env.exec(t, `UPDATE accounts SET balance_query_enabled = ?, balance_query_next_refresh_at = NULL WHERE id = ?`, enabledValue, id)
		return
	}
	env.exec(t, `UPDATE accounts SET balance_query_enabled = ?, balance_query_next_refresh_at = ? WHERE id = ?`, enabledValue, nextRefreshAt, id)
}

// seedRelayBalanceSnapshot writes one kind='relay_balance' stats row.
func seedRelayBalanceSnapshot(t *testing.T, env *testEnv, ownerID, accountID, snapshotJSON, nextRefreshAfter string) {
	t.Helper()
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, snapshot_json,
		next_refresh_after, updated_at, created_at)
		VALUES (?, ?, 'relay_balance', ?, ?, '2026-09-27T00:00:00.000Z', '2026-09-27T00:00:00.000Z')`,
		ownerID, accountID, snapshotJSON, nextRefreshAfter)
}

// requireMissingKeys asserts none of the given keys renders on the item.
func requireMissingKeys(t *testing.T, item map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, present := item[key]; present {
			t.Fatalf("%s 不应出现在列表项：%v", key, item)
		}
	}
}

// TestListPageBalanceSnapshotOverlayAdmin locks the admin surface overlay:
// enabled+matching rows carry the keyBalance-free snapshot, disabled rows
// stay bare, stale revisions and mismatched refresh generations stay bare.
func TestListPageBalanceSnapshotOverlayAdmin(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-bal-ok", adminID, "bal-ok", "active")
	env.seedAccount(t, "acc-bal-disabled", adminID, "bal-disabled", "active")
	env.seedAccount(t, "acc-bal-stale", adminID, "bal-stale", "active")
	env.seedAccount(t, "acc-bal-generation", adminID, "bal-generation", "active")
	env.seedAccount(t, "acc-bal-norow", adminID, "bal-norow", "active")

	// 匹配行：enabled + configRevision 相等 + next_refresh_after 毫秒相等。
	setAccountBalanceQuery(t, env, "acc-bal-ok", true, balanceListNextRefreshAt)
	seedRelayBalanceSnapshot(t, env, adminID, "acc-bal-ok", balanceListSnapshotJSON, balanceListNextRefreshAt)
	// 停用行：三字段皆无。
	setAccountBalanceQuery(t, env, "acc-bal-disabled", false, nil)
	seedRelayBalanceSnapshot(t, env, adminID, "acc-bal-disabled", balanceListSnapshotJSON, balanceListNextRefreshAt)
	// configRevision 不匹配：快照随新配置作废，列表不带。
	setAccountBalanceQuery(t, env, "acc-bal-stale", true, balanceListNextRefreshAt)
	seedRelayBalanceSnapshot(t, env, adminID, "acc-bal-stale",
		`{"status":"ok","configRevision":2,"remainingUsd":"99.00"}`, balanceListNextRefreshAt)
	// next_refresh_after 毫秒不等（刷新代次不一致）：列表不带。
	setAccountBalanceQuery(t, env, "acc-bal-generation", true, balanceListNextRefreshAt)
	seedRelayBalanceSnapshot(t, env, adminID, "acc-bal-generation", balanceListSnapshotJSON, "2026-01-01T00:00:00.000Z")
	// enabled 但 stats 无快照行：仅基础字段。
	setAccountBalanceQuery(t, env, "acc-bal-norow", true, balanceListNextRefreshAt)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("admin 列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)

	okItem := items["acc-bal-ok"]
	if okItem["balanceQueryEnabled"] != true {
		t.Fatalf("acc-bal-ok balanceQueryEnabled 应为 true：%v", okItem)
	}
	if okItem["balanceQueryNextRefreshAt"] != balanceListNextRefreshAt {
		t.Fatalf("acc-bal-ok balanceQueryNextRefreshAt 不符：%v", okItem["balanceQueryNextRefreshAt"])
	}
	snapshot, ok := okItem["balanceSnapshot"].(map[string]any)
	if !ok {
		t.Fatalf("acc-bal-ok 应带 balanceSnapshot：%v", okItem)
	}
	if snapshot["status"] != "ok" {
		t.Fatalf("快照 status 应为 ok：%v", snapshot)
	}
	if snapshot["configRevision"] != float64(1) || snapshot["remainingUsd"] != "12.50" ||
		snapshot["rawUnit"] != "usd" || snapshot["scope"] != "account" ||
		snapshot["aggregation"] != "sum" || snapshot["keyCount"] != float64(2) ||
		snapshot["queriedKeyCount"] != float64(2) {
		t.Fatalf("快照白名单字段投影不符：%v", snapshot)
	}
	// accountBalanceSnapshotForList 语义：逐 Key 明细只走明细接口。
	if _, leaked := snapshot["keyBalances"]; leaked {
		t.Fatalf("列表快照必须剥除 keyBalances：%v", snapshot)
	}

	disabledItem := items["acc-bal-disabled"]
	requireMissingKeys(t, disabledItem, "balanceQueryEnabled", "balanceQueryNextRefreshAt", "balanceSnapshot")

	staleItem := items["acc-bal-stale"]
	if staleItem["balanceQueryEnabled"] != true {
		t.Fatalf("acc-bal-stale 基础字段应保留：%v", staleItem)
	}
	requireMissingKeys(t, staleItem, "balanceSnapshot")

	generationItem := items["acc-bal-generation"]
	if generationItem["balanceQueryEnabled"] != true {
		t.Fatalf("acc-bal-generation 基础字段应保留：%v", generationItem)
	}
	requireMissingKeys(t, generationItem, "balanceSnapshot")

	norowItem := items["acc-bal-norow"]
	if norowItem["balanceQueryEnabled"] != true {
		t.Fatalf("acc-bal-norow 基础字段应保留：%v", norowItem)
	}
	requireMissingKeys(t, norowItem, "balanceSnapshot")
}

// TestListPageBalanceSnapshotSelfView keeps the self surface (forceSelfAccessScope)
// on the same overlay: owner rows of /my-accounts carry the same fields.
func TestListPageBalanceSnapshotSelfView(t *testing.T) {
	env := newTestEnv(t)
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedProviderAndDefaultGroup(t, aliceID)
	env.seedAccount(t, "acc-self-bal", aliceID, "self-bal", "active")
	setAccountBalanceQuery(t, env, "acc-self-bal", true, balanceListNextRefreshAt)
	seedRelayBalanceSnapshot(t, env, aliceID, "acc-self-bal", balanceListSnapshotJSON, balanceListNextRefreshAt)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("self 列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	item, ok := items["acc-self-bal"]
	if !ok {
		t.Fatalf("self 列表缺 acc-self-bal：%v", items)
	}
	if item["accessType"] != "owner" || item["balanceQueryEnabled"] != true {
		t.Fatalf("self owner 行基础字段不符：%v", item)
	}
	if _, hasSnapshot := item["balanceSnapshot"]; !hasSnapshot {
		t.Fatalf("self owner 行应带 balanceSnapshot：%v", item)
	}
}

// TestListPageBalanceSnapshotEmptyStatsDegrades locks the degradation shape:
// an empty stats table (生产当前已知状态) renders the enabled base fields and
// never fails the page.
func TestListPageBalanceSnapshotEmptyStatsDegrades(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-bal-empty", adminID, "bal-empty", "active")
	setAccountBalanceQuery(t, env, "acc-bal-empty", true, balanceListNextRefreshAt)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("空 stats 表列表仍应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	item, ok := items["acc-bal-empty"]
	if !ok {
		t.Fatalf("列表缺 acc-bal-empty：%v", items)
	}
	if item["balanceQueryEnabled"] != true || item["balanceQueryNextRefreshAt"] != balanceListNextRefreshAt {
		t.Fatalf("空快照降级应保留基础字段：%v", item)
	}
	requireMissingKeys(t, item, "balanceSnapshot")
}

// TestMyAccountsAuthorizedInstanceHidesBalanceFields aligns Node :580-581:
// authorized rows never project the balance trio, even with the query
// enabled and a matching snapshot stored.
func TestMyAccountsAuthorizedInstanceHidesBalanceFields(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "owner1", "owner-pass", "user")
	memberID := env.login(t, "member1", "member-pass", "user")
	env.seedAccount(t, "acc-bal-src", ownerID, "余额源账户", "active")
	env.seedProviderAndDefaultGroup(t, memberID)
	env.seedTeamMember(t, "team-bal", ownerID, memberID)
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-bal-src",
		GranteeType: "team", GranteeID: "team-bal",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-bal-src'`, memberID)
	if runtimeID == "" {
		t.Fatal("team runtime row missing")
	}
	env.seedAuthorizationInstance(t, "acc-bal-inst", memberID, runtimeID, "acc-bal-src")
	// 实例行带完整的启用 + 匹配快照组合：authorized 视图仍必须省略。
	env.exec(t, `UPDATE accounts SET balance_query_enabled = 1, balance_query_next_refresh_at = ? WHERE id = ?`,
		balanceListNextRefreshAt, "acc-bal-inst")
	seedRelayBalanceSnapshot(t, env, memberID, "acc-bal-inst", balanceListSnapshotJSON, balanceListNextRefreshAt)

	env.login(t, "member1", "member-pass", "user")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("member 列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	instance, ok := items["acc-bal-inst"]
	if !ok {
		t.Fatalf("member 列表缺授权实例：%v", items)
	}
	if instance["accessType"] != "authorized" {
		t.Fatalf("实例 accessType 应为 authorized：%v", instance["accessType"])
	}
	requireMissingKeys(t, instance, "balanceQueryEnabled", "balanceQueryNextRefreshAt", "balanceSnapshot")
}

// TestLoadRelayBalanceSnapshotRecordsChunking exercises the 900-chunk IN
// reader across the boundary (905 ids = 900 + 5) against the single-file
// test database.
func TestLoadRelayBalanceSnapshotRecordsChunking(t *testing.T) {
	env := newTestEnv(t)
	ownerID := "chunk-owner"
	const total = 905
	ids := make([]string, 0, total)
	for index := 0; index < total; index++ {
		id := fmt.Sprintf("acc-chunk-%04d", index)
		ids = append(ids, id)
		seedRelayBalanceSnapshot(t, env, ownerID, id,
			`{"status":"ok","configRevision":1,"remainingUsd":"1.00"}`, balanceListNextRefreshAt)
	}
	records, err := env.store.loadRelayBalanceSnapshotRecords(context.Background(), ids)
	if err != nil {
		t.Fatalf("分块读取不应失败：%v", err)
	}
	if len(records) != total {
		t.Fatalf("分块读取应覆盖 %d 行，实际 %d", total, len(records))
	}
	for _, id := range ids {
		record, ok := records[id]
		if !ok || record.Snapshot == nil || record.Snapshot["configRevision"] != float64(1) {
			t.Fatalf("分块边界行缺失或快照损坏：%s", id)
		}
	}
}
