package chat

// AI 问答会话三种绑定模式（bindMode = api_key | group | account）的路由级
// 覆盖：创建矩阵（成功 + 400/校验失败）、EnsureChatAPIKey 的调用边界、
// 模型列表/能力接口按绑定模式的 provider_codes 来源与 supported_models 交集。
// Mock 风格与 generation_test.go 一致（严格 mock 闭包 + 录制断言）。

import (
	"net/http"
	"sync"
	"testing"
)

// mockGroupLookup resolves test groups: group-a/group-b 启用，disabled 停用，
// 其余不存在。
type mockGroupLookup struct{}

func (mockGroupLookup) FindChatGroup(groupID string) (*ChatGroupRef, error) {
	switch groupID {
	case "group-a":
		return &ChatGroupRef{ID: groupID, Name: "分组 A", Enabled: true}, nil
	case "group-b":
		return &ChatGroupRef{ID: groupID, Name: "分组 B", Enabled: true}, nil
	case "group-disabled":
		return &ChatGroupRef{ID: groupID, Name: "停用分组", Enabled: false}, nil
	}
	return nil, nil
}

// mockAccountLookup resolves test accounts: account-1 启用（绑定 group-a），
// account-disabled 停用，其余不存在。
type mockAccountLookup struct {
	groupsOfAccount map[string][]string
}

func (m mockAccountLookup) FindChatAccount(accountID string) (*ChatAccountRef, error) {
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

// bindModeCatalog records group/provider calls and serves per-group provider
// codes so the model-list source assertions can discriminate scopes:
// group-a → openai（gpt-5, gpt-5-mini），group-b → anthropic（claude-x）。
type bindModeCatalog struct {
	mu            sync.Mutex
	groupCalls    []string
	providerCalls []string
}

func (c *bindModeCatalog) recordGroup(groupID string) {
	c.mu.Lock()
	c.groupCalls = append(c.groupCalls, groupID)
	c.mu.Unlock()
}

func (c *bindModeCatalog) recordProvider(providerCode string) {
	c.mu.Lock()
	c.providerCalls = append(c.providerCalls, providerCode)
	c.mu.Unlock()
}

func (c *bindModeCatalog) snapshot() (groups, providers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.groupCalls...), append([]string{}, c.providerCalls...)
}

func (c *bindModeCatalog) groupProviderCode(groupID string) string {
	switch groupID {
	case "group-b":
		return "anthropic"
	default:
		return "openai"
	}
}

func (c *bindModeCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	c.recordGroup(groupID)
	enabled := true
	return []ChatTransportAccount{{
		ID: "account-1", Type: "api_key", ProviderCode: c.groupProviderCode(groupID),
		SupportedEndpointModes: []string{"chat_sse", "responses_sse"},
		ModelMappings: []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: requestedModel, SourceEndpointFamily: endpointFamily,
		}},
	}}
}

func (c *bindModeCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	c.recordProvider(providerCode)
	switch providerCode {
	case "anthropic":
		return []ProviderModelCatalogItem{{
			Model: "claude-x", ProviderCode: "anthropic",
			SupportedAPIProtocols: []string{"chat_completions"},
			InputModalities:       []string{"text"}, OutputModalities: []string{"text"},
		}}
	default:
		return mockModelCatalog{}.ListProviderCatalog(providerCode, systemAccountID)
	}
}

// accountViewCatalog 让账号模式测试精确控制收敛后的账户传输视图（provider、
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

// createBoundConversation 直接经 Store 建立带绑定字段的会话夹具。
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

func TestBindModeCreateSuccessMatrix(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupLookup = mockGroupLookup{}
	env.deps.AccountLookup = mockAccountLookup{}
	prefix := "/__aisys__/api/my-chat"

	// api_key：显式选择用户 Key，不触碰专用 Key。
	byKey := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bindMode":"api_key","apiKeyId":"chat_key_provisioned"}`)
	if byKey.status != http.StatusCreated {
		t.Fatalf("api_key create = %d %s", byKey.status, byKey.rawString())
	}
	data := byKey.dataMap()
	if data["bindMode"] != "api_key" || data["apiKeyId"] != "chat_key_provisioned" {
		t.Fatalf("api_key payload = %v", data)
	}
	if _, has := data["bindGroupId"]; has {
		t.Fatalf("api_key 模式不应返回 bindGroupId: %v", data)
	}
	if _, has := data["bindAccountId"]; has {
		t.Fatalf("api_key 模式不应返回 bindAccountId: %v", data)
	}
	env.chatKeys.mu.Lock()
	ensureCount := env.chatKeys.ensureCount
	env.chatKeys.mu.Unlock()
	if ensureCount != 0 {
		t.Fatalf("api_key mode ensure count = %d, want 0", ensureCount)
	}

	// group：鉴权主体为 EnsureChatAPIKey 幂等补齐的专用 Key。
	byGroup := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bindMode":"group","groupId":"group-b"}`)
	if byGroup.status != http.StatusCreated {
		t.Fatalf("group create = %d %s", byGroup.status, byGroup.rawString())
	}
	data = byGroup.dataMap()
	if data["bindMode"] != "group" || data["bindGroupId"] != "group-b" || data["bindGroupName"] != "分组 B" {
		t.Fatalf("group payload = %v", data)
	}
	if data["apiKeyId"] != "chat_key_provisioned" || data["apiKeyNameSnapshot"] != "对话密钥" {
		t.Fatalf("group 鉴权主体 payload = %v", data)
	}
	defaultModel, _ := data["defaultModel"].(map[string]any)
	if defaultModel == nil || defaultModel["id"] != "gpt-5" {
		t.Fatalf("group defaultModel = %v", data["defaultModel"])
	}
	env.chatKeys.mu.Lock()
	ensureCount = env.chatKeys.ensureCount
	env.chatKeys.mu.Unlock()
	if ensureCount != 1 {
		t.Fatalf("group mode ensure count = %d, want 1", ensureCount)
	}

	// account：鉴权主体同样是专用 Key，绑定对象为账户快照。
	byAccount := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bindMode":"account","accountId":"account-1"}`)
	if byAccount.status != http.StatusCreated {
		t.Fatalf("account create = %d %s", byAccount.status, byAccount.rawString())
	}
	data = byAccount.dataMap()
	if data["bindMode"] != "account" || data["bindAccountId"] != "account-1" || data["bindAccountName"] != "账户 account-1" {
		t.Fatalf("account payload = %v", data)
	}
	if data["apiKeyId"] != "chat_key_provisioned" {
		t.Fatalf("account 鉴权主体 payload = %v", data)
	}
	env.chatKeys.mu.Lock()
	ensureCount = env.chatKeys.ensureCount
	env.chatKeys.mu.Unlock()
	if ensureCount != 2 {
		t.Fatalf("account mode ensure count = %d, want 2", ensureCount)
	}

	// 会话详情回读绑定字段。
	detail := env.do("GET", prefix+"/conversations/"+data["id"].(string), routeTestOwner, "")
	if detail.status != http.StatusOK {
		t.Fatalf("detail = %d %s", detail.status, detail.rawString())
	}
	detailData := detail.dataMap()
	if detailData["bindMode"] != "account" || detailData["bindAccountId"] != "account-1" || detailData["bindAccountName"] != "账户 account-1" {
		t.Fatalf("detail bind payload = %v", detailData)
	}
}

func TestBindModeCreateFailures(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupLookup = mockGroupLookup{}
	env.deps.AccountLookup = mockAccountLookup{}
	env.deps.ChatKeys = &owningChatKeys{}
	prefix := "/__aisys__/api/my-chat"
	// 与模式不符的键复用未知键错误契约（英文消息经 kernel 本地化为 400 状态
	// 默认文案"请求参数无效"，与既有未知键行为一致）。
	const unrecognizedBoundary = "请求参数无效"
	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"缺 bindMode", `{}`, "请选择会话绑定方式"},
		{"bindMode 空白", `{"bindMode":"  "}`, "请选择会话绑定方式"},
		{"bindMode 非法值", `{"bindMode":"pool"}`, "会话绑定方式无效"},
		{"api_key 缺 apiKeyId", `{"bindMode":"api_key"}`, "请选择会话绑定的 API Key"},
		{"group 缺 groupId", `{"bindMode":"group"}`, "请选择会话绑定的分组"},
		{"account 缺 accountId", `{"bindMode":"account"}`, "请选择会话绑定的账户"},
		{"api_key 带 groupId", `{"bindMode":"api_key","apiKeyId":"chat_key_provisioned","groupId":"group-a"}`, unrecognizedBoundary},
		{"group 带 apiKeyId", `{"bindMode":"group","groupId":"group-a","apiKeyId":"chat_key_provisioned"}`, unrecognizedBoundary},
		{"account 带 groupId", `{"bindMode":"account","accountId":"account-1","groupId":"group-a"}`, unrecognizedBoundary},
	}
	for _, item := range cases {
		response := env.do("POST", prefix+"/conversations", routeTestOwner, item.body)
		if response.status != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
		}
		if got := response.message(); got != item.message {
			t.Fatalf("%s message = %q, want %q", item.name, got, item.message)
		}
	}

	// 用户引用对象缺失/停用 → 400 chat_invalid_request（可恢复输入错误，
	// 不再按服务端故障 500）。
	failures := []struct {
		name string
		body string
	}{
		{"api_key 他人 Key", `{"bindMode":"api_key","apiKeyId":"other_key"}`},
		{"api_key 不存在的 Key", `{"bindMode":"api_key","apiKeyId":"missing_key"}`},
		{"group 分组不存在", `{"bindMode":"group","groupId":"missing"}`},
		{"group 分组已停用", `{"bindMode":"group","groupId":"group-disabled"}`},
		{"account 账户不存在", `{"bindMode":"account","accountId":"missing"}`},
		{"account 账户已停用", `{"bindMode":"account","accountId":"account-disabled"}`},
	}
	for _, item := range failures {
		response := env.do("POST", prefix+"/conversations", routeTestOwner, item.body)
		if response.status != http.StatusBadRequest || response.code() != "chat_invalid_request" {
			t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
		}
	}
}

func TestBindModeModelListSources(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"

	t.Run("api_key 按策略分组聚合 openai", func(t *testing.T) {
		env := newGenerationEnv(t)
		catalog := &bindModeCatalog{}
		env.deps.ModelCatalog = catalog
		createBoundConversation(t, env.fixture, "bind_conv_key", routeTestOwner, CreateConversationInput{BindMode: BindModeAPIKey})
		list := env.do("GET", prefix+"/conversations/bind_conv_key/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("list = %d %s", list.status, list.rawString())
		}
		groups, providers := catalog.snapshot()
		if len(groups) == 0 || groups[0] != "group-a" {
			t.Fatalf("group calls = %v", groups)
		}
		if len(providers) != 1 || providers[0] != "openai" {
			t.Fatalf("provider calls = %v", providers)
		}
		models := modelIDsFromList(t, list)
		if len(models) != 2 || models["gpt-5"] == false || models["gpt-5-mini"] == false {
			t.Fatalf("models = %v", list.dataArray())
		}
	})

	t.Run("group 只取绑定分组", func(t *testing.T) {
		env := newGenerationEnv(t)
		catalog := &bindModeCatalog{}
		env.deps.ModelCatalog = catalog
		env.deps.GroupLookup = mockGroupLookup{}
		createBoundConversation(t, env.fixture, "bind_conv_group", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-b", BindGroupNameSnapshot: "分组 B",
		})
		list := env.do("GET", prefix+"/conversations/bind_conv_group/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("list = %d %s", list.status, list.rawString())
		}
		groups, providers := catalog.snapshot()
		if len(groups) != 1 || groups[0] != "group-b" {
			t.Fatalf("group calls = %v, want [group-b]", groups)
		}
		if len(providers) != 1 || providers[0] != "anthropic" {
			t.Fatalf("provider calls = %v, want [anthropic]", providers)
		}
		models := modelIDsFromList(t, list)
		if len(models) != 1 || !models["claude-x"] {
			t.Fatalf("models = %v", list.dataArray())
		}
	})

	t.Run("account 单 provider 与 supported_models 交集", func(t *testing.T) {
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
			BindMode: BindModeAccount, BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
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

	t.Run("account 不在快照中为空作用域", func(t *testing.T) {
		env := newGenerationEnv(t)
		catalog := &accountViewCatalog{views: map[string]ChatTransportAccount{
			"group-a": {ID: "account-9", Type: "api_key", ProviderCode: "openai", SupportedEndpointModes: []string{"chat_sse"}},
		}}
		env.deps.ModelCatalog = catalog
		env.deps.AccountLookup = mockAccountLookup{}
		createBoundConversation(t, env.fixture, "bind_conv_absent", routeTestOwner, CreateConversationInput{
			BindMode: BindModeAccount, BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		list := env.do("GET", prefix+"/conversations/bind_conv_absent/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("list = %d %s", list.status, list.rawString())
		}
		if models := list.dataArray(); len(models) != 0 {
			t.Fatalf("空作用域 models = %v", models)
		}
	})

	t.Run("group 绑定对象停用后列表拒绝", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.ModelCatalog = &bindModeCatalog{}
		env.deps.GroupLookup = mockGroupLookup{}
		createBoundConversation(t, env.fixture, "bind_conv_disabled", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-disabled", BindGroupNameSnapshot: "停用分组",
		})
		list := env.do("GET", prefix+"/conversations/bind_conv_disabled/models", routeTestOwner, "")
		if list.status != http.StatusBadRequest || list.code() != "chat_invalid_request" {
			t.Fatalf("停用分组列表 = %d %s", list.status, list.rawString())
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
