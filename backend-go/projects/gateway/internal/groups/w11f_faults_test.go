package groups

// w11f 覆盖波次（文件 2/2）：store/options/routes 的故障注入与 HTTP 分支。

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// w11fReqAuth 构造带 auth 上下文与 Content-Length 的请求。
func w11fReqAuth(method, target, body, accountID, role string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", itoa(len(body)))
	return request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{
		SystemAccountID: accountID, Username: accountID, Role: role,
	}))
}

func w11fInvoke(t *testing.T, handler http.Handler, request *http.Request) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	raw, _ := io.ReadAll(recorder.Result().Body)
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return recorder.Code, payload
}

// ---------------------------------------------------------------------------
// store 写路径故障
// ---------------------------------------------------------------------------

func TestW11FCreateFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	admin := AccessScope{ViewerID: "w11f-owner", IsAdmin: true}
	name := "w11f-组"
	provider := "openai"
	high := GroupTypeHighConcurrency

	// 缺上下文 / 名称 / 供应商 / 类型 / 策略。
	if _, err := store.Create(context.Background(), MutationInput{}, AccessScope{}); err == nil {
		t.Fatal("Create empty scope must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{ProviderCode: &provider}, admin); err == nil {
		t.Fatal("Create no name must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{Name: &name}, admin); err == nil {
		t.Fatal("Create no provider must fail")
	}
	bogusType := "bogus"
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &provider, GroupType: &bogusType}, admin); err == nil {
		t.Fatal("Create bogus type must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &provider, GroupType: &high, SchedulingPolicy: map[string]any{"mode": "balanced_fast"}}, admin); err == nil {
		t.Fatal("Create unknown policy key must fail")
	}
	// 供应商查询错误 / 未知供应商 / 停用供应商。
	env.script.failQuery("SELECT enabled FROM")
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &provider}, admin); err == nil {
		t.Fatal("Create provider query fault must fail")
	}
	missing := "ghost-provider"
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &missing}, admin); err == nil || !strings.Contains(err.Error(), "不支持的供应商") {
		t.Fatalf("Create missing provider = %v", err)
	}
	disabled := "disabled-provider"
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &disabled}, admin); err == nil || !strings.Contains(err.Error(), "供应商已停用") {
		t.Fatalf("Create disabled provider = %v", err)
	}
	// INSERT 错误 / 名称重复。
	env.script.failExec("INSERT INTO groups")
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &provider}, admin); err == nil {
		t.Fatal("Create insert fault must fail")
	}
	if _, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &provider}, admin); err != nil {
		t.Fatal(err)
	}
	_, err := store.Create(context.Background(), MutationInput{Name: &name, ProviderCode: &provider}, admin)
	if err == nil || !strings.Contains(err.Error(), "同一供应商下分组名称已存在") {
		t.Fatalf("Create duplicate = %v", err)
	}
	// 高并发创建带策略 + enabled=false（换名避开唯一索引）。
	highName := "w11f-高并发组"
	item, err := store.Create(context.Background(), MutationInput{
		Name: &highName, ProviderCode: &provider, GroupType: &high,
		SchedulingPolicy: map[string]any{"maxQueueWaitMs": float64(60000)},
	}, admin)
	if err != nil || item.GroupType != GroupTypeHighConcurrency {
		t.Fatalf("Create high concurrency = %+v/%v", item, err)
	}
}

func TestW11FPatchFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	owner := AccessScope{ViewerID: "w11f-owner"}
	revision := env.w11fSeedGroup(t, "w11f-p1", "w11f-owner", "w11f-补丁组", "openai", 1, 0, "personal")
	env.w11fSeedGroup(t, "w11f-p2", "w11f-owner", "w11f-默认组", "openai", 1, 1, "personal")
	newName := "w11f-新名"

	// Begin / 行查询 / 不存在 / 默认分组只读 / 过期版本。
	env.script.failBegin()
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, revision, owner); err == nil {
		t.Fatal("Patch begin fault must fail")
	}
	env.script.failQuery("SELECT g.id")
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, revision, owner); err == nil {
		t.Fatal("Patch row fault must fail")
	}
	if result, err := store.Patch(context.Background(), "w11f-missing", MutationInput{Name: &newName}, revision, owner); err != nil || result != nil {
		t.Fatalf("Patch missing = %v/%v", result, err)
	}
	if _, err := store.Patch(context.Background(), "w11f-p2", MutationInput{Name: &newName}, revision, owner); err == nil || !strings.Contains(err.Error(), "默认分组不允许修改") {
		t.Fatalf("Patch default group = %v", err)
	}
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, "2001-01-01T00:00:00.000Z", owner); err == nil {
		t.Fatal("Patch stale version must conflict")
	}
	// 名称 / 供应商 / 说明校验与坏 stored 值。
	blank := " "
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &blank}, revision, owner); err == nil {
		t.Fatal("Patch blank name must fail")
	}
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{ProviderCode: &blank}, revision, owner); err == nil {
		t.Fatal("Patch blank provider must fail")
	}
	bogus := "bogus"
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{GroupType: &bogus}, revision, owner); err == nil {
		t.Fatal("Patch bogus type must fail")
	}
	env.exec(t, `UPDATE groups SET group_type = 'corrupt' WHERE id = 'w11f-p1'`)
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{GroupType: bogusPtr("personal")}, revision, owner); err == nil {
		t.Fatal("Patch corrupt stored type must fail")
	}
	env.exec(t, `UPDATE groups SET group_type = 'personal' WHERE id = 'w11f-p1'`)
	// 坏 stored updated_at → nextGroupUpdatedAt 错误。
	env.exec(t, `UPDATE groups SET updated_at = 'zzz' WHERE id = 'w11f-p1'`)
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, "", owner); err == nil {
		t.Fatal("Patch garbage stored updated_at must fail")
	}
	env.exec(t, `UPDATE groups SET updated_at = ? WHERE id = 'w11f-p1'`, revision)

	// 供应商切换: 未知供应商 / 已停用 / 有账户 → 拒绝。
	anthropic := "anthropic"
	missingProvider := "ghost-provider"
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{ProviderCode: &missingProvider}, revision, owner); err == nil || !strings.Contains(err.Error(), "不支持的供应商") {
		t.Fatalf("Patch missing provider = %v", err)
	}
	disabledProvider := "disabled-provider"
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{ProviderCode: &disabledProvider}, revision, owner); err == nil || !strings.Contains(err.Error(), "供应商已停用") {
		t.Fatalf("Patch disabled provider = %v", err)
	}
	env.exec(t, `INSERT INTO accounts (id, system_account_id, created_at) VALUES ('w11f-acc1', 'w11f-owner', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('w11f-owner', 'w11f-p1', 'w11f-acc1', 1, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{ProviderCode: &anthropic}, revision, owner); err == nil || !strings.Contains(err.Error(), "已有账户的分组不允许修改供应商") {
		t.Fatalf("Patch provider with accounts = %v", err)
	}
	// ownedGroupHasAccounts 查询错误。
	env.script.failQuery("FROM group_accounts")
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{ProviderCode: &anthropic}, revision, owner); err == nil {
		t.Fatal("Patch hasAccounts fault must fail")
	}

	// UPDATE 错误 / affected 0 / 重复名 / commit。
	env.script.failExec("UPDATE groups")
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, revision, owner); err == nil {
		t.Fatal("Patch update fault must fail")
	}
	env.script.execAffected("UPDATE groups", 0)
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, revision, owner); err == nil {
		t.Fatal("Patch affected0 must conflict")
	}
	env.w11fSeedGroup(t, "w11f-p3", "w11f-owner", "w11f-目标名", "openai", 1, 0, "personal")
	taken := "w11f-目标名"
	_, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &taken}, revision, owner)
	if err == nil || !strings.Contains(err.Error(), "同一供应商下分组名称已存在") {
		t.Fatalf("Patch duplicate = %v", err)
	}
	env.script.failCommit()
	if _, err := store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, revision, owner); err == nil {
		t.Fatal("Patch commit fault must fail")
	}
	// noop（无变更）。
	result, err := store.Patch(context.Background(), "w11f-p1", MutationInput{}, revision, owner)
	if err != nil || len(result.ChangedFields) != 0 {
		t.Fatalf("Patch noop = %+v/%v", result, err)
	}
	// 正常改名。
	result, err = store.Patch(context.Background(), "w11f-p1", MutationInput{Name: &newName}, revision, owner)
	if err != nil || result.Name != newName {
		t.Fatalf("Patch hit = %+v/%v", result, err)
	}
}

func bogusPtr(v string) *string      { return &v }
func missing2() string               { return "ghost-provider" }
func disabledPtr() *string           { d := "disabled-provider"; return &d }
func w11fPolicyPtr() *string         { return nil }
func w11fTimePtrFuture() *string     { f := "2999-01-01T00:00:00Z"; return &f }
func w11fTimePtrPast() *string       { p := "2001-01-01T00:00:00Z"; return &p }
func w11fTimePtrBad() *string        { b := "not-a-time"; return &b }
func w11fStringPtr(v string) *string { return &v }

func TestW11FRouteStrategyGuards(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	owner := AccessScope{ViewerID: "w11f-owner"}
	revision := env.w11fSeedGroup(t, "w11f-rg", "w11f-owner", "w11f-路由组", "openai", 1, 0, "personal")

	// 唯一可用分组 → 停用被拒。
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name) VALUES ('w11f-rs1', 'w11f-owner', 'w11f-策略1')`)
	env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
		VALUES ('w11f-rsg1', 'w11f-rs1', 'w11f-owner', 'w11f-rg', 'active', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	disable := false
	_, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner)
	if err == nil || !strings.Contains(err.Error(), "唯一可用启用分组") {
		t.Fatalf("disable sole binding = %v", err)
	}
	// 加一个策略使 blocker 超过 3 个 → 名称截断。
	for i := 0; i < 4; i++ {
		env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name) VALUES (?, 'w11f-owner', ?)`, "w11f-rs"+itoa(i+2), "w11f-策略"+itoa(i+2))
		env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
			VALUES (?, ?, 'w11f-owner', 'w11f-rg', 'active', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`,
			"w11f-rsg"+itoa(i+2), "w11f-rs"+itoa(i+2))
	}
	_, err = store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner)
	if err == nil || !strings.Contains(err.Error(), "等 5 个") {
		t.Fatalf("many blockers = %v", err)
	}
	// 候选查询错误 / Scan 错误 / rows.Err。
	env.script.failQuery("FROM route_strategy_groups")
	if _, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner); err == nil {
		t.Fatal("candidates fault must fail")
	}
	env.script.canned("FROM route_strategy_groups", []string{"route_strategy_id", "name", "status"},
		[][]driver.Value{{nil, "n", "active"}}, nil)
	if _, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner); err == nil {
		t.Fatal("candidates scan fault must fail")
	}
	env.script.canned("FROM route_strategy_groups", []string{"route_strategy_id", "name", "status"},
		[][]driver.Value{{"s", "n", "active"}}, w11fBoom)
	if _, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner); err == nil {
		t.Fatal("candidates rows.Err fault must fail")
	}
	// 绑定计数查询错误。
	env.script.failQuery("FROM route_strategy_groups route_strategy_groups2")
	if _, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner); err == nil && !strings.Contains(err.Error(), "唯一可用") {
		t.Fatalf("binding counts fault = %v", err)
	}
	// 解除绑定后停用成功（无候选 → nil 变更）。
	env.exec(t, `DELETE FROM route_strategy_groups WHERE group_id = 'w11f-rg'`)
	result, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner)
	if err != nil || len(result.ChangedFields) == 0 {
		t.Fatalf("disable hit = %+v/%v", result, err)
	}
	// 超过 100 个候选 → 数量上限。
	env.exec(t, `UPDATE groups SET enabled = 1 WHERE id = 'w11f-rg'`)
	revision = env.queryString(t, `SELECT updated_at FROM groups WHERE id = 'w11f-rg'`)
	for i := 0; i < 101; i++ {
		env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name) VALUES (?, 'w11f-owner', ?)`, "w11f-bulk"+itoa(i), "w11f-批量"+itoa(i))
		env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
			VALUES (?, ?, 'w11f-owner', 'w11f-rg', 'disabled', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`,
			"w11f-bulkg"+itoa(i), "w11f-bulk"+itoa(i))
	}
	if _, err := store.Patch(context.Background(), "w11f-rg", MutationInput{Enabled: &disable}, revision, owner); err == nil || !strings.Contains(err.Error(), "超过 100 个") {
		t.Fatalf("bulk candidates = %v", err)
	}
}

func TestW11FDeleteFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	owner := AccessScope{ViewerID: "w11f-owner"}

	// Begin / 行查询错误 / 不存在 / 默认分组。
	env.script.failBegin()
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete begin fault must fail")
	}
	env.w11fSeedGroup(t, "w11f-d1", "w11f-owner", "w11f-删除组", "openai", 1, 0, "personal")
	env.w11fSeedGroup(t, "w11f-d2", "w11f-owner", "w11f-默认删除", "openai", 1, 1, "personal")
	env.script.failQuery("SELECT g.system_account_id, g.name, g.is_default")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete row fault must fail")
	}
	if result, err := store.Delete(context.Background(), "w11f-missing", owner); err != nil || result.Deleted {
		t.Fatalf("Delete missing = %+v/%v", result, err)
	}
	if _, err := store.Delete(context.Background(), "w11f-d2", owner); err == nil || !strings.Contains(err.Error(), "默认分组不能删除") {
		t.Fatalf("Delete default = %v", err)
	}
	// 候选查询错误。
	env.script.failQuery("FROM route_strategy_groups")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete candidates fault must fail")
	}
	// 各 DELETE 语句错误。
	env.script.failExec("DELETE FROM route_strategy_groups")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete rsg fault must fail")
	}
	env.script.failExec("DELETE FROM group_accounts")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete group_accounts fault must fail")
	}
	env.script.failExec("DELETE FROM group_authorization_settings")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete settings fault must fail")
	}
	env.script.failExec("DELETE FROM groups")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete groups fault must fail")
	}
	// affected 0 → 未删除。
	env.script.execAffected("DELETE FROM groups", 0)
	if result, err := store.Delete(context.Background(), "w11f-d1", owner); err != nil || result.Deleted {
		t.Fatalf("Delete affected0 = %+v/%v", result, err)
	}
	// commit / 统计脏标错误。
	env.script.failCommit()
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete commit fault must fail")
	}
	env.script.failExec("INSERT INTO group_account_stats_dirty")
	if _, err := store.Delete(context.Background(), "w11f-d1", owner); err == nil {
		t.Fatal("Delete stats dirty fault must fail")
	}
	// 正常删除（上一轮提交成功后统计报错，分组已删，重新播种）。
	env.w11fSeedGroup(t, "w11f-d3", "w11f-owner", "w11f-再次删除", "openai", 1, 0, "personal")
	result, err := store.Delete(context.Background(), "w11f-d3", owner)
	if err != nil || !result.Deleted || result.Name != "w11f-再次删除" {
		t.Fatalf("Delete hit = %+v/%v", result, err)
	}
}

// ---------------------------------------------------------------------------
// store 读路径故障
// ---------------------------------------------------------------------------

func TestW11FFindDetailFaults(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	revision := env.w11fSeedGroup(t, "w11f-f1", "w11f-owner", "w11f-详情组", "openai", 1, 0, "personal")

	// 查询错误 / 不存在 / 坏 group_type。
	env.script.failQuery("FROM groups g WHERE g.id = ?")
	if _, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-owner", IsAdmin: true}); err == nil {
		t.Fatal("FindDetail query fault must fail")
	}
	if detail, err := store.FindDetail(context.Background(), "w11f-missing", AccessScope{ViewerID: "w11f-owner", IsAdmin: true}); err != nil || detail != nil {
		t.Fatalf("FindDetail missing = %v/%v", detail, err)
	}
	env.exec(t, `UPDATE groups SET group_type = 'corrupt' WHERE id = 'w11f-f1'`)
	if _, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-owner", IsAdmin: true}); err == nil {
		t.Fatal("FindDetail corrupt type must fail")
	}
	// 高并发分组坏策略 JSON。
	env.exec(t, `UPDATE groups SET group_type = 'high_concurrency', scheduling_policy_json = '{bad' WHERE id = 'w11f-f1'`)
	if _, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-owner", IsAdmin: true}); err == nil {
		t.Fatal("FindDetail corrupt policy must fail")
	}
	full := `{"mode":"balanced_fast","defaultSoftConcurrency":5000,"fastFirstEnabled":true,"fallbackOnQueueEnabled":true,"breakAffinityOnSoftLimit":true,"breakAffinityOnQueueWaitMs":0,"slowRequestThresholdMs":30000,"firstOutputSlowThresholdMs":15000,"recentTimeoutWindowSeconds":120,"recentTimeoutPenaltyThreshold":2,"maxQueueWaitMs":60000,"maxQueueSize":5000,"perApiKeyQueueLimit":5000,"clientIpConcurrencyLimit":0,"clientIpConcurrencyOverflowMode":"reject","imageLaneMaxConcurrency":0}`
	env.exec(t, `UPDATE groups SET scheduling_policy_json = ? WHERE id = 'w11f-f1'`, full)
	detail, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-owner", IsAdmin: true})
	if err != nil || detail.GroupType != GroupTypeHighConcurrency || detail.SchedulingPolicy == nil {
		t.Fatalf("FindDetail hc hit = %+v/%v", detail, err)
	}
	// owner 视角: 成员账号查询错误。
	env.exec(t, `UPDATE groups SET group_type = 'personal', scheduling_policy_json = NULL WHERE id = 'w11f-f1'`)
	env.exec(t, `UPDATE groups SET updated_at = ? WHERE id = 'w11f-f1'`, revision)
	env.script.failQuery("FROM group_accounts")
	if _, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-owner"}); err == nil {
		t.Fatal("FindDetail member fault must fail")
	}
	// systemAccountNames 查询错误。
	env.script.failQuery("SELECT id, display_name FROM")
	if _, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-owner"}); err == nil {
		t.Fatal("FindDetail names fault must fail")
	}
	// 授权视角: 授权来源查询错误。
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('w11f-ra1', 'group', 'w11f-f1', 'w11f-owner', 'w11f-grantee', 'active', 'w11f-owner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	env.script.failQuery("FROM resource_authorization_sources")
	if _, err := store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-grantee"}); err == nil {
		t.Fatal("FindDetail sources fault must fail")
	}
	// 授权视角命中（accountIds 为空、访问类型 authorized）。
	detail, err = store.FindDetail(context.Background(), "w11f-f1", AccessScope{ViewerID: "w11f-grantee"})
	if err != nil || detail == nil {
		t.Fatalf("FindDetail authorized hit = %+v/%v", detail, err)
	}
}

// ---------------------------------------------------------------------------
// options 读路径与路由
// ---------------------------------------------------------------------------

func TestW11FOptionsStore(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	env.w11fSeedGroup(t, "w11f-o1", "w11f-owner", "w11f-选项组", "openai", 1, 0, "personal")
	env.w11fSeedGroup(t, "w11f-o2", "w11f-owner2", "w11f-他人组", "openai", 1, 1, "personal")
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, expires_at, created_by, created_at, updated_at)
		VALUES ('w11f-oa1', 'group', 'w11f-o2', 'w11f-owner2', 'w11f-grantee', 'active', '2999-01-01T00:00:00Z', 'w11f-owner2', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)

	ctx := context.Background()
	// 管理员全量。
	summaries, err := store.Options(ctx, AccessScope{ViewerID: "w11f-admin", IsAdmin: true}, OptionsQuery{})
	if err != nil || len(summaries) != 2 {
		t.Fatalf("admin options = %v/%v", summaries, err)
	}
	// 管理员带过滤。
	summaries, err = store.Options(ctx, AccessScope{ViewerID: "w11f-admin", IsAdmin: true, FilterID: "w11f-owner"}, OptionsQuery{})
	if err != nil || len(summaries) != 1 || summaries[0].ID != "w11f-o1" {
		t.Fatalf("filtered options = %v/%v", summaries, err)
	}
	// manageableOnly 缺上下文。
	if _, err := store.Options(ctx, AccessScope{}, OptionsQuery{ManageableOnly: true}); err == nil {
		t.Fatal("manageableOnly empty viewer must fail")
	}
	// manageableOnly 命中。
	summaries, err = store.Options(ctx, AccessScope{ViewerID: "w11f-owner"}, OptionsQuery{ManageableOnly: true})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("manageableOnly = %v/%v", summaries, err)
	}
	// 默认两臂（owner UNION authorized）+ preferDefault 排序输入。
	summaries, err = store.Options(ctx, AccessScope{ViewerID: "w11f-grantee"}, OptionsQuery{PreferDefault: true})
	if err != nil || len(summaries) != 1 || summaries[0].AccessType != "authorized" {
		t.Fatalf("grantee options = %v/%v", summaries, err)
	}
	if summaries[0].IsDefault {
		t.Fatal("authorized row must force isDefault=false")
	}
	// ids / keyword / providerCode 过滤。
	summaries, err = store.Options(ctx, AccessScope{ViewerID: "w11f-owner"}, OptionsQuery{IDs: []string{"w11f-o1"}, Keyword: "w11f-选项", ProviderCode: "openai"})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("filtered manageable = %v/%v", summaries, err)
	}
	// 坏 group_type 行 → 读错误。
	env.exec(t, `UPDATE groups SET group_type = 'corrupt' WHERE id = 'w11f-o1'`)
	if _, err := store.Options(ctx, AccessScope{ViewerID: "w11f-owner"}, OptionsQuery{}); err == nil {
		t.Fatal("corrupt type options must fail")
	}
	env.exec(t, `UPDATE groups SET group_type = 'personal' WHERE id = 'w11f-o1'`)
	// 查询错误 / Scan 错误 / rows.Err / 名单查询错误。
	env.script.failQuery("FROM groups")
	if _, err := store.Options(ctx, AccessScope{ViewerID: "w11f-owner"}, OptionsQuery{}); err == nil {
		t.Fatal("options query fault must fail")
	}
	env.script.failQuery("SELECT id, display_name FROM")
	if _, err := store.Options(ctx, AccessScope{ViewerID: "w11f-owner"}, OptionsQuery{}); err == nil {
		t.Fatal("options names fault must fail")
	}

	// RouteStrategyOptions: 管理员 / 授权视角 / 缺上下文 / 错误。
	options, err := store.RouteStrategyOptions(ctx, AccessScope{ViewerID: "w11f-admin", IsAdmin: true}, OptionsQuery{})
	if err != nil || len(options) != 2 {
		t.Fatalf("route strategy admin = %v/%v", options, err)
	}
	options, err = store.RouteStrategyOptions(ctx, AccessScope{ViewerID: "w11f-grantee"}, OptionsQuery{})
	if err != nil || len(options) != 1 || !options[0].Enabled {
		t.Fatalf("route strategy grantee = %v/%v", options, err)
	}
	if _, err := store.RouteStrategyOptions(ctx, AccessScope{}, OptionsQuery{}); err == nil {
		t.Fatal("route strategy empty viewer must fail")
	}
	env.script.failQuery("FROM groups")
	if _, err := store.RouteStrategyOptions(ctx, AccessScope{ViewerID: "w11f-owner"}, OptionsQuery{}); err == nil {
		t.Fatal("route strategy query fault must fail")
	}

	// EditDetail: 授权覆盖视角（settings 覆盖 enabled）。
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, created_at, updated_at)
		VALUES ('w11f-oa1', 'w11f-grantee', 'w11f-o2', 0, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	edit, err := store.EditDetail(ctx, "w11f-o2", AccessScope{ViewerID: "w11f-grantee"})
	if err != nil || edit == nil || edit.Enabled {
		t.Fatalf("edit detail override = %+v/%v", edit, err)
	}
	if _, err := store.EditDetail(ctx, "w11f-o2", AccessScope{}); err == nil || !strings.Contains(err.Error(), "缺少系统账户上下文") {
		t.Fatalf("edit detail empty viewer = %v", err)
	}
}

func TestW11FOptionHandlers(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11foptions", "w11f-pass", "super_admin")
	env.w11fSeedGroup(t, "w11f-h1", "w11f-howner", "w11f-处理器组", "openai", 1, 0, "personal")

	// purpose 非法 / 重复参数。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=bogus", "")
	if code != http.StatusBadRequest || payload["message"] != "分组选项 purpose 仅支持 select 或 account" {
		t.Fatalf("purpose invalid = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?ids=a&ids=b", "")
	if code != http.StatusBadRequest {
		t.Fatalf("repeated params = %d %v", code, payload)
	}
	// purpose=select 投影。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=select&ids=w11f-h1", "")
	if code != http.StatusOK {
		t.Fatalf("select purpose = %d %v", code, payload)
	}
	if items := payload["data"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != "w11f-h1" {
		t.Fatalf("select projection = %v", payload["data"])
	}
	// purpose=account 全量。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=account", "")
	if code != http.StatusOK || len(payload["data"].([]any)) < 1 {
		t.Fatalf("account purpose = %d %v", code, payload)
	}
	// authorization-options / account-options / route-strategy-options / edit-basic。
	for _, path := range []string{
		"/__aisys__/api/groups/authorization-options",
		"/__aisys__/api/groups/account-options",
		"/__aisys__/api/groups/route-strategy-options",
	} {
		code, payload = env.do(t, http.MethodGet, path+"?manageableOnly=1&preferDefault=true&limit=10", "")
		if code != http.StatusOK {
			t.Fatalf("%s = %d %v", path, code, payload)
		}
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/w11f-h1/edit-basic", "")
	if code != http.StatusOK || payload["data"].(map[string]any)["name"] != "w11f-处理器组" {
		t.Fatalf("edit-basic = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/w11f-missing/edit-basic", "")
	if code != http.StatusNotFound || payload["message"] != "分组不存在" {
		t.Fatalf("edit-basic missing = %d %v", code, payload)
	}
	// 重复参数（无 purpose 路由）。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/account-options?keyword=a&keyword=b", "")
	if code != http.StatusBadRequest || payload["message"] != "分组参数无效" {
		t.Fatalf("repeated params no purpose = %d %v", code, payload)
	}
	// 存储层错误 → 500；坏类型 → 400。
	env.script.failQuery("FROM groups")
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("options store fault = %d %v", code, payload)
	}
	env.exec(t, `UPDATE groups SET group_type = 'corrupt' WHERE id = 'w11f-h1'`)
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options", "")
	if code != http.StatusInternalServerError {
		t.Fatalf("options corrupt type = %d %v", code, payload)
	}
	env.exec(t, `UPDATE groups SET group_type = 'personal' WHERE id = 'w11f-h1'`)
}

func TestW11FRouteHelpersAndPatch(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11froute", "w11f-pass", "super_admin")
	revision := env.w11fSeedGroup(t, "w11f-rh1", "w11f-rhowner", "w11f-路由辅助", "openai", 1, 0, "personal")

	// patch 处理器: 坏 JSON / 坏版本 / 未知字段 / 无变更字段。
	code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{bad`)
	if code != http.StatusBadRequest {
		t.Fatalf("patch malformed = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{"expectedUpdatedAt":"zzz","name":"n"}`)
	if code != http.StatusBadRequest || payload["message"] != "分组参数无效" {
		t.Fatalf("patch bad version = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{"expectedUpdatedAt":"`+revision+`","extra":1}`)
	if code != http.StatusBadRequest || payload["message"] != "分组参数无效" {
		t.Fatalf("patch unknown field = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{"expectedUpdatedAt":"`+revision+`"}`)
	if code != http.StatusBadRequest || payload["message"] != "请提供要修改的分组内容" {
		t.Fatalf("patch no change = %d %v", code, payload)
	}
	// patch 命中 + 冲突（旧版本重放）。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{"expectedUpdatedAt":"`+revision+`","name":"w11f-路由改名"}`)
	if code != http.StatusOK {
		t.Fatalf("patch hit = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{"expectedUpdatedAt":"`+revision+`","name":"w11f-路由改名2"}`)
	if code != http.StatusConflict || payload["message"] != "分组已被其他操作更新，请刷新后重试" {
		t.Fatalf("patch conflict = %d %v", code, payload)
	}
	// patch 存储层错误 → 500。
	env.script.failQuery("SELECT g.id")
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/groups/w11f-rh1", `{"expectedUpdatedAt":"`+revision+`","name":"x"}`)
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("patch store fault = %d %v", code, payload)
	}
	// remove 处理器: 命中 / 不存在 / 存储错误。
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/groups/w11f-missing", "")
	if code != http.StatusNotFound || payload["message"] != "分组不存在" {
		t.Fatalf("remove missing = %d %v", code, payload)
	}
	env.script.failQuery("SELECT g.system_account_id, g.name, g.is_default")
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/groups/w11f-rh1", "")
	if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
		t.Fatalf("remove store fault = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/groups/w11f-rh1", "")
	if code != http.StatusNoContent {
		t.Fatalf("remove hit = %d %v", code, payload)
	}
	// create 处理器: 坏 JSON / 未知字段 / 命中。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups", `{bad`)
	if code != http.StatusBadRequest {
		t.Fatalf("create malformed = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"name":"w11f-创建","providerCode":"openai","extra":1}`)
	if code != http.StatusBadRequest || payload["message"] != "分组参数无效" {
		t.Fatalf("create unknown field = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"name":"w11f-创建","providerCode":"ghost"}`)
	if code != http.StatusBadRequest || payload["message"] != "不支持的供应商：ghost" {
		t.Fatalf("create ghost provider = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"name":"w11f-创建成功","providerCode":"openai","enabled":true,"groupType":"high_concurrency","schedulingPolicy":{"maxQueueWaitMs":60000},"description":"d"}`)
	if code != http.StatusCreated {
		t.Fatalf("create hit = %d %v", code, payload)
	}

	// 纯辅助: actorResolver / operationMode / isRFC3339Instant / patchFieldLabel。
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if actorResolver(request) != "anonymous" {
		t.Fatal("actorResolver anonymous drift")
	}
	withAuth := w11fReqAuth(http.MethodGet, "/", "", "w11f-acc", "user")
	if actorResolver(withAuth) != "w11f-acc" {
		t.Fatal("actorResolver auth drift")
	}
	if operationMode(AccessScope{IsAdmin: true}) != "admin" || operationMode(AccessScope{}) != "self" {
		t.Fatal("operationMode drift")
	}
	if isRFC3339Instant("2024-01-01T00:00:00Z") != true || isRFC3339Instant("zzz") {
		t.Fatal("isRFC3339Instant drift")
	}
	labels := map[string]string{"name": "名称", "providerCode": "供应商", "description": "说明", "groupType": "分组类型", "schedulingPolicy": "调度策略", "enabled": "启用状态", "unknown": "unknown"}
	for field, want := range labels {
		if got := patchFieldLabel(field); got != want {
			t.Fatalf("patchFieldLabel(%s) = %s", field, got)
		}
	}
	// summarizeAffectedRouteStrategies: 空名回退 / 停用绑定 / 超过 3 个。
	changes := []RouteStrategyChange{
		{RouteStrategyName: "s1", RemovedGroupID: "g1", RemovedBindingStatus: w11fStringPtr("disabled")},
		{RouteStrategyName: "s2", RemovedGroupName: "w11f-n2"},
		{RouteStrategyName: "s3", RemovedGroupName: "w11f-n3"},
		{RouteStrategyName: "s4", RemovedGroupName: "w11f-n4"},
	}
	summary := summarizeAffectedRouteStrategies(changes)
	if !strings.Contains(summary, "移除停用分组 g1") || !strings.Contains(summary, "另有 1 个策略路由受影响") {
		t.Fatalf("summary = %s", summary)
	}
	// queryTextList / optionLimitValue / booleanQueryValue / groupOptionPurpose。
	if got := queryTextList([]string{" a , b ,a ,"}, 50); len(got) != 2 || got[0] != "a" {
		t.Fatalf("queryTextList = %v", got)
	}
	if got := queryTextList([]string{"a,a,b,b,c,c"}, 2); len(got) != 2 {
		t.Fatalf("queryTextList cap = %v", got)
	}
	if optionLimitValue("") != 50 || optionLimitValue("x") != 50 || optionLimitValue("0") != 1 || optionLimitValue("99") != 50 || optionLimitValue("10") != 10 {
		t.Fatal("optionLimitValue drift")
	}
	if booleanQueryValue("TRUE") != true || booleanQueryValue("Yes") != true || booleanQueryValue("1") != true || booleanQueryValue("0") {
		t.Fatal("booleanQueryValue drift")
	}
	if purpose, ok := groupOptionPurpose(""); !ok || purpose != "select" {
		t.Fatal("groupOptionPurpose empty drift")
	}
	if purpose, ok := groupOptionPurpose("account"); !ok || purpose != "account" {
		t.Fatal("groupOptionPurpose account drift")
	}
	if _, ok := groupOptionPurpose("bogus"); ok {
		t.Fatal("groupOptionPurpose bogus drift")
	}
	// bodyString。
	if _, ok := bodyString(map[string]any{}, "k"); ok {
		t.Fatal("bodyString missing drift")
	}
	if _, ok := bodyString(map[string]any{"k": 1}, "k"); ok {
		t.Fatal("bodyString non-string drift")
	}
	if text, ok := bodyString(map[string]any{"k": "v"}, "k"); !ok || text != "v" {
		t.Fatal("bodyString ok drift")
	}
}

// TestW11FCreatePatchBody 直接覆盖 createBody / patchBody 的逐字段分支。
func TestW11FCreatePatchBody(t *testing.T) {
	valid := map[string]any{"name": "n", "providerCode": "openai"}
	if input, ok := createBody(valid); !ok || input.Name == nil || input.ProviderCode == nil {
		t.Fatalf("createBody valid = %+v", input)
	}
	invalid := []map[string]any{
		{"name": 1, "providerCode": "openai"},
		{"name": " ", "providerCode": "openai"},
		{"providerCode": " "},
		{"name": "n", "providerCode": "openai", "description": 2},
		{"name": "n", "providerCode": "openai", "enabled": "yes"},
		{"name": "n", "providerCode": "openai", "groupType": "bogus"},
		{"name": "n", "providerCode": "openai", "groupType": 3},
		{"name": "n", "providerCode": "openai", "schedulingPolicy": "x"},
		{"name": "n", "providerCode": "openai", "schedulingPolicy": map[string]any{"mode": "balanced_fast"}},
		{"name": "n", "providerCode": "openai", "schedulingPolicy": map[string]any{"maxQueueSize": 0}},
	}
	for _, body := range invalid {
		if _, ok := createBody(body); ok {
			t.Fatalf("createBody(%v) must fail", body)
		}
		if input := patchBody(body); !input.Empty() {
			t.Fatalf("patchBody(%v) must be empty", body)
		}
	}
	// patchBody 允许只带部分字段。
	nameOnly := map[string]any{"name": "n2"}
	if input := patchBody(nameOnly); input.Empty() || *input.Name != "n2" {
		t.Fatalf("patchBody name only = %+v", input)
	}
	// 高并发合法输入。
	hc := map[string]any{"name": "n", "providerCode": "openai", "groupType": "high_concurrency", "schedulingPolicy": map[string]any{"maxQueueWaitMs": float64(60000)}}
	if _, ok := createBody(hc); !ok {
		t.Fatal("createBody high concurrency must pass")
	}
	if input := patchBody(hc); input.Empty() {
		t.Fatal("patchBody high concurrency must pass")
	}
	// enabled=false。
	withEnabled := map[string]any{"name": "n", "providerCode": "openai", "enabled": false}
	input, ok := createBody(withEnabled)
	if !ok || input.Enabled == nil || *input.Enabled {
		t.Fatalf("createBody enabled = %+v", input)
	}
	// description 空串允许。
	withDescription := map[string]any{"name": "n", "providerCode": "openai", "description": ""}
	input, ok = createBody(withDescription)
	if !ok || input.Description == nil {
		t.Fatalf("createBody description = %+v", input)
	}
}

// TestW11FReturnAuthorizationHandler 通过挂载路由覆盖归还授权分支。
func TestW11FReturnAuthorizationHandler(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11freturn", "w11f-pass", "super_admin")

	// scopeQueryOK 空 → 400。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/groups/w11f-x1/return-authorization?systemAccountId=", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("scope query = %d %v", code, payload)
	}
	// 无授权行 → 404。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups/w11f-x2/return-authorization?systemAccountId=w11f-grantee", `{}`)
	if code != http.StatusNotFound || payload["message"] != "授权分组不存在或不可归还" {
		t.Fatalf("missing authorization = %d %v", code, payload)
	}
	// 仓储错误 → 500。
	env.script.failQueryAlways("FROM resource_authorizations")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups/w11f-x3/return-authorization?systemAccountId=w11f-grantee", `{}`)
	if code != http.StatusInternalServerError || payload["message"] != "归还授权分组失败" {
		t.Fatalf("store fault = %d %v", code, payload)
	}

	// Authz 为 nil → 500（独立 kernel + 登录态）。
	nilK := kernel.New(kernel.Options{CompressionDisabled: true})
	env.deps.MountAuth(nilK, "lax", false)
	(&Deps{Store: env.store, Auth: env.deps}).Mount(nilK)
	nilServer := httptest.NewServer(nilK.Handler())
	t.Cleanup(nilServer.Close)
	saved := env.server
	env.server = nilServer
	env.login(t, "w11freturn2", "w11f-pass", "super_admin")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/groups/w11f-x4/return-authorization?systemAccountId=w11f-grantee", `{}`)
	if code != http.StatusInternalServerError || payload["message"] != "归还授权分组失败" {
		t.Fatalf("nil authz = %d %v", code, payload)
	}
	env.server = saved
}

// TestW11FGroupAccountIDs 覆盖 groupAccountIDsByGroupIDs/groupAccountIDs 分支。
func TestW11FGroupAccountIDs(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	ctx := context.Background()
	env.w11fSeedGroup(t, "w11f-ga", "w11f-owner", "w11f-账户组", "openai", 1, 0, "personal")

	// 空 ID → 空结果。
	if got, err := store.groupAccountIDsByGroupIDs(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty ids = %v/%v", got, err)
	}
	if got, err := store.groupAccountIDs(ctx, "w11f-missing"); err != nil || len(got) != 0 {
		t.Fatalf("missing group = %v/%v", got, err)
	}
	// 命中: 自有账户 + 授权账户 + 已删账户排除。
	env.exec(t, `INSERT INTO accounts (id, system_account_id, created_at) VALUES ('w11f-acc-a', 'w11f-owner', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, created_at) VALUES ('w11f-acc-x', 'w11f-other', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, deleted_at, created_at) VALUES ('w11f-acc-deleted', 'w11f-owner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('w11f-ga-ra', 'group', 'w11f-any', 'w11f-other', 'w11f-owner', 'active', 'w11f-owner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at) VALUES
		('w11f-owner', 'w11f-ga', 'w11f-acc-a', NULL, 1, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z'),
		('w11f-owner', 'w11f-ga', 'w11f-acc-x', 'w11f-ga-ra', 1, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z'),
		('w11f-owner', 'w11f-ga', 'w11f-acc-deleted', NULL, 1, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	ids, err := store.groupAccountIDs(ctx, "w11f-ga")
	if err != nil || len(ids) != 2 {
		t.Fatalf("groupAccountIDs = %v/%v", ids, err)
	}
	// 查询错误 / Scan 错误 / rows.Err。
	env.script.failQuery("FROM group_accounts")
	if _, err := store.groupAccountIDsByGroupIDs(ctx, []string{"w11f-ga"}); err == nil {
		t.Fatal("groupAccountIDs query fault must fail")
	}
	env.script.canned("FROM group_accounts", []string{"group_id", "account_id"}, [][]driver.Value{{nil, "a"}}, nil)
	if _, err := store.groupAccountIDsByGroupIDs(ctx, []string{"w11f-ga"}); err == nil {
		t.Fatal("groupAccountIDs scan fault must fail")
	}
	env.script.canned("FROM group_accounts", []string{"group_id", "account_id"}, [][]driver.Value{{"g", "a"}}, w11fBoom)
	if _, err := store.groupAccountIDsByGroupIDs(ctx, []string{"w11f-ga"}); err == nil {
		t.Fatal("groupAccountIDs rows.Err fault must fail")
	}
	// systemAccountNames: Scan 错误 / rows.Err / 空。
	env.script.canned("SELECT id, display_name FROM", []string{"id", "display_name"}, [][]driver.Value{{nil, "n"}}, nil)
	if _, err := store.systemAccountNames(ctx, []string{"w11f-acc-a"}); err == nil {
		t.Fatal("systemAccountNames scan fault must fail")
	}
	env.script.canned("SELECT id, display_name FROM", []string{"id", "display_name"}, [][]driver.Value{{"i", "n"}}, w11fBoom)
	if _, err := store.systemAccountNames(ctx, []string{"w11f-acc-a"}); err == nil {
		t.Fatal("systemAccountNames rows.Err fault must fail")
	}
	if names, err := store.systemAccountNames(ctx, nil); err != nil || len(names) != 0 {
		t.Fatalf("systemAccountNames empty = %v/%v", names, err)
	}
}

var _ = time.Now
