package gatewaydispatch

// 切号冻结目标（SwitchTarget）过滤语义与消费点接线测试。
// 契约锚点：docs/functions/切号时有效上游目标与上下文迁移设计.md 第 4/6/9 节。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// 测试数据构造
// ---------------------------------------------------------------------------

func newTestResponsesRequest(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	raw.Header.Set("Content-Type", "application/json")
	request := gatewaypreauth.NewGatewayRequest(raw)
	if body == "" {
		body = `{"model":"gpt-5","stream":true}`
	}
	request.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		Body:    mustJSONObject(t, body),
		State: &gatewaybody.BodyState{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
		},
	}
	return request
}

func mappingOf(sourceModel, sourceFamily, upstreamModel, upstreamFamily string) gatewayruntimecache.AccountModelMapping {
	return gatewayruntimecache.AccountModelMapping{
		SourceModel:            sourceModel,
		SourceEndpointFamily:   sourceFamily,
		UpstreamModel:          upstreamModel,
		UpstreamEndpointFamily: upstreamFamily,
		Enabled:                true,
	}
}

func accountWithMappings(id, providerCode string, supportedModels []string, modes []string, mappings []gatewayruntimecache.AccountModelMapping) AccountCandidate {
	return AccountCandidate{
		ID:                        id,
		ProviderCode:              providerCode,
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		SupportedModels:           supportedModels,
		SupportedEndpointModes:    modes,
		ModelMappings:             mappings,
		ProviderProtocolProfileID: "profile-test",
	}
}

// frozenResponsesTarget 是原生 Responses 冻结目标（GPT 供应商）。
func frozenResponsesTarget() SwitchTarget {
	return SwitchTarget{
		ProviderCode:           "openai",
		UpstreamModel:          "gpt-5",
		UpstreamEndpointFamily: "responses",
		UpstreamEndpointMode:   "responses_sse",
	}
}

// frozenBridgedChatTarget 是 Responses 初始 mapping 桥接后的 Chat 冻结目标。
func frozenBridgedChatTarget() SwitchTarget {
	return SwitchTarget{
		ProviderCode:           "openai",
		UpstreamModel:          "gpt-5-chat",
		UpstreamEndpointFamily: "chat_completions",
		UpstreamEndpointMode:   "chat_sse",
	}
}

// freezeOf 把目标与其构造侧客户端上下文一起冻结（过滤输入与冻结同源）。
func freezeOf(capture *SwitchTargetCapture, sourceAccountID string, target SwitchTarget, clientModel, clientFamily string) {
	copied := target
	capture.Freeze(SwitchTargetFreezeInput{
		SourceAccountID:            sourceAccountID,
		Target:                     &copied,
		ClientRequestedModel:       clientModel,
		ClientSourceEndpointFamily: clientFamily,
	})
}

func filterInputFor(target SwitchTarget, clientModel, clientFamily string) SwitchTargetFilterInput {
	return SwitchTargetFilterInput{
		Target:                     target,
		ClientRequestedModel:       clientModel,
		ClientSourceEndpointFamily: clientFamily,
	}
}

// ---------------------------------------------------------------------------
// 纯函数：FilterAccountsForSwitchTarget 三门语义（验收矩阵 §9 对应用例）
// ---------------------------------------------------------------------------

func TestFilterAccountsForSwitchTarget(t *testing.T) {
	tests := []struct {
		name         string
		target       SwitchTarget
		clientModel  string
		clientFamily string
		candidate    AccountCandidate
		expectKept   bool
		expectReason string
	}{
		{
			// 原生 Responses 目标：同供应商直连候选（直接持有模型）保留。
			name:         "native_responses_same_provider_direct_kept",
			target:       frozenResponsesTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate:    accountWithMappings("cand", "openai", []string{"GPT-5"}, nil, nil),
			expectKept:   true,
		},
		{
			// 原生 Responses 目标：异供应商候选剔除（供应商门）。
			name:         "native_responses_other_provider_dropped",
			target:       frozenResponsesTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate:    accountWithMappings("cand", "glm", []string{"gpt-5"}, nil, nil),
			expectKept:   false,
			expectReason: "provider gate",
		},
		{
			// 原生 Responses 目标：chat 别名 RHS 剔除（承接门：别名族不同）。
			name:         "native_responses_chat_alias_dropped",
			target:       frozenResponsesTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate: accountWithMappings("cand", "openai", []string{"gpt-5-chat"}, nil,
				[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-chat", "chat_completions")}),
			expectKept:   false,
			expectReason: "alias family gate",
		},
		{
			// 原生 Responses 目标：同供应商别名 RHS（模型+族）与冻结目标一致
			// 时保留（冻结源本身经同一别名冻结到该 RHS）。
			name: "native_responses_same_family_alias_kept",
			target: SwitchTarget{
				ProviderCode:           "openai",
				UpstreamModel:          "gpt-5-upstream",
				UpstreamEndpointFamily: "responses",
				UpstreamEndpointMode:   "responses_sse",
			},
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate: accountWithMappings("cand", "openai", []string{"gpt-5-upstream"}, nil,
				[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-upstream", "responses")}),
			expectKept: true,
		},
		{
			// 桥接 Chat 目标：RHS 匹配的跨供应商候选保留（Chat 允许跨供应商）。
			name:         "bridged_chat_cross_provider_alias_kept",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate: accountWithMappings("cand", "glm", []string{"gpt-5-chat"}, nil,
				[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-chat", "chat_completions")}),
			expectKept: true,
		},
		{
			// 桥接 Chat 目标：仅支持 responses 形态的候选剔除（承接门直连分支：
			// 目标族 != 客户端族）。
			name:         "bridged_chat_responses_only_direct_dropped",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate:    accountWithMappings("cand", "openai", []string{"gpt-5"}, nil, nil),
			expectKept:   false,
			expectReason: "direct native-shape gate",
		},
		{
			// 桥接 Chat 目标：RHS 模型不匹配剔除。
			name:         "bridged_chat_alias_model_mismatch_dropped",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate: accountWithMappings("cand", "openai", []string{"gpt-5-mini"}, nil,
				[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-mini", "chat_completions")}),
			expectKept:   false,
			expectReason: "alias model gate",
		},
		{
			// mode 不匹配剔除（声明了 modes 但缺 chat_sse）。
			name:         "mode_mismatch_dropped",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate: accountWithMappings("cand", "glm", []string{"gpt-5-chat"}, []string{"chat_json"},
				[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-chat", "chat_completions")}),
			expectKept:   false,
			expectReason: "mode gate",
		},
		{
			// 空 modes 候选不受限（既有 D-155 语义不改变）。
			name:         "empty_modes_unrestricted",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate: accountWithMappings("cand", "glm", []string{"gpt-5-chat"}, []string{},
				[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-chat", "chat_completions")}),
			expectKept: true,
		},
		{
			// 无别名候选在原生目标 + 模型直持时保留（EqualFold）。
			name:         "native_target_direct_model_equalfold_kept",
			target:       frozenResponsesTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate:    accountWithMappings("cand", "openai", []string{"GPT-5"}, []string{"responses_sse"}, nil),
			expectKept:   true,
		},
		{
			// 无别名候选在桥接目标时剔除（直连分支族不等）。
			name:         "bridged_target_direct_dropped",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "responses",
			candidate:    accountWithMappings("cand", "openai", []string{"gpt-5-chat"}, nil, nil),
			expectKept:   false,
			expectReason: "direct native-shape gate",
		},
		{
			// 桥接目标 + 直持模型的 Chat 账户（族等、模型直持）保留。
			name:         "bridged_target_native_chat_kept",
			target:       frozenBridgedChatTarget(),
			clientModel:  "gpt-5",
			clientFamily: "chat_completions",
			candidate:    accountWithMappings("cand", "glm", []string{"GPT-5-Chat"}, []string{"chat_sse"}, nil),
			expectKept:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept := FilterAccountsForSwitchTarget([]AccountCandidate{tt.candidate}, filterInputFor(tt.target, tt.clientModel, tt.clientFamily))
			if tt.expectKept && len(kept) != 1 {
				t.Fatalf("候选应保留（%s），实际被剔除", tt.expectReason)
			}
			if !tt.expectKept && len(kept) != 0 {
				t.Fatalf("候选应剔除（%s），实际被保留", tt.expectReason)
			}
		})
	}
}

// TestSwitchTargetGateExemptsSourceAccount：冻结源账户不是切换点（同账户
// Key 轮换 / 同账户重试语义不变），即使它不再满足过滤条件也保留。
func TestSwitchTargetGateExemptsSourceAccount(t *testing.T) {
	capture := &SwitchTargetCapture{}
	freezeOf(capture, "src-1", frozenResponsesTarget(), "gpt-5", "responses")
	gate := &SwitchTargetGate{capture: capture}
	accounts := []AccountCandidate{
		accountWithMappings("src-1", "glm", []string{"other"}, nil, nil), // 冻结源，异供应商：豁免保留
		accountWithMappings("other-1", "glm", []string{"gpt-5"}, nil, nil),
	}
	kept := gate.FilterAccounts(accounts)
	if len(kept) != 1 || kept[0].ID != "src-1" {
		t.Fatalf("仅冻结源应保留，实际 = %#v", accountIDs(kept))
	}
}

// TestSwitchTargetCaptureFreezeOnce：每请求只捕获一次，不随后续尝试重置；
// 过滤输入与冻结同源。
func TestSwitchTargetCaptureFreezeOnce(t *testing.T) {
	capture := &SwitchTargetCapture{}
	capture.Freeze(SwitchTargetFreezeInput{
		SourceAccountID:            "a-1",
		Target:                     &SwitchTarget{ProviderCode: "openai", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "responses", UpstreamEndpointMode: "responses_sse"},
		ClientRequestedModel:       "gpt-5",
		ClientSourceEndpointFamily: "responses",
	})
	capture.Freeze(SwitchTargetFreezeInput{
		SourceAccountID: "a-2",
		Target:          &SwitchTarget{ProviderCode: "glm", UpstreamModel: "glm-x", UpstreamEndpointFamily: "responses"},
	})
	snapshot := capture.Snapshot()
	if !snapshot.Frozen || snapshot.SourceAccountID != "a-1" || snapshot.Target.UpstreamModel != "gpt-5" {
		t.Fatalf("首次冻结应生效且不被覆盖：snapshot=%#v", snapshot)
	}
	if snapshot.ClientRequestedModel != "gpt-5" || snapshot.ClientSourceEndpointFamily != "responses" {
		t.Fatalf("构造侧过滤输入应随冻结记录：%#v", snapshot)
	}
	if !capture.MarkUnresolvedDiagnosed() {
		t.Fatal("首次未解析诊断应触发")
	}
	if capture.MarkUnresolvedDiagnosed() {
		t.Fatal("未解析诊断每请求只触发一次")
	}
}

// TestSwitchTargetCaptureConcurrentFreeze：并发 Freeze / Snapshot / 诊断标记
// 的 race 安全性与 freeze-once 语义（go test -race 下运行）。
func TestSwitchTargetCaptureConcurrentFreeze(t *testing.T) {
	capture := &SwitchTargetCapture{}
	const goroutines = 32
	diagnosedCount := atomic.Int64{}
	var wg sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			capture.Freeze(SwitchTargetFreezeInput{
				SourceAccountID:            "acc-" + string(rune('a'+index%26)),
				Target:                     &SwitchTarget{ProviderCode: "openai", UpstreamModel: "gpt-5", UpstreamEndpointFamily: "responses"},
				ClientRequestedModel:       "gpt-5",
				ClientSourceEndpointFamily: "responses",
			})
			snapshot := capture.Snapshot()
			if snapshot.Frozen && snapshot.SourceAccountID == "" {
				t.Error("冻结后冻结源不得为空")
			}
			if capture.MarkUnresolvedDiagnosed() {
				diagnosedCount.Add(1)
			}
		}(index)
	}
	wg.Wait()
	snapshot := capture.Snapshot()
	if !snapshot.Frozen {
		t.Fatal("并发冻结后应有且仅有一次冻结生效")
	}
	if snapshot.Target == nil || snapshot.Target.UpstreamModel != "gpt-5" {
		t.Fatalf("冻结目标应保持首次值：%#v", snapshot.Target)
	}
	if diagnosedCount.Load() > 1 {
		t.Fatalf("未解析诊断并发下至多触发一次，实际 = %d", diagnosedCount.Load())
	}
	// 并发结束后再冻结不得覆盖首次值。
	before := snapshot.SourceAccountID
	capture.Freeze(SwitchTargetFreezeInput{SourceAccountID: "late", Target: &SwitchTarget{ProviderCode: "glm", UpstreamModel: "x", UpstreamEndpointFamily: "responses"}})
	if capture.Snapshot().SourceAccountID != before {
		t.Fatal("后续冻结不得覆盖首次冻结")
	}
}

// ---------------------------------------------------------------------------
// 消费点接线：引擎侧账户推进
// ---------------------------------------------------------------------------

// switchTargetFreezingDriver 包装 fakeDriver：构造成功后按账户冻结指定目标，
// 镜像 cmd 层 chainProviderDriver.BuildGatewayUpstreamRequestParts 的冻结
// 行为。
type switchTargetFreezingDriver struct {
	*fakeDriver
	freezeFor map[string]SwitchTarget
}

func (f *switchTargetFreezingDriver) BuildGatewayUpstreamRequestParts(ctx context.Context, req *gatewaypreauth.GatewayRequest, account AccountCandidate, identity UsageIdentity, requestClientCompatibility string) (PreparedRequestParts, error) {
	parts, err := f.fakeDriver.BuildGatewayUpstreamRequestParts(ctx, req, account, identity, requestClientCompatibility)
	if err == nil {
		if target, ok := f.freezeFor[account.ID]; ok {
			copied := target
			SwitchTargetCaptureFromContext(ctx).Freeze(SwitchTargetFreezeInput{
				SourceAccountID:            account.ID,
				Target:                     &copied,
				ClientRequestedModel:       "gpt-5",
				ClientSourceEndpointFamily: "responses",
			})
		}
	}
	return parts, err
}

func statusServer(t *testing.T, status int, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
}

func newSwitchTargetDispatchArgs(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, audit *frozenAudit) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	args := dispatchArgs(t, req, accounts)
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	return args
}

// TestEngineAdvancesOnlySwitchTargetServingAccounts：a-1 完成构造并冻结
// Responses 目标后，引擎推进到异供应商 a-2 前被过滤（a-2 上游零请求）。
func TestEngineAdvancesOnlySwitchTargetServingAccounts(t *testing.T) {
	hitsA := &atomic.Int64{}
	hitsB := &atomic.Int64{}
	serverA := statusServer(t, http.StatusInternalServerError, hitsA)
	defer serverA.Close()
	serverB := statusServer(t, http.StatusOK, hitsB)
	defer serverB.Close()

	engine, driver, _ := newTestEngine(t)
	wrapped := &switchTargetFreezingDriver{fakeDriver: driver}
	wrapped.urlByAccount = map[string][]string{
		"a-1": {serverA.URL + "/v1/responses"},
		"a-2": {serverB.URL + "/v1/responses"},
	}
	wrapped.freezeFor = map[string]SwitchTarget{"a-1": frozenResponsesTarget()}
	engine.Driver = wrapped

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	a1 := accountWithMappings("a-1", "openai", []string{"gpt-5"}, nil, nil)
	a2 := accountWithMappings("a-2", "glm", []string{"gpt-5"}, nil, nil)

	capture := &SwitchTargetCapture{}
	ctx := WithSwitchTargetCapture(context.Background(), capture)
	audit := &frozenAudit{sink: &fakeAuditSink{}}
	_, err := engine.FetchFirstAvailableUpstream(ctx, newSwitchTargetDispatchArgs(t, req, []AccountCandidate{a1, a2}, audit))
	var attemptErr *UpstreamAttemptError
	if !asUpstreamAttemptError(err, &attemptErr) {
		t.Fatalf("应返回既有 UpstreamAttemptError 最终失败，实际 = %v", err)
	}
	// a-1 的 500 属 transient 同账户重试语义（1 次原始 + 最多 2 次同账户
	// 重试，既有行为不变——同账户重试不是切换点，不受冻结目标过滤影响）。
	if hitsA.Load() != 3 {
		t.Fatalf("冻结源 a-1 应走既有同账户重试语义（3 次），实际 = %d", hitsA.Load())
	}
	if hitsB.Load() != 0 {
		t.Fatalf("不承接冻结目标的 a-2 不应发出上游请求，实际 = %d", hitsB.Load())
	}
	for _, label := range audit.metadata {
		if label == "switch_target_unresolved" {
			t.Fatal("可解析目标不应输出 switch_target_unresolved")
		}
	}
}

// TestEngineWithoutFrozenSwitchTargetKeepsInitialSemantics：无冻结目标（初始
// 候选选择）过滤完全不参与——a-2 正常接管成功。
func TestEngineWithoutFrozenSwitchTargetKeepsInitialSemantics(t *testing.T) {
	hitsA := &atomic.Int64{}
	hitsB := &atomic.Int64{}
	serverA := statusServer(t, http.StatusInternalServerError, hitsA)
	defer serverA.Close()
	serverB := statusServer(t, http.StatusOK, hitsB)
	defer serverB.Close()

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {serverA.URL + "/v1/responses"},
		"a-2": {serverB.URL + "/v1/responses"},
	}
	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	a1 := accountWithMappings("a-1", "openai", []string{"gpt-5"}, nil, nil)
	a2 := accountWithMappings("a-2", "glm", []string{"gpt-5"}, nil, nil)

	// ctx 注入载体但本请求无账户冻结目标前不过滤；构造后也无 freezeFor，
	// 故窗口保持初始语义。
	ctx := WithSwitchTargetCapture(context.Background(), &SwitchTargetCapture{})
	result, err := engine.FetchFirstAvailableUpstream(ctx, newSwitchTargetDispatchArgs(t, req, []AccountCandidate{a1, a2}, &frozenAudit{sink: &fakeAuditSink{}}))
	if err != nil {
		t.Fatalf("a-2 应接管成功，实际错误 = %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("应由 a-2 接管，实际 = %s", result.Account.ID)
	}
	if hitsB.Load() != 1 {
		t.Fatalf("a-2 应发出一次上游请求，实际 = %d", hitsB.Load())
	}
}

// TestEngineFailClosedOnUnresolvedSwitchTarget：冻结目标不可解析（不变量
// 违规）时 fail-closed——跨账户候选全部被拒并输出一次 switch_target_unresolved。
func TestEngineFailClosedOnUnresolvedSwitchTarget(t *testing.T) {
	hitsA := &atomic.Int64{}
	hitsB := &atomic.Int64{}
	serverA := statusServer(t, http.StatusInternalServerError, hitsA)
	defer serverA.Close()
	serverB := statusServer(t, http.StatusOK, hitsB)
	defer serverB.Close()

	engine, driver, _ := newTestEngine(t)
	wrapped := &switchTargetFreezingDriver{fakeDriver: driver}
	wrapped.urlByAccount = map[string][]string{
		"a-1": {serverA.URL + "/v1/responses"},
		"a-2": {serverB.URL + "/v1/responses"},
	}
	// 冻结了目标但字段不可解析：空 family / model。
	wrapped.freezeFor = map[string]SwitchTarget{"a-1": {}}
	engine.Driver = wrapped

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	a1 := accountWithMappings("a-1", "openai", []string{"gpt-5"}, nil, nil)
	a2 := accountWithMappings("a-2", "openai", []string{"gpt-5"}, nil, nil)

	ctx := WithSwitchTargetCapture(context.Background(), &SwitchTargetCapture{})
	audit := &frozenAudit{sink: &fakeAuditSink{}}
	_, err := engine.FetchFirstAvailableUpstream(ctx, newSwitchTargetDispatchArgs(t, req, []AccountCandidate{a1, a2}, audit))
	if !asUpstreamAttemptError(err, nil) {
		t.Fatalf("fail-closed 应走既有最终失败路径，实际 = %v", err)
	}
	if hitsB.Load() != 0 {
		t.Fatalf("目标不可解析时不得跨账户发送，实际 a-2 请求 = %d", hitsB.Load())
	}
	found := false
	for _, label := range audit.metadata {
		if label == "switch_target_unresolved" {
			found = true
		}
	}
	if !found {
		t.Fatal("应输出 switch_target_unresolved 诊断")
	}
}

func asUpstreamAttemptError(err error, target **UpstreamAttemptError) bool {
	attempt, ok := err.(*UpstreamAttemptError)
	if target != nil && ok {
		*target = attempt
	}
	return ok
}

// ---------------------------------------------------------------------------
// 消费点接线：分组回退候选
// ---------------------------------------------------------------------------

// switchTargetFallbackCache 按 groupID 返回预置候选窗口。
type switchTargetFallbackCache struct {
	*fakeCache
	accountsByGroup map[string][]AccountCandidate
}

func (f *switchTargetFallbackCache) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	if _, ok := f.accountsByGroup[groupID]; ok {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, true, nil
	}
	return gatewayruntimecache.GroupUsageAccessMetadata{}, false, nil
}

func (f *switchTargetFallbackCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsOptions) ([]AccountCandidate, error) {
	return f.accountsByGroup[groupID], nil
}

// recordingFallbackAudit 捕获 fallback 层诊断输出。
type recordingFallbackAudit struct {
	gatewaypreauth.AuditCaptureContext
	labels []string
}

func (a *recordingFallbackAudit) AddGatewayMetadata(label string, _ map[string]any) {
	a.labels = append(a.labels, label)
}

func groupFallbackArgs(t *testing.T, req *gatewaypreauth.GatewayRequest) GroupFallbackArgs {
	t.Helper()
	return GroupFallbackArgs{
		Req:    req,
		Reason: "upstream_accounts_exhausted",
		APIKeyRecord: &gatewayruntimecache.GatewayAPIKeyRow{
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
				{GroupID: "group-1", Status: "active", GroupEnabled: 1},
				{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			},
		},
		SystemAccountID:            "system-1",
		GroupID:                    "group-1",
		RequestLane:                "text",
		RequestClientCompatibility: "",
	}
}

// TestGroupFallbackCandidateFilteredByFrozenSwitchTarget：Responses 冻结目标
// 下，回退分组内异供应商候选被后置过滤（分组视作不可用，found=false）。
func TestGroupFallbackCandidateFilteredByFrozenSwitchTarget(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	cache := &switchTargetFallbackCache{accountsByGroup: map[string][]AccountCandidate{
		"group-2": {accountWithMappings("glm-1", "glm", []string{"gpt-5"}, nil, nil)},
	}}
	engine.Cache = cache

	capture := &SwitchTargetCapture{}
	freezeOf(capture, "a-1", frozenResponsesTarget(), "gpt-5", "responses")
	ctx := WithSwitchTargetCapture(context.Background(), capture)

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	args := groupFallbackArgs(t, req)
	_, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(ctx, args)
	if err != nil {
		t.Fatalf("ResolveNextGroupFallbackCandidateForArgs: %v", err)
	}
	if found {
		t.Fatal("回退分组候选全部不承接冻结目标时应视为分组不可用")
	}
}

// TestGroupFallbackCandidateFailClosedDiagnosesOnce：冻结目标不可解析时，
// 分组回退层 fail-closed 并输出一次 switch_target_unresolved 诊断。
func TestGroupFallbackCandidateFailClosedDiagnosesOnce(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	cache := &switchTargetFallbackCache{accountsByGroup: map[string][]AccountCandidate{
		"group-2": {accountWithMappings("openai-1", "openai", []string{"gpt-5"}, nil, nil)},
	}}
	engine.Cache = cache

	capture := &SwitchTargetCapture{}
	// 已完成构造但目标不可解析（空 family / model）。
	capture.Freeze(SwitchTargetFreezeInput{SourceAccountID: "a-1", Target: &SwitchTarget{}})
	ctx := WithSwitchTargetCapture(context.Background(), capture)

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	args := groupFallbackArgs(t, req)
	audit := &recordingFallbackAudit{}
	args.AuditCapture = audit
	output, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(ctx, args)
	if err != nil {
		t.Fatalf("ResolveNextGroupFallbackCandidateForArgs: %v", err)
	}
	if found || len(output.Accounts) != 0 {
		t.Fatalf("fail-closed 应视为分组不可用，found=%v accounts=%#v", found, accountIDs(output.Accounts))
	}
	if len(audit.labels) != 1 || audit.labels[0] != "switch_target_unresolved" {
		t.Fatalf("应输出一次 switch_target_unresolved 诊断，实际 = %#v", audit.labels)
	}

	// 第二次解析（同请求）诊断不重复（每请求一次）。
	args2 := groupFallbackArgs(t, req)
	args2.AuditCapture = audit
	if _, _, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(ctx, args2); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if len(audit.labels) != 1 {
		t.Fatalf("诊断每请求只输出一次，实际 = %#v", audit.labels)
	}
}

// TestGroupFallbackCandidateKeptWithoutFrozenSwitchTarget：无冻结目标时回退
// 分组候选保持既有语义。
func TestGroupFallbackCandidateKeptWithoutFrozenSwitchTarget(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	cache := &switchTargetFallbackCache{accountsByGroup: map[string][]AccountCandidate{
		"group-2": {accountWithMappings("glm-1", "glm", []string{"gpt-5"}, nil, nil)},
	}}
	engine.Cache = cache

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	args := groupFallbackArgs(t, req)
	output, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), args)
	if err != nil {
		t.Fatalf("ResolveNextGroupFallbackCandidateForArgs: %v", err)
	}
	if !found || len(output.Accounts) != 1 || output.Accounts[0].ID != "glm-1" {
		t.Fatalf("无冻结目标时回退候选应保持初始语义，found=%v accounts=%#v", found, accountIDs(output.Accounts))
	}
}

// ---------------------------------------------------------------------------
// 消费点接线：候选窗口（含模型感知 reload）后置过滤
// ---------------------------------------------------------------------------

// TestFilterCandidatesPostFiltersByFrozenSwitchTarget：初始窗口按冻结目标
// 过滤——桥接 Chat 目标下 glm responses 直连候选剔除、Chat 别名候选保留。
func TestFilterCandidatesPostFiltersByFrozenSwitchTarget(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)

	capture := &SwitchTargetCapture{}
	freezeOf(capture, "src-1", frozenBridgedChatTarget(), "gpt-5", "responses")
	ctx := WithSwitchTargetCapture(context.Background(), capture)

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	coordinator := &capturingCoordinator{}
	input := candidateFilterInput(t, req, nil)
	input.RouteCoordinator = coordinator
	input.RawCandidates = []AccountCandidate{
		accountWithMappings("glm-direct", "glm", []string{"gpt-5"}, nil, nil),
		accountWithMappings("chat-alias", "glm", []string{"gpt-5-chat"}, nil,
			[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "responses", "gpt-5-chat", "chat_completions")}),
	}
	result, err := pipeline.FilterCandidates(ctx, input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "chat-alias" {
		t.Fatalf("仅 Chat 别名候选应保留，实际 = %#v", accountIDs(result.Accounts))
	}
}

// TestFilterCandidatesReloadResultPostFilteredByFrozenTarget：模型感知 reload
// 产生的候选同样按冻结目标后置过滤；过滤后为空走既有 fallback 兜底。
func TestFilterCandidatesReloadResultPostFilteredByFrozenTarget(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)

	capture := &SwitchTargetCapture{}
	freezeOf(capture, "src-1", frozenBridgedChatTarget(), "gpt-5", "responses")
	ctx := WithSwitchTargetCapture(context.Background(), capture)

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	coordinator := &capturingCoordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
	input := candidateFilterInput(t, req, nil)
	input.RouteCoordinator = coordinator
	// 初始候选不支持模型 → 触发模型感知 reload；reload 结果（responses 直连
	// glm 候选）不承接桥接 Chat 目标 → 过滤后为空 → 既有 fallback 契约。
	input.RawCandidates = []AccountCandidate{{ID: "a-1", SupportedModels: []string{"other"}}}
	input.LoadModelAwareCandidateAccounts = func(requestedModel, sourceEndpointFamily string) ([]AccountCandidate, error) {
		return []AccountCandidate{accountWithMappings("glm-direct", "glm", []string{"gpt-5"}, nil, nil)}, nil
	}
	result, err := pipeline.FilterCandidates(ctx, input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback {
		t.Fatalf("reload 候选被过滤后应走既有 fallback 契约，实际 outcome = %s", result.Outcome)
	}
	if !coordinator.fallbackCalled {
		t.Fatal("应调用既有分组回退决策")
	}
}

// TestFilterCandidatesUseFrozenFilterContextNotRequestFamily：过滤输入取自
// 冻结快照而非消费点自行推导——构造侧族（chat_completions default 词表）与
// 过滤一致时，同映射候选保留（覆盖 /responses/compact 形态的族分歧场景：
// 初始路由族推导会把 compact 视为 responses，构造侧实为 chat_completions，
// 同源后不再过度收窄）。
func TestFilterCandidatesUseFrozenFilterContextNotRequestFamily(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)

	capture := &SwitchTargetCapture{}
	// 构造侧：/responses/compact 请求经 requestMappingSourceFamilyOf 解析为
	// chat_completions，mapping (gpt-5, chat_completions)→(gpt-5-up, chat)
	// 命中，冻结目标 (gpt-5-up, chat_completions)；过滤输入族同源。
	freezeOf(capture, "src-1", SwitchTarget{
		ProviderCode:           "openai",
		UpstreamModel:          "gpt-5-up",
		UpstreamEndpointFamily: "chat_completions",
	}, "gpt-5", "chat_completions")
	ctx := WithSwitchTargetCapture(context.Background(), capture)

	req := newTestResponsesRequest(t, `{"model":"gpt-5","stream":true}`)
	input := candidateFilterInput(t, req, nil)
	input.RouteCoordinator = &capturingCoordinator{}
	input.RawCandidates = []AccountCandidate{
		// 候选直持客户端请求模型（通过既有初始模型过滤，初始筛选不变）；
		// gate 层若按请求路径自行推导族（responses）解析别名会失败并落入
		// 直连分支被剔除，取冻结快照族（chat_completions）则命中保留。
		accountWithMappings("chat-alias", "openai", []string{"gpt-5"}, nil,
			[]gatewayruntimecache.AccountModelMapping{mappingOf("gpt-5", "chat_completions", "gpt-5-up", "chat_completions")}),
	}
	result, err := pipeline.FilterCandidates(ctx, input)
	if err != nil {
		t.Fatalf("FilterCandidates: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts || len(result.Accounts) != 1 {
		t.Fatalf("冻结与过滤族同源时候选应保留，outcome=%s accounts=%#v", result.Outcome, accountIDs(result.Accounts))
	}
}
