package chat

// AI 问答三种绑定模式的发送链覆盖（设计 §6）：发送前置校验按绑定作用域收敛
// （resolveChatBindingScope）、协议选择按 scope 账户视图、调度覆盖目标绑定
// 到执行器视图（chatDispatchTargetAware 端口，cmd 组合根实现）。测试风格与
// chat_bind_modes_test.go / generation_test.go 一致（严格 mock 闭包 + 录制
// 断言）。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// targetAwareExecutor 记录 WithChatDispatchTarget 调用并透传 Dispatch，模拟
// 组合根 chatGatewayExecutor 的覆盖端口实现。
type targetAwareExecutor struct {
	inner *mockExecutor

	mu        sync.Mutex
	withCalls int
	mode      string
	groupID   string
	accountID string
}

func (e *targetAwareExecutor) WithChatDispatchTarget(bindMode, groupID, accountID string) GenerationExecutor {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.withCalls++
	e.mode, e.groupID, e.accountID = bindMode, groupID, accountID
	return e
}

func (e *targetAwareExecutor) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	return e.inner.Dispatch(ctx, req)
}

func (e *targetAwareExecutor) snapshot() (withCalls int, mode, groupID, accountID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.withCalls, e.mode, e.groupID, e.accountID
}

// dispatchPaths 返回已发生的派发路径序列。
func dispatchPaths(executor *mockExecutor) []string {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	paths := make([]string, 0, len(executor.calls))
	for _, call := range executor.calls {
		paths = append(paths, call.Path)
	}
	return paths
}

// inactiveChatKeys 让专用 Key 停用（group/account 模式鉴权主体失效语义）。
type inactiveChatKeys struct {
	mockChatKeys
}

func (m *inactiveChatKeys) FindChatAPIKey(keyID, ownerID string) (*ChatAPIKeyRecord, error) {
	return &ChatAPIKeyRecord{ID: keyID, Name: "对话密钥", Secret: "chat-secret", Status: "inactive"}, nil
}

// scriptChatCompletions 装配一条 chat_completions 成功脚本。
func scriptChatCompletions(env *generationEnv) {
	env.executor.steps = []scriptStep{{
		match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("绑定回复", true))
		},
	}}
}

func TestStreamBindModeGroupScope(t *testing.T) {
	env := newGenerationEnv(t)
	aware := &targetAwareExecutor{inner: env.executor}
	env.deps.Executor = aware
	env.deps.GroupLookup = mockGroupLookup{}
	catalog := &bindModeCatalog{}
	env.deps.ModelCatalog = catalog
	createBoundConversation(t, env.fixture, "bind_stream_group", routeTestOwner, CreateConversationInput{
		BindMode: BindModeGroup, BindGroupID: "group-b", BindGroupNameSnapshot: "分组 B",
	})
	scriptChatCompletions(env)
	response := env.streamPost("bind_stream_group", routeTestOwner, streamPayload("cmid-group", "问题", "claude-x"))
	if response.status != http.StatusOK {
		t.Fatalf("group stream = %d %s", response.status, response.rawString())
	}
	// 模型目录/账户候选只取绑定分组（group-b → anthropic），不读 Key 视图分组。
	groups, providers := catalog.snapshot()
	if len(groups) == 0 || groups[0] != "group-b" {
		t.Fatalf("scope group calls = %v, want [group-b ...]", groups)
	}
	if len(providers) == 0 || providers[0] != "anthropic" {
		t.Fatalf("scope provider calls = %v, want [anthropic ...]", providers)
	}
	// 调度覆盖目标绑定到执行器视图（进程内通道；cmd 侧注入 context）。
	withCalls, mode, groupID, accountID := aware.snapshot()
	if withCalls != 1 || mode != "group" || groupID != "group-b" || accountID != "" {
		t.Fatalf("dispatch target = calls:%d %s/%s/%s", withCalls, mode, groupID, accountID)
	}
	if paths := dispatchPaths(env.executor); len(paths) == 0 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("dispatch paths = %v", paths)
	}
}

func TestStreamBindModeAccountScopeAndProtocol(t *testing.T) {
	env := newGenerationEnv(t)
	aware := &targetAwareExecutor{inner: env.executor}
	env.deps.Executor = aware
	// 账户启用的分组是 group-b（与 Key 视图分组 group-a 不同）：作用域证据 =
	// 目录与账户候选只来自 group-b 的快照收敛。
	env.deps.AccountLookup = mockAccountLookup{groupsOfAccount: map[string][]string{"account-1": {"group-b"}}}
	catalog := &accountViewCatalog{views: map[string]ChatTransportAccount{
		"group-b": {
			ID: "account-1", Type: "api_key", ProviderCode: "openai",
			SupportedEndpointModes: []string{"chat_sse"},
			SupportedModels:        []string{"gpt-5"},
		},
	}}
	env.deps.ModelCatalog = catalog
	createBoundConversation(t, env.fixture, "bind_stream_account", routeTestOwner, CreateConversationInput{
		BindMode: BindModeAccount, BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
	})
	scriptChatCompletions(env)
	response := env.streamPost("bind_stream_account", routeTestOwner, streamPayload("cmid-account", "问题", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("account stream = %d %s", response.status, response.rawString())
	}
	groups, providers := catalog.snapshot()
	if len(groups) != 1 || groups[0] != "group-b" {
		t.Fatalf("account scope group calls = %v, want [group-b]", groups)
	}
	if len(providers) == 0 || providers[0] != "openai" {
		t.Fatalf("account scope provider calls = %v, want [openai ...]", providers)
	}
	// 协议选择按 scope 单账户端点视图：gpt-5 目录声明双协议，但该账户只有
	// chat_sse → 派发走 chat_completions。
	if paths := dispatchPaths(env.executor); len(paths) == 0 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("protocol dispatch paths = %v, want /v1/chat/completions", paths)
	}
	withCalls, mode, groupID, accountID := aware.snapshot()
	// 生产形状：account 会话不持久化承载分组，target 恒为 {account, GroupID:"",
	// AccountID}；承载分组由 /v1 消费侧（cmd resolveChatDispatchTargetScope）
	// 按账户启用分组解析，不在 chat 侧构造。
	if withCalls != 1 || mode != "account" || groupID != "" || accountID != "account-1" {
		t.Fatalf("account dispatch target = calls:%d %s/%s/%s", withCalls, mode, groupID, accountID)
	}
}

func TestStreamBindModeValidationFailures(t *testing.T) {
	prefix := "bind_stream_fail"

	t.Run("group 分组已停用", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.GroupLookup = mockGroupLookup{}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-gdisabled", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-disabled", BindGroupNameSnapshot: "停用分组",
		})
		response := env.streamPost(prefix+"-gdisabled", routeTestOwner, streamPayload("cmid-gd", "问题", "claude-x"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "会话绑定的分组已停用") {
			t.Fatalf("停用分组发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("停用分组不得派发上游")
		}
	})

	t.Run("account 账户已停用", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-adisabled", routeTestOwner, CreateConversationInput{
			BindMode: BindModeAccount, BindAccountID: "account-disabled", BindAccountNameSnapshot: "停用账户",
		})
		response := env.streamPost(prefix+"-adisabled", routeTestOwner, streamPayload("cmid-ad", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "会话绑定的账户已停用") {
			t.Fatalf("停用账户发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("停用账户不得派发上游")
		}
	})

	t.Run("api_key 模式 Key 不存在", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.ChatKeys = &owningChatKeys{}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-keymissing", routeTestOwner, CreateConversationInput{
			BindMode: BindModeAPIKey,
		})
		// 覆写鉴权 Key 为不存在的 Key（createBoundConversation 固定 chat_key_1）。
		if _, err := env.fixture.store.CreateConversation(CreateConversationInput{
			ID: prefix + "-keymissing2", SystemAccountID: routeTestOwner,
			APIKeyID: "missing_key", APIKeyNameSnapshot: "他人 Key",
			BindMode:                BindModeAPIKey,
			Now:                     env.fixture.nowISO,
			MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		response := env.streamPost(prefix+"-keymissing2", routeTestOwner, streamPayload("cmid-km", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "API Key 不存在或不可用") {
			t.Fatalf("Key 不存在发送 = %d %s", response.status, response.rawString())
		}
	})

	t.Run("group 模式专用 Key 停用后不能发送", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.ChatKeys = &inactiveChatKeys{}
		env.deps.GroupLookup = mockGroupLookup{}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-gkeydead", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-a", BindGroupNameSnapshot: "分组 A",
		})
		response := env.streamPost(prefix+"-gkeydead", routeTestOwner, streamPayload("cmid-gk", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "API Key 不存在或不可用") {
			t.Fatalf("专用 Key 停用发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("Key 失效不得派发上游")
		}
	})

	t.Run("执行器未实现覆盖端口时 group 会话照常发送", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.GroupLookup = mockGroupLookup{}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-plain", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-a", BindGroupNameSnapshot: "分组 A",
		})
		scriptChatCompletions(env)
		response := env.streamPost(prefix+"-plain", routeTestOwner, streamPayload("cmid-plain", "问题", "gpt-5"))
		if response.status != http.StatusOK {
			t.Fatalf("plain executor group stream = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() == 0 {
			t.Fatalf("group 会话必须照常派发")
		}
	})
}

// TestCompactionServiceBindsConversationDispatchTarget：压缩摘要的内部二次
// 调用统一（设计 §6）——group/account 会话经 CompactionService 解析会话绑定
// 目标并绑定到执行器视图；api_key/legacy 返回原执行器；会话缺失/读失败
// fail-closed 返回跳过错误，不回落未 pin 执行器。
func TestCompactionServiceBindsConversationDispatchTarget(t *testing.T) {
	aware := &targetAwareExecutor{inner: &mockExecutor{}}

	t.Run("group 会话绑定目标", func(t *testing.T) {
		fixture := newChatFixture(t)
		aware.mu.Lock()
		aware.withCalls = 0
		aware.mu.Unlock()
		if _, err := fixture.store.CreateConversation(CreateConversationInput{
			ID: "comp_conv_group", SystemAccountID: routeTestOwner, APIKeyID: "chat_key_1",
			BindMode: BindModeGroup, BindGroupID: "group-b", BindGroupNameSnapshot: "分组 B",
			Now: fixture.nowISO, MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_conv_group", SystemAccountID: routeTestOwner})
		if executorErr != nil || executor != GenerationExecutor(aware) {
			t.Fatalf("group 会话压缩必须绑定到覆盖执行器视图: %v", executorErr)
		}
		if withCalls, mode, groupID, accountID := aware.snapshot(); withCalls != 1 || mode != "group" || groupID != "group-b" || accountID != "" {
			t.Fatalf("compaction target = calls:%d %s/%s/%s", withCalls, mode, groupID, accountID)
		}
	})

	t.Run("api_key 会话不触碰端口", func(t *testing.T) {
		fixture := newChatFixture(t)
		aware.mu.Lock()
		aware.withCalls = 0
		aware.mu.Unlock()
		if _, err := fixture.store.CreateConversation(CreateConversationInput{
			ID: "comp_conv_key", SystemAccountID: routeTestOwner, APIKeyID: "chat_key_1",
			BindMode: BindModeAPIKey,
			Now:      fixture.nowISO, MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_conv_key", SystemAccountID: routeTestOwner})
		if executorErr != nil || executor != GenerationExecutor(aware) {
			t.Fatalf("api_key 会话压缩必须返回原执行器: %v", executorErr)
		}
		if withCalls, _, _, _ := aware.snapshot(); withCalls != 0 {
			t.Fatalf("api_key 会话不得触碰覆盖端口, calls=%d", withCalls)
		}
	})

	t.Run("account 会话 GroupID 为空是生产形状", func(t *testing.T) {
		fixture := newChatFixture(t)
		aware.mu.Lock()
		aware.withCalls = 0
		aware.mu.Unlock()
		if _, err := fixture.store.CreateConversation(CreateConversationInput{
			ID: "comp_conv_account", SystemAccountID: routeTestOwner, APIKeyID: "chat_key_1",
			BindMode: BindModeAccount, BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
			Now: fixture.nowISO, MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_conv_account", SystemAccountID: routeTestOwner})
		if executorErr != nil || executor != GenerationExecutor(aware) {
			t.Fatalf("account 会话压缩必须绑定到覆盖执行器视图: %v", executorErr)
		}
		// 生产形状：bind_group_id 恒空，承载分组由 /v1 消费侧解析。
		if withCalls, mode, groupID, accountID := aware.snapshot(); withCalls != 1 || mode != "account" || groupID != "" || accountID != "account-1" {
			t.Fatalf("compaction account target = calls:%d %s/%s/%s", withCalls, mode, groupID, accountID)
		}
	})

	t.Run("会话缺失返回跳过错误", func(t *testing.T) {
		fixture := newChatFixture(t)
		aware.mu.Lock()
		aware.withCalls = 0
		aware.mu.Unlock()
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_missing", SystemAccountID: routeTestOwner})
		if executorErr == nil || executorErr.Error() != "chat_context_target_unavailable" || executor != nil {
			t.Fatalf("会话缺失必须 fail-closed 返回跳过错误: executor=%v err=%v", executor, executorErr)
		}
		if withCalls, _, _, _ := aware.snapshot(); withCalls != 0 {
			t.Fatalf("会话缺失不得触碰覆盖端口, calls=%d", withCalls)
		}
	})

	t.Run("会话读失败返回跳过错误", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW13B(t)
		aware := &targetAwareExecutor{inner: &mockExecutor{}}
		// GetConversation 查询故障（conversationColumns 特有列）。
		script.failOnce("api_key_name_snapshot")
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_readfail", SystemAccountID: routeTestOwner})
		if executorErr == nil || executorErr.Error() != "chat_context_target_unavailable" || executor != nil {
			t.Fatalf("会话读失败必须 fail-closed 返回跳过错误: executor=%v err=%v", executor, executorErr)
		}
	})
}

// TestCompactionSummarizePageSkipsDispatchWhenTargetUnavailable：绑定会话读
// 失败/缺失时压缩总结 fail-closed——不派发上游（内部二次调用不得逃逸绑定
// 作用域），返回 chat_context_target_unavailable 走既有压缩失败路径。
func TestCompactionSummarizePageSkipsDispatchWhenTargetUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		seeds  func(t *testing.T, fixture *chatFixture, script *faultScriptW10D)
		convID string
	}{
		{name: "会话缺失", seeds: func(t *testing.T, fixture *chatFixture, script *faultScriptW10D) {}, convID: "comp_missing"},
		{name: "会话读失败", seeds: func(t *testing.T, fixture *chatFixture, script *faultScriptW10D) {
			script.failOnce("api_key_name_snapshot")
		}, convID: "comp_readfail"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, script := newFaultChatFixtureW13B(t)
			testCase.seeds(t, fixture, script)
			aware := &targetAwareExecutor{inner: &mockExecutor{}}
			service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
			_, err := service.summarizePage(context.Background(), CompactionInput{
				ConversationID: testCase.convID, SystemAccountID: routeTestOwner,
				Model: "m", Protocol: ProtocolChatCompletions,
			}, emptySnapshot(), []any{map[string]any{"role": "user", "content": "内容"}})
			if err == nil || err.Error() != "chat_context_target_unavailable" {
				t.Fatalf("读失败/缺失必须返回跳过错误: %v", err)
			}
			if aware.inner.callCount() != 0 {
				t.Fatalf("读失败/缺失不得派发上游: calls=%d", aware.inner.callCount())
			}
		})
	}
}

// TestStreamBindModeLegacyConversationUnchanged 钉住存量会话（bind_mode 为空
// = api_key 语义）发送行为不变：走 Key 视图分组，不产生覆盖目标。
func TestStreamBindModeLegacyConversationUnchanged(t *testing.T) {
	env := newGenerationEnv(t)
	aware := &targetAwareExecutor{inner: env.executor}
	env.deps.Executor = aware
	env.fixture.createConversation("legacy_stream_conv", routeTestOwner)
	scriptChatCompletions(env)
	response := env.streamPost("legacy_stream_conv", routeTestOwner, streamPayload("cmid-legacy", "问题", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("legacy stream = %d %s", response.status, response.rawString())
	}
	if withCalls, _, _, _ := aware.snapshot(); withCalls != 0 {
		t.Fatalf("legacy 会话不得触碰覆盖端口，calls=%d", withCalls)
	}
	if !strings.Contains(response.rawString(), "message.completed") {
		t.Fatalf("legacy stream 未完成: %s", response.rawString())
	}
	_ = context.Background
}
