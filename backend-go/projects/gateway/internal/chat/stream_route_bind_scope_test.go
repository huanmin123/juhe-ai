package chat

// AI 问答会话账户唯一绑定的发送链覆盖：发送前置校验（账户可用性）、协议选
// 择按 scope 账户视图、调度覆盖目标绑定到执行器视图（chatDispatchTargetAware
// 端口，cmd 组合根实现）。测试风格与 chat_bind_modes_test.go /
// generation_test.go 一致（严格 mock 闭包 + 录制断言）。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// targetAwareExecutor 记录 WithChatDispatchAccount 调用并透传 Dispatch，模拟
// 组合根 chatGatewayExecutor 的覆盖端口实现。
type targetAwareExecutor struct {
	inner *mockExecutor

	mu        sync.Mutex
	withCalls int
	accountID string
}

func (e *targetAwareExecutor) WithChatDispatchAccount(accountID string) GenerationExecutor {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.withCalls++
	e.accountID = accountID
	return e
}

func (e *targetAwareExecutor) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	return e.inner.Dispatch(ctx, req)
}

func (e *targetAwareExecutor) snapshot() (withCalls int, accountID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.withCalls, e.accountID
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

// inactiveChatKeys 让专用 Key 停用（鉴权主体失效语义）。
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

func TestStreamBindAccountScopeAndProtocol(t *testing.T) {
	env := newGenerationEnv(t)
	aware := &targetAwareExecutor{inner: env.executor}
	env.deps.Executor = aware
	// 账户启用的分组是 group-b：作用域证据 = 目录与账户候选只来自 group-b 的
	// 快照收敛（mockModelCatalog/mockGatewayKeys 的 Key 视图分组是 group-a，
	// 断言发送链不再读 Key 视图分组）。
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
		BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
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
	// 调度覆盖目标绑定到执行器视图（进程内通道；cmd 侧注入 context）。生产形
	// 状：target 只携带账户 ID，承载分组由 /v1 消费侧（cmd
	// resolveChatDispatchTargetScope）按账户启用分组解析，不在 chat 侧构造。
	if withCalls, accountID := aware.snapshot(); withCalls != 1 || accountID != "account-1" {
		t.Fatalf("account dispatch target = calls:%d %s", withCalls, accountID)
	}
}

func TestStreamBindValidationFailures(t *testing.T) {
	prefix := "bind_stream_fail"

	t.Run("绑定账户已停用", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		env.deps.ModelCatalog = mockModelCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-adisabled", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-disabled", BindAccountNameSnapshot: "停用账户",
		})
		response := env.streamPost(prefix+"-adisabled", routeTestOwner, streamPayload("cmid-ad", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "会话绑定的账户已停用") {
			t.Fatalf("停用账户发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("停用账户不得派发上游")
		}
	})

	t.Run("会话 Key 不存在", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		env.deps.ChatKeys = &owningChatKeys{}
		env.deps.ModelCatalog = mockModelCatalog{}
		// 绑定账户合法，但鉴权主体（会话 api_key_id）指向不存在的 Key。
		if _, err := env.fixture.store.CreateConversation(CreateConversationInput{
			ID: prefix + "-keymissing", SystemAccountID: routeTestOwner,
			APIKeyID: "missing_key", APIKeyNameSnapshot: "他人 Key",
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
			Now:                     env.fixture.nowISO,
			MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		response := env.streamPost(prefix+"-keymissing", routeTestOwner, streamPayload("cmid-km", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "API Key 不存在或不可用") {
			t.Fatalf("Key 不存在发送 = %d %s", response.status, response.rawString())
		}
	})

	t.Run("专用 Key 停用后不能发送", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		env.deps.ChatKeys = &inactiveChatKeys{}
		env.deps.ModelCatalog = mockModelCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-keydead", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		response := env.streamPost(prefix+"-keydead", routeTestOwner, streamPayload("cmid-gk", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || !strings.Contains(response.message(), "API Key 不存在或不可用") {
			t.Fatalf("专用 Key 停用发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("Key 失效不得派发上游")
		}
	})

	t.Run("执行器未实现覆盖端口时绑定会话照常发送", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = mockAccountLookup{}
		env.deps.ModelCatalog = mockModelCatalog{}
		createBoundConversation(t, env.fixture, prefix+"-plain", routeTestOwner, CreateConversationInput{
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		scriptChatCompletions(env)
		response := env.streamPost(prefix+"-plain", routeTestOwner, streamPayload("cmid-plain", "问题", "gpt-5"))
		if response.status != http.StatusOK {
			t.Fatalf("plain executor stream = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() == 0 {
			t.Fatalf("绑定会话必须照常派发")
		}
	})
}

// TestCompactionServiceBindsConversationDispatchTarget：压缩摘要的内部二次
// 调用统一——绑定账户会话经 CompactionService 解析会话绑定账户并绑定到执行
// 器视图；未绑定会话不触碰端口（发送预检已拦截，服务层兜底）；会话缺失/读
// 失败 fail-closed 返回跳过错误，不回落未 pin 执行器。
func TestCompactionServiceBindsConversationDispatchTarget(t *testing.T) {
	aware := &targetAwareExecutor{inner: &mockExecutor{}}

	t.Run("绑定账户会话绑定目标", func(t *testing.T) {
		fixture := newChatFixture(t)
		aware.mu.Lock()
		aware.withCalls = 0
		aware.mu.Unlock()
		if _, err := fixture.store.CreateConversation(CreateConversationInput{
			ID: "comp_conv_account", SystemAccountID: routeTestOwner, APIKeyID: "chat_key_1",
			BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
			Now: fixture.nowISO, MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_conv_account", SystemAccountID: routeTestOwner})
		if executorErr != nil || executor != GenerationExecutor(aware) {
			t.Fatalf("绑定会话压缩必须绑定到覆盖执行器视图: %v", executorErr)
		}
		if withCalls, accountID := aware.snapshot(); withCalls != 1 || accountID != "account-1" {
			t.Fatalf("compaction target = calls:%d %s", withCalls, accountID)
		}
	})

	t.Run("未绑定会话不触碰端口", func(t *testing.T) {
		fixture := newChatFixture(t)
		aware.mu.Lock()
		aware.withCalls = 0
		aware.mu.Unlock()
		if _, err := fixture.store.CreateConversation(CreateConversationInput{
			ID: "comp_conv_unbound", SystemAccountID: routeTestOwner, APIKeyID: "chat_key_1",
			Now: fixture.nowISO, MaxConversationsPerUser: 30,
		}); err != nil {
			t.Fatal(err)
		}
		service := NewCompactionService(fixture.store, aware, func(text string) int { return 1 }, func() string { return fixture.nowISO })
		executor, executorErr := service.dispatchExecutor(CompactionInput{ConversationID: "comp_conv_unbound", SystemAccountID: routeTestOwner})
		if executorErr != nil || executor != GenerationExecutor(aware) {
			t.Fatalf("未绑定会话压缩必须返回原执行器: %v", executorErr)
		}
		if withCalls, _ := aware.snapshot(); withCalls != 0 {
			t.Fatalf("未绑定会话不得触碰覆盖端口, calls=%d", withCalls)
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
		if withCalls, _ := aware.snapshot(); withCalls != 0 {
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
				Model: "m",
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

// TestStreamUnboundConversationRejected 钉住未选账户会话的发送行为：发送预检
// 400 chat_account_required，不产生覆盖目标、不派发上游。
func TestStreamUnboundConversationRejected(t *testing.T) {
	env := newGenerationEnv(t)
	aware := &targetAwareExecutor{inner: env.executor}
	env.deps.Executor = aware
	env.fixture.createConversation("unbound_stream_conv", routeTestOwner)
	// 夹具默认绑定 account-1；本用例钉未绑定行为，显式清空。
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET bind_account_id = NULL, bind_account_name_snapshot = '' WHERE id = 'unbound_stream_conv'`); err != nil {
		t.Fatal(err)
	}
	scriptChatCompletions(env)
	response := env.streamPost("unbound_stream_conv", routeTestOwner, streamPayload("cmid-unbound", "问题", "gpt-5"))
	if response.status != http.StatusBadRequest || response.code() != "chat_account_required" {
		t.Fatalf("unbound stream = %d %s", response.status, response.rawString())
	}
	if withCalls, _ := aware.snapshot(); withCalls != 0 {
		t.Fatalf("未绑定会话不得触碰覆盖端口，calls=%d", withCalls)
	}
	if env.executor.callCount() != 0 {
		t.Fatalf("未绑定会话不得派发上游")
	}
	_ = context.Background
}
