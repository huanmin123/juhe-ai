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
			`"releaseDate":"2026-01-01","audioInputUsdPer1M":1,"audioOutputUsdPer1M":2}`)
	if code != http.StatusCreated {
		t.Fatalf("audio create: %d %v", code, created)
	}
	var mode, protocols string
	if err := env.db.QueryRow(`SELECT mode, supported_api_protocols_json FROM custom_provider_models WHERE model = ?`, "my-tts").Scan(&mode, &protocols); err != nil {
		t.Fatal(err)
	}
	if mode != "audio" || protocols != `["audio_speech"]` {
		t.Fatalf("audio row persisted mode=%q protocols=%s", mode, protocols)
	}

	// STT 协议同样合法（同一枚举集合）。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-stt","scope":"global","mode":"audio",`+
			`"supportedApiProtocols":["audio_transcription"],`+
			`"releaseDate":"2026-01-01","audioInputUsdPer1M":1}`)
	if code != http.StatusCreated {
		t.Fatalf("transcription create: %d", code)
	}
}

// ---------------------------------------------------------------------------
// M2 视频：mode=video + video 协议合法（draft 创建路径）；启用态因无管理面
// 价格通道被价格完整性校验挡住（write_routes.go customInputHasDirectPrice
// video 分支，价格通道随 M3 计价完善交付）。
// ---------------------------------------------------------------------------

func TestWeProvidersCreateVideoModeModel(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-sora","scope":"global","status":"draft","mode":"video",`+
			`"supportedApiProtocols":["video"],`+
			`"releaseDate":"2026-01-01"}`)
	if code != http.StatusCreated {
		t.Fatalf("video draft create: %d %v", code, created)
	}
	var mode, protocols string
	if err := env.db.QueryRow(`SELECT mode, supported_api_protocols_json FROM custom_provider_models WHERE model = ?`, "my-sora").Scan(&mode, &protocols); err != nil {
		t.Fatal(err)
	}
	if mode != "video" || protocols != `["video"]` {
		t.Fatalf("video row persisted mode=%q protocols=%s", mode, protocols)
	}

	// 启用态（默认 status=active）被价格完整性校验挡住：视频分类无管理面
	// 价格字段可配（M2 词表只放开分类与协议）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"my-sora-2","scope":"global","mode":"video",`+
			`"supportedApiProtocols":["video"],`+
			`"releaseDate":"2026-01-01"}`)
	if code != http.StatusBadRequest || payload["message"] != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("video active create: %d %v", code, payload)
	}
}
