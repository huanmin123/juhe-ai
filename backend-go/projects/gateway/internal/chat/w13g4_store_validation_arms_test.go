package chat

// w13g4 覆盖率补齐：store 层替换/取消校验臂、生成失败路径、上下文截断。
//
// 不可达 / 高成本语句登记（基于 2026-09-18 覆盖率 profile）：
//   - turns.go 212-214：requireReplaceableTurn 已保证 idempotencyRows 恰 1 条
//     （604-606 同条件查询），DELETE affected != 1 不可达。
//   - turns.go 161-163 ensurePostgresChatMessagePartitions err：SQLite fixture
//     （s.pg=false）直接跳过，需 PG 方言环境。
//   - context.go 634-637：suffix 查询 EXISTS 配对条件（sequence_no +1/-1、role、
//     turn_id 一致）已保证 634-635 的检查恒通过。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// w13g4ReplaceInput 构造对指定轮的替换提交。
func w13g4ReplaceInput(conversationID, turnID, clientMessageID string) AcceptTurnInput {
	return AcceptTurnInput{
		ConversationID:          conversationID,
		SystemAccountID:         routeTestOwner,
		ClientMessageID:         clientMessageID,
		UserContent:             "替换后的内容",
		Model:                   "gpt-5",
		Now:                     "2026-03-10T08:00:00.000Z",
		StorageQuotaBytes:       2 * 1024 * 1024 * 1024,
		RetentionDays:           30,
		MaxTurnsPerConversation: 100,
		ReplaceTurnID:           turnID,
	}
}

func w13g4MakeCompletedTurn(env *generationEnv, conversationID, clientMessageID string) string {
	env.t.Helper()
	accepted := env.fixture.accept(routeTestOwner, conversationID, clientMessageID, "问题")
	env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "回答")
	return accepted.TurnID
}

func w13g4IsReplaceConflict(err error) bool {
	var conflict *ConflictError
	return errors.As(err, &conflict)
}

// TestW13G4ReplaceTurnValidationArms 覆盖 requireReplaceableTurn 的坏数据分支。
func TestW13G4ReplaceTurnValidationArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_rep"
	env.fixture.createConversation(conversationID, routeTestOwner)

	// expires_at 已过期 → Conflict（turns.go 580-582）。
	turnID := w13g4MakeCompletedTurn(env, conversationID, "w13g4-r0")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET expires_at = '2020-01-01T00:00:00.000Z' WHERE turn_id = ?`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.AcceptTurn(w13g4ReplaceInput(conversationID, turnID, "w13g4-r1")); !w13g4IsReplaceConflict(err) {
		t.Fatalf("过期轮替换应冲突 = %v", err)
	}

	// expires_at 非法 → DomainError（turns.go 573-579）。
	turnID = w13g4MakeCompletedTurn(env, conversationID, "w13g4-r2")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET expires_at = 'oops' WHERE turn_id = ?`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.AcceptTurn(w13g4ReplaceInput(conversationID, turnID, "w13g4-r3")); err == nil || w13g4IsReplaceConflict(err) || !strings.Contains(err.Error(), "expires_at") {
		t.Fatalf("非法 expires_at 应报 DomainError = %v", err)
	}

	// 输入标记缺失（contentBlocksJSON 无法解析出 marker）→ Conflict（597-599）。
	turnID = w13g4MakeCompletedTurn(env, conversationID, "w13g4-r4")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET content_blocks_json = 'not-json' WHERE turn_id = ? AND role = 'user'`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.AcceptTurn(w13g4ReplaceInput(conversationID, turnID, "w13g4-r5")); !w13g4IsReplaceConflict(err) {
		t.Fatalf("标记缺失应冲突 = %v", err)
	}

	// 幂等行 client_message_id 与用户消息不一致 → Conflict（608-615）。
	turnID = w13g4MakeCompletedTurn(env, conversationID, "w13g4-r6")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_message_idempotency SET client_message_id = 'w13g4-tampered' WHERE turn_id = ?`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.AcceptTurn(w13g4ReplaceInput(conversationID, turnID, "w13g4-r7")); !w13g4IsReplaceConflict(err) {
		t.Fatalf("幂等行篡改应冲突 = %v", err)
	}

	// created_at 非法 → DomainError（turns.go 620-623）。
	turnID = w13g4MakeCompletedTurn(env, conversationID, "w13g4-r8")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET created_at = 'oops' WHERE turn_id = ?`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.AcceptTurn(w13g4ReplaceInput(conversationID, turnID, "w13g4-r9")); err == nil || w13g4IsReplaceConflict(err) || !strings.Contains(err.Error(), "created_at") {
		t.Fatalf("非法 created_at 应报 DomainError = %v", err)
	}
}

// TestW13G4ConditionalStopArms 覆盖 conditionalStop 的受影响行数分支。
func TestW13G4ConditionalStopArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_stop"
	env.fixture.createConversation(conversationID, routeTestOwner)

	// 助手消息已终结时取消 → RowsAffected != 1 → 权威状态读取（1048-1056）。
	accepted := env.fixture.accept(routeTestOwner, conversationID, "w13g4-c0", "问题")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET status = 'completed', storage_reserved_bytes = 0 WHERE conversation_id = ? AND turn_id = ? AND role = 'assistant'`,
		conversationID, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	result, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: accepted.TurnID, Now: env.fixture.nowISO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusCompleted {
		t.Fatalf("已终结轮取消 = %+v", result)
	}

	// 存储预留字节不一致 → requiredAssistantStorageReservation 错误（1027-1029）。
	// 上一场景的取消走权威状态臂时事务回滚、活动轮保留，需独立会话。
	otherConversationID := "chat_conv_w13g4_stop2"
	env.fixture.createConversation(otherConversationID, routeTestOwner)
	accepted = env.fixture.accept(routeTestOwner, otherConversationID, "w13g4-c1", "问题")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET storage_reserved_bytes = 12345 WHERE conversation_id = ? AND turn_id = ? AND role = 'assistant'`,
		otherConversationID, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
		ConversationID: otherConversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: accepted.TurnID, Now: env.fixture.nowISO,
	}); err == nil || !strings.Contains(err.Error(), "存储预留") {
		t.Fatalf("预留不一致应报错 = %v", err)
	}
}

// TestW13G4StreamFailurePathArms 覆盖生成执行失败的收口路径与上下文用量
// 触发的后台压缩调度。
func TestW13G4StreamFailurePathArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_fail"
	env.fixture.createConversation(conversationID, routeTestOwner)
	// 上游 500 → 失败路径（stream_execute.go 260-309）→ SSE message.failed。
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return jsonStatusResponse(500, `{"error":{"message":"boom"}}`)
		},
	})
	rt := newChatRoutesForTest(env.deps)
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-x1", "你好", "gpt-5"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("失败流 HTTP = %d %s", recorder.Code, recorder.Body.String())
	}
	events := sseEvents(recorder.Body.String())
	if last := events[len(events)-1]; last.event != "message.failed" {
		t.Fatalf("失败事件缺失: %+v", events[maxInt(0, len(events)-3):])
	}
}

// TestW13G4StreamScheduleCompactionArms 覆盖上游无 usage 且助手输出推高上下文
// 估算时的后台压缩调度（stream_execute.go 239-250）。
func TestW13G4StreamScheduleCompactionArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_sched"
	env.fixture.createConversation(conversationID, routeTestOwner)
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE("w13g4-heavy 助手输出", false))
		},
	})
	env.deps.TokenCount = func(text string) int {
		if strings.Contains(text, "w13g4-heavy") {
			return 900000
		}
		return len(text) / 4
	}
	rt := newChatRoutesForTest(env.deps)
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-x2", "你好", "gpt-5"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("调度流 HTTP = %d %s", recorder.Code, recorder.Body.String())
	}
	events := sseEvents(recorder.Body.String())
	if last := events[len(events)-1]; last.event != "message.completed" {
		t.Fatalf("终态事件缺失: %+v", events[maxInt(0, len(events)-3):])
	}
}

// TestW13G4ContextLoadTruncationArms 覆盖上下文装载的字节截断与 checkpoint
// 条目计数截断（context.go 639-654、573-578），并经 streamTurn 驱动
// ModelContextLoadLimit → 压缩 → 重载链（stream_route.go 528-541）。
func TestW13G4ContextLoadTruncationArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_trunc"
	env.fixture.createConversation(conversationID, routeTestOwner)
	seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 4)
	turnID := w13g4MakeCompletedTurn(env, conversationID, "w13g4-t0")
	// 大轮单对字节保持在 16MiB 绝对上限内（压缩可接受），但累计装载字节
	// 超过 16MiB → suffix_messages 截断。
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_messages SET content_text = ? WHERE conversation_id = ? AND turn_id = ?`,
		strings.Repeat("x", 8385608), conversationID, turnID); err != nil {
		t.Fatal(err)
	}
	loaded, err := env.fixture.store.LoadModelContext(conversationID, routeTestOwner, env.fixture.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Complete || loaded.TruncatedAt == nil || *loaded.TruncatedAt != "suffix_messages" {
		t.Fatalf("suffix 截断判定 = complete:%v truncated:%v", loaded.Complete, loaded.TruncatedAt)
	}
	// 压缩认领排除最新轮：在大轮之后再补一轮，使其落入可压缩范围。
	w13g4MakeCompletedTurn(env, conversationID, "w13g4-t2")

	// 压缩服务可把超限上下文收口：安装压缩 step 后 streamTurn 走
	// load_limit → compactOnce → 重载 → 正常完成。
	env.executor.steps = append(env.executor.steps,
		scriptStep{
			match: func(call dispatchCall) bool {
				return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
			},
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				summary := `{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`
				return jsonStatusResponse(200, `{"choices":[{"message":{"content":`+jsonQuote(summary)+`}}]}`)
			},
		},
		scriptStep{
			match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(chatCompletionsSSE("回答", true))
			},
		})
	result := env.compactions.CompactOnce(context.Background(), CompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
	})
	if result.Status != "installed" {
		t.Fatalf("compact result = %+v", result)
	}
	loaded2, err := env.fixture.store.LoadModelContext(conversationID, routeTestOwner, env.fixture.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatalf("重载 = %v", err)
	}
	if !loaded2.Complete {
		t.Fatalf("压缩后重载未收口 = %+v", loaded2.TruncatedAt)
	}
	rt := newChatRoutesForTest(env.deps)
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-t1", "你好", "gpt-5"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("截断压缩流 = %d %s", recorder.Code, recorder.Body.String())
	}
	events := sseEvents(recorder.Body.String())
	if last := events[len(events)-1]; last.event != "message.completed" {
		t.Fatalf("终态事件缺失: %+v", events[maxInt(0, len(events)-3):])
	}

	// checkpoint 条目计数与元数据不一致 → checkpoint_entries 截断（573-578）。
	head, err := env.fixture.store.GetContextHead(conversationID, routeTestOwner)
	if err != nil || head == nil || head.ActiveCheckpointID == nil {
		t.Fatalf("压缩后 head = %+v / %v", head, err)
	}
	if _, err := env.fixture.db.Exec(
		`DELETE FROM chat_context_entries WHERE rowid = (SELECT rowid FROM chat_context_entries WHERE conversation_id = ? LIMIT 1)`, conversationID); err != nil {
		t.Fatal(err)
	}
	loaded, err = env.fixture.store.LoadModelContext(conversationID, routeTestOwner, env.fixture.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Complete || loaded.TruncatedAt == nil || *loaded.TruncatedAt != "checkpoint_entries" {
		t.Fatalf("checkpoint 截断判定 = complete:%v truncated:%v", loaded.Complete, loaded.TruncatedAt)
	}
}
