package accounts

// w10a runtime-reset 攻坚：授权路径的 unchanged 重置结果、失败列清理布尔、
// 授权不可用消息矩阵、effective availability 全分支、授权额度门（含端口
// 失败臂）与 explicit_policy_cooldown 跳过链。纯函数直测 + HTTP 流程各一条。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"
)

// ---- 纯函数直测 ----

func TestW10AClearAuthorizedFailureStateColumns(t *testing.T) {
	empty := authorizedDispatchResetRow{}
	if clearAuthorizedFailureStateColumnsForReset(map[string]any{}, empty) {
		t.Fatal("空行不应报告变更")
	}
	full := authorizedDispatchResetRow{
		CooldownUntil:              sqlNullStr("2030-01-01T00:00:00Z"),
		LastErrorCode:              sqlNullStr("rate_limited"),
		LastErrorMessage:           sqlNullStr("boom"),
		LastErrorTraceID:           sqlNullStr("trace-1"),
		CooldownRetestFailureCount: sqlInt64Lit(2),
	}
	sets := map[string]any{}
	if !clearAuthorizedFailureStateColumnsForReset(sets, full) {
		t.Fatal("有失败列时应报告变更")
	}
	for _, key := range []string{"cooldown_until", "last_error_code", "last_error_message",
		"last_error_trace_id", "cooldown_retest_failure_count"} {
		if _, ok := sets[key]; !ok {
			t.Fatalf("缺失清理键 %s：%v", key, sets)
		}
	}
	// 全零/全空行（除 stream 计数外）也覆盖 0 计数不写分支。
	zeroCounts := authorizedDispatchResetRow{
		CooldownRetestFailureCount: sqlInt64Lit(0),
		StreamFailureCount:         sqlInt64Lit(0),
		LastErrorCode:              sqlNullStr("x"),
	}
	sets2 := map[string]any{}
	if !clearAuthorizedFailureStateColumnsForReset(sets2, zeroCounts) {
		t.Fatal("last_error_code 应报告变更")
	}
	if _, ok := sets2["cooldown_retest_failure_count"]; ok {
		t.Fatal("0 计数不应写入清理集合")
	}
}

func TestW10AUnchangedResetResults(t *testing.T) {
	row := authorizedDispatchResetRow{
		ID:              "acc-1",
		ConfigRevision:  7,
		SystemAccountID: "owner-1",
		Name:            "实例",
		Status:          "active",
		Schedulable:     1,
		LastErrorCode:   sqlNullStr("rate_limited"),
	}
	result := unchangedAuthorizedResetResult(row, authorizedDispatchResetBinding{
		GroupID:                "grp-1",
		AccountAuthorizationID: "ra-1",
	})
	if result.ConfigRevision != 7 || result.OwnerSystemAccountID != "owner-1" || len(result.ChangedFields) != 0 {
		t.Fatalf("unchanged 结果字段不一致：%+v", result)
	}
	if result.AuthorizedBinding == nil || result.AuthorizedBinding.GroupID != "grp-1" ||
		result.AuthorizedBinding.AccountAuthorizationID != "ra-1" {
		t.Fatalf("unchanged 绑定不一致：%+v", result.AuthorizedBinding)
	}
	patchRow := patchFailureStateRow{ID: "acc-2", ConfigRevision: 3, Name: "n",
		SystemAccountID: "owner-2", Status: "disabled"}
	patchResult, patchErr := (&Store{}).unchangedFailureStateResult(nil, patchRow)
	if patchErr != nil {
		t.Fatalf("unchangedFailureStateResult 意外错误：%v", patchErr)
	}
	if patchResult.ID != "acc-2" || patchResult.ConfigRevision != 3 || patchResult.Status != "disabled" {
		t.Fatalf("unchanged failure 结果不一致：%+v", patchResult)
	}
	if len(patchResult.ChangedFields) != 0 {
		t.Fatalf("unchanged 不应有变更字段：%v", patchResult.ChangedFields)
	}
	if healthCheckReasonValue(true) != "activation" || healthCheckReasonValue(false) != "" {
		t.Fatal("healthCheckReasonValue 分支不一致")
	}
}

func w10aSummary(base resetSummary, mutate func(*resetSummary)) *resetSummary {
	summary := base
	if mutate != nil {
		mutate(&summary)
	}
	return &summary
}

func TestW10AResetEffectiveAvailability(t *testing.T) {
	env := newTestEnv(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	fake := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(fake)

	ownerBase := resetSummary{AccessType: "owner", Status: "active", Schedulable: true, AccountType: "oauth"}
	cases := []struct {
		name string
		make func() *resetSummary
		want bool
	}{
		{"owner 健康活跃", func() *resetSummary { return w10aSummary(ownerBase, nil) }, true},
		{"owner 冷却已过", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.CooldownUntil = sqlNullStr("2026-09-16T00:00:00Z") })
		}, true},
		{"owner 未来冷却", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.CooldownUntil = sqlNullStr("2030-01-01T00:00:00Z") })
		}, false},
		{"owner 停用", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Status = "disabled" })
		}, false},
		{"owner 待检查", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Status = "pending_test" })
		}, false},
		{"owner 异常", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Status = "error" })
		}, false},
		{"owner 限流", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Status = "rate_limited" })
		}, false},
		{"owner 临时不可用", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Status = "temporary_unavailable" })
		}, false},
		{"owner 质量隔离", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Status = "quality_isolated" })
		}, false},
		{"owner 关闭调度", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.Schedulable = false })
		}, false},
		{"owner 套餐过期标记", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.LastErrorCode = sqlNullStr("account_expired") })
		}, false},
		{"owner 套餐到期时间已过", func() *resetSummary {
			return w10aSummary(ownerBase, func(s *resetSummary) { s.AccountExpiresAt = sqlNullStr("2020-01-01T00:00:00Z") })
		}, false},
	}
	for _, tc := range cases {
		if got := env.store.resetEffectiveAvailability(context.Background(), tc.make(), now); got != tc.want {
			t.Fatalf("%s：got %v want %v", tc.name, got, tc.want)
		}
	}

	// owner api_key 池全不可用：端口 allUnavailable=true → false。
	fake.poolAllUnavailable = true
	if env.store.resetEffectiveAvailability(context.Background(),
		w10aSummary(ownerBase, func(s *resetSummary) { s.AccountType = "api_key" }), now) {
		t.Fatal("api_key 池全不可用应返回 false")
	}
	fake.poolAllUnavailable = false

	authzBase := resetSummary{
		AccessType:                "authorized",
		Status:                    "active",
		Schedulable:               true,
		BoundGroupID:              sqlNullStr("grp-1"),
		AuthorizationID:           sqlNullStr("ra-1"),
		BoundGroupAuthorizationID: sqlNullStr("ra-1"),
		AuthorizationStatus:       sqlNullStr("active"),
		SourceAccountID:           sqlNullStr("acc-src"),
		SourceID:                  sqlNullStr("acc-src"),
		SourceStatus:              sqlNullStr("active"),
		SourceSchedulable:         sqlInt64Lit(1),
	}
	authzCases := []struct {
		name string
		make func() *resetSummary
		want bool
	}{
		{"authorized 健康绑定", func() *resetSummary { return w10aSummary(authzBase, nil) }, true},
		{"authorized 未绑定分组", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.BoundGroupID = sqlNullStr("") })
		}, false},
		{"authorized 授权行缺失", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationID = sql.NullString{} })
		}, false},
		{"authorized 绑定授权不匹配", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.BoundGroupAuthorizationID = sqlNullStr("ra-other") })
		}, false},
		{"authorized 授权已到期状态", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationStatus = sqlNullStr("expired") })
		}, false},
		{"authorized 授权已暂停", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationStatus = sqlNullStr("paused") })
		}, false},
		{"authorized 授权已撤销", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationStatus = sqlNullStr("revoked") })
		}, false},
		{"authorized 授权已归还", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationStatus = sqlNullStr("returned") })
		}, false},
		{"authorized 授权到期时间已过", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationExpiresAt = sqlNullStr("2020-01-01T00:00:00Z") })
		}, false},
		{"authorized 额度用完", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.AuthorizationQuotaExceeded = true })
		}, false},
		{"authorized 来源缺失", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceAccountID = sql.NullString{} })
		}, false},
		{"authorized 来源状态为空", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sql.NullString{} })
		}, false},
		{"authorized 来源套餐过期标记", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceLastErrorCode = sqlNullStr("account_expired") })
		}, false},
		{"authorized 来源已停用", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sqlNullStr("disabled") })
		}, false},
		{"authorized 来源待检查", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sqlNullStr("pending_test") })
		}, false},
		{"authorized 来源异常", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sqlNullStr("error") })
		}, false},
		{"authorized 来源限流", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sqlNullStr("rate_limited") })
		}, false},
		{"authorized 来源临时不可用", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sqlNullStr("temporary_unavailable") })
		}, false},
		{"authorized 来源质量隔离", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceStatus = sqlNullStr("quality_isolated") })
		}, false},
		{"authorized 来源冷却中", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceCooldownUntil = sqlNullStr("2030-01-01T00:00:00Z") })
		}, false},
		{"authorized 来源关闭调度", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.SourceSchedulable = sqlInt64Lit(0) })
		}, false},
		{"authorized 实例停用", func() *resetSummary {
			return w10aSummary(authzBase, func(s *resetSummary) { s.Status = "disabled" })
		}, false},
	}
	for _, tc := range authzCases {
		if got := env.store.resetEffectiveAvailability(context.Background(), tc.make(), now); got != tc.want {
			t.Fatalf("%s：got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestW10AAuthorizedResetUnavailableMessage(t *testing.T) {
	env := newTestEnv(t)
	fake := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(fake)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	_ = now

	healthy := authorizedDispatchResetRow{
		AuthorizationStatus:    sqlNullStr("active"),
		AuthorizationExpiresAt: sqlNullStr("2030-01-01T00:00:00Z"),
		SourceID:               sqlNullStr("acc-src"),
		SourceStatus:           sqlNullStr("active"),
		SourceExpiresAt:        sqlNullStr("2030-01-01T00:00:00Z"),
		AccountExpiresAt:       sqlNullStr("2030-01-01T00:00:00Z"),
	}
	binding := authorizedDispatchResetBinding{GroupID: "grp-1", AccountAuthorizationID: "ra-1"}
	access := AccessScope{IsAdmin: true}

	cases := []struct {
		name string
		make func() authorizedDispatchResetRow
		want string
	}{
		{"授权状态到期", func() authorizedDispatchResetRow {
			row := healthy
			row.AuthorizationStatus = sqlNullStr("expired")
			return row
		}, "授权已到期，当前账户不能调用"},
		{"授权到期时间已过", func() authorizedDispatchResetRow {
			row := healthy
			row.AuthorizationExpiresAt = sqlNullStr("2020-01-01T00:00:00Z")
			return row
		}, "授权已到期，当前账户不能调用"},
		{"授权暂停", func() authorizedDispatchResetRow {
			row := healthy
			row.AuthorizationStatus = sqlNullStr("paused")
			return row
		}, "授权已暂停，当前账户不能调用"},
		{"授权撤销", func() authorizedDispatchResetRow {
			row := healthy
			row.AuthorizationStatus = sqlNullStr("revoked")
			return row
		}, "授权关系已失效，当前账户不能调用"},
		{"来源不存在", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceID = sql.NullString{}
			return row
		}, "授权方原账户不存在或已删除，当前账户不能调用"},
		{"来源状态为空", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sql.NullString{}
			return row
		}, "授权方原账户不存在或已删除，当前账户不能调用"},
		{"来源套餐过期标记", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceLastErrorCode = sqlNullStr("account_expired")
			return row
		}, "授权方原账户已到期，当前账户不能调用"},
		{"来源已停用", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("disabled")
			return row
		}, "授权方原账户已停用，当前账户不能调用"},
		{"来源待检查", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("pending_test")
			return row
		}, "授权方原账户尚未通过后台健康检查，当前账户不能调用"},
		{"来源异常带消息", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("error")
			row.SourceLastErrorMessage = sqlNullStr("源异常详情")
			return row
		}, "源异常详情"},
		{"来源异常无消息", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("error")
			return row
		}, "授权方原账户处于异常状态，当前账户不能调用"},
		{"来源限流带消息", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("rate_limited")
			row.SourceLastErrorMessage = sqlNullStr("源限流详情")
			return row
		}, "源限流详情"},
		{"来源限流无消息", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("rate_limited")
			return row
		}, "授权方原账户限流中，当前账户不能调用"},
		{"来源临时不可用带消息", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("temporary_unavailable")
			row.SourceLastErrorMessage = sqlNullStr("源临时详情")
			return row
		}, "源临时详情"},
		{"来源临时不可用无消息", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("temporary_unavailable")
			return row
		}, "授权方原账户临时不可调用，当前账户不能调用"},
		{"来源质量隔离", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceStatus = sqlNullStr("quality_isolated")
			return row
		}, "授权方原账户因模型质量不达标已隔离，恢复前不能调用"},
		{"来源冷却中", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceCooldownUntil = sqlNullStr("2030-01-01T00:00:00Z")
			return row
		}, "授权方原账户正在冷却，恢复前当前账户不能调用"},
		{"来源关闭调度", func() authorizedDispatchResetRow {
			row := healthy
			row.SourceSchedulable = sqlInt64Lit(0)
			return row
		}, "授权方原账户已关闭调度，当前账户不能调用"},
		{"实例套餐过期", func() authorizedDispatchResetRow {
			row := healthy
			row.LastErrorCode = sqlNullStr("account_expired")
			return row
		}, "授权账户已到期，当前不可用"},
		{"未绑定分组", func() authorizedDispatchResetRow {
			return healthy
		}, "授权账户需要先绑定到你的分组"},
		{"健康", func() authorizedDispatchResetRow {
			return healthy
		}, ""},
	}
	for _, tc := range cases {
		b := binding
		if tc.name == "未绑定分组" {
			b = authorizedDispatchResetBinding{}
		}
		got, err := env.store.authorizedResetUnavailableMessage(context.Background(), tc.make(), b, access)
		if err != nil {
			t.Fatalf("%s：意外错误 %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s：got %q want %q", tc.name, got, tc.want)
		}
	}

	// 额度门：fake 端口超额 → 额度消息。
	fake.quotaExceeded = true
	got, err := env.store.authorizedResetUnavailableMessage(context.Background(), healthy, binding, access)
	if err != nil || got != "授权额度已用完，当前账户不能调用" {
		t.Fatalf("额度门：got %q err %v", got, err)
	}
	fake.quotaExceeded = false

	// 额度读取失败的 error 臂（端口错误按未超额处理）。
	errEffects := &w10aQuotaErrEffects{fakeRuntimeEffects: fake}
	env.store.SetRuntimeResetEffects(errEffects)
	if got, err := env.store.authorizedResetUnavailableMessage(context.Background(), healthy, binding, access); err != nil || got != "" {
		t.Fatalf("额度读取失败应按未超额处理：got %q err %v", got, err)
	}
	env.store.SetRuntimeResetEffects(nil)
}

// w10aQuotaErrEffects 覆盖授权额度读取，使其返回错误（端口失败臂）。
type w10aQuotaErrEffects struct {
	*fakeRuntimeEffects
}

func (w *w10aQuotaErrEffects) AuthorizationQuotaExceeded(context.Context, AuthorizationQuotaCheckInput) (bool, error) {
	return false, errors.New("quota read failed")
}

func TestW10AResetAuthorizationQuotaExceeded(t *testing.T) {
	env := newTestEnv(t)
	row := authorizedDispatchResetRow{ID: "acc-1", SystemAccountID: "owner-1"}
	fake := &fakeRuntimeEffects{quotaExceeded: true}
	env.store.SetRuntimeResetEffects(fake)
	if !env.store.resetAuthorizationQuotaExceeded(context.Background(), row, AccessScope{IsAdmin: true}) {
		t.Fatal("端口超额应返回 true")
	}
	fake.quotaExceeded = false
	if env.store.resetAuthorizationQuotaExceeded(context.Background(), row, AccessScope{IsAdmin: true}) {
		t.Fatal("端口未超额应返回 false")
	}
	errEffects := &w10aQuotaErrEffects{fakeRuntimeEffects: fake}
	env.store.SetRuntimeResetEffects(errEffects)
	if env.store.resetAuthorizationQuotaExceeded(context.Background(), row, AccessScope{IsAdmin: true}) {
		t.Fatal("端口错误应按未超额处理")
	}
	env.store.SetRuntimeResetEffects(nil)
	if env.store.resetAuthorizationQuotaExceeded(context.Background(), row, AccessScope{IsAdmin: true}) {
		t.Fatal("无端口应返回 false")
	}
}

// ---- HTTP 流程 ----

// w10aSeedAuthorizedInstance 构造一条干净的授权实例链（活跃源 + 活跃实例 +
// 绑定分组），返回实例 ID。夹具契约沿用 w2_m11_authorized_flows_test.go。
func w10aSeedAuthorizedInstance(t *testing.T, env *testEnv, granteeID, ownerID, groupID, instanceID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.seedM11Account(t, "acc-w10a-src", ownerID, "w10a源账户", "api_key", "active", Credentials{"api_key": "sk-w10a-src"})
	env.seedAuthorizationInstance(t, instanceID, granteeID, "ra-w10a", "acc-w10a-src")
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-w10a', 'account', 'acc-w10a-src', ?, ?, 'active', 'manual', ?, ?, ?)`,
		ownerID, granteeID, ownerID, now, now)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 'ra-w10a', 5, 0, 0, 1, ?, ?)`, granteeID, groupID, instanceID, now, now)
}

func TestW10AAuthorizedRuntimeResetUnchanged(t *testing.T) {
	env, _ := newM11TestEnv(t)
	granteeID := env.login(t, "grantee-w10a", "grantee-pass", "user")
	ownerID := env.login(t, "owner-w10a", "owner-pass", "user")
	env.login(t, "grantee-w10a", "grantee-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-w10a', ?, 'w10a授权分组', 'gpt', 1, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ownerID)
	w10aSeedAuthorizedInstance(t, env, granteeID, ownerID, "grp-w10a", "acc-w10a-inst")

	// 干净活跃实例：无失败态可清 → unchanged 结果（changed=false，状态保持）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w10a-inst/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("unchanged 重置状态码：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["changed"] != false || data["status"] != "active" || data["schedulable"] != true {
		t.Fatalf("干净实例应 unchanged：%v", data)
	}
	if !data["dispatchEligible"].(bool) {
		t.Fatalf("干净绑定实例应可调度：%v", data)
	}
	if row := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = 'acc-w10a-inst'`); row != "1" {
		t.Fatalf("unchanged 不应推进配置版本：%s", row)
	}

	// pending_test 授权实例在摘要层进入 skipped（行为契约）。
	env.exec(t, `UPDATE accounts SET status = 'pending_test' WHERE id = 'acc-w10a-inst'`)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w10a-inst/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("pending_test 授权实例状态码：%d %v", code, payload)
	}
	skipped, ok := dataMap(t, payload)["skipped"].([]any)
	if !ok || !containsAnyW10(skipped, "pending_test") {
		t.Fatalf("skipped 应含 pending_test：%v", dataMap(t, payload)["skipped"])
	}
}

func TestW10AOwnerResetPolicyCooldownSkip(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("策略冷却账户"))
	if code != http.StatusCreated {
		t.Fatalf("create：%d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.exec(t, `UPDATE accounts SET status = 'error',
		last_error_code = 'explicit_account_error_policy_cooldown',
		last_error_message = '策略冷却' WHERE id = ?`, id)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("策略冷却重置状态码：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["changed"] != false {
		t.Fatalf("策略冷却应跳过持久清理：%v", data)
	}
	skipped, ok := data["skipped"].([]any)
	if !ok || !containsAnyW10(skipped, "explicit_policy_cooldown") {
		t.Fatalf("skipped 应含 explicit_policy_cooldown：%v", data["skipped"])
	}
	if got := env.queryCell(t, `SELECT COALESCE(last_error_code,'') FROM accounts WHERE id = ?`, id); got != "explicit_account_error_policy_cooldown" {
		t.Fatalf("策略冷却不应被清理：%s", got)
	}

	// 遗留消息前缀变体：last_error_code 为空 + 账户错误策略「 前缀。
	env.exec(t, `UPDATE accounts SET last_error_code = NULL,
		last_error_message = '账户错误策略「429」持续 10 分钟' WHERE id = ?`, id)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("遗留前缀重置状态码：%d %v", code, payload)
	}
	if dataMap(t, payload)["changed"] != false {
		t.Fatalf("遗留前缀应跳过持久清理：%v", dataMap(t, payload))
	}
}

func TestW10AAuthorizedResetQuotaSkipped(t *testing.T) {
	env, store := newM11TestEnv(t)
	fake := &fakeRuntimeEffects{quotaExceeded: true}
	store.SetRuntimeResetEffects(fake)
	granteeID := env.login(t, "grantee-w10aq", "grantee-pass", "user")
	ownerID := env.login(t, "owner-w10aq", "owner-pass", "user")
	env.login(t, "grantee-w10aq", "grantee-pass", "user")
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-w10aq', ?, 'w10a额度分组', 'gpt', 1, 'personal', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ownerID)
	w10aSeedAuthorizedInstance(t, env, granteeID, ownerID, "grp-w10aq", "acc-w10aq-inst")

	// 端口超额：授权实例进入 authorization_quota 跳过，不做失败态清理。
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable', schedulable = 0,
		cooldown_until = '2030-01-01T00:00:00Z', last_error_code = 'rate_limited' WHERE id = 'acc-w10aq-inst'`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-w10aq-inst/runtime-reset",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("额度跳过重置状态码：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	skipped, ok := data["skipped"].([]any)
	if !ok || !containsAnyW10(skipped, "authorization_quota") {
		t.Fatalf("skipped 应含 authorization_quota：%v", data["skipped"])
	}
	if got := env.queryCell(t, `SELECT COALESCE(cooldown_until,'') FROM accounts WHERE id = 'acc-w10aq-inst'`); got == "" {
		t.Fatal("额度跳过时不应清理冷却时间")
	}
}

func containsAnyW10(values []any, want string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && text == want {
			return true
		}
	}
	return false
}

func sqlNullStr(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func sqlInt64Lit(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}
