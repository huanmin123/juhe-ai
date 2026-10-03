package accounts

// 列表运行态 overlay 回归（缺陷修复批次一，修复前必红）：管理面账户列表此前
// 从不返回 runtimeAvailability / circuitSummary / apiKeyRuntime，被熔断或
// 运行态抑制的账户在列表显示为"可调度"（effectiveAvailability 恒实例臂），
// 前端运行态色、探针 tooltip 与"重新验证 Key 池"入口不可见。本文件锁定：
//   - 四字段（三 overlay + effectiveAvailability 运行态分支）的投影与降级；
//   - runtimeKey 派生（owner 行=账户 id；authorized 实例行=authorized 键）；
//   - nil 端口 / 读失败的字段缺席降级（页面不 500）。
//
// 禁止 go test 的批次约束下由主代理统一执行；下列"修复前必红"注释逐一给出
// 修复前会失败的断言依据。

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// fakeRuntimeAvailabilitySource records the requested runtime keys and serves
// a fixed availability map.
type fakeRuntimeAvailabilitySource struct {
	requested [][]string
	byKeys    map[string]AccountRuntimeAvailabilityPublic
	err       error
}

func (f *fakeRuntimeAvailabilitySource) LoadRuntimeAvailabilityByRuntimeKeys(_ context.Context, runtimeKeys []string) (map[string]AccountRuntimeAvailabilityPublic, error) {
	f.requested = append(f.requested, append([]string(nil), runtimeKeys...))
	if f.err != nil {
		return nil, f.err
	}
	return f.byKeys, nil
}

// fakeCircuitSummarySource records the requested runtime keys and serves a
// fixed public summary map.
type fakeCircuitSummarySource struct {
	requested [][]string
	byKeys    map[string]AccountCircuitSummaryPublic
	err       error
}

func (f *fakeCircuitSummarySource) LoadCircuitSummariesByRuntimeKeys(_ context.Context, runtimeKeys []string) (map[string]AccountCircuitSummaryPublic, error) {
	f.requested = append(f.requested, append([]string(nil), runtimeKeys...))
	if f.err != nil {
		return nil, f.err
	}
	return f.byKeys, nil
}

// fakeAPIKeyRuntimeSummarySource records the requested account ids and serves
// a fixed pool summary map.
type fakeAPIKeyRuntimeSummarySource struct {
	requested [][]string
	byIDs     map[string]AccountApiKeyRuntimeSummaryPublic
	err       error
}

func (f *fakeAPIKeyRuntimeSummarySource) LoadAPIKeyRuntimeSummariesByAccountIds(_ context.Context, accountIDs []string) (map[string]AccountApiKeyRuntimeSummaryPublic, error) {
	f.requested = append(f.requested, append([]string(nil), accountIDs...))
	if f.err != nil {
		return nil, f.err
	}
	return f.byIDs, nil
}

func overlayListItems(t *testing.T, env *testEnv, path string) map[string]map[string]any {
	t.Helper()
	code, payload := env.do(t, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", code, payload)
	}
	return listItems(t, payload)
}

// TestListPageHydratesRuntimeOverlay locks the batch overlay: the owner row
// with all three facts carries runtimeAvailability + circuitSummary +
// apiKeyRuntime, effectiveAvailability 重算命中 api_key_pool 分支（分支顺序：
// 实例臂 → api_key_pool → runtime，池全不可用先于运行态抑制），normal 的
// circuit 摘要保持缺席。
//
// 修复前必红：ListItem 无三个 overlay 字段（JSON 缺键），且
// effectiveAvailability.status 恒 "available"（实例臂）——两处断言都失败。
func TestListPageHydratesRuntimeOverlay(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ov-1", adminID, "ov-1", "active")
	env.seedAccount(t, "acc-ov-2", adminID, "ov-2", "active")

	env.store.SetRuntimeAvailabilitySource(&fakeRuntimeAvailabilitySource{byKeys: map[string]AccountRuntimeAvailabilityPublic{
		"acc-ov-1": {Status: "local_suppressed", Reason: "上游连续失败", Since: "2026-10-03T00:00:00.000Z"},
	}})
	env.store.SetCircuitSummarySource(&fakeCircuitSummarySource{byKeys: map[string]AccountCircuitSummaryPublic{
		"acc-ov-1": {Status: "avoided", Reason: "connect_failed", Since: "2026-10-03T00:00:01.000Z", NextCheckAt: "2026-10-03T00:01:00.000Z"},
		"acc-ov-2": {Status: "normal"},
	}})
	env.store.SetAPIKeyRuntimeSummarySource(&fakeAPIKeyRuntimeSummarySource{byIDs: map[string]AccountApiKeyRuntimeSummaryPublic{
		"acc-ov-1": {Total: 3, Active: 0, TemporaryUnavailable: 1, RateLimited: 1, Error: 1, Unavailable: 3, AllUnavailable: true, NextProbeAt: "2026-10-03T01:00:00.000Z"},
	}})

	items := overlayListItems(t, env, "/__aisys__/api/accounts")
	item := items["acc-ov-1"]

	runtime, ok := item["runtimeAvailability"].(map[string]any)
	if !ok {
		t.Fatalf("runtimeAvailability 应出现：%v", item)
	}
	if runtime["status"] != "local_suppressed" || runtime["reason"] != "上游连续失败" || runtime["since"] != "2026-10-03T00:00:00.000Z" {
		t.Fatalf("runtimeAvailability 形状不符：%v", runtime)
	}
	circuit, ok := item["circuitSummary"].(map[string]any)
	if !ok {
		t.Fatalf("circuitSummary 应出现：%v", item)
	}
	if circuit["status"] != "avoided" || circuit["reason"] != "connect_failed" || circuit["nextCheckAt"] != "2026-10-03T00:01:00.000Z" {
		t.Fatalf("circuitSummary 形状不符：%v", circuit)
	}
	keyRuntime, ok := item["apiKeyRuntime"].(map[string]any)
	if !ok {
		t.Fatalf("apiKeyRuntime 应出现：%v", item)
	}
	if keyRuntime["total"] != float64(3) || keyRuntime["active"] != float64(0) ||
		keyRuntime["temporaryUnavailable"] != float64(1) || keyRuntime["rateLimited"] != float64(1) ||
		keyRuntime["error"] != float64(1) || keyRuntime["disabled"] != float64(0) ||
		keyRuntime["unavailable"] != float64(3) || keyRuntime["allUnavailable"] != true {
		t.Fatalf("apiKeyRuntime 形状不符（前 8 字段必须齐备）：%v", keyRuntime)
	}
	if keyRuntime["nextProbeAt"] != "2026-10-03T01:00:00.000Z" {
		t.Fatalf("apiKeyRuntime.nextProbeAt：%v", keyRuntime)
	}
	effective := item["effectiveAvailability"].(map[string]any)
	if effective["available"] != false || effective["status"] != "api_key_pool_unavailable" ||
		effective["blockerScope"] != "api_key_pool" ||
		effective["reason"] != "账户内 3 个 API Key 均不可用，后台探测恢复前不会参与调度" ||
		effective["retryAt"] != "2026-10-03T01:00:00.000Z" {
		t.Fatalf("effectiveAvailability 应重算 api_key_pool 分支：%v", effective)
	}

	// 无运行态事实的行：三字段缺席（JSON 键集回归），effective 保持基线。
	other := items["acc-ov-2"]
	requireMissingKeys(t, other, "runtimeAvailability", "circuitSummary", "apiKeyRuntime")
	otherEffective := other["effectiveAvailability"].(map[string]any)
	if otherEffective["available"] != true || otherEffective["status"] != "available" {
		t.Fatalf("无事实行 effective 应保持基线可调度：%v", otherEffective)
	}

	// 两行一次批量读（键与账户 id 各一批）。
	runtimeSource := env.store.runtimeAvailabilitySource.(*fakeRuntimeAvailabilitySource)
	if len(runtimeSource.requested) != 1 || len(runtimeSource.requested[0]) != 2 {
		t.Fatalf("runtime 源应一次批量读 2 键：%v", runtimeSource.requested)
	}
	keySource := env.store.apiKeyRuntimeSummarySource.(*fakeAPIKeyRuntimeSummarySource)
	if len(keySource.requested) != 1 || len(keySource.requested[0]) != 2 {
		t.Fatalf("apiKeyRuntime 源应一次批量读 2 id：%v", keySource.requested)
	}
}

// TestListPageRuntimeDegradedBranchKeepsAvailable locks the degraded branch:
// runtime_degraded 不阻断（available=true、色 gold、blockerScope=runtime），
// 且 api_key_pool 未全不可用时不产出池分支。
//
// 修复前必红：effective.status 恒 "available"，runtime_degraded 分支不可达。
func TestListPageRuntimeDegradedBranchKeepsAvailable(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ov-deg", adminID, "ov-deg", "active")

	env.store.SetRuntimeAvailabilitySource(&fakeRuntimeAvailabilitySource{byKeys: map[string]AccountRuntimeAvailabilityPublic{
		"acc-ov-deg": {Status: "degraded"},
	}})
	env.store.SetAPIKeyRuntimeSummarySource(&fakeAPIKeyRuntimeSummarySource{byIDs: map[string]AccountApiKeyRuntimeSummaryPublic{
		"acc-ov-deg": {Total: 2, Active: 2, Unavailable: 0, AllUnavailable: false},
	}})

	items := overlayListItems(t, env, "/__aisys__/api/accounts")
	effective := items["acc-ov-deg"]["effectiveAvailability"].(map[string]any)
	if effective["available"] != true || effective["status"] != "runtime_degraded" ||
		effective["blockerScope"] != "runtime" || effective["color"] != "gold" {
		t.Fatalf("degraded 分支应 available=true + runtime_degraded：%v", effective)
	}
	// 无 reason 时回落 Node 同款文案。
	if effective["reason"] != "当前账号近期失败，正常候选不足时才会兜底尝试" {
		t.Fatalf("degraded 默认 reason：%v", effective)
	}
}

// TestListPageRuntimeSuppressedEffectiveBranch locks a blocked runtime branch
// without pool facts：local_suppressed 产出 runtime_local_suppressed + 短暂
// 避让（overlay 原因优先于默认文案）。
//
// 修复前必红：effective.status 恒 "available"。
func TestListPageRuntimeSuppressedEffectiveBranch(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ov-sup", adminID, "ov-sup", "active")

	env.store.SetRuntimeAvailabilitySource(&fakeRuntimeAvailabilitySource{byKeys: map[string]AccountRuntimeAvailabilityPublic{
		"acc-ov-sup": {Status: "half_open", Reason: "租约半开确认中"},
	}})

	items := overlayListItems(t, env, "/__aisys__/api/accounts")
	effective := items["acc-ov-sup"]["effectiveAvailability"].(map[string]any)
	if effective["available"] != false || effective["status"] != "runtime_half_open" ||
		effective["blockerScope"] != "runtime" || effective["label"] != "半开探测" ||
		effective["reason"] != "租约半开确认中" {
		t.Fatalf("half_open 分支不符：%v", effective)
	}
}

// TestListPageRuntimeOverlayNilPortsKeepFieldsAbsent keeps the degraded shape:
// without any port every row stays field-absent and the page succeeds（JSON
// 键集回归：修复前后都绿，防 omitempty 回归）。
func TestListPageRuntimeOverlayNilPortsKeepFieldsAbsent(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ov-nil", adminID, "ov-nil", "active")

	items := overlayListItems(t, env, "/__aisys__/api/accounts")
	item := items["acc-ov-nil"]
	requireMissingKeys(t, item, "runtimeAvailability", "circuitSummary", "apiKeyRuntime")
	if effective := item["effectiveAvailability"].(map[string]any); effective["available"] != true {
		t.Fatalf("nil 端口 effective 应保持基线：%v", effective)
	}
}

// TestListPageRuntimeOverlaySourceErrorDegrades locks the per-source
// degradation contract: a source failure never fails the page; the matching
// field stays absent（hydrateBalanceSnapshots 同款降级）。
func TestListPageRuntimeOverlaySourceErrorDegrades(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ov-err", adminID, "ov-err", "active")
	env.store.SetRuntimeAvailabilitySource(&fakeRuntimeAvailabilitySource{err: errors.New("runtime down")})
	env.store.SetCircuitSummarySource(&fakeCircuitSummarySource{err: errors.New("control plane down")})
	env.store.SetAPIKeyRuntimeSummarySource(&fakeAPIKeyRuntimeSummarySource{err: errors.New("key states down")})

	items := overlayListItems(t, env, "/__aisys__/api/accounts")
	item := items["acc-ov-err"]
	requireMissingKeys(t, item, "runtimeAvailability", "circuitSummary", "apiKeyRuntime")
	if effective := item["effectiveAvailability"].(map[string]any); effective["available"] != true {
		t.Fatalf("读失败 effective 应保持基线：%v", effective)
	}
}

// TestListPageAuthorizedInstanceRuntimeOverlayKeyDerivation locks the
// authorized-instance derivation: the overlay keys must use
// `{id}:authorized:{systemAccountId}:{boundGroupId}:{authorizationId}`（绑定
// 齐全时）且 apiKeyRuntime 解析到来源账户 id——派生键不命中即字段缺席。
//
// 修复前必红：overlay 字段不存在，且 effective.status 恒基线实例臂。
func TestListPageAuthorizedInstanceRuntimeOverlayKeyDerivation(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "owner1", "owner-pass", "user")
	memberID := env.login(t, "member1", "member-pass", "user")
	env.seedAccount(t, "acc-rt-src", ownerID, "运行态源账户", "active")
	env.seedTeamMember(t, "team-rt", ownerID, memberID)
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-rt-src",
		GranteeType: "team", GranteeID: "team-rt",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-rt-src'`, memberID)
	if runtimeID == "" {
		t.Fatal("runtime authorization row missing")
	}
	env.seedAuthorizationInstance(t, "acc-rt-inst", memberID, runtimeID, "acc-rt-src")
	now := "2026-10-03T00:00:00.000Z"
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-rt', ?, '成员分组', 'gpt', 1, 'personal', ?, ?)`, memberID, now, now)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
		enabled, created_at, updated_at)
		VALUES (?, 'grp-rt', 'acc-rt-inst', ?, 1, ?, ?)`, memberID, runtimeID, now, now)

	derivedKey := "acc-rt-inst:authorized:" + memberID + ":grp-rt:" + runtimeID
	env.store.SetRuntimeAvailabilitySource(&fakeRuntimeAvailabilitySource{byKeys: map[string]AccountRuntimeAvailabilityPublic{
		derivedKey: {Status: "local_suppressed", Reason: "实例行运行态抑制"},
	}})
	env.store.SetAPIKeyRuntimeSummarySource(&fakeAPIKeyRuntimeSummarySource{byIDs: map[string]AccountApiKeyRuntimeSummaryPublic{
		// 池摘要按来源账户 id 读（jobs apiKeyRuntimeAccountIDOf 语义）。
		"acc-rt-src": {Total: 2, Active: 1, TemporaryUnavailable: 1, Unavailable: 1, AllUnavailable: false},
	}})

	env.login(t, "member1", "member-pass", "user")
	items := overlayListItems(t, env, "/__aisys__/api/my-accounts")
	instance, ok := items["acc-rt-inst"]
	if !ok {
		t.Fatalf("授权实例缺失：%v", items)
	}
	if instance["accessType"] != "authorized" {
		t.Fatalf("实例 accessType 应为 authorized：%v", instance["accessType"])
	}
	runtime, ok := instance["runtimeAvailability"].(map[string]any)
	if !ok {
		t.Fatalf("authorized 行应按派生键命中 runtimeAvailability：%v", instance)
	}
	if runtime["status"] != "local_suppressed" || runtime["reason"] != "实例行运行态抑制" {
		t.Fatalf("authorized 行 runtimeAvailability 形状不符：%v", runtime)
	}
	if keyRuntime, ok := instance["apiKeyRuntime"].(map[string]any); !ok || keyRuntime["total"] != float64(2) {
		t.Fatalf("authorized 行 apiKeyRuntime 应解析来源账户 id：%v", instance)
	}
	effective := instance["effectiveAvailability"].(map[string]any)
	if effective["available"] != false || effective["status"] != "runtime_local_suppressed" ||
		effective["blockerScope"] != "runtime" {
		t.Fatalf("authorized 行 effective 应命中 runtime 分支：%v", effective)
	}
}

// TestListItemRuntimeKeyDerivation locks the pure derivation table（含 jobs
// 同款回落：绑定缺失的实例行回落账户 id）。
func TestListItemRuntimeKeyDerivation(t *testing.T) {
	owner := ListItem{ID: "acc-1", AccessType: "owner"}
	if got := listItemRuntimeKey(owner); got != "acc-1" {
		t.Fatalf("owner 行 runtimeKey 应为账户 id：%q", got)
	}
	bound := ListItem{
		ID: "acc-2", AccessType: "authorized",
		OwnerSystemAccountID: "sys-1", AccountAuthorizationID: strPtr("authz-1"),
		BoundGroupID: strPtr("grp-1"), BindingSystemAccountID: strPtr("sys-1"),
	}
	if got := listItemRuntimeKey(bound); got != "acc-2:authorized:sys-1:grp-1:authz-1" {
		t.Fatalf("authorized 行 runtimeKey 派生不符：%q", got)
	}
	// 绑定归属另一系统账户（bindingSystemAccountID != systemAccountID）→ 回落。
	unboundOwner := bound
	unboundOwner.BindingSystemAccountID = strPtr("sys-other")
	if got := listItemRuntimeKey(unboundOwner); got != "acc-2" {
		t.Fatalf("绑定归属不符应回落账户 id：%q", got)
	}
	// 缺授权 id / 缺分组 → 回落。
	noAuthz := bound
	noAuthz.AccountAuthorizationID = nil
	if got := listItemRuntimeKey(noAuthz); got != "acc-2" {
		t.Fatalf("缺授权 id 应回落：%q", got)
	}
	noGroup := bound
	noGroup.BoundGroupID = nil
	if got := listItemRuntimeKey(noGroup); got != "acc-2" {
		t.Fatalf("缺分组应回落：%q", got)
	}
	// apiKeyRuntime 查找 id：实例行解析来源账户，owner 行用自身。
	instance := ListItem{ID: "acc-3", AuthorizationInstanceSourceAccountID: strPtr("acc-src")}
	if got := listItemAPIKeyRuntimeAccountID(instance); got != "acc-src" {
		t.Fatalf("实例行池摘要应解析来源账户 id：%q", got)
	}
	if got := listItemAPIKeyRuntimeAccountID(owner); got != "acc-1" {
		t.Fatalf("owner 行池摘要应用自身 id：%q", got)
	}
}

// strPtr is the local string pointer helper（invalidation_test.go 的 stringPtr
// 已占用该名，这里用别名避免重声明）。
func strPtr(value string) *string { return &value }

// TestListPageCircuitSummaryNormalOmitted pins the filter contract at the DTO
// level: a normal summary never serializes（前端按缺席过滤 normal）。
func TestListPageCircuitSummaryNormalOmitted(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-ov-nrm", adminID, "ov-nrm", "active")
	env.store.SetCircuitSummarySource(&fakeCircuitSummarySource{byKeys: map[string]AccountCircuitSummaryPublic{
		"acc-ov-nrm": {Status: "normal"},
	}})

	items := overlayListItems(t, env, "/__aisys__/api/accounts")
	requireMissingKeys(t, items["acc-ov-nrm"], "circuitSummary")
}
