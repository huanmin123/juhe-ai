package accounts

// §5.1 编辑弹窗草稿测试自由选模型：POST /test-draft-options 双面接口
// （草稿载荷 → 与实际草稿测试一致的准备链 → 共享目录装配）与草稿测试请求
// 携带模型覆盖（POST /test-draft 与 POST /{id}/test 草稿分支）。

import (
	"net/http"
	"strings"
	"testing"
)

// seedDraftCatalogExtra 追加一个目录内、草稿支持列表外的模型（gpt-4.1）。
func seedDraftCatalogExtra(t *testing.T, env *testEnv) {
	t.Helper()
	env.exec(t, `INSERT OR IGNORE INTO provider_model_catalog
		(id, provider_code, model, status, mode, release_date, supported_api_protocols_json, catalog_visible, created_at, updated_at)
		VALUES ('cat-tdo-411', 'gpt', 'gpt-4.1', 'active', NULL, '2026-02-01',
		'["chat_completions","responses"]', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
}

func TestTestDraftOptionsRoutes(t *testing.T) {
	env, adminID, _ := w2DispatchEnv(t, true)
	groupID := "grp-default-" + adminID
	seedDraftCatalogExtra(t, env)
	base := "/__aisys__/api/accounts"

	t.Run("合法草稿返回目录候选", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, base+"/test-draft-options",
			`{`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusOK {
			t.Fatalf("test-draft-options 状态码：%d %v", code, payload)
		}
		options := dataArray(t, payload)
		if len(options) != 2 {
			t.Fatalf("候选数量不一致：%v", options)
		}
		models := map[string][]string{}
		for _, item := range options {
			option := item.(map[string]any)
			modes := []string{}
			for _, mode := range option["testEndpointModes"].([]any) {
				modes = append(modes, mode.(string))
			}
			models[option["id"].(string)] = modes
		}
		// 草稿检查模型 gpt-4o-mini 保底返回（对齐 GET test-options 选中语义），
		// modes 形态与 GET 同口径（默认检查形态置顶）。
		pinned, ok := models["gpt-4o-mini"]
		if !ok {
			t.Fatalf("草稿检查模型应保底返回：%v", models)
		}
		if len(pinned) != 4 || pinned[0] != "chat_json" || pinned[1] != "chat_sse" ||
			pinned[2] != "responses_sse" || pinned[3] != "responses_json" {
			t.Fatalf("保底模型形态不一致：%v", pinned)
		}
		if _, ok := models["gpt-4.1"]; !ok {
			t.Fatalf("目录候选缺失：%v", models)
		}
	})
	t.Run("keyword/limit/selectedIds 对齐 GET 语义", func(t *testing.T) {
		// keyword 不命中：只剩保底检查模型（与 GET 口径一致）。
		code, payload := env.do(t, http.MethodPost, base+"/test-draft-options",
			`{"keyword":"nomatch",`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusOK || len(dataArray(t, payload)) != 1 ||
			dataArray(t, payload)[0].(map[string]any)["id"] != "gpt-4o-mini" {
			t.Fatalf("keyword 不命中应只剩保底模型：%d %v", code, payload)
		}
		// selectedIds + 保底检查模型均不受 keyword 限制。
		code, payload = env.do(t, http.MethodPost, base+"/test-draft-options",
			`{"keyword":"zzz","selectedIds":["gpt-4.1"],`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusOK {
			t.Fatalf("selectedIds 状态码：%d %v", code, payload)
		}
		options := dataArray(t, payload)
		if len(options) != 2 {
			t.Fatalf("selectedIds 与保底模型应都不受 keyword 限制：%v", options)
		}
		ids := map[string]bool{}
		for _, item := range options {
			ids[item.(map[string]any)["id"].(string)] = true
		}
		if !ids["gpt-4.1"] || !ids["gpt-4o-mini"] {
			t.Fatalf("selectedIds 候选缺失：%v", ids)
		}
		// limit 越界/非整数 → 400 与 GET 同文案。
		for _, limit := range []string{`0`, `51`, `1.5`} {
			code, payload = env.do(t, http.MethodPost, base+"/test-draft-options",
				`{"limit":`+limit+`,`+w2DraftAccountBody(groupID)+`}`)
			if code != http.StatusBadRequest || payload["message"] != "limit 必须是 1 到 50 的整数" {
				t.Fatalf("limit=%s 校验不一致：%d %v", limit, code, payload)
			}
		}
	})
	t.Run("非法草稿与非法参数", func(t *testing.T) {
		cases := []struct {
			name    string
			body    string
			message string
		}{
			{"缺 account", `{}`, "账户草稿测试参数无效"},
			{"未知键", `{"bogus":1,` + w2DraftAccountBody(groupID) + `}`, "账户草稿测试参数无效"},
			{"account 非对象", `{"account":"x"}`, "账户草稿测试参数无效"},
			{"keyword 非字符串", `{"keyword":9,` + w2DraftAccountBody(groupID) + `}`, "账户草稿测试参数无效"},
			{"selectedIds 非数组", `{"selectedIds":"gpt",` + w2DraftAccountBody(groupID) + `}`, "账户草稿测试参数无效"},
			{"selectedIds 成员非字符串", `{"selectedIds":[9],` + w2DraftAccountBody(groupID) + `}`, "账户草稿测试参数无效"},
			{"检查模型越界", `{` + strings.Replace(w2DraftAccountBody(groupID),
				`"supportedModels":["gpt-4o-mini"]`, `"supportedModels":["other-model"]`, 1) + `}`, "账户检查模型必须属于账户支持模型"},
			{"缺档案", `{` + strings.Replace(w2DraftAccountBody(groupID),
				`"providerProtocolProfileId":"prof-gpt"`, `"providerProtocolProfileId":"prof-missing"`, 1) + `}`, "供应商 gpt 不支持账户类型 api_key"},
			{"分组无效", `{` + strings.Replace(w2DraftAccountBody(groupID), groupID, "grp-missing", 1) + `}`, "账户分组无效"},
		}
		for _, testCase := range cases {
			code, payload := env.do(t, http.MethodPost, base+"/test-draft-options", testCase.body)
			if code != http.StatusBadRequest {
				t.Fatalf("%s：期望 400，实际 %d %v", testCase.name, code, payload)
			}
			if !strings.Contains(payload["message"].(string), testCase.message) {
				t.Fatalf("%s：消息缺少 %q：%v", testCase.name, testCase.message, payload["message"])
			}
		}
		// 坏 JSON → 400。
		if code, _ := env.doRaw(t, http.MethodPost, base+"/test-draft-options", "{not-json"); code != http.StatusBadRequest {
			t.Fatalf("坏 JSON 应 400：%d", code)
		}
	})
	t.Run("self 面", func(t *testing.T) {
		// 他人分组不可见 → 400（与 /test-draft 同 gate）。
		env.login(t, "user-tdo", "user-tdo-pass", "user")
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-draft-options",
			`{`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusBadRequest || payload["message"] != "账户分组无效" {
			t.Fatalf("用户侧分组 gate 不一致：%d %v", code, payload)
		}
	})
	t.Run("self 面自有分组", func(t *testing.T) {
		userID := env.login(t, "user-tdo2", "user-tdo2-pass", "user")
		env.seedProviderAndDefaultGroup(t, userID)
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-draft-options",
			`{`+w2DraftAccountBody("grp-default-"+userID)+`}`)
		if code != http.StatusOK {
			t.Fatalf("自面草稿选项状态码：%d %v", code, payload)
		}
		if len(dataArray(t, payload)) != 2 {
			t.Fatalf("自面候选数量不一致：%v", payload)
		}
	})
}

func TestDraftTestModelOverride(t *testing.T) {
	env, adminID, accountID := w2DispatchEnv(t, true)
	groupID := "grp-default-" + adminID
	seedDraftCatalogExtra(t, env)

	t.Run("test-draft 携带支持列表外目录模型", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft",
			`{"model":"gpt-4.1",`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusAccepted {
			t.Fatalf("草稿自由模型投递状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["model"] != "gpt-4.1" || data["testEndpointMode"] != "chat_json" {
			t.Fatalf("任务模型不一致：%v", data)
		}
		if env.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE id = ? AND model = 'gpt-4.1'`,
			data["id"].(string)) != 1 {
			t.Fatal("任务行 model 未落库为所选模型")
		}
	})
	t.Run("saved-draft 分支携带支持列表外目录模型", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/test",
			`{"model":"gpt-4.1",`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusAccepted {
			t.Fatalf("保存账户草稿分支投递状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		if data["model"] != "gpt-4.1" {
			t.Fatalf("任务模型不一致：%v", data)
		}
		if env.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE id = ? AND draft_account_encrypted IS NOT NULL AND draft_account_encrypted != ''`,
			data["id"].(string)) != 1 {
			t.Fatal("草稿分支应携带草稿快照")
		}
	})
	t.Run("不携带 model 回落草稿检查模型", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft",
			`{`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusAccepted {
			t.Fatalf("回落投递状态码：%d %v", code, payload)
		}
		if dataMap(t, payload)["model"] != "gpt-4o-mini" {
			t.Fatalf("未携带 model 应回落草稿检查模型：%v", payload)
		}
	})
	t.Run("目录外模型仍拒绝", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft",
			`{"model":"no-such-model",`+w2DraftAccountBody(groupID)+`}`)
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "模型不在当前账户供应商可用目录中") {
			t.Fatalf("目录外模型校验不一致：%d %v", code, payload)
		}
	})
}
