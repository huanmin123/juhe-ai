package chat

// 恢复收口携带部分正文（AI 问答设计 §13.3）回归：进程内恢复路径
// （recoverChatTurnFinalization → FailInterruptedTurnIfMatches）把内存中已
// 流出的部分正文随 interrupted 条件收口落库，不再落 failed+空内容；权威终态
// 优先不覆盖、CAS 不匹配不收口、空内容保持旧语义、超预留按 finalizeTurn
// 同型降级；恢复收口后的 failed 轮（有正文）满足 §14.1 尾部注入条件。

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestChatGenerationContinuationMessage 动态失败文案（AI 问答设计 §11.1）：
// 四个可能带半截内容的错误码有续写引导变体，其余码回落静态映射；静态映射的
// upstream_stream_failed 为无内容语境的原始重试引导。
func TestChatGenerationContinuationMessage(t *testing.T) {
	cases := []struct {
		code PublicChatGenerationErrorCode
		want string
	}{
		{GenErrUpstreamStream, "模型响应中断，可直接发送消息继续，或重新生成"},
		{GenErrInternal, "生成任务异常结束，已生成部分可直接发送消息继续，或重新生成"},
		{GenErrUpstreamHTTP, "模型服务请求失败，已生成部分可直接发送消息继续"},
		{GenErrImageFailed, "图片生成失败，已生成文本可直接发送消息继续，或重新生成"},
	}
	for _, item := range cases {
		if got := ChatGenerationContinuationMessage(item.code); got != item.want {
			t.Fatalf("续写引导变体不符 %s: %q", item.code, got)
		}
	}
	// 无变体的码回落静态映射。
	if got := ChatGenerationContinuationMessage(GenErrStreamInterrupted); got != "生成连接已中断，请重新发送" {
		t.Fatalf("stream_interrupted 不应设变体: %q", got)
	}
	// 静态映射回落为无内容语境的原始重试引导。
	if got := ChatGenerationErrorMessage(GenErrUpstreamStream); got != "模型响应中断，请重新发送" {
		t.Fatalf("静态 upstream_stream_failed 应为重试引导: %q", got)
	}
}

// TestStreamRouteFailureMessageContinuationVariant 端到端：上游流断流但已有
// 半截内容 → message.failed 与落库 error_message 均为续写引导变体（§14.1
// 续写可用），下发与落库同值。
func TestStreamRouteFailureMessageContinuationVariant(t *testing.T) {
	env := buildGenerationEnvW10D(t, newChatFixture(t))
	conversationID := "chat_conv_failmsg"
	env.fixture.createConversation(conversationID, routeTestOwner)
	// 截断 SSE：有 content 增量但缺 [DONE] → 收集器报错，partialContent 非空。
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse("data: " + `{"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
				"data: " + `{"choices":[{"delta":{"content":"断流前已写出的半截"}}]}` + "\n\n")
		},
	})
	rt := newChatRoutesForTest(env.deps)

	recorder := w14lStreamPostRaw(rt, conversationID, streamPayload("failmsg-cmid-1", "帮我写一段长文", "gpt-5"), routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.failed") {
		t.Fatalf("断流应以 failed 终态收口: %d %s", recorder.Code, body[:min(len(body), 300)])
	}
	variant := "模型响应中断，可直接发送消息继续，或重新生成"
	if !strings.Contains(body, variant) {
		t.Fatalf("message.failed 应携带续写引导变体: %s", body[:min(len(body), 600)])
	}
	turnID := streamTurnIDByClientMessageID(t, env.fixture, conversationID, "failmsg-cmid-1")
	var errorCode, errorMessage, contentText string
	if err := env.fixture.db.QueryRow(`SELECT error_code, error_message, content_text FROM chat_messages
		WHERE turn_id = ? AND role = 'assistant'`, turnID).Scan(&errorCode, &errorMessage, &contentText); err != nil {
		t.Fatal(err)
	}
	if errorCode != "upstream_stream_failed" || errorMessage != variant || contentText != "断流前已写出的半截" {
		t.Fatalf("落库错误信息应为动态变体且保留半截内容: %q / %q / %q", errorCode, errorMessage, contentText)
	}
}

// TestFailInterruptedTurnWithContentPersistsPartialContent streaming 占位 +
// active_turn_id 匹配 → 收口 failed 且 content_text=传入正文，预留按实际字节
// 结算（finalizeTurn 同型）。
func TestFailInterruptedTurnWithContentPersistsPartialContent(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_rec_content"
	f.createConversation(conversationID, routeTestOwner)
	accepted := f.accept(routeTestOwner, conversationID, "rec-content-cmid-1", "问题")
	partial := "恢复前已流出的半截内容"

	result, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID:     conversationID,
		SystemAccountID:    routeTestOwner,
		ExpectedTurnID:     accepted.TurnID,
		InterruptedContent: partial,
		Now:                f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusFailed {
		t.Fatalf("恢复收口应应用 interrupted 终态: %+v", result)
	}
	status, content := streamAssistantState(t, f, accepted.TurnID)
	if status != string(StatusFailed) || content != partial {
		t.Fatalf("恢复收口应写入部分正文: status=%q content=%q", status, content)
	}
	var errorCode string
	if err := f.db.QueryRow(`SELECT error_code FROM chat_messages
		WHERE turn_id = ? AND role = 'assistant'`, accepted.TurnID).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "stream_interrupted" {
		t.Fatalf("恢复收口错误码应为 stream_interrupted: %q", errorCode)
	}
	// 容量窗口：预留全部释放，实际字节按正文结算入账。
	var reserved int64
	if err := f.db.QueryRow(`SELECT COALESCE(SUM(reserved_bytes), 0) FROM chat_user_storage_windows
		WHERE system_account_id = ?`, routeTestOwner).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("恢复收口后预留应清零: %d", reserved)
	}
	var userBytes, assistantBytes int64
	if err := f.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN role = 'user' THEN content_bytes ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN role = 'assistant' THEN content_bytes ELSE 0 END), 0)
		FROM chat_messages WHERE conversation_id = ?`, conversationID).Scan(&userBytes, &assistantBytes); err != nil {
		t.Fatal(err)
	}
	var windowContent int64
	if err := f.db.QueryRow(`SELECT COALESCE(SUM(content_bytes), 0) FROM chat_user_storage_windows
		WHERE system_account_id = ?`, routeTestOwner).Scan(&windowContent); err != nil {
		t.Fatal(err)
	}
	if windowContent != userBytes+assistantBytes || assistantBytes != int64(len(partial)) {
		t.Fatalf("容量窗口应按实际字节结算: window=%d user=%d assistant=%d", windowContent, userBytes, assistantBytes)
	}
}

// TestFailInterruptedTurnWithContentSkipsTerminalTurn 轮次已 completed 时恢复
// 路径不写内容，返回既有终态（权威状态优先）。
func TestFailInterruptedTurnWithContentSkipsTerminalTurn(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_rec_terminal"
	f.createConversation(conversationID, routeTestOwner)
	accepted := f.accept(routeTestOwner, conversationID, "rec-terminal-cmid-1", "问题")
	f.complete(routeTestOwner, conversationID, accepted.TurnID, "完整回答")

	result, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID:     conversationID,
		SystemAccountID:    routeTestOwner,
		ExpectedTurnID:     accepted.TurnID,
		InterruptedContent: "迟到的部分正文",
		Now:                f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusCompleted {
		t.Fatalf("已终态轮次应返回其既有状态: %+v", result)
	}
	status, content := streamAssistantState(t, f, accepted.TurnID)
	if status != string(StatusCompleted) || content != "完整回答" {
		t.Fatalf("已终态轮次不得被覆盖: status=%q content=%q", status, content)
	}
}

// TestFailInterruptedTurnEmptyContentKeepsLegacySemantics 空内容调用兼容旧
// 语义：不写内容列（content_text 保持空），错误码仍为 stream_interrupted。
func TestFailInterruptedTurnEmptyContentKeepsLegacySemantics(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_rec_empty"
	f.createConversation(conversationID, routeTestOwner)
	accepted := f.accept(routeTestOwner, conversationID, "rec-empty-cmid-1", "问题")

	result, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID:  conversationID,
		SystemAccountID: routeTestOwner,
		ExpectedTurnID:  accepted.TurnID,
		Now:             f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusFailed {
		t.Fatalf("中断收口应应用 failed 终态: %+v", result)
	}
	status, content := streamAssistantState(t, f, accepted.TurnID)
	if status != string(StatusFailed) || content != "" {
		t.Fatalf("空内容调用不得写正文: status=%q content=%q", status, content)
	}
}

// TestFailInterruptedTurnContentTurnMismatch active_turn_id 不匹配（竞态：
// 并发收口方已接管）时不收口、不写内容，行保持 streaming。
func TestFailInterruptedTurnContentTurnMismatch(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_rec_mismatch"
	f.createConversation(conversationID, routeTestOwner)
	accepted := f.accept(routeTestOwner, conversationID, "rec-mismatch-cmid-1", "问题")
	if _, err := f.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'turn-other' WHERE id = ?`, conversationID); err != nil {
		t.Fatal(err)
	}

	result, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID:     conversationID,
		SystemAccountID:    routeTestOwner,
		ExpectedTurnID:     accepted.TurnID,
		InterruptedContent: "不应写入的正文",
		Now:                f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CancelStateTurnMismatch {
		t.Fatalf("active_turn_id 不匹配应返回 turn_mismatch: %+v", result)
	}
	status, content := streamAssistantState(t, f, accepted.TurnID)
	if status != "streaming" || content != "" {
		t.Fatalf("不匹配时不得收口或写正文: status=%q content=%q", status, content)
	}
}

// TestFailInterruptedTurnContentOverReservationDowngrades 正文超过 448 KiB
// 预留时按 finalizeTurn 同型降级：空内容安全 failed 终态 + 存储上限错误码，
// 不因正文超限产生新失败。
func TestFailInterruptedTurnContentOverReservationDowngrades(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_rec_downgrade"
	f.createConversation(conversationID, routeTestOwner)
	accepted := f.accept(routeTestOwner, conversationID, "rec-downgrade-cmid-1", "问题")
	oversized := strings.Repeat("超", AssistantStorageReservationBytes) // 每字 3 字节，远超预留

	result, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID:     conversationID,
		SystemAccountID:    routeTestOwner,
		ExpectedTurnID:     accepted.TurnID,
		InterruptedContent: oversized,
		Now:                f.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusFailed {
		t.Fatalf("超预留降级仍应有界收口为 failed: %+v", result)
	}
	status, content := streamAssistantState(t, f, accepted.TurnID)
	if status != string(StatusFailed) || content != "" {
		t.Fatalf("超预留应降级为空内容: status=%q content_len=%d", status, len(content))
	}
	var errorCode string
	if err := f.db.QueryRow(`SELECT error_code FROM chat_messages
		WHERE turn_id = ? AND role = 'assistant'`, accepted.TurnID).Scan(&errorCode); err != nil {
		t.Fatal(err)
	}
	if errorCode != "chat_assistant_storage_limit_exceeded" {
		t.Fatalf("超预留降级错误码不符: %q", errorCode)
	}
}

// TestRecoverChatTurnFinalizationPersistsContent 路由级恢复入口携带正文：
// streaming 轮恢复收口为 failed 且正文落库，并满足 §14.1 尾部注入条件。
func TestRecoverChatTurnFinalizationPersistsContent(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_rec_route"
	env.fixture.createConversation(conversationID, routeTestOwner)
	accepted := env.fixture.accept(routeTestOwner, conversationID, "rec-route-cmid-1", "问题")
	rt := newChatRoutesForTest(env.deps)

	status := rt.recoverChatTurnFinalization(conversationID, routeTestOwner, accepted.TurnID, "rec-route-cmid-1", "恢复半截正文", errors.New("boom"))
	if status != string(StatusFailed) {
		t.Fatalf("streaming 轮恢复应返回 failed: %s", status)
	}
	restoreStatus, content := streamAssistantState(t, env.fixture, accepted.TurnID)
	if restoreStatus != string(StatusFailed) || content != "恢复半截正文" {
		t.Fatalf("恢复收口应落库部分正文: status=%q content=%q", restoreStatus, content)
	}

	// §14.1 联动：恢复收口后的 failed 轮（有正文）作为尾部中断轮注入上下文。
	loaded, err := env.fixture.store.LoadModelContext(conversationID, routeTestOwner, env.fixture.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	roles := suffixRoles(t, loaded)
	if len(roles) != 2 || strings.Join(roles, ",") != "user,assistant" {
		t.Fatalf("恢复收口轮应注入 suffix: %v", roles)
	}
	if loaded.Suffix[0].contentText != "问题" || loaded.Suffix[1].contentText != "恢复半截正文" {
		t.Fatalf("尾部注入内容不正确: %q / %q", loaded.Suffix[0].contentText, loaded.Suffix[1].contentText)
	}
}

// TestStreamCompleteFailureRecoveryPersistsContent 端到端：成功路径
// CompleteChatTurn 终结 UPDATE 失败一次 → 恢复收口按部分正文落库（不再丢弃
// 已生成内容）；随后发送「继续」，§14.1 尾部中断轮进入上游 history。
func TestStreamCompleteFailureRecoveryPersistsContent(t *testing.T) {
	env, script := newFaultGenerationEnvW10D(t)
	conversationID := "chat_conv_rec_e2e"
	env.fixture.createConversation(conversationID, routeTestOwner)
	env.executor.steps = append(env.executor.steps,
		scriptStep{
			match: func(call dispatchCall) bool {
				return call.Path == "/v1/chat/completions" && !strings.Contains(call.Body, "继续")
			},
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(chatCompletionsSSE("恢复前已生成的半截回答", true))
			},
		},
		scriptStep{
			match: func(call dispatchCall) bool {
				return call.Path == "/v1/chat/completions" && strings.Contains(call.Body, "继续")
			},
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(chatCompletionsSSE("继续后的新回答", true))
			},
		},
	)
	// CompleteChatTurn 的终结 UPDATE 失败一次 → recoverChatTurnFinalization 兜底。
	script.failOnce("SET status = ?, content_text = ?, content_blocks_json = ?, content_bytes = ?, trace_id = ?")
	rt := newChatRoutesForTest(env.deps)

	recorder := w14lStreamPostRaw(rt, conversationID, streamPayload("rec-e2e-cmid-1", "帮我写一段长文", "gpt-5"), routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.failed") {
		t.Fatalf("首次发送应以 failed 终态收口: %d %s", recorder.Code, body[:min(len(body), 300)])
	}
	turnID := streamTurnIDByClientMessageID(t, env.fixture, conversationID, "rec-e2e-cmid-1")
	status, content := streamAssistantState(t, env.fixture, turnID)
	if status != string(StatusFailed) || content != "恢复前已生成的半截回答" {
		t.Fatalf("恢复收口应保留已生成正文: status=%q content=%q", status, content)
	}

	// 继续发送：恢复收口的 failed 轮（有正文）作为尾部中断轮进入上游 history。
	recorder2 := w14lStreamPostRaw(rt, conversationID, streamPayload("rec-e2e-cmid-2", "继续", "gpt-5"), routeTestOwner)
	body2 := recorder2.Body.String()
	if recorder2.Code != http.StatusOK || !strings.Contains(body2, "message.completed") {
		t.Fatalf("继续发送应成功完成: %d %s", recorder2.Code, body2[:min(len(body2), 300)])
	}
	if env.executor.callCount() < 2 {
		t.Fatalf("上游请求缺失: %d", env.executor.callCount())
	}
	messages := decodeUpstreamMessages(t, env.executor.calls[1].Body)
	foundQuestion, foundPartial, foundContinue := false, false, false
	for _, message := range messages {
		switch message["content"] {
		case "帮我写一段长文":
			foundQuestion = true
		case "恢复前已生成的半截回答":
			foundPartial = true
		case "继续":
			foundContinue = true
		}
	}
	if !foundQuestion || !foundPartial || !foundContinue {
		t.Fatalf("上游 history 应含恢复收口轮的提问与半截回答: %+v", messages)
	}
	if messages[len(messages)-1]["content"] != "继续" {
		t.Fatalf("新用户消息应在最后: %v", messages[len(messages)-1])
	}
}
