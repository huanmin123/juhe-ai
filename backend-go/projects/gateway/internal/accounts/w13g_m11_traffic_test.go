package accounts

// w13g m11_traffic_migration.go 深度补测：授权实例流量迁移分支
//（migrateAuthorizedBindingTraffic 守卫矩阵与两个 sourceStatus 写入臂）、
// owner 分支的空 ID/读错误臂、runtime 输入构造与纯函数。
//
// 不可达登记（w13g）：
// - 165-167 / 172-174 trafficManagedRow 的 QueryRow err：前置 findTrafficSummary
//   的 ListPage 与该查询同表（accounts 及其 join），表级故障必先在 134-136
//   返回；单连接 SQLite 无法在两次查询之间注入故障。
// - 205-214 / 292-301 unchanged 与更新后 summary 的 err/nil 臂：同一次调用内
//   ownerAccess 与外层 access 读到的可见集合一致，行在迁移前后不消失。
// - 237-239 BeginTx err：同上，前置查询必先失败。
// - 282-284 / 288-290 owner UPDATE err 与 commit err：单连接限制。
// - 285-287 / 411-413 UPDATE affected==0：CAS 条件（system_account_id、
//   authorization stamp、enabled binding EXISTS）与 331-346 的前置守卫读取
//   同一快照，单连接内无法制造失配。
// - 414-424 更新后 nextSource/nextTarget err/nil：同 205-214。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

// w13gTrafficAuthorizedEnv 复用 m10 的授权装配并把带 reader 的 store 暴露给
// 直接调用。
func w13gTrafficAuthorizedEnv(t *testing.T) (*testEnv, *authz.Store, *Store) {
	t.Helper()
	base := newTestEnv(t)
	for _, statement := range m10AuthzDDL {
		base.exec(t, statement)
	}
	authzStore, err := authz.NewStore(base.db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(base.db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.SetAuthorizedReader(authzStore)
	return base, authzStore, store
}

// w13gSeedTrafficInstance 建立一个授权实例：源账户 + team 授权 + grantee 侧
// 实例行 + 绑定 grantee 默认组。返回实例 ID。
func w13gSeedTrafficInstance(t *testing.T, env *testEnv, authzStore *authz.Store, ownerID, memberID, sourceID, suffix string) string {
	t.Helper()
	env.seedAccount(t, sourceID, ownerID, "w13g-tm-src-"+suffix, "active")
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: sourceID,
		GranteeType: "team", GranteeID: "team-w13g-tm",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = ?`, memberID, sourceID)
	if runtimeID == "" {
		t.Fatalf("runtime row missing：%s", suffix)
	}
	instanceID := "acc-w13g-inst-" + suffix
	env.seedAuthorizationInstance(t, instanceID, memberID, runtimeID, sourceID)
	groupID := env.queryCell(t, `SELECT id FROM groups WHERE system_account_id = ? AND is_default = 1`, memberID)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at, account_authorization_id)
		VALUES (?, ?, ?, 1, ?, ?, ?)`, memberID, groupID, instanceID,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", runtimeID)
	return instanceID
}

func TestW13GMigrateAuthorizedTrafficMatrix(t *testing.T) {
	env, authzStore, store := w13gTrafficAuthorizedEnv(t)
	ownerID := env.login(t, "tm-owner-w13g", "owner-pass", "user")
	memberID := env.login(t, "tm-member-w13g", "member-pass", "user")
	env.seedProviderAndDefaultGroup(t, ownerID)
	env.seedProviderAndDefaultGroup(t, memberID)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, memberID)
	instA := w13gSeedTrafficInstance(t, env, authzStore, ownerID, memberID, "acc-w13g-tmsrc-a", "a")
	instB := w13gSeedTrafficInstance(t, env, authzStore, ownerID, memberID, "acc-w13g-tmsrc-b", "b")
	memberAccess := AccessScope{ViewerID: memberID}

	// 同账户。
	if _, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instA}, memberAccess); err != errTrafficSameAccount {
		t.Fatalf("同账户应报错：%v", err)
	}
	// grantee 上下文为空 → (nil,nil)。
	if out, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instB}, AccessScope{}); err != nil || out != nil {
		t.Fatalf("空 grantee：%v %v", out, err)
	}
	// 目标缺失 → (nil,nil)。
	if out, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: "acc-w13g-none"}, memberAccess); err != nil || out != nil {
		t.Fatalf("目标缺失：%v %v", out, err)
	}
	// 目标为 owner 普通账户（可见但无绑定分组）→ 分组守卫文案。
	env.seedAccount(t, "acc-w13g-owner-x", memberID, "w13g-owner-x", "active")
	if _, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: "acc-w13g-owner-x"}, memberAccess); err == nil ||
		err.Error() != "目标账户必须和当前账户在你的同一个分组内" {
		t.Fatalf("跨组守卫：%v", err)
	}
	// 跨供应商目标（另一个授权实例，供应商不同）→ 供应商守卫。
	// 修复（w13g）：不再手工 INSERT resource_authorizations —— team 授权创建时
	// 已为 member 物化 (account, source, member) 运行时行，重复插入撞
	// UNIQUE(resource_type, resource_id, grantee_system_account_id)；改用独立
	// 源账户 + 复用既有运行时行。授权实例摘要的 ProviderCode 由源账户覆盖
	//（list.go 源值替换），因此必须改源账户的供应商，改实例行无效。
	instX := w13gSeedTrafficInstance(t, env, authzStore, ownerID, memberID, "acc-w13g-tmsrc-x", "x")
	env.exec(t, `UPDATE accounts SET provider_code = 'tx' WHERE id = 'acc-w13g-tmsrc-x'`)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-w13g-tx', ?, 'TX 组', 'tx', 1, 0, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, memberID)
	env.exec(t, `UPDATE group_accounts SET group_id = 'grp-w13g-tx', updated_at = '2026-01-01T00:00:00Z'
		WHERE account_id = ?`, instX)
	// 先在同组外比较（跨组）。
	if _, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instX}, memberAccess); err == nil ||
		!strings.Contains(err.Error(), "同一个分组内") {
		t.Fatalf("跨组实例守卫：%v", err)
	}
	// 同组同供应商但源供应商不同：把 inst-x 移回 member 默认组。
	defaultGroup := env.queryCell(t, `SELECT id FROM groups WHERE system_account_id = ? AND is_default = 1`, memberID)
	env.exec(t, `UPDATE group_accounts SET group_id = ?, updated_at = '2026-01-02T00:00:00Z' WHERE account_id = ?`, defaultGroup, instX)
	if _, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instX}, memberAccess); err == nil ||
		err.Error() != "目标账户必须和当前账户属于同一个供应商" {
		t.Fatalf("跨供应商守卫：%v", err)
	}

	// 目标不可调度（error 态）→ 目标不可用文案。
	env.exec(t, `UPDATE accounts SET status = 'error' WHERE id = ?`, instB)
	if _, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instB}, memberAccess); err == nil ||
		err.Error() != "目标账户当前不可调度，请选择正常可用的账户" {
		t.Fatalf("目标不可用：%v", err)
	}
	env.exec(t, `UPDATE accounts SET status = 'active' WHERE id = ?`, instB)

	// unchanged：不落任何写，返回两侧摘要。
	result, err := store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instB, SourceStatus: trafficSourceUnchanged}, memberAccess)
	if err != nil || result == nil {
		t.Fatalf("unchanged 迁移：%v", err)
	}
	if result.SourceStatus != string(trafficSourceUnchanged) || result.GroupID == nil || *result.GroupID != defaultGroup {
		t.Fatalf("unchanged 结果：%+v", result)
	}
	var status string
	if err := env.db.QueryRow(`SELECT status FROM accounts WHERE id = ?`, instA).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("unchanged 不应改状态：%s", status)
	}

	// 默认 sourceStatus（空）→ temporary_unavailable 写入 + 冷却列。
	result, err = store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instB}, memberAccess)
	if err != nil || result == nil {
		t.Fatalf("临时不可用迁移：%v", err)
	}
	if result.SourceStatus != string(trafficSourceTemporaryUnavailable) || result.SourceCooldownUntil == nil {
		t.Fatalf("临时不可用结果：%+v", result)
	}
	if err := env.db.QueryRow(`SELECT status, schedulable FROM accounts WHERE id = ?`, instA).Scan(&status, new(int)); err != nil {
		t.Fatal(err)
	}
	if status != "temporary_unavailable" {
		t.Fatalf("源应临时不可用：%s", status)
	}

	// disabled：停用并关调度。
	env.exec(t, `UPDATE accounts SET status = 'active', cooldown_until = NULL WHERE id = ?`, instA)
	result, err = store.MigrateTraffic(context.Background(), instA,
		TrafficMigrationInput{TargetAccountID: instB, SourceStatus: trafficSourceDisabled}, memberAccess)
	if err != nil || result == nil {
		t.Fatalf("停用迁移：%v", err)
	}
	if result.SourceStatus != string(trafficSourceDisabled) || result.SourceCooldownUntil != nil {
		t.Fatalf("停用结果：%+v", result)
	}
	var schedulable int
	if err := env.db.QueryRow(`SELECT status, schedulable FROM accounts WHERE id = ?`, instA).Scan(&status, &schedulable); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" || schedulable != 0 {
		t.Fatalf("停用行：%s %d", status, schedulable)
	}

	// UPDATE 内的 EXISTS 子查询失败（group_accounts 表缺失）→ 错误传播。
	instC := w13gSeedTrafficInstance(t, env, authzStore, ownerID, memberID, "acc-w13g-tmsrc-c", "c")
	env.exec(t, `DROP TABLE group_accounts`)
	if _, err := store.MigrateTraffic(context.Background(), instC,
		TrafficMigrationInput{TargetAccountID: instB}, memberAccess); err == nil {
		t.Fatal("EXISTS 子查询失败应报错")
	}
}

func TestW13GMigrateTrafficErrorArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 空 source ID → (nil,nil)。
	if out, err := env.store.MigrateTraffic(context.Background(), "  ",
		TrafficMigrationInput{TargetAccountID: "x"}, scope); err != nil || out != nil {
		t.Fatalf("空 ID：%v %v", out, err)
	}
	// 源摘要读错误 → ListPage 错误传播。
	env.seedAccount(t, "acc-w13g-tme", adminID, "w13g-tme", "active")
	env.exec(t, `DROP TABLE groups`)
	if _, err := env.store.MigrateTraffic(context.Background(), "acc-w13g-tme",
		TrafficMigrationInput{TargetAccountID: "x"}, scope); err == nil {
		t.Fatal("ListPage 错误应传播")
	}
}

func TestW13GTrafficRuntimeInputAndPureHelpers(t *testing.T) {
	// isLaterInstant 纯函数矩阵。
	if isLaterInstant("", "x") || isLaterInstant("x", "") || isLaterInstant("bad", "2026-01-01T00:00:00Z") {
		t.Fatal("空/坏输入应 false")
	}
	if !isLaterInstant("2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z") {
		t.Fatal("更晚应 true")
	}
	if isLaterInstant("2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z") {
		t.Fatal("更早应 false")
	}
	// nilToEmpty / nullableStringPointer。
	if nilToEmpty(" ") != nil || nilToEmpty("x") != "x" || nullableStringPointer(" ") != nil {
		t.Fatal("nil 归一")
	}
	if newCooldownGeneration() == "" || !strings.HasPrefix(newCooldownGeneration(), "cooldown:") {
		t.Fatal("冷却代次格式")
	}

	// buildRuntimeMigrationInput：unchanged + authorized → PreferMigratedSessions
	// 与 AffinityScope。
	groupID := "g1"
	authorized := &ListItem{ID: "s1", AccessType: "authorized", BoundGroupID: &groupID, OwnerSystemAccountID: "owner-1"}
	runtime := buildRuntimeMigrationInput(&TrafficMigrationResult{
		SourceAccount: authorized, TargetAccount: &ListItem{ID: "t1"}, SourceStatus: string(trafficSourceUnchanged),
	}, TrafficMigrationInput{TargetAccountID: "t1"}, AccessScope{ViewerID: "viewer-1"})
	if !runtime.PreferMigratedSessions || runtime.AffinityScope == nil ||
		runtime.AffinityScope.SystemAccountID != "viewer-1" || runtime.AffinityScope.GroupID != groupID ||
		runtime.PreferenceScope != nil {
		t.Fatalf("unchanged authorized runtime：%+v", runtime)
	}
	if runtime.SourceAccountID != "s1" || runtime.TargetAccountID != "t1" {
		t.Fatalf("目标对：%+v", runtime)
	}

	// 临时不可用 + authorized → PreferenceScope 用 viewer。
	runtime = buildRuntimeMigrationInput(&TrafficMigrationResult{
		SourceAccount: authorized, TargetAccount: &ListItem{ID: "t1"},
		SourceStatus: string(trafficSourceTemporaryUnavailable), GroupID: &groupID,
	}, TrafficMigrationInput{TargetAccountID: "t1"}, AccessScope{ViewerID: "viewer-1"})
	if runtime.PreferenceScope == nil || runtime.PreferenceScope.SystemAccountID != "viewer-1" ||
		runtime.AffinityScope == nil {
		t.Fatalf("changed authorized runtime：%+v", runtime)
	}

	// owner 分支 → PreferenceScope 用 OwnerSystemAccountID；GroupID 回落绑定组。
	owner := &ListItem{ID: "s2", AccessType: "owner", BoundGroupID: &groupID, OwnerSystemAccountID: "owner-2"}
	runtime = buildRuntimeMigrationInput(&TrafficMigrationResult{
		SourceAccount: owner, TargetAccount: &ListItem{ID: "t2"},
		SourceStatus: string(trafficSourceDisabled),
	}, TrafficMigrationInput{TargetAccountID: "t2"}, AccessScope{ViewerID: "viewer-2"})
	if runtime.PreferenceScope == nil || runtime.PreferenceScope.SystemAccountID != "owner-2" ||
		runtime.PreferenceScope.GroupID != groupID || runtime.AffinityScope != nil {
		t.Fatalf("owner runtime：%+v", runtime)
	}
}

func TestW13GTrafficMigrationRouteRuntimePort(t *testing.T) {
	// HTTP 面：runtime 端口故障渲染 400，成功时回填迁移会话数。
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13g-tr-s", adminID, "w13g-tr-s", "active")
	env.seedAccount(t, "acc-w13g-tr-t", adminID, "w13g-tr-t", "active")
	now := "2026-01-01T00:00:00Z"
	for _, id := range []string{"acc-w13g-tr-s", "acc-w13g-tr-t"} {
		env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)`, adminID, "grp-default-"+adminID, id, now, now)
	}
	env.store.SetTrafficRuntimeMigrator(w13gFailingMigrator{})
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13g-tr-s/traffic-migration",
		`{"targetAccountId":"acc-w13g-tr-t"}`)
	// 修复（w13g）：kernel 对 4xx 会把非 CJK 消息本地化为状态默认文案，故障
	// 消息必须含中文才能原样透出。
	if code != http.StatusBadRequest || payload["message"] != "w13g 运行时端口不可用" {
		t.Fatalf("端口故障应 400：%d %v", code, payload)
	}
	// 成功端口：会话数回填。
	env.store.SetTrafficRuntimeMigrator(w13gCountingMigrator{count: 4})
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13g-tr-s/traffic-migration",
		`{"targetAccountId":"acc-w13g-tr-t","sourceStatus":"unchanged"}`)
	if code != http.StatusOK {
		t.Fatalf("端口成功应 200：%d %v", code, payload)
	}
	if payload["data"].(map[string]any)["migratedSessionCount"] != float64(4) {
		t.Fatalf("会话数回填：%v", payload)
	}
	// 未登录 → 401。
	request, err := http.NewRequest(http.MethodPost, env.server.URL+"/__aisys__/api/accounts/acc-w13g-tr-s/traffic-migration",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401：%d", response.StatusCode)
	}
	// 空 systemAccountId → 400。
	if code, _ := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13g-tr-s/traffic-migration?systemAccountId=%20", `{}`); code != http.StatusBadRequest {
		t.Fatal("空白作用域应 400")
	}
	_ = time.Now
}

type w13gFailingMigrator struct{}

func (w13gFailingMigrator) MigrateOpenAIAccountTrafficRuntime(context.Context, TrafficRuntimeMigrationInput) (int, error) {
	return 0, &trafficMigrationFailure{message: "w13g 运行时端口不可用"}
}

type w13gCountingMigrator struct{ count int }

func (m w13gCountingMigrator) MigrateOpenAIAccountTrafficRuntime(context.Context, TrafficRuntimeMigrationInput) (int, error) {
	return m.count, nil
}
