package chat

// 尾部中断轮进入上下文（AI 问答设计 §14.1）回归：会话尾部收口为 failed 或
// canceled 的轮次（用户提问 + 半截回答，保留非空内容）进入下一次模型请求
// 上下文，使「继续」可以续上；空内容（含未保留正文的兜底取消）、无提问
// 配对、非尾部与 replace 命中轮次不进入。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// seedCompletedTurn 落一轮 completed 轮次（store 接受 + 完成收口）。
func seedCompletedTurn(f *chatFixture, conversationID, clientMessageID, question, answer string) string {
	f.t.Helper()
	accepted := f.accept(routeTestOwner, conversationID, clientMessageID, question)
	f.complete(routeTestOwner, conversationID, accepted.TurnID, answer)
	return accepted.TurnID
}

// seedFailedTurn 落一轮 failed 轮次（模拟上游流中断后的半截回答落库）。
func seedFailedTurn(f *chatFixture, conversationID, clientMessageID, question, partialAnswer string) string {
	f.t.Helper()
	accepted := f.accept(routeTestOwner, conversationID, clientMessageID, question)
	if _, err := f.store.FailChatTurn(FailTurnInput{
		ConversationID:   conversationID,
		SystemAccountID:  routeTestOwner,
		TurnID:           accepted.TurnID,
		AssistantContent: partialAnswer,
		ErrorCode:        "upstream_stream_failed",
		ErrorMessage:     "模型响应中断",
		Now:              f.nowISO,
	}); err != nil {
		f.t.Fatal(err)
	}
	return accepted.TurnID
}

// seedCanceledTurn 落一轮用户主动停止的 canceled 轮次（正常停止路径携带半截
// 内容落库），返回轮次 ID。
func seedCanceledTurn(f *chatFixture, conversationID, clientMessageID, question, partialAnswer string) string {
	f.t.Helper()
	accepted := f.accept(routeTestOwner, conversationID, clientMessageID, question)
	if _, err := f.store.CancelChatTurn(CancelTurnInput{
		ConversationID:   conversationID,
		SystemAccountID:  routeTestOwner,
		TurnID:           accepted.TurnID,
		AssistantContent: partialAnswer,
		Now:              f.nowISO,
	}); err != nil {
		f.t.Fatal(err)
	}
	return accepted.TurnID
}

// insertBareAssistant 绕过轮次链路直插一条指定状态的 assistant 消息（无配对
// 用户提问；流式占位等构造专用，streaming 行按 CHECK 约束带预留字节）。
func insertBareAssistant(t *testing.T, f *chatFixture, conversationID, messageID, turnID string, sequenceNo int64, status, content string) {
	t.Helper()
	reserved := int64(0)
	if status == "streaming" {
		reserved = 448 * 1024
	}
	if _, err := f.db.Exec(`INSERT INTO chat_messages (id, conversation_id, system_account_id, turn_id,
		sequence_no, role, status, content_text, content_blocks_json, content_bytes, storage_reserved_bytes, model, created_at, completed_at, expires_at)
		VALUES (?, ?, ?, ?, ?, 'assistant', ?, ?, '[]', ?, ?, 'gpt-5', ?, ?, ?)`,
		messageID, conversationID, routeTestOwner, turnID, sequenceNo, status, content,
		len(content), reserved, f.nowISO, f.nowISO, "2027-03-10T08:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
}

// suffixRoles 提取 suffix 的 role 序列，便于断言注入位置与顺序。
func suffixRoles(t *testing.T, loaded *ModelContextLoadResult) []string {
	t.Helper()
	roles := make([]string, 0, len(loaded.Suffix))
	for _, message := range loaded.Suffix {
		roles = append(roles, message.role)
	}
	return roles
}

// TestLoadModelContextTailFailedTurnInjected 尾部 failed 轮（user completed +
// assistant failed 有内容）注入 suffix 末尾，顺序为 [...completed 轮, user 原
// 问题, assistant 半截]。
func TestLoadModelContextTailFailedTurnInjected(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_ok", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_ok", "tail-cmid-1", "问题1", "回答1")
	failedTurnID := seedFailedTurn(f, "chat_conv_tail_ok", "tail-cmid-2", "帮我写一段长文", "开头半截内容")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_ok", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || !loaded.Complete || loaded.TruncatedAt != nil {
		t.Fatalf("装载应保持完整: %+v", loaded)
	}
	roles := suffixRoles(t, loaded)
	if len(roles) != 4 || strings.Join(roles, ",") != "user,assistant,user,assistant" {
		t.Fatalf("suffix 应为两对且尾部为中断轮: %v", roles)
	}
	tailUser := loaded.Suffix[2]
	tailAssistant := loaded.Suffix[3]
	if tailUser.contentText != "帮我写一段长文" || tailAssistant.contentText != "开头半截内容" {
		t.Fatalf("尾部中断轮内容不正确: %q / %q", tailUser.contentText, tailAssistant.contentText)
	}
	if tailUser.turnID != failedTurnID || tailAssistant.turnID != failedTurnID {
		t.Fatalf("尾部中断轮 turn_id 不正确: %q / %q", tailUser.turnID, tailAssistant.turnID)
	}
	if tailAssistant.sequenceNo != tailUser.sequenceNo+1 || tailUser.role != "user" || tailAssistant.role != "assistant" {
		t.Fatalf("尾部中断轮顺序不正确: %d/%d", tailUser.sequenceNo, tailAssistant.sequenceNo)
	}
}

// TestLoadModelContextTailFailedTurnCoveredByLaterTurn failed 轮之后存在更新的
// completed 轮次（非尾部）→ 不注入。
func TestLoadModelContextTailFailedTurnCoveredByLaterTurn(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_covered", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_covered", "tail-cmid-1", "问题1", "回答1")
	seedFailedTurn(f, "chat_conv_tail_covered", "tail-cmid-2", "中断的问题", "半截回答")
	seedCompletedTurn(f, "chat_conv_tail_covered", "tail-cmid-3", "问题2", "回答2")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_covered", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range loaded.Suffix {
		if message.contentText == "中断的问题" || message.contentText == "半截回答" {
			t.Fatalf("非尾部失败轮不应注入: %+v", loaded.Suffix)
		}
	}
	roles := suffixRoles(t, loaded)
	if len(roles) != 4 {
		t.Fatalf("suffix 应只含两个 completed 轮次: %v", roles)
	}
}

// TestLoadModelContextTailCanceledTurnInjected 用户主动停止的尾部轮次
// （canceled 且保留半截内容）与 failed 轮同判据注入 suffix 末尾，顺序为
// [...completed 轮, user 原问题, assistant 半截]。
func TestLoadModelContextTailCanceledTurnInjected(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_cancel", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_cancel", "tail-cmid-1", "问题1", "回答1")
	canceledTurnID := seedCanceledTurn(f, "chat_conv_tail_cancel", "tail-cmid-2", "被停止的问题", "被停止的半截")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_cancel", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || !loaded.Complete || loaded.TruncatedAt != nil {
		t.Fatalf("装载应保持完整: %+v", loaded)
	}
	roles := suffixRoles(t, loaded)
	if len(roles) != 4 || strings.Join(roles, ",") != "user,assistant,user,assistant" {
		t.Fatalf("suffix 应为两对且尾部为中断轮: %v", roles)
	}
	tailUser := loaded.Suffix[2]
	tailAssistant := loaded.Suffix[3]
	if tailUser.contentText != "被停止的问题" || tailAssistant.contentText != "被停止的半截" {
		t.Fatalf("尾部中断轮内容不正确: %q / %q", tailUser.contentText, tailAssistant.contentText)
	}
	if tailUser.turnID != canceledTurnID || tailAssistant.turnID != canceledTurnID {
		t.Fatalf("尾部中断轮 turn_id 不正确: %q / %q", tailUser.turnID, tailAssistant.turnID)
	}
	if tailAssistant.sequenceNo != tailUser.sequenceNo+1 || tailUser.role != "user" || tailAssistant.role != "assistant" {
		t.Fatalf("尾部中断轮顺序不正确: %d/%d", tailUser.sequenceNo, tailAssistant.sequenceNo)
	}
}

// TestLoadModelContextTailCanceledEmptyContentNotInjected 空内容取消轮不注入
// （含 runner 不在 hub 时兜底取消不写内容的形态，由「内容非空」条件自然排除）。
func TestLoadModelContextTailCanceledEmptyContentNotInjected(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_cancel_empty", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_cancel_empty", "tail-cmid-1", "问题1", "回答1")
	seedCanceledTurn(f, "chat_conv_tail_cancel_empty", "tail-cmid-2", "空内容的停止轮", "")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_cancel_empty", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Suffix) != 2 {
		t.Fatalf("空内容取消轮不应注入: %v", suffixRoles(t, loaded))
	}
}

// TestLoadModelContextTailCanceledTurnCoveredByLaterTurn canceled 轮之后存在
// 更新的 completed 轮次（非尾部）→ 不注入。
func TestLoadModelContextTailCanceledTurnCoveredByLaterTurn(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_cancel_covered", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_cancel_covered", "tail-cmid-1", "问题1", "回答1")
	seedCanceledTurn(f, "chat_conv_tail_cancel_covered", "tail-cmid-2", "被停止的问题", "被停止的半截")
	seedCompletedTurn(f, "chat_conv_tail_cancel_covered", "tail-cmid-3", "问题2", "回答2")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_cancel_covered", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range loaded.Suffix {
		if message.contentText == "被停止的问题" || message.contentText == "被停止的半截" {
			t.Fatalf("非尾部取消轮不应注入: %+v", loaded.Suffix)
		}
	}
	roles := suffixRoles(t, loaded)
	if len(roles) != 4 {
		t.Fatalf("suffix 应只含两个 completed 轮次: %v", roles)
	}
}

// TestLoadModelContextTailFailedEmptyContentNotInjected 空内容失败轮不注入。
func TestLoadModelContextTailFailedEmptyContentNotInjected(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_empty", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_empty", "tail-cmid-1", "问题1", "回答1")
	seedFailedTurn(f, "chat_conv_tail_empty", "tail-cmid-2", "空内容的问题", "")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_empty", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Suffix) != 2 {
		t.Fatalf("空内容失败轮不应注入: %v", suffixRoles(t, loaded))
	}
}

// TestLoadModelContextTailFailedWithoutUserPairNotInjected 无紧邻 completed 用户
// 提问配对的失败轮不注入。
func TestLoadModelContextTailFailedWithoutUserPairNotInjected(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_nopair", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_nopair", "tail-cmid-1", "问题1", "回答1")
	insertBareAssistant(t, f, "chat_conv_tail_nopair", "chat_msg_tail_orphan", "chat_turn_orphan", 3, string(StatusFailed), "无配对的半截回答")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_nopair", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Suffix) != 2 {
		t.Fatalf("无配对失败轮不应注入: %v", suffixRoles(t, loaded))
	}
}

// TestLoadModelContextTailFailedTurnWithStreamingPlaceholder 存在更新的 streaming
// 占位（崩溃遗留/发送时序）时不阻断注入。
func TestLoadModelContextTailFailedTurnWithStreamingPlaceholder(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_streaming", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_streaming", "tail-cmid-1", "问题1", "回答1")
	seedFailedTurn(f, "chat_conv_tail_streaming", "tail-cmid-2", "中断的问题", "半截回答")
	insertBareAssistant(t, f, "chat_conv_tail_streaming", "chat_msg_tail_placeholder", "chat_turn_placeholder", 5, "streaming", "")

	loaded, err := f.store.LoadModelContext("chat_conv_tail_streaming", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	roles := suffixRoles(t, loaded)
	if len(roles) != 4 || strings.Join(roles, ",") != "user,assistant,user,assistant" {
		t.Fatalf("streaming 占位不应阻断尾部中断轮注入: %v", roles)
	}
	if loaded.Suffix[2].contentText != "中断的问题" || loaded.Suffix[3].contentText != "半截回答" {
		t.Fatalf("尾部中断轮内容不正确: %+v", loaded.Suffix[2:])
	}
}

// TestLoadModelContextExcludingTurnSkipsTailFailedTurn excludeTurnID 命中尾部
// 中断轮时不注入；未命中时照常注入。
func TestLoadModelContextExcludingTurnSkipsTailFailedTurn(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_exclude", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_exclude", "tail-cmid-1", "问题1", "回答1")
	failedTurnID := seedFailedTurn(f, "chat_conv_tail_exclude", "tail-cmid-2", "被替换的问题", "被替换的半截")

	excluded, err := f.store.LoadModelContextExcludingTurn("chat_conv_tail_exclude", routeTestOwner, f.nowISO, 512, 16*1024*1024, failedTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.Suffix) != 2 {
		t.Fatalf("replace 命中尾部中断轮时不应注入: %v", suffixRoles(t, excluded))
	}
	included, err := f.store.LoadModelContextExcludingTurn("chat_conv_tail_exclude", routeTestOwner, f.nowISO, 512, 16*1024*1024, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(included.Suffix) != 4 {
		t.Fatalf("未命中 excludeTurnID 时应照常注入: %v", suffixRoles(t, included))
	}
}

// TestLoadModelContextTailFailedTurnBudgetShortfall 行数或字节预算不足以容纳
// 完整两行时放弃注入且不报错；Complete/TruncatedAt 语义不变。
func TestLoadModelContextTailFailedTurnBudgetShortfall(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("chat_conv_tail_budget", routeTestOwner)
	seedCompletedTurn(f, "chat_conv_tail_budget", "tail-cmid-1", "问题1", "回答1")
	bigPartial := strings.Repeat("尾", 4096)
	seedFailedTurn(f, "chat_conv_tail_budget", "tail-cmid-2", "长半截问题", bigPartial)

	full, err := f.store.LoadModelContext("chat_conv_tail_budget", routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Suffix) != 4 {
		t.Fatalf("预算充足时应注入: %v", suffixRoles(t, full))
	}
	tailBytes := full.Suffix[2].contentBytes + full.Suffix[3].contentBytes
	if tailBytes < 2 {
		t.Fatalf("尾部两行字节应非零: %d", tailBytes)
	}

	// 字节预算比完整装载少一字节：尾部两行放不下，放弃注入且不报错。
	byteShort, err := f.store.LoadModelContext("chat_conv_tail_budget", routeTestOwner, f.nowISO, 512, int(full.LoadedBytes)-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(byteShort.Suffix) != 2 {
		t.Fatalf("字节预算不足时应放弃注入: %v", suffixRoles(t, byteShort))
	}
	if !byteShort.Complete || byteShort.TruncatedAt != nil {
		t.Fatalf("放弃注入不应改变 Complete/TruncatedAt: %+v", byteShort)
	}

	// 行数预算恰被 completed 对耗尽：同样放弃注入。
	rowShort, err := f.store.LoadModelContext("chat_conv_tail_budget", routeTestOwner, f.nowISO, 2, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowShort.Suffix) != 2 {
		t.Fatalf("行数预算不足时应放弃注入: %v", suffixRoles(t, rowShort))
	}
	if !rowShort.Complete || rowShort.TruncatedAt != nil {
		t.Fatalf("放弃注入不应改变 Complete/TruncatedAt: %+v", rowShort)
	}
}

// decodeUpstreamMessages 解析发往上游的 chat completions 请求体 messages。
func decodeUpstreamMessages(t *testing.T, body string) []map[string]any {
	t.Helper()
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("上游请求体解析失败: %v", err)
	}
	return parsed.Messages
}

// TestCompactionKeepsTailFailedTurnInSuffix 压缩交互（§14.1 核实）：压缩源
// 只折叠 completed 成对轮次，不把尾部 failed 轮卷入 checkpoint；水印
// （sourceThrough = next_sequence_no - 3）停在 failed 轮之前，压缩安装后
// 尾部中断轮仍位于压缩水位之后、照常注入。
func TestCompactionKeepsTailFailedTurnInSuffix(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_tail_compaction"
	f.createConversation(conversationID, routeTestOwner)
	seedCompletedTurn(f, conversationID, "tail-cmp-cmid-1", "问题1", "回答1")
	seedCompletedTurn(f, conversationID, "tail-cmp-cmid-2", "问题2", "回答2")
	seedFailedTurn(f, conversationID, "tail-cmp-cmid-3", "中断的问题", "半截回答")

	loaded, err := f.store.LoadModelContext(conversationID, routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	sourceThrough := loaded.Head.NextSequenceNo - 3
	if requested, err := f.store.RequestContextCompaction(RequestCompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedRevision: loaded.Head.ContextRevision, SourceThroughSequence: sourceThrough, Now: f.nowISO,
	}); err != nil || !requested {
		t.Fatalf("请求压缩失败: %v %v", requested, err)
	}
	stale, _ := shiftInstantISO(f.nowISO, compactionStaleClaimBefore)
	claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner,
		ExpectedRevision: loaded.Head.ContextRevision, SourceThroughSequence: sourceThrough,
		Now: f.nowISO, StaleClaimBefore: stale,
	})
	if err != nil || claim == nil {
		t.Fatalf("认领压缩失败: %+v %v", claim, err)
	}
	// 压缩源翻页只含 completed 成对轮次，不含尾部 failed 轮内容。
	page, err := f.store.LoadCompactionSourcePage(conversationID, routeTestOwner, claim.ClaimID, claim.ProgressSequence, f.nowISO, 40, 512*1024)
	if err != nil || page == nil {
		t.Fatalf("读取压缩源失败: %+v %v", page, err)
	}
	for _, message := range page.Messages {
		if message.contentText == "中断的问题" || message.contentText == "半截回答" {
			t.Fatalf("压缩源不应卷入尾部 failed 轮: %+v", message)
		}
	}
	if len(page.Messages) != 4 {
		t.Fatalf("压缩源应只含两个 completed 轮次: %d", len(page.Messages))
	}
	if progressed, err := f.store.RecordCompactionProgress(RecordCompactionProgressInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner, ClaimID: claim.ClaimID,
		ThroughSequence: sourceThrough, EarliestExpiresAt: "2027-03-10T08:00:00.000Z", Now: f.nowISO,
	}); err != nil || !progressed {
		t.Fatalf("推进压缩进度失败: %v %v", progressed, err)
	}
	expiry := "2027-03-10T08:00:00.000Z"
	if _, err := f.store.InstallContextCheckpoint(InstallCheckpointInput{
		ClaimID: claim.ClaimID, ConversationID: conversationID, SystemAccountID: routeTestOwner,
		SourceRevision: loaded.Head.ContextRevision, SourceThroughSequence: sourceThrough,
		ExpiresAt: expiry, PayloadDigest: digest64("abcd"),
		RequestBodyBytes: 10, ModelID: "gpt-5", EndpointFamily: string(ProtocolChatCompletions), PromptVersion: compactionPromptVersion,
		Entries: []CheckpointEntryInput{
			{Kind: "verbatim", Content: jsonRaw(`{"text":"问题1/回答1"}`), Provenance: "user", TrustLevel: "untrusted"},
			{Kind: "verbatim", Content: jsonRaw(`{"text":"问题2/回答2"}`), Provenance: "assistant", TrustLevel: "assistant_derived"},
		},
		Now: f.nowISO,
	}); err != nil {
		t.Fatal(err)
	}
	// 压缩安装后：水印不越过 failed 轮，尾部中断轮照常注入。
	after, err := f.store.LoadModelContext(conversationID, routeTestOwner, f.nowISO, 512, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if after.Head.CompactedThroughSequence != sourceThrough {
		t.Fatalf("压缩水位应为 %d: %d", sourceThrough, after.Head.CompactedThroughSequence)
	}
	roles := suffixRoles(t, after)
	if len(roles) != 2 || strings.Join(roles, ",") != "user,assistant" {
		t.Fatalf("压缩后 suffix 应只含尾部中断轮: %v", roles)
	}
	if after.Suffix[0].contentText != "中断的问题" || after.Suffix[1].contentText != "半截回答" {
		t.Fatalf("压缩后尾部中断轮内容不正确: %+v", after.Suffix)
	}
}

// appendChatCompletionStep 为 executor 追加一个恒定 chat completions SSE 步骤。
func appendChatCompletionStep(env *generationEnv, answer string) {
	env.t.Helper()
	env.executor.steps = append(env.executor.steps, scriptStep{
		match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			return sseResponse(chatCompletionsSSE(answer, true))
		},
	})
}

// TestStreamRouteTailFailedTurnEntersUpstreamHistory 端到端：失败轮之后发送
// 「继续」，发往上游的请求体 history 包含 user 原问题与 assistant 半截内容，
// 且位于新用户消息之前。
func TestStreamRouteTailFailedTurnEntersUpstreamHistory(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_tail_route"
	f.createConversation(conversationID, routeTestOwner)
	seedCompletedTurn(f, conversationID, "tail-route-cmid-1", "问题1", "回答1")
	seedFailedTurn(f, conversationID, "tail-route-cmid-2", "帮我写一段长文", "开头半截内容")

	env := buildGenerationEnvW10D(t, f)
	appendChatCompletionStep(env, "继续后的新回答")
	rt := newChatRoutesForTest(env.deps)

	recorder := w14lStreamPostRaw(rt, conversationID, streamPayload("tail-route-cmid-3", "继续", "gpt-5"), routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.started") {
		t.Fatalf("发送应成功出流: %d %s", recorder.Code, body[:min(len(body), 300)])
	}
	if env.executor.callCount() < 1 {
		t.Fatal("上游请求缺失")
	}
	messages := decodeUpstreamMessages(t, env.executor.calls[0].Body)
	wantRoles := []string{"system", "user", "assistant", "user", "assistant", "user"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("上游 history 形状不正确: %v", messages)
	}
	for index, role := range wantRoles {
		if messages[index]["role"] != role {
			t.Fatalf("上游第 %d 条 role 应为 %s: %v", index, role, messages[index])
		}
	}
	if messages[3]["content"] != "帮我写一段长文" || messages[4]["content"] != "开头半截内容" {
		t.Fatalf("上游 history 缺少失败轮的提问与半截回答: %v / %v", messages[3], messages[4])
	}
	if messages[5]["content"] != "继续" {
		t.Fatalf("新用户消息应在最后: %v", messages[5])
	}
}

// TestStreamRouteTailCanceledTurnEntersUpstreamHistory 端到端：用户停止生成
// （canceled 且保留半截内容）之后发送「继续」，发往上游的请求体 history 包含
// 停止轮 user 原问题与 assistant 半截内容，且位于新用户消息之前。
func TestStreamRouteTailCanceledTurnEntersUpstreamHistory(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_tail_cancel_route"
	f.createConversation(conversationID, routeTestOwner)
	seedCompletedTurn(f, conversationID, "tail-cancel-route-cmid-1", "问题1", "回答1")
	seedCanceledTurn(f, conversationID, "tail-cancel-route-cmid-2", "帮我写一段长文", "开头半截内容")

	env := buildGenerationEnvW10D(t, f)
	appendChatCompletionStep(env, "继续后的新回答")
	rt := newChatRoutesForTest(env.deps)

	recorder := w14lStreamPostRaw(rt, conversationID, streamPayload("tail-cancel-route-cmid-3", "继续", "gpt-5"), routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.started") {
		t.Fatalf("发送应成功出流: %d %s", recorder.Code, body[:min(len(body), 300)])
	}
	if env.executor.callCount() < 1 {
		t.Fatal("上游请求缺失")
	}
	messages := decodeUpstreamMessages(t, env.executor.calls[0].Body)
	wantRoles := []string{"system", "user", "assistant", "user", "assistant", "user"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("上游 history 形状不正确: %v", messages)
	}
	for index, role := range wantRoles {
		if messages[index]["role"] != role {
			t.Fatalf("上游第 %d 条 role 应为 %s: %v", index, role, messages[index])
		}
	}
	if messages[3]["content"] != "帮我写一段长文" || messages[4]["content"] != "开头半截内容" {
		t.Fatalf("上游 history 缺少停止轮的提问与半截回答: %v / %v", messages[3], messages[4])
	}
	if messages[5]["content"] != "继续" {
		t.Fatalf("新用户消息应在最后: %v", messages[5])
	}
}

// TestStreamRouteReplaceExcludesTailFailedTurn 端到端：replace 命中尾部失败轮
// 时，旧提问与半截回答不进入发往上游的请求体 history。
func TestStreamRouteReplaceExcludesTailFailedTurn(t *testing.T) {
	f := newChatFixture(t)
	conversationID := "chat_conv_tail_replace"
	f.createConversation(conversationID, routeTestOwner)
	seedCompletedTurn(f, conversationID, "tail-replace-cmid-1", "问题1", "回答1")
	failedTurnID := seedFailedTurn(f, conversationID, "tail-replace-cmid-2", "帮我写一段长文", "开头半截内容")

	env := buildGenerationEnvW10D(t, f)
	appendChatCompletionStep(env, "重新生成的回答")
	rt := newChatRoutesForTest(env.deps)

	payload := `{"clientMessageId":"tail-replace-cmid-3","content":"重新写","model":"gpt-5","replaceTurnId":"` + failedTurnID + `"}`
	recorder := w14lStreamPostRaw(rt, conversationID, payload, routeTestOwner)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "message.started") {
		t.Fatalf("replace 发送应成功出流: %d %s", recorder.Code, body[:min(len(body), 300)])
	}
	if env.executor.callCount() < 1 {
		t.Fatal("上游请求缺失")
	}
	messages := decodeUpstreamMessages(t, env.executor.calls[0].Body)
	for _, message := range messages {
		if message["content"] == "帮我写一段长文" || message["content"] == "开头半截内容" {
			t.Fatalf("replace 命中后旧失败轮不应进入上游 history: %v", messages)
		}
	}
	// 保留的 completed 轮次与新用户消息仍在。
	foundQuestion := false
	for _, message := range messages {
		if message["content"] == "问题1" {
			foundQuestion = true
		}
	}
	if !foundQuestion {
		t.Fatalf("replace 后 completed 轮次应保留: %v", messages)
	}
	if messages[len(messages)-1]["content"] != "重新写" {
		t.Fatalf("新用户消息应在最后: %v", messages[len(messages)-1])
	}
}
