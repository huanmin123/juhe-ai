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
		{"mode 非法枚举", `{"model":"x","mode":"realtime",` + base + `}`},
		{"mode audio 协议非法", `{"model":"x","mode":"audio","supportedApiProtocols":["nope"],"audioOutputUsdPer1M":2}`},
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
		{"mode 非法", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","mode":"realtime"}`},
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

// ---------------------------------------------------------------------------
// M1 同步音频：mode=audio + audio_speech 协议从非法枚举反转为合法创建路径。
// ---------------------------------------------------------------------------

func TestWeProvidersCreateAudioModeModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-tts","scope":"global","mode":"audio",`+
			`"supportedApiProtocols":["audio_speech"],`+
			`"releaseDate":"2026-01-01","mediaPricing":`+testAudioMediaPricing+`}`)
	if code != http.StatusCreated {
		t.Fatalf("audio create: %d %v", code, created)
	}
	var protocols string
	if err := env.db.QueryRow(`SELECT supported_endpoint_modes_json FROM models WHERE public_model = ?`, "my-tts").Scan(&protocols); err != nil {
		t.Fatal(err)
	}
	if protocols != `["audio_speech"]` {
		t.Fatalf("audio row persisted protocols=%s", protocols)
	}

	// STT 协议同样合法（同一枚举集合）。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-stt","scope":"global","mode":"audio",`+
			`"supportedApiProtocols":["audio_transcription"],`+
			`"releaseDate":"2026-01-01","mediaPricing":`+testAudioMediaPricing+`}`)
	if code != http.StatusCreated {
		t.Fatalf("transcription create: %d", code)
	}
}

// ---------------------------------------------------------------------------
// M2/P3 视频：mode=video + video 协议合法；统一媒体计价后启用态必须携带
// mediaPricing（无同族模板时管理员创建返回 400「请先配置媒体统一价格」）。
// ---------------------------------------------------------------------------

func TestWeProvidersCreateVideoModeModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-sora","scope":"global","status":"draft","mode":"video",`+
			`"supportedApiProtocols":["video"],`+
			`"releaseDate":"2026-01-01","mediaPricing":`+testVideoMediaPricingSecond+`}`)
	if code != http.StatusCreated {
		t.Fatalf("video draft create: %d %v", code, created)
	}
	var protocols string
	if err := env.db.QueryRow(`SELECT supported_endpoint_modes_json FROM models WHERE public_model = ?`, "my-sora").Scan(&protocols); err != nil {
		t.Fatal(err)
	}
	if protocols != `["video_cancel","video_content","video_create","video_get"]` {
		t.Fatalf("video row persisted protocols=%s", protocols)
	}

	// 启用态（默认 status=active）必须有统一媒体价格；没有同族模板可复制
	// 时管理员创建返回明确 400，不落未定价 active 行。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-sora-2","scope":"global","mode":"video",`+
			`"supportedApiProtocols":["video"],`+
			`"releaseDate":"2026-01-01"}`)
	if code != http.StatusBadRequest || payload["message"] != "请先配置媒体统一价格" {
		t.Fatalf("video active create without pricing: %d %v", code, payload)
	}

	// 显式 mediaPricing 的启用态视频模型创建成功。
	code, activeCreated := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-sora-2","scope":"global","mode":"video",`+
			`"supportedApiProtocols":["video"],`+
			`"releaseDate":"2026-01-01","mediaPricing":`+testVideoMediaPricingSecond+`}`)
	if code != http.StatusCreated {
		t.Fatalf("video active create with pricing: %d %v", code, activeCreated)
	}
}

// ---------------------------------------------------------------------------
// T93-2（C3-H2）：启用态价格完整性按能力域校验——text 必须 input+output，
// image 必须有按张价；缺必填维度 400，draft 放行。
// ---------------------------------------------------------------------------

func TestT93PricingCompletenessHTTP(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 文本只填 input：400。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"t93-text-half","scope":"global","supportedApiProtocols":["chat_completions"],`+
			`"releaseDate":"2026-01-01","inputUsdPer1M":1}`)
	if code != http.StatusBadRequest || payload["message"] != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("text input-only: %d %v", code, payload)
	}

	// 文本 input+output：201。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"t93-text-full","scope":"global","supportedApiProtocols":["chat_completions"],`+
			`"releaseDate":"2026-01-01","inputUsdPer1M":1,"outputUsdPer1M":2}`)
	if code != http.StatusCreated {
		t.Fatalf("text full price: %d %v", code, payload)
	}

	// 文本草稿只填 input：201（未启用不校验）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"t93-text-draft","scope":"global","status":"draft","supportedApiProtocols":["chat_completions"],`+
			`"releaseDate":"2026-01-01","inputUsdPer1M":1}`)
	if code != http.StatusCreated {
		t.Fatalf("text draft input-only: %d %v", code, payload)
	}

	// 图片模型未提交 mediaPricing 且无同族模板可复制：400（统一媒体计价后
	// legacy 图片 token/按张价不再作为媒体模式的创建价通道）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"t93-image-half","scope":"global","mode":"image","supportedApiProtocols":["images"],`+
			`"releaseDate":"2026-01-01","imageInputUsdPer1M":1}`)
	if code != http.StatusBadRequest || payload["message"] != "请先配置媒体统一价格" {
		t.Fatalf("image token-only: %d %v", code, payload)
	}

	// 图片携带统一媒体模板：201。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"t93-image-full","scope":"global","mode":"image","supportedApiProtocols":["images"],`+
			`"releaseDate":"2026-01-01","mediaPricing":`+testImageMediaPricing+`}`)
	if code != http.StatusCreated {
		t.Fatalf("image media pricing: %d %v", code, payload)
	}
}
