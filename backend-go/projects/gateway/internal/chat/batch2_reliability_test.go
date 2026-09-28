package chat

// 可靠性批次2测试：
//  1. GenerationHub.Shutdown 多 runner 并发排空（Abort + 有界等待 + 终态收敛）。
//  2. 手动压缩 claim 后立即返回 202：service 层 acceptance 时序 + handler 层
//     请求 context 与压缩执行 context 脱钩（WithoutCancel）。
//  4. requireChatAPIKeyForOwner 对停用/过期专用 Key 返回 400（查询失败仍 500）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- 缺陷1：hub Shutdown 多 runner 并发排空 ---

// TestGenerationHubShutdownConcurrentRunnersBatch2 覆盖多 runner 并发在途时
// Shutdown 的排空契约：Abort 全部 runner、有界等待终态收敛、清空注册表。
func TestGenerationHubShutdownConcurrentRunnersBatch2(t *testing.T) {
	hub := NewGenerationHub(func() string { return "2026-09-28T08:00:00.000Z" })
	const runnerCount = 3
	entered := make(chan struct{}, runnerCount)
	buildRunner := func(conversationID string) *ChatGenerationRunner {
		ctx, cancel := context.WithCancel(context.Background())
		return NewChatGenerationRunner(ChatGenerationRunnerOptions{
			Identity: ChatGenerationIdentity{OwnerID: "owner-1", ConversationID: conversationID, TurnID: "turn-1", AssistantMessageID: "asst-" + conversationID},
			Execute: func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
				entered <- struct{}{}
				<-ctx.Context.Done()
				return ChatGenerationTerminalResult{}, ctx.Context.Err()
			},
			Now: func() string { return "2026-09-28T08:00:00.000Z" },
		}, ctx, cancel, func() bool { return ctx.Err() != nil })
	}
	runners := make([]*ChatGenerationRunner, 0, runnerCount)
	for index := 0; index < runnerCount; index++ {
		runner := buildRunner("conv-batch2-" + strings.Repeat("a", index+1))
		if !hub.Start(runner) {
			t.Fatalf("hub.Start(%d) 失败", index)
		}
		runners = append(runners, runner)
	}
	// 确认三个 runner 都已进入 Execute（Shutdown 前确有在途生成）。
	for index := 0; index < runnerCount; index++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("runner %d 未进入 Execute", index)
		}
	}
	done := make(chan struct{})
	go func() {
		hub.Shutdown(2 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Shutdown 未在有界等待内完成")
	}
	for index, runner := range runners {
		runner.Wait()
		if !runner.Terminal() {
			t.Fatalf("runner %d 未收敛终态", index)
		}
	}
	hub.mu.Lock()
	remaining := len(hub.runners)
	hub.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("Shutdown 后应清空 runners: %d", remaining)
	}
	if !hub.shuttingDownState() {
		t.Fatalf("Shutdown 后应处于 shuttingDown")
	}
}

// --- 缺陷2：压缩 claim 后立即返回 202 ---

// batch2GateExecutor 是压缩后台执行的可控执行器：Dispatch 阻塞在 gate 上，
// 同时尊重自身 ctx 取消；成功时返回合法摘要 JSON（可安装 checkpoint）。
type batch2GateExecutor struct {
	gate    chan struct{}
	mu      sync.Mutex
	gotCtx  context.Context
	called  int
	ctxDead bool
}

func (e *batch2GateExecutor) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	e.mu.Lock()
	e.gotCtx = ctx
	e.called++
	e.mu.Unlock()
	summary := `{"choices":[{"message":{"content":"{\"durableMemory\":[\"稳定事实\"],\"currentGoal\":\"目标\",\"recentUserIntent\":\"意图\"}"}}]}`
	select {
	case <-e.gate:
		return &GenerationDispatchResponse{Status: 200, Body: io.NopCloser(strings.NewReader(summary))}, nil
	case <-ctx.Done():
		e.mu.Lock()
		e.ctxDead = true
		e.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (e *batch2GateExecutor) dispatchContext() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gotCtx
}

// TestCompactionStartAcceptsImmediatelyBatch2 覆盖 service 层：claim 成功后
// Start 立即结算 acceptance（accepted），压缩仍在后台执行（二次 Start 报
// already_running），执行结束后 active 槽释放。
func TestCompactionStartAcceptsImmediatelyBatch2(t *testing.T) {
	fixture := newChatFixture(t)
	fixture.createConversation("conv-batch2-start", "owner")
	fixture.seedTurns("owner", "conv-batch2-start", 4)
	executor := &batch2GateExecutor{gate: make(chan struct{})}
	service := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := CompactionInput{ConversationID: "conv-batch2-start", SystemAccountID: "owner", Model: "gpt-5"}

	accepted := make(chan CompactionStartResult, 1)
	go func() { accepted <- service.Start(ctx, input) }()
	select {
	case result := <-accepted:
		if result.Status != "accepted" {
			t.Fatalf("Start 应在 claim 后立即返回 accepted: %+v", result)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("Start 未在压缩完成前返回（acceptance 仍被 completion 阻塞）")
	}
	// 压缩仍在后台：同 key 二次 Start 走 already_running。
	if second := service.Start(ctx, input); second.Status != "already_running" {
		t.Fatalf("后台压缩在途时二次 Start 应为 already_running: %+v", second)
	}
	// 释放 gate：压缩在后台完成（service 层 Start 直接透传 ctx，脱钩由 handler
	// 层 WithoutCancel 承担，见 TestCompactionTriggerHandlerDecoupledContextBatch2）。
	close(executor.gate)
	deadline := time.After(3 * time.Second)
	for {
		service.mu.Lock()
		active := len(service.active)
		service.mu.Unlock()
		if active == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("后台压缩未在释放后完成，active=%d", active)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	// 完成后可再次启动全新压缩。
	if third := service.Start(context.Background(), input); third.Status == "" {
		t.Fatalf("active 释放后 Start 应返回明确状态: %+v", third)
	}
	service.mu.Lock()
	remaining := len(service.active)
	service.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("测试结束时应无在途压缩: %d", remaining)
	}
}

// TestCompactionTriggerHandlerDecoupledContextBatch2 覆盖 handler 层：executor
// 阻塞（压缩在途）时 compactionTrigger 立即返回 202；请求 context 取消后压缩
// 执行 context 不被传染（WithoutCancel 脱钩）。
func TestCompactionTriggerHandlerDecoupledContextBatch2(t *testing.T) {
	env := newGenerationEnv(t)
	executor := &batch2GateExecutor{gate: make(chan struct{})}
	compactions := NewCompactionService(env.fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return env.fixture.nowISO })
	env.deps.Compactions = compactions
	rt := newChatRoutesForTest(env.deps)
	conversationID := "conv-batch2-handler"
	env.fixture.createConversation(conversationID, routeTestOwner)
	env.fixture.seedTurns(routeTestOwner, conversationID, 4)

	request := w13bJSONRequest(t, "POST", "/conversations/"+conversationID+"/context/compactions", `{"model":"gpt-5"}`)
	requestCtx, cancelRequest := context.WithCancel(authedContextW13B(routeTestOwner))
	request = request.WithContext(requestCtx)
	request.SetPathValue("conversationId", conversationID)
	recorder := httptest.NewRecorder()

	returned := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rt.compactionTrigger(recorder, request)
		returned <- recorder
	}()
	select {
	case done := <-returned:
		if done.Code != http.StatusAccepted {
			t.Fatalf("压缩在途时 handler 应立即返回 202: %d %s", done.Code, done.Body.String())
		}
		body := map[string]any{}
		_ = json.Unmarshal(done.Body.Bytes(), &body)
		payload, _ := body["data"].(map[string]any)
		if payload == nil {
			payload = body
		}
		if state, _ := payload["state"].(string); state != "accepted" {
			t.Fatalf("202 载荷 state 应为 accepted: %v", body)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("handler 未在压缩完成前返回（手动压缩仍阻塞到结束）")
	}
	// 压缩仍在后台执行：handler 的会话动作表应拦下重复触发（already_running）。
	repeat := w13bJSONRequest(t, "POST", "/conversations/"+conversationID+"/context/compactions", `{"model":"gpt-5"}`)
	repeat = repeat.WithContext(authedContextW13B(routeTestOwner))
	repeat.SetPathValue("conversationId", conversationID)
	repeatRecorder := httptest.NewRecorder()
	rt.compactionTrigger(repeatRecorder, repeat)
	if repeatRecorder.Code != http.StatusAccepted || !strings.Contains(repeatRecorder.Body.String(), "already_running") {
		t.Fatalf("后台压缩在途时重复触发应为 202 already_running: %d %s", repeatRecorder.Code, repeatRecorder.Body.String())
	}
	// 请求返回后取消请求 context：执行 context 不应被传染。
	cancelRequest()
	execCtx := executor.dispatchContext()
	if execCtx == nil {
		t.Fatalf("压缩应已派发上游")
	}
	select {
	case <-execCtx.Done():
		t.Fatalf("压缩执行 context 被请求取消传染（脱钩缺失）")
	default:
	}
	// 释放后台压缩并等待结束，避免 goroutine 泄漏。
	close(executor.gate)
	deadline := time.After(3 * time.Second)
	for {
		compactions.mu.Lock()
		active := len(compactions.active)
		compactions.mu.Unlock()
		if active == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("后台压缩未在释放后完成: %d", active)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// --- 缺陷4：停用/过期专用 chat Key 返回 400 ---

// batch2ChatKeys 是状态可控的 chat key 端口 fake：EnsureChatAPIKey 幂等返回
// 固定 keyID，FindChatAPIKey 按测试注入返回。
type batch2ChatKeys struct {
	mu      sync.Mutex
	record  *ChatAPIKeyRecord
	findErr error
	ensure  int32
}

func (b *batch2ChatKeys) EnsureChatAPIKey(string) (string, error) {
	atomic.AddInt32(&b.ensure, 1)
	return "chat_key_batch2", nil
}

func (b *batch2ChatKeys) FindChatAPIKey(string, string) (*ChatAPIKeyRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.record, b.findErr
}

// batch2GroupLookup 是创建会话 group 模式的最小分组端口 fake。
type batch2GroupLookup struct{}

func (batch2GroupLookup) FindChatGroup(_ ChatBindScope, groupID string) (*ChatGroupRef, error) {
	return &ChatGroupRef{ID: groupID, Name: "分组", Enabled: true}, nil
}

func TestRequireChatAPIKeyForOwnerDisabledKeyBatch2(t *testing.T) {
	env := newGenerationEnv(t)
	rt := newChatRoutesForTest(env.deps)
	t.Run("停用 Key 返回 400 可恢复输入错误", func(t *testing.T) {
		keys := &batch2ChatKeys{record: &ChatAPIKeyRecord{ID: "chat_key_batch2", Secret: "s", Status: "disabled"}}
		rt.deps.ChatKeys = keys
		_, err := rt.requireChatAPIKeyForOwner(routeTestOwner)
		if _, ok := err.(*invalidRequestError); !ok {
			t.Fatalf("停用 Key 应为 invalidRequestError(400): %T %v", err, err)
		}
		if err.Error() != "专用对话 Key 已停用或过期，请在 API Key 页面恢复后重试" {
			t.Fatalf("文案不符: %q", err.Error())
		}
	})
	t.Run("缺失 Key 返回 400", func(t *testing.T) {
		rt.deps.ChatKeys = &batch2ChatKeys{record: nil}
		_, err := rt.requireChatAPIKeyForOwner(routeTestOwner)
		if _, ok := err.(*invalidRequestError); !ok {
			t.Fatalf("缺失 Key 应为 invalidRequestError(400): %T %v", err, err)
		}
	})
	t.Run("查询失败保持 500", func(t *testing.T) {
		rt.deps.ChatKeys = &batch2ChatKeys{findErr: errors.New("db down")}
		_, err := rt.requireChatAPIKeyForOwner(routeTestOwner)
		if _, ok := err.(*invalidRequestError); ok {
			t.Fatalf("查询失败不应是 invalidRequestError: %v", err)
		}
		if err == nil || err.Error() != "db down" {
			t.Fatalf("查询失败应保留原始错误: %v", err)
		}
	})
	t.Run("端口未接线保持 500", func(t *testing.T) {
		rt.deps.ChatKeys = nil
		if _, err := rt.requireChatAPIKeyForOwner(routeTestOwner); err == nil {
			t.Fatalf("ChatKeys 未接线应返回错误")
		} else if _, ok := err.(*DomainError); !ok {
			t.Fatalf("ChatKeys 未接线应为 DomainError(500)")
		}
	})
}

// TestCreateConversationGroupModeDisabledChatKeyBatch2 覆盖 handler 级：group
// 模式创建会话时专用 chat Key 停用 → 400 + 中文文案（原为 500 不对称）。
func TestCreateConversationGroupModeDisabledChatKeyBatch2(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupLookup = batch2GroupLookup{}
	rt := newChatRoutesForTest(env.deps)
	rt.deps.ChatKeys = &batch2ChatKeys{record: &ChatAPIKeyRecord{ID: "chat_key_batch2", Secret: "s", Status: "disabled"}}
	request := w13bJSONRequest(t, "POST", "/conversations", `{"bindMode":"group","groupId":"grp-1"}`)
	request = request.WithContext(authedContextW13B(routeTestOwner))
	recorder := httptest.NewRecorder()
	rt.createConversationHandler(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("停用专用 Key 的 group 模式创建应为 400: %d %s", recorder.Code, recorder.Body.String())
	}
	body := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if message, _ := body["message"].(string); message != "专用对话 Key 已停用或过期，请在 API Key 页面恢复后重试" {
		t.Fatalf("400 文案不符: %v", body)
	}
	if code, _ := body["code"].(string); code != "chat_invalid_request" {
		t.Fatalf("错误码应与 api_key 模式口径一致: %v", body)
	}
}
