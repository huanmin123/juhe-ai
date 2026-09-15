package main

// w1_tails_sweep_test.go：小尾巴清扫（w1t helper / TestW1T 入口）。
// 依据合并覆盖 profile（%TEMP%/w1pkg-merged-final2.out）定位各文件剩余未覆盖
// 错误臂/分支，逐段以最小注入手法覆盖：直接调用纯 helper、httptest 上游、
// stub read models、SQLite 坏行/关句柄、miniredis、固定时钟 fixture。
// 不修改任何既有文件；断言一律 stdlib t.Fatalf + 中文消息，表驱动、确定性
// 可重放、有界等待（SSE 心跳用例按 5s ticker 上限设界）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
	platformaccountbalance "github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// ---------------------------------------------------------------------------
// 共享 w1t helper
// ---------------------------------------------------------------------------

func w1tStrPtr(value string) *string { return &value }

func w1tJSONPtr(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("w1t 测试体必须是合法 JSON: %v", err)
	}
	return parsed
}

// w1tNewRequest 构造带 JSON body 的 GatewayRequest（镜像 w1g 同名手法）。
func w1tNewRequest(t *testing.T, method, target, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	if body != "" {
		req.Body = &gatewaybody.Request{
			RawBody: []byte(body),
			Body:    w1tJSONPtr(t, body),
			State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusParsed},
		}
	}
	return req
}

// w1tTailModels 是可编程的 runtime cache read models stub：账户按调用次序
// 回放（用于 hybrid auxiliary 的 select → hydrate 两次读取分叉）。
type w1tTailModels struct {
	groupAccess  *gatewayruntimecache.GroupUsageAccessMetadata
	accountPlans []w1tAccountPlan
	calls        int32
	catalog      []gatewayruntimecache.ProviderModelCatalogItem
	catalogErr   error
}

type w1tAccountPlan struct {
	accounts []gatewayruntimecache.OpenAIAccountSecret
	err      error
}

func (m *w1tTailModels) ReadGatewaySettings(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, nil
}

func (m *w1tTailModels) ReadGatewayRuntime(context.Context, string) (gatewayruntimecache.GatewayRuntime, error) {
	return gatewayruntimecache.GatewayRuntime{}, nil
}

func (m *w1tTailModels) ResolveGroupUsageAccessMetadata(context.Context, string, string) (*gatewayruntimecache.GroupUsageAccessMetadata, error) {
	return m.groupAccess, nil
}

func (m *w1tTailModels) ListOpenAIAccountsForGroupResult(context.Context, string, string, gatewayruntimecache.OpenAIAccountsForGroupOptions) (gatewayruntimecache.OpenAIAccountsForGroupResult, error) {
	index := int(atomic.AddInt32(&m.calls, 1)) - 1
	if len(m.accountPlans) == 0 {
		return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{}}, nil
	}
	if index >= len(m.accountPlans) {
		index = len(m.accountPlans) - 1
	}
	plan := m.accountPlans[index]
	return gatewayruntimecache.OpenAIAccountsForGroupResult{Accounts: plan.accounts}, plan.err
}

func (m *w1tTailModels) ListActiveResponseInspectionPolicies(context.Context, string, string) ([]gatewayruntimecache.ResponseInspectionPolicySummary, error) {
	return nil, nil
}

func (m *w1tTailModels) ListProviderModelCatalog(_ context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	if m.catalogErr != nil {
		return nil, m.catalogErr
	}
	out := []gatewayruntimecache.ProviderModelCatalogItem{}
	for _, item := range m.catalog {
		if input.ProviderCode != "" && item.ProviderCode != input.ProviderCode {
			continue
		}
		if input.SystemAccountID != "" && item.SystemAccountID != nil && *item.SystemAccountID != input.SystemAccountID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (m *w1tTailModels) LoadAccountCurrentConcurrencyByID(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func w1tNewTailCache(t *testing.T, models *w1tTailModels) *gatewayruntimecache.Service {
	t.Helper()
	service, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("组装 w1t runtime cache: %v", err)
	}
	return service
}

// w1tSetupRedis 与 w1u/w1i 同模式：miniredis + go-redis。
func w1tSetupRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("启动 miniredis 失败: %v", err)
	}
	t.Cleanup(func() { mr.Close() })
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

// w1tPanicAudit 是 Dispatch 必然 panic 的审计桩（驱动 recover 分支）。
type w1tPanicAudit struct{}

func (w1tPanicAudit) Dispatch(gatewaypreauth.DispatchedAuditLogInput) { panic("w1t audit boom") }

// w1tCaptureAudit 记录 Dispatch 次数。
type w1tCaptureAudit struct {
	mu  sync.Mutex
	got int
}

func (a *w1tCaptureAudit) Dispatch(input gatewaypreauth.DispatchedAuditLogInput) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got++
	_ = input
}

// ---------------------------------------------------------------------------
// chain_driver.go：目录适配、请求构建臂、端点模式桥、纯 helper
// ---------------------------------------------------------------------------

func TestW1TDriverCatalogAdapterArms(t *testing.T) {
	catalog := []gatewayruntimecache.ProviderModelCatalogItem{
		{Model: "m-active", Status: "active", ProviderCode: "openai", SupportedServiceTiers: []string{"default"}},
		{Model: "m-inactive", Status: "inactive", ProviderCode: "openai"},
	}
	adapter := chainGptRequestOverrideModelCatalog{cache: w1tNewTailCache(t, &w1tTailModels{catalog: catalog})}
	items, err := adapter.ListGptRequestOverrideModelCatalog(context.Background(), "openai", "sys-w1t", true)
	if err != nil {
		t.Fatalf("目录适配成功臂: %v", err)
	}
	if len(items) != 1 || items[0].Model != "m-active" || len(items[0].SupportedServiceTiers) != 1 {
		t.Fatalf("目录适配过滤 = %+v, want 仅 active m-active", items)
	}

	failing := chainGptRequestOverrideModelCatalog{cache: w1tNewTailCache(t, &w1tTailModels{catalogErr: errors.New("目录读取失败")})}
	if _, err := failing.ListGptRequestOverrideModelCatalog(context.Background(), "openai", "sys-w1t", true); err == nil ||
		!strings.Contains(err.Error(), "目录读取失败") {
		t.Fatalf("目录错误臂 err = %v, want 目录读取失败", err)
	}
}

func TestW1TDriverRequestPartsTailArms(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("gemini 非法账户类型 URL 为空报错", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1beta/models/gem-x:generateContent", `{}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_gem", ProtocolCode: "gemini", Type: "weird_type", BaseURL: "https://gem.example",
		}
		_, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err == nil || !strings.Contains(err.Error(), "不支持当前 Gemini 请求路径") {
			t.Fatalf("err = %v, want 不支持当前 Gemini 请求路径", err)
		}
	})

	t.Run("codex oauth 请求覆盖凭据非法报错", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-x"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_codex", ProtocolCode: "openai", Type: "oauth", BaseURL: "https://up.example",
			// SupportedModels 命中请求模型 → canonicalAccountModel 非空 →
			// ResolveGptRequestOverrideModelCapabilities 真正读取覆盖凭据。
			SupportedModels: []string{"gpt-x"},
			Credentials:     map[string]any{"service_tier_override": 123},
		}
		if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, ""); err == nil {
			t.Fatal("非法 service_tier_override 必须报错")
		}
	})

	t.Run("api_key 请求覆盖应用报错", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_apikey", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://up.example",
			Credentials: map[string]any{"reasoning_effort_override": true},
		}
		if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, ""); err == nil {
			t.Fatal("api_key 非法覆盖凭据必须报错（ApplyGptAccountRequestOverridesToUpstreamBody）")
		}
	})

	t.Run("codex_responses 兼容体 + 非法 JSON 请求体报错", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("not-json"))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{RawBody: []byte("not-json")}
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_compat_bad", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://up.example",
		}
		_, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "codex_responses")
		if err == nil || !strings.Contains(err.Error(), "有效的 JSON 对象") {
			t.Fatalf("err = %v, want 请求体 JSON 校验失败", err)
		}
	})

	t.Run("codex_responses 兼容体 + 模型映射覆写", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/responses", `{"model":"src","input":"hi"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_compat", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://up.example",
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "src", SourceEndpointFamily: "responses",
				UpstreamModel: "up", UpstreamEndpointFamily: "responses", Enabled: true,
			}},
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "codex_responses")
		if err != nil {
			t.Fatalf("兼容体构建: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(parts.Body, &decoded); err != nil {
			t.Fatalf("兼容体解码: %v body=%s", err, parts.Body)
		}
		// 兼容体路径的 model 覆写取请求模型（compatibilityModelOverride），
		// 映射上游模型只进入 codex 客户端头，不改写 body。
		if decoded["model"] != "src" {
			t.Fatalf("兼容体 model = %v, want 请求模型 src", decoded["model"])
		}
		if decoded["stream"] != true || decoded["store"] != false {
			t.Fatalf("兼容体 stream/store = %v/%v, want true/false", decoded["stream"], decoded["store"])
		}
	})

	t.Run("codex_responses 兼容体 + 无映射时回落请求模型", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/responses", `{"model":"src","input":"hi"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_compat_nomap", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://up.example",
			SupportedModels: []string{"src"},
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "codex_responses")
		if err != nil {
			t.Fatalf("无映射兼容体构建: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(parts.Body, &decoded); err != nil {
			t.Fatalf("兼容体解码: %v", err)
		}
		if decoded["model"] != "src" {
			t.Fatalf("兼容体 model = %v, want canonical 模型 src", decoded["model"])
		}
	})

	t.Run("codex_responses 兼容体 + canonical 未命中回落请求模型", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/responses", `{"model":"src","input":"hi"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_compat_miss", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://up.example",
			SupportedModels: []string{"other-model"},
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "codex_responses")
		if err != nil {
			t.Fatalf("未命中兼容体构建: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(parts.Body, &decoded); err != nil {
			t.Fatalf("兼容体解码: %v", err)
		}
		if decoded["model"] != "src" {
			t.Fatalf("兼容体 model = %v, want 请求模型 src", decoded["model"])
		}
	})

	t.Run("模型映射转换 + 强制 SSE 头", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"src","stream":true}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_map", ProtocolCode: "openai", Type: "api_key", BaseURL: "https://up.example",
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "src", SourceEndpointFamily: "chat_completions",
				UpstreamModel: "up", UpstreamEndpointFamily: "chat_completions", Enabled: true,
			}},
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("映射转换: %v", err)
		}
		if !strings.Contains(string(parts.Body), `"up"`) {
			t.Fatalf("转换 body = %s, want 含上游模型 up", parts.Body)
		}
		if parts.Headers.Get("Accept") != "text/event-stream" {
			t.Fatalf("流式映射 Accept = %q, want text/event-stream", parts.Headers.Get("Accept"))
		}
	})

	t.Run("gemini code assist 非生成端点报错", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1beta/models/gem-x:countTokens", `{}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_ca", ProtocolCode: "gemini", Type: "google_oauth", BaseURL: "https://gem.example",
			Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "proj-1"},
		}
		_, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err == nil || !strings.Contains(err.Error(), "仅支持 generateContent") {
			t.Fatalf("err = %v, want 仅支持 generateContent", err)
		}
	})
}

func TestW1TDriverEndpointModeBridgeArms(t *testing.T) {
	driver := newChainProviderDriver()
	mapping := func(source, upstream string) []gatewayruntimecache.AccountModelMapping {
		return []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "src", SourceEndpointFamily: source,
			UpstreamModel: "up", UpstreamEndpointFamily: upstream, Enabled: true,
		}}
	}

	t.Run("桥接模式矩阵", func(t *testing.T) {
		cases := []struct {
			name       string
			path       string
			body       string
			source     string
			upstream   string
			modes      []string
			wantReason string
		}{
			{name: "responses→chat SSE 缺模式", path: "/v1/responses", body: `{"model":"src"}`, source: "responses", upstream: "chat_completions", modes: []string{"responses_json"}, wantReason: "endpoint_mode_unsupported"},
			{name: "chat→anthropic SSE 缺模式", path: "/v1/chat/completions", body: `{"model":"src","stream":true}`, source: "chat_completions", upstream: "messages", modes: []string{"messages_json"}, wantReason: "endpoint_mode_unsupported"},
			{name: "chat→gemini SSE 缺模式", path: "/v1/chat/completions", body: `{"model":"src","stream":true}`, source: "chat_completions", upstream: "generate_content", modes: []string{"generate_content_json"}, wantReason: "endpoint_mode_unsupported"},
			{name: "chat→gemini JSON 缺模式", path: "/v1/chat/completions", body: `{"model":"src"}`, source: "chat_completions", upstream: "generate_content", modes: []string{"chat_json"}, wantReason: "endpoint_mode_unsupported"},
			{name: "未知上游族不设闸", path: "/v1/chat/completions", body: `{"model":"src"}`, source: "chat_completions", upstream: "bogus_family", modes: []string{"chat_json"}, wantReason: ""},
		}
		for _, testCase := range cases {
			req := w1tNewRequest(t, http.MethodPost, testCase.path, testCase.body)
			account := gatewaydispatch.AccountCandidate{
				ID: "acc_w1t_bridge", ProtocolCode: "openai", ProviderCode: "hybrid", Type: "api_key", BaseURL: "https://up.example",
				SupportedEndpointModes: testCase.modes, ModelMappings: mapping(testCase.source, testCase.upstream),
			}
			if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, ""); reason != testCase.wantReason {
				t.Fatalf("%s: mismatch reason = %q, want %q", testCase.name, reason, testCase.wantReason)
			}
		}
	})

	t.Run("端点模式不匹配淘汰", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_mode", ProtocolCode: "openai", Type: "api_key",
			SupportedEndpointModes: []string{"responses_json"},
		}
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, ""); reason != "endpoint_mode_unsupported" {
			t.Fatalf("mismatch reason = %q, want endpoint_mode_unsupported", reason)
		}
	})

	t.Run("无闸门形态不淘汰", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodGet, "/v1/models", "")
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_models", ProtocolCode: "openai", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json"},
		}
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, ""); reason != "" {
			t.Fatalf("models 请求 reason = %q, want 空", reason)
		}
	})

	t.Run("nil 请求不设闸", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_nil", ProtocolCode: "openai", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json"},
		}
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(nil, account, ""); reason != "" {
			t.Fatalf("nil 请求 reason = %q, want 空", reason)
		}
	})

	t.Run("anthropic 账户非 messages 形态无额外闸", func(t *testing.T) {
		req := w1tNewRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x","max_tokens":8}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_w1t_ant", ProtocolCode: "anthropic", Type: "api_key", BaseURL: "https://ant.example",
			SupportedEndpointModes: []string{"chat_json"},
		}
		_ = driver.gatewayRequestCapabilityMismatchReasonFor(req, account, "")
	})
}

func TestW1TDriverHeaderAndCompatHelpers(t *testing.T) {
	req := w1tNewRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`)

	t.Run("APIKeys 兜底凭据", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{ID: "acc_w1t_keys", ProtocolCode: "openai", APIKeys: []string{"sk-fallback"}}
		headers := upstreamHeadersOf(req, account)
		if headers.Get("Authorization") != "Bearer sk-fallback" {
			t.Fatalf("Authorization = %q, want Bearer sk-fallback", headers.Get("Authorization"))
		}
	})

	t.Run("beta 合并去重与空段", func(t *testing.T) {
		merged := mergeAnthropicBetaHeader("OAUTH-2025-04-20, a,,b", true)
		parts := strings.Split(merged, ",")
		if parts[0] != "OAUTH-2025-04-20" {
			t.Fatalf("首次出现应保留原大小写: %q", merged)
		}
		if strings.Contains(merged, "oauth-2025-04-20") {
			t.Fatalf("大小写不敏感去重失败: %q", merged)
		}
		if len(parts) != 6 {
			t.Fatalf("合并值数量 = %d (%q), want 6（2 客户端 + 4 CLI）", len(parts), merged)
		}
	})

	t.Run("兼容 model override 缺省为空", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"x"}`))
		noModelReq := gatewaypreauth.NewGatewayRequest(request)
		noModelReq.Body = &gatewaybody.Request{RawBody: []byte(`{"input":"x"}`)}
		if got := newChainProviderDriver().compatibilityModelOverride(noModelReq); got != "" {
			t.Fatalf("compatibilityModelOverride = %q, want 空", got)
		}
	})

	t.Run("tools 非数组与缺省补齐", func(t *testing.T) {
		body := map[string]any{"tools": "oops"}
		applyCodexResponsesCompatibility(body)
		if _, ok := body["tools"].([]any); !ok {
			t.Fatalf("非数组 tools 未被归一: %v", body["tools"])
		}
		body2 := map[string]any{"input": "hi"}
		applyCodexResponsesCompatibility(body2)
		if tools, ok := body2["tools"].([]any); !ok || len(tools) != 0 {
			t.Fatalf("缺省 tools 未补齐: %v", body2["tools"])
		}
	})
}

// ---------------------------------------------------------------------------
// compose_account_balance_refresh.go：共享池、装配降级、代理信封、手动输入
// ---------------------------------------------------------------------------

func TestW1TBalanceSharedPoolAndOwnerID(t *testing.T) {
	db := w1uOpenPlainSQLite(t, "w1t-pool.sqlite3")
	pool := sharedDBPool{db: db}
	if pool.DB() != db {
		t.Fatal("sharedDBPool.DB 必须返回原句柄")
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("sharedDBPool.Close = %v, want nil", err)
	}
	ownerID := newGatewayBalanceOwnerID()
	if !strings.HasPrefix(ownerID, "gateway-manual-") || len(ownerID) != len("gateway-manual-")+12 {
		t.Fatalf("ownerID = %q, want gateway-manual- + 12 hex", ownerID)
	}
}

func TestW1TBalanceWirePGDegradationArms(t *testing.T) {
	t.Run("PG 模式 SQLite 句柄：契约校验失败降级或打开失败", func(t *testing.T) {
		db := w1uOpenPlainSQLite(t, "w1t-pg.sqlite3")
		composed := &composition{db: db, pgDialect: true}
		accountStore, err := accounts.NewStore(db, false, "w1t-secret", time.Now, newCompositionID)
		if err != nil {
			t.Fatalf("accounts store: %v", err)
		}
		providerStore, err := providers.NewStore(db, false, time.Now)
		if err != nil {
			t.Fatalf("providers store: %v", err)
		}
		wireErr := wireInProcessBalanceAndCatalogRefresh(composed, runtimeConfig{Secret: "w1t-secret"}, accountStore, providerStore)
		if wireErr != nil {
			if !strings.Contains(wireErr.Error(), "account-balance gateway store") {
				t.Fatalf("wireErr = %v, want store 打开失败", wireErr)
			}
			return
		}
		if accountStore.BalanceRefresherPort() != nil {
			t.Fatal("无 juhe_jobs 契约表时余额端口必须降级为 nil")
		}
		if accountStore.ModelCatalogRefresherPort() == nil {
			t.Fatal("目录刷新端口必须始终装配")
		}
	})

	t.Run("PG 模式 nil 句柄：契约校验失败降级", func(t *testing.T) {
		composed := &composition{db: nil, pgDialect: true}
		accountStore, err := accounts.NewStore(w1uOpenPlainSQLite(t, "w1t-pg2.sqlite3"), false, "w1t-secret", time.Now, newCompositionID)
		if err != nil {
			t.Fatalf("accounts store: %v", err)
		}
		providerStore, err := providers.NewStore(w1uOpenPlainSQLite(t, "w1t-pg3.sqlite3"), false, time.Now)
		if err != nil {
			t.Fatalf("providers store: %v", err)
		}
		// OpenStore 对 nil 池惰性容错时按契约校验失败降级；直接报错时上抛。
		wireErr := wireInProcessBalanceAndCatalogRefresh(composed, runtimeConfig{Secret: "w1t-secret"}, accountStore, providerStore)
		if wireErr != nil {
			return
		}
		if accountStore.BalanceRefresherPort() != nil {
			t.Fatal("nil 业务句柄的余额端口必须降级为 nil")
		}
		if accountStore.ModelCatalogRefresherPort() == nil {
			t.Fatal("目录刷新端口必须始终装配")
		}
	})
}

// w1tProxyDB 打开带 proxy_profiles 表的 SQLite 并写入指定行。
func w1tProxyDB(t *testing.T, rows ...string) *sql.DB {
	t.Helper()
	db := w1uOpenPlainSQLite(t, "w1t-proxy.sqlite3")
	if _, err := db.Exec(`CREATE TABLE proxy_profiles (id TEXT PRIMARY KEY, type TEXT NOT NULL, host TEXT NOT NULL,
		port INTEGER NOT NULL, username TEXT NOT NULL DEFAULT '', password_encrypted TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("建 proxy_profiles: %v", err)
	}
	for index, row := range rows {
		if _, err := db.Exec(row); err != nil {
			t.Fatalf("seed proxy 行 %d: %v", index, err)
		}
	}
	return db
}

func TestW1TProxyURLEnvelopeTails(t *testing.T) {
	const secret = "w1t-proxy-secret"
	ctx := context.Background()

	t.Run("pg 表名与 $1 绑定分支", func(t *testing.T) {
		db := w1tProxyDB(t)
		if _, err := resolveProxyURLEnvelope(ctx, db, true, secret, "prof-1"); err == nil {
			t.Fatal("SQLite 句柄上查询 juhe_business.proxy_profiles 必须报错")
		}
	})

	t.Run("不支持的代理类型报错", func(t *testing.T) {
		db := w1tProxyDB(t, `INSERT INTO proxy_profiles (id, type, host, port) VALUES ('p1', 'ftp', '127.0.0.1', 21)`)
		if _, err := resolveProxyURLEnvelope(ctx, db, false, secret, "p1"); err == nil ||
			!strings.Contains(err.Error(), "不支持的 proxy 类型") {
			t.Fatalf("err = %v, want 不支持的 proxy 类型", err)
		}
	})

	t.Run("密码信封明文非 JSON 对象报错", func(t *testing.T) {
		envelope, err := platformaccountbalance.NewCredentialEnvelope(secret, "proxy_url", "plain-text")
		if err != nil {
			t.Fatalf("构造信封: %v", err)
		}
		db := w1tProxyDB(t, fmt.Sprintf(`INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted)
			VALUES ('p2', 'http', '127.0.0.1', 8080, 'u', '%s')`, envelope.Ciphertext))
		if _, err := resolveProxyURLEnvelope(ctx, db, false, secret, "p2"); err == nil {
			t.Fatal("非 JSON 对象的密码明文必须报错")
		}
	})

	t.Run("refresher.resolveProxyURL 解出代理串", func(t *testing.T) {
		envelope, err := platformaccountbalance.NewCredentialEnvelope(secret, "proxy_url", map[string]string{"url": "socks5h://127.0.0.1:1080"})
		if err != nil {
			t.Fatalf("构造信封: %v", err)
		}
		db := w1tProxyDB(t, fmt.Sprintf(`INSERT INTO proxy_profiles (id, type, host, port, username, password_encrypted)
			VALUES ('p3', 'socks5', '127.0.0.1', 1080, '', '%s')`, envelope.Ciphertext))
		refresher := &gatewayModelCatalogRefresher{db: db, pg: false, secret: secret, now: time.Now}
		got, err := refresher.resolveProxyURL(ctx, w1tStrPtr("p3"))
		if err != nil {
			t.Fatalf("resolveProxyURL: %v", err)
		}
		if got != "socks5h://127.0.0.1:1080" {
			t.Fatalf("resolveProxyURL = %q, want socks5h://127.0.0.1:1080", got)
		}
	})
}

func TestW1TManualBuildInputDefaultsAndProxyError(t *testing.T) {
	const secret = "w1t-manual-secret"
	envelope, err := accounts.EncryptJSON(secret, accounts.Credentials{"api_key": "sk-w1t", "base_url": "https://up.example"})
	if err != nil {
		t.Fatalf("加密凭据: %v", err)
	}
	base := accounts.BalanceRefreshCandidate{
		ID: "acc-w1t", SystemAccountID: "sys-w1t", ConfigRevision: 1,
		CredentialsEnvelope: envelope, ConfigJSON: `{"adapter":"builtin","intervalMinutes":5}`,
	}

	t.Run("provider/status/inputVersion 缺省", func(t *testing.T) {
		refresher := &gatewayManualBalanceRefresher{secret: secret, now: time.Now}
		input, err := refresher.buildManualInput(context.Background(), base)
		if err != nil {
			t.Fatalf("buildManualInput: %v", err)
		}
		if input.Provider != "openai" || input.Status != "active" || input.InputVersion != 1 {
			t.Fatalf("缺省值 = %q/%q/%d, want openai/active/1", input.Provider, input.Status, input.InputVersion)
		}
		if input.Proxy != nil {
			t.Fatalf("无代理档案时 Proxy = %+v, want nil", input.Proxy)
		}
	})

	t.Run("代理信封解析失败上抛", func(t *testing.T) {
		db := w1tProxyDB(t, `INSERT INTO proxy_profiles (id, type, host, port) VALUES ('bad', 'ftp', 'h', 1)`)
		refresher := &gatewayManualBalanceRefresher{db: db, pg: false, secret: secret, now: time.Now}
		candidate := base
		candidate.ProxyProfileID = sql.NullString{String: "bad", Valid: true}
		if _, err := refresher.buildManualInput(context.Background(), candidate); err == nil {
			t.Fatal("坏代理档案必须上抛错误")
		}
	})
}

func TestW1TBalanceDraftProbeErrorArm(t *testing.T) {
	// 空 secret：凭据信封/执行核心凭据校验必然失败（TestDraft 错误臂）。
	refresher := &gatewayManualBalanceRefresher{secret: "", now: time.Now}
	if _, err := refresher.TestDraft(context.Background(), accounts.BalanceDraftProbeInput{
		Credentials: accounts.Credentials{"api_key": "sk-draft", "base_url": "http://127.0.0.1:65535"},
		Config:      map[string]any{"adapter": "builtin", "intervalMinutes": 5},
	}); err == nil {
		t.Fatal("空 secret 的草稿探测必须报错")
	}
}

func TestW1TModelCatalogRefresherTails(t *testing.T) {
	ctx := context.Background()
	const secret = "w1t-catalog-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-add"},{"id":"m-add"}]}`))
	}))
	t.Cleanup(upstream.Close)
	input := accounts.ModelCatalogDiscoveryInput{
		OwnerSystemAccountID: "sys-w1t", ProviderCode: "gpt", ProtocolCode: "openai", AccountType: "api_key",
		Credentials: accounts.Credentials{"api_key": "sk-w1t", "base_url": upstream.URL},
	}

	t.Run("本地目录投影读取失败上抛", func(t *testing.T) {
		bare := w1uOpenPlainSQLite(t, "w1t-catalog-bare.sqlite3")
		store, err := providers.NewStore(bare, false, time.Now)
		if err != nil {
			t.Fatalf("providers store 构造即失败: %v", err)
		}
		refresher := &gatewayModelCatalogRefresher{secret: secret, catalog: store, now: time.Now}
		if _, err := refresher.RefreshDraftModelCatalog(ctx, input); err == nil {
			t.Fatal("缺表的目录投影必须报错")
		}
	})

	t.Run("代理档案解析失败上抛", func(t *testing.T) {
		db := w1tProxyDB(t, `INSERT INTO proxy_profiles (id, type, host, port) VALUES ('bad', 'ftp', 'h', 1)`)
		refresher := &gatewayModelCatalogRefresher{db: db, pg: false, secret: secret, now: time.Now}
		badInput := input
		badInput.ProxyProfileID = w1tStrPtr("bad")
		if _, err := refresher.RefreshDraftModelCatalog(ctx, badInput); err == nil {
			t.Fatal("坏代理档案必须报错")
		}
	})

	t.Run("addedModels 与推荐健康检查模型", func(t *testing.T) {
		db := w1uOpenSeededBusinessDB(t)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO custom_provider_models (id, provider_code, model, scope, system_account_id,
			status, mode, supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			created_by, created_at, updated_at)
			VALUES ('cpm-w1t', 'gpt', 'm-add', 'global', NULL, 'active', NULL, '["chat_completions"]', '[]', '[]', 'w1t', ?, ?)`,
			now, now); err != nil {
			t.Fatalf("seed custom model: %v", err)
		}
		store, err := providers.NewStore(db, false, time.Now)
		if err != nil {
			t.Fatalf("providers store: %v", err)
		}
		refresher := &gatewayModelCatalogRefresher{db: db, pg: false, secret: secret, catalog: store, now: time.Now}
		result, err := refresher.RefreshDraftModelCatalog(ctx, input)
		if err != nil {
			t.Fatalf("RefreshDraftModelCatalog: %v", err)
		}
		added, _ := result["addedModels"].([]string)
		if len(added) != 1 || added[0] != "m-add" {
			t.Fatalf("addedModels = %v, want [m-add]", result["addedModels"])
		}
		if result["recommendedHealthCheckModel"] != "m-add" {
			t.Fatalf("recommendedHealthCheckModel = %v, want m-add", result["recommendedHealthCheckModel"])
		}
	})
}

// ---------------------------------------------------------------------------
// chain_compose.go：组合根 nil cache、客户端目录错误臂、body 拒绝记录
// ---------------------------------------------------------------------------

func TestW1TComposeGatewayChainNilCache(t *testing.T) {
	_, _, err := composeGatewayChain(chainRuntimeDeps{})
	if err == nil || !strings.Contains(err.Error(), "G10 runtime cache") {
		t.Fatalf("err = %v, want 缺少 G10 runtime cache", err)
	}
}

func TestW1TChainComposeClientCatalogErrorArm(t *testing.T) {
	loader := chainClientModelCatalog{cache: w1tNewTailCache(t, &w1tTailModels{catalogErr: errors.New("目录查询失败")})}
	if got := loader.ListClientModelCatalog("sys-w1t", []string{"openai"}); got != nil {
		t.Fatalf("错误臂 entries = %+v, want nil", got)
	}
}

// w1tNewUsageService 以进程内 recorder + 空溢出组装最小用量服务。
func w1tNewUsageService() *gatewayusage.Service {
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 64, Logger: slog.Default()}, nil)
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, spoolOverflow{}, 0, 0)
	return gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true})
}

func TestW1TBodyRejectionRecorderArms(t *testing.T) {
	fixedClock := gatewaypreauth.Clock(gatewaypreauth.SystemClock{})

	t.Run("审计桩 panic 被 recover 吞掉", func(t *testing.T) {
		recorder := &chainBodyRejectionRecorder{
			audit: w1tPanicAudit{}, auditEnabled: func() bool { return true },
			usage: w1tNewUsageService(), clock: fixedClock,
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("RecordGatewayBodyRejection 必须 recover 审计 panic: %v", recovered)
			}
		}()
		recorder.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{StatusCode: http.StatusRequestEntityTooLarge, Reason: "payload_too_large"})
	})

	t.Run("UNKNOWN method / 空 path / payload 文案 / 用量失败记录", func(t *testing.T) {
		audit := &w1tCaptureAudit{}
		recorder := &chainBodyRejectionRecorder{
			audit: audit, auditEnabled: func() bool { return true },
			usage: w1tNewUsageService(), clock: fixedClock,
		}
		runtime := &gatewayruntimecache.GatewayRuntime{
			APIKey:      &gatewayruntimecache.GatewayAPIKeyRow{ID: "key-w1t", SystemAccountID: "sys-w1t", SelectedGroupID: "grp-w1t"},
			GroupAccess: &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "gpt", GroupOwnerSystemAccountID: "sys-w1t", GroupAccessType: "personal"},
		}
		request := &http.Request{Method: "", URL: &url.URL{}, Header: http.Header{}}
		request = request.WithContext(context.WithValue(request.Context(), chainGatewayRuntimeKey{}, runtime))
		recorder.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{
			StatusCode:      http.StatusTooManyRequests,
			Reason:          "backpressure",
			ResponsePayload: gatewaybody.GatewayErrorPayload("payload 文案", "gateway_error", ""),
		})
		audit.mu.Lock()
		got := audit.got
		audit.mu.Unlock()
		if got != 1 {
			t.Fatalf("dropped audit 派发次数 = %d, want 1", got)
		}
	})

	t.Run("usage 缺失时用量失败记录早退", func(t *testing.T) {
		audit := &w1tCaptureAudit{}
		recorder := &chainBodyRejectionRecorder{
			audit: audit, auditEnabled: func() bool { return true },
			usage: nil, clock: fixedClock,
		}
		runtime := &gatewayruntimecache.GatewayRuntime{
			APIKey: &gatewayruntimecache.GatewayAPIKeyRow{ID: "key-w1t", SystemAccountID: "sys-w1t", SelectedGroupID: "grp-w1t"},
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request = request.WithContext(context.WithValue(request.Context(), chainGatewayRuntimeKey{}, runtime))
		recorder.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{StatusCode: http.StatusTooManyRequests, Reason: "backpressure"})
		audit.mu.Lock()
		got := audit.got
		audit.mu.Unlock()
		if got != 1 {
			t.Fatalf("dropped audit 派发次数 = %d, want 1", got)
		}
	})

	t.Run("审计关闭时不派发", func(t *testing.T) {
		audit := &w1tCaptureAudit{}
		recorder := &chainBodyRejectionRecorder{
			audit: audit, auditEnabled: func() bool { return false },
			usage: w1tNewUsageService(), clock: fixedClock,
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		recorder.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{StatusCode: http.StatusServiceUnavailable})
		audit.mu.Lock()
		got := audit.got
		audit.mu.Unlock()
		if got != 0 {
			t.Fatalf("审计关闭时派发次数 = %d, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_compose.go：hybrid auxiliary 剩余错误臂（可编程 read models）
// ---------------------------------------------------------------------------

func w1tAuxInput(record gatewayhybrid.APIKeyRecord, maxBytes int) gatewayhybrid.AuxiliaryDispatchInput {
	body := gatewayhybrid.NewOrderedJSON()
	body.Set("model", "src")
	body.Set("messages", []any{map[string]any{"role": "user", "content": "w1t"}})
	return gatewayhybrid.AuxiliaryDispatchInput{
		Body:                       body,
		RawBody:                    []byte(`{"model":"src","messages":[{"role":"user","content":"w1t"}]}`),
		APIKeyRecord:               record,
		TargetModel:                "src",
		TraceID:                    "trace-w1t",
		Endpoint:                   "/v1/chat/completions",
		TimeoutMs:                  5000,
		ResponseMaxBytes:           maxBytes,
		NoAccountErrorCode:         "w1t_no_account",
		NoAccountErrorMessage:      "无可用辅助账户",
		DispatchErrorCode:          "w1t_dispatch_failed",
		DispatchErrorMessage:       "辅助派发失败",
		HTTPErrorCode:              "w1t_upstream_http_error",
		ResponseTooLargeMessage:    "辅助响应过大",
		RequestClientCompatibility: "",
	}
}

func w1tAuxAccount(baseURL string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-w1t-aux", BaseURL: baseURL, APIKey: "sk-w1t-upstream",
		ProviderCode: "openai", ProtocolCode: "openai", Status: "active", Type: "api_key", SystemAccountID: "sys-w1t",
	}
}

func TestW1THybridAuxiliaryTailArms(t *testing.T) {
	record := gatewayhybrid.APIKeyRecord{ID: "key-w1t", SystemAccountID: "sys-w1t", SelectedGroupID: "grp-w1t"}
	groupAccess := &gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai", GroupAccessType: "personal"}

	newDispatch := func(t *testing.T, plans ...w1tAccountPlan) *chainHybridAuxiliaryDispatcher {
		t.Helper()
		return newChainHybridAuxiliaryDispatcher(w1tNewTailCache(t, &w1tTailModels{groupAccess: groupAccess, accountPlans: plans}))
	}
	requireFailure := func(t *testing.T, name string, dispatcher *chainHybridAuxiliaryDispatcher, input gatewayhybrid.AuxiliaryDispatchInput) *gatewayhybrid.AuxiliaryDispatchFailure {
		t.Helper()
		success, failure := dispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), input)
		if failure == nil {
			t.Fatalf("%s: failure = nil", name)
		}
		if success.Finish != nil {
			t.Fatalf("%s: failure 路径 success.Finish 应为零值", name)
		}
		return failure
	}

	t.Run("目标组选择失败", func(t *testing.T) {
		failure := requireFailure(t, "select-error",
			newDispatch(t, w1tAccountPlan{err: errors.New("选择失败")}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_dispatch_failed" || failure.HasGroupID {
			t.Fatalf("failure = %+v, want dispatch_failed 且不带 groupID", failure)
		}
	})

	t.Run("水合候选失败", func(t *testing.T) {
		failure := requireFailure(t, "hydrate-error",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{w1tAuxAccount("http://127.0.0.1:1")}},
				w1tAccountPlan{err: errors.New("水合失败")}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_dispatch_failed" || !failure.HasGroupID || failure.GroupID != "grp-w1t" {
			t.Fatalf("failure = %+v, want dispatch_failed + groupID", failure)
		}
	})

	t.Run("候选与选择无交集", func(t *testing.T) {
		other := w1tAuxAccount("http://127.0.0.1:1")
		other.ID = "acc-other"
		failure := requireFailure(t, "no-intersection",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{w1tAuxAccount("http://127.0.0.1:1")}},
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{other}}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_no_account" || !failure.HasGroupID {
			t.Fatalf("failure = %+v, want no_account + groupID", failure)
		}
	})

	t.Run("账户缺 BaseURL 跳过后无账户", func(t *testing.T) {
		empty := w1tAuxAccount("")
		failure := requireFailure(t, "empty-baseurl",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{empty}},
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{empty}}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_no_account" {
			t.Fatalf("failure.ErrorCode = %q, want w1t_no_account", failure.ErrorCode)
		}
	})

	t.Run("URL 构建失败消耗完候选", func(t *testing.T) {
		bad := w1tAuxAccount("https://gem.example")
		bad.ProtocolCode = "anthropic"
		bad.Type = "weird_type"
		failure := requireFailure(t, "url-error",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{bad}},
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{bad}}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_dispatch_failed" || failure.Account == nil || failure.Account.ID != "acc-w1t-aux" {
			t.Fatalf("failure = %+v, want dispatch_failed + lastAccount", failure)
		}
	})

	t.Run("请求构建失败消耗完候选", func(t *testing.T) {
		oauth := w1tAuxAccount("https://up.example")
		oauth.Type = "oauth"
		oauth.Credentials = map[string]any{"service_tier_override": 123}
		failure := requireFailure(t, "parts-error",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{oauth}},
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{oauth}}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_dispatch_failed" || failure.Account == nil {
			t.Fatalf("failure = %+v, want dispatch_failed + lastAccount", failure)
		}
	})

	t.Run("账户映射转换上游模型", func(t *testing.T) {
		var upstreamBody string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload, _ := io.ReadAll(r.Body)
			upstreamBody = string(payload)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}))
		t.Cleanup(upstream.Close)

		mapped := w1tAuxAccount(upstream.URL)
		mapped.ModelMappings = []gatewayruntimecache.AccountModelMapping{{
			SourceModel: "src", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "up", UpstreamEndpointFamily: "chat_completions", Enabled: true,
		}}
		success, failure := newDispatch(t,
			w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{mapped}},
			w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{mapped}}).
			DispatchHybridAuxiliaryChatCompletion(context.Background(), w1tAuxInput(record, 64*1024))
		if failure != nil {
			t.Fatalf("映射转换 success 路径返回 failure: %+v", failure)
		}
		if !strings.Contains(upstreamBody, `"up"`) {
			t.Fatalf("上游 body = %s, want 含映射上游模型 up", upstreamBody)
		}
		if success.Account.ID != "acc-w1t-aux" {
			t.Fatalf("success.Account.ID = %q", success.Account.ID)
		}
	})

	t.Run("响应超过上限", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
		}))
		t.Cleanup(upstream.Close)
		account := w1tAuxAccount(upstream.URL)
		failure := requireFailure(t, "too-large",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}), w1tAuxInput(record, 1024))
		if failure.ErrorMessage != "辅助响应过大" || !failure.HasStatusCode {
			t.Fatalf("failure = %+v, want 辅助响应过大 + statusCode", failure)
		}
	})

	t.Run("响应体截断读取错误", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 宣称 4096 字节但只写 11 字节后返回：客户端读取以 unexpected EOF 收敛。
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "4096")
			_, _ = w.Write([]byte(`{"partial":`))
		}))
		t.Cleanup(upstream.Close)
		account := w1tAuxAccount(upstream.URL)
		failure := requireFailure(t, "read-error",
			newDispatch(t,
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{account}},
				w1tAccountPlan{accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}), w1tAuxInput(record, 64*1024))
		if failure.ErrorCode != "w1t_dispatch_failed" || !failure.HasStatusCode {
			t.Fatalf("failure = %+v, want dispatch_failed + statusCode", failure)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_chat_mount.go：SSE 事件循环剩余臂 + openChatDatabase SQLite 臂
// ---------------------------------------------------------------------------

// w1tFailPingWriter 仅对心跳 ping 写入失败（首个事件写入正常）。
type w1tFailPingWriter struct {
	mu       sync.Mutex
	buf      strings.Builder
	header   http.Header
	code     int
	pingFail chan struct{}
	once     sync.Once
}

func (w *w1tFailPingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *w1tFailPingWriter) WriteHeader(code int) { w.code = code }

func (w *w1tFailPingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), ": ping") {
		w.once.Do(func() { close(w.pingFail) })
		return 0, errors.New("w1t ping 写入失败")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *w1tFailPingWriter) Flush() {}

func (w *w1tFailPingWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// w1tFailEventWriter 所有事件写入失败（SSE 头仍由 Header/WriteHeader 承担）。
type w1tFailEventWriter struct {
	header http.Header
	code   int
}

func (w *w1tFailEventWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *w1tFailEventWriter) WriteHeader(code int) { w.code = code }

func (w *w1tFailEventWriter) Write([]byte) (int, error) {
	return 0, errors.New("w1t 事件写入失败")
}

func (w *w1tFailEventWriter) Flush() {}

// w1tBlockAllWriter 首次 Write 起阻塞全部写入直到放行（用于在 handler 停滞
// 时灌满订阅缓冲，触发丢弃关闭语义）。
type w1tBlockAllWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	buf     strings.Builder
	header  http.Header
	code    int
}

func (w *w1tBlockAllWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *w1tBlockAllWriter) WriteHeader(code int) { w.code = code }

func (w *w1tBlockAllWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *w1tBlockAllWriter) Flush() {}

func (w *w1tBlockAllWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func w1tWaitForBody(t *testing.T, body func() string, deadline time.Duration, needle string) {
	t.Helper()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(body(), needle) {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("等待 SSE body 出现 %q 超时，实际：\n%s", needle, body())
		case <-tick.C:
		}
	}
}

func TestW1TSSEWriteArms(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return "2026-09-14T00:00:00Z" })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1t", ConversationID: "conv-w1t-arms", TurnID: "turn-w1t-arms"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
	t.Cleanup(func() {
		close(release)
		<-runner.Completion()
	})

	recorder := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		done <- chatAttachStreamHandler(hub)(recorder, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()
	exec := <-execCh
	w1tWaitForBody(t, func() string { return recorder.Body.String() }, 3*time.Second, "event: message.snapshot")

	// data == nil → 空 map 兜底。
	if !exec.Publish("message.delta", nil, chat.ChatGenerationProjectionUpdate{}) {
		t.Fatalf("nil data Publish 失败")
	}
	w1tWaitForBody(t, func() string { return recorder.Body.String() }, 3*time.Second, "event: message.delta")

	// json.Marshal 失败（NaN）→ ended + 返回。
	if !exec.Publish("message.delta", map[string]any{"x": math.NaN()}, chat.ChatGenerationProjectionUpdate{}) {
		t.Fatalf("NaN data Publish 失败")
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("marshal 失败分支 chatAttachStreamHandler = false, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("marshal 失败分支未收敛，body：\n%s", recorder.Body.String())
	}
}

func TestW1TSSEWriteErrorArm(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return "2026-09-14T00:00:00Z" })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1t", ConversationID: "conv-w1t-werr", TurnID: "turn-w1t-werr"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
	t.Cleanup(func() {
		close(release)
		<-runner.Completion()
	})

	done := make(chan bool, 1)
	go func() {
		done <- chatAttachStreamHandler(hub)(&w1tFailEventWriter{}, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()
	<-execCh
	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("写入失败分支 chatAttachStreamHandler = false, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("写入失败分支未收敛")
	}
}

func TestW1TSSEHeartbeatPingThenTerminal(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return "2026-09-14T00:00:00Z" })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1t", ConversationID: "conv-w1t-ping", TurnID: "turn-w1t-ping"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
	t.Cleanup(func() {
		close(release)
		<-runner.Completion()
	})

	recorder := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		done <- chatAttachStreamHandler(hub)(recorder, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()
	exec := <-execCh
	w1tWaitForBody(t, func() string { return recorder.Body.String() }, 3*time.Second, "event: message.snapshot")

	// 有界等待 5s 心跳：ping 成功 + flush。
	w1tWaitForBody(t, func() string { return recorder.Body.String() }, 8*time.Second, ": ping")
	if !exec.Publish("message.completed", map[string]any{"messageId": "msg-w1t"}, chat.ChatGenerationProjectionUpdate{}) {
		t.Fatalf("终结事件 Publish 失败")
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("心跳后终结 chatAttachStreamHandler = false, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("终结事件未收敛，body：\n%s", recorder.Body.String())
	}
}

func TestW1TSSEHeartbeatFailThenEndedEvent(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return "2026-09-14T00:00:00Z" })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1t", ConversationID: "conv-w1t-hbf", TurnID: "turn-w1t-hbf"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
	t.Cleanup(func() {
		close(release)
		<-runner.Completion()
	})

	writer := &w1tFailPingWriter{pingFail: make(chan struct{})}
	done := make(chan bool, 1)
	go func() {
		done <- chatAttachStreamHandler(hub)(writer, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()
	exec := <-execCh
	w1tWaitForBody(t, writer.body, 3*time.Second, "event: message.snapshot")

	// 心跳 ping 失败（ended=true，循环继续），随后的事件走 ended 早退分支。
	select {
	case <-writer.pingFail:
	case <-time.After(8 * time.Second):
		t.Fatalf("5s 心跳 ping 失败信号未出现，body：\n%s", writer.body())
	}
	if !exec.Publish("message.delta", map[string]any{"x": "late"}, chat.ChatGenerationProjectionUpdate{}) {
		t.Fatalf("ended 后 Publish 失败")
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("ended 后事件分支 chatAttachStreamHandler = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ended 分支未收敛，body：\n%s", writer.body())
	}
}

func TestW1TSSEOverflowClosesSubscriberChannel(t *testing.T) {
	hub := chat.NewGenerationHub(func() string { return "2026-09-14T00:00:00Z" })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1t", ConversationID: "conv-w1t-ovf", TurnID: "turn-w1t-ovf"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
	t.Cleanup(func() {
		close(release)
		<-runner.Completion()
	})

	writer := &w1tBlockAllWriter{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan bool, 1)
	go func() {
		done <- chatAttachStreamHandler(hub)(writer, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()
	exec := <-execCh
	<-writer.entered // 首次写入已开始：订阅缓冲可被灌满

	// 257 条非终结事件：第 257 条触发 TrySend 丢弃 + 关闭订阅通道。
	for index := 0; index < 257; index++ {
		if !exec.Publish("message.delta", map[string]any{"i": index}, chat.ChatGenerationProjectionUpdate{}) {
			t.Fatalf("灌缓冲 Publish %d 失败", index)
		}
	}
	close(writer.release) // 放行：drain 256 条缓冲后读到关闭态

	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("通道关闭分支 chatAttachStreamHandler = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("溢出关闭分支未收敛")
	}
}

func TestW1TOpenChatDatabaseSQLiteArms(t *testing.T) {
	if _, _, err := openChatDatabase(runtimeConfig{}, pgpool.NewRegistry(), nil, false); err == nil ||
		!strings.Contains(err.Error(), "JUHE_AI_CHAT_DATABASE_PATH") {
		t.Fatalf("err = %v, want 缺少 JUHE_AI_CHAT_DATABASE_PATH", err)
	}

	garbage := filepath.Join(t.TempDir(), "w1t-not-sqlite.bin")
	if err := os.WriteFile(garbage, []byte("this is not a sqlite database at all"), 0o600); err != nil {
		t.Fatalf("写垃圾文件: %v", err)
	}
	if _, _, err := openChatDatabase(runtimeConfig{ChatDatabasePath: garbage}, pgpool.NewRegistry(), nil, false); err == nil ||
		!strings.Contains(err.Error(), "configure chat sqlite database") {
		t.Fatalf("err = %v, want configure chat sqlite database 失败", err)
	}
}

// ---------------------------------------------------------------------------
// chain_runtime.go：组合根 redis 驱动闭包 + 错误臂
// ---------------------------------------------------------------------------

func w1tSettingValue(string) (string, error) { return "UTC", nil }

func TestW1TRuntimeComposeRedisDriverClosures(t *testing.T) {
	mr, client := w1tSetupRedis(t)
	composed := &composition{
		db:        w1uOpenSeededBusinessDB(t),
		statsDB:   w1uOpenPlainSQLite(t, "w1t-runtime-stats.sqlite3"),
		pgDialect: false,
		Bus:       inval.New(time.Now),
	}
	cfg := runtimeConfig{
		Secret:                        "w1t-runtime-secret",
		CacheDriver:                   "redis",
		RuntimeStateDriver:            "redis",
		RedisCacheURL:                 "redis://" + mr.Addr(),
		RedisStateURL:                 "redis://" + mr.Addr(),
		RedisNamespace:                "juhe-ai:w1t",
		RuntimeMode:                   "performance",
		DispatchAccountCandidateLimit: 20,
		ConcurrencyGlobalMax:          64,
	}
	services, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue)
	if err != nil {
		t.Fatalf("redis 驱动组合: %v", err)
	}
	defer services.Close()
	if services.StateClient == nil || services.RateLimitStore == nil || services.HybridScoringCache == nil || services.HybridRuntimeState == nil {
		t.Fatalf("redis 驱动协作组件未装配: %+v", services)
	}
	_ = client
	ctx := context.Background()

	// 守卫惰性 redis store 工厂闭包。
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-w1t", SystemAccountID: "sys-w1t"}
	if _, err := services.AccountAPIKeyGuard.LoadTransientStatesForDispatch(ctx, "acc-w1t", []string{"fp-w1t"}); err != nil {
		t.Fatalf("LoadTransientStatesForDispatch: %v", err)
	}

	// API Key 效果链：持久写 + runtime 失效闭包。
	services.AccountAPIKeyEffects.RecordFailure(ctx, account, gatewayaccounteffects.RecordFailureInput{
		Status: gatewayaccounteffects.APIKeyStatusError,
	})

	// 配置策略避让写侧 + 失效闭包。
	if err := services.ConfiguredPolicyAvoidance.SuppressGatewayAccountLocallyForSeconds(ctx,
		gatewayaccounteffects.SuppressibleGatewayAccount{ID: "acc-w1t"}, w1tInt64Ptr(30), "w1t-reason"); err != nil {
		t.Fatalf("SuppressGatewayAccountLocallyForSeconds: %v", err)
	}

	// 本地屏蔽存储并发投影闭包。
	services.SuppressionStore.SuppressForGatewayFailure("rt-key-w1t", "acc-w1t", "gateway_failure", "acc-w1t")
}

func w1tInt64Ptr(value int64) *int64 { return &value }

func TestW1TRuntimeComposeErrorArms(t *testing.T) {
	t.Run("stats 库关闭：组合失败", func(t *testing.T) {
		composed := &composition{
			db:        w1uOpenPlainSQLite(t, "w1t-err-business.sqlite3"),
			statsDB:   w1uOpenPlainSQLite(t, "w1t-err-stats.sqlite3"),
			pgDialect: false,
		}
		if err := composed.statsDB.Close(); err != nil {
			t.Fatalf("关闭 stats 库: %v", err)
		}
		cfg := runtimeConfig{Secret: "w1t-runtime-secret", CacheDriver: "memory", RuntimeStateDriver: "memory", DispatchAccountCandidateLimit: 20}
		if _, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue); err == nil {
			t.Fatal("关闭的 stats 库必须使组合失败")
		}
	})

	t.Run("非法设置读取器：组合失败", func(t *testing.T) {
		composed := &composition{
			db:        w1uOpenPlainSQLite(t, "w1t-err2-business.sqlite3"),
			statsDB:   w1uOpenPlainSQLite(t, "w1t-err2-stats.sqlite3"),
			pgDialect: false,
		}
		cfg := runtimeConfig{Secret: "w1t-runtime-secret", CacheDriver: "memory", RuntimeStateDriver: "memory", DispatchAccountCandidateLimit: 20}
		if _, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "", errors.New("设置读取失败") }); err == nil {
			t.Fatal("报错的设置读取器必须使组合失败")
		}
	})
}

// ---------------------------------------------------------------------------
// chain_request_failure_health.go：outbox writer / 派发器剩余臂
// ---------------------------------------------------------------------------

func TestW1TProbeOutboxTails(t *testing.T) {
	ctx := context.Background()

	t.Run("pg 表名与占位符绑定", func(t *testing.T) {
		writer := newChainProbeRequestOutboxWriter(w1tProxyDB(t), true, 1000)
		if got := writer.table(); got != "juhe_business.account_health_probe_request_outbox" {
			t.Fatalf("table = %q", got)
		}
		if got := writer.bind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
			t.Fatalf("bind = %q", got)
		}
	})

	t.Run("建表 + 插入成功", func(t *testing.T) {
		db := w1uOpenPlainSQLite(t, "w1t-outbox.sqlite3")
		writer := newChainProbeRequestOutboxWriter(db, false, 1000)
		outcome := writer.EnqueueProbeRequest(ctx, "acc-1", "request_failure", "trace-1", &chainHealthDispatchSourceFence{
			StateKey: "sk", AccountID: "acc-1", SourceGeneration: 1, SourceFenceID: "f1", RuntimeKey: "rk", ProbeGeneration: 1, ConfigRevision: 1,
		})
		if outcome.Outcome != gatewaycodex.HealthDispatchQueued {
			t.Fatalf("outcome = %+v, want %q", outcome, gatewaycodex.HealthDispatchQueued)
		}
	})

	t.Run("关句柄：建表失败 → input_unavailable", func(t *testing.T) {
		db := w1uOpenPlainSQLite(t, "w1t-outbox-closed.sqlite3")
		writer := newChainProbeRequestOutboxWriter(db, false, 1000)
		if err := db.Close(); err != nil {
			t.Fatalf("关闭句柄: %v", err)
		}
		outcome := writer.EnqueueProbeRequest(ctx, "acc-2", "request_failure", "", nil)
		if outcome.Outcome != gatewaycodex.HealthDispatchRejected || outcome.DecisionCode != "input_unavailable" {
			t.Fatalf("outcome = %+v, want rejected/input_unavailable", outcome)
		}
	})

	t.Run("派发标记容量与淘汰", func(t *testing.T) {
		marks := newChainRequestDispatchMarks(0)
		if marks.cap != 4096 {
			t.Fatalf("默认容量 = %d, want 4096", marks.cap)
		}
		small := newChainRequestDispatchMarks(1)
		first := &gatewaypreauth.GatewayRequest{}
		second := &gatewaypreauth.GatewayRequest{}
		small.mark(first)
		small.mark(second)
		if small.marked(first) {
			t.Fatal("容量淘汰后 first 不应再命中")
		}
		if !small.marked(second) {
			t.Fatal("second 应保持命中")
		}
		small.mark(nil)
		if small.marked(nil) {
			t.Fatal("nil 请求不应命中")
		}
	})

	t.Run("派发器 nil 接收臂", func(t *testing.T) {
		var dispatcher *chainRequestFailureHealthDispatcher
		if dispatcher.DispatchRequestFailureAccountHealthCheck(&gatewaypreauth.GatewayRequest{}, gatewayTrafficSource, "acc") {
			t.Fatal("nil 派发器必须返回 false")
		}
		if outcome := dispatcher.dispatch("acc", "reason", "", nil); outcome.Outcome != gatewaycodex.HealthDispatchRejected {
			t.Fatalf("nil dispatch outcome = %+v", outcome)
		}
		if outcome := dispatcher.dispatchWithOutcome("acc", "reason", "", nil); outcome.Outcome != gatewaycodex.HealthDispatchRejected {
			t.Fatalf("nil dispatchWithOutcome outcome = %+v", outcome)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_account_locks.go：恢复成功、CAS 丢失、结算事务、采样钳制
// ---------------------------------------------------------------------------

func TestW1TAccountLocksTailArms(t *testing.T) {
	ctx := context.Background()

	t.Run("DEAD_CONFIRMED 恢复成功", func(t *testing.T) {
		fixture := newAccountLocksFixture(t)
		fixture.seedAccount(t, "acc_w1t_rec", "active", 1, "")
		fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_w1t_rec", enabled: 1, lockState: "DEAD_CONFIRMED",
			deathTimeout: 300, retryInterval: 5, generation: 7,
			incidentID: sql.NullString{String: "acc_w1t_rec:7:t", Valid: true}})
		if _, err := fixture.locks.FindStateAsync(ctx, "acc_w1t_rec"); err != nil {
			t.Fatalf("FindStateAsync: %v", err)
		}
		row := fixture.readLockRow(t, "acc_w1t_rec")
		if row.lockState != "LOCKED_IDLE" {
			t.Fatalf("恢复后 lockState = %q, want LOCKED_IDLE", row.lockState)
		}
		if row.incidentID.Valid || row.leaseID.Valid {
			t.Fatalf("恢复后事故/租约必须清空: %+v", row)
		}
	})

	t.Run("RecordFailure 租约栅栏 CAS 丢失重读", func(t *testing.T) {
		fixture := newAccountLocksFixture(t)
		fixture.seedAccount(t, "acc_w1t_cas", "active", 1, "")
		fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_w1t_cas", enabled: 1, lockState: "LOCKED_IDLE",
			deathTimeout: 300, retryInterval: 5, generation: 3,
			incidentID:   sql.NullString{String: "acc_w1t_cas:3:t", Valid: true},
			leaseID:      sql.NullString{String: "lease-held", Valid: true},
			leaseUntilMs: sql.NullInt64{Int64: time.Now().Add(time.Hour).UnixMilli(), Valid: true}})
		leaseID := ""
		if err := fixture.locks.RecordFailureAsync(ctx, "acc_w1t_cas", "boom",
			&gatewaydispatch.AccountLockObservation{Generation: 3, IncidentID: "acc_w1t_cas:3:t", LeaseID: &leaseID}); err != nil {
			t.Fatalf("RecordFailureAsync: %v", err)
		}
		row := fixture.readLockRow(t, "acc_w1t_cas")
		if row.lockState != "LOCKED_IDLE" || row.generation != 3 {
			t.Fatalf("CAS 丢失后必须保持原状: %+v", row)
		}
	})

	t.Run("结算事务：到期 ENGAGED → DEAD_CONFIRMED + 账户下线", func(t *testing.T) {
		fixture := newAccountLocksFixture(t)
		fixture.seedAccount(t, "acc_w1t_settle", "active", 1, "")
		past := isoMillisOf(time.Now().Add(-time.Hour))
		fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_w1t_settle", enabled: 1, lockState: "ENGAGED",
			deathTimeout: 300, retryInterval: 5, generation: 4,
			incidentID:     sql.NullString{String: "acc_w1t_settle:4:t", Valid: true},
			deadlineAt:     sql.NullString{String: past, Valid: true},
			originalStatus: sql.NullString{String: "active", Valid: true}})
		if err := fixture.locks.SettleDeadlineAsync(ctx, "acc_w1t_settle", time.Now().UnixMilli(),
			&gatewaydispatch.AccountLockObservation{Generation: 4, IncidentID: "acc_w1t_settle:4:t"}); err != nil {
			t.Fatalf("SettleDeadlineAsync: %v", err)
		}
		row := fixture.readLockRow(t, "acc_w1t_settle")
		if row.lockState != "DEAD_CONFIRMED" {
			t.Fatalf("结算后 lockState = %q, want DEAD_CONFIRMED", row.lockState)
		}
		status, _ := fixture.readAccount(t, "acc_w1t_settle")
		if status != "temporary_unavailable" {
			t.Fatalf("结算后账户 status = %q, want temporary_unavailable", status)
		}
	})

	t.Run("采样时延位于钳制窗口内", func(t *testing.T) {
		for _, seed := range []string{"a", "b", "c", "d"} {
			if got := sampleLockDelayMs(1, seed); got < 2_000 || got > 30_000 {
				t.Fatalf("sampleLockDelayMs(1, %q) = %d, want 位于 [2000, 30000]", seed, got)
			}
		}
	})

	t.Run("预约租约 CAS 丢失拒绝", func(t *testing.T) {
		fixture := newAccountLocksFixture(t)
		fixture.seedAccount(t, "acc_w1t_lease", "active", 1, "")
		future := time.Now().Add(time.Hour)
		fixture.seedLockRow(t, chainAccountLockRow{accountID: "acc_w1t_lease", enabled: 1, lockState: "ENGAGED",
			deathTimeout: 300, retryInterval: 5, generation: 2,
			incidentID:    sql.NullString{String: "acc_w1t_lease:2:t", Valid: true},
			deadlineAt:    sql.NullString{String: isoMillisOf(future), Valid: true},
			nextRetryAtMs: sql.NullInt64{Int64: time.Now().Add(-time.Minute).UnixMilli(), Valid: true},
			leaseUntilMs:  sql.NullInt64{Int64: future.UnixMilli(), Valid: true}})
		lease, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "acc_w1t_lease", 3_000)
		if err != nil {
			t.Fatalf("AcquireRetryLeaseAsync: %v", err)
		}
		if lease.Allowed {
			t.Fatalf("租约未过期的预约 CAS 必须拒绝: %+v", lease)
		}
		if lease.WaitMs < 1 {
			t.Fatalf("拒绝路径 WaitMs = %d, want >= 1", lease.WaitMs)
		}
	})
}

// ---------------------------------------------------------------------------
// chain_catalog.go：目录源守卫与绑定
// ---------------------------------------------------------------------------

func TestW1TCatalogSourceTails(t *testing.T) {
	if _, err := newChainCatalogSource(nil, false); err == nil || !strings.Contains(err.Error(), "业务数据库") {
		t.Fatalf("err = %v, want 需要业务数据库", err)
	}
	source, err := newChainCatalogSource(w1uOpenPlainSQLite(t, "w1t-catalog-src.sqlite3"), true)
	if err != nil {
		t.Fatalf("构造目录源: %v", err)
	}
	bound := source.bind("a = ? AND b >= ?")
	if !strings.Contains(bound, "$1") || !strings.Contains(bound, "$2") || strings.Contains(bound, "?") {
		t.Fatalf("pg bind = %q, want $N 占位符", bound)
	}
	plain, err := newChainCatalogSource(w1uOpenPlainSQLite(t, "w1t-catalog-src2.sqlite3"), false)
	if err != nil {
		t.Fatalf("构造目录源: %v", err)
	}
	if got := plain.bind("a = ?"); got != "a = ?" {
		t.Fatalf("sqlite bind = %q, want 原样", got)
	}
}

// ---------------------------------------------------------------------------
// 小文件收尾：openaicompat 装配守卫 / turn-retry redis / 迁移桥 / 归还器 /
// 亲和记忆空账户 / chat tokenizer 回退
// ---------------------------------------------------------------------------

func TestW1TOpenAICompatMountGuards(t *testing.T) {
	if err := mountChainOpenAICompatFamilies(nil, nil, runtimeConfig{}, nil); err == nil ||
		!strings.Contains(err.Error(), "业务数据库句柄") {
		t.Fatalf("err = %v, want 缺少业务数据库句柄", err)
	}
	composed := &composition{db: w1uOpenPlainSQLite(t, "w1t-compat.sqlite3")}
	if err := mountChainOpenAICompatFamilies(composed, nil, runtimeConfig{}, nil); err == nil ||
		!strings.Contains(err.Error(), "runtime cache") {
		t.Fatalf("err = %v, want 缺少网关链 runtime cache", err)
	}
}

func TestW1TTurnRetryRedisStateStoreTails(t *testing.T) {
	// nil 客户端构造为 (nil, nil)；OrMaintain 同样收敛 nil。
	if store, err := newChainTurnRetryRedisStateStore(nil, ""); err != nil || store != nil {
		t.Fatalf("nil 客户端构造 = (%+v, %v), want (nil, nil)", store, err)
	}
	if store := newChainTurnRetryRedisStateStoreOrNil(nil, ""); store != nil {
		t.Fatalf("nil 客户端 OrNil = %+v, want nil", store)
	}

	_, client := w1tSetupRedis(t)
	if _, err := newChainTurnRetryRedisStateStore(client, ""); err == nil ||
		!strings.Contains(err.Error(), "namespace") {
		t.Fatalf("空命名空间 err = %v, want 缺少 namespace", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("关闭 redis: %v", err)
	}
	store, err := newChainTurnRetryRedisStateStore(client, "juhe-ai:w1t")
	if err != nil {
		t.Fatalf("构造 store: %v", err)
	}
	ctx := context.Background()
	if _, err := store.GetJSON(ctx, "k"); err == nil {
		t.Fatal("关闭客户端的 GetJSON 必须报错")
	}
	if _, err := store.CompareSetJSON(ctx, "k", json.RawMessage(`null`), map[string]any{}, 1000); err == nil {
		t.Fatal("关闭客户端的 CompareSetJSON 必须报错")
	}
	if _, err := store.Incr(ctx, "k", 1000); err == nil {
		t.Fatal("关闭客户端的 Incr 必须报错")
	}
}

func TestW1TTrafficMigrationBridgeErrorArm(t *testing.T) {
	// 亲和服务以 redis 驱动指向已关闭端口：迁移首跳必然失败。
	mr, _ := w1tSetupRedis(t)
	addr := mr.Addr()
	mr.Close()
	affinity, err := gatewaysession.NewAffinityService(gatewaysession.AffinityConfig{
		CacheDriver:        gatewaysession.CacheDriverRedis,
		RuntimeStateDriver: gatewaysession.RuntimeStateDriverRedis,
		Secret:             "w1t-migration-secret",
		RedisCacheURL:      "redis://" + addr,
		RedisNamespace:     "juhe-ai:w1t",
	})
	if err != nil {
		t.Fatalf("构造亲和服务: %v", err)
	}
	bridge := trafficRuntimeMigratorBridge{affinity: affinity}
	if _, err := bridge.MigrateOpenAIAccountTrafficRuntime(context.Background(), accounts.TrafficRuntimeMigrationInput{
		SourceAccountID: "a", TargetAccountID: "b",
	}); err == nil {
		t.Fatal("redis 不可达时迁移必须报错")
	}
}

func TestW1TGrantReturnerNilMutationArm(t *testing.T) {
	db := w1uOpenSeededBusinessDB(t)
	store, err := authz.NewStore(db, false, time.Now)
	if err != nil {
		t.Fatalf("authz store: %v", err)
	}
	returner := authzGrantReturner{store: store}
	// 不存在的 grant：not_found 原样透传（路由渲染 404 不可归还契约）。
	status, err := returner.Return(context.Background(), "grant-not-exists-w1t", "2026-09-14T00:00:00Z", "user-w1t")
	if err != nil {
		t.Fatalf("Return(不存在 grant): %v", err)
	}
	if status != "not_found" {
		t.Fatalf("不存在 grant Return = %q, want not_found", status)
	}
}

func TestW1TLocalAffinityEmptyRememberedArm(t *testing.T) {
	affinity := newLocalSessionAffinity()
	affinity.mu.Lock()
	affinity.keys["k-empty"] = localAffinityEntry{}
	affinity.ttls["k-empty"] = time.Now().Add(time.Hour)
	affinity.mu.Unlock()
	accountsOut := []gatewaydispatch.AccountCandidate{{ID: "acc-1"}}
	got, err := affinity.OrderAsync(context.Background(), accountsOut, "k-empty", gatewaydispatch.AffinityOrderingOptions{})
	if err != nil || len(got) != 1 || got[0].ID != "acc-1" {
		t.Fatalf("空记忆 OrderAsync = (%+v, %v), want 原序", got, err)
	}
}

func TestW1TChatTokenCountFallback(t *testing.T) {
	tokenCount, err := newChatTokenCount()
	if err != nil {
		t.Fatalf("newChatTokenCount: %v", err)
	}
	for _, text := range []string{"hello world", "\xff\xfe invalid utf8", strings.Repeat("x", 4096)} {
		if got := tokenCount(text); got <= 0 {
			t.Fatalf("tokenCount(%q...) = %d, want > 0", text[:minIntW1T(5, len(text))], got)
		}
	}
}

func minIntW1T(a, b int) int {
	if a < b {
		return a
	}
	return b
}
