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

// TestChatToolCapabilitiesFullAvailabilityMatrix: both tools available when
// the model supports web_search + function_calling and a responses route plus
// an image route exist; reasons stay omitted (Node conditional spread).
func TestChatToolCapabilitiesFullAvailabilityMatrix(t *testing.T) {
	catalog, view := toolCapsFixture()
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)
	payload := resolveChatToolCapabilities(deps, toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", true, "")
	expectTool(t, payload, "generate_image", true, "")
	payloadMap := payload.(map[string]any)
	if payloadMap["model"] != "gpt-5.3" {
		t.Fatalf("model = %v, want gpt-5.3", payloadMap["model"])
	}
}

// TestChatToolCapabilitiesChatCompletionsOnlyRoute: web_search supported by
// the model but the resolved route is chat_completions.
func TestChatToolCapabilitiesChatCompletionsOnlyRoute(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	catalog := rawCatalog.(toolCapsCatalog)
	catalog.accountsByGroup["grp-responses|gpt-5.3|responses"] = nil
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)
	payload := resolveChatToolCapabilities(deps, toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前路由不支持 Responses 网页搜索")
	// 图片生成不要求 responses 路由：权限开 + function_calling + 图片路由在。
	expectTool(t, payload, "generate_image", true, "")
}

// TestChatToolCapabilitiesNoRouteReason: no group route at all.
func TestChatToolCapabilitiesNoRouteReason(t *testing.T) {
	rawCatalog, view := toolCapsFixture()
	catalog := rawCatalog.(toolCapsCatalog)
	catalog.accountsByGroup = map[string][]chat.ChatTransportAccount{}
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)
	payload := resolveChatToolCapabilities(deps, toolCapsConversation("gpt-5.3"), "owner-1")
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
	deps := toolCapsDeps(
		toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
		toolCapsGatewayKeys{view: view},
		catalog)
	payload := resolveChatToolCapabilities(deps, toolCapsConversation("gpt-5.3"), "owner-1")
	expectTool(t, payload, "web_search", false, "当前模型不支持网页搜索")
	expectTool(t, payload, "generate_image", true, "")
}

// TestChatToolCapabilitiesImageReasonMatrix covers the three image failure
// reasons in Node order.
func TestChatToolCapabilitiesImageReasonMatrix(t *testing.T) {
	build := func(mutate func(view *chat.GatewayKeyView, catalog toolCapsCatalog)) any {
		rawCatalog, view := toolCapsFixture()
		catalog := rawCatalog.(toolCapsCatalog)
		mutate(view, catalog)
		deps := toolCapsDeps(
			toolCapsChatKeys{record: &chat.ChatAPIKeyRecord{ID: "key-1", Secret: "sk-chat", Status: "active"}},
			toolCapsGatewayKeys{view: view},
			catalog)
		return resolveChatToolCapabilities(deps, toolCapsConversation("gpt-5.3"), "owner-1")
	}
	expectTool(t, build(func(view *chat.GatewayKeyView, _ toolCapsCatalog) {
		view.ImageGenerationEnabled = false
	}), "generate_image", false, "当前用户未开启图片生成")
	expectTool(t, build(func(_ *chat.GatewayKeyView, catalog toolCapsCatalog) {
		catalog.catalogByCode["gpt"] = []chat.ProviderModelCatalogItem{toolCapsCatalogItem("gpt-5.3", "web_search")}
	}), "generate_image", false, "当前模型不支持函数工具调用")
	expectTool(t, build(func(_ *chat.GatewayKeyView, catalog toolCapsCatalog) {
		catalog.accountsByGroup["grp-image|gpt-image-2|"] = []chat.ChatTransportAccount{toolCapsAccount("acct-image", "oauth", "chat_sse")}
	}), "generate_image", false, "当前 API Key 路由没有可用的 gpt-image-2 API Key 账户")
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
