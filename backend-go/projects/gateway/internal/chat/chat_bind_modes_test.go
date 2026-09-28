package chat

// AI 问答会话账户唯一绑定（docs/functions/AI问答会话账户唯一绑定设计.md）的
// 路由级覆盖：免请求体创建空会话、PATCH accountId 写入/切换与模型联动、
// 发送预检（未绑定 400 chat_account_required、归档 403 只读）、模型列表按绑定
// 账户收敛。Mock 风格与 generation_test.go 一致（严格 mock 闭包 + 录制断言）。

import (
	"net/http"
	"sync"
	"testing"
	"strings"
)

// bindScopeRecorder 记录 mock 收到的 (scope, id) 序列，供 scope 传递断言
// （普通用户=请求者 viewer，admin=IsAdmin true）。
type bindScopeRecorder struct {
	mu     sync.Mutex
	scopes []ChatBindScope
	ids    []string
}

func (r *bindScopeRecorder) record(scope ChatBindScope, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, scope)
	r.ids = append(r.ids, id)
}

func (r *bindScopeRecorder) snapshot() (scopes []ChatBindScope, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ChatBindScope{}, r.scopes...), append([]string{}, r.ids...)
}

// mockAccountLookup resolves test accounts: account-1/account-2/account-3 启
// 用（默认绑定 group-a，可经 groupsOfAccount 覆盖），account-disabled 停用，
// 其余不存在；seen 非 nil 时记录收到的 scope 与 accountID。
type mockAccountLookup struct {
	groupsOfAccount map[string][]string
	seen            *bindScopeRecorder
}

func (m mockAccountLookup) FindChatAccount(scope ChatBindScope, accountID string) (*ChatAccountRef, error) {
	if m.seen != nil {
		m.seen.record(scope, accountID)
	}
	switch accountID {
	case "account-1", "account-2", "account-3":
		groups := []string{"group-a"}
		if m.groupsOfAccount != nil {
			groups = m.groupsOfAccount[accountID]
		}
		return &ChatAccountRef{ID: accountID, Name: "账户 " + accountID, ProviderCode: "openai", Enabled: true, EnabledGroupIDs: groups}, nil
	case "account-disabled":
		return &ChatAccountRef{ID: accountID, Name: "停用账户", Enabled: false}, nil
	}
	return nil, nil
}

// owningChatKeys 模拟 Key 属主校验：other_key 属于他人、missing_key 不存在，
// find 返回 nil 走 requireOwnedApiKey 的"API Key 不存在或不可用"分支。
type owningChatKeys struct {
	mockChatKeys
}

func (m *owningChatKeys) FindChatAPIKey(keyID, ownerID string) (*ChatAPIKeyRecord, error) {
	if keyID == "other_key" || keyID == "missing_key" {
		return nil, nil
	}
	return &ChatAPIKeyRecord{ID: keyID, Name: "对话密钥", Secret: "chat-secret", Status: "active"}, nil
}

// createBoundConversation 直接经 Store 建立会话夹具（可携带绑定账户）。
func createBoundConversation(t *testing.T, fixture *chatFixture, id, ownerID string, input CreateConversationInput) *Conversation {
	t.Helper()
	input.ID = id
	input.SystemAccountID = ownerID
	input.APIKeyID = "chat_key_1"
	input.APIKeyNameSnapshot = "对话密钥"
	input.Now = fixture.nowISO
	input.MaxConversationsPerUser = 30
	conversation, err := fixture.store.CreateConversation(input)
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

// archiveConversation 把夹具会话置为归档（模拟一次性迁移对存量旧模式行的
// archived=1 标记）。
func archiveConversation(t *testing.T, fixture *chatFixture, conversationID string) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`UPDATE chat_conversations SET archived = 1 WHERE id = ?`, conversationID); err != nil {
		t.Fatal(err)
	}
}

// setConversationLastModel 直写夹具会话的 last_model（模型联动断言用）。
func setConversationLastModel(t *testing.T, fixture *chatFixture, conversationID, model string) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`UPDATE chat_conversations SET last_model = ? WHERE id = ?`, model, conversationID); err != nil {
		t.Fatal(err)
	}
}

// TestCreateConversationEmptyBody：免弹窗直进契约——空 body 与携带历史字段
// （bindMode/apiKeyId/groupId/accountId）的 body 均创建空会话（bindAccountId
// NULL、archived=false），鉴权主体为 EnsureChatAPIKey 幂等补齐的专用 Key。
func TestCreateConversationEmptyBody(t *testing.T) {
	env := newGenerationEnv(t)
	prefix := "/__aisys__/api/my-chat"

	created := env.do("POST", prefix+"/conversations", routeTestOwner, "")
	if created.status != http.StatusCreated {
		t.Fatalf("empty body create = %d %s", created.status, created.rawString())
	}
	data := created.dataMap()
	if _, has := data["bindAccountId"]; has {
		t.Fatalf("空会话不应返回 bindAccountId: %v", data)
	}
	if data["archived"] != false {
		t.Fatalf("空会话 archived = %v, want false", data["archived"])
	}
	if data["apiKeyId"] != "chat_key_provisioned" || data["apiKeyNameSnapshot"] != "对话密钥" {
		t.Fatalf("鉴权主体 payload = %v", data)
	}
	if _, has := data["defaultModel"]; has {
		t.Fatalf("空会话不应携带 defaultModel: %v", data)
	}
	if data["lastModel"] != nil {
		t.Fatalf("空会话 lastModel = %v, want null", data["lastModel"])
	}
	env.chatKeys.mu.Lock()
	ensureCount := env.chatKeys.ensureCount
	env.chatKeys.mu.Unlock()
	if ensureCount != 1 {
		t.Fatalf("ensure count = %d, want 1", ensureCount)
	}

	// 历史字段按兼容口径忽略：携带 bindMode/accountId 的旧客户端请求体仍创建
	// 空会话，不因未知字段 400。
	legacy := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bindMode":"account","apiKeyId":"legacy","groupId":"g","accountId":"a"}`)
	if legacy.status != http.StatusCreated {
		t.Fatalf("legacy body create = %d %s", legacy.status, legacy.rawString())
	}
	legacyData := legacy.dataMap()
	if _, has := legacyData["bindAccountId"]; has {
		t.Fatalf("legacy body 不得写入绑定: %v", legacyData)
	}

	// 非法 JSON 仍按既有 400 契约拒绝（Express json() 同款）。
	malformed := env.do("POST", prefix+"/conversations", routeTestOwner, `{`)
	if malformed.status != http.StatusBadRequest || malformed.code() != "chat_invalid_request" {
		t.Fatalf("malformed body = %d %s", malformed.status, malformed.rawString())
	}
}

// TestPatchAccountIdFlow：accountId 键写入/切换绑定账户；切换时 lastModel 不
// 在新账户可路由范围则联动清空，在范围内保留；停用/不存在/空白值 400；
// searchBinding/imageBinding 属工具阶段契约，本阶段按未知键拒绝。
func TestPatchAccountIdFlow(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	t.Run("首次写入绑定并回显名称快照", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{seen: &bindScopeRecorder{}}
		conversation := createBoundConversation(t, env.fixture, "patch_bind_conv", routeTestOwner, CreateConversationInput{})
		response := env.do("PATCH", prefix+"/conversations/"+conversation.ID, routeTestOwner, `{"accountId":"account-1"}`)
		if response.status != http.StatusOK {
			t.Fatalf("patch accountId = %d %s", response.status, response.rawString())
		}
		data := response.dataMap()
		if data["bindAccountId"] != "account-1" || data["bindAccountName"] != "账户 account-1" {
			t.Fatalf("bind payload = %v", data)
		}
	})

	t.Run("切换账户联动清空不可路由模型", func(t *testing.T) {
		env := newGenerationEnv(t)
		// account-1（group-a）仅 gpt-5-mini 可路由；account-2（group-b）仅 gpt-5。
		catalog := &accountViewCatalog{views: map[string]ChatTransportAccount{
			"group-a": {
				ID: "account-1", Type: "api_key", ProviderCode: "openai",
				SupportedEndpointModes: []string{"chat_sse"},
				SupportedModels:        []string{"gpt-5-mini"},
			},
			"group-b": {
				ID: "account-2", Type: "api_key", ProviderCode: "openai",
				SupportedEndpointModes: []string{"chat_sse"},
				SupportedModels:        []string{"gpt-5"},
			},
		}}
		env.deps.ModelCatalog = catalog
		env.deps.AccountLookup = mockAccountLookup{groupsOfAccount: map[string][]string{
			"account-1": {"group-a"}, "account-2": {"group-b"},
		}}
		conversation := createBoundConversation(t, env.fixture, "patch_switch_conv", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-2", BindAccountNameSnapshot: "账户 account-2",
		})
		setConversationLastModel(t, env.fixture, conversation.ID, "gpt-5")

		// 切到 account-1：gpt-5 不在 [gpt-5-mini] → 清空。
		switched := env.do("PATCH", prefix+"/conversations/"+conversation.ID, routeTestOwner, `{"accountId":"account-1"}`)
		if switched.status != http.StatusOK {
			t.Fatalf("switch = %d %s", switched.status, switched.rawString())
		}
		if data := switched.dataMap(); data["lastModel"] != nil {
			t.Fatalf("切换后 lastModel = %v, want null", data["lastModel"])
		}

		// 切回 account-2 前先恢复 last_model=gpt-5：范围内 → 保留。
		setConversationLastModel(t, env.fixture, conversation.ID, "gpt-5")
		kept := env.do("PATCH", prefix+"/conversations/"+conversation.ID, routeTestOwner, `{"accountId":"account-2"}`)
		if kept.status != http.StatusOK {
			t.Fatalf("switch back = %d %s", kept.status, kept.rawString())
		}
		if data := kept.dataMap(); data["lastModel"] != "gpt-5" {
			t.Fatalf("范围内切换 lastModel = %v, want gpt-5", data["lastModel"])
		}
	})

	t.Run("失败臂", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
		conversation := createBoundConversation(t, env.fixture, "patch_fail_conv", routeTestOwner, CreateConversationInput{})
		cases := []struct {
			name    string
			body    string
			message string
		}{
			{"账户不存在", `{"accountId":"missing"}`, "绑定的账户不存在"},
			{"账户已停用", `{"accountId":"account-disabled"}`, "绑定的账户已停用"},
			{"空白账户", `{"accountId":"  "}`, "请选择会话绑定的账户"},
			// 工具绑定键（阶段 2 接入）：二元组不在候选内 → 400 + 候选返回
			//（mock 账户 a1 无可派发视图，候选为空）。
			{"搜索绑定不在候选", `{"searchBinding":{"accountId":"a1","modelId":"gpt-5"}}`, "搜索绑定必须在候选列表内（账户可派发且模型支持联网搜索）"},
			{"生图绑定不在候选", `{"imageBinding":{"accountId":"a1"}}`, "生图绑定必须在候选列表内（账户可路由当前默认图像模型；如需切换模型请同时提交 defaultImageModel）"},
			{"搜索绑定缺模型", `{"searchBinding":{"accountId":"a1"}}`, "请选择工具绑定的模型"},
		}
		for _, item := range cases {
			response := env.do("PATCH", prefix+"/conversations/"+conversation.ID, routeTestOwner, item.body)
			if response.status != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
			}
			if got := response.message(); got != item.message {
				t.Fatalf("%s message = %q, want %q", item.name, got, item.message)
			}
		}
		// 候选校验失败的负载携带候选数组与 toolId。
		invalid := env.do("PATCH", prefix+"/conversations/"+conversation.ID, routeTestOwner, `{"searchBinding":{"accountId":"a1","modelId":"gpt-5"}}`)
		if invalid.code() != "chat_tool_binding_invalid" || !strings.Contains(invalid.rawString(), `"toolId":"web_search"`) || !strings.Contains(invalid.rawString(), `"candidates":[]`) {
			t.Fatalf("chat_tool_binding_invalid 负载 = %s", invalid.rawString())
		}
	})
}

// TestAccountBindingSendPrechecks：发送预检——未绑定 400 chat_account_required；
// 归档会话 403 只读（clear/delete 照常、PATCH accountId 拒绝、压缩同拒）。
func TestAccountBindingSendPrechecks(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	t.Run("未绑定发送 400 chat_account_required", func(t *testing.T) {
		env := newGenerationEnv(t)
		conversation := createBoundConversation(t, env.fixture, "precheck_unbound", routeTestOwner, CreateConversationInput{})
		response := env.streamPost(conversation.ID, routeTestOwner, streamPayload("cmid-unbound", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || response.code() != "chat_account_required" {
			t.Fatalf("未绑定发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("未绑定不得派发上游")
		}
	})

	t.Run("归档会话只读", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		conversation := createBoundConversation(t, env.fixture, "precheck_archived", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		archiveConversation(t, env.fixture, conversation.ID)

		sent := env.streamPost(conversation.ID, routeTestOwner, streamPayload("cmid-archived", "问题", "gpt-5"))
		if sent.status != http.StatusForbidden || sent.code() != "chat_conversation_archived" || sent.message() != "该会话绑定方式已升级，请新建会话" {
			t.Fatalf("归档发送 = %d %s", sent.status, sent.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("归档会话不得派发上游")
		}

		compacted := env.do("POST", prefix+"/conversations/"+conversation.ID+"/context/compactions", routeTestOwner, `{"model":"gpt-5"}`)
		if compacted.status != http.StatusForbidden || compacted.code() != "chat_conversation_archived" {
			t.Fatalf("归档压缩 = %d %s", compacted.status, compacted.rawString())
		}

		patched := env.do("PATCH", prefix+"/conversations/"+conversation.ID, routeTestOwner, `{"accountId":"account-2"}`)
		if patched.status != http.StatusForbidden || patched.code() != "chat_conversation_archived" {
			t.Fatalf("归档切账户 = %d %s", patched.status, patched.rawString())
		}

		// 归档 clear 与资产上传同闸只读；delete 照常（用户可自行删除归档会话）。
		cleared := env.do("POST", prefix+"/conversations/"+conversation.ID+"/clear", routeTestOwner, "{}")
		if cleared.status != http.StatusForbidden || cleared.code() != "chat_conversation_archived" {
			t.Fatalf("归档 clear = %d %s", cleared.status, cleared.rawString())
		}
		uploaded := env.do("POST", prefix+"/conversations/"+conversation.ID+"/assets", routeTestOwner, "")
		if uploaded.status != http.StatusForbidden || uploaded.code() != "chat_conversation_archived" {
			t.Fatalf("归档上传资产 = %d %s", uploaded.status, uploaded.rawString())
		}
		deleted := env.do("DELETE", prefix+"/conversations/"+conversation.ID, routeTestOwner, "")
		if deleted.status != http.StatusNoContent {
			t.Fatalf("归档 delete = %d %s", deleted.status, deleted.rawString())
		}
	})
}

// accountViewCatalog 让账户绑定测试精确控制收敛后的账户传输视图（provider、
// supportedModels），覆盖 supported_models 交集与 provider_code 单值来源。
type accountViewCatalog struct {
	mu            sync.Mutex
	views         map[string]ChatTransportAccount
	groupCalls    []string
	providerCalls []string
}

func (c *accountViewCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	c.mu.Lock()
	c.groupCalls = append(c.groupCalls, groupID)
	view := c.views[groupID]
	c.mu.Unlock()
	if view.ID == "" {
		return nil
	}
	return []ChatTransportAccount{view}
}

func (c *accountViewCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	c.mu.Lock()
	c.providerCalls = append(c.providerCalls, providerCode)
	c.mu.Unlock()
	return mockModelCatalog{}.ListProviderCatalog(providerCode, systemAccountID)
}

func (c *accountViewCatalog) snapshot() (groups, providers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.groupCalls...), append([]string{}, c.providerCalls...)
}

// TestAccountBindingModelListSources：模型候选=会话绑定账户可路由模型；未绑定
// 返回空列表与单模型 404；绑定账户的 provider 单值与 supported_models 交集；
// 不在启用分组快照中为空作用域。
func TestAccountBindingModelListSources(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	t.Run("未绑定空列表与单模型 404", func(t *testing.T) {
		env := newGenerationEnv(t)
		conversation := createBoundConversation(t, env.fixture, "models_unbound", routeTestOwner, CreateConversationInput{})
		list := env.do("GET", prefix+"/conversations/"+conversation.ID+"/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("list = %d %s", list.status, list.rawString())
		}
		if models := list.dataArray(); len(models) != 0 {
			t.Fatalf("未绑定 models = %v, want []", models)
		}
		missing := env.do("GET", prefix+"/conversations/"+conversation.ID+"/models/gpt-5", routeTestOwner, "")
		if missing.status != http.StatusNotFound || missing.code() != "chat_model_not_found" {
			t.Fatalf("未绑定单模型 = %d %s", missing.status, missing.rawString())
		}
	})

	t.Run("绑定账户单 provider 与 supported_models 交集", func(t *testing.T) {
		env := newGenerationEnv(t)
		catalog := &accountViewCatalog{views: map[string]ChatTransportAccount{
			// account-1 的运行时视图：openai、仅 gpt-5-mini。
			"group-a": {
				ID: "account-1", Type: "api_key", ProviderCode: "openai",
				SupportedEndpointModes: []string{"chat_sse"},
				SupportedModels:        []string{"gpt-5-mini"},
			},
		}}
		env.deps.ModelCatalog = catalog
		env.deps.AccountLookup = mockAccountLookup{}
		createBoundConversation(t, env.fixture, "bind_conv_account", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		list := env.do("GET", prefix+"/conversations/bind_conv_account/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("list = %d %s", list.status, list.rawString())
		}
		groups, providers := catalog.snapshot()
		if len(groups) != 1 || groups[0] != "group-a" {
			t.Fatalf("group calls = %v, want [group-a]", groups)
		}
		if len(providers) != 1 || providers[0] != "openai" {
			t.Fatalf("provider calls = %v, want [openai]", providers)
		}
		models := modelIDsFromList(t, list)
		if len(models) != 1 || !models["gpt-5-mini"] {
			t.Fatalf("交集 models = %v, want [gpt-5-mini]", list.dataArray())
		}
		// 交集外的模型在能力接口返回 404；交集内的模型返回能力。
		missing := env.do("GET", prefix+"/conversations/bind_conv_account/models/gpt-5", routeTestOwner, "")
		if missing.status != http.StatusNotFound || missing.code() != "chat_model_not_found" {
			t.Fatalf("交集外模型 = %d %s", missing.status, missing.rawString())
		}
		capability := env.do("GET", prefix+"/conversations/bind_conv_account/models/gpt-5-mini", routeTestOwner, "")
		if capability.status != http.StatusOK {
			t.Fatalf("交集内模型 = %d %s", capability.status, capability.rawString())
		}
	})

	t.Run("绑定账户不在快照中为空作用域", func(t *testing.T) {
		env := newGenerationEnv(t)
		catalog := &accountViewCatalog{views: map[string]ChatTransportAccount{
			"group-a": {ID: "account-9", Type: "api_key", ProviderCode: "openai", SupportedEndpointModes: []string{"chat_sse"}},
		}}
		env.deps.ModelCatalog = catalog
		env.deps.AccountLookup = mockAccountLookup{}
		createBoundConversation(t, env.fixture, "bind_conv_absent", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		list := env.do("GET", prefix+"/conversations/bind_conv_absent/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("list = %d %s", list.status, list.rawString())
		}
		if models := list.dataArray(); len(models) != 0 {
			t.Fatalf("空作用域 models = %v", models)
		}
	})
}

// modelIDsFromList 把模型列表响应转成 id 集合。
func modelIDsFromList(t *testing.T, response routeResponse) map[string]bool {
	t.Helper()
	models := map[string]bool{}
	for _, item := range response.dataArray() {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		models[id] = true
	}
	return models
}
