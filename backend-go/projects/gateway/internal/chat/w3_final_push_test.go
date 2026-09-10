package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 最后一批高价值分支：压缩总结链路（两种协议 + 失败路径）、runner 订阅者
// 裁剪与时间线补发、流式路由的 Hub 冲突/超大消息/替换链路、deps 参数守卫、
// ETag 匹配表与 Windows 锁重入。

// TestSummarizePageW3 直接驱动压缩总结的两种协议与失败路径。
func TestSummarizePageW3(t *testing.T) {
	_, clock := fixedChatClock()
	newService := func(executor GenerationExecutor) *CompactionService {
		return NewCompactionService(newChatFixture(t).store, executor, func(text string) int { return len(text) }, func() string { return isoMillis(clock()) })
	}
	input := CompactionInput{ConversationID: "c", SystemAccountID: "o", APIKeySecret: "k", Model: "gpt-5", Protocol: ProtocolChatCompletions}
	messages := []any{map[string]any{"role": "user", "content": "问题"}}

	t.Run("chat 协议成功", func(t *testing.T) {
		executor := mockExecutor{steps: []scriptStep{{
			match:   func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(200, `{"choices":[{"message":{"content":"{\"currentGoal\":\"目标\",\"recentUserIntent\":\"意图\",\"durableMemory\":[\"记忆\"]}"}}]}`) },
		}}}
		snapshot, err := newService(&executor).summarizePage(context.Background(), input, emptySnapshot(), messages)
		if err != nil || snapshot.CurrentGoal != "目标" || !equalStringsW3(snapshot.DurableMemory, []string{"记忆"}) {
			t.Fatalf("chat 总结失败: %+v err=%v", snapshot, err)
		}
	})
	t.Run("responses 协议成功", func(t *testing.T) {
		executor := mockExecutor{steps: []scriptStep{{
			match:   func(call dispatchCall) bool { return call.Path == "/v1/responses" },
			respond: func(dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(200, `{"output_text":"{\"currentGoal\":\"G\",\"recentUserIntent\":\"I\"}"}`) },
		}}}
		snapshot, err := newService(&executor).summarizePage(context.Background(), CompactionInput{
			ConversationID: "c", SystemAccountID: "o", APIKeySecret: "k", Model: "gpt-5", Protocol: ProtocolResponses,
		}, emptySnapshot(), messages)
		if err != nil || snapshot.CurrentGoal != "G" {
			t.Fatalf("responses 总结失败: %+v err=%v", snapshot, err)
		}
	})
	t.Run("失败路径", func(t *testing.T) {
		if _, err := newService(failingExecutorW3{}).summarizePage(context.Background(), input, emptySnapshot(), messages); err == nil {
			t.Fatalf("dispatch 失败应透传")
		}
		nilResponse := nilResponseExecutorW3{}
		if _, err := newService(nilResponse).summarizePage(context.Background(), input, emptySnapshot(), messages); err == nil || !strings.Contains(err.Error(), "dispatch_missing") {
			t.Fatalf("空响应应报错: %v", err)
		}
		badStatus := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(500, `{}`) }}}}
		if _, err := newService(&badStatus).summarizePage(context.Background(), input, emptySnapshot(), messages); err == nil || !strings.Contains(err.Error(), "chat_context_model_http_500") {
			t.Fatalf("非 2xx 应报错: %v", err)
		}
		badBody := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(200, `not-json`) }}}}
		if _, err := newService(&badBody).summarizePage(context.Background(), input, emptySnapshot(), messages); err == nil || !strings.Contains(err.Error(), "summary_missing_response") {
			t.Fatalf("非法 JSON 应报错: %v", err)
		}
		badSummary := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(200, `{"choices":[{"message":{"content":"not-json"}}]}`) }}}}
		if _, err := newService(&badSummary).summarizePage(context.Background(), input, emptySnapshot(), messages); err == nil || !strings.Contains(err.Error(), "summary_invalid_json") {
			t.Fatalf("非法摘要应报错: %v", err)
		}
	})
	t.Run("enrichSourceMessages 无图片", func(t *testing.T) {
		service := newService(&mockExecutor{})
		enriched, err := service.enrichSourceMessages(input, []contextSourceMessage{
			{role: "user", contentText: "问题", contentBlocksJSON: "[]"},
			{role: "user", contentText: "坏块", contentBlocksJSON: "{bad"},
		})
		if err != nil || len(enriched) != 2 {
			t.Fatalf("enrich 失败: %+v err=%v", enriched, err)
		}
	})
}

// TestRunnerSubscriberLifecycleW3 覆盖订阅者裁剪、panic 吸收与终态补发。
func TestRunnerSubscriberLifecycleW3(t *testing.T) {
	t.Run("失败订阅者被裁剪", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			delta := "a"
			ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			delta2 := "b"
			ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &delta2})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		flaky := &flakySubscriberW3{failAfter: 1}
		_ = runner.Subscribe(flaky)
		runner.Start(nil)
		runner.Wait()
		if len(flaky.received) != 1 {
			t.Fatalf("失败订阅者应只收到 1 个事件: %d", len(flaky.received))
		}
	})
	t.Run("panic 订阅者被吸收", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			delta := "a"
			ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		_ = runner.Subscribe(panickingSubscriberW3{})
		runner.Start(nil)
		runner.Wait()
	})
	t.Run("非权威失败时间线静默收敛", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			delta := "正文"
			ctx.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			reasoning := "思考"
			ctx.Publish("reasoning.delta", nil, ChatGenerationProjectionUpdate{ReasoningTextDelta: &reasoning})
			return ChatGenerationTerminalResult{}, errorsNewW3("boom")
		})
		runner.onUnexpectedError = func(PublicChatGenerationError) error { return errorsNewW3("persist") }
		subscriber := &collectingSubscriberW3{}
		_ = runner.Subscribe(subscriber)
		runner.Start(nil)
		runner.Wait()
		// 契约：非权威失败仅做时间线 Finalize（内存态收敛为 failed），不再补发事件。
		var patches int
		for _, event := range subscriber.snapshot() {
			if event.Type == "content_block.updated" || event.Type == "content_block.completed" {
				patches++
			}
		}
		if patches != 0 {
			t.Fatalf("非权威失败不应补发事件: %d", patches)
		}
		for _, block := range runner.SnapshotContentBlocks() {
			if block.Type == "reasoning" && block.Status != asstFailed {
				t.Fatalf("推理块应收敛为 failed: %+v", block)
			}
		}
	})
	t.Run("订阅快照携带工具事件", func(t *testing.T) {
		runner := newTestRunnerW3(func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
			toolEvent := &ChatGenerationToolEvent{ID: "call_1", ToolType: "web_search", Status: "completed", Item: map[string]any{"ok": 1}}
			ctx.Publish("tool.completed", nil, ChatGenerationProjectionUpdate{ToolEvent: toolEvent})
			return ChatGenerationTerminalResult{Status: "completed"}, nil
		})
		runner.Start(nil)
		runner.Wait()
		late := &collectingSubscriberW3{}
		if !runner.Subscribe(late) {
			t.Fatalf("终态后订阅应仍成功")
		}
		snapshot := late.snapshot()[0]
		if snapshot.Type != "message.snapshot" {
			t.Fatalf("应先发快照")
		}
		assistant := snapshot.Data["assistant"].(map[string]any)
		events := assistant["toolEvents"].([]any)
		if len(events) != 1 {
			t.Fatalf("快照应包含工具事件: %v", events)
		}
		if assistant["status"] != "completed" {
			t.Fatalf("快照状态 = %v", assistant["status"])
		}
	})
}

// nilResponseExecutorW3 返回 (nil, nil) 响应，用于触发 dispatch_missing 分支
// （mockExecutor 对 nil 响应会阻塞等待取消，不能用于该路径）。
type nilResponseExecutorW3 struct{}

func (nilResponseExecutorW3) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	return nil, nil
}

type flakySubscriberW3 struct {
	failAfter int
	received  []ChatGenerationEvent
}

func (s *flakySubscriberW3) TrySend(event ChatGenerationEvent) bool {
	if len(s.received) >= s.failAfter {
		return false
	}
	s.received = append(s.received, event)
	return true
}

type panickingSubscriberW3 struct{}

func (panickingSubscriberW3) TrySend(ChatGenerationEvent) bool { panic("订阅者崩溃") }

func errorsNewW3(message string) error { return &staticErrorW3{message: message} }

type staticErrorW3 struct{ message string }

func (e *staticErrorW3) Error() string { return e.message }

// streamPostWithHeaderW3 带单个额外头部的流式 POST。
func streamPostWithHeaderW3(t *testing.T, env *generationEnv, conversationID, owner, payload, key, value string) routeResponse {
	t.Helper()
	request, err := http.NewRequest("POST", env.server.URL+"/__aisys__/api/my-chat/conversations/"+conversationID+"/stream", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Owner", owner)
	if key != "" {
		request.Header.Set(key, value)
	}
	return doRequestW3(t, request)
}

// TestStreamRouteConflictBranchesW3 覆盖流式路由的 Hub 冲突与超大消息分支。
func TestStreamRouteConflictBranchesW3(t *testing.T) {
	t.Run("Hub 缺失 → 生成冲突", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_nohub", routeTestOwner)
		env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("好", true))
		}}}
		env.deps.Hub = nil
		response := env.streamPost("chat_conv_nohub", routeTestOwner, streamPayload("cmid-nohub", "内容", "gpt-5"))
		if response.status != http.StatusConflict || response.code() != "chat_stream_conflict" {
			t.Fatalf("Hub 缺失 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("消息超 192KiB → 413", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_big", routeTestOwner)
		big := strings.Repeat("字", 70*1024)
		payload := `{"clientMessageId":"cmid-big","content":"` + big + `","model":"gpt-5"}`
		response := env.streamPost("chat_conv_big", routeTestOwner, payload)
		if response.status != http.StatusRequestEntityTooLarge || response.message() != "消息内容超过 192 KiB 上限" {
			t.Fatalf("413 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("带 trace id 与替换链路", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_trace", routeTestOwner)
		env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("首轮", true))
		}}}
		first := streamPostWithHeaderW3(t, env, "chat_conv_trace", routeTestOwner, streamPayload("cmid-t1", "第一问", "gpt-5"), "X-Trace-Id", "trace-w3")
		if first.status != http.StatusOK {
			t.Fatalf("首轮失败: %d %s", first.status, first.rawString())
		}
		env.fixture.seedTurns(routeTestOwner, "chat_conv_trace", 0)
		// 替换第一轮。
		env.executor.steps = []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("改写", true))
		}}}
		events := sseEvents(first.rawString())
		var turnID string
		var started struct {
			TurnID string `json:"turnId"`
		}
		if err := json.Unmarshal([]byte(events[0].data), &started); err != nil {
			t.Fatalf("started 事件解析失败: %v", err)
		}
		turnID = started.TurnID
		replacePayload := `{"clientMessageId":"cmid-t2","replaceTurnId":"` + turnID + `","content":"改写后","model":"gpt-5"}`
		replacement := streamPostWithHeaderW3(t, env, "chat_conv_trace", routeTestOwner, replacePayload, "X-Trace-Id", "trace-w3")
		if replacement.status != http.StatusOK {
			t.Fatalf("替换流失败: %d %s", replacement.status, replacement.rawString())
		}
	})
}

// TestChatEnvIntOrDefaultW3 覆盖环境变量整数的 fail-fast 契约。
func TestChatEnvIntOrDefaultW3(t *testing.T) {
	t.Setenv("W3_CHAT_INT", "")
	if got := chatEnvIntOrDefault("W3_CHAT_INT", 7, 1, 10); got != 7 {
		t.Fatalf("空值应回退: %d", got)
	}
	t.Setenv("W3_CHAT_INT", "5")
	if got := chatEnvIntOrDefault("W3_CHAT_INT", 7, 1, 10); got != 5 {
		t.Fatalf("合法值应生效: %d", got)
	}
	t.Setenv("W3_CHAT_INT", "abc")
	assertPanicMessageW3(t, func() { chatEnvIntOrDefault("W3_CHAT_INT", 7, 1, 10) }, "必须配置为整数")
	t.Setenv("W3_CHAT_INT", "99")
	assertPanicMessageW3(t, func() { chatEnvIntOrDefault("W3_CHAT_INT", 7, 1, 10) }, "必须在 1-10 范围内")
}

func assertPanicMessageW3(t *testing.T, fn func(), contains string) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("应发生 panic")
		}
		if message, ok := recovered.(string); !ok || !strings.Contains(message, contains) {
			t.Fatalf("panic 消息不正确: %v", recovered)
		}
	}()
	fn()
}

// TestDepsGuardsW3 覆盖 deps 层密钥与目录守卫分支。
func TestDepsGuardsW3(t *testing.T) {
	rt := &chatRoutes{deps: &Deps{ChatKeys: &mockChatKeys{}}}
	if _, err := rt.requireOwnedApiKey("", "owner"); err == nil || !strings.Contains(err.Error(), "已删除") {
		t.Fatalf("空 key 应报已删除: %v", err)
	}
	noKeys := &chatRoutes{deps: &Deps{}}
	if _, err := noKeys.requireOwnedApiKey("k", "owner"); err == nil {
		t.Fatalf("ChatKeys 缺失应报错")
	}
	returnsNil := &stubKeysNilW3{}
	nilKeys := &chatRoutes{deps: &Deps{ChatKeys: returnsNil}}
	if _, err := nilKeys.requireOwnedApiKey("k", "owner"); err == nil {
		t.Fatalf("nil 密钥应报错")
	}
	if _, err := nilKeys.requireChatAPIKeyForOwner("owner"); err == nil {
		t.Fatalf("nil 密钥的自动开通应报错")
	}
	noGateway := &chatRoutes{deps: &Deps{}}
	if _, err := noGateway.loadChatModelAccess(&ChatAPIKeyRecord{Secret: "s"}); err == nil {
		t.Fatalf("GatewayKeys 缺失应报错")
	}
	returnsNilView := &stubGatewayNilW3{}
	if _, err := (&chatRoutes{deps: &Deps{GatewayKeys: returnsNilView}}).loadChatModelAccess(&ChatAPIKeyRecord{Secret: "s"}); err == nil {
		t.Fatalf("nil 视图应报错")
	}
	// 分组去重与无效绑定过滤。
	dup := &chatRoutes{deps: &Deps{GatewayKeys: mockGatewayKeys{}, ModelCatalog: mockModelCatalog{}}}
	access, err := dup.loadChatModelAccess(&ChatAPIKeyRecord{Secret: "s"})
	if err != nil || !equalStringsW3(access.GroupIDs, []string{"group-a"}) {
		t.Fatalf("分组提取不正确: %+v err=%v", access, err)
	}
	if !dup.hasChatImageGenerationRoute([]string{"group-a"}, "owner") {
		t.Fatalf("api_key 账户应支持图片路由")
	}
	emptyCatalog := &emptyCatalogW3{}
	if (&chatRoutes{deps: &Deps{ModelCatalog: emptyCatalog}}).hasChatImageGenerationRoute([]string{"g"}, "owner") {
		t.Fatalf("无账户不应支持图片路由")
	}
	if got := normalizeProviderToken("  OpenAI "); got != "openai" {
		t.Fatalf("normalizeProviderToken = %q", got)
	}
	deps := &Deps{MaxConversationsPerUserInt: func() int { return 33 }}
	if (&chatRoutes{deps: deps}).deps.maxConversationsPerUser() != 33 {
		t.Fatalf("注入上限应生效")
	}
	payload := chatModelCapabilitiesPayload(&ChatModelOption{
		ID: "gpt-5", SupportsPromptCaching: true, DefaultReasoningEffort: "low",
		ContextWindowTokens: int64PtrT(100), MaxInputTokens: int64PtrT(80), MaxOutputTokens: int64PtrT(20),
		SupportedReasoningEfforts: []string{"low"},
	})
	if payload["defaultReasoningEffort"] != "low" || payload["contextWindowTokens"] != int64(100) || payload["maxInputTokens"] != int64(80) {
		t.Fatalf("能力载荷不正确: %v", payload)
	}
}

type stubKeysNilW3 struct{}

func (stubKeysNilW3) EnsureChatAPIKey(string) (string, error) { return "k", nil }

func (stubKeysNilW3) FindChatAPIKey(string, string) (*ChatAPIKeyRecord, error) { return nil, nil }

type stubGatewayNilW3 struct{}

func (stubGatewayNilW3) ValidateGatewayKey(string) (*GatewayKeyView, error) { return nil, nil }

type emptyCatalogW3 struct{}

func (emptyCatalogW3) ListAccountsForGroup(string, string, string, string) []ChatTransportAccount {
	return nil
}

func (emptyCatalogW3) ListProviderCatalog(string, string) []ProviderModelCatalogItem { return nil }

// TestRequestEtagMatchesW3 覆盖 If-None-Match 匹配表。
func TestRequestEtagMatchesW3(t *testing.T) {
	etag := `"abc"`
	if requestEtagMatches(nil, etag) {
		t.Fatalf("空头部不应匹配")
	}
	if !requestEtagMatches([]string{"*"}, etag) {
		t.Fatalf("星号应匹配")
	}
	if !requestEtagMatches([]string{`W/"abc"`}, etag) {
		t.Fatalf("弱匹配应生效")
	}
	if !requestEtagMatches([]string{`"x", "abc"`}, etag) {
		t.Fatalf("列表应匹配")
	}
	if requestEtagMatches([]string{`"other"`}, etag) {
		t.Fatalf("不匹配的 etag 应返回 false")
	}
}

// TestAssetRouteRuntimeGapsW3 覆盖资产路由的运行时缺口分支。
func TestAssetRouteRuntimeGapsW3(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_gaps", routeTestOwner)
	server := mountAssetMuxW3(t, env)

	t.Run("ImageProcessor 缺失", func(t *testing.T) {
		previous := env.deps.ImageProcessor
		env.deps.ImageProcessor = nil
		response := uploadAssetW3(t, server.URL+"/conversations/chat_conv_gaps/assets", routeTestOwner, "cat.png", mustDecodeBase64W3(testTinyPNGBase64))
		env.deps.ImageProcessor = previous
		if response.status != http.StatusBadRequest || response.message() != "图片上传必须使用 multipart/form-data" {
			t.Fatalf("处理器缺失 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("ObjectStore 缺失的内容读取", func(t *testing.T) {
		created := uploadAssetW3(t, server.URL+"/conversations/chat_conv_gaps/assets", routeTestOwner, "cat.png", mustDecodeBase64W3(testTinyPNGBase64))
		assetID, _ := created.dataMap()["id"].(string)
		previous := env.deps.ObjectStore
		env.deps.ObjectStore = nil
		request := mustGetW3(t, server.URL+"/conversations/chat_conv_gaps/assets/"+assetID+"/content", routeTestOwner)
		response := doRequestW3(t, request)
		env.deps.ObjectStore = previous
		if response.status != http.StatusInternalServerError {
			t.Fatalf("存储缺失 = %d %s", response.status, response.rawString())
		}
	})
	rt := newChatRoutesForTest(env.deps)
	if got := rt.releaseRetryAt(); got == "" {
		t.Fatalf("重试时间不应为空")
	}
}

// TestImageHelpersW3 覆盖图片解码 helper 的边界。
func TestImageHelpersW3(t *testing.T) {
	if _, err := decodeBase64Payload(strings.Repeat("Q", 200), 8); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("超限应报错（文案固定）: %v", err)
	}
	if _, err := decodeBase64Payload("", 8); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空载荷应报错: %v", err)
	}
	if got := extractImageResultChunksWithFields(`{"b64_json":"QUJD"}`, "b64_json"); !equalStringsW3(got, []string{"QUJD"}) {
		t.Fatalf("抽取失败: %v", got)
	}
	// 该抽取器不做 base64 校验（校验在 decodeBase64Payload）。
	if got := extractImageResultChunksWithFields(`{"b64_json":"!!"}`, "b64_json"); !equalStringsW3(got, []string{"!!"}) {
		t.Fatalf("抽取结果不正确: %v", got)
	}
	if message, errorType := readImageGenerationErrorPayload(`{"error":{"message":"具体","type":"t"}}`, "兜底"); message != "具体" || errorType != "t" {
		t.Fatalf("错误载荷解析失败: %q %q", message, errorType)
	}
	if message, errorType := readImageGenerationErrorPayload(`{"error":{}}`, "兜底"); message != "兜底" || errorType != "" {
		t.Fatalf("空错误应回兜底: %q %q", message, errorType)
	}
	if message, _ := readImageGenerationErrorPayload(`not-json`, "兜底"); message != "兜底" {
		t.Fatalf("非 JSON 应回兜底: %q", message)
	}
}

// TestLockUserPolicyReentrantW3 覆盖 SQLite 用户策略锁的顺序加解锁。
// 注意：该锁不可重入（同 owner 二次 Lock 会阻塞），生产调用点互不嵌套。
func TestLockUserPolicyReentrantW3(t *testing.T) {
	f := newChatFixture(t)
	release := f.store.lockUserPolicy("owner-lock-1")
	done := make(chan struct{})
	go func() {
		inner := f.store.lockUserPolicy("owner-lock-1")
		inner()
		close(done)
	}()
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("释放后同 owner 应可再次加锁")
	}
	if f.store.pg {
		t.Fatalf("fixture 应为 SQLite 模式")
	}
	// pg 模式的锁是 no-op。
	pgStore := &Store{pg: true}
	pgRelease := pgStore.lockUserPolicy("owner-lock")
	pgRelease()
	if got, err := requiredAssistantStorageReservation(AssistantStorageReservationBytes); err != nil || got != AssistantStorageReservationBytes {
		t.Fatalf("预留校验失败: %d %v", got, err)
	}
	if _, err := requiredAssistantStorageReservation(0); err == nil {
		t.Fatalf("非法预留应报错")
	}
}

// TestIntegerQueryEdgeW3 补充 integerQuery 边界与 utf8 长度。
func TestIntegerQueryEdgeW3(t *testing.T) {
	if utf8RuneLen("中文") != 2 {
		t.Fatalf("utf8RuneLen 失败")
	}
	if utf8Len("中文") != 2 || utf8Bytes("中文") != 6 {
		t.Fatalf("utf8 计数契约不正确")
	}
	if ascDesc(true) != "ASC" || ascDesc(false) != "DESC" {
		t.Fatalf("ascDesc 契约不正确")
	}
	if _, err := decodeObjectBody(json.RawMessage(`"str"`)); err == nil {
		t.Fatalf("标量体应报错")
	}
	recorder := httptest.NewRecorder()
	writeOKStatus(recorder, http.StatusAccepted, map[string]any{"state": "accepted"})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("writeOKStatus 状态码不正确: %d", recorder.Code)
	}
}
