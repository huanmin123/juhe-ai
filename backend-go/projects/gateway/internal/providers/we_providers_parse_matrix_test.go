package providers

import (
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// POST /{code}/models 的 zod 解析矩阵：所有形状错误统一折叠为固定 400 文案。
// ---------------------------------------------------------------------------

func TestWeProvidersCreateModelParseMatrix(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	base := `"supportedApiProtocols":["chat_completions"],"inputUsdPer1M":1,"outputUsdPer1M":2`
	cases := []struct {
		name string
		body string
	}{
		{"model 缺失", `{` + base + `}`},
		{"model 空白", `{"model":"  ",` + base + `}`},
		{"model 非字符串", `{"model":1,` + base + `}`},
		{"status 非字符串", `{"model":"x","status":1,` + base + `}`},
		{"status 非法枚举", `{"model":"x","status":"gone",` + base + `}`},
		{"scope 非法", `{"model":"x","scope":"team",` + base + `}`},
		{"scope 非字符串", `{"model":"x","scope":1,` + base + `}`},
		{"mode 非字符串", `{"model":"x","mode":1,` + base + `}`},
		{"mode 非法枚举", `{"model":"x","mode":"audio",` + base + `}`},
		{"protocols 非数组", `{"model":"x","supportedApiProtocols":"chat_completions",` + strings.Replace(base, `"supportedApiProtocols":["chat_completions"],`, "", 1) + `}`},
		{"protocols 项非法", `{"model":"x","supportedApiProtocols":["nope"],` + strings.Replace(base, `"supportedApiProtocols":["chat_completions"],`, "", 1) + `}`},
		{"tiers 非数组", `{"model":"x","supportedServiceTiers":"priority",` + base + `}`},
		{"tiers 项含空格", `{"model":"x","supportedServiceTiers":["bad tier"],` + base + `}`},
		{"efforts 项非法", `{"model":"x","supportedReasoningEfforts":["BAD"],` + base + `}`},
		{"defaultReasoningEffort 非字符串", `{"model":"x","defaultReasoningEffort":1,` + base + `}`},
		{"releaseDate 非法格式", `{"model":"x","releaseDate":"2026/01/01",` + base + `}`},
		{"releaseDate 非字符串", `{"model":"x","releaseDate":7,` + base + `}`},
		{"shutdownDate 非法格式", `{"model":"x","shutdownDate":"soon",` + base + `}`},
		{"contextWindowTokens 小数", `{"model":"x","contextWindowTokens":1.5,` + base + `}`},
		{"contextWindowTokens 负数", `{"model":"x","contextWindowTokens":-1,` + base + `}`},
		{"maxInputTokens 非数字", `{"model":"x","maxInputTokens":"x",` + base + `}`},
		{"maxOutputTokens 负数", `{"model":"x","maxOutputTokens":-2,` + base + `}`},
		{"inputUsdPer1M 负数", `{"model":"x","inputUsdPer1M":-1,"outputUsdPer1M":2}`},
		{"outputUsdPer1M 非数字", `{"model":"x","outputUsdPer1M":"x"}`},
		{"cachedInputUsdPer1M 负数", `{"model":"x","cachedInputUsdPer1M":-0.5}`},
		{"cacheWriteUsdPer1M 非数字", `{"model":"x","cacheWriteUsdPer1M":true}`},
		{"serviceTierPrices 非对象", `{"model":"x","serviceTierPrices":"x"}`},
		{"未知字段", `{"model":"x","bogus":1,` + base + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", tc.body)
			if code != http.StatusBadRequest || payload["message"] != "自定义模型参数无效" {
				t.Fatalf("%s: %d %v", tc.name, code, payload)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 自定义模型 PATCH 解析矩阵（expectedUpdatedAt 必填 + 字段形状校验）。
// ---------------------------------------------------------------------------

func TestWeProvidersPatchCustomModelParseMatrix(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name string
		body string
	}{
		{"expectedUpdatedAt 缺失", `{"inputUsdPer1M":2}`},
		{"expectedUpdatedAt 非字符串", `{"expectedUpdatedAt":1,"inputUsdPer1M":2}`},
		{"expectedUpdatedAt 空白", `{"expectedUpdatedAt":" ","inputUsdPer1M":2}`},
		{"mode 非法", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","mode":"audio"}`},
		{"protocols 项非法", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","supportedApiProtocols":["x"]}`},
		{"releaseDate 非法", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","releaseDate":"x"}`},
		{"contextWindowTokens 非数字", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","contextWindowTokens":"x"}`},
		{"未知字段", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","bogus":1}`},
		{"无变化字段", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/cu-1", tc.body)
			if code != http.StatusBadRequest || payload["message"] != "自定义模型参数无效" {
				t.Fatalf("%s: %d %v", tc.name, code, payload)
			}
		})
	}
}
