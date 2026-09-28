package main

// BUG-0175 D-201 composition-root regression: the ToolCapabilitiesResolver
// (chat.routes.ts:1548-1626 loadChatConversationToolCapabilities) availability
// matrix, reason matrix and response shape over mock ports, plus the wiring
// source assertion (装配断线零容忍先例).

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// --- mock ports（Mock 优先规范：可回放、结果稳定）---

type toolCapsChatKeys struct {
	record *chat.ChatAPIKeyRecord
	err    error
}

func (f toolCapsChatKeys) EnsureChatAPIKey(string) (string, error) {
	return "", errors.New("not wired")
}
func (f toolCapsChatKeys) FindChatAPIKey(string, string) (*chat.ChatAPIKeyRecord, error) {
	return f.record, f.err
}

type toolCapsGatewayKeys struct {
	view *chat.GatewayKeyView
	err  error
}

func (f toolCapsGatewayKeys) ValidateGatewayKey(string) (*chat.GatewayKeyView, error) {
	return f.view, f.err
}

type toolCapsCatalog struct {
	accountsByGroup map[string][]chat.ChatTransportAccount
	catalogByCode   map[string][]chat.ProviderModelCatalogItem
}

func (f toolCapsCatalog) ListAccountsForGroup(groupID, _, requestedModel, endpointFamily string) []chat.ChatTransportAccount {
	return f.accountsByGroup[groupID+"|"+requestedModel+"|"+endpointFamily]
}

func (f toolCapsCatalog) ListProviderCatalog(providerCode, _ string) []chat.ProviderModelCatalogItem {
	return f.catalogByCode[providerCode]
}

func toolCapsDeps(keys chat.ChatAPIKeyProvider, gateway chat.GatewayKeyValidator, catalog chat.ModelCatalog) *chat.Deps {
	return &chat.Deps{ChatKeys: keys, GatewayKeys: gateway, ModelCatalog: catalog}
}

func toolCapsConversation(model string) *chat.Conversation {
	conversation := &chat.Conversation{}
	value := model
	conversation.LastModel = &value
	keyID := "key-1"
	conversation.APIKeyID = &keyID
	return conversation
}

func toolCapsTools(t *testing.T, payload any) map[string]map[string]any {
	t.Helper()
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload shape: %T", payload)
	}
	rawTools, ok := payloadMap["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools shape: %T", payloadMap["tools"])
	}
	tools := map[string]map[string]any{}
	for _, tool := range rawTools {
		id, _ := tool["id"].(string)
		tools[id] = tool
	}
	return tools
}

func expectTool(t *testing.T, payload any, id string, available bool, reason string) {
	t.Helper()
	tools := toolCapsTools(t, payload)
	tool, ok := tools[id]
	if !ok {
		t.Fatalf("tool %q missing in %v", id, tools)
	}
	if tool["available"] != available {
		t.Fatalf("tool %q available = %v, want %v (%v)", id, tool["available"], available, tool)
	}
	if reason == "" {
		if _, has := tool["reason"]; has {
			t.Fatalf("tool %q must omit reason when available: %v", id, tool)
		}
		return
	}
	if tool["reason"] != reason {
		t.Fatalf("tool %q reason = %v, want %q", id, tool["reason"], reason)
	}
}

// account builds a transport account: supported models + one endpoint mode
// (chat_sse / responses_sse), optionally carrying a mapping.
func toolCapsAccount(id, accountType string, modes ...string) chat.ChatTransportAccount {
	return chat.ChatTransportAccount{
		ID:                     id,
		Type:                   accountType,
		ProviderCode:           "gpt",
		SupportedModels:        []string{"gpt-5.3", "gpt-image-2"},
		SupportedEndpointModes: modes,
	}
}

func toolCapsCatalogItem(model string, tools ...string) chat.ProviderModelCatalogItem {
	return chat.ProviderModelCatalogItem{Model: model, ProviderCode: "gpt", SupportedTools: tools}
}

// catalogFixture: gpt provider; chat group carries a chat_sse account,
// responses group a responses_sse account; image group an api_key account for
// gpt-image-2. Fake keys are groupID|requestedModel|endpointFamily.
func toolCapsFixture() (chat.ModelCatalog, *chat.GatewayKeyView) {
	accounts := map[string][]chat.ChatTransportAccount{
		"grp-chat|gpt-5.3|":                 {toolCapsAccount("acct-chat", "api_key", "chat_sse")},
		"grp-chat|gpt-5.3|chat_completions": {toolCapsAccount("acct-chat", "api_key", "chat_sse")},
		"grp-responses|gpt-5.3|responses":   {toolCapsAccount("acct-responses", "oauth", "responses_sse")},
		"grp-image|gpt-image-2|":            {toolCapsAccount("acct-image", "api_key", "chat_sse")},
	}
	catalog := map[string][]chat.ProviderModelCatalogItem{
		"gpt": {
			toolCapsCatalogItem("gpt-5.3", "web_search", "function_calling"),
			toolCapsCatalogItem("gpt-image-2"),
		},
	}
	return toolCapsCatalog{accountsByGroup: accounts, catalogByCode: catalog},
		&chat.GatewayKeyView{
			GroupBindings: []chat.GatewayGroupBinding{
				{GroupID: "grp-chat", Status: "active", GroupEnabled: true},
				{GroupID: "grp-responses", Status: "active", GroupEnabled: true},
				{GroupID: "grp-image", Status: "active", GroupEnabled: true},
			},
			ImageGenerationEnabled: true,
		}
}

// toolCapsBoundDeps 构建会话绑定账户口径的 deps：绑定账户 acct-bound（启用
// 分组 grp-bound），收敛路径按 requestedModel="" 查询（目录快照键
// grp-bound||）。account 零值时默认 api_key 双协议账户（api_key 类型账户同时
// 构成生图路由）。
func toolCapsBoundDeps(view *chat.GatewayKeyView, catalog toolCapsCatalog, account chat.ChatTransportAccount) *chat.Deps {
	if account.ID == "" {
		account = toolCapsAccount("acct-bound", "api_key", "chat_sse", "responses_sse")
	}
	catalog.accountsByGroup["grp-bound||"] = []chat.ChatTransportAccount{account}
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{
		ID: account.ID, Name: "账户", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp-bound"},
	}}
	return deps
}

func toolCapsBoundConversation(model string) *chat.Conversation {
	conversation := toolCapsConversation(model)
	accountID := "acct-bound"
	conversation.BindAccountID = &accountID
	return conversation
}

// TestChatToolCapabilitiesFullAvailabilityMatrix: both tools available when
// the model supports web_search + function_calling and the bound account
// carries a responses route; reasons stay omitted (Node conditional spread).
func TestChatToolCapabilitiesFullAvailabilityMatrix(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	deps := toolCapsBoundDeps(view, rawCatalog.(toolCapsCatalog), chat.ChatTransportAccount{})
	payload := resolveChatToolCapabilities(deps, toolCapsBoundConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", true, "")
	expectTool(t, payload, "generate_image", true, "")
	payloadMap := payload.(map[string]any)
	if payloadMap["model"] != "gpt-5.3" {
		t.Fatalf("model = %v, want gpt-5.3", payloadMap["model"])
	}
}

// TestChatToolCapabilitiesChatCompletionsOnlyRoute: web_search supported by
// the model but the bound account only carries chat_completions.
func TestChatToolCapabilitiesChatCompletionsOnlyRoute(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	deps := toolCapsBoundDeps(view, rawCatalog.(toolCapsCatalog), toolCapsAccount("acct-bound", "api_key", "chat_sse"))
	payload := resolveChatToolCapabilities(deps, toolCapsBoundConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前路由不支持 Responses 网页搜索")
	// 图片生成不要求 responses 路由：权限开 + function_calling + api_key 账户路由在。
	expectTool(t, payload, "generate_image", true, "")
}

// TestChatToolCapabilitiesNoRouteReason: bound account resolves to an empty
// scope (no snapshot row) — no route at all.
func TestChatToolCapabilitiesNoRouteReason(t *testing.T) {
	_, view := toolCapsFixture()
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		toolCapsCatalog{catalogByCode: map[string][]chat.ProviderModelCatalogItem{}})
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{
		ID: "acct-bound", Name: "账户", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp-bound"},
	}}
	payload := resolveChatToolCapabilities(deps, toolCapsBoundConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前 API Key 没有可用的对话路由")
	expectTool(t, payload, "generate_image", false, "当前 API Key 没有可用的对话路由")
}

// TestChatToolCapabilitiesModelWithoutWebSearch: intersection loses
// web_search when any catalog row of the model drops it.
func TestChatToolCapabilitiesModelWithoutWebSearch(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	catalog := rawCatalog.(toolCapsCatalog)
	catalog.catalogByCode["gpt"] = []chat.ProviderModelCatalogItem{
		toolCapsCatalogItem("gpt-5.3", "web_search", "function_calling"),
		toolCapsCatalogItem("gpt-5.3", "function_calling"), // second row drops web_search
		toolCapsCatalogItem("gpt-image-2"),
	}
	deps := toolCapsBoundDeps(view, catalog, chat.ChatTransportAccount{})
	payload := resolveChatToolCapabilities(deps, toolCapsBoundConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前模型不支持网页搜索")
	expectTool(t, payload, "generate_image", true, "")
}

// TestChatToolCapabilitiesImageReasonMatrix covers the three image failure
// reasons in Node order.
func TestChatToolCapabilitiesImageReasonMatrix(t *testing.T) {
	build := func(mutate func(view *chat.GatewayKeyView, catalog toolCapsCatalog, account *chat.ChatTransportAccount)) any {
		rawCatalog, view := toolCapsFixture()
		catalog := rawCatalog.(toolCapsCatalog)
		account := chat.ChatTransportAccount{}
		mutate(view, catalog, &account)
		deps := toolCapsBoundDeps(view, catalog, account)
		return resolveChatToolCapabilities(deps, toolCapsBoundConversation("gpt-5.3"), "owner-1")
	}
	expectTool(t, build(func(view *chat.GatewayKeyView, _ toolCapsCatalog, _ *chat.ChatTransportAccount) {
		view.ImageGenerationEnabled = false
	}), "generate_image", false, "当前用户未开启图片生成")
	expectTool(t, build(func(_ *chat.GatewayKeyView, catalog toolCapsCatalog, _ *chat.ChatTransportAccount) {
		catalog.catalogByCode["gpt"] = []chat.ProviderModelCatalogItem{toolCapsCatalogItem("gpt-5.3", "web_search")}
	}), "generate_image", false, "当前模型不支持函数工具调用")
	expectTool(t, build(func(_ *chat.GatewayKeyView, _ toolCapsCatalog, account *chat.ChatTransportAccount) {
		*account = toolCapsAccount("acct-bound", "oauth", "chat_sse")
	}), "generate_image", false, "当前 API Key 路由没有可用的图像生成 API Key 账户")
}

// TestChatToolCapabilitiesUnavailableFallbacks: no model, missing key row,
// gateway miss and the catch branch.
func TestChatToolCapabilitiesUnavailableFallbacks(t *testing.T) {
	catalog, view := toolCapsFixture()
	keys := toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}}
	gateway := toolCapsGatewayKeys{view: view}
	deps := toolCapsDeps(keys, gateway, catalog)

	// 无模型。
	payload := resolveChatToolCapabilities(deps, toolCapsConversation(""), "owner-1")
	expectTool(t, payload, "web_search", false, "当前会话尚未选择对话模型")
	expectTool(t, payload, "generate_image", false, "当前会话尚未选择对话模型")

	// gateway 校验未命中（key 无效）→「会话绑定的 API Key 不可用」。
	nilViewDeps := toolCapsDeps(keys, toolCapsGatewayKeys{view: nil}, catalog)
	payload = resolveChatToolCapabilities(nilViewDeps, toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, "会话绑定的 API Key 不可用")

	// catch 分支：key 行缺失 / 校验报错 / key 停用 →「工具能力状态暂时无法读取」。
	payload = resolveChatToolCapabilities(toolCapsDeps(toolCapsChatKeys{record: nil}, gateway, catalog), toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, chatToolCapabilitiesCatchReason)
	payload = resolveChatToolCapabilities(toolCapsDeps(toolCapsChatKeys{err: errors.New("db down")}, gateway, catalog), toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, chatToolCapabilitiesCatchReason)
	payload = resolveChatToolCapabilities(toolCapsDeps(toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "disabled"}}, gateway, catalog), toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "generate_image", false, chatToolCapabilitiesCatchReason)
	payload = resolveChatToolCapabilities(toolCapsDeps(keys, toolCapsGatewayKeys{err: errors.New("cache down")}, catalog), toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "generate_image", false, chatToolCapabilitiesCatchReason)

	// 无绑定 API Key 的会话同样落入 catch 分支。
	conversation := toolCapsConversation("gpt-5.3")
	noKey := ""
	conversation.APIKeyID = &noKey
	payload = resolveChatToolCapabilities(deps, conversation, "owner-1")
	expectTool(t, payload, "web_search", false, chatToolCapabilitiesCatchReason)
}

// TestChatToolCapabilitiesWiredAtCompositionRoot pins the mount wiring line
// (装配断线零容忍先例：TestComposeSystemAPIWiresBalanceAndCatalogRefresh).
func TestChatToolCapabilitiesWiredAtCompositionRoot(t *testing.T) {
	source, err := os.ReadFile("chain_chat_mount.go")
	if err != nil {
		t.Fatal(err)
	}
	needle := "deps.ToolCapabilit = newChatToolCapabilitiesResolver(deps)"
	if !strings.Contains(string(source), needle) {
		t.Fatalf("chat mount must wire the tool capabilities resolver: %s", needle)
	}
	if registerPos := strings.Index(string(source), "deps.Register(composed.kernel"); registerPos >= 0 {
		if wirePos := strings.Index(string(source), needle); wirePos < 0 || wirePos > registerPos {
			t.Fatalf("tool capabilities resolver must be wired before deps.Register")
		}
	}
}

// --- 会话绑定账户感知（account 聚合口径与发送侧对齐）---

type toolCapsAccountLookup struct {
	ref *chat.ChatAccountRef
	err error
}

func (f toolCapsAccountLookup) FindChatAccount(chat.ChatBindScope, string) (*chat.ChatAccountRef, error) {
	return f.ref, f.err
}

func toolCapsAccountConversation(model, accountID string) *chat.Conversation {
	conversation := toolCapsConversation(model)
	conversation.BindAccountID = &accountID
	return conversation
}

// TestChatToolCapabilitiesAccountBindScope：收敛为绑定账户的运行时传输视图，
// 能力按该账户判定，与 Key 自身分组绑定解耦。
func TestChatToolCapabilitiesAccountBindScope(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	catalog := rawCatalog.(toolCapsCatalog)
	// 收敛路径按 requestedModel="" 查询：为 grp-responses / grp-chat 补空模型
	// 快照行。
	catalog.accountsByGroup["grp-responses||"] = []chat.ChatTransportAccount{toolCapsAccount("acct-responses", "oauth", "responses_sse")}
	catalog.accountsByGroup["grp-chat||"] = []chat.ChatTransportAccount{toolCapsAccount("acct-chat", "api_key", "chat_sse")}
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)

	// 绑定 Responses 账户：web_search 可用；oauth 账户无 api_key 生图路由。
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{
		ID: "acct-responses", Name: "账户", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp-responses"},
	}}
	payload := resolveChatToolCapabilities(deps, toolCapsAccountConversation("gpt-5.3", "acct-responses"), "owner-1")
	expectTool(t, payload, "web_search", true, "")
	expectTool(t, payload, "generate_image", false, "当前 API Key 路由没有可用的图像生成 API Key 账户")

	// 绑定仅 chat_sse 的账户：路由不支持 Responses 搜索。
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{
		ID: "acct-chat", Name: "账户", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp-chat"},
	}}
	payload = resolveChatToolCapabilities(deps, toolCapsAccountConversation("gpt-5.3", "acct-chat"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前路由不支持 Responses 网页搜索")

	// 账户不在任何启用分组快照：空作用域 → 无路由 reason。
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{
		ID: "acct-orphan", Name: "账户", ProviderCode: "gpt", Enabled: true,
	}}
	payload = resolveChatToolCapabilities(deps, toolCapsAccountConversation("gpt-5.3", "acct-orphan"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前 API Key 没有可用的对话路由")

	// 绑定账户停用/不存在沿发送侧 400 文案。
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{ID: "acct-responses", Enabled: false}}
	payload = resolveChatToolCapabilities(deps, toolCapsAccountConversation("gpt-5.3", "acct-responses"), "owner-1")
	expectTool(t, payload, "web_search", false, "会话绑定的账户已停用")
	deps.AccountLookup = toolCapsAccountLookup{}
	payload = resolveChatToolCapabilities(deps, toolCapsAccountConversation("gpt-5.3", "acct-x"), "owner-1")
	expectTool(t, payload, "web_search", false, "会话绑定的账户不存在或已删除")

	// 端口查询失败与未接线保持 catch 文案。
	deps.AccountLookup = toolCapsAccountLookup{err: errors.New("db down")}
	payload = resolveChatToolCapabilities(deps, toolCapsAccountConversation("gpt-5.3", "acct-responses"), "owner-1")
	expectTool(t, payload, "web_search", false, chatToolCapabilitiesCatchReason)
	payload = resolveChatToolCapabilities(toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog), toolCapsAccountConversation("gpt-5.3", "acct-responses"), "owner-1")
	expectTool(t, payload, "web_search", false, chatToolCapabilitiesCatchReason)
}

// TestChatToolCapabilitiesUnboundAndArchivedArms：未选账户与归档（存量旧模式）
// 会话的只读/引导文案臂。
func TestChatToolCapabilitiesUnboundAndArchivedArms(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	catalog := rawCatalog.(toolCapsCatalog)
	catalog.accountsByGroup["grp-responses||"] = []chat.ChatTransportAccount{toolCapsAccount("acct-responses", "oauth", "responses_sse")}
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)
	deps.AccountLookup = toolCapsAccountLookup{ref: &chat.ChatAccountRef{
		ID: "acct-responses", Name: "账户", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp-responses"},
	}}

	// 未选账户：引导先选账户。
	unbound := toolCapsConversation("gpt-5.3")
	payload := resolveChatToolCapabilities(deps, unbound, "owner-1")
	expectTool(t, payload, "web_search", false, "当前会话尚未选择 AI 账户")
	expectTool(t, payload, "generate_image", false, "当前会话尚未选择 AI 账户")

	// 归档会话（一次性迁移的存量旧模式行）：升级提示。
	archived := toolCapsAccountConversation("gpt-5.3", "acct-responses")
	archived.Archived = true
	payload = resolveChatToolCapabilities(deps, archived, "owner-1")
	expectTool(t, payload, "web_search", false, "该会话绑定方式已升级，请新建会话")
	expectTool(t, payload, "generate_image", false, "该会话绑定方式已升级，请新建会话")
}
