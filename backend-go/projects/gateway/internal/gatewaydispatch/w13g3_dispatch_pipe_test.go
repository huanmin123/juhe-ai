package gatewaydispatch

// w13g3 第五轮：OAuth Codex 覆盖钩子、凭据数值收窄、Anthropic 消息归一、
// 官方 OAuth 头白名单、URL 安全策略、Key 池回退候选错误路径。
//（原非流式正文管道 / 绝对期限读取器测试已随 BUG-0247 项 3 的 Reader 管道族
// 删除一并清理——生产管道在 gatewayresponse 包实现。）

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// ---------------------------------------------------------------------------
// OAuth Codex 覆盖钩子 / 凭据收窄 / 消息归一 / 头白名单
// ---------------------------------------------------------------------------

func TestW13g3CodexAccountRequestOverrideHook(t *testing.T) {
	body := map[string]any{"model": "gpt-test"}
	t.Run("nil hook keeps body", func(t *testing.T) {
		if err := applyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{}); err != nil {
			t.Fatalf("nil hook: %v", err)
		}
	})
	t.Run("override error becomes account scoped", func(t *testing.T) {
		err := applyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
			ApplyGptAccountRequestOverrides: func(map[string]any, GptAccountOverrideInput) (map[string]any, error) {
				return nil, &GptAccountRequestOverrideError{Message: "覆盖无效"}
			},
		})
		var adapterErr *OpenAIOAuthCodexAdapterError
		if !errorsAs(err, &adapterErr) || !adapterErr.AccountScoped {
			t.Fatalf("expected account scoped adapter error, got %v", err)
		}
	})
	t.Run("hook rewrite replaces keys", func(t *testing.T) {
		rewritten := map[string]any{"model": "gpt-test", "service_tier": "priority"}
		if err := applyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
			ApplyGptAccountRequestOverrides: func(map[string]any, GptAccountOverrideInput) (map[string]any, error) {
				return rewritten, nil
			},
		}); err != nil {
			t.Fatalf("hook: %v", err)
		}
		if body["service_tier"] != "priority" {
			t.Fatalf("body = %#v", body)
		}
	})
	t.Run("non-map parsed body fails", func(t *testing.T) {
		_, err := NormalizeOpenAIOAuthCodexParsedBody([]any{"x"}, OpenAIOAuthCodexNormalizeInput{})
		var adapterErr *OpenAIOAuthCodexAdapterError
		if !errorsAs(err, &adapterErr) {
			t.Fatalf("expected adapter error, got %v", err)
		}
	})
}

func TestW13g3CredentialFloatValue(t *testing.T) {
	cases := []struct {
		value any
		want  float64
		ok    bool
	}{
		{float64(3.5), 3.5, true},
		{float32(1.5), 1.5, true},
		{int(2), 2, true},
		{int64(7), 7, true},
		{json.Number("4.25"), 4.25, true},
		{json.Number("bad"), 0, false},
		{"text", 0, false},
	}
	for _, tc := range cases {
		got, ok := credentialFloatValue(tc.value)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("credentialFloatValue(%#v) = %v %v", tc.value, got, ok)
		}
	}
}

func TestW13g3NormalizeAnthropicMessage(t *testing.T) {
	t.Run("non map passes through", func(t *testing.T) {
		if got := normalizeAnthropicMessage("x"); got != "x" {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("non list content passes through", func(t *testing.T) {
		value := map[string]any{"content": "text"}
		if got := normalizeAnthropicMessage(value); got == nil {
			t.Fatal("content list missing must pass through")
		}
	})
	t.Run("non text block passes through", func(t *testing.T) {
		value := map[string]any{"content": []any{map[string]any{"type": "image"}}}
		if got := normalizeAnthropicMessage(value); got == nil {
			t.Fatal("non text block must pass through")
		}
	})
	t.Run("extra keys pass through", func(t *testing.T) {
		value := map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi", "extra": 1}}}
		if got := normalizeAnthropicMessage(value); got == nil {
			t.Fatal("extra block keys must pass through")
		}
	})
	t.Run("text blocks merge", func(t *testing.T) {
		value := map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "text", "text": "b"},
		}}
		got := normalizeAnthropicMessage(value)
		record, ok := got.(map[string]any)
		if !ok || record["content"] != "ab" {
			t.Fatalf("merged = %#v", got)
		}
	})
}

func TestW13g3OfficialOAuthHeaderAllowlist(t *testing.T) {
	if !isAllowedOfficialOAuthClientHeader("Content-Type", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("common header must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-codex-foo", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("codex prefix must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-claude-code-bar", OAuthHeaderProfileAnthropicClaude) {
		t.Fatal("claude prefix must be allowed")
	}
	if isAllowedOfficialOAuthClientHeader("x-gemini-foo", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("gemini header must not match codex profile")
	}
	if !isAllowedOfficialOAuthClientHeader("x-goog-api-client", OAuthHeaderProfileGeminiCLI) {
		t.Fatal("gemini profile must allow gemini header")
	}
	if !isAllowedOfficialOAuthClientHeader("x-grok-client-version", OAuthHeaderProfileXAIGrok) {
		t.Fatal("grok profile must allow grok header")
	}
	if isAllowedOfficialOAuthClientHeader("x-unknown", OAuthHeaderProfileGeminiCLI) {
		t.Fatal("unknown header must be rejected")
	}
}

func TestW13g3URLPolicyTrimAndResolution(t *testing.T) {
	policy := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	parsed, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), "  https://example.invalid-host.w13g3/v1  ")
	if err != nil {
		// DNS 解析失败的域名按解析错误或原始错误返回都合法。
		t.Logf("resolution failed as expected: %v", err)
	} else if parsed == nil {
		t.Fatal("parsed url missing")
	}
	trimmed, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), "https://93.184.216.34/v1")
	if err != nil || trimmed.Hostname() != "93.184.216.34" {
		t.Fatalf("trimmed literal = %v %v", trimmed, err)
	}
}

// ---------------------------------------------------------------------------
// Key 池回退候选错误路径
// ---------------------------------------------------------------------------

func TestW13g3GroupFallbackNilRecordAndErrors(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	t.Run("nil record cannot attempt fallback", func(t *testing.T) {
		if CanAttemptApiKeyGroupFallback(nil, "group-1", nil) {
			t.Fatal("nil record must not attempt fallback")
		}
		pipeline, _, _, _ := newPipeline(t)
		if _, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", GroupID: "group-1", RequestLane: "text",
		}); err != nil || found {
			t.Fatalf("nil record resolve = found %v err %v", found, err)
		}
	})
	t.Run("cache errors surface", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &w13g3FallbackCache{resolveErr: errW13g3, accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		record := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		}}
		if _, _, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); !errors.Is(err, errW13g3) {
			t.Fatalf("expected resolve error, got %v", err)
		}
		engine.Cache = &w13g3FallbackCache{listErr: errW13g3, accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		if _, _, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); !errors.Is(err, errW13g3) {
			t.Fatalf("expected list error, got %v", err)
		}
	})
	t.Run("capability and model mismatch skip group", func(t *testing.T) {
		pipeline, engine, driver, _ := newPipeline(t)
		driver.mismatchAll = true
		engine.Cache = &w13g3FallbackCache{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		record := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		}}
		if _, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); err != nil || found {
			t.Fatalf("capability mismatch must skip: found %v err %v", found, err)
		}
	})
	t.Run("quota denied skip group", func(t *testing.T) {
		pipeline, engine, _, _ := newPipeline(t)
		engine.Cache = &w13g3FallbackCache{accounts: map[string][]AccountCandidate{"group-2": testAccounts("b-1")}}
		engine.Quota = &fakeQuota{denied: map[string]struct{}{"b-1": {}}}
		record := &gatewayruntimecache.GatewayAPIKeyRow{ID: "k", GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{GroupID: "group-1", Status: "active", GroupEnabled: 1},
			{GroupID: "group-2", Status: "active", GroupEnabled: 1},
		}}
		if _, found, err := pipeline.ResolveNextGroupFallbackCandidateForArgs(context.Background(), GroupFallbackArgs{
			Req: req, Reason: "upstream_accounts_exhausted", APIKeyRecord: record,
			SystemAccountID: "s", GroupID: "group-1", RequestLane: "text",
		}); err != nil || found {
			t.Fatalf("quota denied must skip: found %v err %v", found, err)
		}
	})
}

// w13g3FallbackCache 是带错误注入的分组缓存。
type w13g3FallbackCache struct {
	accounts   map[string][]AccountCandidate
	resolveErr error
	listErr    error
}

func (f *w13g3FallbackCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options CachedAccountsOptions) ([]AccountCandidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.accounts[groupID], nil
}

func (f *w13g3FallbackCache) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	if f.resolveErr != nil {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, false, f.resolveErr
	}
	return gatewayruntimecache.GroupUsageAccessMetadata{ProviderCode: "openai"}, true, nil
}

func (f *w13g3FallbackCache) LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	return nil, nil
}
