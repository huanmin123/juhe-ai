package groups

// w11f 覆盖波次（文件 3/3）：补 ListPage/FindDetail 授权视图/Patch 策略臂/
// remove 元数据等剩余分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// w11fStatsReader 注入式统计读取器。
type w11fStatsReader struct {
	stats map[string]AccountStats
}

func (r w11fStatsReader) ReadGroupAccountStats(_ context.Context, groupIDs []string) (map[string]AccountStats, error) {
	out := map[string]AccountStats{}
	for _, id := range groupIDs {
		if stats, ok := r.stats[id]; ok {
			out[id] = stats
		}
	}
	return out, nil
}

func TestW11FListPageArms(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	ctx := context.Background()
	env.w11fSeedGroup(t, "w11f-l1", "w11f-lowner", "w11f-列表A", "openai", 1, 0, "personal")
	env.w11fSeedGroup(t, "w11f-l2", "w11f-lowner", "w11f-列表B", "anthropic", 1, 0, "personal")
	env.w11fSeedGroup(t, "w11f-l3", "w11f-lother", "w11f-授权列表", "openai", 1, 0, "personal")
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('w11f-la', 'group', 'w11f-l3', 'w11f-lother', 'w11f-lowner', 'active', 'w11f-lother', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)

	// 管理员 keyword 过滤。
	result, err := store.ListPage(ctx, AccessScope{ViewerID: "w11f-admin", IsAdmin: true}, 1, 50, "w11f-列表")
	if err != nil || len(result.Items) != 2 {
		t.Fatalf("admin keyword = %v/%v", result, err)
	}
	// 普通用户两臂（owner UNION authorized）。
	result, err = store.ListPage(ctx, AccessScope{ViewerID: "w11f-lowner"}, 1, 50, "")
	if err != nil || len(result.Items) != 3 {
		t.Fatalf("viewer two arms = %v/%v", result, err)
	}
	// 空 viewer → 空结果。
	result, err = store.ListPage(ctx, AccessScope{}, 1, 50, "")
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("empty viewer = %v/%v", result, err)
	}
	// 分页截断: pageSize=2 → hasMore。
	result, err = store.ListPage(ctx, AccessScope{ViewerID: "w11f-lowner"}, 1, 2, "")
	if err != nil || !result.HasMore || len(result.Items) != 2 || result.Total != 3 {
		t.Fatalf("paging = %+v/%v", result, err)
	}
	// 查询错误 / Scan 错误 / rows.Err。
	env.script.failQuery("ORDER BY g.updated_at DESC")
	if _, err := store.ListPage(ctx, AccessScope{ViewerID: "w11f-admin", IsAdmin: true}, 1, 50, ""); err == nil {
		t.Fatal("ListPage query fault must fail")
	}
	env.script.failQuery("SELECT id, system_account_id, name, provider_code")
	if _, err := store.ListPage(ctx, AccessScope{ViewerID: "w11f-lowner"}, 1, 50, ""); err == nil {
		t.Fatal("ListPage union fault must fail")
	}
	// names 查询错误。
	env.script.failQuery("SELECT id, display_name FROM")
	if _, err := store.ListPage(ctx, AccessScope{ViewerID: "w11f-lowner"}, 1, 50, ""); err == nil {
		t.Fatal("ListPage names fault must fail")
	}
}

func TestW11FFindDetailAuthorizedExtras(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	ctx := context.Background()
	// 授权分组 + 多来源（manual active / manual ended / team active）。
	env.w11fSeedGroup(t, "w11f-s1", "w11f-sowner", "w11f-来源组", "openai", 1, 0, "personal")
	env.exec(t, `INSERT INTO system_teams (id, name) VALUES ('w11f-team1', 'w11f-团队一')`)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('w11f-sa', 'group', 'w11f-s1', 'w11f-sowner', 'w11f-sgrantee', 'active', 'w11f-sowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO resource_authorization_sources (id, authorization_id, source_type, source_team_id, status, activated_at, ended_at, ended_reason, created_by, created_at, updated_at) VALUES
		('w11f-src1', 'w11f-sa', 'manual', NULL, 'active', '2024-01-01T00:00:00.000Z', NULL, NULL, 'w11f-sowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z'),
		('w11f-src2', 'w11f-sa', 'manual', NULL, 'ended', '2024-01-01T00:00:00.000Z', '2024-01-02T00:00:00.000Z', 'revoked', 'w11f-sowner', '2024-01-01T00:00:00.000Z', '2024-01-02T00:00:00.000Z'),
		('w11f-src3', 'w11f-sa', 'team', 'w11f-team1', 'active', '2024-01-01T00:00:00.000Z', NULL, NULL, 'w11f-sowner', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)

	detail, err := store.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sgrantee"})
	if err != nil || detail == nil {
		t.Fatalf("authorized detail = %+v/%v", detail, err)
	}
	if len(detail.AuthorizationSources) != 3 {
		t.Fatalf("sources = %+v", detail.AuthorizationSources)
	}
	if !detail.Permissions.CanReturnAuthorization {
		t.Fatalf("manual source must allow return: %+v", detail.Permissions)
	}
	// 列表授权臂携带来源摘要（manual + team 均存在）。
	listPage, err := store.ListPage(ctx, AccessScope{ViewerID: "w11f-sgrantee"}, 1, 50, "")
	if err != nil || len(listPage.Items) != 1 {
		t.Fatalf("authorized list = %+v/%v", listPage, err)
	}
	summary := listPage.Items[0].AuthorizationSourceSummary
	if summary == nil || !summary.HasManual || !summary.HasTeam || summary.ActiveSourceCount != 2 {
		t.Fatalf("source summary = %+v", summary)
	}

	// 授权行过期/状态非 active 的 canBind=false 视图。
	env.exec(t, `UPDATE resource_authorizations SET status = 'paused' WHERE id = 'w11f-sa'`)
	detail, err = store.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sgrantee"})
	if err != nil || detail == nil || detail.Permissions.CanBindToApiKey {
		t.Fatalf("paused detail = %+v/%v", detail, err)
	}
	// 坏 expiresAt → 读错误。
	env.exec(t, `UPDATE resource_authorizations SET status = 'active', expires_at = 'not-a-time' WHERE id = 'w11f-sa'`)
	if _, err := store.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sgrantee"}); err == nil {
		t.Fatal("malformed expiresAt must fail")
	}
	env.exec(t, `UPDATE resource_authorizations SET expires_at = NULL WHERE id = 'w11f-sa'`)

	// 授权来源查询错误 / Scan 错误 / rows.Err。
	env.script.failQuery("FROM resource_authorization_sources ras")
	if _, err := store.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sgrantee"}); err == nil {
		t.Fatal("sources fault must fail")
	}
	sourceCols := []string{"id", "authorization_id", "source_type", "source_team_id", "team_name", "status", "activated_at", "ended_at", "ended_reason", "created_by", "created_at", "revoked_by", "revoked_at", "updated_at"}
	badRow := [][]driver.Value{{nil, "a", "manual", nil, nil, "active", nil, nil, nil, nil, nil, nil, nil, nil}}
	env.script.canned("FROM resource_authorization_sources ras", sourceCols, badRow, nil)
	if _, err := store.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sgrantee"}); err == nil {
		t.Fatal("sources scan fault must fail")
	}
	goodRow := [][]driver.Value{{"s", "a", "manual", nil, nil, "active", nil, nil, nil, nil, nil, nil, nil, nil}}
	env.script.canned("FROM resource_authorization_sources ras", sourceCols, goodRow, w11fBoom)
	if _, err := store.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sgrantee"}); err == nil {
		t.Fatal("sources rows.Err fault must fail")
	}

	// 注入 stats reader 的 owner 视图。
	statsStore, err := NewStore(env.db, false, nil, nil, nil, WithStatsReader(w11fStatsReader{stats: map[string]AccountStats{
		"w11f-s1": {Total: 9, Available: 8, Active: 7, Disabled: 1, Error: 0, RateLimited: 0, CurrentConcurrency: 2, ConcurrencyLimit: 5},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	detail, err = statsStore.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sowner"})
	if err != nil || detail == nil || detail.AccountStats.Available != 8 || detail.AccountStats.ConcurrencyLimit != 5 {
		t.Fatalf("stats detail = %+v/%v", detail, err)
	}
	// stats 未命中时保持 accountIDs 长度。
	missStore, err := NewStore(env.db, false, nil, nil, nil, WithStatsReader(w11fStatsReader{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missStore.FindDetail(ctx, "w11f-s1", AccessScope{ViewerID: "w11f-sowner"}); err != nil {
		t.Fatal(err)
	}
}

func TestW11FPatchPolicyArms(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store
	owner := AccessScope{ViewerID: "w11f-pa"}
	revision := env.w11fSeedGroup(t, "w11f-pa1", "w11f-pa", "w11f-策略组", "openai", 1, 0, "personal")

	// 供应商切换成功（无账户）。
	anthropic := "anthropic"
	result, err := store.Patch(context.Background(), "w11f-pa1", MutationInput{ProviderCode: &anthropic}, revision, owner)
	if err != nil || result.Name == "" {
		t.Fatalf("provider change = %+v/%v", result, err)
	}
	// ownedGroupHasAccounts 查询错误（provider 变更路径内）。
	env.exec(t, `INSERT INTO accounts (id, system_account_id, created_at) VALUES ('w11f-pacc', 'w11f-pa', '2024-01-01T00:00:00.000Z')`)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ('w11f-pa', 'w11f-pa1', 'w11f-pacc', 1, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	current := env.queryString(t, `SELECT updated_at FROM groups WHERE id = 'w11f-pa1'`)
	openai := "openai"
	env.script.failQuery("SELECT 1 FROM group_accounts")
	if _, err := store.Patch(context.Background(), "w11f-pa1", MutationInput{ProviderCode: &openai}, current, owner); err == nil {
		t.Fatal("hasAccounts query fault must fail")
	}
	env.exec(t, `DELETE FROM group_accounts WHERE group_id = 'w11f-pa1'`)
	env.exec(t, `DELETE FROM accounts WHERE id = 'w11f-pacc'`)

	// 类型转换 personal → high_concurrency（带策略重置 + diff）。
	current = env.queryString(t, `SELECT updated_at FROM groups WHERE id = 'w11f-pa1'`)
	high := GroupTypeHighConcurrency
	result, err = store.Patch(context.Background(), "w11f-pa1", MutationInput{GroupType: &high}, current, owner)
	if err != nil {
		t.Fatal(err)
	}
	// 高并发分组: 相同策略 no-op 与不同策略更新。
	current = env.queryString(t, `SELECT updated_at FROM groups WHERE id = 'w11f-pa1'`)
	samePolicy := map[string]any{}
	result, err = store.Patch(context.Background(), "w11f-pa1", MutationInput{SchedulingPolicy: samePolicy}, current, owner)
	if err != nil || len(result.ChangedFields) != 0 {
		t.Fatalf("policy noop = %+v/%v", result, err)
	}
	changedPolicy := map[string]any{"maxQueueWaitMs": float64(90000)}
	result, err = store.Patch(context.Background(), "w11f-pa1", MutationInput{SchedulingPolicy: changedPolicy}, current, owner)
	if err != nil || len(result.ChangedFields) == 0 {
		t.Fatalf("policy change = %+v/%v", result, err)
	}
	// 高并发 → personal 转换（策略清空）。
	current = env.queryString(t, `SELECT updated_at FROM groups WHERE id = 'w11f-pa1'`)
	personal := GroupTypePersonal
	if _, err := store.Patch(context.Background(), "w11f-pa1", MutationInput{GroupType: &personal}, current, owner); err != nil {
		t.Fatal(err)
	}
	// 转换时坏策略输入 → 校验错误。
	env.exec(t, `UPDATE groups SET group_type = 'high_concurrency', scheduling_policy_json = '{bad' WHERE id = 'w11f-pa1'`)
	env.exec(t, `UPDATE groups SET updated_at = ? WHERE id = 'w11f-pa1'`, revision)
	if _, err := store.Patch(context.Background(), "w11f-pa1", MutationInput{GroupType: &personal, SchedulingPolicy: map[string]any{}}, revision, owner); err == nil {
		t.Fatal("corrupt stored policy on transition must fail")
	}
}

func TestW11FRemoveMetadataAndHelpers(t *testing.T) {
	env := newW11FEnv(t)
	env.login(t, "w11frmeta", "w11f-pass", "super_admin")
	revision := env.w11fSeedGroup(t, "w11f-mg", "w11f-mgowner", "w11f-元数据组", "openai", 1, 0, "personal")
	_ = revision

	// 绑定一个策略路由后删除 → sink 记录受影响策略。
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name) VALUES ('w11f-mrs', 'w11f-mgowner', 'w11f-元策略')`)
	env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
		VALUES ('w11f-mrsg', 'w11f-mrs', 'w11f-mgowner', 'w11f-mg', 'disabled', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	// 再绑定第二个启用分组，避免唯一可用分组拦截。
	env.w11fSeedGroup(t, "w11f-mg2", "w11f-mgowner", "w11f-元数据组2", "openai", 1, 0, "personal")
	env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
		VALUES ('w11f-mrsg2', 'w11f-mrs', 'w11f-mgowner', 'w11f-mg2', 'active', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)

	code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/groups/w11f-mg", "")
	if code != http.StatusNoContent {
		t.Fatalf("remove with strategies = %d %v", code, payload)
	}
	entries := env.sink.snapshotAll()
	found := false
	for _, entry := range entries {
		if entry.Action == "delete" {
			found = true
			if len(entry.Changes) < 2 {
				t.Fatalf("delete changes = %+v", entry.Changes)
			}
		}
	}
	if !found {
		t.Fatal("delete entry missing")
	}

	// adminScope "all" 过滤分支。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups?systemAccountId=all", "")
	if code != http.StatusOK {
		t.Fatalf("admin list all = %d %v", code, payload)
	}

	// writeOptionsError 直连: 校验错误 → 400; 其他 → 500。
	recorder := httptest.NewRecorder()
	(&Deps{}).writeOptionsError(recorder, &ValidationError{Message: "校验错误"})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "校验错误") {
		t.Fatalf("writeOptionsError validation = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	(&Deps{}).writeOptionsError(recorder, w11fBoom)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("writeOptionsError generic = %d", recorder.Code)
	}

	// canBindAuthorizedGroupRow: 禁用行与停用授权。
	store := env.store
	disabledRow := optionRow{enabled: 0}
	if bound, err := store.canBindAuthorizedGroupRow(disabledRow); err != nil || bound {
		t.Fatalf("disabled row = %v/%v", bound, err)
	}
	pausedRow := optionRow{enabled: 1, authorizationStatus: w11fNullString("paused")}
	if bound, err := store.canBindAuthorizedGroupRow(pausedRow); err != nil || bound {
		t.Fatalf("paused row = %v/%v", bound, err)
	}
	activeFuture := optionRow{enabled: 1, authorizationStatus: w11fNullString("active"), authorizationExpiresAt: w11fNullString("2999-01-01T00:00:00Z")}
	if bound, err := store.canBindAuthorizedGroupRow(activeFuture); err != nil || !bound {
		t.Fatalf("active future row = %v/%v", bound, err)
	}
	_ = kernel.WriteOK
}

// w11fNullString 快速构造有效 NullString。
func w11fNullString(v string) (result sql.NullString) {
	result.String = v
	result.Valid = true
	return result
}

// snapshotAll 返回 sink 记录副本。
func (s *recordingSink) snapshotAll() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry(nil), s.entries...)
}
