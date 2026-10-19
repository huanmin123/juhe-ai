package gatewayoauthcodex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// T14 + T17 收敛验收钉子（api_key 出站链合并入口
// ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody）：
//   - 目录端口每请求恰 1 次调用（T14 完成判定，计数替身钉住）；
//   - request 级已解析 map 被复用且永不被改写（T17，哨兵键判别）；
//   - 双形状输入（reasoning / reasoning_effort 并存、仅其一、都无 ×
//     chat/responses 族）逐字节输出与改造前两步管线一致（差分 oracle +
//     手算 golden 字面量双重锚定）；
//   - 非法凭据 token 三态与非法 JSON body 的错误文案逐字保留（T14）。

// countingOverrideCatalog 记录目录端口调用次数（T14 计数钉住）。
type countingOverrideCatalog struct {
	mock  mockGptOverrideCatalog
	calls int
}

func (c *countingOverrideCatalog) ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]GptRequestOverrideModelCatalogItem, error) {
	c.calls++
	return c.mock.ListGptRequestOverrideModelCatalog(ctx, providerCode, systemAccountID, includeUnpriced)
}

func mergedOverrideAccount(credentials map[string]any) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                          "acc-merged",
		VendorCode:                  "gpt",
		AccountOwnerSystemAccountID: "sys-merged",
		Credentials:                 credentials,
	}
}

func fullCapabilityCatalog() []GptRequestOverrideModelCatalogItem {
	return []GptRequestOverrideModelCatalogItem{{
		Model:                     "gpt-test",
		SupportedServiceTiers:     []string{"priority"},
		SupportedReasoningEfforts: []string{"high"},
	}}
}

// TestMergedOverridesCatalogCalledExactlyOnce 钉住 T14：覆盖命中时目录端口
// 每请求恰 1 次；覆盖惰性 / 供应商身份为空时 0 次。
func TestMergedOverridesCatalogCalledExactlyOnce(t *testing.T) {
	catalog := &countingOverrideCatalog{mock: mockGptOverrideCatalog{items: fullCapabilityCatalog()}}
	runtime := GptRequestOverrideRuntime{Catalog: catalog}

	account := mergedOverrideAccount(map[string]any{
		"service_tier_override":     "priority",
		"reasoning_effort_override": "high",
	})
	body := []byte(`{"model":"gpt-test","input":"hi","reasoning":{"effort":"low"},"reasoning_effort":"low"}`)
	out, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, account, "responses", false, "gpt-test", "/v1/responses", runtime)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(out), `"service_tier":"priority"`) || !strings.Contains(string(out), `"effort":"high"`) {
		t.Fatalf("overrides must apply: %s", out)
	}
	if catalog.calls != 1 {
		t.Fatalf("目录端口调用次数 = %d, want 恰 1 次", catalog.calls)
	}

	// 无覆盖配置 → 早退不触发目录查询；reasoning 归一不涉及时 body 原样返回。
	plain := mergedOverrideAccount(nil)
	catalog.calls = 0
	neutralBody := []byte(`{"model":"gpt-test","input":"hi"}`)
	out, err = ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), neutralBody, nil, plain, "responses", false, "gpt-test", "/v1/responses", runtime)
	if err != nil || string(out) != string(neutralBody) {
		t.Fatalf("inert body must stay untouched: %s err=%v", out, err)
	}
	if catalog.calls != 0 {
		t.Fatalf("惰性路径不应查询目录，实际 %d 次", catalog.calls)
	}

	// 供应商身份为空 → 覆盖惰性，不查目录。
	emptyVendor := mergedOverrideAccount(map[string]any{"service_tier_override": "priority"})
	emptyVendor.VendorCode = ""
	catalog.calls = 0
	if _, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, emptyVendor, "responses", false, "gpt-test", "/v1/responses", runtime); err != nil {
		t.Fatalf("空 vendor 覆盖必须惰性: %v", err)
	}
	if catalog.calls != 0 {
		t.Fatalf("空 vendor 不应查询目录，实际 %d 次", catalog.calls)
	}
}

// TestMergedOverridesReusesRequestLevelParsedMap 钉住 T17：调用方传入的
// request 级解析 map 被直接复用（哨兵键只存在于 map、不存在于字节流，输出
// 含哨兵即证明未重新解析 body），且 map 本体不被改写（深拷贝后变异）。
func TestMergedOverridesReusesRequestLevelParsedMap(t *testing.T) {
	catalog := &countingOverrideCatalog{mock: mockGptOverrideCatalog{items: fullCapabilityCatalog()}}
	runtime := GptRequestOverrideRuntime{Catalog: catalog}
	account := mergedOverrideAccount(map[string]any{"service_tier_override": "priority"})
	body := []byte(`{"model":"gpt-test","input":"hi"}`)
	parsedBody := map[string]any{
		"model":    "gpt-test",
		"input":    "hi",
		"sentinel": true,
	}
	out, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, parsedBody, account, "responses", false, "gpt-test", "/v1/responses", runtime)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(out), `"sentinel":true`) {
		t.Fatalf("caller-parsed map must be reused verbatim (sentinel lost): %s", out)
	}
	if !strings.Contains(string(out), `"service_tier":"priority"`) {
		t.Fatalf("overrides must apply: %s", out)
	}
	if _, mutated := parsedBody["service_tier"]; mutated {
		t.Fatalf("caller map must stay read-only: %v", parsedBody)
	}

	// 同一 body 不传 map：走函数内解析，输出无哨兵（字节同源解析路径）。
	out, err = ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, account, "responses", false, "gpt-test", "/v1/responses", runtime)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.Contains(string(out), "sentinel") {
		t.Fatalf("nil parsedBody must parse the bytes themselves: %s", out)
	}
}

// goldenMergedCases 是手算 golden 字面量：json.Marshal(map[string]any) 键序
// 确定性（字典序）下改造前两步管线的逐字节终态。改造后合并入口必须逐字节
// 相等。
func goldenMergedCases(t *testing.T) map[string]struct {
	name string
	body []byte
	want string
} {
	t.Helper()
	return map[string]struct {
		name string
		body []byte
		want string
	}{
		// 双 shape × responses：覆盖写 reasoning.effort + 删 reasoning_effort，
		// 随后归一无动作。
		"both-shapes-responses-override": {
			body: []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low"},"reasoning_effort":"low"}`),
			want: `{"input":"hi","model":"m","reasoning":{"effort":"high"},"service_tier":"priority"}`,
		},
		// 双 shape × chat：覆盖写 reasoning_effort + 删 reasoning，随后归一无动作。
		"both-shapes-chat-override": {
			body: []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low"},"reasoning_effort":"low"}`),
			want: `{"input":"hi","model":"m","reasoning_effort":"high","service_tier":"priority"}`,
		},
		// 仅 reasoning_effort × responses、无覆盖：归一转嵌套。
		"flat-only-responses-normalize": {
			body: []byte(`{"model":"m","input":"hi","reasoning_effort":"low"}`),
			want: `{"input":"hi","model":"m","reasoning":{"effort":"low"}}`,
		},
		// 仅 reasoning × chat、无覆盖：归一转顶层（summary 随对象删除，既有语义）。
		"nested-only-chat-normalize": {
			body: []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low","summary":"auto"}}`),
			want: `{"input":"hi","model":"m","reasoning_effort":"low"}`,
		},
	}
}

func TestMergedOverridesGoldenBytes(t *testing.T) {
	runtime := GptRequestOverrideRuntime{Catalog: &countingOverrideCatalog{mock: mockGptOverrideCatalog{items: fullCapabilityCatalog()}}}
	overrideAccount := mergedOverrideAccount(map[string]any{
		"service_tier_override":     "priority",
		"reasoning_effort_override": "high",
	})
	plainAccount := mergedOverrideAccount(nil)

	cases := []struct {
		name       string
		body       []byte
		account    gatewayruntimecache.OpenAIAccountSecret
		family     string
		reasonPath string
		golden     string
	}{
		{
			name:       "both-shapes-responses-override",
			body:       []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low"},"reasoning_effort":"low"}`),
			account:    overrideAccount,
			family:     "responses",
			reasonPath: "/v1/responses",
			golden:     `{"input":"hi","model":"m","reasoning":{"effort":"high"},"service_tier":"priority"}`,
		},
		{
			name:       "both-shapes-chat-override",
			body:       []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low"},"reasoning_effort":"low"}`),
			account:    overrideAccount,
			family:     "chat_completions",
			reasonPath: "/v1/chat/completions",
			golden:     `{"input":"hi","model":"m","reasoning_effort":"high","service_tier":"priority"}`,
		},
		{
			name:       "flat-only-responses-normalize",
			body:       []byte(`{"model":"m","input":"hi","reasoning_effort":"low"}`),
			account:    plainAccount,
			family:     "responses",
			reasonPath: "/v1/responses",
			golden:     `{"input":"hi","model":"m","reasoning":{"effort":"low"}}`,
		},
		{
			name:       "nested-only-chat-normalize",
			body:       []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low","summary":"auto"}}`),
			account:    plainAccount,
			family:     "responses",
			reasonPath: "/v1/chat/completions",
			golden:     `{"input":"hi","model":"m","reasoning_effort":"low"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
				context.Background(), tc.body, nil, tc.account, tc.family, false, "gpt-test", tc.reasonPath, runtime)
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if string(got) != tc.golden {
				t.Fatalf("golden mismatch:\n got  %s\n want %s", got, tc.golden)
			}
		})
	}

	// 都无 effort、无覆盖：归一无动作 → 逐字节返回原始 body。
	body := []byte(`{"model":"m","input":"hi"}`)
	got, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, plainAccount, "responses", false, "gpt-test", "/v1/responses", runtime)
	if err != nil || string(got) != string(body) {
		t.Fatalf("无动作必须返回原字节: %s err=%v", got, err)
	}
	// 非 reasoning 族出站：原字节透传。
	got, err = ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, plainAccount, "", false, "m", "/v1/messages", runtime)
	if err != nil || string(got) != string(body) {
		t.Fatalf("族外出站必须返回原字节: %s err=%v", got, err)
	}
	// 非 reasoning 族 + 非法 JSON + 无覆盖：同样原字节（不报错）。
	got, err = ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), []byte("not-json"), nil, plainAccount, "", false, "", "/v1/messages", runtime)
	if err != nil || string(got) != "not-json" {
		t.Fatalf("族外非法 body 必须原样返回: %s err=%v", got, err)
	}
	// reasoning 族 + 非法 JSON + 无覆盖：归一解析失败语义 → 原字节不报错。
	got, err = ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), []byte("not-json"), nil, plainAccount, "responses", false, "", "/v1/responses", runtime)
	if err != nil || string(got) != "not-json" {
		t.Fatalf("归一解析失败必须原样返回: %s err=%v", got, err)
	}
}

// legacyOverridesThenReasoning 复刻改造前 chain_driver 的两步出站管线
// （ApplyGptAccountRequestOverridesToUpstreamBody →
// NormalizeOpenAIReasoningFieldsForUpstream），作为合并入口的差分 oracle。
// 覆盖步骤逐行取自收敛前实现；reasoning 步骤逐行取自 bodypreparation.go
// 既有实现（该函数本批未改动，根包 reasoning_wire_test 继续钉住）。
func legacyOverridesThenReasoning(t *testing.T, body []byte, account gatewayruntimecache.OpenAIAccountSecret, endpointFamily string, compact bool, upstreamModel string, reasoningPath string, runtime GptRequestOverrideRuntime) []byte {
	t.Helper()

	// ---- step 1: overrides（收敛前 ApplyGptAccountRequestOverridesToUpstreamBody）----
	out := body
	overrides, err := ReadGptAccountRequestOverrides(account.Credentials)
	if err != nil {
		t.Fatalf("oracle read: %v", err)
	}
	if err := AssertGptAccountRequestOverrideValuesForProvider(overrides, overrideVendorCode(account)); err != nil {
		t.Fatalf("oracle assert: %v", err)
	}
	if HasApplicableGptAccountRequestOverrides(overrides, endpointFamily, compact) {
		model := strings.TrimSpace(upstreamModel)
		if model == "" {
			parsed := map[string]any{}
			if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
				t.Fatalf("oracle parse: 账户请求覆盖要求请求体是有效的 JSON 对象")
			}
			model, _ = parsed["model"].(string)
		}
		resolved, err := resolveGptAccountRequestOverridesForAccount(account)
		if err != nil {
			t.Fatalf("oracle resolve: %v", err)
		}
		capabilities, err := resolved.resolveModelCapabilities(context.Background(), runtime, model)
		if err != nil {
			t.Fatalf("oracle capabilities: %v", err)
		}
		effective := ResolveEffectiveOverrides(overrides, capabilities)
		if HasApplicableGptAccountRequestOverrides(effective, endpointFamily, compact) {
			parsed := map[string]any{}
			if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
				t.Fatalf("oracle parse 2: 账户请求覆盖要求请求体是有效的 JSON 对象")
			}
			overridden, err := ApplyGptAccountRequestOverridesBody(parsed, GptAccountOverrideInput{
				Credentials:       account.Credentials,
				VendorCode:        overrideVendorCode(account),
				EndpointFamily:    endpointFamily,
				Compact:           compact,
				ModelCapabilities: capabilities,
			})
			if err != nil {
				t.Fatalf("oracle apply: %v", err)
			}
			encoded, err := json.Marshal(overridden)
			if err != nil {
				t.Fatalf("oracle encode: %v", err)
			}
			out = encoded
		}
	}

	// ---- step 2: reasoning wire 归一（bodypreparation.go 原实现镜像）----
	family := openAIReasoningWireFamily(reasoningPath)
	if family == "" || len(out) == 0 {
		return out
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil || parsed == nil {
		return out
	}
	reasoning, hasReasoning := parsed["reasoning"]
	reasoningObject, _ := reasoning.(map[string]any)
	nestedEffort, nestedOK := reasoningObject["effort"].(string)
	nestedEffort = strings.TrimSpace(nestedEffort)
	flatEffort, flatOK := parsed["reasoning_effort"].(string)
	flatEffort = strings.TrimSpace(flatEffort)
	if !nestedOK || nestedEffort == "" {
		nestedEffort = ""
	}
	if !flatOK || flatEffort == "" {
		flatEffort = ""
	}
	if nestedEffort == "" && flatEffort == "" {
		return out
	}
	changed := false
	switch family {
	case "chat_completions":
		if flatEffort == "" && nestedEffort != "" {
			parsed["reasoning_effort"] = nestedEffort
			changed = true
		}
		if hasReasoning {
			delete(parsed, "reasoning")
			changed = true
		}
	case "responses":
		if nestedEffort == "" && flatEffort != "" {
			parsed["reasoning"] = map[string]any{"effort": flatEffort}
			changed = true
		}
		if _, ok := parsed["reasoning_effort"]; ok {
			delete(parsed, "reasoning_effort")
			changed = true
		}
	}
	if !changed {
		return out
	}
	serialized, err := json.Marshal(parsed)
	if err != nil {
		return out
	}
	return serialized
}

// TestMergedOverridesMatchesLegacyPipeline 双形状 × 覆盖配置 × 族 × compact
// 的差分矩阵：合并入口逐字节等于改造前两步管线。
func TestMergedOverridesMatchesLegacyPipeline(t *testing.T) {
	runtime := GptRequestOverrideRuntime{Catalog: &countingOverrideCatalog{mock: mockGptOverrideCatalog{items: fullCapabilityCatalog()}}}
	overrideAccount := mergedOverrideAccount(map[string]any{
		"service_tier_override":     "priority",
		"reasoning_effort_override": "high",
	})
	tierOnlyAccount := mergedOverrideAccount(map[string]any{"service_tier_override": "default"})
	plainAccount := mergedOverrideAccount(nil)

	shapes := map[string][]byte{
		"both":    []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low"},"reasoning_effort":"low"}`),
		"nested":  []byte(`{"model":"m","input":"hi","reasoning":{"effort":"low","summary":"auto"}}`),
		"flat":    []byte(`{"model":"m","input":"hi","reasoning_effort":"low"}`),
		"neither": []byte(`{"model":"m","input":"hi"}`),
	}
	accounts := map[string]gatewayruntimecache.OpenAIAccountSecret{
		"override": overrideAccount,
		"default":  tierOnlyAccount,
		"plain":    plainAccount,
	}
	families := []struct {
		wire       string
		reasonPath string
	}{{"responses", "/v1/responses"}, {"chat_completions", "/v1/chat/completions"}}

	for shapeName, body := range shapes {
		for accountName, account := range accounts {
			for _, family := range families {
				for _, compact := range []bool{false, true} {
					name := shapeName + "/" + accountName + "/" + family.wire
					if compact {
						name += "/compact"
					}
					t.Run(name, func(t *testing.T) {
						want := legacyOverridesThenReasoning(t, body, account, family.wire, compact, "gpt-test", family.reasonPath, runtime)
						got, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
							context.Background(), body, nil, account, family.wire, compact, "gpt-test", family.reasonPath, runtime)
						if err != nil {
							t.Fatalf("merged apply: %v", err)
						}
						if string(got) != string(want) {
							t.Fatalf("merged output diverges from legacy pipeline:\n got  %s\n want %s", got, want)
						}
					})
				}
			}
		}
	}
}

// TestMergedOverridesErrorTextsVerbatim 钉住 T14：非法凭据 token 三态与非法
// JSON body 的错误文案逐字不变。
func TestMergedOverridesErrorTextsVerbatim(t *testing.T) {
	runtime := GptRequestOverrideRuntime{Catalog: &countingOverrideCatalog{mock: mockGptOverrideCatalog{items: fullCapabilityCatalog()}}}

	cases := []struct {
		name        string
		credentials map[string]any
		body        []byte
		model       string
		wantErr     string
	}{
		{
			name:        "token 非字符串值",
			credentials: map[string]any{"service_tier_override": 123},
			body:        []byte(`{"model":"gpt-test"}`),
			model:       "gpt-test",
			wantErr:     "账户请求覆盖字段 service_tier_override 无效",
		},
		{
			name:        "token 首尾空白",
			credentials: map[string]any{"reasoning_effort_override": " high"},
			body:        []byte(`{"model":"gpt-test"}`),
			model:       "gpt-test",
			wantErr:     "账户请求覆盖字段 reasoning_effort_override 无效",
		},
		{
			name:        "token 字符形态不匹配",
			credentials: map[string]any{"reasoning_effort_override": "high low"},
			body:        []byte(`{"model":"gpt-test"}`),
			model:       "gpt-test",
			wantErr:     "账户请求覆盖字段 reasoning_effort_override 无效",
		},
		{
			name:        "gpt 枚举外值",
			credentials: map[string]any{"reasoning_effort_override": "ultra"},
			body:        []byte(`{"model":"gpt-test"}`),
			model:       "gpt-test",
			wantErr:     "GPT 账户请求覆盖字段 reasoning_effort_override 无效",
		},
		{
			name:        "非法 JSON body 且模型已传",
			credentials: map[string]any{"service_tier_override": "priority"},
			body:        []byte("not-json"),
			model:       "gpt-test",
			wantErr:     "账户请求覆盖要求请求体是有效的 JSON 对象",
		},
		{
			name:        "非法 JSON body 且需解析提取模型",
			credentials: map[string]any{"service_tier_override": "priority"},
			body:        []byte("not-json"),
			model:       "",
			wantErr:     "账户请求覆盖要求请求体是有效的 JSON 对象",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
				context.Background(), tc.body, nil, mergedOverrideAccount(tc.credentials), "responses", false, tc.model, "/v1/responses", runtime)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v, want 逐字 %q", err, tc.wantErr)
			}
		})
	}
}

// TestMergedOverridesEmptyEndpointFamilySkipsCredentialRead 钉住与收敛前
// 两步管线一致的失败面：端点族为空（媒体等非五族路径）时整段跳过覆盖，
// 不读取凭据、不执行值域断言；同一非法凭据在五族路径上仍按原错误文案失败。
func TestMergedOverridesEmptyEndpointFamilySkipsCredentialRead(t *testing.T) {
	account := mergedOverrideAccount(map[string]any{"service_tier_override": 42})
	body := []byte(`{"model":"gpt-test","input":"hi"}`)
	out, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, account, "", false, "gpt-test", "/v1/images/generations", GptRequestOverrideRuntime{})
	if err != nil {
		t.Fatalf("非覆盖端点族必须跳过凭据读取: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("body 必须原样返回: %s", out)
	}
	if _, err := ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
		context.Background(), body, nil, account, "responses", false, "gpt-test", "/v1/responses", GptRequestOverrideRuntime{}); err == nil {
		t.Fatal("五族路径必须保留非法 token 错误")
	}
}
