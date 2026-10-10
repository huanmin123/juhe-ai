package chat

// 用户级默认工具绑定（AI 问答工具体系与主子模型设计 §2.11/§7/§8.5-8.6，
// 2026-10-02）的路由级与 store 级覆盖：GET/PATCH /my-chat/tool-preferences、
// 新建会话继承偏好、会话绑定变更回写全局默认。候选 mock 造出确定性候选：
// account-1（openai）× gpt-5 搜索候选与 gpt-image-2 生图候选、account-grok
// （xai）× grok-imagine 系生图候选。Mock 风格与 chat_bind_modes_test.go 一致。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// toolPrefsTestLookup 解析测试账户：account-1（openai/group-a）、account-grok
// （xai/group-grok）均启用；其余不存在。
type toolPrefsTestLookup struct{}

func (toolPrefsTestLookup) FindChatAccount(scope ChatBindScope, accountID string) (*ChatAccountRef, error) {
	switch accountID {
	case "account-1":
		return &ChatAccountRef{ID: "account-1", Name: "账户 account-1", ProviderCode: "openai", Enabled: true, EnabledGroupIDs: []string{"group-a"}}, nil
	case "account-grok":
		return &ChatAccountRef{ID: "account-grok", Name: "Grok 账户", ProviderCode: "xai", Enabled: true, EnabledGroupIDs: []string{"group-grok"}}, nil
	}
	return nil, nil
}

// toolPrefsTestOptions 返回两个 active 账户摘要（候选解析入口）。
type toolPrefsTestOptions struct{}

func (toolPrefsTestOptions) ListChatAccountOptions(_ context.Context, scope ChatBindScope) ([]ChatAccountOption, error) {
	return []ChatAccountOption{
		{ID: "account-1", Name: "账户 account-1", ProviderCode: "openai", Status: "active"},
		{ID: "account-grok", Name: "Grok 账户", ProviderCode: "xai", Status: "active"},
	}, nil
}

// toolPrefsTestCatalog 提供候选解析的账户视图与 provider 目录：
// account-1 可派发 responses（gpt-5 声明 web_search）；account-grok 仅
// chat_sse（openai 目录对该账户不产出搜索候选）。
type toolPrefsTestCatalog struct{}

func (toolPrefsTestCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	switch groupID {
	case "group-a":
		return []ChatTransportAccount{{
			ID: "account-1", Type: "api_key", ProviderCode: "openai",
			SupportedEndpointModes: []string{"chat_sse", "responses_sse"},
		}}
	case "group-grok":
		return []ChatTransportAccount{{
			ID: "account-grok", Type: "api_key", ProviderCode: "xai",
			SupportedEndpointModes: []string{"chat_sse"},
		}}
	}
	return nil
}

// ListChatPinnedAccountsForGroup 与 ListAccountsForGroup 同一视图按 ID 收敛
// 单元素（对齐生产 pinned 直取语义）。
func (c toolPrefsTestCatalog) ListChatPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []ChatTransportAccount {
	for _, account := range c.ListAccountsForGroup(groupID, systemAccountID, "", "") {
		if account.ID == accountID {
			return []ChatTransportAccount{account}
		}
	}
	return nil
}

func (toolPrefsTestCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	if providerCode != "openai" {
		return nil
	}
	return []ProviderModelCatalogItem{{
		Model: "gpt-5", ProviderCode: "openai",
		SupportedAPIProtocols:    []string{"chat_completions", "responses"},
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling"}, "responses": {"function_calling", "web_search"}},
	}}
}

// newToolPrefsEnv 构建候选可解析的偏好测试环境。确定性候选：
// search=[account-1 × gpt-5]；image=[account-1 × gpt-image-2,
// account-grok × grok-imagine-image, account-grok × grok-imagine-image-quality]。
func newToolPrefsEnv(t *testing.T) *generationEnv {
	t.Helper()
	env := newGenerationEnv(t)
	env.deps.ModelCatalog = toolPrefsTestCatalog{}
	env.deps.AccountLookup = toolPrefsTestLookup{}
	env.deps.AccountOptionsLookup = toolPrefsTestOptions{}
	return env
}

// toolPrefsTools 把 GET/PATCH tool-preferences 响应按工具 id 索引。
func toolPrefsTools(t *testing.T, response routeResponse) map[string]map[string]any {
	t.Helper()
	data := response.dataMap()
	if data == nil {
		t.Fatalf("tool-preferences 缺少 data 信封: %s", response.rawString())
	}
	rawTools, _ := data["tools"].([]any)
	out := map[string]map[string]any{}
	for _, item := range rawTools {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		out[id] = entry
	}
	return out
}

// toolPrefsBinding 断言某工具的 bound/binding 二元组。
func toolPrefsAssertBinding(t *testing.T, tools map[string]map[string]any, toolID, accountID, modelID string, bound, valid bool) {
	t.Helper()
	tool, has := tools[toolID]
	if !has {
		t.Fatalf("tool-preferences 缺少 %s: %v", toolID, tools)
	}
	if tool["kind"] != "model" {
		t.Fatalf("%s kind = %v, want model", toolID, tool["kind"])
	}
	if tool["bound"] != bound {
		t.Fatalf("%s bound = %v, want %v", toolID, tool["bound"], bound)
	}
	if tool["valid"] != valid {
		t.Fatalf("%s valid = %v, want %v", toolID, tool["valid"], valid)
	}
	binding, _ := tool["binding"].(map[string]any)
	if !bound {
		if binding != nil {
			t.Fatalf("%s 未绑定 binding 应为 null: %v", toolID, binding)
		}
		return
	}
	if binding == nil {
		t.Fatalf("%s 已绑定 binding 不应为 null", toolID)
	}
	if binding["accountId"] != accountID || binding["modelId"] != modelID {
		t.Fatalf("%s binding = %v, want {%s %s}", toolID, binding, accountID, modelID)
	}
}

// TestUserToolPreferencesGetDefault：未设偏好时 GET 返回两类 model 工具
// bound:false（契约 §8.5/§14.8-1），code 工具照常列出；响应禁缓存。
func TestUserToolPreferencesGetDefault(t *testing.T) {
	env := newToolPrefsEnv(t)
	prefix := "/__aisys__/api/my-chat"
	response := env.do("GET", prefix+"/tool-preferences", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("GET tool-preferences = %d %s", response.status, response.rawString())
	}
	tools := toolPrefsTools(t, response)
	toolPrefsAssertBinding(t, tools, "web_search", "", "", false, false)
	toolPrefsAssertBinding(t, tools, "generate_image", "", "", false, false)
	if _, has := tools["diagnostic_echo"]; !has {
		t.Fatalf("code 工具应照常列出: %v", tools)
	}
	if tools["diagnostic_echo"]["kind"] != "code" {
		t.Fatalf("diagnostic_echo kind = %v, want code", tools["diagnostic_echo"]["kind"])
	}
	// 候选与 tool-bindings 同源：搜索候选 account-1 × gpt-5。
	searchCandidates, _ := tools["web_search"]["candidates"].([]any)
	if len(searchCandidates) != 1 {
		t.Fatalf("搜索候选 = %v, want [account-1 × gpt-5]", searchCandidates)
	}
	// no-store 头（内容随偏好/数据范围实时变化）。
	request, err := http.NewRequest(http.MethodGet, env.server.URL+prefix+"/tool-preferences", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test-Owner", routeTestOwner)
	noStore, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer noStore.Body.Close()
	if noStore.StatusCode != http.StatusOK {
		t.Fatalf("tool-preferences = %d", noStore.StatusCode)
	}
	if got := noStore.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
	if got := noStore.Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want %q", got, "no-cache")
	}
}

// TestUserToolPreferencesPatchArms：PATCH 候选内成功（搜索二元组 / 生图 +
// defaultImageModel）、候选外 400 chat_tool_binding_invalid + 候选、未知键
// 400、枚举外 400、null 解绑、合并只更新请求键；偏好端点不修改任何会话
// （契约 §8.6/§14.8-2/§14.8-8 末句）。
func TestUserToolPreferencesPatchArms(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	t.Run("候选内成功并反映到 GET", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		search := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"gpt-5"}}`)
		if search.status != http.StatusOK {
			t.Fatalf("PATCH search = %d %s", search.status, search.rawString())
		}
		toolPrefsAssertBinding(t, toolPrefsTools(t, search), "web_search", "account-1", "gpt-5", true, true)

		image := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"imageBinding":{"accountId":"account-1"},"defaultImageModel":"gpt-image-2"}`)
		if image.status != http.StatusOK {
			t.Fatalf("PATCH image = %d %s", image.status, image.rawString())
		}
		tools := toolPrefsTools(t, image)
		toolPrefsAssertBinding(t, tools, "generate_image", "account-1", "gpt-image-2", true, true)
		// 合并语义：image 键不触碰已设的搜索默认。
		toolPrefsAssertBinding(t, tools, "web_search", "account-1", "gpt-5", true, true)

		got := env.do("GET", prefix+"/tool-preferences", routeTestOwner, "")
		if got.status != http.StatusOK {
			t.Fatalf("GET = %d %s", got.status, got.rawString())
		}
		persisted := toolPrefsTools(t, got)
		toolPrefsAssertBinding(t, persisted, "web_search", "account-1", "gpt-5", true, true)
		toolPrefsAssertBinding(t, persisted, "generate_image", "account-1", "gpt-image-2", true, true)
	})

	t.Run("grok 生图候选按生效模型判定", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		// 不带 defaultImageModel：生效模型取偏好现值（空 → gpt-image-2），
		// account-grok × gpt-image-2 不在候选 → 400。
		rejected := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"imageBinding":{"accountId":"account-grok"}}`)
		if rejected.status != http.StatusBadRequest || rejected.code() != "chat_tool_binding_invalid" {
			t.Fatalf("grok×gpt-image-2 = %d %s", rejected.status, rejected.rawString())
		}
		// 同请求带 defaultImageModel 以新值为准：grok × grok-imagine-image 候选内。
		accepted := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"imageBinding":{"accountId":"account-grok"},"defaultImageModel":"grok-imagine-image"}`)
		if accepted.status != http.StatusOK {
			t.Fatalf("grok×grok-imagine-image = %d %s", accepted.status, accepted.rawString())
		}
		toolPrefsAssertBinding(t, toolPrefsTools(t, accepted), "generate_image", "account-grok", "grok-imagine-image", true, true)
	})

	t.Run("候选外与非法请求 400", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		// 英文文案经 kernel localize 中间件改写为 400 状态默认文案（与既有
		// 会话 PATCH 同机制），HTTP 层断言改写后的客户端可见行为；parse 级
		// 精确文案由 TestParseUpdateToolPreferencesBody 锁定。
		cases := []struct {
			name    string
			body    string
			code    string
			message string
		}{
			{"搜索候选外模型", `{"searchBinding":{"accountId":"account-1","modelId":"no-such-model"}}`, "chat_tool_binding_invalid", "搜索绑定必须在候选列表内（账户可派发且模型支持联网搜索）"},
			{"搜索候选外账户", `{"searchBinding":{"accountId":"account-grok","modelId":"gpt-5"}}`, "chat_tool_binding_invalid", "搜索绑定必须在候选列表内（账户可派发且模型支持联网搜索）"},
			{"生图候选外账户", `{"imageBinding":{"accountId":"account-missing"}}`, "chat_tool_binding_invalid", "生图绑定必须在候选列表内（账户可路由当前默认图像模型；如需切换模型请同时提交 defaultImageModel）"},
			{"未知键", `{"foo":1}`, "chat_invalid_request", "请求参数无效"},
			{"空请求", `{}`, "chat_invalid_request", "没有可更新的工具偏好字段"},
			{"枚举外模型", `{"defaultImageModel":"dall-e-3"}`, "chat_invalid_request", "请求参数无效"},
			{"搜索缺模型", `{"searchBinding":{"accountId":"account-1"}}`, "chat_invalid_request", "请选择工具绑定的模型"},
		}
		for _, item := range cases {
			response := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, item.body)
			if response.status != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
			}
			if response.code() != item.code || response.message() != item.message {
				t.Fatalf("%s = code %q message %q, want code %q message %q", item.name, response.code(), response.message(), item.code, item.message)
			}
		}
		// 候选校验失败负载携带候选数组与 toolId。
		invalid := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"no-such-model"}}`)
		raw := invalid.rawString()
		if !strings.Contains(raw, `"toolId":"web_search"`) || !strings.Contains(raw, `"candidates":[{`) {
			t.Fatalf("chat_tool_binding_invalid 负载 = %s", raw)
		}
		// 失败请求不落任何偏好。
		pref, err := env.fixture.store.GetUserToolPreferences(routeTestOwner)
		if err != nil || pref != nil {
			t.Fatalf("失败后偏好行 = %v err=%v, want nil", pref, err)
		}
	})

	t.Run("null 解绑与只更新请求键", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		if response := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"gpt-5"},"imageBinding":{"accountId":"account-1"}}`); response.status != http.StatusOK {
			t.Fatalf("初始设置 = %d %s", response.status, response.rawString())
		}
		unbound := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"searchBinding":null}`)
		if unbound.status != http.StatusOK {
			t.Fatalf("解绑 = %d %s", unbound.status, unbound.rawString())
		}
		tools := toolPrefsTools(t, unbound)
		toolPrefsAssertBinding(t, tools, "web_search", "", "", false, false)
		// image 键不受影响。
		toolPrefsAssertBinding(t, tools, "generate_image", "account-1", "gpt-image-2", true, true)
	})

	t.Run("偏好端点不修改任何会话", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		conversation := createBoundConversation(t, env.fixture, "tool_prefs_conv_readonly", routeTestOwner, CreateConversationInput{})
		if response := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"gpt-5"}}`); response.status != http.StatusOK {
			t.Fatalf("PATCH 偏好 = %d %s", response.status, response.rawString())
		}
		detail := env.do("GET", prefix+"/conversations/"+conversation.ID, routeTestOwner, "")
		if detail.status != http.StatusOK {
			t.Fatalf("GET 会话 = %d %s", detail.status, detail.rawString())
		}
		data := detail.dataMap()
		if _, has := data["searchAccountId"]; has {
			t.Fatalf("偏好 PATCH 不得触碰会话绑定: %v", data)
		}
	})
}

// TestUserToolPreferencesCreateInheritance：新建会话继承偏好非空列（绑定三列
// + default_image_model）；未设偏好用户维持现状（NULL / gpt-image-2）——契约
// §2.11/§10.6/§14.8-3。继承不校验候选（失效组合读侧 valid=false 兜底）。
func TestUserToolPreferencesCreateInheritance(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	t.Run("偏好非空列继承", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		if err := env.fixture.store.UpsertUserToolPreferences(UserToolPreferences{
			SystemAccountID:   routeTestOwner,
			SearchAccountID:   "account-1",
			SearchModelID:     "gpt-5",
			ImageAccountID:    "account-grok",
			DefaultImageModel: "grok-imagine-image",
		}); err != nil {
			t.Fatal(err)
		}
		created := env.do("POST", prefix+"/conversations", routeTestOwner, "")
		if created.status != http.StatusCreated {
			t.Fatalf("create = %d %s", created.status, created.rawString())
		}
		data := created.dataMap()
		if data["searchAccountId"] != "account-1" || data["searchModelId"] != "gpt-5" {
			t.Fatalf("搜索继承 = %v", data)
		}
		if data["imageAccountId"] != "account-grok" || data["defaultImageModel"] != "grok-imagine-image" {
			t.Fatalf("生图继承 = %v", data)
		}
	})

	t.Run("偏好部分列继承（空列不覆盖默认）", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		if err := env.fixture.store.UpsertUserToolPreferences(UserToolPreferences{
			SystemAccountID: routeTestOwner,
			SearchAccountID: "account-1",
			SearchModelID:   "gpt-5",
			ImageAccountID:  "",
		}); err != nil {
			t.Fatal(err)
		}
		created := env.do("POST", prefix+"/conversations", routeTestOwner, "")
		if created.status != http.StatusCreated {
			t.Fatalf("create = %d %s", created.status, created.rawString())
		}
		data := created.dataMap()
		if data["searchAccountId"] != "account-1" || data["searchModelId"] != "gpt-5" {
			t.Fatalf("搜索继承 = %v", data)
		}
		if _, has := data["imageAccountId"]; has {
			t.Fatalf("生图未设默认应保持 NULL: %v", data)
		}
		if data["defaultImageModel"] != "gpt-image-2" {
			t.Fatalf("defaultImageModel = %v, want gpt-image-2", data["defaultImageModel"])
		}
	})

	t.Run("未设偏好维持现状", func(t *testing.T) {
		env := newToolPrefsEnv(t)
		created := env.do("POST", prefix+"/conversations", routeTestOwner, "")
		if created.status != http.StatusCreated {
			t.Fatalf("create = %d %s", created.status, created.rawString())
		}
		data := created.dataMap()
		for _, key := range []string{"searchAccountId", "searchModelId", "imageAccountId"} {
			if _, has := data[key]; has {
				t.Fatalf("未设偏好 %s 应为 null: %v", key, data)
			}
		}
		if data["defaultImageModel"] != "gpt-image-2" {
			t.Fatalf("defaultImageModel = %v, want gpt-image-2", data["defaultImageModel"])
		}
	})
}

// TestConversationPatchWritesBackToolPreferences：会话内显式改/解绑
// searchBinding/imageBinding 成功后回写全局默认（解绑置空；生图回写含联动后
// default_image_model）；单独 defaultImageModel 不回写；归档 403 不回写；回写
// 只动对应列不覆盖另一工具的既有默认——契约 §2.11/§10.6/§14.8-4。
func TestConversationPatchWritesBackToolPreferences(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	newConv := func(t *testing.T, id string) (*generationEnv, string) {
		env := newToolPrefsEnv(t)
		conversation := createBoundConversation(t, env.fixture, id, routeTestOwner, CreateConversationInput{})
		return env, conversation.ID
	}

	assertPreference := func(t *testing.T, env *generationEnv, want *UserToolPreferences) {
		t.Helper()
		pref, err := env.fixture.store.GetUserToolPreferences(routeTestOwner)
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			if pref != nil {
				t.Fatalf("偏好行 = %+v, want 无行", pref)
			}
			return
		}
		if pref == nil {
			t.Fatalf("偏好行缺失, want %+v", *want)
		}
		if *pref != *want {
			t.Fatalf("偏好行 = %+v, want %+v", *pref, *want)
		}
	}

	t.Run("searchBinding 回写与解绑置空", func(t *testing.T) {
		env, conversationID := newConv(t, "conv_writeback")
		patched := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"gpt-5"}}`)
		if patched.status != http.StatusOK {
			t.Fatalf("PATCH 会话 = %d %s", patched.status, patched.rawString())
		}
		assertPreference(t, env, &UserToolPreferences{SystemAccountID: routeTestOwner, SearchAccountID: "account-1", SearchModelID: "gpt-5"})
		// GET tool-preferences 反映同一变化。
		tools := toolPrefsTools(t, env.do("GET", prefix+"/tool-preferences", routeTestOwner, ""))
		toolPrefsAssertBinding(t, tools, "web_search", "account-1", "gpt-5", true, true)

		unbound := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"searchBinding":null}`)
		if unbound.status != http.StatusOK {
			t.Fatalf("解绑 = %d %s", unbound.status, unbound.rawString())
		}
		assertPreference(t, env, &UserToolPreferences{SystemAccountID: routeTestOwner})
	})

	t.Run("imageBinding 联动回写 defaultImageModel", func(t *testing.T) {
		env, conversationID := newConv(t, "conv_writeback")
		patched := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"imageBinding":{"accountId":"account-grok"},"defaultImageModel":"grok-imagine-image"}`)
		if patched.status != http.StatusOK {
			t.Fatalf("PATCH 会话 = %d %s", patched.status, patched.rawString())
		}
		assertPreference(t, env, &UserToolPreferences{SystemAccountID: routeTestOwner, ImageAccountID: "account-grok", DefaultImageModel: "grok-imagine-image"})
		if data := patched.dataMap(); data["defaultImageModel"] != "grok-imagine-image" || data["imageAccountId"] != "account-grok" {
			t.Fatalf("会话生效值 = %v", data)
		}
	})

	t.Run("单独 defaultImageModel 不回写", func(t *testing.T) {
		env, conversationID := newConv(t, "conv_writeback")
		patched := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"defaultImageModel":"grok-imagine-image"}`)
		if patched.status != http.StatusOK {
			t.Fatalf("PATCH 会话 = %d %s", patched.status, patched.rawString())
		}
		assertPreference(t, env, nil)
	})

	t.Run("归档会话绑定修改 403 且偏好不变", func(t *testing.T) {
		env, conversationID := newConv(t, "conv_writeback")
		archiveConversation(t, env.fixture, conversationID)
		rejected := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"gpt-5"}}`)
		if rejected.status != http.StatusForbidden || rejected.code() != "chat_conversation_archived" {
			t.Fatalf("归档 PATCH = %d %s", rejected.status, rejected.rawString())
		}
		assertPreference(t, env, nil)
	})

	t.Run("回写不覆盖另一工具的既有默认", func(t *testing.T) {
		env, conversationID := newConv(t, "conv_writeback")
		// 先经偏好端点设生图默认（image 列 + defaultImageModel），再改会话
		// 搜索绑定：偏好行的生图两列保持，搜索列回写。
		if response := env.do("PATCH", prefix+"/tool-preferences", routeTestOwner, `{"imageBinding":{"accountId":"account-1"},"defaultImageModel":"gpt-image-2"}`); response.status != http.StatusOK {
			t.Fatalf("设生图默认 = %d %s", response.status, response.rawString())
		}
		patched := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"searchBinding":{"accountId":"account-1","modelId":"gpt-5"}}`)
		if patched.status != http.StatusOK {
			t.Fatalf("PATCH 会话搜索 = %d %s", patched.status, patched.rawString())
		}
		assertPreference(t, env, &UserToolPreferences{
			SystemAccountID: routeTestOwner,
			SearchAccountID: "account-1", SearchModelID: "gpt-5",
			ImageAccountID: "account-1", DefaultImageModel: "gpt-image-2",
		})
		// 反向：再改会话生图绑定，搜索默认保持。
		image := env.do("PATCH", prefix+"/conversations/"+conversationID, routeTestOwner, `{"imageBinding":{"accountId":"account-grok"},"defaultImageModel":"grok-imagine-image"}`)
		if image.status != http.StatusOK {
			t.Fatalf("PATCH 会话生图 = %d %s", image.status, image.rawString())
		}
		assertPreference(t, env, &UserToolPreferences{
			SystemAccountID: routeTestOwner,
			SearchAccountID: "account-1", SearchModelID: "gpt-5",
			ImageAccountID: "account-grok", DefaultImageModel: "grok-imagine-image",
		})
	})
}

// TestParseUpdateToolPreferencesBody：parse 级精确文案（HTTP 层英文消息会被
// kernel localize 改写，此处直接锁定 parse 契约）与至少一键/严格键集语义。
func TestParseUpdateToolPreferencesBody(t *testing.T) {
	full, err := parseUpdateToolPreferencesBody(map[string]json.RawMessage{
		"searchBinding":     json.RawMessage(`{"accountId":"account-1","modelId":"gpt-5"}`),
		"imageBinding":      json.RawMessage(`null`),
		"defaultImageModel": json.RawMessage(`"grok-imagine-image"`),
	})
	if err != nil {
		t.Fatalf("合法请求解析失败: %v", err)
	}
	if full.searchBinding == nil || full.searchBinding.accountID != "account-1" || full.searchBinding.modelID != "gpt-5" {
		t.Fatalf("searchBinding = %+v", full.searchBinding)
	}
	if full.imageBinding == nil || !full.imageBinding.unbound {
		t.Fatalf("imageBinding = %+v, want unbound", full.imageBinding)
	}
	if full.defaultImageModel == nil || *full.defaultImageModel != "grok-imagine-image" {
		t.Fatalf("defaultImageModel = %v", full.defaultImageModel)
	}
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"未知键", `{"foo":1}`, `Unrecognized key: "foo"`},
		{"空请求", `{}`, "没有可更新的工具偏好字段"},
		{"枚举外模型", `{"defaultImageModel":"dall-e-3"}`, "Invalid enum value. Expected one of: 'gpt-image-2', 'grok-imagine-image', 'grok-imagine-image-quality', received 'dall-e-3'"},
		{"搜索缺模型", `{"searchBinding":{"accountId":"account-1"}}`, "请选择工具绑定的模型"},
		{"生图带模型键", `{"imageBinding":{"accountId":"account-1","modelId":"gpt-image-2"}}`, `Unrecognized key: "modelId"`},
		{"模型非字符串", `{"defaultImageModel":1}`, "Expected string, received number"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(testCase.raw), &raw); err != nil {
				t.Fatalf("载荷无效: %v", err)
			}
			_, err := parseUpdateToolPreferencesBody(raw)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, want 含 %q", err, testCase.wantErr)
			}
		})
	}
}

// TestUserToolPreferencesStoreUpsert：store 级 Get（无行 nil,nil）/整行 upsert
// 幂等（二次写同值不报错）/覆盖更新（二次写全空 = 全部清除默认）。
func TestUserToolPreferencesStoreUpsert(t *testing.T) {
	fixture := newChatFixture(t)
	pref, err := fixture.store.GetUserToolPreferences("owner-pref")
	if err != nil {
		t.Fatal(err)
	}
	if pref != nil {
		t.Fatalf("无行 Get = %+v, want nil", pref)
	}
	first := UserToolPreferences{
		SystemAccountID: "owner-pref", SearchAccountID: "account-1", SearchModelID: "gpt-5",
		ImageAccountID: "account-grok", DefaultImageModel: "grok-imagine-image",
	}
	if err := fixture.store.UpsertUserToolPreferences(first); err != nil {
		t.Fatal(err)
	}
	// 幂等：二次写同值不报错，读回一致。
	if err := fixture.store.UpsertUserToolPreferences(first); err != nil {
		t.Fatalf("二次 upsert = %v", err)
	}
	got, err := fixture.store.GetUserToolPreferences("owner-pref")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != first {
		t.Fatalf("upsert 后 = %+v, want %+v", got, first)
	}
	// 覆盖更新：整行 upsert 支持清除全部默认。
	cleared := UserToolPreferences{SystemAccountID: "owner-pref"}
	if err := fixture.store.UpsertUserToolPreferences(cleared); err != nil {
		t.Fatal(err)
	}
	got, err = fixture.store.GetUserToolPreferences("owner-pref")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != cleared {
		t.Fatalf("清除后 = %+v, want %+v", got, cleared)
	}
}
