package main

// W1f：chain_dispatch.go / chain_preflight.go / chain_chat_tool_capabilities.go
// 组合根适配层的分支补测。全部依赖以 Mock/夹具注入（内存并发追踪器、fake
// ResponseSink、fake SessionAffinity、SQLite accountkeystates、runtime cache
// 夹具），可回放、结果稳定，不修改任何既有文件。
//
// 已知不可达分支：chainImagePreflight.Apply 的 DowngradeReasonInvalidJSON /
// DowngradeReasonJSONWorkerOverloaded 两个 arm 只消费降级结果；当前 Go 生产
// 代码（gatewaybody）没有任何路径产生这两个 reason（Node json worker 的
// invalid/overloaded 观测未移植到 Go 降级原语），Apply 入口无法构造出对应
// 输入，故不强行构造不可达测试。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// 测试协作对象
// ---------------------------------------------------------------------------

// w1fResponseSink 捕获 preauth Responses 端口的失败响应调用。
type w1fResponseSink struct {
	failures []gatewaypreauth.FailureResponseInput
}

func (s *w1fResponseSink) SendGatewayFailureResponse(input gatewaypreauth.FailureResponseInput) {
	s.failures = append(s.failures, input)
}

func (s *w1fResponseSink) FinalizeGatewayAuthFailureAudit(*gatewaypreauth.GatewayRequest, gatewaypreauth.GatewayResponseWriter, gatewaypreauth.AuditCaptureContext) {
}

func (s *w1fResponseSink) SendAuthenticatedModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}

func (s *w1fResponseSink) SendOpenAIModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {}

func (s *w1fResponseSink) SendAnthropicModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {}

func (s *w1fResponseSink) SendGeminiModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {}

// w1fAffinityForget 记录一次会话亲和遗忘调用。
type w1fAffinityForget struct {
	sessionKey string
	accountID  string
}

// w1fAffinityPort 是 gatewaydispatch.SessionAffinityPort 的最小 fake。
type w1fAffinityPort struct {
	forgot []w1fAffinityForget
}

func (p *w1fAffinityPort) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ string, _ gatewaydispatch.AffinityOrderingOptions) ([]gatewaydispatch.AccountCandidate, error) {
	return accounts, nil
}

func (p *w1fAffinityPort) ClaimAsync(context.Context, string, string, gatewaydispatch.AffinityScope) (string, bool) {
	return "", false
}

func (p *w1fAffinityPort) RememberAsync(context.Context, string, string, gatewaydispatch.AffinityScope) {
}

func (p *w1fAffinityPort) ForgetAsync(_ context.Context, sessionKey, accountID string) error {
	p.forgot = append(p.forgot, w1fAffinityForget{sessionKey: sessionKey, accountID: accountID})
	return nil
}

func (p *w1fAffinityPort) AreHighConcurrencyAccountsBusyForLaneAsync(context.Context, []gatewaydispatch.AccountCandidate, gatewaydispatch.HighConcurrencyBusyOptions) (bool, error) {
	return false, nil
}

// w1fAccountView 是 gatewayresponse.AccountView 的非 OpenAI 实现（窄口回落
// 分支覆盖）。
type w1fAccountView struct {
	id string
}

func (v w1fAccountView) GetID() string                        { return v.id }
func (v w1fAccountView) GetName() string                      { return "" }
func (v w1fAccountView) GetProviderCode() string              { return "" }
func (v w1fAccountView) GetProviderProtocolProfileID() string { return "" }
func (v w1fAccountView) GetProtocolCode() string              { return "" }
func (v w1fAccountView) GetProtocolVersion() string           { return "" }
func (v w1fAccountView) GetClientCompatibility() string       { return "" }

// w1fImagePreflight 装配带 fake ResponseSink 的图像权限预检。
func w1fImagePreflight(t *testing.T, obs *chainCapturedObservability) (*chainImagePreflight, *w1fResponseSink) {
	t.Helper()
	service, err := gatewaypreauth.New(gatewaypreauth.Service{
		RuntimeCache:  chainPreauthStubRuntimeCache{},
		Observability: obs,
		Clock:         gatewaypreauth.SystemClock{},
	})
	if err != nil {
		t.Fatalf("创建 preauth 服务: %v", err)
	}
	sink := &w1fResponseSink{}
	service.Responses = sink
	return &chainImagePreflight{preauth: service}, sink
}

// w1fImageBodyRequest 构造带已捕获 body 状态的网关请求。
func w1fImageBodyRequest(t *testing.T, path string, rawBody []byte, parsed map[string]any, state *gatewaybody.BodyState, withLease bool) *gatewaypreauth.GatewayRequest {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(rawBody)))
	httpReq.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(httpReq)
	req.Body = &gatewaybody.Request{
		RawBody:           rawBody,
		Body:              parsed,
		ContentTypeHeader: "application/json",
		State:             state,
	}
	if withLease {
		limiter := gatewaybody.NewInFlightLimiter()
		lease, ok := limiter.TryAcquire(len(rawBody), gatewaybody.DefaultGatewayBodyInFlightMaxBytes)
		if !ok {
			t.Fatalf("in-flight 租约必须获取成功")
		}
		req.Body.Lease = lease
	}
	return req
}

// ---------------------------------------------------------------------------
// chain_dispatch.go：mutation context / epoch / deref 辅助
// ---------------------------------------------------------------------------

func TestW1FDispatchMutationContextArms(t *testing.T) {
	t.Run("空映射保持 nil", func(t *testing.T) {
		if chainMutationContextOf(nil) != nil {
			t.Fatalf("nil 映射必须返回 nil")
		}
		if chainMutationContextOf(map[string]any{}) != nil {
			t.Fatalf("空映射必须返回 nil")
		}
		if mode := chainQuotaRecoveryModeOf(nil); mode != "" {
			t.Fatalf("nil 映射的恢复模式 = %q, want 空串", mode)
		}
	})

	t.Run("字段名大小写不敏感投影", func(t *testing.T) {
		context := chainMutationContextOf(map[string]any{
			"authority":         "confirmed_same_account_key_rotation",
			"trafficSource":     "gateway",
			"quotaRecoveryMode": "explicit_reset",
		})
		if context == nil {
			t.Fatal("合法映射必须投影成功")
		}
		if context.Authority != gatewayaccounteffects.MutationAuthorityConfirmedSameAccountKeyRotation {
			t.Fatalf("authority = %q", context.Authority)
		}
		if context.TrafficSource != "gateway" {
			t.Fatalf("trafficSource = %q", context.TrafficSource)
		}
		if context.QuotaRecoveryMode != gatewayaccounteffects.QuotaRecoveryMode("explicit_reset") {
			t.Fatalf("quotaRecoveryMode = %q", context.QuotaRecoveryMode)
		}
		if mode := chainQuotaRecoveryModeOf(map[string]any{"quotaRecoveryMode": "generic"}); mode != "generic" {
			t.Fatalf("恢复模式 = %q, want generic", mode)
		}
	})

	t.Run("marshal 失败回落 nil", func(t *testing.T) {
		if context := chainMutationContextOf(map[string]any{"bad": make(chan int)}); context != nil {
			t.Fatalf("不可编码载荷必须返回 nil, got %+v", context)
		}
	})

	t.Run("字段类型不匹配回落 nil", func(t *testing.T) {
		// 数字写入 string 字段：marshal 成功但 unmarshal 失败。
		if context := chainMutationContextOf(map[string]any{"authority": 123}); context != nil {
			t.Fatalf("类型不匹配必须返回 nil, got %+v", context)
		}
		if mode := chainQuotaRecoveryModeOf(map[string]any{"quotaRecoveryMode": 123}); mode != "" {
			t.Fatalf("类型不匹配的恢复模式 = %q, want 空串", mode)
		}
	})

	t.Run("observation epoch 归一化", func(t *testing.T) {
		if chainObservationEpochPtrOf("") != nil {
			t.Fatalf("空串必须返回 nil")
		}
		if chainObservationEpochPtrOf("   ") != nil {
			t.Fatalf("纯空白必须返回 nil")
		}
		if chainObservationEpochPtrOf("abc") != nil {
			t.Fatalf("非数字必须返回 nil")
		}
		parsed := chainObservationEpochPtrOf(" 456 ")
		if parsed == nil || *parsed != 456 {
			t.Fatalf("epoch = %+v, want 456", parsed)
		}
	})
}

func TestW1FDispatchDerefHelpers(t *testing.T) {
	value := "x"
	number := int64(9)
	if derefStringValue(nil) != "" || derefStringValue(&value) != "x" {
		t.Fatalf("derefStringValue 分支错误")
	}
	if derefInt64Ptr2(nil) != 0 || derefInt64Ptr2(&number) != 9 {
		t.Fatalf("derefInt64Ptr2 分支错误")
	}
	if derefInt64(nil) != 0 || derefInt64(&number) != 9 {
		t.Fatalf("derefInt64 分支错误")
	}
	if bodyOf(nil) != nil {
		t.Fatalf("bodyOf(nil) 必须返回 nil")
	}
}

func TestW1FAvoidanceAccountsProjection(t *testing.T) {
	binding := "sys-bind"
	bound := "grp-bind"
	authz := "authz-1"
	projections := chainAvoidanceAccountsOf([]gatewaydispatch.AccountCandidate{
		{
			ID:                     "acc-1",
			AccountAccessType:      "personal",
			BindingSystemAccountID: &binding,
			BoundGroupID:           &bound,
			AccountAuthorizationID: &authz,
		},
		{ID: "acc-2"},
	})
	if len(projections) != 2 {
		t.Fatalf("投影数 = %d, want 2", len(projections))
	}
	first := projections[0]
	if first.ID != "acc-1" || first.AccountAccessType != "personal" ||
		first.BindingSystemAccountID != "sys-bind" || first.BoundGroupID != "grp-bind" ||
		first.AccountAuthorizationID != "authz-1" {
		t.Fatalf("首账户投影 = %+v", first)
	}
	if projections[1].BindingSystemAccountID != "" || projections[1].BoundGroupID != "" || projections[1].AccountAuthorizationID != "" {
		t.Fatalf("nil 指针必须展开为空串: %+v", projections[1])
	}
	if empty := chainAvoidanceAccountsOf(nil); len(empty) != 0 {
		t.Fatalf("空输入必须返回空切片")
	}
}

func TestW1FConfigIntOfArms(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int64
	}{
		{"float64", float64(7), 7},
		{"int64", int64(9), 9},
		{"int", int(11), 11},
		{"字符串回落 0", "12", 0},
		{"nil 回落 0", nil, 0},
		{"布尔回落 0", true, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := chainConfigIntOf(testCase.value); got != testCase.want {
				t.Fatalf("chainConfigIntOf(%v) = %d, want %d", testCase.value, got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_dispatch.go：并发存储适配
// ---------------------------------------------------------------------------

func TestW1FConcurrencyStoreAcquireRelease(t *testing.T) {
	tracker := gatewayclientip.NewMemoryAccountConcurrency(nil)
	store := newChainConcurrencyStore(tracker)
	ctx := context.Background()

	currents, err := store.LoadCurrentAsync(ctx, []string{"acc-a", "acc-b"})
	if err != nil {
		t.Fatalf("读取总并发: %v", err)
	}
	if len(currents) != 2 || currents["acc-a"] != 0 || currents["acc-b"] != 0 {
		t.Fatalf("初始总并发 = %+v, want 全 0", currents)
	}
	byLane, err := store.LoadCurrentByLaneAsync(ctx, []string{"acc-a"}, "text")
	if err != nil || byLane["acc-a"] != 0 {
		t.Fatalf("初始 lane 并发 = %+v err %v", byLane, err)
	}

	laneLimit := 1
	slot, err := store.TryAcquireAsync(ctx, "acc-a", 1, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "text", LaneLimit: &laneLimit})
	if err != nil {
		t.Fatalf("首次获取: %v", err)
	}
	if !slot.Acquired || slot.Current != 1 || slot.Limit != 1 || slot.Lane != "text" ||
		slot.LaneCurrent != 1 || slot.LaneLimit != 1 || slot.Release == nil {
		t.Fatalf("首个槽位 = %+v", slot)
	}

	full, err := store.TryAcquireAsync(ctx, "acc-a", 1, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "text", LaneLimit: &laneLimit})
	if err != nil {
		t.Fatalf("饱和获取: %v", err)
	}
	if full.Acquired || full.Current != 1 || full.LaneCurrent != 1 || full.Release != nil {
		t.Fatalf("饱和槽位 = %+v, want 未获取且无释放闭包", full)
	}

	slot.Release()
	after, err := store.LoadCurrentByLaneAsync(ctx, []string{"acc-a"}, "text")
	if err != nil || after["acc-a"] != 0 {
		t.Fatalf("释放后 lane 并发 = %+v err %v, want 0", after, err)
	}

	// LaneLimit 覆盖总上限：lane 限额 2 时前两次成功、第三次失败。
	wideLimit := 2
	for index := 0; index < 2; index++ {
		acquired, err := store.TryAcquireAsync(ctx, "acc-b", 1, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "text", LaneLimit: &wideLimit})
		if err != nil || !acquired.Acquired {
			t.Fatalf("第 %d 次获取 = %+v err %v, want 成功", index+1, acquired, err)
		}
	}
	blocked, err := store.TryAcquireAsync(ctx, "acc-b", 1, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "text", LaneLimit: &wideLimit})
	if err != nil || blocked.Acquired {
		t.Fatalf("lane 限额耗尽后 = %+v err %v, want 拒绝", blocked, err)
	}

	// concurrencyLimit=0 表示不限流：总能获取。
	unlimited, err := store.TryAcquireAsync(ctx, "acc-c", 0, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "image"})
	if err != nil || !unlimited.Acquired || unlimited.LaneLimit != 0 {
		t.Fatalf("不限流槽位 = %+v err %v", unlimited, err)
	}
}

// ---------------------------------------------------------------------------
// chain_dispatch.go：runtime cache 端口的派发读面
// ---------------------------------------------------------------------------

func TestW1FDispatchRuntimeCachePortReads(t *testing.T) {
	fixture := newChainFixture(t)
	port := newChainRuntimeCachePort(fixture.cache)
	ctx := context.Background()

	accounts, err := port.ListCachedOpenAIAccountsForGroupAsync(ctx, fixture.groupID, fixture.systemAccount, gatewaydispatch.CachedAccountsOptions{RequestedModel: "gpt-test"})
	if err != nil {
		t.Fatalf("列派发候选: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != fixture.accountID {
		t.Fatalf("派发候选 = %+v, want %s", accounts, fixture.accountID)
	}

	meta, ok, err := port.ResolveCachedGroupUsageAccessMetadataAsync(ctx, fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("解析分组用量元数据: %v", err)
	}
	// 夹具分组是 owner 直连访问（无授权实例），命中 ok=true 且携带属主投影。
	if !ok || meta.GroupOwnerSystemAccountID != fixture.systemAccount || meta.GroupAccessType != "owner" {
		t.Fatalf("分组元数据存在性 = %v, meta = %+v", ok, meta)
	}

	missing, ok, err := port.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "group_missing", fixture.systemAccount)
	if err != nil {
		t.Fatalf("缺失分组必须无错误: %v", err)
	}
	if ok {
		t.Fatalf("缺失分组必须回落 ok=false, got %+v", missing)
	}
}

// ---------------------------------------------------------------------------
// chain_dispatch.go：响应层账户效果的无操作面 + 视图还原
// ---------------------------------------------------------------------------

func TestW1FResponseAccountEffectsNoOps(t *testing.T) {
	effects := &chainResponseAccountEffects{}
	if err := effects.HandleStreamFailure(nil, "上游断流", "stream_error", gatewayresponse.StreamFailureContext{}, true); err != nil {
		t.Fatalf("流失败观察必须保持 no-op: %v", err)
	}
	if effects.DispatchRequestFailureAccountHealthCheck("gateway", "acc-1") {
		t.Fatalf("响应层健康检查派发必须恒 false")
	}
	// affinity 未装配时遗忘调用必须安全跳过。
	effects.ForgetSessionAffinity("sess-1", "acc-1")

	port := &w1fAffinityPort{}
	wired := &chainResponseAccountEffects{affinity: port}
	wired.ForgetSessionAffinity("sess-2", "acc-2")
	if len(port.forgot) != 1 || port.forgot[0].sessionKey != "sess-2" || port.forgot[0].accountID != "acc-2" {
		t.Fatalf("遗忘调用 = %+v, want sess-2/acc-2", port.forgot)
	}

	view := gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-view"}}
	secret, ok := chainAccountSecretOfView(view)
	if !ok || secret.ID != "acc-view" {
		t.Fatalf("OpenAI 视图还原 = %+v ok %v", secret, ok)
	}
	fallback, ok := chainAccountSecretOfView(w1fAccountView{id: "acc-fallback"})
	if ok || fallback.ID != "acc-fallback" {
		t.Fatalf("未知视图回落 = %+v ok %v, want ID 回落且 ok=false", fallback, ok)
	}
}

// ---------------------------------------------------------------------------
// chain_dispatch.go：账户 API Key 运行态写侧桥（真 SQLite store）
// ---------------------------------------------------------------------------

func TestW1FAccountAPIKeyWriterSQLite(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	keyStates := fixture.keyStates
	ctx := context.Background()

	// 既有夹具表缺生产 DDL 的 last_success_at 列；RecordSuccess 的 upsert
	// 会写该列，这里在测试内补齐（不改既有夹具文件）。
	if _, err := fixture.db.Exec(`ALTER TABLE account_api_key_runtime_states ADD COLUMN last_success_at TEXT`); err != nil {
		t.Fatalf("补齐 last_success_at 列: %v", err)
	}

	fingerprint := keyStates.FingerprintAPIKey("sk-pool-1")
	poolAccount := gatewayruntimecache.OpenAIAccountSecret{
		ID:                        "acc_w1f_writer",
		SystemAccountID:           "sys_owner",
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		Type:                      "api_key",
		APIKeys:                   []string{"sk-pool-1", "sk-pool-2"},
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	writer := &chainAccountAPIKeyWriter{keyStates: keyStates}

	// 非 pool 账户（单 Key）保持跳过语义。
	singleFingerprint := keyStates.FingerprintAPIKey("sk-single")
	single := poolAccount
	single.ID = "acc_w1f_single"
	single.APIKeys = []string{"sk-single"}
	single.SelectedAPIKeyFingerprint = &singleFingerprint
	skipped, err := writer.RecordFailure(ctx, gatewayaccounteffects.AccountAPIKeyFailureWrite{
		Account: single,
		Input: gatewayaccounteffects.AccountAPIKeyFailureWriteInput{
			Status:     gatewayaccounteffects.APIKeyStatusRateLimited,
			ObservedAt: "2026-09-01T10:00:00.000Z",
		},
	})
	if err != nil {
		t.Fatalf("单 Key 写入: %v", err)
	}
	if skipped.Changed || skipped.SkippedReason == nil || *skipped.SkippedReason != "not_api_key_pool_account" {
		t.Fatalf("单 Key 结果 = %+v, want 未变更且带跳过原因", skipped)
	}

	// pool 账户失败写入：状态/错误来源/指纹落库。
	cooldownUntil := "2026-09-01T11:00:00.000Z"
	failure, err := writer.RecordFailure(ctx, gatewayaccounteffects.AccountAPIKeyFailureWrite{
		Account: poolAccount,
		Input: gatewayaccounteffects.AccountAPIKeyFailureWriteInput{
			Status:        gatewayaccounteffects.APIKeyStatusRateLimited,
			StatusCode:    int64PtrOf(429),
			ErrorCode:     strPtr("rate_limit_exceeded"),
			ErrorMessage:  strPtr("上游限流"),
			TraceID:       strPtr("trace-w1f"),
			CooldownUntil: &cooldownUntil,
			ObservedAt:    "2026-09-01T10:00:00.000Z",
		},
	})
	if err != nil {
		t.Fatalf("失败写入: %v", err)
	}
	if !failure.Changed || failure.SkippedReason != nil {
		t.Fatalf("失败写入结果 = %+v, want 已变更无跳过", failure)
	}
	var status, cooldown, errorCode, errorMessage, traceID, storedFingerprint string
	if err := fixture.db.QueryRow(`SELECT status, cooldown_until, last_error_code, last_error_message, last_trace_id, key_fingerprint
		FROM account_api_key_runtime_states WHERE account_id = ?`, poolAccount.ID).
		Scan(&status, &cooldown, &errorCode, &errorMessage, &traceID, &storedFingerprint); err != nil {
		t.Fatalf("读取失败状态行: %v", err)
	}
	if status != "rate_limited" {
		t.Fatalf("状态 = %q, want rate_limited", status)
	}
	if cooldown == "" || storedFingerprint != fingerprint {
		t.Fatalf("冷却/指纹 = %q/%q, want 非空且指纹一致", cooldown, storedFingerprint)
	}
	if errorCode != "rate_limit_exceeded" || errorMessage != "上游限流" || traceID != "trace-w1f" {
		t.Fatalf("失败来源 = %q/%q/%q", errorCode, errorMessage, traceID)
	}

	// 成功写入：状态复位、冷却清空、成功计数累加。
	success, err := writer.RecordSuccess(ctx, gatewayaccounteffects.AccountAPIKeySuccessWrite{
		Account:    poolAccount,
		ObservedAt: "2026-09-01T12:00:00.000Z",
	})
	if err != nil {
		t.Fatalf("成功写入: %v", err)
	}
	if !success.Changed || success.SkippedReason != nil {
		t.Fatalf("成功写入结果 = %+v, want 已变更无跳过", success)
	}
	var activeStatus string
	var successCount int
	var clearedCooldown sql.NullString
	if err := fixture.db.QueryRow(`SELECT status, success_count, cooldown_until
		FROM account_api_key_runtime_states WHERE account_id = ?`, poolAccount.ID).
		Scan(&activeStatus, &successCount, &clearedCooldown); err != nil {
		t.Fatalf("读取成功状态行: %v", err)
	}
	if activeStatus != "active" || successCount != 1 || clearedCooldown.Valid {
		t.Fatalf("成功复位 = %q/%d/%+v, want active/1/NULL", activeStatus, successCount, clearedCooldown)
	}
}

// ---------------------------------------------------------------------------
// chain_preflight.go：超限自动降级拒绝
// ---------------------------------------------------------------------------

func TestW1FImagePreflightOversizedAutoDowngrade(t *testing.T) {
	megabytes := int64(1)
	limitBytes := 1024 * 1024
	cases := []struct {
		name           string
		limitMegabytes *int64
		rawLen         int
		wantRejected   bool
	}{
		{"超限必须 413 拒绝", &megabytes, limitBytes + 16, true},
		{"恰好等于上限不拒绝", &megabytes, limitBytes, false},
		{"未配置时默认上限内不拒绝", nil, 4096, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			obs := &chainCapturedObservability{}
			preflight, sink := w1fImagePreflight(t, obs)
			rawBody := make([]byte, testCase.rawLen)
			parsed := map[string]any{
				"model": "gpt-test",
				"tools": []any{map[string]any{"type": "image_generation"}},
			}
			req := w1fImageBodyRequest(t, "/v1/chat/completions", rawBody, parsed, &gatewaybody.BodyState{
				JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON,
				ContentType:     "application/json",
				ImageGeneration: true,
			}, testCase.wantRejected)
			capture := &chainTestAuditCapture{}
			res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

			result, err := preflight.Apply(context.Background(), gatewaypreauth.ImagePermissionPreflightInput{
				Req:                              req,
				Res:                              res,
				AuditCapture:                     capture,
				APIKeyRecord:                     &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
				RequestLane:                      string(gatewayproto.LaneImage),
				SystemAccountID:                  "sys_owner",
				APIKeyID:                         "key_1",
				GroupID:                          "group_1",
				Endpoint:                         "/v1/chat/completions",
				GatewayTextRawBodyLimitMegabytes: testCase.limitMegabytes,
			})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if !testCase.wantRejected {
				// 未超限：继续按 auto 工具降级走文本 lane。
				if result.Completed {
					t.Fatalf("未超限请求必须继续, result = %+v", result)
				}
				if result.RequestLane != string(gatewaypreauth.RequestLaneText) {
					t.Fatalf("requestLane = %q, want text", result.RequestLane)
				}
				if len(sink.failures) != 0 {
					t.Fatalf("未超限不得发送失败响应: %+v", sink.failures)
				}
				return
			}
			if !result.Completed {
				t.Fatalf("超限请求必须终局, result = %+v", result)
			}
			if len(sink.failures) != 1 {
				t.Fatalf("失败响应数 = %d, want 1", len(sink.failures))
			}
			sent := sink.failures[0]
			if sent.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("状态码 = %d, want 413", sent.StatusCode)
			}
			if sent.Audit.ErrorCode != "request_body_too_large" || sent.Audit.ErrorPhase != "request_validation" {
				t.Fatalf("审计 = %+v", sent.Audit)
			}
			if sent.ResponsePayload.Error.Message != "请求体过大" {
				t.Fatalf("客户端文案 = %q", sent.ResponsePayload.Error.Message)
			}
			if req.Body.RawBody != nil || req.Body.Body != nil {
				t.Fatalf("超限请求体必须清空: raw=%d", len(req.Body.RawBody))
			}
			if req.Body.Lease != nil {
				t.Fatalf("超限请求必须释放 in-flight 租约")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_preflight.go：终局 403 与 forced 工具 defer 分支
// ---------------------------------------------------------------------------

func TestW1FImagePreflightFinalForbiddenArms(t *testing.T) {
	t.Run("图像端点请求终局 403", func(t *testing.T) {
		obs := &chainCapturedObservability{}
		preflight, sink := w1fImagePreflight(t, obs)
		req := w1fImageBodyRequest(t, "/v1/images/generations", nil, nil, nil, false)
		capture := &chainTestAuditCapture{}
		res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

		result, err := preflight.Apply(context.Background(), gatewaypreauth.ImagePermissionPreflightInput{
			Req:          req,
			Res:          res,
			AuditCapture: capture,
			APIKeyRecord: &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
			RequestLane:  string(gatewayproto.LaneImage),
			Endpoint:     "/v1/images/generations",
		})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !result.Completed {
			t.Fatalf("终局 403 必须 Completed, result = %+v", result)
		}
		if len(sink.failures) != 1 {
			t.Fatalf("失败响应数 = %d, want 1", len(sink.failures))
		}
		sent := sink.failures[0]
		if sent.StatusCode != http.StatusForbidden {
			t.Fatalf("状态码 = %d, want 403", sent.StatusCode)
		}
		if sent.Audit.ErrorCode != gatewaypreauth.ImageGenerationDisabledCode || sent.Audit.ErrorPhase != "authorization" {
			t.Fatalf("审计 = %+v", sent.Audit)
		}
		if sent.ResponsePayload.Error.Code != gatewaypreauth.ImageGenerationDisabledCode ||
			sent.ResponsePayload.Error.Message != gatewaypreauth.ImageGenerationDisabledMessage {
			t.Fatalf("客户端载荷 = %+v", sent.ResponsePayload.Error)
		}
	})

	t.Run("forced 工具 defer 保持原 lane 继续", func(t *testing.T) {
		obs := &chainCapturedObservability{}
		preflight, sink := w1fImagePreflight(t, obs)
		parsed := map[string]any{
			"model":       "gpt-test",
			"tools":       []any{map[string]any{"type": "image_generation"}},
			"tool_choice": "required",
		}
		req := w1fImageBodyRequest(t, "/v1/chat/completions", []byte(`{"forced":true}`), parsed, &gatewaybody.BodyState{
			JSONParseStatus:       gatewaybody.JSONParseStatusScannedJSON,
			ContentType:           "application/json",
			ImageGeneration:       true,
			ImageGenerationForced: true,
		}, false)
		capture := &chainTestAuditCapture{}
		res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

		result, err := preflight.Apply(context.Background(), gatewaypreauth.ImagePermissionPreflightInput{
			Req:                            req,
			Res:                            res,
			AuditCapture:                   capture,
			APIKeyRecord:                   &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
			RequestLane:                    string(gatewayproto.LaneImage),
			DeferForcedImageGenerationTool: true,
		})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if result.Completed {
			t.Fatalf("defer 请求必须继续, result = %+v", result)
		}
		if result.RequestLane != string(gatewayproto.LaneImage) {
			t.Fatalf("requestLane = %q, want image", result.RequestLane)
		}
		if len(sink.failures) != 0 {
			t.Fatalf("defer 分支不得发送失败响应: %+v", sink.failures)
		}
	})

	t.Run("forced 工具不 defer 时终局 403", func(t *testing.T) {
		obs := &chainCapturedObservability{}
		preflight, sink := w1fImagePreflight(t, obs)
		parsed := map[string]any{
			"model":       "gpt-test",
			"tools":       []any{map[string]any{"type": "image_generation"}},
			"tool_choice": "required",
		}
		req := w1fImageBodyRequest(t, "/v1/chat/completions", []byte(`{"forced":true}`), parsed, &gatewaybody.BodyState{
			JSONParseStatus:       gatewaybody.JSONParseStatusScannedJSON,
			ContentType:           "application/json",
			ImageGeneration:       true,
			ImageGenerationForced: true,
		}, false)
		capture := &chainTestAuditCapture{}
		res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())

		result, err := preflight.Apply(context.Background(), gatewaypreauth.ImagePermissionPreflightInput{
			Req:          req,
			Res:          res,
			AuditCapture: capture,
			APIKeyRecord: &gatewayruntimecache.GatewayAPIKeyRow{SystemAccountImageGenerationEnabled: 0},
			RequestLane:  string(gatewayproto.LaneImage),
		})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !result.Completed || len(sink.failures) != 1 || sink.failures[0].StatusCode != http.StatusForbidden {
			t.Fatalf("不 defer 的 forced 请求必须终局 403: result=%+v failures=%+v", result, sink.failures)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_preflight.go：speed-first 准入门辅助
// ---------------------------------------------------------------------------

// w1fHybridRuntime 构造 hybrid_smart 运行态：运行时不解码 normalRoutingConfig，
// NormalRoutingConfig 恒 nil（准入门依赖该解码契约排除 hybrid_smart）。
func w1fHybridRuntime(mode *string) *gatewayruntimecache.GatewayRuntime {
	runtime := speedFirstRuntime(mode, nil, nil, nil, 1)
	runtime.APIKey.NormalRoutingConfig = nil
	return runtime
}

func TestW1FSpeedFirstAdmissionGuards(t *testing.T) {
	personal := "personal"
	weightedMode := "weighted"
	failoverMode := "failover"
	roundRobinMode := "round_robin"
	hybridMode := "hybrid_smart"
	costFirst := "cost_first"

	t.Run("applicability 守卫", func(t *testing.T) {
		cases := []struct {
			name    string
			runtime *gatewayruntimecache.GatewayRuntime
			lane    gatewayproto.RequestLane
			want    bool
		}{
			{"nil runtime", nil, gatewayproto.LaneText, false},
			{"weighted 模式适用", speedFirstRuntime(&weightedMode, nil, nil, nil, 1), gatewayproto.LaneText, true},
			{"failover 模式适用", speedFirstRuntime(&failoverMode, nil, nil, nil, 1), gatewayproto.LaneText, true},
			{"round_robin 模式适用", speedFirstRuntime(&roundRobinMode, nil, nil, nil, 1), gatewayproto.LaneText, true},
			{"hybrid_smart 不适用", w1fHybridRuntime(&hybridMode), gatewayproto.LaneText, false},
			{"非 speed_first 偏好", speedFirstRuntime(nil, &costFirst, nil, nil, 1), gatewayproto.LaneText, false},
			{"非 high_concurrency 分组", speedFirstRuntime(nil, nil, &personal, nil, 1), gatewayproto.LaneText, false},
			{"无账户", speedFirstRuntime(nil, nil, nil, nil, 0), gatewayproto.LaneText, false},
			{"图像 lane", speedFirstRuntime(nil, nil, nil, nil, 1), gatewayproto.LaneImage, false},
			{"全部满足", speedFirstRuntime(nil, nil, nil, nil, 1), gatewayproto.LaneText, true},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				if got := chainSpeedFirstBodyAdmissionApplies(testCase.runtime, testCase.lane); got != testCase.want {
					t.Fatalf("applicability = %v, want %v", got, testCase.want)
				}
			})
		}
		t.Run("缺 NormalRoutingConfig 不适用", func(t *testing.T) {
			runtime := speedFirstRuntime(nil, nil, nil, nil, 1)
			runtime.APIKey.NormalRoutingConfig = nil
			if chainSpeedFirstBodyAdmissionApplies(runtime, gatewayproto.LaneText) {
				t.Fatalf("缺 NormalRoutingConfig 必须不适用")
			}
		})
		t.Run("缺 GroupAccess 不适用", func(t *testing.T) {
			runtime := speedFirstRuntime(nil, nil, nil, nil, 1)
			runtime.GroupAccess = nil
			if chainSpeedFirstBodyAdmissionApplies(runtime, gatewayproto.LaneText) {
				t.Fatalf("缺 GroupAccess 必须不适用")
			}
		})
	})

	t.Run("调度策略指针解引用", func(t *testing.T) {
		if derefGroupSchedulingPolicy(nil) != nil {
			t.Fatalf("nil 策略必须返回 nil map")
		}
		policy := gatewayruntimecache.GroupSchedulingPolicy{"maxQueueWaitMs": float64(500)}
		if derefGroupSchedulingPolicy(&policy)["maxQueueWaitMs"] != float64(500) {
			t.Fatalf("非 nil 策略必须原样返回")
		}
	})

	t.Run("队列策略投影", func(t *testing.T) {
		waitMs, queueSize, perKey, err := chainSpeedFirstQueuePolicy(nil, gatewayclientip.HighConcurrencyPolicyDefaults{})
		if err != nil {
			t.Fatalf("nil 策略: %v", err)
		}
		if waitMs != 60_000 || queueSize != 1 || perKey != 1 {
			t.Fatalf("默认策略 = %d/%d/%d, want 60000/1/1", waitMs, queueSize, perKey)
		}
		policy := gatewayruntimecache.GroupSchedulingPolicy{
			"maxQueueWaitMs":      float64(500),
			"maxQueueSize":        float64(3),
			"perApiKeyQueueLimit": float64(2),
		}
		waitMs, queueSize, perKey, err = chainSpeedFirstQueuePolicy(&policy, gatewayclientip.HighConcurrencyPolicyDefaults{})
		if err != nil || waitMs != 500 || queueSize != 3 || perKey != 2 {
			t.Fatalf("显式策略 = %d/%d/%d err %v", waitMs, queueSize, perKey, err)
		}
		partial := gatewayruntimecache.GroupSchedulingPolicy{
			"maxQueueWaitMs": float64(1000),
			"maxQueueSize":   float64(4),
		}
		waitMs, queueSize, perKey, err = chainSpeedFirstQueuePolicy(&partial, gatewayclientip.HighConcurrencyPolicyDefaults{})
		if err != nil || waitMs != 1000 || queueSize != 4 || perKey != 4 {
			t.Fatalf("缺省 perApiKeyQueueLimit = %d/%d/%d err %v, want 1000/4/4", waitMs, queueSize, perKey, err)
		}
		malformed := gatewayruntimecache.GroupSchedulingPolicy{"maxQueueWaitMs": float64(0)}
		if _, _, _, err := chainSpeedFirstQueuePolicy(&malformed, gatewayclientip.HighConcurrencyPolicyDefaults{}); err == nil {
			t.Fatalf("越界 maxQueueWaitMs 必须报错")
		}
	})

	t.Run("body 准入容量", func(t *testing.T) {
		source := "acc-src"
		cases := []struct {
			name     string
			accounts []gatewayruntimecache.OpenAIAccountSecret
			want     int
		}{
			{"空列表", nil, 0},
			{"单账户", []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1", ConcurrencyLimit: 5}}, 5},
			{"下限钳制到 1", []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1", ConcurrencyLimit: 0}}, 1},
			{"共享源取最小", []gatewayruntimecache.OpenAIAccountSecret{
				{ID: "acc-1", CredentialSourceAccountID: &source, ConcurrencyLimit: 5},
				{ID: "acc-2", CredentialSourceAccountID: &source, ConcurrencyLimit: 3},
			}, 3},
			{"不同账户求和", []gatewayruntimecache.OpenAIAccountSecret{
				{ID: "acc-1", ConcurrencyLimit: 2},
				{ID: "acc-2", ConcurrencyLimit: 4},
			}, 6},
			{"空 ID 跳过", []gatewayruntimecache.OpenAIAccountSecret{{ID: "  ", ConcurrencyLimit: 9}}, 0},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				if got := chainBodyAdmissionCapacity(testCase.accounts); got != testCase.want {
					t.Fatalf("capacity = %d, want %d", got, testCase.want)
				}
			})
		}
	})

	t.Run("content-length 解析", func(t *testing.T) {
		cases := []struct {
			name   string
			header string
			set    bool
			want   int
		}{
			{"正常值", "123", true, 123},
			{"缺省为 0", "", false, 0},
			{"非法为 0", "abc", true, 0},
			{"负数为 0", "-5", true, 0},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				if testCase.set {
					httpReq.Header.Set("Content-Length", testCase.header)
				}
				if got := chainContentLengthBytes(gatewaypreauth.NewGatewayRequest(httpReq)); got != testCase.want {
					t.Fatalf("contentLength = %d, want %d", got, testCase.want)
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// chain_preflight.go：模型快路径 raw-key 校验
// ---------------------------------------------------------------------------

func TestW1FAPIKeyValidatorValidate(t *testing.T) {
	fixture := newChainFixture(t)
	validator := &chainAPIKeyValidator{cache: fixture.cache}
	ctx := context.Background()

	row, err := validator.Validate(ctx, "")
	if err != nil || row != nil {
		t.Fatalf("空 apiKey 必须早退 nil/nil, got %+v err %v", row, err)
	}

	row, err = validator.Validate(ctx, fixture.apiKeySecret)
	if err != nil {
		t.Fatalf("校验合法 key: %v", err)
	}
	if row == nil || row.ID != "key_1" || row.SystemAccountID != fixture.systemAccount {
		t.Fatalf("合法 key 行 = %+v", row)
	}

	row, err = validator.Validate(ctx, "sk-w1f-bogus")
	if err != nil || row != nil {
		t.Fatalf("未知 key 必须回落 nil/nil, got %+v err %v", row, err)
	}
}

// ---------------------------------------------------------------------------
// chain_chat_tool_capabilities.go：协议判定与传输选择
// ---------------------------------------------------------------------------

func TestW1FChatToolAccountSupportsProtocolArms(t *testing.T) {
	enabled := true
	disabled := false
	model := "gpt-5.3"
	cases := []struct {
		name     string
		account  chat.ChatTransportAccount
		protocol chat.ChatTransportProtocol
		want     bool
	}{
		{
			name:     "messages 协议 arm",
			account:  chat.ChatTransportAccount{SupportedModels: []string{model}, SupportedEndpointModes: []string{"messages_sse"}},
			protocol: "messages",
			want:     true,
		},
		{
			name:     "generate_content 协议 arm",
			account:  chat.ChatTransportAccount{SupportedEndpointModes: []string{"generate_content_sse"}},
			protocol: "generate_content",
			want:     true,
		},
		{
			name:     "未知协议无 SSE 模式要求",
			account:  chat.ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}},
			protocol: "xml",
			want:     false,
		},
		{
			name:     "缺少必需 SSE 模式",
			account:  chat.ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}},
			protocol: chat.ProtocolResponses,
			want:     false,
		},
		{
			name: "SupportedModels 门拒绝未改写模型",
			account: chat.ChatTransportAccount{
				SupportedModels:        []string{"gpt-real-1"},
				SupportedEndpointModes: []string{"chat_sse"},
			},
			protocol: chat.ProtocolChatCompletions,
			want:     false,
		},
		{
			name: "UpstreamModel 改写后通过 SupportedModels 门",
			account: chat.ChatTransportAccount{
				SupportedModels:        []string{"gpt-real-1"},
				SupportedEndpointModes: []string{"chat_sse"},
				ModelMappings: []chat.ChatTransportModelMapping{
					{Enabled: &enabled, SourceModel: model, UpstreamModel: "gpt-real-1"},
				},
			},
			protocol: chat.ProtocolChatCompletions,
			want:     true,
		},
		{
			name: "停用映射跳过且原模型过门",
			account: chat.ChatTransportAccount{
				SupportedModels:        []string{model},
				SupportedEndpointModes: []string{"chat_sse"},
				ModelMappings: []chat.ChatTransportModelMapping{
					{Enabled: &disabled, SourceModel: model, UpstreamModel: "gpt-real-1"},
				},
			},
			protocol: chat.ProtocolChatCompletions,
			want:     true,
		},
		{
			name: "SourceEndpointFamily 不匹配的映射不选中",
			account: chat.ChatTransportAccount{
				SupportedModels:        []string{"gpt-real-1"},
				SupportedEndpointModes: []string{"responses_sse"},
				ModelMappings: []chat.ChatTransportModelMapping{
					{Enabled: &enabled, SourceModel: model, UpstreamModel: "gpt-real-1", SourceEndpointFamily: "responses"},
				},
			},
			protocol: chat.ProtocolChatCompletions,
			want:     false,
		},
		{
			name: "UpstreamEndpointFamily 改写上游协议",
			account: chat.ChatTransportAccount{
				SupportedModels:        []string{model},
				SupportedEndpointModes: []string{"responses_sse"},
				ModelMappings: []chat.ChatTransportModelMapping{
					{Enabled: &enabled, SourceModel: model, UpstreamEndpointFamily: "responses"},
				},
			},
			protocol: chat.ProtocolChatCompletions,
			want:     true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := chatToolAccountSupportsProtocol(testCase.account, model, testCase.protocol); got != testCase.want {
				t.Fatalf("support = %v, want %v (account=%+v protocol=%s)", got, testCase.want, testCase.account, testCase.protocol)
			}
		})
	}
}

func TestW1FChatToolSupportedProtocolsArms(t *testing.T) {
	chatOnly := toolCapsCatalog{
		accountsByGroup: map[string][]chat.ChatTransportAccount{
			"grp-a|gpt-5.3|chat_completions": {toolCapsAccount("acct-a", "api_key", "chat_sse")},
		},
	}
	responsesOnly := toolCapsCatalog{
		accountsByGroup: map[string][]chat.ChatTransportAccount{
			"grp-a|gpt-5.3|responses": {toolCapsAccount("acct-a", "oauth", "responses_sse")},
		},
	}
	both := toolCapsCatalog{
		accountsByGroup: map[string][]chat.ChatTransportAccount{
			"grp-a|gpt-5.3|chat_completions": {toolCapsAccount("acct-a", "api_key", "chat_sse")},
			"grp-a|gpt-5.3|responses":        {toolCapsAccount("acct-b", "oauth", "responses_sse")},
		},
	}
	none := toolCapsCatalog{accountsByGroup: map[string][]chat.ChatTransportAccount{}}

	cases := []struct {
		name string
		deps *chat.Deps
		want []chat.ChatTransportProtocol
	}{
		{"仅 chat_completions", toolCapsDeps(nil, nil, chatOnly), []chat.ChatTransportProtocol{chat.ProtocolChatCompletions}},
		{"仅 responses", toolCapsDeps(nil, nil, responsesOnly), []chat.ChatTransportProtocol{chat.ProtocolResponses}},
		{"双协议保持协议序", toolCapsDeps(nil, nil, both), []chat.ChatTransportProtocol{chat.ProtocolChatCompletions, chat.ProtocolResponses}},
		{"无路由", toolCapsDeps(nil, nil, none), []chat.ChatTransportProtocol{}},
		{"目录端口缺席", toolCapsDeps(nil, nil, nil), []chat.ChatTransportProtocol{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := chatToolSupportedProtocols(testCase.deps, []string{"grp-a", "grp-a", "  "}, "owner-1", "gpt-5.3")
			if len(got) != len(testCase.want) {
				t.Fatalf("protocols = %v, want %v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("protocols = %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

func TestW1FChatToolTransportSelection(t *testing.T) {
	cases := []struct {
		name      string
		protocols []chat.ChatTransportProtocol
		prefer    bool
		want      chat.ChatTransportProtocol
	}{
		{"偏好 responses 且可用", []chat.ChatTransportProtocol{chat.ProtocolChatCompletions, chat.ProtocolResponses}, true, chat.ProtocolResponses},
		{"偏好缺失时回落 chat", []chat.ChatTransportProtocol{chat.ProtocolChatCompletions}, true, chat.ProtocolChatCompletions},
		{"无偏好优先 chat", []chat.ChatTransportProtocol{chat.ProtocolResponses, chat.ProtocolChatCompletions}, false, chat.ProtocolChatCompletions},
		{"仅 responses", []chat.ChatTransportProtocol{chat.ProtocolResponses}, false, chat.ProtocolResponses},
		{"空列表回落 chat", nil, false, chat.ProtocolChatCompletions},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := chatToolSelectTransport(testCase.protocols, testCase.prefer); got != testCase.want {
				t.Fatalf("transport = %s, want %s", got, testCase.want)
			}
		})
	}

	t.Run("图像生成路由判定", func(t *testing.T) {
		catalog, _ := toolCapsFixture()
		if chatToolHasImageGenerationRoute(nil, []string{"grp-image"}, "owner-1") {
			t.Fatalf("nil deps 必须返回 false")
		}
		if chatToolHasImageGenerationRoute(toolCapsDeps(nil, nil, nil), []string{"grp-image"}, "owner-1") {
			t.Fatalf("nil 目录必须返回 false")
		}
		if !chatToolHasImageGenerationRoute(toolCapsDeps(nil, nil, catalog), []string{"grp-image", "grp-image"}, "owner-1") {
			t.Fatalf("api_key 账户必须命中图像路由")
		}
		raw := catalog.(toolCapsCatalog)
		raw.accountsByGroup["grp-image|gpt-image-2|"] = []chat.ChatTransportAccount{toolCapsAccount("acct-image", "oauth", "chat_sse")}
		if chatToolHasImageGenerationRoute(toolCapsDeps(nil, nil, raw), []string{"grp-image"}, "owner-1") {
			t.Fatalf("纯 oauth 账户不得命中图像路由")
		}
	})
}

// ---------------------------------------------------------------------------
// chain_chat_tool_capabilities.go：字符串与列表交集辅助
// ---------------------------------------------------------------------------

func TestW1FChatToolStringHelpers(t *testing.T) {
	unique := chatToolUniqueStrings([]string{" a", "a", "", "  ", "b ", "a"})
	if len(unique) != 2 || unique[0] != "a" || unique[1] != "b" {
		t.Fatalf("去重结果 = %+v, want [a b]", unique)
	}
	if !chatToolContains([]string{"web_search", "x"}, "web_search") {
		t.Fatalf("包含判定失败")
	}
	if chatToolContains([]string{"web_search"}, "function_calling") {
		t.Fatalf("缺失项不得命中")
	}

	cases := []struct {
		name  string
		lists [][]string
		want  []string
	}{
		{"空输入", nil, []string{}},
		{"单列表首见去重", [][]string{{"a", "b", "a"}}, []string{"a", "b"}},
		{"双列表交集", [][]string{{"a", "b", "c"}, {"b", "c", "d"}}, []string{"b", "c"}},
		{"无交集", [][]string{{"a"}, {"b"}}, []string{}},
		{"三列表交集", [][]string{{"a", "b"}, {"a", "b"}, {"a"}}, []string{"a"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := chatToolIntersectLists(testCase.lists)
			if len(got) != len(testCase.want) {
				t.Fatalf("交集 = %+v, want %+v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("交集 = %+v, want %+v", got, testCase.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_chat_tool_capabilities.go：载荷构造与目录快照
// ---------------------------------------------------------------------------

func TestW1FChatToolPayloadBuilders(t *testing.T) {
	t.Run("tool entry 条件展开", func(t *testing.T) {
		available := chatToolEntry("web_search", "网页搜索", true, "")
		if available["available"] != true {
			t.Fatalf("可用条目 = %+v", available)
		}
		if _, has := available["reason"]; has {
			t.Fatalf("可用条目不得携带 reason: %+v", available)
		}
		unavailable := chatToolEntry("generate_image", "图片生成", false, "当前用户未开启图片生成")
		if unavailable["available"] != false || unavailable["reason"] != "当前用户未开启图片生成" {
			t.Fatalf("不可用条目 = %+v", unavailable)
		}
		silent := chatToolEntry("generate_image", "图片生成", false, "")
		if _, has := silent["reason"]; has {
			t.Fatalf("空 reason 不得写入: %+v", silent)
		}
	})

	t.Run("payload 形状", func(t *testing.T) {
		payload := chatToolCapabilitiesPayload("gpt-5.3", chatToolEntry("web_search", "网页搜索", true, "")).(map[string]any)
		if payload["model"] != "gpt-5.3" {
			t.Fatalf("model = %v", payload["model"])
		}
		tools, ok := payload["tools"].([]map[string]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("tools = %+v", payload["tools"])
		}
		empty := chatToolCapabilitiesPayload(nil).(map[string]any)
		if empty["model"] != nil {
			t.Fatalf("nil model = %v", empty["model"])
		}
	})

	t.Run("模型选项能力交集", func(t *testing.T) {
		option := chatToolModelOption("gpt-5.3", []chat.ProviderModelCatalogItem{
			{Model: "gpt-5.3", SupportedTools: []string{"web_search", "function_calling"}},
			{Model: "gpt-image-2", SupportedTools: []string{"web_search"}},
			{Model: "gpt-5.3", SupportedTools: []string{"web_search", "code_interpreter"}},
		})
		if len(option.supportedTools) != 1 || option.supportedTools[0] != "web_search" {
			t.Fatalf("supportedTools = %+v, want [web_search]", option.supportedTools)
		}
		missing := chatToolModelOption("gpt-missing", []chat.ProviderModelCatalogItem{{Model: "gpt-5.3"}})
		if len(missing.supportedTools) != 0 {
			t.Fatalf("无目录行必须为空列表: %+v", missing.supportedTools)
		}
	})

	t.Run("目录快照首见 provider 序", func(t *testing.T) {
		if accounts, catalog := chatToolCatalogSnapshot(nil, []string{"grp"}, "owner", "m"); accounts != nil || catalog != nil {
			t.Fatalf("nil deps 必须返回 nil: %+v %+v", accounts, catalog)
		}
		if accounts, catalog := chatToolCatalogSnapshot(toolCapsDeps(nil, nil, nil), []string{"grp"}, "owner", "m"); accounts != nil || catalog != nil {
			t.Fatalf("nil 目录必须返回 nil: %+v %+v", accounts, catalog)
		}
		fake := toolCapsCatalog{
			accountsByGroup: map[string][]chat.ChatTransportAccount{
				"grp-a|m|": {
					{ID: "acc-1", ProviderCode: "OpenAI"},
					{ID: "acc-no-provider"},
					{ID: "acc-2", ProviderCode: "gpt"},
					{ID: "acc-3", ProviderCode: "openai"},
				},
			},
			catalogByCode: map[string][]chat.ProviderModelCatalogItem{
				"openai": {{Model: "m", ProviderCode: "openai"}},
				"gpt":    {{Model: "m", ProviderCode: "gpt"}},
			},
		}
		accounts, catalog := chatToolCatalogSnapshot(toolCapsDeps(nil, nil, fake), []string{"grp-a", "grp-a", " "}, "owner", "m")
		if len(accounts) != 4 {
			t.Fatalf("账户扇出 = %d, want 4", len(accounts))
		}
		if len(catalog) != 2 || catalog[0].ProviderCode != "openai" || catalog[1].ProviderCode != "gpt" {
			t.Fatalf("provider 目录 = %+v, want 首见序 openai→gpt", catalog)
		}
	})
}
