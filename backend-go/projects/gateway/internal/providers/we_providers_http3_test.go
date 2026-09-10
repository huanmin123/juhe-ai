package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---------------------------------------------------------------------------
// createModel：供应商/模板/解析分支
// ---------------------------------------------------------------------------

func TestWeProvidersCreateModelBranches(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 未知供应商 → 404。
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/providers/nope/models",
		`{"model":"x","supportedApiProtocols":["chat_completions"]}`)
	if code != http.StatusNotFound || missing["message"] != "供应商不存在" {
		t.Fatalf("未知供应商: %d %v", code, missing)
	}

	// 非对象 body：按空记录解码 → 模型缺失 → 固定 400 文案。
	code, arrayBody := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `[1,2]`)
	if code != http.StatusBadRequest || arrayBody["message"] != "自定义模型参数无效" {
		t.Fatalf("非对象 body: %d %v", code, arrayBody)
	}
	// 非法 JSON body → 400。
	code, notJSON := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models", `not-json`)
	if code != http.StatusBadRequest || notJSON["message"] != "请求体必须是 JSON 对象" {
		t.Fatalf("非 JSON: %d %v", code, notJSON)
	}

	// 管理员个人创建：owner 回落为调用者自身账户；无价格信息触发定价完整性校验。
	code, noPrice := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"gpt-we-owner","supportedApiProtocols":["chat_completions"]}`)
	if code != http.StatusBadRequest || noPrice["message"] != "启用的自定义模型必须配置完整当前价格" {
		t.Fatalf("缺价格: %d %v", code, noPrice)
	}

	// 配置模板不存在 → 400。
	code, badTemplate := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"gpt-we-tpl","configurationTemplateId":"cat-missing","supportedApiProtocols":["chat_completions"]}`)
	if code != http.StatusBadRequest || badTemplate["message"] != "配置模板不可用" {
		t.Fatalf("模板缺失: %d %v", code, badTemplate)
	}

	// 模板存在：继承模板配置并覆盖模型名（模板目录按当前调用者可见性解析）。
	env.login(t, "user1", "user-pass", "user")
	code, tplCreated := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models",
		`{"model":"gpt-we-tpl2","configurationTemplateId":"cat-2","supportedApiProtocols":["chat_completions"]}`)
	if code != http.StatusCreated {
		t.Fatalf("模板创建: %d %v", code, tplCreated)
	}

	// 服务档位价格必须属于支持的服务等级（text 模型 + 未声明 tier）。
	code, unknownTier := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models?systemAccountId=all",
		`{"model":"gpt-we-tier","supportedApiProtocols":["chat_completions"],"supportedServiceTiers":["priority"],`+
			`"serviceTierPrices":{"flex":{"inputUsdPer1M":1}}}`)
	if code != http.StatusBadRequest || unknownTier["message"] != "服务档位价格必须属于模型支持的服务等级" {
		t.Fatalf("未知 tier 价格: %d %v", code, unknownTier)
	}
}

// ---------------------------------------------------------------------------
// 自定义模型全字段 PATCH：归一化赋值与能力校验
// ---------------------------------------------------------------------------

func TestWeProvidersPatchCustomModelAllFields(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gpt/models?systemAccountId=all",
		`{"model":"gpt-we-allfields","supportedApiProtocols":["chat_completions"],"mode":"text",`+
			`"supportedReasoningEfforts":["low"],"defaultReasoningEffort":"low","inputUsdPer1M":1}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	modelID := dataMap(t, created)["id"].(string)

	updatedAt := env.updatedAtOf(t, modelID)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/"+modelID,
		`{"expectedUpdatedAt":"`+updatedAt+`",`+
			`"releaseDate":"2026-02-01","shutdownDate":"2099-01-01",`+
			`"cachedInputUsdPer1M":0.4,"cacheWriteUsdPer1M":1.2,"cacheWrite1hUsdPer1M":2.4,`+
			`"cacheStorageUsdPer1MPerHour":0.2,`+
			`"imageInputUsdPer1M":3,"imageOutputUsdPer1M":12,"audioInputUsdPer1M":5,"audioOutputUsdPer1M":20,`+
			`"pricingNotes":"价格备注","capabilityNotes":"能力备注","notes":"说明"}`)
	if code != http.StatusOK {
		t.Fatalf("全字段补丁: %d %v", code, patched)
	}
	var shutdown string
	var cacheWrite1h, audioOut float64
	if err := env.db.QueryRow(`SELECT shutdown_date, cache_write_1h_usd_per_1m, audio_output_usd_per_1m
		FROM custom_provider_models WHERE id = ?`, modelID).Scan(&shutdown, &cacheWrite1h, &audioOut); err != nil {
		t.Fatal(err)
	}
	if shutdown != "2099-01-01" || cacheWrite1h != 2.4 || audioOut != 20 {
		t.Fatalf("补丁投影: shutdown=%s cache1h=%v audioOut=%v", shutdown, cacheWrite1h, audioOut)
	}

	// 思考能力档位数组：trim + 去重后落库。
	updatedAt = env.updatedAtOf(t, modelID)
	code, efforts := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gpt/models/"+modelID,
		`{"expectedUpdatedAt":"`+updatedAt+`","supportedReasoningEfforts":["low","medium","low"]}`)
	if code != http.StatusOK {
		t.Fatalf("思考档位: %d %v", code, efforts)
	}
	var effortsJSON string
	if err := env.db.QueryRow(`SELECT supported_reasoning_efforts_json FROM custom_provider_models WHERE id = ?`, modelID).Scan(&effortsJSON); err != nil {
		t.Fatal(err)
	}
	if effortsJSON != `["low","medium"]` {
		t.Fatalf("efforts = %s", effortsJSON)
	}
}

// ---------------------------------------------------------------------------
// 关闭数据库：写路径（创建/补丁/删除/默认检查模型）的内部错误分支。
// 关库后 auth 中间件会先失败，因此直调 handler 并注入管理员身份。
// ---------------------------------------------------------------------------

func TestWeProvidersWriteErrorsWhenDatabaseClosed(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}

	adminCtx := authsys.WithAuthContext(context.Background(), &authsys.AuthContext{
		SystemAccountID: "sys-root", Username: "root", Role: "super_admin",
	})
	call := func(handler http.Handler, method, target, body string, pathValues map[string]string) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, target, reader).WithContext(adminCtx)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		for key, value := range pathValues {
			request.SetPathValue(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	deps := env.providersDeps
	cases := []struct {
		name       string
		handler    http.Handler
		method     string
		target     string
		body       string
		pathValues map[string]string
	}{
		{"create", http.HandlerFunc(deps.createModel), http.MethodPost, "/x", `{"model":"x","supportedApiProtocols":["chat_completions"]}`, map[string]string{"code": "gpt"}},
		{"patchBuiltIn", http.HandlerFunc(deps.patchModel), http.MethodPatch, "/x", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","mode":"text"}`, map[string]string{"code": "gpt", "modelId": "cat-2"}},
		{"patchCustom", http.HandlerFunc(deps.patchModel), http.MethodPatch, "/x", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","inputUsdPer1M":2}`, map[string]string{"code": "gpt", "modelId": "cu-1"}},
		{"delete", http.HandlerFunc(deps.deleteModel), http.MethodDelete, "/x", "", map[string]string{"code": "gpt", "modelId": "cu-2"}},
		{"putDefault", http.HandlerFunc(deps.putDefaultHealthCheckModel), http.MethodPut, "/x", `{"model":"gpt-4o"}`, map[string]string{"code": "gpt"}},
		{"createWithTemplate", http.HandlerFunc(deps.createModel), http.MethodPost, "/x", `{"model":"y","configurationTemplateId":"cat-2","supportedApiProtocols":["chat_completions"]}`, map[string]string{"code": "gpt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := call(tc.handler, tc.method, tc.target, tc.body, tc.pathValues); code != http.StatusInternalServerError {
				t.Fatalf("%s = %d", tc.name, code)
			}
		})
	}
}
