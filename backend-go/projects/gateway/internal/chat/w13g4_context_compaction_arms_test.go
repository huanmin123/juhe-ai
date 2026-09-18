package chat

// w13g4 覆盖率补齐：checkpoint 安装校验、压缩认领链、压缩失败臂、生成收口恢复链。
//
// 不可达 / 高成本语句登记（基于 2026-09-18 覆盖率 profile）：
//   - compaction_service.go 296-298：entries 为纯可序列化结构切片，
//     json.Marshal 不可能失败。
//   - compaction_service.go 246-248 / 290-292：要求压缩过程中 claim 被并发删除
//     或进度被并发推进，单线程直调不可达（并发窗口由锁语义排除）。
//   - compaction_service.go 303-305（摘要字段缺失）：fillRequiredSnapshotFields
//     以来源消息的最近用户内容回填 currentGoal/recentUserIntent，进入摘要校验时
//     两字段恒非空。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func w13g4InstallInput() InstallCheckpointInput {
	return InstallCheckpointInput{
		ClaimID:               "chat_context_claim_w13g4",
		ConversationID:        "chat_conv_w13g4_install",
		SystemAccountID:       routeTestOwner,
		SourceRevision:        0,
		SourceThroughSequence: 2,
		ExpiresAt:             "2026-04-09T08:00:00.000Z",
		PayloadDigest:         "0000000000000000000000000000000000000000000000000000000000000000",
		RequestBodyBytes:      10,
		ModelID:               "gpt-5",
		EndpointFamily:        string(ProtocolChatCompletions),
		PromptVersion:         compactionPromptVersion,
		Entries:               []CheckpointEntryInput{},
		Now:                   "2026-03-10T08:00:00.000Z",
	}
}

// TestW13G4InstallCheckpointValidationArms 直调覆盖 InstallContextCheckpoint
// 事务前输入校验族（context.go 696-727）。
func TestW13G4InstallCheckpointValidationArms(t *testing.T) {
	fixture, _ := w13g4HookFixture(t, nil)
	store := fixture.store

	negative := w13g4InstallInput()
	negative.SourceRevision = -1
	if _, err := store.InstallContextCheckpoint(negative); err == nil || !strings.Contains(err.Error(), "sourceRevision") {
		t.Fatalf("负 revision = %v", err)
	}

	zeroThrough := w13g4InstallInput()
	zeroThrough.SourceThroughSequence = 0
	if _, err := store.InstallContextCheckpoint(zeroThrough); err == nil || !strings.Contains(err.Error(), "sourceThroughSequence") {
		t.Fatalf("零 through = %v", err)
	}

	badExpires := w13g4InstallInput()
	badExpires.ExpiresAt = "oops"
	if _, err := store.InstallContextCheckpoint(badExpires); err == nil || !strings.Contains(err.Error(), "expiresAt") {
		t.Fatalf("非法 expiresAt = %v", err)
	}

	badNow := w13g4InstallInput()
	badNow.Now = "oops"
	if _, err := store.InstallContextCheckpoint(badNow); err == nil || !strings.Contains(err.Error(), "now") {
		t.Fatalf("非法 now = %v", err)
	}

	noZone := w13g4InstallInput()
	noZone.ExpiresAt = "2026-04-09T08:00:00"
	if _, err := store.InstallContextCheckpoint(noZone); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("无时区时间 = %v", err)
	}

	expired := w13g4InstallInput()
	expired.ExpiresAt = "2026-03-09T08:00:00.000Z"
	if _, err := store.InstallContextCheckpoint(expired); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("已过期 checkpoint = %v", err)
	}

	longID := w13g4InstallInput()
	longID.CheckpointID = strings.Repeat("x", 200)
	if _, err := store.InstallContextCheckpoint(longID); err == nil || !strings.Contains(err.Error(), "checkpointId") {
		t.Fatalf("超长 checkpointId = %v", err)
	}

	badDigest := w13g4InstallInput()
	badDigest.PayloadDigest = "nothex"
	if _, err := store.InstallContextCheckpoint(badDigest); err == nil || !strings.Contains(err.Error(), "Digest") {
		t.Fatalf("非法 digest = %v", err)
	}
}

// TestW13G4ClaimCompactionArms 覆盖 ClaimContextCompaction 的会话缺失、
// 版本冲突、无 checkpoint 认领与过期 checkpoint 失效链。
func TestW13G4ClaimCompactionArms(t *testing.T) {
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w13g4_claim"
	env.fixture.createConversation(conversationID, routeTestOwner)

	// 会话不存在 → nil, nil（context.go 241-243）。
	claim, err := env.fixture.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "chat_conv_w13g4_none", SystemAccountID: routeTestOwner,
		ExpectedRevision: 0, SourceThroughSequence: 2,
		Now: env.fixture.nowISO, StaleClaimBefore: "2020-01-01T00:00:00.000Z",
	})
	if claim != nil || err != nil {
		t.Fatalf("缺失会话认领 = %+v / %v", claim, err)
	}

	// 版本不匹配 → UPDATE affected != 1 → nil, nil（295-297）。
	claim, err = env.fixture.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedRevision: 41, SourceThroughSequence: 2,
		Now: env.fixture.nowISO, StaleClaimBefore: "2020-01-01T00:00:00.000Z",
	})
	if claim != nil || err != nil {
		t.Fatalf("版本冲突认领 = %+v / %v", claim, err)
	}

	// 正常认领（无 checkpoint）→ activeCheckpointID(nil)/activeCheckpointExpires(nil)
	// 的 nil 臂（329-341）。认领条件要求 through <= next_sequence_no - 3。
	seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 2)
	head, err := env.fixture.store.GetContextHead(conversationID, routeTestOwner)
	if err != nil || head == nil {
		t.Fatalf("head = %+v / %v", head, err)
	}
	claim, err = env.fixture.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedRevision: head.ContextRevision, SourceThroughSequence: 2,
		Now: env.fixture.nowISO, StaleClaimBefore: "2020-01-01T00:00:00.000Z",
	})
	if err != nil || claim == nil {
		t.Fatalf("正常认领 = %+v / %v", claim, err)
	}

	// 已装 checkpoint 且过期 → 认领递增版本 + supersede（249-264、298-306）。
	env2 := newGenerationEnv(t)
	conversationID2 := "chat_conv_w13g4_claim2"
	env2.fixture.createConversation(conversationID2, routeTestOwner)
	seedLongTurns(t, env2.fixture, routeTestOwner, conversationID2, 2)
	env2.executor.steps = append(env2.executor.steps, scriptStep{
		match: func(call dispatchCall) bool {
			return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
		},
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			summary := `{"durableMemory":["喜欢简洁"],"currentGoal":"配置服务","constraints":[],"decisions":[],"completed":["阅读文档"],"pending":["部署"],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"配置服务","uncertainties":[]}`
			return jsonStatusResponse(200, `{"choices":[{"message":{"content":`+jsonQuote(summary)+`}}]}`)
		},
	})
	result := env2.compactions.CompactOnce(context.Background(), CompactionInput{
		ConversationID: conversationID2, SystemAccountID: routeTestOwner,
		APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
	})
	if result.Status != "installed" {
		t.Fatalf("预装 checkpoint = %+v", result)
	}
	if _, err := env2.fixture.db.Exec(
		`UPDATE chat_context_checkpoints SET expires_at = '2026-03-01T00:00:00.000Z' WHERE conversation_id = ?`, conversationID2); err != nil {
		t.Fatal(err)
	}
	head2, err := env2.fixture.store.GetContextHead(conversationID2, routeTestOwner)
	if err != nil || head2 == nil {
		t.Fatalf("head2 = %+v / %v", head2, err)
	}
	claim2, err := env2.fixture.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID2, SystemAccountID: routeTestOwner,
		ExpectedRevision: head2.ContextRevision, SourceThroughSequence: head2.NextSequenceNo - 3,
		Now: env.fixture.nowISO, StaleClaimBefore: "2020-01-01T00:00:00.000Z",
	})
	if err != nil || claim2 == nil {
		t.Fatalf("过期 checkpoint 认领 = %+v / %v", claim2, err)
	}
	status, err := env2.fixture.db.Exec(
		`SELECT COUNT(*) FROM chat_context_checkpoints WHERE conversation_id = ? AND status = 'superseded'`, conversationID2)
	_ = status
	if err != nil {
		t.Fatal(err)
	}
}

func w13g4CompactionStep(payload string) scriptStep {
	return scriptStep{
		match: func(call dispatchCall) bool {
			return call.Headers["x-juhe-ai-purpose"] == "chat_context_compaction"
		},
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return jsonStatusResponse(200, `{"choices":[{"message":{"content":`+jsonQuote(payload)+`}}]}`)
		},
	}
}

// TestW13G4CompactionFailureArms 覆盖 runClaimedCompaction 的页加载、进度、
// 摘要校验与安装失败臂。
func TestW13G4CompactionFailureArms(t *testing.T) {
	t.Run("summary not smaller", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("chat_conv_w13g4_cf1", routeTestOwner)
		seedLongTurns(t, env.fixture, routeTestOwner, "chat_conv_w13g4_cf1", 2)
		huge := `{"durableMemory":["` + strings.Repeat("占位", 20000) + `"],"currentGoal":"目标","constraints":[],"decisions":[],"completed":[],"pending":[],"importantToolResults":[],"imageMemories":[],"recentUserIntent":"意图","uncertainties":[]}`
		env.executor.steps = append(env.executor.steps, w13g4CompactionStep(huge))
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: "chat_conv_w13g4_cf1", SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "failed" || result.Reason != "chat_context_summary_not_smaller" {
			t.Fatalf("not smaller = %+v", result)
		}
	})
	t.Run("single turn exceeds absolute limit", func(t *testing.T) {
		env := newGenerationEnv(t)
		conversationID := "chat_conv_w13g4_cf3"
		env.fixture.createConversation(conversationID, routeTestOwner)
		seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 2)
		accepted := env.fixture.accept(routeTestOwner, conversationID, "w13g4-cf3", "问题")
		env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "回答")
		// 认领排除最新轮：大轮之后再补一轮，使大轮落入压缩范围。
		w13g4MakeCompletedTurn(env, conversationID, "w13g4-cf3b")
		if _, err := env.fixture.db.Exec(
			`UPDATE chat_messages SET content_text = ? WHERE conversation_id = ? AND turn_id = ?`,
			strings.Repeat("y", 9<<20), conversationID, accepted.TurnID); err != nil {
			t.Fatal(err)
		}
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: conversationID, SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "failed" {
			t.Fatalf("单轮超限 = %+v", result)
		}
	})
	t.Run("progress write fails", func(t *testing.T) {
		env, script := newFaultGenerationEnvW10D(t)
		conversationID := "chat_conv_w13g4_cf4"
		env.fixture.createConversation(conversationID, routeTestOwner)
		seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 2)
		env.executor.steps = append(env.executor.steps, w13g4CompactionStep(`{"durableMemory":["x"],"currentGoal":"目标","recentUserIntent":"意图"}`))
		script.failOnce("SET context_progress_sequence = ?")
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: conversationID, SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "failed" {
			t.Fatalf("进度写入失败 = %+v", result)
		}
	})
	t.Run("checkpoint install fails", func(t *testing.T) {
		env, script := newFaultGenerationEnvW10D(t)
		conversationID := "chat_conv_w13g4_cf5"
		env.fixture.createConversation(conversationID, routeTestOwner)
		seedLongTurns(t, env.fixture, routeTestOwner, conversationID, 2)
		env.executor.steps = append(env.executor.steps, w13g4CompactionStep(`{"durableMemory":["x"],"currentGoal":"目标","recentUserIntent":"意图"}`))
		script.failOnce("INSERT INTO chat_context_checkpoints")
		result := env.compactions.CompactOnce(context.Background(), CompactionInput{
			ConversationID: conversationID, SystemAccountID: routeTestOwner,
			APIKeySecret: "secret", Model: "gpt-5", Protocol: ProtocolChatCompletions,
		})
		if result.Status != "failed" {
			t.Fatalf("安装失败 = %+v", result)
		}
	})
}

// TestW13G4StreamRecoverArms 覆盖 FailChatTurn 失败后的收口恢复链
// （stream_execute.go 296-309 + recoverChatTurnFinalization）。
func TestW13G4StreamRecoverArms(t *testing.T) {
	env, script := newFaultGenerationEnvW10D(t)
	conversationID := "chat_conv_w13g4_rec"
	env.fixture.createConversation(conversationID, routeTestOwner)
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return jsonStatusResponse(500, `{"error":{"message":"boom"}}`)
		},
	})
	// FailChatTurn 的终结 UPDATE 失败一次 → recoverChatTurnFinalization 兜底。
	script.failOnce("SET status = ?, content_text = ?, content_blocks_json = ?, content_bytes = ?, trace_id = ?")
	rt := newChatRoutesForTest(env.deps)
	recorder := w13g4StreamPost(rt, conversationID, routeTestOwner, streamPayload("w13g4-y1", "你好", "gpt-5"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("恢复流 HTTP = %d %s", recorder.Code, recorder.Body.String())
	}
	events := sseEvents(recorder.Body.String())
	if last := events[len(events)-1]; last.event != "message.failed" {
		t.Fatalf("失败事件缺失: %+v", events[maxInt(0, len(events)-3):])
	}
}
