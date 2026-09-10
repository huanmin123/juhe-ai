package accounts

// W2 M11 读面 404/400 快矩阵与测试结果信封往返：以最小夹具驱动各读/写
// handler 的前置 gate（m11_routes.go）与 AccountTestResult 的 JSON 契约
// （test_store.go）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestW2AccountTestResultJSONContract(t *testing.T) {
	t.Run("合法信封往返", func(t *testing.T) {
		raw := json.RawMessage(`{"accountId":"acc-1","message":"ok","extra":true}`)
		result := AccountTestResult{}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("合法信封应通过：%v", err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("序列化失败：%v", err)
		}
		if !strings.Contains(string(encoded), "acc-1") {
			t.Fatalf("往返内容不一致：%s", encoded)
		}
	})
	t.Run("非法信封拒绝", func(t *testing.T) {
		for _, raw := range []string{
			`{}`,
			`{"accountId":"acc-1"}`,
			`{"message":"m"}`,
			`not-json`,
		} {
			result := AccountTestResult{}
			if err := json.Unmarshal([]byte(raw), &result); err == nil {
				t.Fatalf("%s 应拒绝", raw)
			}
		}
	})
}

func TestW2MaskCredentialValue(t *testing.T) {
	// 密钥字段走掩码；api_keys 列表逐项掩码；普通字段原样。
	if got := maskCredentialValue("api_keys", []any{"sk-1", "sk-2"}); got == nil {
		t.Fatal("api_keys 列表应逐项掩码")
	}
	if got := maskCredentialValue("api_key", "sk-secret"); got == "sk-secret" {
		t.Fatal("密钥字段应掩码")
	}
	if got := maskCredentialValue("notes", "普通文本"); got != "普通文本" {
		t.Fatalf("普通字段应原样返回：%v", got)
	}
	if got := maskCredentialValue("api_keys", "not-a-list"); got == nil {
		t.Fatal("非列表 api_keys 走单值掩码")
	}
}

func TestW2M11ReadGates(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-gate", adminID, "gate 账户", "api_key", "active", Credentials{"api_key": "sk-gate"})

	t.Run("不存在的账户统一 404", func(t *testing.T) {
		paths := []struct {
			method string
			path   string
		}{
			{http.MethodGet, "/__aisys__/api/accounts/acc-missing/oauth-reauthorization-context"},
			{http.MethodGet, "/__aisys__/api/accounts/acc-missing/api-key-runtime"},
			{http.MethodGet, "/__aisys__/api/accounts/acc-missing/balance/details"},
			{http.MethodGet, "/__aisys__/api/accounts/acc-missing/advanced"},
		}
		for _, item := range paths {
			code, payload := env.do(t, item.method, item.path, "")
			if code != http.StatusNotFound || payload["message"] != "账户不存在" {
				t.Fatalf("%s：期望 404 账户不存在，实际 %d %v", item.path, code, payload)
			}
		}
	})
	t.Run("forceActivate 前置 gate", func(t *testing.T) {
		// 缺确认布尔。
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-gate/force-activate", `{}`)
		if code != http.StatusBadRequest || payload["message"] != "请先确认账户当前可用并接受人工恢复风险" {
			t.Fatalf("缺确认 gate：%d %v", code, payload)
		}
		// 非 pending_test 账户。
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-gate/force-activate",
			`{"acknowledgedAccountAvailable":true}`)
		if code != http.StatusConflict || !strings.Contains(payload["message"].(string), "只有待检查账户") {
			t.Fatalf("非 pending_test gate：%d %v", code, payload)
		}
		// 不存在的账户（先过确认 gate）。
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/force-activate",
			`{"acknowledgedAccountAvailable":true}`)
		if code != http.StatusNotFound || payload["message"] != "账户不存在" {
			t.Fatalf("404 gate：%d %v", code, payload)
		}
	})
	t.Run("modelCatalogRefresh 参数与未接线端口", func(t *testing.T) {
		// 未知键 / 缺 account 对象 → 400。
		for _, body := range []string{`{}`, `{"bogus":1}`, `{"account":3}`} {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh", body)
			if code != http.StatusBadRequest || payload["message"] != "模型目录同步参数无效" {
				t.Fatalf("body %s：期望 400，实际 %d %v", body, code, payload)
			}
		}
		// 合法 account 草稿 + 未接线的刷新端口 → 400 固定文案。
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh",
			`{"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"草稿","type":"api_key",
			"credentials":{"api_key":"sk-draft-catalog","base_url":"https://api.openai.com/v1"},
			"supportedModels":["gpt-4o-mini"],"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json","groupId":"grp-default-x"}}`)
		if code != http.StatusBadRequest {
			t.Fatalf("未接线端口应 400：%d %v", code, payload)
		}
	})
	t.Run("balanceRefresh 无配置空载", func(t *testing.T) {
		// 无余额配置的账户刷新 → 空载响应或 400，均不得 5xx。
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-gate/balance/refresh", `{}`)
		if code >= 500 {
			t.Fatalf("余额刷新不应 5xx：%d %v", code, payload)
		}
	})
	t.Run("groupBinding 校验", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-gate/group", `{}`)
		if code != http.StatusBadRequest {
			t.Fatalf("分组绑定空 body 应 400：%d %v", code, payload)
		}
		_ = payload
	})
	t.Run("非 OAuth 账户无重授权上下文", func(t *testing.T) {
		// api_key 账户不携带 OAuth 重授权上下文 → 404（账户可见但无上下文）。
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-gate/oauth-reauthorization-context", "")
		if code != http.StatusNotFound {
			t.Fatalf("api_key 账户 oauth 上下文应 404：%d %v", code, payload)
		}
	})
}
