package authz

// 领取必选目标分组 + 领取后换绑分组（2026-11-22 用户裁决）钉值测试（外部 PG
// 臂，pgtestkit 门控）：账户授权领取缺省/空 targetGroupId 拒绝
// target_group_required（绑定完成才是领取，无缺省回落默认分组，拒绝零写面）；
// change-group 仅账户授权、仅 active 来源，同组 no-op、异组移动绑定并保留
// 实例本地调度标记；目标分组校验与领取同口径（他人分组/超级分组/停用分组
// 拒绝）；父 grant paused 拒绝；换绑不复活接收方停用的实例。
import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestPinAccountClaimRequiresExplicitTargetGroup 钉账户领取必选分组：缺省
// targetGroupId 拒绝且零写面（不建实例、不写绑定、来源保持 pending）——
// 即使接收方没有任何分组，也即使接收方有默认分组，都不得回落。
func TestPinAccountClaimRequiresExplicitTargetGroup(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	// grantee 没有任何分组：旧契约在此回落默认分组并报「缺少启用的默认分组」，
	// 新契约在更早的必选校验处拒绝。
	offer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	_, claimErr := f.store.AcceptSource(context.Background(), "grantee", offer.SourceID,
		AcceptInput{}, f.now.UTC().Format(time.RFC3339Nano))
	fail, ok := claimErr.(*Fail)
	if !ok || fail.Code != "target_group_required" {
		t.Fatalf("claim without target group must fail with target_group_required: %#v", claimErr)
	}
	runtimeID := accountRuntimeIDFor(t, f, "acc-src", "grantee")
	var instanceCount int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_authorization_id = ?`),
		runtimeID).Scan(&instanceCount); err != nil || instanceCount != 0 {
		t.Fatalf("failed claim must not provision instance: %d, %v", instanceCount, err)
	}
	var bindingCount int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM group_accounts WHERE account_authorization_id = ?`),
		runtimeID).Scan(&bindingCount); err != nil || bindingCount != 0 {
		t.Fatalf("failed claim must not write binding: %d, %v", bindingCount, err)
	}
	var sourceStatus string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_sources WHERE id = ?`),
		offer.SourceID).Scan(&sourceStatus); err != nil || sourceStatus != StatusPending {
		t.Fatalf("failed claim must leave source pending: %q, %v", sourceStatus, err)
	}

	// 接收方有默认分组也不得静默回落：空 target 仍在必选校验处拒绝
	//（防回落回归钉）。
	seedGranteeDefaultGroup(t, f, "grantee", "gpt")
	f.seedProvisionSource(t, "acc-src-2", "owner", "生产账户二", "gpt")
	offer2, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "acc-src-2",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptSource(context.Background(), "grantee", offer2.SourceID,
		AcceptInput{}, f.now.UTC().Format(time.RFC3339Nano)); err == nil {
		t.Fatalf("claim with default group present but empty target must still be rejected")
	} else if fail, ok := err.(*Fail); !ok || fail.Code != "target_group_required" {
		t.Fatalf("empty target with default group present must fail with target_group_required: %#v", err)
	}
}

// TestChangeSourceGroupMovesBindingPreservingLocalFlags 钉换绑主链：异组换绑
// DELETE+INSERT 移动绑定且保留 local_priority 本地调度标记，同组 no-op；
// 空 target 与非法目标（他人分组/超级分组/停用分组）拒绝且绑定不动。
func TestChangeSourceGroupMovesBindingPreservingLocalFlags(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-a", "grantee", "gpt", 1, 0)
	f.seedGranteeGroup(t, "grp-b", "grantee", "gpt", 1, 0)
	f.seedGranteeGroup(t, "grp-disabled", "grantee", "gpt", 0, 0)
	f.seedGroup(t, "grp-owner", "owner")
	if _, err := f.db.Exec(fixtureBind(`INSERT INTO groups (id, name, system_account_id, status, enabled, is_default, group_kind, updated_at)
		VALUES ('grp-super', '超级', 'grantee', 'active', 1, 0, 'super', '2026-01-01T00:00:00Z')`)); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := f.now.UTC().Format(time.RFC3339Nano)
	offer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.AcceptSource(ctx, "grantee", offer.SourceID,
		AcceptInput{TargetGroupID: "grp-a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	instanceID := claimed.InstanceID
	// 领取后模拟接收方本地调度标记（列表行内编辑器写 local_*）。
	if _, err := f.db.Exec(fixtureBind(`UPDATE group_accounts SET local_priority = 7,
		local_super_priority_enabled = 1, local_fallback_enabled = 1 WHERE account_id = ?`), instanceID); err != nil {
		t.Fatal(err)
	}
	stats := &recordingStatsDirty{}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)

	// 空 target → target_group_required。
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", offer.SourceID, "", now)
	fail, ok := err.(*Fail)
	if !ok || fail.Code != "target_group_required" {
		t.Fatalf("change-group without target must fail with target_group_required: %#v", err)
	}
	// 他人分组（owner 所有）拒绝。
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", offer.SourceID, "grp-owner", now)
	if err == nil || !strings.Contains(err.Error(), "目标分组不存在或不属于被授权用户") {
		t.Fatalf("change-group to foreign group must be rejected: %v", err)
	}
	// 超级分组拒绝。
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", offer.SourceID, "grp-super", now)
	fail, ok = err.(*Fail)
	if !ok || fail.Code != "group_kind_conflict" {
		t.Fatalf("change-group to super group must return group_kind_conflict: %#v", err)
	}
	// 停用分组拒绝。
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", offer.SourceID, "grp-disabled", now)
	if err == nil || !strings.Contains(err.Error(), "目标分组已停用") {
		t.Fatalf("change-group to disabled group must be rejected: %v", err)
	}
	// 上述拒绝后绑定不动、零失效扇出：仍 grp-a、三个本地标记保留。
	assertNoFanout(t, stats, bus, 0, 0)
	f.assertBinding(t, instanceID, "grp-a", 7, 1, 1)

	// 异组换绑：移动到 grp-b，三个本地标记保留，旧行删除；提交后走 updated
	// 扇出（与领取同理由）。
	res, err := f.store.ChangeSourceGroup(ctx, "grantee", offer.SourceID, "grp-b", now)
	if err != nil || !res.Bound || res.InstanceID != instanceID || res.Status != StatusActive {
		t.Fatalf("change-group must move binding: %+v, %v", res, err)
	}
	assertOneFanout(t, stats, bus, 0, 0, invalidationReasonUpdated)
	f.assertBinding(t, instanceID, "grp-b", 7, 1, 1)

	// 同组换绑 no-op：成功、仍单行，且保持幂等无操作静默（零失效扇出）。
	if _, err := f.store.ChangeSourceGroup(ctx, "grantee", offer.SourceID, "grp-b", now); err != nil {
		t.Fatalf("same-group change must be a no-op success: %v", err)
	}
	assertNoFanout(t, stats, bus, 1, len(bus.calls))
	f.assertBinding(t, instanceID, "grp-b", 7, 1, 1)
}

// assertBinding 断言实例当前只有一条启用绑定且落在目标分组、三个本地调度
// 标记（组内优先级 / 超级优先 / 备用）与期望一致。
func (f *fixture) assertBinding(t *testing.T, instanceID, wantGroupID string, wantPriority, wantSuper, wantFallback int) {
	t.Helper()
	var bindingCount int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM group_accounts WHERE account_id = ?`),
		instanceID).Scan(&bindingCount); err != nil || bindingCount != 1 {
		t.Fatalf("instance must keep exactly one binding: %d, %v", bindingCount, err)
	}
	var groupID string
	var enabled, localPriority, superPriority, fallback int
	if err := f.db.QueryRow(fixtureBind(`SELECT group_id, enabled, local_priority, local_super_priority_enabled, local_fallback_enabled
		FROM group_accounts WHERE account_id = ?`), instanceID).
		Scan(&groupID, &enabled, &localPriority, &superPriority, &fallback); err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if groupID != wantGroupID || enabled != 1 || localPriority != wantPriority || superPriority != wantSuper || fallback != wantFallback {
		t.Fatalf("binding = %s/%d/%d/%d/%d, want %s/1/%d/%d/%d",
			groupID, enabled, localPriority, superPriority, fallback, wantGroupID, wantPriority, wantSuper, wantFallback)
	}
}

// TestChangeSourceGroupGuardsState 钉换绑守卫：非账户来源、非 active 来源、
// paused grant 拒绝；换绑不做来源字段同步，不复活接收方停用的实例。
func TestChangeSourceGroupGuardsState(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGroup(t, "grp_authz", "owner")
	f.seedGranteeGroup(t, "grp-a", "grantee", "gpt", 1, 0)
	f.seedGranteeGroup(t, "grp-b", "grantee", "gpt", 1, 0)

	ctx := context.Background()
	now := f.now.UTC().Format(time.RFC3339Nano)

	// 分组来源：领取后换绑拒绝（分组授权没有绑定概念）。
	groupOffer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: "group", ResourceID: "grp_authz",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptSource(ctx, "grantee", groupOffer.SourceID, AcceptInput{}, now); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", groupOffer.SourceID, "grp-a", now)
	fail, ok := err.(*Fail)
	if !ok || fail.Code != "state_conflict" {
		t.Fatalf("group source change-group must be rejected with state_conflict: %#v", err)
	}

	// 账户来源领取后归还：非 active 来源换绑拒绝。
	accountOffer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.AcceptSource(ctx, "grantee", accountOffer.SourceID,
		AcceptInput{TargetGroupID: "grp-a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	instanceID := claimed.InstanceID
	// 非接收方（资源所有者冒充接收方）拒绝：来源对 actor 不可见。
	if _, err := f.store.ChangeSourceGroup(ctx, "owner", accountOffer.SourceID, "grp-b", now); err == nil ||
		!strings.Contains(err.Error(), "授权不存在") {
		t.Fatalf("non-grantee actor must be rejected: %v", err)
	}
	if err := f.store.ReturnSourceForGrantee(ctx, accountOffer.SourceID, "grantee"); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", accountOffer.SourceID, "grp-b", now)
	fail, ok = err.(*Fail)
	if !ok || fail.Code != "state_conflict" {
		t.Fatalf("returned source change-group must be rejected with state_conflict: %#v", err)
	}

	// 重新发起并领取后暂停 grant：paused 期间换绑拒绝。
	reactiveOffer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = f.store.AcceptSource(ctx, "grantee", reactiveOffer.SourceID,
		AcceptInput{TargetGroupID: "grp-a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	instanceID = claimed.InstanceID
	if err := f.store.SetOfferPaused(ctx, reactiveOffer.GrantID, "owner", true, false, now); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.ChangeSourceGroup(ctx, "grantee", reactiveOffer.SourceID, "grp-b", now)
	fail, ok = err.(*Fail)
	if !ok || fail.Code != "state_conflict" {
		t.Fatalf("paused grant change-group must be rejected with state_conflict: %#v", err)
	}

	// 换绑不复活停用实例：恢复授权后停用实例再换绑，实例保持 disabled，
	// 绑定仍移动到 grp-b。
	if err := f.store.SetOfferPaused(ctx, reactiveOffer.GrantID, "owner", false, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(fixtureBind(`UPDATE accounts SET status = 'disabled', schedulable = 0 WHERE id = ?`), instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ChangeSourceGroup(ctx, "grantee", reactiveOffer.SourceID, "grp-b", now); err != nil {
		t.Fatalf("change-group after resume must succeed: %v", err)
	}
	var status string
	var schedulable int
	if err := f.db.QueryRow(fixtureBind(`SELECT status, schedulable FROM accounts WHERE id = ?`),
		instanceID).Scan(&status, &schedulable); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" || schedulable != 0 {
		t.Fatalf("change-group must not reactivate disabled instance: %s/%d", status, schedulable)
	}
	f.assertBinding(t, instanceID, "grp-b", 0, 0, 0)
}

// TestPinChangeGroupRouteContract 钉 HTTP 面：self 鉴权下 200 成功形状、
// 空 body 400 target_group_required、严格 JSON 白名单拒绝未知键、非接收方
// 被拒（store 层「授权不存在」原文透传）。
func TestPinChangeGroupRouteContract(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "stranger", "active")
	f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
	f.seedGranteeGroup(t, "grp-a", "grantee", "gpt", 1, 0)
	f.seedGranteeGroup(t, "grp-b", "grantee", "gpt", 1, 0)

	ctx := context.Background()
	now := f.now.UTC().Format(time.RFC3339Nano)
	offer, err := f.store.CreateOffer(ctx, OfferInput{
		ResourceType: "account", ResourceID: "acc-src",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptSource(ctx, "grantee", offer.SourceID,
		AcceptInput{TargetGroupID: "grp-a"}, now); err != nil {
		t.Fatal(err)
	}

	env := newUsageRouteEnv(t, "grantee", f)
	route := "/__aisys__/api/my-authorization-sources/" + offer.SourceID + "/change-group"
	status, body := env.do(t, http.MethodPost, route, `{"targetGroupId":"grp-b"}`)
	if status != http.StatusOK || !strings.Contains(body, `"bound":true`) {
		t.Fatalf("change-group 200 = %d %s", status, body)
	}
	if status, body = env.do(t, http.MethodPost, route, `{}`); status != http.StatusBadRequest ||
		!strings.Contains(body, "target_group_required") {
		t.Fatalf("空 body 应 400 target_group_required = %d %s", status, body)
	}
	if status, body = env.do(t, http.MethodPost, route, `{"targetGroupId":"grp-a","extra":1}`); status != http.StatusBadRequest {
		t.Fatalf("未知键应被严格 JSON 白名单拒绝 = %d %s", status, body)
	}
	other := newUsageRouteEnv(t, "stranger", f)
	if status, body = other.do(t, http.MethodPost, route, `{"targetGroupId":"grp-a"}`); status != http.StatusBadRequest ||
		!strings.Contains(body, "授权不存在") {
		t.Fatalf("非接收方应被拒为授权不存在 = %d %s", status, body)
	}
}
