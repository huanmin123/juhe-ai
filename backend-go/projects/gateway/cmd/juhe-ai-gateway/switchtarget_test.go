package main

// 切号冻结捕获（SwitchTarget）的 cmd 链路层测试：driver 构造完成后的冻结
// 推导、每请求一次语义、冻结与过滤的族词表同源（compact / countTokens 形态）、
// 内部辅助派发的载体豁免，以及 v1DispatchLoop 重派窗口的冻结目标后置过滤与
// fail-closed 诊断。
// 契约：docs/functions/切号时有效上游目标与上下文迁移设计.md §3.1/§4/§6。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// switchTargetTestAudit 记录 AddGatewayMetadata 的 label 序列。
type switchTargetTestAudit struct {
	labels []string
}

func (a *switchTargetTestAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (a *switchTargetTestAudit) AddGatewayMetadata(label string, _ map[string]any) {
	a.labels = append(a.labels, label)
}

func (a *switchTargetTestAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

// switchTargetTestObservability 记录 Warn 事件。
type switchTargetTestObservability struct {
	warnEvents []string
}

func (o *switchTargetTestObservability) Logger() gatewaypreauth.Logger { return o }

func (o *switchTargetTestObservability) Warn(event string, _ map[string]any, _ string) {
	o.warnEvents = append(o.warnEvents, event)
}

func (o *switchTargetTestObservability) TraceID() string { return "trace-test" }

func (o *switchTargetTestObservability) CreateTraceID() string { return "trace-test" }

func (o *switchTargetTestObservability) SanitizeURLForLog(value string) string { return value }

func (o *switchTargetTestObservability) LogRequestStage(string, map[string]any, string, time.Time) {}

func switchTargetRequest(t *testing.T, path, body, model string, stream bool) *gatewaypreauth.GatewayRequest {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")
	req := gatewaypreauth.NewGatewayRequest(raw)
	req.Body = &gatewaybody.Request{
		RawBody: []byte(body),
		State: &gatewaybody.BodyState{
			JSONParseStatus: gatewaybody.JSONParseStatusParsed,
			Model:           stringPtr(model),
			Stream:          boolPtr(stream),
		},
	}
	return req
}

func switchTargetMapping(sourceModel, sourceFamily, upstreamModel, upstreamFamily string) gatewayruntimecache.AccountModelMapping {
	return gatewayruntimecache.AccountModelMapping{
		SourceModel:            sourceModel,
		SourceEndpointFamily:   sourceFamily,
		UpstreamModel:          upstreamModel,
		UpstreamEndpointFamily: upstreamFamily,
		Enabled:                true,
	}
}

func freezeSwitchTargetForTest(capture *gatewaydispatch.SwitchTargetCapture, sourceID string, target *gatewaydispatch.SwitchTarget, clientModel, clientFamily string) {
	capture.Freeze(gatewaydispatch.SwitchTargetFreezeInput{
		SourceAccountID:            sourceID,
		Target:                     target,
		ClientRequestedModel:       clientModel,
		ClientSourceEndpointFamily: clientFamily,
	})
}

// ---------------------------------------------------------------------------
// 冻结推导（switchTargetForRequest）
// ---------------------------------------------------------------------------

func TestChainDriverSwitchTargetDerivation(t *testing.T) {
	driver := newChainProviderDriver()
	tests := []struct {
		name          string
		path          string
		body          string
		requestModel  string
		requestStream bool
		mappings      []gatewayruntimecache.AccountModelMapping
		wantModel     string
		wantFamily    string
		wantMode      string
	}{
		{
			name:          "native_responses_stream",
			path:          "/v1/responses",
			body:          `{"model":"gpt-5","stream":true}`,
			requestModel:  "gpt-5",
			requestStream: true,
			wantModel:     "gpt-5",
			wantFamily:    "responses",
			wantMode:      "responses_sse",
		},
		{
			name:          "native_chat_non_stream",
			path:          "/v1/chat/completions",
			body:          `{"model":"gpt-test","stream":false}`,
			requestModel:  "gpt-test",
			requestStream: false,
			wantModel:     "gpt-test",
			wantFamily:    "chat_completions",
			wantMode:      "chat_json",
		},
		{
			name:          "mapping_rename_within_responses",
			path:          "/v1/responses",
			body:          `{"model":"gpt-5","stream":true}`,
			requestModel:  "gpt-5",
			requestStream: true,
			mappings:      []gatewayruntimecache.AccountModelMapping{switchTargetMapping("gpt-5", "responses", "gpt-5-upstream", "responses")},
			wantModel:     "gpt-5-upstream",
			wantFamily:    "responses",
			wantMode:      "responses_sse",
		},
		{
			// Responses → Chat 桥：上游 Chat SSE 为桥接载体（即使下游非流式，
			// bridge 分支仍要求 chat_sse——与 requiredSupportedEndpointMode 一致）。
			name:          "mapping_responses_to_chat_bridge",
			path:          "/v1/responses",
			body:          `{"model":"gpt-5","stream":false}`,
			requestModel:  "gpt-5",
			requestStream: false,
			mappings:      []gatewayruntimecache.AccountModelMapping{switchTargetMapping("gpt-5", "responses", "gpt-5-chat", "chat_completions")},
			wantModel:     "gpt-5-chat",
			wantFamily:    "chat_completions",
			wantMode:      "chat_sse",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := switchTargetRequest(t, tt.path, tt.body, tt.requestModel, tt.requestStream)
			account := gatewaydispatch.AccountCandidate{
				ID:              "acc-1",
				ProviderCode:    "openai",
				ProtocolCode:    "openai",
				ProtocolVersion: "v1",
				ModelMappings:   tt.mappings,
			}
			target := driver.switchTargetForRequest(req, account, "")
			if target.UpstreamModel != tt.wantModel || target.UpstreamEndpointFamily != tt.wantFamily || target.UpstreamEndpointMode != tt.wantMode {
				t.Fatalf("target = %#v, want (%s, %s, %s)", target, tt.wantModel, tt.wantFamily, tt.wantMode)
			}
			if target.ProviderCode != "openai" {
				t.Fatalf("providerCode = %q", target.ProviderCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 冻结时点（构造成功后冻结，每请求一次）与载体豁免
// ---------------------------------------------------------------------------

func TestChainDriverFreezesSwitchTargetOncePerRequest(t *testing.T) {
	driver := newChainProviderDriver()
	req := switchTargetRequest(t, "/v1/responses", `{"model":"gpt-5","stream":true}`, "gpt-5", true)
	first := gatewaydispatch.AccountCandidate{
		ID:              "acc-1",
		ProviderCode:    "openai",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		SupportedModels: []string{"gpt-5"},
	}
	second := gatewaydispatch.AccountCandidate{
		ID:              "acc-2",
		ProviderCode:    "glm",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		SupportedModels: []string{"gpt-5"},
	}

	capture := &gatewaydispatch.SwitchTargetCapture{}
	ctx := gatewaydispatch.WithSwitchTargetCapture(context.Background(), capture)

	if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, first, gatewaydispatch.UsageIdentity{}, ""); err != nil {
		t.Fatalf("first build: %v", err)
	}
	// 同账户 Key 轮换会再次构造：冻结不重置。
	if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, first, gatewaydispatch.UsageIdentity{}, ""); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	// 后续不同账户构造不再覆盖冻结目标。
	if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, second, gatewaydispatch.UsageIdentity{}, ""); err != nil {
		t.Fatalf("second build: %v", err)
	}

	snapshot := capture.Snapshot()
	if !snapshot.Frozen || snapshot.SourceAccountID != "acc-1" {
		t.Fatalf("首次完成构造的账户应冻结：snapshot=%#v", snapshot)
	}
	if snapshot.Target.UpstreamModel != "gpt-5" || snapshot.Target.UpstreamEndpointFamily != "responses" || snapshot.Target.UpstreamEndpointMode != "responses_sse" {
		t.Fatalf("冻结目标错误：%#v", snapshot.Target)
	}
	// 构造侧过滤输入随冻结记录（原生 responses 请求 → responses 族）。
	if snapshot.ClientRequestedModel != "gpt-5" || snapshot.ClientSourceEndpointFamily != "responses" {
		t.Fatalf("过滤输入应与构造同源：%#v", snapshot)
	}

	// ctx 未注入载体（探针等链路）时不冻结、不 panic。
	other := &gatewaydispatch.SwitchTargetCapture{}
	if _, err := driver.BuildGatewayUpstreamRequestParts(context.Background(), req, second, gatewaydispatch.UsageIdentity{}, ""); err != nil {
		t.Fatalf("no-capture build: %v", err)
	}
	if snapshot := other.Snapshot(); snapshot.Frozen {
		t.Fatal("无载体的 ctx 不应冻结任何目标")
	}
}

// TestWithoutSwitchTargetCaptureStripsFreeze：内部合成 / 辅助 / 打分类派发
// 经 WithoutSwitchTargetCapture 剥离载体后，构造不再冻结主请求目标。
func TestWithoutSwitchTargetCaptureStripsFreeze(t *testing.T) {
	driver := newChainProviderDriver()
	req := switchTargetRequest(t, "/v1/chat/completions", `{"model":"score-model","messages":[]}`, "score-model", false)
	account := gatewaydispatch.AccountCandidate{
		ID:              "scoring-1",
		ProviderCode:    "openai",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		SupportedModels: []string{"score-model"},
	}

	capture := &gatewaydispatch.SwitchTargetCapture{}
	requestCtx := gatewaydispatch.WithSwitchTargetCapture(context.Background(), capture)
	stripped := gatewaydispatch.WithoutSwitchTargetCapture(requestCtx)

	// 剥离后的构造（辅助打分路径语义）不得冻结。
	if _, err := driver.BuildGatewayUpstreamRequestParts(stripped, req, account, gatewaydispatch.UsageIdentity{}, ""); err != nil {
		t.Fatalf("stripped build: %v", err)
	}
	if snapshot := capture.Snapshot(); snapshot.Frozen {
		t.Fatalf("剥离载体后的构造不得冻结主请求目标：snapshot=%#v", snapshot)
	}
	if gatewaydispatch.SwitchTargetCaptureFromContext(stripped) != nil {
		t.Fatal("剥离后的 ctx 不应再解析出载体")
	}

	// 同一载体在未剥离的主派发 ctx 上构造正常冻结。
	if _, err := driver.BuildGatewayUpstreamRequestParts(requestCtx, req, account, gatewaydispatch.UsageIdentity{}, ""); err != nil {
		t.Fatalf("main build: %v", err)
	}
	snapshot := capture.Snapshot()
	if !snapshot.Frozen || snapshot.SourceAccountID != "scoring-1" {
		t.Fatalf("主派发构造应正常冻结：snapshot=%#v", snapshot)
	}
}

// TestSwitchTargetFreezeFilterFamilyVocabularyConsistent：冻结与过滤对同一
// 请求的族判定同源——/v1/responses/compact 构造侧解析为 chat_completions，
// 过滤输入取自冻结快照后同映射候选保留（按请求路径自行推导的 responses 族
// 会造成候选池过度收窄）；countTokens 形态冻结出非空 family，不再误诊。
func TestSwitchTargetFreezeFilterFamilyVocabularyConsistent(t *testing.T) {
	driver := newChainProviderDriver()

	t.Run("responses_compact_family_consistent", func(t *testing.T) {
		req := switchTargetRequest(t, "/v1/responses/compact", `{"model":"gpt-5"}`, "gpt-5", false)
		account := gatewaydispatch.AccountCandidate{
			ID:              "acc-1",
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			SupportedModels: []string{"gpt-5-up"},
			ModelMappings:   []gatewayruntimecache.AccountModelMapping{switchTargetMapping("gpt-5", "chat_completions", "gpt-5-up", "chat_completions")},
		}
		capture := &gatewaydispatch.SwitchTargetCapture{}
		ctx := gatewaydispatch.WithSwitchTargetCapture(context.Background(), capture)
		if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, ""); err != nil {
			t.Fatalf("build: %v", err)
		}
		snapshot := capture.Snapshot()
		if !snapshot.Frozen {
			t.Fatal("构造应冻结目标")
		}
		// 构造侧 mapping 按chat_completions 族解析：冻结族与过滤输入族同源，
		// 不落入初始路由 contains 判定的 responses 歧义。
		if snapshot.Target.UpstreamEndpointFamily != "chat_completions" {
			t.Fatalf("冻结族应取构造侧词表 = chat_completions，实际 = %q", snapshot.Target.UpstreamEndpointFamily)
		}
		if snapshot.ClientSourceEndpointFamily != "chat_completions" {
			t.Fatalf("过滤输入族应与构造同源 = chat_completions，实际 = %q", snapshot.ClientSourceEndpointFamily)
		}
		// 同映射候选按冻结过滤上下文解析别名 → 保留。
		gate := gatewaydispatch.SwitchTargetGateFromContext(ctx)
		if !gate.Frozen() {
			t.Fatal("冻结后消费点门应生效")
		}
		candidate := gatewaydispatch.AccountCandidate{
			ID:              "cand-1",
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			SupportedModels: []string{"gpt-5-up"},
			ModelMappings:   []gatewayruntimecache.AccountModelMapping{switchTargetMapping("gpt-5", "chat_completions", "gpt-5-up", "chat_completions")},
		}
		kept := gate.FilterAccounts([]gatewaydispatch.AccountCandidate{candidate})
		if len(kept) != 1 {
			t.Fatal("冻结与过滤族同源时同映射候选应保留")
		}
	})

	t.Run("count_tokens_no_empty_family_misdiagnosis", func(t *testing.T) {
		req := switchTargetRequest(t, "/v1/messages/count_tokens", `{"model":"gpt-5","messages":[]}`, "gpt-5", false)
		account := gatewaydispatch.AccountCandidate{
			ID:              "acc-1",
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			SupportedModels: []string{"gpt-5"},
		}
		capture := &gatewaydispatch.SwitchTargetCapture{}
		ctx := gatewaydispatch.WithSwitchTargetCapture(context.Background(), capture)
		if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, ""); err != nil {
			t.Fatalf("build: %v", err)
		}
		snapshot := capture.Snapshot()
		if !snapshot.Frozen {
			t.Fatal("构造应冻结目标")
		}
		if snapshot.Target.UpstreamEndpointFamily == "" {
			t.Fatal("构造侧族词表恒非空，countTokens 形态不得产生空 family")
		}
		gate := gatewaydispatch.SwitchTargetGateFromContext(ctx)
		if gate.Unresolved() {
			t.Fatal("countTokens 形态冻结目标可解析，不得误判 fail-closed")
		}
	})
}

// ---------------------------------------------------------------------------
// 消费点：v1DispatchLoop 重派窗口过滤 + fail-closed
// ---------------------------------------------------------------------------

func newSwitchTargetTestLoop(t *testing.T, req *gatewaypreauth.GatewayRequest) (*v1DispatchLoop, *switchTargetTestAudit, *switchTargetTestObservability) {
	t.Helper()
	audit := &switchTargetTestAudit{}
	observability := &switchTargetTestObservability{}
	loop := &v1DispatchLoop{
		c:            &gatewayChain{observability: observability},
		req:          req,
		auditCapture: audit,
		traceID:      "trace-test",
		current: &gatewaypreauth.DispatchContext{
			UsageContext: gatewaypreauth.GatewayFailureUsageContext{
				Endpoint: "/v1/responses",
				APIKeyID: "apikey-1",
				GroupID:  "group-1",
				TraceID:  "trace-test",
			},
		},
	}
	return loop, audit, observability
}

func switchTargetLoopAccounts() []gatewaydispatch.AccountCandidate {
	return []gatewaydispatch.AccountCandidate{
		{ID: "src-1", ProviderCode: "openai", SupportedModels: []string{"other"}},
		{ID: "glm-1", ProviderCode: "glm", SupportedModels: []string{"gpt-5"}},
		{ID: "gpt-1", ProviderCode: "openai", SupportedModels: []string{"gpt-5"}},
	}
}

func TestV1DispatchLoopFiltersFrozenSwitchTargetWindow(t *testing.T) {
	req := switchTargetRequest(t, "/v1/responses", `{"model":"gpt-5","stream":true}`, "gpt-5", true)

	t.Run("no_capture_keeps_window", func(t *testing.T) {
		loop, audit, _ := newSwitchTargetTestLoop(t, req)
		accounts := switchTargetLoopAccounts()
		got := loop.filterAccountsForFrozenSwitchTarget(context.Background(), accounts)
		if len(got) != 3 {
			t.Fatalf("无冻结目标时窗口不变，实际 = %d", len(got))
		}
		if len(audit.labels) != 0 {
			t.Fatalf("不应输出诊断：%#v", audit.labels)
		}
	})

	t.Run("frozen_target_filters_window", func(t *testing.T) {
		loop, audit, _ := newSwitchTargetTestLoop(t, req)
		capture := &gatewaydispatch.SwitchTargetCapture{}
		freezeSwitchTargetForTest(capture, "src-1", &gatewaydispatch.SwitchTarget{
			ProviderCode:           "openai",
			UpstreamModel:          "gpt-5",
			UpstreamEndpointFamily: "responses",
			UpstreamEndpointMode:   "responses_sse",
		}, "gpt-5", "responses")
		ctx := gatewaydispatch.WithSwitchTargetCapture(context.Background(), capture)
		got := loop.filterAccountsForFrozenSwitchTarget(ctx, switchTargetLoopAccounts())
		if len(got) != 2 || got[0].ID != "src-1" || got[1].ID != "gpt-1" {
			t.Fatalf("应保留冻结源与同供应商直连候选，实际 = %#v", got)
		}
		if len(audit.labels) != 0 {
			t.Fatalf("可解析目标不应输出诊断：%#v", audit.labels)
		}
	})

	t.Run("unresolved_target_fails_closed_once", func(t *testing.T) {
		loop, audit, observability := newSwitchTargetTestLoop(t, req)
		capture := &gatewaydispatch.SwitchTargetCapture{}
		// 已发生完成构造但目标不可解析（空 family / model）。
		capture.Freeze(gatewaydispatch.SwitchTargetFreezeInput{SourceAccountID: "src-1", Target: &gatewaydispatch.SwitchTarget{}})
		ctx := gatewaydispatch.WithSwitchTargetCapture(context.Background(), capture)
		got := loop.filterAccountsForFrozenSwitchTarget(ctx, switchTargetLoopAccounts())
		if len(got) != 1 || got[0].ID != "src-1" {
			t.Fatalf("fail-closed 应仅保留冻结源，实际 = %#v", got)
		}
		if len(audit.labels) != 1 || audit.labels[0] != "switch_target_unresolved" {
			t.Fatalf("应输出一次 switch_target_unresolved，实际 = %#v", audit.labels)
		}
		if len(observability.warnEvents) != 1 || observability.warnEvents[0] != "switch_target_unresolved" {
			t.Fatalf("应输出一次结构化日志，实际 = %#v", observability.warnEvents)
		}
		// 再次重派不重复诊断（每请求一次）。
		_ = loop.filterAccountsForFrozenSwitchTarget(ctx, switchTargetLoopAccounts())
		if len(audit.labels) != 1 || len(observability.warnEvents) != 1 {
			t.Fatalf("诊断每请求只输出一次：audit=%#v warn=%#v", audit.labels, observability.warnEvents)
		}
	})
}
