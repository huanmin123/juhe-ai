package gatewaypreauth

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func whFullDispatchCandidates(account gatewayruntimecache.OpenAIAccountSecret) *fakeCandidates {
	return &fakeCandidates{
		filterResult: CandidateFilterResult{
			Outcome:  CandidateOutcomeAccounts,
			Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
		},
		preparation: DispatchPreparationResult{
			Outcome:                    CandidateOutcomeAccounts,
			Accounts:                   []gatewayruntimecache.OpenAIAccountSecret{account},
			ReleaseClientIPConcurrency: func() {},
		},
	}
}

// Gemini Interaction 账号亲和：命中亲和时请求锁定到原账号与原分组。
func TestWhPreflightInteractionAffinityFound(t *testing.T) {
	// 亲和绑定的有效性要求 ProviderCode 与 gemini 协议码一致。
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_aff", ProviderCode: "gemini"}
	affinity := gatewaygemini.NewInteractionAffinity(nil)
	// 预绑定 interaction → 账号。
	if _, err := affinity.Remember(context.Background(), "ia-1",
		gatewaygemini.UpstreamAccount{ID: "acc_aff", ProviderCode: "gemini"},
		gatewaygemini.AffinityScope{SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1"},
	); err != nil {
		t.Fatal(err)
	}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = whFullDispatchCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
		s.Affinity = affinity
	})
	audit := &fakeAuditCapture{}
	req, _, writer := newTestRequest("GET", "/v1beta/interactions/ia-1")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: audit,
		Options:   plainPreflightOptions(),
		StartedAt: 1, TraceID: "trace", Endpoint: "GET /v1beta/interactions/ia-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchContext == nil {
		t.Fatalf("亲和命中必须继续派发: %+v", result)
	}
	if result.DispatchContext.InteractionResourceAffinity == nil {
		t.Fatal("亲和绑定必须进入派发上下文")
	}
	if len(audit.metadata) == 0 {
		t.Fatal("缺少亲和审计元数据")
	}
}

// 亲和未命中：404 就地失败。
func TestWhPreflightInteractionAffinityNotFound(t *testing.T) {
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
		s.Affinity = gatewaygemini.NewInteractionAffinity(nil)
	})
	req, _, writer := newTestRequest("GET", "/v1beta/interactions/ia-missing")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options:   plainPreflightOptions(),
		StartedAt: 1, TraceID: "trace", Endpoint: "GET /v1beta/interactions/ia-missing",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 404 || failure.Audit.ErrorCode != "interaction_affinity_not_found" {
		t.Fatalf("亲和未命中失败 = %+v", failure)
	}
}

// 亲和分组不再绑定当前 API Key：404 拒绝。
func TestWhPreflightInteractionAffinityGroupUnbound(t *testing.T) {
	affinity := gatewaygemini.NewInteractionAffinity(nil)
	if _, err := affinity.Remember(context.Background(), "ia-2",
		gatewaygemini.UpstreamAccount{ID: "acc_aff", ProviderCode: "gemini"},
		gatewaygemini.AffinityScope{SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "g-other"},
	); err != nil {
		t.Fatal(err)
	}
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
		s.Affinity = affinity
	})
	req, _, writer := newTestRequest("GET", "/v1beta/interactions/ia-2")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options:   plainPreflightOptions(),
		StartedAt: 1, TraceID: "trace", Endpoint: "GET /v1beta/interactions/ia-2",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 404 {
		t.Fatalf("分组解绑失败 = %+v", failure)
	}
}

// 亲和账号不可用：409 拒绝。
func TestWhPreflightInteractionAffinityAccountUnavailable(t *testing.T) {
	affinity := gatewaygemini.NewInteractionAffinity(nil)
	if _, err := affinity.Remember(context.Background(), "ia-3",
		gatewaygemini.UpstreamAccount{ID: "acc_gone", ProviderCode: "gemini"},
		gatewaygemini.AffinityScope{SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1"},
	); err != nil {
		t.Fatal(err)
	}
	service, _, sink := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{{ID: "acc_other", ProviderCode: "gemini"}})
		s.Affinity = affinity
	})
	req, _, writer := newTestRequest("GET", "/v1beta/interactions/ia-3")
	result, err := service.PrepareOpenAIGatewayDispatchContext(context.Background(), PreflightInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options:   plainPreflightOptions(),
		StartedAt: 1, TraceID: "trace", Endpoint: "GET /v1beta/interactions/ia-3",
	})
	if err != nil || result.DispatchContext != nil {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	failure, ok := sink.lastFailure()
	if !ok || failure.StatusCode != 409 || failure.Audit.ErrorCode != "interaction_affinity_account_unavailable" {
		t.Fatalf("账号不可用失败 = %+v", failure)
	}
}

func IDAndProvider(id string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{ID: id, ProviderCode: "openai"}
}

// API Key 分组回退派发上下文：候选命中时切换分组并返回完整预检结果。
func TestWhPrepareAPIKeyGroupFallbackDispatchFound(t *testing.T) {
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_fb", ProviderCode: "openai"}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor([]gatewayruntimecache.OpenAIAccountSecret{account})
		s.Candidates = &fakeCandidates{
			filterResult: CandidateFilterResult{
				Outcome:  CandidateOutcomeAccounts,
				Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
			},
			preparation: DispatchPreparationResult{
				Outcome:                    CandidateOutcomeAccounts,
				Accounts:                   []gatewayruntimecache.OpenAIAccountSecret{account},
				ReleaseClientIPConcurrency: func() {},
			},
			fallbackCandidate: &GroupFallbackCandidate{
				GroupID:  "g2",
				Accounts: []gatewayruntimecache.OpenAIAccountSecret{account},
			},
			fallbackFound: true,
		}
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})
	row := validRuntimeRow()
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	result, err := service.PrepareAPIKeyGroupFallbackDispatchContext(context.Background(), APIKeyGroupFallbackDispatchInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options: plainPreflightOptions(), StartedAt: 1, TraceID: "trace",
		ClientIP: "1.2.3.4", Endpoint: "POST /v1/chat/completions",
		Reason: "no_candidate_accounts", APIKeyRecord: row,
		SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "group_1",
		TrafficSource: TrafficSourceGateway, RequestLane: gatewayproto.LaneText,
		RoutePlanSnapshot: gatewayrouting.RoutePlanSnapshot[string]{
			Cursor: 0, OrderedAllowedTargets: []string{"group_1", "g2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Attempted || result.Context.DispatchContext == nil {
		t.Fatalf("回退候选必须产出派发上下文: attempted=%v", result.Attempted)
	}
	if result.Context.DispatchContext.UsageContext.GroupID != "g2" {
		t.Fatalf("回退分组 = %q", result.Context.DispatchContext.UsageContext.GroupID)
	}

	// 候选缺失：尝试但未找到 → Attempted=false。
	service2, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = whRuntimeCacheFor(nil)
	})
	result2, err := service2.PrepareAPIKeyGroupFallbackDispatchContext(context.Background(), APIKeyGroupFallbackDispatchInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options: plainPreflightOptions(), APIKeyRecord: row, GroupID: "group_1",
		RoutePlanSnapshot: gatewayrouting.RoutePlanSnapshot[string]{
			Cursor: 0, OrderedAllowedTargets: []string{"group_1", "g2"},
		},
	})
	if err != nil || result2.Attempted {
		t.Fatalf("无候选回退 = %+v err=%v", result2, err)
	}
	// 游标已到末位：不可尝试。
	result3, err := service2.PrepareAPIKeyGroupFallbackDispatchContext(context.Background(), APIKeyGroupFallbackDispatchInput{
		Req: req, Res: writer, AuditCapture: &fakeAuditCapture{},
		Options: plainPreflightOptions(), APIKeyRecord: row, GroupID: "group_1",
		RoutePlanSnapshot: gatewayrouting.RoutePlanSnapshot[string]{
			Cursor: 1, OrderedAllowedTargets: []string{"group_1", "g2"},
		},
	})
	if err != nil || result3.Attempted {
		t.Fatalf("末位游标回退 = %+v err=%v", result3, err)
	}
}

// whCallbackRecoverable 驱动等待回调（刷新/就绪/重试时间）后立即返回。
type whCallbackRecoverable struct{ waited []string }

func (f *whCallbackRecoverable) WaitForRecoverableUnavailableState(ctx context.Context, input RecoverableWaitInput) error {
	f.waited = append(f.waited, input.ScopeKey)
	_, _ = input.NextRetryAfterMs(ctx)
	_ = input.Refresh(ctx)
	_ = input.IsReady(ctx)
	return nil
}

// 可恢复等待回调闭包的直读路径。
func TestWhWaitForRecoverableCallbacks(t *testing.T) {
	cooled := gatewayruntimecache.OpenAIAccountSecret{
		ID: "cool", ProviderCode: "openai",
		CooldownUntil: whMillisecondsToISO(1_700_000_003_000),
	}
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = &whRuntimeCacheWithRecoverable{
			fakeRuntimeCache: whRuntimeCacheFor(nil),
			recoverable:      []gatewayruntimecache.OpenAIAccountSecret{cooled},
		}
	})
	probe := &whCallbackRecoverable{}
	service.Recoverable = probe
	input := recoveryInput{
		req: whNewRequest("POST", "/v1/chat/completions"), auditCapture: &fakeAuditCapture{},
		systemAccountID: "sys_1", apiKeyID: "key_1", groupID: "group_1",
		serverRetryBudget: NewServerRetryBudget(5_000, newFakeClock(1_700_000_000_000)),
	}
	if _, err := service.waitForRecoverableOpenAIGatewayCandidateAccounts(input); err != nil {
		t.Fatal(err)
	}
	if len(probe.waited) != 1 {
		t.Fatalf("等待未被触发: %v", probe.waited)
	}
	// 坏冷却时间戳：重试回调显式失败路径（返回 0,false）。
	broken := []gatewayruntimecache.OpenAIAccountSecret{{ID: "x", CooldownUntil: whStringPtr("bad")}}
	if _, ok := nextRecoverableAccountRetryAfterMs(broken, 1); ok {
		t.Fatal("坏时间戳必须失败")
	}
}

func whMillisecondsToISO(ms int64) *string {
	value := time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return &value
}
