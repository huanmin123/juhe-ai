package chat

// 批次1-B 修复回归：
//   - 缺陷一（客户端断开即取消）：runner context 与请求 context 脱钩后，
//     请求取消只停止写 SSE 响应，生成继续跑完并落库（可经重附恢复）。
//   - 缺陷二（20 分钟误杀）：TouchActiveChatTurn 只刷新仍处 streaming 的
//     活跃轮次，不复活终态轮次；流式期间 ticker 周期推进 active_started_at。
//   - 缺陷三（成功路径裸 return）：CompleteChatTurn 失败走与失败路径同型的
//     recoverChatTurnFinalization 收敛，发终止事件而非丢弃内容。

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// streamDetachPostRaw 以可取消的请求 context 提交流式请求（直接调
// streamTurn，绕过会话中间件），返回取消函数与完成通道。
func streamDetachPostRaw(rt *chatRoutes, conversationID, payload, owner string) (cancelRequest func(), done <-chan *httptest.ResponseRecorder) {
	requestCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/stream", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	request = request.WithContext(authsys.WithAuthContext(requestCtx, &authsys.AuthContext{
		SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user",
	}))
	recorder := httptest.NewRecorder()
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rt.streamTurn(recorder, request)
		completed <- recorder
	}()
	return cancel, completed
}

// streamTurnIDByClientMessageID 从幂等表取轮次 ID。
func streamTurnIDByClientMessageID(t *testing.T, f *chatFixture, conversationID, clientMessageID string) string {
	t.Helper()
	var turnID string
	if err := f.db.QueryRow(`SELECT turn_id FROM chat_message_idempotency
		WHERE conversation_id = ? AND client_message_id = ?`, conversationID, clientMessageID).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	return turnID
}

// streamAssistantState 读取助手消息状态与内容。
func streamAssistantState(t *testing.T, f *chatFixture, turnID string) (status, content string) {
	t.Helper()
	if err := f.db.QueryRow(`SELECT status, content_text FROM chat_messages
		WHERE turn_id = ? AND role = 'assistant'`, turnID).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	return status, content
}

// waitForExecutorCall 等待上游派发到达（生成已进入执行）。
func waitForExecutorCall(t *testing.T, executor *mockExecutor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for executor.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if executor.callCount() == 0 {
		t.Fatal("上游派发未到达")
	}
}

// TestStreamDetachRunsWithoutClientContext 模拟客户端断开（取消请求
// context）：runner 不被取消，仍跑完并落库 completed——重附契约的核心行为。
func TestStreamDetachRunsWithoutClientContext(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_detach_run"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	bindStreamConversation(t, env, conversationID)
	rt := newChatRoutesForTest(env.deps)
	gate := make(chan struct{})
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.Contains(call.Path, "chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			<-gate // 上游挂起，等测试先模拟客户端断开
			return sseResponse(chatCompletionsSSE("断开后依然完成的回答", true))
		},
	})
	cancelRequest, done := streamDetachPostRaw(rt, conversationID,
		streamPayload("detach-run-cmid", "问题", "gpt-5"), routeTestOwner)
	waitForExecutorCall(t, env.executor)
	// 客户端断开：只应停止写响应，不得取消 runner。
	cancelRequest()
	close(gate)
	select {
	case recorder := <-done:
		if recorder.Code >= 500 {
			t.Fatalf("handler 异常返回：%d %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner 未在客户端断开后继续跑完并让 handler 返回")
	}
	turnID := streamTurnIDByClientMessageID(t, fixture, conversationID, "detach-run-cmid")
	status, content := streamAssistantState(t, fixture, turnID)
	if status != "completed" {
		t.Fatalf("断开后轮次应完成落库而非 %q（重附契约被破坏）", status)
	}
	if !strings.Contains(content, "断开后依然完成的回答") {
		t.Fatalf("内容未落库：%q", content)
	}
	var active sql.NullString
	if err := fixture.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active.Valid {
		t.Fatalf("完成后活跃轮应清空，仍为 %q", active.String)
	}
}

// TestStoreTouchActiveChatTurn 存储层契约：streaming 轮次刷新
// active_started_at；终态轮次不被复活。
func TestStoreTouchActiveChatTurn(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_touch_store"
	fixture.createConversation(conversationID, routeTestOwner)
	accepted := fixture.accept(routeTestOwner, conversationID, "touch-store-cmid", "问题")
	// streaming 期间：刷新生效。
	if err := fixture.store.TouchActiveChatTurn(context.Background(), conversationID, accepted.TurnID,
		"2026-03-10T08:10:00.000Z"); err != nil {
		t.Fatal(err)
	}
	var active string
	if err := fixture.db.QueryRow(`SELECT active_started_at FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != "2026-03-10T08:10:00.000Z" {
		t.Fatalf("streaming 轮次应刷新 active_started_at，得到 %q", active)
	}
	// 终态后：幂等，不复活。
	fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "回答")
	if err := fixture.store.TouchActiveChatTurn(context.Background(), conversationID, accepted.TurnID,
		"2026-03-10T09:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	var activeAfter sql.NullString
	if err := fixture.db.QueryRow(`SELECT active_started_at FROM chat_conversations WHERE id = ?`, conversationID).Scan(&activeAfter); err != nil {
		t.Fatal(err)
	}
	if activeAfter.Valid {
		t.Fatalf("终态轮次不应被 touch 复活，active_started_at = %q", activeAfter.String)
	}
}

// TestStreamCompleteChatTurnFailureRecovers 构造 CompleteChatTurn 失败
// （生成期间被外部清 active_turn_id 并置 failed，模拟 jobs 误判中断）：
// 成功路径不再裸 return err，走恢复收口并发终止事件。
func TestStreamCompleteChatTurnFailureRecovers(t *testing.T) {
	fixture := newChatFixture(t)
	conversationID := "chat_conv_complete_recover"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	bindStreamConversation(t, env, conversationID)
	rt := newChatRoutesForTest(env.deps)
	// 注意：respond 在 runner goroutine 内执行，禁止 t.Fatal（Goexit 会悬挂
	// 生成），破坏失败以 500 响应 + 破坏标志回传，由测试主体断言。
	var sabotageErr error
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.Contains(call.Path, "chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			// 与 jobs 的 failInterruptedChatTurnIfMatches 同语义：置 failed
			// 必须同时释放 storage_reserved_bytes（表级 CHECK 约束）。
			if _, err := fixture.db.Exec(`UPDATE chat_messages
				SET status = 'failed', storage_reserved_bytes = 0,
					error_code = 'stream_interrupted', error_message = '生成进程异常中断'
				WHERE conversation_id = ? AND role = 'assistant' AND status = 'streaming'`, conversationID); err != nil {
				sabotageErr = err
				return jsonStatusResponse(500, `{"message":"sabotage failed"}`)
			}
			if _, err := fixture.db.Exec(`UPDATE chat_conversations
				SET active_turn_id = NULL, active_started_at = NULL WHERE id = ?`, conversationID); err != nil {
				sabotageErr = err
				return jsonStatusResponse(500, `{"message":"sabotage failed"}`)
			}
			return sseResponse(chatCompletionsSSE("本应成功的回答", true))
		},
	})
	recorder := w14lStreamPostRaw(rt, conversationID,
		streamPayload("complete-recover-cmid", "问题", "gpt-5"), routeTestOwner)
	if sabotageErr != nil {
		t.Fatalf("破坏前置状态失败：%v", sabotageErr)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "message.failed") {
		t.Fatalf("应走恢复路径发出 message.failed 终止事件：%d %s", recorder.Code, body)
	}
	if strings.Contains(body, "message.completed") {
		t.Fatalf("不应再出现 message.completed：%s", body)
	}
	turnID := streamTurnIDByClientMessageID(t, fixture, conversationID, "complete-recover-cmid")
	status, _ := streamAssistantState(t, fixture, turnID)
	if status != "failed" {
		t.Fatalf("轮次应收敛为权威侧 failed，得到 %q", status)
	}
	var active sql.NullString
	if err := fixture.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active.Valid {
		t.Fatalf("恢复后活跃轮应保持清空，仍为 %q", active.String)
	}
}

// TestStreamActiveTouchDuringRun 流式期间 ticker 周期推进
// active_started_at；runner 完成后活跃轮清空。
func TestStreamActiveTouchDuringRun(t *testing.T) {
	originalInterval := chatActiveTurnTouchInterval
	chatActiveTurnTouchInterval = 10 * time.Millisecond
	t.Cleanup(func() { chatActiveTurnTouchInterval = originalInterval })

	fixture := newChatFixture(t)
	conversationID := "chat_conv_touch_run"
	fixture.createConversation(conversationID, routeTestOwner)
	env := buildGenerationEnvW10D(t, fixture)
	bindStreamConversation(t, env, conversationID)
	rt := newChatRoutesForTest(env.deps)
	// rt.now() 换为递增时钟：每次调用 +30s，使 active_started_at 的刷新可观察。
	var clockMu sync.Mutex
	base := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	env.deps.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		base = base.Add(30 * time.Second)
		return base
	}
	gate := make(chan struct{})
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.Contains(call.Path, "chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			<-gate // 挂起上游，保持轮次处于 streaming
			return sseResponse(chatCompletionsSSE("长回答", true))
		},
	})
	_, done := streamDetachPostRaw(rt, conversationID,
		streamPayload("touch-run-cmid", "问题", "gpt-5"), routeTestOwner)
	waitForExecutorCall(t, env.executor)
	var initial string
	if err := fixture.db.QueryRow(`SELECT active_started_at FROM chat_conversations WHERE id = ?`, conversationID).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var active string
		if err := fixture.db.QueryRow(`SELECT active_started_at FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active > initial {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("流式期间 active_started_at 未被刷新：%q -> %q", initial, active)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runner 未在放行后完成")
	}
	turnID := streamTurnIDByClientMessageID(t, fixture, conversationID, "touch-run-cmid")
	status, _ := streamAssistantState(t, fixture, turnID)
	if status != "completed" {
		t.Fatalf("轮次应完成，得到 %q", status)
	}
	var active sql.NullString
	if err := fixture.db.QueryRow(`SELECT active_turn_id FROM chat_conversations WHERE id = ?`, conversationID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active.Valid {
		t.Fatalf("完成后活跃轮应清空，仍为 %q", active.String)
	}
}

// TestStartActiveTurnTouchLoopStops 单元验证循环的停止契约：stop 之后与
// ctx 取消之后 touch 不再触发。
func TestStartActiveTurnTouchLoopStops(t *testing.T) {
	var mu sync.Mutex
	waitForCount := func(counter *int, target int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			mu.Lock()
			count := *counter
			mu.Unlock()
			if count >= target {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("touch 未按间隔触发：%d", count)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	assertStable := func(counter *int) {
		t.Helper()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		first := *counter
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		second := *counter
		mu.Unlock()
		if first != second {
			t.Fatalf("循环停止后 touch 仍被触发：%d -> %d", first, second)
		}
	}

	stopCount := 0
	stop := startActiveTurnTouchLoop(context.Background(), 5*time.Millisecond, func() {
		mu.Lock()
		defer mu.Unlock()
		stopCount++
	})
	waitForCount(&stopCount, 3)
	stop()
	assertStable(&stopCount)
	stop() // 幂等：重复 stop 不得 panic。

	ctxCount := 0
	ctx, cancel := context.WithCancel(context.Background())
	ctxStop := startActiveTurnTouchLoop(ctx, 5*time.Millisecond, func() {
		mu.Lock()
		defer mu.Unlock()
		ctxCount++
	})
	waitForCount(&ctxCount, 3)
	cancel()
	assertStable(&ctxCount)
	ctxStop()
}
