package chat

// w13b 波次：store 层覆盖（turns 生命周期、assets 持久化、context 状态机、
// windows 容量与 pg 方言分支）。全部使用 SQLite 内存 fixture；pg 方言分支用
// pg=true 的 Store 叠加 SQLite 驱动驱动错误臂；单条语句失败臂复用 w10d
// 故障注入脚本。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestW13BAcceptTurnReplaceLifecycle(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-rep", owner)
	first := f.accept(owner, "w13b-conv-rep", "cmid-1", "第一问")
	f.complete(owner, "w13b-conv-rep", first.TurnID, "第一答")

	// 替换成功：保留块绑定、storage 窗口回退、标题源迁移。
	result, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID:          "w13b-conv-rep",
		SystemAccountID:         owner,
		ClientMessageID:         "cmid-2",
		UserContent:             "替换后的问题",
		Model:                   "gpt-5",
		Now:                     f.nowISO,
		StorageQuotaBytes:       2 * 1024 * 1024 * 1024,
		RetentionDays:           30,
		MaxTurnsPerConversation: 100,
		ReplaceTurnID:           first.TurnID,
	})
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("替换不应为重复")
	}
	messages, err := f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-rep", SystemAccountID: owner, Now: f.nowISO, Limit: 10})
	if err != nil || len(messages) != 2 {
		t.Fatalf("替换后应只有一对消息: %d %v", len(messages), err)
	}
	if messages[0].ContentText != "替换后的问题" {
		t.Fatalf("替换内容不正确: %+v", messages[0])
	}
	if total := storageWindowTotal(t, f.db, owner); total <= 0 {
		t.Fatalf("storage 窗口应保留新轮次字节: %d", total)
	}

	// 幂等重复提交。
	dup, err := f.store.AcceptTurn(AcceptTurnInput{
		ConversationID: "w13b-conv-rep", SystemAccountID: owner, ClientMessageID: "cmid-2",
		UserContent: "x", Model: "gpt-5", Now: f.nowISO,
		StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 100,
	})
	if err != nil || !dup.Duplicate || dup.TurnID != result.TurnID {
		t.Fatalf("重复提交应命中幂等: %+v %v", dup, err)
	}
}

func TestW13BAcceptTurnReplaceConflictArms(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-rep2", owner)
	replaceInput := func(cmid, replaceTurnID string) AcceptTurnInput {
		return AcceptTurnInput{
			ConversationID: "w13b-conv-rep2", SystemAccountID: owner, ClientMessageID: cmid,
			UserContent: "第二问", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 30,
			MaxTurnsPerConversation: 100, ReplaceTurnID: replaceTurnID,
		}
	}
	var conflict *ConflictError

	// 基线：第一轮完成后成为最近轮。
	first := f.accept(owner, "w13b-conv-rep2", "cmid-1", "第一问")
	f.complete(owner, "w13b-conv-rep2", first.TurnID, "第一答")

	// 活动轮次冲突：接受后未完成（replace 分支的 activeTurn 冲突）。
	active := f.accept(owner, "w13b-conv-rep2", "cmid-active", "进行中")
	if _, err := f.store.AcceptTurn(replaceInput("cmid-2", first.TurnID)); !errorsAs(err, &conflict) || conflict.Code != ConflictReplaceConflict {
		t.Fatalf("活动轮次应报 replace 冲突: %v", err)
	}
	if _, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-rep2", SystemAccountID: owner, ExpectedTurnID: active.TurnID, Now: f.nowISO}); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	// 窗口数据不一致：清空 storage 窗口后替换最近轮（第三轮）。
	third := f.accept(owner, "w13b-conv-rep2", "cmid-3", "第三问")
	f.complete(owner, "w13b-conv-rep2", third.TurnID, "第三答")
	if _, err := f.db.Exec("DELETE FROM chat_user_storage_windows WHERE system_account_id = ?", owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptTurn(replaceInput("cmid-4", third.TurnID)); err == nil || !strings.Contains(err.Error(), "容量窗口数据不一致") {
		t.Fatalf("窗口不一致应报错: %v", err)
	}

	// 容量超限：注入巨大窗口。
	if err := f.store.incrementStorageWindow(f.db, owner, f.nowISO, 2*1024*1024*1024, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptTurn(replaceInput("cmid-5", third.TurnID)); !errorsAs(err, &conflict) || conflict.Code != ConflictStorageQuotaExceeded {
		t.Fatalf("替换容量超限应报冲突: %v", err)
	}
	if _, err := f.db.Exec("DELETE FROM chat_user_storage_windows WHERE system_account_id = ?", owner); err != nil {
		t.Fatal(err)
	}

	// 幂等记录缺失：删除 idempotency 行后替换应报 replace 冲突（affected != 1）。
	if _, err := f.db.Exec("DELETE FROM chat_message_idempotency WHERE conversation_id = 'w13b-conv-rep2' AND turn_id = ?", third.TurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcceptTurn(replaceInput("cmid-6", third.TurnID)); !errorsAs(err, &conflict) || conflict.Code != ConflictReplaceConflict {
		t.Fatalf("幂等缺失应报 replace 冲突: %v", err)
	}
}

func TestW13BRequireReplaceableTurnArms(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-req", owner)
	conversationID := "w13b-conv-req-a"
	f.createConversation(conversationID, owner)
	accepted := f.accept(owner, conversationID, "cmid-1", "问题")
	f.complete(owner, conversationID, accepted.TurnID, "回答")

	assertReplaceable := func(turnID string) error {
		return f.store.AssertTurnReplaceable(AssertReplaceableInput{
			ConversationID: conversationID, SystemAccountID: owner, ReplaceTurnID: turnID, Now: f.nowISO,
		})
	}
	if err := assertReplaceable("turn-missing"); err == nil {
		t.Fatalf("缺失轮次应报冲突")
	}
	if err := assertReplaceable(accepted.TurnID); err != nil {
		t.Fatalf("可替换轮次应通过: %v", err)
	}
	// assistant 状态非法分支：streaming（带预留字节满足 CHECK）。
	if _, err := f.db.Exec(`UPDATE chat_messages SET status = 'streaming', storage_reserved_bytes = 64 WHERE turn_id = ? AND role = 'assistant'`, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	if err := assertReplaceable(accepted.TurnID); err == nil {
		t.Fatalf("streaming 状态应报冲突")
	}
	if _, err := f.db.Exec(`UPDATE chat_messages SET status = 'completed', storage_reserved_bytes = 0 WHERE turn_id = ? AND role = 'assistant'`, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	// markers 缺失：content_blocks_json 无可解析标记。
	if _, err := f.db.Exec(`UPDATE chat_messages SET content_blocks_json = '{}' WHERE turn_id = ? AND role = 'user'`, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	if err := assertReplaceable(accepted.TurnID); err == nil {
		t.Fatalf("markers 非数组应报冲突")
	}
	if _, err := f.db.Exec(`UPDATE chat_messages SET content_blocks_json = '[]' WHERE turn_id = ? AND role = 'user'`, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	if err := assertReplaceable(accepted.TurnID); err == nil {
		t.Fatalf("空 markers 应报冲突")
	}
	// 恢复 markers。
	if _, err := f.db.Exec(`UPDATE chat_messages SET content_blocks_json = '[]' WHERE turn_id = ? AND role = 'user'`, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	// 标题源消息匹配分支。
	if _, err := f.db.Exec(`UPDATE chat_conversations SET title_source_message_id = (
		SELECT id FROM chat_messages WHERE turn_id = ? AND role = 'assistant') WHERE id = ?`,
		accepted.TurnID, conversationID); err != nil {
		t.Fatal(err)
	}
	if err := assertReplaceable(accepted.TurnID); err == nil {
		t.Fatalf("标题源为助手消息应报冲突")
	}
	if _, err := f.db.Exec(`UPDATE chat_conversations SET title_source_message_id = NULL WHERE id = ?`, conversationID); err != nil {
		t.Fatal(err)
	}
	// 追加新轮次后 maxSequenceNo 变化。
	second := f.accept(owner, conversationID, "cmid-2", "第二问")
	f.complete(owner, conversationID, second.TurnID, "第二答")
	if err := assertReplaceable(accepted.TurnID); err == nil {
		t.Fatalf("非末轮应报冲突")
	}
}

func TestW13BFinalizeTurnStorageLimit(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-fin", owner)
	accepted := f.accept(owner, "w13b-conv-fin", "cmid-1", "问题")

	// 超大 blocks：序列化超过预留 → 降级 failed。
	huge := strings.Repeat("x", maxContentBlocksBytes+10)
	message, err := f.store.CompleteChatTurn(CompleteTurnInput{
		ConversationID:   "w13b-conv-fin",
		SystemAccountID:  owner,
		TurnID:           accepted.TurnID,
		AssistantContent: "回答",
		ContentBlocksRaw: []byte(`[{"type":"output_text","text":"` + huge + `"}]`),
		Now:              f.nowISO,
	})
	var limitErr *AssistantStorageLimitError
	if !errorsAs(err, &limitErr) || message != nil {
		t.Fatalf("超大内容应报存储上限: %v", err)
	}
	failed, err := f.store.FindTurnByClientMessageID("w13b-conv-fin", owner, "cmid-1")
	if err != nil || failed == nil || failed.AssistantStatus != StatusFailed {
		t.Fatalf("降级后应为 failed: %+v %v", failed, err)
	}
	if failed.ErrorCode == nil || *failed.ErrorCode != "chat_assistant_storage_limit_exceeded" {
		t.Fatalf("降级错误码不正确: %+v", failed.ErrorCode)
	}
}

func TestW13BFinalizeTurnValidationArms(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	complete := func(conversationID, turnID, now string) error {
		_, err := f.store.CompleteChatTurn(CompleteTurnInput{
			ConversationID: conversationID, SystemAccountID: owner, TurnID: turnID,
			AssistantContent: "答", Now: now,
		})
		return err
	}
	if err := complete("missing", "turn", f.nowISO); err == nil || !strings.Contains(err.Error(), "会话不存在") {
		t.Fatalf("缺失会话应报错: %v", err)
	}
	f.createConversation("w13b-conv-fv", owner)
	if err := complete("w13b-conv-fv", "turn-missing", f.nowISO); err == nil || !strings.Contains(err.Error(), "活动回答不存在") {
		t.Fatalf("缺失活动轮应报错: %v", err)
	}
	accepted := f.accept(owner, "w13b-conv-fv", "cmid-1", "问")
	if err := complete("w13b-conv-fv", accepted.TurnID, "bad-time"); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if err := complete("w13b-conv-fv", accepted.TurnID, f.nowISO); err != nil {
		t.Fatalf("完成失败: %v", err)
	}
	// 已终结轮次再完成：活动轮不匹配。
	if err := complete("w13b-conv-fv", accepted.TurnID, f.nowISO); err == nil {
		t.Fatalf("重复完成应报错")
	}
	if _, err := requiredAssistantStorageReservation(1); err == nil {
		t.Fatalf("预留不一致应报错")
	}
	if _, err := f.store.FailChatTurn(FailTurnInput{
		ConversationID: "w13b-conv-fv", SystemAccountID: owner, TurnID: accepted.TurnID,
		ErrorCode: "x", ErrorMessage: "y", Now: "bad",
	}); err == nil {
		t.Fatalf("FailChatTurn 非法 now 应报错")
	}
	if _, err := f.store.CancelChatTurn(CancelTurnInput{
		ConversationID: "w13b-conv-fv", SystemAccountID: owner, TurnID: accepted.TurnID, Now: "bad",
	}); err == nil {
		t.Fatalf("CancelChatTurn 非法 now 应报错")
	}
}

func TestW13BConditionalStopMatrix(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-stop", owner)

	// not found。
	result, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: "missing", SystemAccountID: owner, ExpectedTurnID: "t", Now: f.nowISO})
	if err != nil || result.State != CancelStateNotFound {
		t.Fatalf("缺失会话应返回 not_found: %+v %v", result, err)
	}
	accepted := f.accept(owner, "w13b-conv-stop", "cmid-1", "问")
	// turn mismatch。
	result, err = f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: "other", Now: f.nowISO})
	if err != nil || result.State != CancelStateTurnMismatch {
		t.Fatalf("不匹配应返回 turn_mismatch: %+v %v", result, err)
	}
	// already terminal。
	f.complete(owner, "w13b-conv-stop", accepted.TurnID, "答")
	result, err = f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: accepted.TurnID, Now: f.nowISO})
	if err != nil || result.State != CancelStateAlreadyTerminal || result.AssistantStatus != StatusCompleted {
		t.Fatalf("已终态应返回 already_terminal: %+v %v", result, err)
	}
	// 中断收口：新建活动轮。
	second := f.accept(owner, "w13b-conv-stop", "cmid-2", "问二")
	interrupted, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: second.TurnID, Now: f.nowISO})
	if err != nil || interrupted.State != CancelStateAlreadyTerminal || interrupted.AssistantStatus != StatusFailed {
		t.Fatalf("中断收口失败: %+v %v", interrupted, err)
	}
	third := f.accept(owner, "w13b-conv-stop", "cmid-3", "问三")
	canceled, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: third.TurnID, Now: f.nowISO})
	if err != nil || canceled.State != CancelStateCanceled || canceled.AssistantStatus != StatusCanceled {
		t.Fatalf("取消失败: %+v %v", canceled, err)
	}
	// 非法 now。
	if _, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: "t", Now: "bad"}); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if _, err := f.store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: "t", Now: "bad"}); err == nil {
		t.Fatalf("中断非法 now 应报错")
	}
}

func TestW13BReadConditionalStopState(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-rcs", owner)
	input := CancelIfMatchesInput{ConversationID: "w13b-conv-rcs", SystemAccountID: owner, ExpectedTurnID: "t1", Now: f.nowISO}
	result, err := f.store.readConditionalStopState(f.db, input)
	if err != nil || result.State != CancelStateNotFound {
		t.Fatalf("空会话分类失败: %+v %v", result, err)
	}
	accepted := f.accept(owner, "w13b-conv-rcs", "cmid-1", "问")
	result, err = f.store.readConditionalStopState(f.db, input)
	if err != nil || result.State != CancelStateTurnMismatch {
		t.Fatalf("不匹配分类失败: %+v %v", result, err)
	}
	matched := CancelIfMatchesInput{ConversationID: "w13b-conv-rcs", SystemAccountID: owner, ExpectedTurnID: accepted.TurnID, Now: f.nowISO}
	result, err = f.store.readConditionalStopState(f.db, matched)
	if err != nil || result != nil {
		t.Fatalf("活动轮应返回 nil（可继续收口）: %+v %v", result, err)
	}
	f.complete(owner, "w13b-conv-rcs", accepted.TurnID, "答")
	result, err = f.store.readConditionalStopState(f.db, matched)
	if err != nil || result.State != CancelStateAlreadyTerminal {
		t.Fatalf("终态分类失败: %+v %v", result, err)
	}
}

func TestW13BListMessagesValidation(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-lm", owner)
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-lm", SystemAccountID: owner, Now: "bad"}); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	one := int64(1)
	two := int64(2)
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-lm", SystemAccountID: owner, Now: f.nowISO, BeforeSequenceNo: &one, AfterSequenceNo: &two}); err == nil {
		t.Fatalf("双游标应报错")
	}
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "missing", SystemAccountID: owner, Now: f.nowISO}); err == nil {
		t.Fatalf("缺失会话应报错")
	}
	accepted := f.accept(owner, "w13b-conv-lm", "cmid-1", "问")
	f.complete(owner, "w13b-conv-lm", accepted.TurnID, "答")
	messages, err := f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-lm", SystemAccountID: owner, Now: f.nowISO, BeforeSequenceNo: &two, Limit: 1})
	if err != nil || len(messages) != 1 || messages[0].SequenceNo != 1 {
		t.Fatalf("before 游标分页失败: %+v %v", messages, err)
	}
	after := int64(0)
	messages, err = f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-lm", SystemAccountID: owner, Now: f.nowISO, AfterSequenceNo: &after, Limit: 50})
	if err != nil || len(messages) != 2 || messages[0].SequenceNo != 1 {
		t.Fatalf("after 游标分页失败: %+v %v", messages, err)
	}
	from := int64(2)
	messages, err = f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-lm", SystemAccountID: owner, Now: f.nowISO, FromSequenceNo: &from, Limit: 500})
	if err != nil || len(messages) != 1 || messages[0].SequenceNo != 2 {
		t.Fatalf("from 游标分页失败: %+v %v", messages, err)
	}
}

func TestW13BGetConversationSyncHeadInvalidNow(t *testing.T) {
	f := newChatFixture(t)
	f.createConversation("w13b-conv-sh", "owner")
	if _, err := f.store.GetConversationSyncHead("w13b-conv-sh", "owner", "bad"); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if head, err := f.store.GetConversationSyncHead("missing", "owner", f.nowISO); err != nil || head != nil {
		t.Fatalf("缺失会话应返回 nil: %+v %v", head, err)
	}
}

func TestW13BPgDialectArms(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	pgStore, err := NewStore(fixture.db, true, fixtureStoreClockW13B(fixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := "w13b-owner"
	// lockChatUserStorageQuota / lockAssetUserQuota / 分区创建 / pg upsert 均
	// 在 SQLite 上执行 PG 语法而失败，覆盖错误臂。
	if err := pgStore.lockChatUserStorageQuota(fixture.db, owner); err == nil {
		t.Fatalf("pg 存储锁应在 SQLite 上失败")
	}
	if err := pgStore.lockAssetUserQuota(fixture.db, owner); err == nil {
		t.Fatalf("pg 配额锁应在 SQLite 上失败")
	}
	// 分区日期避开 fixture 的 2026-03-10：stub 渲染测试会把它进程级
	// memoize，使 ensure 变 no-op（顺序依赖失败）。
	if err := pgStore.ensurePostgresChatMessagePartitions(fixture.db, "2027-07-01T00:00:00.000Z"); err == nil {
		t.Fatalf("pg 分区创建应在 SQLite 上失败")
	}
	if err := pgStore.incrementStorageWindow(fixture.db, owner, fNowISO(fixture), 1, 1); err == nil {
		t.Fatalf("pg upsert 应在 SQLite 上失败")
	}
	// pg=false 下这些守卫直接通过。
	store := fixture.store
	if err := store.lockChatUserStorageQuota(fixture.db, owner); err != nil {
		t.Fatalf("sqlite 守卫应为 no-op: %v", err)
	}
	if err := store.incrementStorageWindow(fixture.db, owner, fNowISO(fixture), 1, 1); err != nil {
		t.Fatalf("sqlite upsert 失败: %v", err)
	}
	_ = script
}

func TestW13BAcceptTurnPgLockArm(t *testing.T) {
	fixture, _ := newFaultChatFixtureW13B(t)
	pgStore, err := NewStore(fixture.db, true, fixtureStoreClockW13B(fixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	// pg 方言下 AcceptTurn 在存储锁一步即失败（SQLite 不识别 advisory lock）。
	_, err = pgStore.AcceptTurn(AcceptTurnInput{
		ConversationID: "c", SystemAccountID: "o", ClientMessageID: "m",
		UserContent: "x", Model: "gpt-5", Now: fNowISO(fixture),
		StorageQuotaBytes: 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
	})
	if err == nil {
		t.Fatalf("pg 方言的 AcceptTurn 应在 SQLite 上失败")
	}
	// 会话创建与删除同路。
	if _, err := pgStore.CreateConversation(CreateConversationInput{
		SystemAccountID: "o", APIKeyID: "k", APIKeyNameSnapshot: "n", Now: fNowISO(fixture), MaxConversationsPerUser: 1,
	}); err == nil {
		t.Fatalf("pg 方言的 CreateConversation 应在 SQLite 上失败")
	}
	if _, err := pgStore.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: "o", ConversationID: "c", OriginalFilename: "a.png",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("0", 64),
		QuotaBytes: 1, Now: fNowISO(fixture), RetentionDays: 30,
	}); err == nil {
		t.Fatalf("pg 方言的 CreateChatAsset 应在 SQLite 上失败")
	}
	if _, err := pgStore.CompleteChatTurn(CompleteTurnInput{
		ConversationID: "c", SystemAccountID: "o", TurnID: "t", AssistantContent: "x", Now: fNowISO(fixture),
	}); err == nil {
		t.Fatalf("pg 方言的 CompleteChatTurn 应在 SQLite 上失败")
	}
}

func TestW13BReleaseConversationStorage(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-rel", owner)
	accepted := f.accept(owner, "w13b-conv-rel", "cmid-1", "问")
	f.complete(owner, "w13b-conv-rel", accepted.TurnID, "答")
	if total := storageWindowTotal(t, f.db, owner); total <= 0 {
		t.Fatalf("窗口应有数据")
	}
	// 故障：消息行扫描失败。
	script.failOnce("SELECT created_at, content_bytes, storage_reserved_bytes")
	if err := f.store.releaseConversationStorageAndExpireAssets(f.db, "w13b-conv-rel", owner, f.nowISO); err == nil {
		t.Fatalf("注入故障应上抛")
	}
	// 正常释放：窗口清零并删除空桶，资产过期。
	if err := f.store.releaseConversationStorageAndExpireAssets(f.db, "w13b-conv-rel", owner, f.nowISO); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if total := storageWindowTotal(t, f.db, owner); total != 0 {
		t.Fatalf("释放后窗口应清零: %d", total)
	}
	// 资产过期臂：插入资产后释放会话应压缩 expires_at。
	if _, err := f.db.Exec(`INSERT INTO chat_assets (
		id, system_account_id, conversation_id, source_kind, original_filename, original_mime_type,
		original_bytes, original_sha256, quota_bytes, processing_status, cleanup_status, created_at, updated_at, expires_at
	) VALUES ('chat_asset_'+replace(hex(randomblob(16)),'',''), ?, 'w13b-conv-rel', 'user_upload', 'a.png', 'image/png',
		1, ?, 1, 'ready', 'active', ?, ?, ?)`,
		owner, strings.Repeat("0", 64), f.nowISO, f.nowISO, "2099-01-01T00:00:00.000Z"); err != nil {
		t.Fatalf("插入资产失败: %v", err)
	}
	var assetID string
	if err := f.db.QueryRow(`SELECT id FROM chat_assets WHERE conversation_id = 'w13b-conv-rel'`).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.expireChatAssetsForConversation(f.db, "w13b-conv-rel", owner, f.nowISO); err != nil {
		t.Fatalf("资产过期失败: %v", err)
	}
	var expiresAt string
	if err := f.db.QueryRow(`SELECT expires_at FROM chat_assets WHERE id = ?`, assetID).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	if expiresAt != f.nowISO {
		t.Fatalf("过期时间应被压缩: %q", expiresAt)
	}
	if err := f.store.expireChatAssetsForConversation(f.db, "w13b-conv-rel", owner, "bad"); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	// recentStorageBytes 错误臂。
	if _, err := f.store.recentStorageBytes(f.db, owner, "bad", 30); err == nil {
		t.Fatalf("非法 now 应报错")
	}
}

func TestW13BFaultArmsTurns(t *testing.T) {
	// AcceptTurn 各故障臂：每个臂使用独立 fixture 与 clientMessageID。
	armPatterns := []struct {
		name    string
		pattern string
	}{
		{"幂等查询", "FROM chat_message_idempotency\n\t\tWHERE conversation_id"},
		{"storage 窗口统计", "SUM(content_bytes + reserved_bytes)"},
		{"插入用户消息", "INSERT INTO chat_messages"},
		{"插入幂等", "INSERT INTO chat_message_idempotency"},
		{"窗口递增", "INSERT INTO chat_user_storage_windows"},
		{"更新会话", "SET title = CASE WHEN next_sequence_no = 1"},
		{"提交", commitFaultKeyW10D},
	}
	for index, arm := range armPatterns {
		t.Run(arm.name, func(t *testing.T) {
			fixture, script := newFaultChatFixtureW13B(t)
			fixture.createConversation("w13b-conv-fault", "w13b-owner")
			script.failOnce(arm.pattern)
			_, err := fixture.store.AcceptTurn(AcceptTurnInput{
				ConversationID: "w13b-conv-fault", SystemAccountID: "w13b-owner",
				ClientMessageID: fmt.Sprintf("w13b-arm-%d", index),
				UserContent:     "问", Model: "gpt-5", Now: fixture.nowISO,
				StorageQuotaBytes: 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
			})
			if err == nil {
				t.Fatalf("%s 故障臂应上抛", arm.name)
			}
			// 无残留故障：同输入重试应成功。
			if _, err := fixture.store.AcceptTurn(AcceptTurnInput{
				ConversationID: "w13b-conv-fault", SystemAccountID: "w13b-owner",
				ClientMessageID: fmt.Sprintf("w13b-arm-%d", index),
				UserContent:     "问", Model: "gpt-5", Now: fixture.nowISO,
				StorageQuotaBytes: 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
			}); err != nil {
				t.Fatalf("重试应成功（存在残留故障）: %v", err)
			}
		})
	}

	t.Run("消息对读取", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW13B(t)
		f := fixture
		f.createConversation("w13b-conv-fault", "w13b-owner")
		accepted := f.accept("w13b-owner", "w13b-conv-fault", "cmid-dup", "问二")
		f.complete("w13b-owner", "w13b-conv-fault", accepted.TurnID, "答二")
		script.failOnce("ORDER BY sequence_no ASC")
		if _, err := f.store.AcceptTurn(AcceptTurnInput{
			ConversationID: "w13b-conv-fault", SystemAccountID: "w13b-owner", ClientMessageID: "cmid-dup",
			UserContent: "问", Model: "gpt-5", Now: f.nowISO,
			StorageQuotaBytes: 1024 * 1024, RetentionDays: 30, MaxTurnsPerConversation: 10,
		}); err == nil {
			t.Fatalf("消息对读取故障臂应上抛")
		}
	})

	t.Run("finalize", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW13B(t)
		f := fixture
		owner := "w13b-owner"
		f.createConversation("w13b-conv-fin", owner)
		pending := f.accept(owner, "w13b-conv-fin", "cmid-3", "问三")
		complete := func() error {
			_, err := f.store.CompleteChatTurn(CompleteTurnInput{
				ConversationID: "w13b-conv-fin", SystemAccountID: owner, TurnID: pending.TurnID,
				AssistantContent: "答", Now: f.nowISO,
			})
			return err
		}
		arms := []struct {
			name    string
			pattern string
		}{
			{"流式查询", "SELECT id, conversation_id, system_account_id, turn_id, sequence_no"},
			{"会话更新", "UPDATE chat_conversations\n\t\tSET active_turn_id = NULL"},
			{"窗口结算", "SET content_bytes = content_bytes + ?"},
			{"提交", commitFaultKeyW10D},
		}
		for _, arm := range arms {
			script.failOnce(arm.pattern)
			if err := complete(); err == nil {
				t.Fatalf("%s 故障臂应上抛", arm.name)
			}
		}
		// 无残留：补偿完成。
		if err := complete(); err != nil {
			t.Fatalf("补偿完成失败（存在残留故障）: %v", err)
		}
	})

	t.Run("conditionalStop", func(t *testing.T) {
		fixture, script := newFaultChatFixtureW13B(t)
		f := fixture
		owner := "w13b-owner"
		f.createConversation("w13b-conv-stop", owner)
		pending := f.accept(owner, "w13b-conv-stop", "cmid-4", "问四")
		cancel := func() error {
			_, err := f.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{
				ConversationID: "w13b-conv-stop", SystemAccountID: owner, ExpectedTurnID: pending.TurnID, Now: f.nowISO,
			})
			return err
		}
		script.failOnce("SELECT id, conversation_id, system_account_id, turn_id, sequence_no")
		if err := cancel(); err == nil {
			t.Fatalf("查询故障臂应上抛")
		}
		// 窗口释放臂：上一臂已消费查询故障，本轮直达释放语句。
		script.failOnce("SET reserved_bytes = reserved_bytes - ?")
		if err := cancel(); err == nil {
			t.Fatalf("窗口释放故障臂应上抛")
		}
		// 无残留：取消成功。
		if err := cancel(); err != nil {
			t.Fatalf("取消失败（存在残留故障）: %v", err)
		}
	})
}

func TestW13BFaultArmsStoreQueries(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-q", owner)
	accepted := f.accept(owner, "w13b-conv-q", "cmid-1", "问")
	f.complete(owner, "w13b-conv-q", accepted.TurnID, "答")

	// FindTurnByClientMessageID 查询故障 + completed_at 非法。
	script.failOnce("SELECT submission.turn_id")
	if _, err := f.store.FindTurnByClientMessageID("w13b-conv-q", owner, "cmid-1"); err == nil {
		t.Fatalf("submission 查询故障臂应上抛")
	}
	if _, err := f.db.Exec(`UPDATE chat_messages SET completed_at = 'bad' WHERE turn_id = ? AND role = 'assistant'`, accepted.TurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.FindTurnByClientMessageID("w13b-conv-q", owner, "cmid-1"); err == nil {
		t.Fatalf("completed_at 非法应上抛")
	}
	if _, err := f.db.Exec(`UPDATE chat_messages SET completed_at = ? WHERE turn_id = ? AND role = 'assistant'`, f.nowISO, accepted.TurnID); err != nil {
		t.Fatal(err)
	}

	// ListMessages 查询与映射故障。
	script.failOnce("FROM chat_messages")
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-q", SystemAccountID: owner, Now: f.nowISO}); err == nil {
		t.Fatalf("ListMessages 查询故障臂应上抛")
	}
	if _, err := f.db.Exec(`UPDATE chat_messages SET created_at = 'bad' WHERE conversation_id = 'w13b-conv-q'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ListMessages(ListMessagesInput{ConversationID: "w13b-conv-q", SystemAccountID: owner, Now: f.nowISO}); err == nil {
		t.Fatalf("非法 created_at 应上抛")
	}
	if _, err := f.db.Exec(`UPDATE chat_messages SET created_at = ? WHERE conversation_id = 'w13b-conv-q'`, f.nowISO); err != nil {
		t.Fatal(err)
	}

	// GetConversationSyncHead 行扫描故障。
	script.failOnce("WITH owned_conversation AS")
	if _, err := f.store.GetConversationSyncHead("w13b-conv-q", owner, f.nowISO); err == nil {
		t.Fatalf("sync head 查询故障臂应上抛")
	}
	if _, err := f.db.Exec(`UPDATE chat_conversations SET active_turn_id = NULL WHERE id = 'w13b-conv-q'`); err != nil {
		t.Fatal(err)
	}
}

func TestW13BStorePureArmHelpers(t *testing.T) {
	if containsString([]string{"a"}, "a") != true || containsString(nil, "a") {
		t.Fatalf("containsString 失败")
	}
	if ascDesc(true) != "ASC" || ascDesc(false) != "DESC" {
		t.Fatalf("ascDesc 失败")
	}
	if (AcceptTurnInput{ReplaceTurnID: "x"}).ReplaceTurnIDValid() != true ||
		(AcceptTurnInput{}).ReplaceTurnIDValid() {
		t.Fatalf("ReplaceTurnIDValid 失败")
	}
	if inputImageAssetIDs([]InputContentBlock{{Type: "input_image", AssetID: stringPtr("a")}, {Type: "input_image"}, {Type: "input_text"}}) == nil {
		t.Fatalf("inputImageAssetIDs 失败")
	}
	if len(inputImageAssetIDs([]InputContentBlock{{Type: "input_image", AssetID: stringPtr("")}})) != 0 {
		t.Fatalf("空 assetId 应跳过")
	}
}

func TestW13BAssetStoreLifecycle(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-asset", owner)

	// 上传槽位与配额。
	slots, err := f.store.AssertChatAssetUploadSlotAvailable(owner, "w13b-conv-asset", f.nowISO)
	if err != nil || slots != maxChatAssetsPerMessage {
		t.Fatalf("初始槽位不正确: %d %v", slots, err)
	}
	if _, err := f.store.AssertChatAssetUploadSlotAvailable(owner, "w13b-conv-asset", "bad"); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	asset, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-asset", SourceKind: "user_upload",
		OriginalFilename: "a.png", OriginalMimeType: "image/png", OriginalBytes: 4,
		OriginalSha256: strings.Repeat("1", 64), QuotaBytes: 10, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil || asset == nil || asset.ProcessingStatus != "pending" {
		t.Fatalf("创建资产失败: %+v %v", asset, err)
	}
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-missing", OriginalFilename: "a",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("1", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	}); err == nil || !strings.Contains(err.Error(), "聊天会话不存在") {
		t.Fatalf("会话缺失应报错: %v", err)
	}
	// 配额超限：注入接近上限的 usage 行。
	if _, err := f.db.Exec(`INSERT INTO chat_user_asset_usage (system_account_id, asset_bytes, asset_count, updated_at)
		VALUES (?, ?, 1, ?) ON CONFLICT(system_account_id) DO UPDATE SET asset_bytes = ?`,
		owner, ChatAssetUserMaxBytes-1, f.nowISO, ChatAssetUserMaxBytes-1); err != nil {
		t.Fatal(err)
	}
	var quotaErr *AssetQuotaExceededError
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-asset", OriginalFilename: "b",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("2", 64),
		QuotaBytes: 10, Now: f.nowISO, RetentionDays: 30,
	}); !errorsAs(err, &quotaErr) {
		t.Fatalf("配额超限应报错: %v", err)
	}
	if _, err := f.db.Exec(`DELETE FROM chat_user_asset_usage WHERE system_account_id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	// 槽位耗尽：首条 + 4 条共 5 条未提交资产。
	for i := 0; i < maxChatAssetsPerMessage-1; i++ {
		if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
			SystemAccountID: owner, ConversationID: "w13b-conv-asset", SourceKind: "user_upload", OriginalFilename: "c",
			OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("3", 64),
			QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
		}); err != nil {
			t.Fatalf("第 %d 个资产创建失败: %v", i, err)
		}
	}
	var countErr *AssetCountExceededError
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-asset", SourceKind: "user_upload", OriginalFilename: "d",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("4", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	}); !errorsAs(err, &countErr) {
		t.Fatalf("槽位耗尽应报错: %v", err)
	}
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-asset", SourceKind: "user_upload", OriginalFilename: "d",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("4", 64),
		QuotaBytes: 1, Now: "bad", RetentionDays: 30,
	}); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	// 完成处理。
	first, _ := f.store.GetAssetNoExpiryGuard("none", owner, "w13b-conv-asset")
	if first != nil {
		t.Fatalf("不存在的资产应返回 nil")
	}
	if _, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: asset.ID, SystemAccountID: owner, ConversationID: "w13b-conv-asset",
		ProcessedMimeType: "image/webp", ProcessedWidth: 16, ProcessedHeight: 16,
		ProcessedBytes: 4, ProcessedSha256: strings.Repeat("5", 64),
		StorageKey: "k1", Now: f.nowISO,
	}); err != nil {
		t.Fatalf("完成处理失败: %v", err)
	}
	if _, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: asset.ID, SystemAccountID: owner, ConversationID: "w13b-conv-asset",
		Now: f.nowISO,
	}); err == nil {
		t.Fatalf("重复完成应报错")
	}
	if _, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{Now: "bad"}); err == nil {
		t.Fatalf("完成处理非法 now 应报错")
	}
	if ok, err := f.store.FailChatAssetProcessing(asset.ID, owner, "w13b-conv-asset", "x", f.nowISO); err != nil || ok {
		t.Fatalf("已 ready 资产失败化应返回 false: %v %v", ok, err)
	}
	if _, err := f.store.FailChatAssetProcessing(asset.ID, owner, "w13b-conv-asset", "x", "bad"); err == nil {
		t.Fatalf("失败化非法 now 应报错")
	}
	// GetAsset 与 ListReadyAssetsByID。
	got, err := f.store.GetAsset(asset.ID, owner, "w13b-conv-asset", f.nowISO)
	if err != nil || got == nil || got.ID != asset.ID {
		t.Fatalf("读取资产失败: %v", err)
	}
	if _, err := f.store.GetAsset(asset.ID, owner, "w13b-conv-asset", "bad"); err == nil {
		t.Fatalf("GetAsset 非法 now 应报错")
	}
	ready, err := f.store.ListReadyAssetsByID([]string{asset.ID}, owner, "w13b-conv-asset", f.nowISO)
	if err != nil || len(ready) != 1 {
		t.Fatalf("就绪资产列表失败: %v %v", ready, err)
	}
	if _, err := f.store.ListReadyAssetsByID([]string{asset.ID}, owner, "w13b-conv-asset", "bad"); err == nil {
		t.Fatalf("ListReady 非法 now 应报错")
	}
	if _, err := f.store.ListReadyAssetsByID([]string{"bad-id"}, owner, "w13b-conv-asset", f.nowISO); err == nil {
		t.Fatalf("非法资产 ID 应报错")
	}
	if empty, err := f.store.ListReadyAssetsByID(nil, owner, "w13b-conv-asset", f.nowISO); err != nil || len(empty) != 0 {
		t.Fatalf("空列表应返回空: %v", err)
	}
	// 删除认领与释放。
	claim, err := f.store.ClaimUncommittedAssetForDeletion("none", owner, "w13b-conv-asset", f.nowISO)
	if err != nil || claim != nil {
		t.Fatalf("无未提交资产应返回 nil: %+v %v", claim, err)
	}
	if _, err := f.store.ClaimUncommittedAssetForDeletion(asset.ID, owner, "w13b-conv-asset", "bad"); err == nil {
		t.Fatalf("认领非法 now 应报错")
	}
	// ready + 已提交资产不可删除（先提交到消息）。
	f.accept(owner, "w13b-conv-asset", "cmid-a", "问")
	var userMessageID, turnID string
	if err := f.db.QueryRow(`SELECT id, turn_id FROM chat_messages WHERE conversation_id = 'w13b-conv-asset' AND role = 'user'`).Scan(&userMessageID, &turnID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.commitChatAssetsToMessage(tx, []string{asset.ID}, owner, "w13b-conv-asset", userMessageID, f.nowISO, 30); err != nil {
		t.Fatalf("提交资产失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err = f.store.ClaimUncommittedAssetForDeletion(asset.ID, owner, "w13b-conv-asset", f.nowISO)
	if err != nil || claim != nil {
		t.Fatalf("已提交资产不可删除: %+v %v", claim, err)
	}
	if ok, err := f.store.ReleaseAssetDeletionClaim("none", "claim", "x", f.nowISO, f.nowISO); err != nil || ok {
		t.Fatalf("释放缺失认领应返回 false: %v %v", ok, err)
	}
	if _, err := f.store.ReleaseAssetDeletionClaim("none", "claim", "x", "bad", f.nowISO); err == nil {
		t.Fatalf("释放非法 retryAt 应报错")
	}
	if _, err := f.store.ReleaseAssetDeletionClaim("none", "claim", "x", f.nowISO, "bad"); err == nil {
		t.Fatalf("释放非法 now 应报错")
	}
	if ok, err := f.store.CompleteAssetDeletion("none", "claim"); err != nil || ok {
		t.Fatalf("完成缺失删除应返回 false: %v %v", ok, err)
	}
	// 故障臂：claimed 资产查询失败。
	script.failOnce("WHERE id = ? AND cleanup_status = 'claimed'")
	if _, err := f.store.getClaimedAsset("x", "y"); err == nil {
		t.Fatalf("claimed 查询故障臂应上抛")
	}
	// scanAsset observation JSON 非法。
	if _, err := f.db.Exec(`UPDATE chat_assets SET observation_json = '{bad}' WHERE id = ?`, asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetAssetNoExpiryGuard(asset.ID, owner, "w13b-conv-asset"); err != nil {
		t.Fatalf("非法 observation JSON 应被忽略: %v", err)
	}
}

func TestW13BCommitGeneratedAssetLifecycle(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-gen", owner)
	accepted := f.accept(owner, "w13b-conv-gen", "cmid-1", "问")

	asset, err := f.store.CommitChatGeneratedAsset(GeneratedAssetCommitInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-gen", TurnID: accepted.TurnID,
		MessageID: accepted.AssistantMessage.ID, ContentOrder: 0,
		MimeType: "image/webp", Width: 16, Height: 16, Bytes: 4, Sha256: strings.Repeat("6", 64),
		StorageKey: "gen-key", PreviewMimeType: "image/webp", PreviewWidth: 8,
		PreviewHeight: 8, PreviewBytes: 2, PreviewSha256: strings.Repeat("7", 64),
		PreviewStorageKey: "gen-preview", Now: f.nowISO, RetentionDays: 30,
		Generation: GeneratedImageGenerationRecord{Operation: "generate", Model: "gpt-image-2", Prompt: "猫"},
	})
	if err != nil || asset == nil || asset.SourceKind != "assistant_generated" {
		t.Fatalf("生成资产提交失败: %+v %v", asset, err)
	}
	if _, err := f.store.CommitChatGeneratedAsset(GeneratedAssetCommitInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-gen", TurnID: accepted.TurnID,
		MessageID: "missing", Now: f.nowISO, RetentionDays: 30,
	}); err == nil || !strings.Contains(err.Error(), "生成图片只能绑定") {
		t.Fatalf("错误助手消息应报错: %v", err)
	}
	if _, err := f.store.CommitChatGeneratedAsset(GeneratedAssetCommitInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-gen", TurnID: accepted.TurnID,
		MessageID: accepted.AssistantMessage.ID, Now: "bad", RetentionDays: 30,
	}); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	records, err := f.store.ListRecentImageGenerations("w13b-conv-gen", owner, f.nowISO, 0)
	if err != nil || len(records) != 1 || records[0].RootAssetID == "" {
		t.Fatalf("生成谱系列表失败: %+v %v", records, err)
	}
	if _, err := f.store.ListRecentImageGenerations("w13b-conv-gen", owner, "bad", 5); err == nil {
		t.Fatalf("谱系列表非法 now 应报错")
	}
	// generatedAssetExtension。
	if generatedAssetExtension("image/png") != ".png" || generatedAssetExtension("image/jpeg") != ".jpg" || generatedAssetExtension("other") != ".webp" {
		t.Fatalf("生成扩展名失败")
	}
}

func TestW13BCommitAssetsToMessageMatrix(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-commit", owner)
	// 准备就绪资产。
	asset, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-commit", SourceKind: "user_upload", OriginalFilename: "a",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("8", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CompleteChatAssetProcessing(CompleteAssetProcessingInput{
		AssetID: asset.ID, SystemAccountID: owner, ConversationID: "w13b-conv-commit",
		ProcessedMimeType: "image/webp", ProcessedWidth: 16, ProcessedHeight: 16,
		ProcessedBytes: 4, ProcessedSha256: strings.Repeat("9", 64), StorageKey: "kk", Now: f.nowISO,
	}); err != nil {
		t.Fatal(err)
	}
	accepted := f.accept(owner, "w13b-conv-commit", "cmid-1", "问")
	commit := func(assetID string) error {
		tx, err := f.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := f.store.commitChatAssetsToMessage(tx, []string{assetID}, owner, "w13b-conv-commit", accepted.UserMessage.ID, f.nowISO, 30); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := commit(asset.ID); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if err := commit("chat_asset_" + strings.Repeat("e", 32)); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("缺失资产应报错: %v", err)
	}
	if err := commit("bad"); err == nil {
		t.Fatalf("非法资产 ID 应报错")
	}
	// 消息不匹配。
	if err := f.store.commitChatAssetsToMessage(f.db, []string{asset.ID}, owner, "w13b-conv-commit", "missing", f.nowISO, 30); err == nil {
		t.Fatalf("缺失消息应报错")
	}
	// 故障臂：资产行查询失败。
	script.failOnce("SELECT " + assetColumns)
	if err := commit(asset.ID); err == nil {
		t.Fatalf("资产查询故障臂应上抛")
	}
	// 重复提交同资产到同消息：幂等更新仍成功（user_upload turn 相同）。
	if err := commit(asset.ID); err != nil {
		t.Fatalf("重复提交应成功: %v", err)
	}
}

func TestW13BImageGenerationRootAssetIDs(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-root", owner)
	// 空列表直通。
	ids, err := f.store.imageGenerationRootAssetIDs(f.db, nil, "w13b-conv-root", owner)
	if err != nil || len(ids) != 0 {
		t.Fatalf("空输入应返回空: %v", err)
	}
	// 注入 generation 行（含非法 root）。
	if _, err := f.db.Exec(`INSERT INTO chat_image_generations (
		asset_id, conversation_id, system_account_id, operation, model, prompt,
		source_asset_ids_json, root_asset_id, size, quality, output_format, created_at, expires_at
	) VALUES (?, 'w13b-conv-root', ?, 'generate', 'gpt-image-2', 'p', '[]', ?, 'auto', 'auto', 'webp', ?, ?)`,
		"chat_asset_"+strings.Repeat("b", 32), owner, "chat_asset_"+strings.Repeat("c", 32), f.nowISO, "2099-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO chat_image_generations (
		asset_id, conversation_id, system_account_id, operation, model, prompt,
		source_asset_ids_json, root_asset_id, size, quality, output_format, created_at, expires_at
	) VALUES (?, 'w13b-conv-root', ?, 'generate', 'gpt-image-2', 'p', '[]', ?, 'auto', 'auto', 'webp', ?, ?)`,
		"chat_asset_"+strings.Repeat("d", 32), owner, "not-an-id", f.nowISO, "2099-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	ids, err = f.store.imageGenerationRootAssetIDs(f.db, []string{
		"chat_asset_" + strings.Repeat("b", 32), "chat_asset_" + strings.Repeat("d", 32),
	}, "w13b-conv-root", owner)
	if err == nil || !strings.Contains(err.Error(), "聊天资产 ID 无效") {
		t.Fatalf("非法 root 应报错: %v %v", ids, err)
	}
	// 去重 + 去除非法后仅剩合法 root。
	ids, err = f.store.imageGenerationRootAssetIDs(f.db, []string{"chat_asset_" + strings.Repeat("b", 32)}, "w13b-conv-root", owner)
	if err != nil || len(ids) != 1 {
		t.Fatalf("root 提取失败: %v %v", ids, err)
	}
}

func TestW13BLoadCompactionSourcePageMatrix(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-page", owner)
	f.seedTurns(owner, "w13b-conv-page", 3)

	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, "claim", 0, "bad", 10, 1000); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, "claim", -1, f.nowISO, 10, 1000); err == nil {
		t.Fatalf("负游标应报错")
	}
	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, "claim", 0, f.nowISO, 1, 1000); err == nil {
		t.Fatalf("limit 下界应报错")
	}
	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, "claim", 0, f.nowISO, 513, 1000); err == nil {
		t.Fatalf("limit 上界应报错")
	}
	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, "claim", 0, f.nowISO, 10, 0); err == nil {
		t.Fatalf("maxBytes 下界应报错")
	}
	// 无认领。
	page, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, "no-claim", 0, f.nowISO, 10, 1000)
	if err != nil || page != nil {
		t.Fatalf("无认领应返回 nil: %+v %v", page, err)
	}
	// 制造认领（经 ClaimContextCompaction）。
	loaded, err := f.store.LoadModelContext("w13b-conv-page", owner, f.nowISO, 512, 16*1024*1024)
	if err != nil || loaded == nil {
		t.Fatalf("加载上下文失败: %v", err)
	}
	sourceThrough := loaded.Head.NextSequenceNo - 3
	ok, err := f.store.RequestContextCompaction(RequestCompactionInput{
		ConversationID: "w13b-conv-page", SystemAccountID: owner, ExpectedRevision: loaded.Head.ContextRevision,
		SourceThroughSequence: sourceThrough, Now: f.nowISO,
	})
	if err != nil || !ok {
		t.Fatalf("请求压缩失败: %v %v", ok, err)
	}
	stale, _ := shiftInstantISO(f.nowISO, compactionStaleClaimBefore)
	claim, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "w13b-conv-page", SystemAccountID: owner, ExpectedRevision: loaded.Head.ContextRevision,
		SourceThroughSequence: sourceThrough, Now: f.nowISO, StaleClaimBefore: stale,
	})
	if err != nil || claim == nil {
		t.Fatalf("认领失败: %v %v", claim, err)
	}
	// 游标不匹配。
	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, claim.ClaimID, 99, f.nowISO, 10, 1000); err == nil {
		t.Fatalf("游标不匹配应报错")
	}
	// 正常翻页。
	page, err = f.store.LoadCompactionSourcePage("w13b-conv-page", owner, claim.ClaimID, claim.ProgressSequence, f.nowISO, 2, 1000)
	if err != nil || page == nil || len(page.Messages) != 2 || page.NextAfterSequence != 2 {
		t.Fatalf("翻页失败: %+v %v", page, err)
	}
	// 查询故障臂。
	script.failOnce("FROM chat_messages AS source")
	if _, err := f.store.LoadCompactionSourcePage("w13b-conv-page", owner, claim.ClaimID, claim.ProgressSequence, f.nowISO, 10, 1000); err == nil {
		t.Fatalf("翻页查询故障臂应上抛")
	}
	// 释放认领。
	if ok, err := f.store.ReleaseCompactionClaim("w13b-conv-page", owner, claim.ClaimID, f.nowISO); err != nil || !ok {
		t.Fatalf("释放认领失败: %v %v", ok, err)
	}
}

func TestW13BFaultArmsAssetsQueries(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-fa", owner)
	asset, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-fa", SourceKind: "user_upload", OriginalFilename: "a",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("a", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	script.failOnce("FROM chat_assets")
	if _, err := f.store.GetAssetNoExpiryGuard(asset.ID, owner, "w13b-conv-fa"); err == nil {
		t.Fatalf("资产查询故障臂应上抛")
	}
	script.failOnce("SELECT COUNT(*) AS total FROM " + "chat_assets")
	slots, err := f.store.AssertChatAssetUploadSlotAvailable(owner, "w13b-conv-fa", f.nowISO)
	if err == nil {
		t.Fatalf("槽位统计故障臂应上抛: %d", slots)
	}
	script.failOnce("SELECT asset_bytes, asset_count FROM chat_user_asset_usage")
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-fa", OriginalFilename: "b",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("b", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	}); err == nil {
		t.Fatalf("usage 读取故障臂应上抛")
	}
	script.failOnce("INSERT INTO chat_assets")
	if _, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-fa", OriginalFilename: "b",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("b", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	}); err == nil {
		t.Fatalf("资产插入故障臂应上抛")
	}
}

func TestW13BContextStoreValidationArms(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-ctx", owner)

	if _, err := f.store.LoadModelContext("w13b-conv-ctx", owner, f.nowISO, 0, 1000); err == nil {
		t.Fatalf("maxRows 下界应报错")
	}
	if _, err := f.store.LoadModelContext("w13b-conv-ctx", owner, f.nowISO, 513, 1000); err == nil {
		t.Fatalf("maxRows 上界应报错")
	}
	if _, err := f.store.LoadModelContext("w13b-conv-ctx", owner, f.nowISO, 10, 0); err == nil {
		t.Fatalf("maxBytes 下界应报错")
	}
	if _, err := f.store.LoadModelContext("w13b-conv-ctx", owner, f.nowISO, 10, maxContextLoadBytes+1); err == nil {
		t.Fatalf("maxBytes 上界应报错")
	}
	if _, err := f.store.LoadModelContext("w13b-conv-ctx", owner, "bad", 10, 1000); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if head, err := f.store.LoadModelContext("missing", owner, f.nowISO, 10, 1000); err != nil || head != nil {
		t.Fatalf("缺失会话应返回 nil: %+v %v", head, err)
	}
	if _, err := f.store.GetContextHead("missing", owner); err != nil {
		t.Fatalf("缺失 head 应返回 nil: %v", err)
	}
	// InstallContextCheckpoint 校验臂。
	install := func(mutate func(*InstallCheckpointInput)) error {
		input := InstallCheckpointInput{
			ClaimID: "claim", ConversationID: "w13b-conv-ctx", SystemAccountID: owner,
			SourceRevision: 0, SourceThroughSequence: 1, ExpiresAt: "2099-01-01T00:00:00.000Z",
			PayloadDigest: strings.Repeat("a", 64), Now: f.nowISO,
			Entries: []CheckpointEntryInput{{Kind: "task_state", Content: []byte(`{"a":1}`), Provenance: "assistant", TrustLevel: "assistant_derived"}},
		}
		mutate(&input)
		_, err := f.store.InstallContextCheckpoint(input)
		return err
	}
	if err := install(func(i *InstallCheckpointInput) { i.SourceRevision = -1 }); err == nil {
		t.Fatalf("负 revision 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.SourceThroughSequence = 0 }); err == nil {
		t.Fatalf("零 through 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.ExpiresAt = "bad" }); err == nil {
		t.Fatalf("非法过期时间应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.Now = "bad" }); err == nil {
		t.Fatalf("非法 now 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.ExpiresAt = "2000-01-01T00:00:00.000Z" }); err == nil {
		t.Fatalf("已过期 checkpoint 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.CheckpointID = "bad id" }); err == nil {
		t.Fatalf("非法 checkpointId 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.PayloadDigest = "zz" }); err == nil {
		t.Fatalf("非法摘要应报错")
	}
	if err := install(func(i *InstallCheckpointInput) { i.Entries = nil }); err == nil {
		t.Fatalf("空 entries 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) {
		i.Entries = []CheckpointEntryInput{{Kind: "bogus", Content: []byte(`{}`), Provenance: "assistant", TrustLevel: "assistant_derived"}}
	}); err == nil {
		t.Fatalf("未知 kind 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) {
		i.Entries = []CheckpointEntryInput{{Kind: "task_state", Content: []byte(`{}`), Provenance: "bogus", TrustLevel: "assistant_derived"}}
	}); err == nil {
		t.Fatalf("未知 provenance 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) {
		i.Entries = []CheckpointEntryInput{{Kind: "task_state", Content: []byte(`{}`), Provenance: "assistant", TrustLevel: "bogus"}}
	}); err == nil {
		t.Fatalf("未知 trust level 应报错")
	}
	if err := install(func(i *InstallCheckpointInput) {
		i.Entries = []CheckpointEntryInput{{Kind: "task_state", Content: []byte(`{}`), Provenance: "assistant", TrustLevel: "assistant_derived", SourceMessageID: "bad id"}}
	}); err == nil {
		t.Fatalf("非法 sourceMessageId 应报错")
	}
	// 会话不在 compacting 状态 → 冲突。
	if err := install(func(i *InstallCheckpointInput) {}); err == nil {
		t.Fatalf("非压缩状态安装应冲突")
	}
	// 序列化大小臂。
	if err := install(func(i *InstallCheckpointInput) {
		i.Entries = []CheckpointEntryInput{{Kind: "task_state", Content: []byte(`"` + strings.Repeat("x", maxCheckpointEntryBytes) + `"`), Provenance: "assistant", TrustLevel: "assistant_derived"}}
	}); err == nil {
		t.Fatalf("超大 entry 应报错")
	}
}

func TestW13BContextStoreClaimArms(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-claim", owner)
	f.seedTurns(owner, "w13b-conv-claim", 4)
	loaded, err := f.store.LoadModelContext("w13b-conv-claim", owner, f.nowISO, 512, 16*1024*1024)
	if err != nil || loaded == nil {
		t.Fatalf("加载上下文失败: %v", err)
	}
	stale, _ := shiftInstantISO(f.nowISO, compactionStaleClaimBefore)
	claimInput := ClaimCompactionInput{
		ConversationID: "w13b-conv-claim", SystemAccountID: owner,
		ExpectedRevision: loaded.Head.ContextRevision, SourceThroughSequence: loaded.Head.NextSequenceNo - 3,
		Now: f.nowISO, StaleClaimBefore: stale,
	}
	claim, err := f.store.ClaimContextCompaction(claimInput)
	if err != nil || claim == nil {
		t.Fatalf("认领失败: %v %v", claim, err)
	}
	// 重复认领：状态已 compacting 且未过期 → 冲突返回 nil。
	again, err := f.store.ClaimContextCompaction(claimInput)
	if err != nil || again != nil {
		t.Fatalf("重复认领应返回 nil: %+v %v", again, err)
	}
	// 释放后再次认领成功（attempt 递增）。
	if ok, _ := f.store.ReleaseCompactionClaim("w13b-conv-claim", owner, claim.ClaimID, f.nowISO); !ok {
		t.Fatalf("释放失败")
	}
	second, err := f.store.ClaimContextCompaction(claimInput)
	if err != nil || second == nil {
		t.Fatalf("二次认领失败: %+v %v", second, err)
	}
	// 头行缺失。
	missing, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "missing", SystemAccountID: owner, ExpectedRevision: 0,
		SourceThroughSequence: 1, Now: f.nowISO, StaleClaimBefore: stale,
	})
	if err != nil || missing != nil {
		t.Fatalf("缺失会话认领应返回 nil: %+v %v", missing, err)
	}
	// 故障臂。
	script.failOnce("FROM chat_conversations")
	if _, err := f.store.ClaimContextCompaction(ClaimCompactionInput{
		ConversationID: "w13b-conv-claim", SystemAccountID: owner, ExpectedRevision: second.SourceRevision,
		SourceThroughSequence: 1, Now: f.nowISO, StaleClaimBefore: stale,
	}); err == nil {
		t.Fatalf("认领查询故障臂应上抛")
	}
	script.failOnce("SELECT id, system_account_id, context_claim_id")
	if _, err := f.store.findCompactionClaim(f.db, "w13b-conv-claim", owner, "x"); err == nil {
		t.Fatalf("认领读取故障臂应上抛")
	}
	// RequestContextCompaction 前置分支。
	ok, err := f.store.RequestContextCompaction(RequestCompactionInput{
		ConversationID: "missing", SystemAccountID: owner, ExpectedRevision: 0, SourceThroughSequence: 1, Now: f.nowISO,
	})
	if err != nil || ok {
		t.Fatalf("缺失会话请求压缩应返回 false: %v %v", ok, err)
	}
	if _, err := f.store.RecordContextUsage(RecordContextUsageInput{
		ConversationID: "missing", SystemAccountID: owner, ExpectedContextRevision: 0, ActiveContextTokens: 1, Now: f.nowISO,
	}); err != nil {
		t.Fatalf("usage 记录不应报错: %v", err)
	}
	if _, err := f.store.FailCompaction(FailCompactionInput{ConversationID: "x", SystemAccountID: owner, ClaimID: "c", ErrorCode: strings.Repeat("x", 200), Now: f.nowISO}); err == nil {
		t.Fatalf("超长错误码应报错")
	}
	if _, err := f.store.FailPendingCompaction(FailPendingCompactionInput{ConversationID: "x", SystemAccountID: owner, ExpectedRevision: 0, ErrorCode: "bad\x01code", Now: f.nowISO}); err == nil {
		t.Fatalf("含控制符错误码应报错")
	}
}

func TestW13BSerializableContextCheckpoint(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	// serializeCheckpointEntries 直接矩阵。
	_, err := serializeCheckpointEntries([]CheckpointEntryInput{
		{Kind: "task_state", Content: []byte(`{"a":1}`), Provenance: "assistant", TrustLevel: "assistant_derived", SourceMessageID: "msg_1"},
	}, "cp", "conv", f.nowISO, "2099-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	entries, err := serializeCheckpointEntries([]CheckpointEntryInput{
		{Kind: "verbatim", Content: []byte(`{"b":2}`), Provenance: "user", TrustLevel: "untrusted"},
	}, "cp", "conv", f.nowISO, "2099-01-01T00:00:00.000Z")
	if err != nil || len(entries) != 1 || entries[0].sourceMessageID.Valid {
		t.Fatalf("无来源序列化失败: %v", err)
	}
	_ = owner
}

func TestW13BCompactionFaultArms(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-cf", owner)
	f.seedTurns(owner, "w13b-conv-cf", 4)
	executor := &mockExecutor{}
	service := NewCompactionService(f.store, executor, func(text string) int { return len(text) / 4 }, func() string { return f.nowISO })
	input := CompactionInput{ConversationID: "w13b-conv-cf", SystemAccountID: owner, Model: "gpt-5"}

	// LoadModelContext 故障。
	script.failOnce("FROM chat_conversations")
	if result := service.CompactOnce(context.Background(), input); result.Status != "failed" {
		t.Fatalf("LoadModelContext 故障应失败: %+v", result)
	}
	// RequestContextCompaction 故障（请求 UPDATE 失败）。
	script.failOnce("AND ? > compacted_through_sequence AND ? <= next_sequence_no - 3")
	if result := service.CompactOnce(context.Background(), input); result.Status != "failed" {
		t.Fatalf("请求压缩故障应失败: %+v", result)
	}
	// ClaimContextCompaction 故障 → FailPendingCompaction 分支（Request 成功）。
	script.failOnce("WHERE id = ? AND system_account_id = ? AND context_revision = ?")
	if result := service.CompactOnce(context.Background(), input); result.Status != "failed" {
		t.Fatalf("认领故障应失败: %+v", result)
	}
	// compacting 且认领未过期：Request 不命中 → compaction_conflict skipped。
	var revision, nextSeq int64
	if err := f.db.QueryRow(`SELECT context_revision, next_sequence_no FROM chat_conversations WHERE id = 'w13b-conv-cf'`).Scan(&revision, &nextSeq); err != nil {
		t.Fatal(err)
	}
	claimThrough := nextSeq - 3
	if _, err := f.db.Exec(`UPDATE chat_conversations SET context_state = 'compacting',
		context_claim_id = 'w13b-claim-x', context_claim_revision = ?,
		context_claim_through_sequence = ?, context_claimed_at = ?,
		context_progress_sequence = 0 WHERE id = 'w13b-conv-cf'`, revision, claimThrough, f.nowISO); err != nil {
		t.Fatal(err)
	}
	if result := service.CompactOnce(context.Background(), input); result.Status != "skipped" {
		t.Fatalf("认领冲突应跳过: %+v", result)
	}
	// 无可压缩区间（独立 fixture）。
	f2 := newChatFixture(t)
	service2 := NewCompactionService(f2.store, executor, func(text string) int { return len(text) / 4 }, func() string { return f2.nowISO })
	f2.createConversation("w13b-conv-cf2", owner)
	f2.seedTurns(owner, "w13b-conv-cf2", 1)
	if result := service2.CompactOnce(context.Background(), input); result.Status != "skipped" {
		t.Fatalf("无可压缩应跳过: %+v", result)
	}
}

func TestW13BSummarizePageProtocolBodies(t *testing.T) {
	var seenPaths []string
	summaryPayload := `{"choices":[{"message":{"content":"` + `{\"currentGoal\":\"g\",\"recentUserIntent\":\"i\"}` + `"}}],"output_text":"` + `{\"currentGoal\":\"g\",\"recentUserIntent\":\"i\"}` + `"}`
	executor := &pathRecorderExecutorW13B{paths: &seenPaths, body: summaryPayload}
	service := NewCompactionService(newChatFixture(t).store, executor, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	if _, err := service.summarizePage(context.Background(), CompactionInput{Model: "m", Protocol: ProtocolChatCompletions}, emptySnapshot(), []any{}); err != nil {
		t.Fatalf("chat 协议总结失败: %v", err)
	}
	if _, err := service.summarizePage(context.Background(), CompactionInput{Model: "m", Protocol: ProtocolResponses}, emptySnapshot(), []any{map[string]any{"role": "user", "content": "内容"}}); err != nil {
		t.Fatalf("responses 协议总结失败: %v", err)
	}
	if len(seenPaths) != 2 || seenPaths[0] != "/v1/chat/completions" || seenPaths[1] != "/v1/responses" {
		t.Fatalf("协议路径不正确: %v", seenPaths)
	}
	// 执行器错误与空响应：阻塞执行器用已取消的 context 快速返回。
	failService := NewCompactionService(newChatFixture(t).store, &mockExecutor{steps: []scriptStep{{
		match:   func(call dispatchCall) bool { return true },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return nil },
	}}}, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	canceledCtx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx()
	if _, err := failService.summarizePage(canceledCtx, CompactionInput{Model: "m"}, emptySnapshot(), nil); err == nil {
		t.Fatalf("阻塞执行器应随取消返回错误")
	}
	errorService := NewCompactionService(newChatFixture(t).store, &failingExecutorW13B{}, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	if _, err := errorService.summarizePage(context.Background(), CompactionInput{Model: "m"}, emptySnapshot(), nil); err == nil {
		t.Fatalf("执行器错误应上抛")
	}
	// HTTP 500。
	httpService := NewCompactionService(newChatFixture(t).store, &mockExecutor{steps: []scriptStep{{
		match:   func(call dispatchCall) bool { return true },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(500, "{}") },
	}}}, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	if _, err := httpService.summarizePage(context.Background(), CompactionInput{Model: "m"}, emptySnapshot(), nil); err == nil || !strings.Contains(err.Error(), "chat_context_model_http_500") {
		t.Fatalf("HTTP 500 应报错: %v", err)
	}
	// 非法 JSON。
	badService := NewCompactionService(newChatFixture(t).store, &mockExecutor{steps: []scriptStep{{
		match:   func(call dispatchCall) bool { return true },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(200, "not json") },
	}}}, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	if _, err := badService.summarizePage(context.Background(), CompactionInput{Model: "m"}, emptySnapshot(), nil); err == nil {
		t.Fatalf("非法响应应报错")
	}
	// 缺失文本。
	emptyService := NewCompactionService(newChatFixture(t).store, &mockExecutor{steps: []scriptStep{{
		match:   func(call dispatchCall) bool { return true },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return jsonStatusResponse(200, `{"choices":[]}`) },
	}}}, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	if _, err := emptyService.summarizePage(context.Background(), CompactionInput{Model: "m"}, emptySnapshot(), nil); err == nil {
		t.Fatalf("空响应应报错")
	}
	// 围栏 JSON 成功回填。
	fenced := NewCompactionService(newChatFixture(t).store, &mockExecutor{steps: []scriptStep{{
		match: func(call dispatchCall) bool { return true },
		respond: func(call dispatchCall) *GenerationDispatchResponse {
			fence := "```json\n" + `{"currentGoal":"目标","recentUserIntent":"意图"}` + "\n```"
			encoded, _ := json.Marshal(fence)
			return jsonStatusResponse(200, `{"output_text":`+string(encoded)+`}`)
		},
	}}}, func(text string) int { return 1 }, func() string { return "2026-03-10T08:00:00.000Z" })
	snapshot, err := fenced.summarizePage(context.Background(), CompactionInput{Model: "m", Protocol: ProtocolResponses}, emptySnapshot(), []any{map[string]any{"role": "user", "content": "用户消息"}})
	if err != nil || snapshot.CurrentGoal != "目标" {
		t.Fatalf("围栏总结失败: %+v %v", snapshot, err)
	}
}

type pathRecorderExecutorW13B struct {
	paths *[]string
	body  string
}

func (p *pathRecorderExecutorW13B) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	*p.paths = append(*p.paths, req.Path)
	return jsonStatusResponse(200, p.body), nil
}

type failingExecutorW13B struct{}

func (failingExecutorW13B) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	return nil, errors.New("dispatch boom")
}

func TestW13BEnrichSourceMessages(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-enrich", owner)
	executor := &mockExecutor{}
	service := NewCompactionService(f.store, executor, func(text string) int { return 1 }, func() string { return f.nowISO })
	input := CompactionInput{ConversationID: "w13b-conv-enrich", SystemAccountID: owner}

	// 非法 blocks JSON → 跳过。
	out, err := service.enrichSourceMessages(input, []contextSourceMessage{{contentBlocksJSON: "{bad}", role: "user"}})
	if err != nil || len(out) != 1 {
		t.Fatalf("非法 blocks 应跳过: %v %v", out, err)
	}
	// 资产待观察 → 报错。
	if _, err := service.enrichSourceMessages(input, []contextSourceMessage{{
		contentBlocksJSON: `[{"type":"input_image","assetId":"chat_asset_` + strings.Repeat("1", 32) + `"}]`, role: "user",
	}}); err == nil || !strings.Contains(err.Error(), "observation_pending") {
		t.Fatalf("待观察资产应报错: %v", err)
	}
	// 就绪资产 + observation。
	asset, err := f.store.CreateChatAsset(CreateChatAssetInput{
		SystemAccountID: owner, ConversationID: "w13b-conv-enrich", SourceKind: "user_upload", OriginalFilename: "a",
		OriginalMimeType: "image/png", OriginalBytes: 1, OriginalSha256: strings.Repeat("c", 64),
		QuotaBytes: 1, Now: f.nowISO, RetentionDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE chat_assets SET processing_status = 'ready', observation_status = 'ready',
		observation_json = ? WHERE id = ?`, `{"summary":"一只猫"}`, asset.ID); err != nil {
		t.Fatal(err)
	}
	out, err = service.enrichSourceMessages(input, []contextSourceMessage{{
		contentBlocksJSON: `[{"type":"input_image","assetId":"` + asset.ID + `"}]`, role: "user", contentText: "看图",
	}})
	if err != nil || len(out) != 1 {
		t.Fatalf("enrich 失败: %v %v", out, err)
	}
	rendered := out[0].(map[string]any)
	blocks := rendered["blocks"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("渲染块缺失")
	}
	observation := blocks[0].(map[string]any)["observation"].(map[string]any)
	if observation["summary"] != "一只猫" {
		t.Fatalf("observation 注入失败: %v", observation)
	}
	// 就绪资产 + 非图块保留原样。
	out, err = service.enrichSourceMessages(input, []contextSourceMessage{{
		contentBlocksJSON: `[{"type":"input_text","text":"hi"}]`, role: "user", contentText: "文本",
	}})
	if err != nil || len(out) != 1 {
		t.Fatalf("非图块 enrich 失败: %v", err)
	}
}

func newFaultChatFixtureW13B(t *testing.T) (*chatFixture, *faultScriptW10D) {
	return newFaultChatFixtureW10D(t)
}

func fixtureStoreClockW13B(f *chatFixture) func() time.Time {
	_, clock := fixedChatClock()
	return clock
}

func fNowISO(f *chatFixture) string { return f.nowISO }

func TestW13BCommitFaultArmsDeletion(t *testing.T) {
	fixture, script := newFaultChatFixtureW13B(t)
	f := fixture
	owner := "w13b-owner"
	f.createConversation("w13b-conv-del", owner)
	accepted := f.accept(owner, "w13b-conv-del", "cmid-1", "问")
	f.complete(owner, "w13b-conv-del", accepted.TurnID, "答")

	// DeleteConversation 故障臂。
	script.failOnce("SELECT COUNT(*) AS total FROM " + "chat_conversations")
	if _, err := f.store.CreateConversation(CreateConversationInput{
		SystemAccountID: owner, APIKeyID: "k", APIKeyNameSnapshot: "n", Now: f.nowISO, MaxConversationsPerUser: 1,
	}); err == nil {
		t.Fatalf("会话计数故障臂应上抛")
	}
	script.failOnce("DELETE FROM chat_user_storage_windows")
	if err := f.store.releaseConversationStorageAndExpireAssets(f.db, "w13b-conv-del", owner, f.nowISO); err == nil {
		t.Fatalf("窗口删除故障臂应上抛")
	}
	script.failOnce("DELETE FROM chat_assets")
	if _, err := f.db.Exec("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	_ = script
	// ClearConversation / DeleteConversation 正常路径。
	cleared, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "w13b-conv-del", SystemAccountID: owner, Now: f.nowISO})
	if err != nil || cleared == nil {
		t.Fatalf("清空失败: %+v %v", cleared, err)
	}
	if _, err := f.store.ClearConversation(ClearConversationInput{ConversationID: "missing", SystemAccountID: owner, Now: f.nowISO}); err != nil {
		t.Fatalf("清空缺失应返回 nil: %v", err)
	}
	deleted, err := f.store.DeleteConversation("w13b-conv-del", owner)
	if err != nil || !deleted {
		t.Fatalf("删除失败: %v %v", deleted, err)
	}
	if deletedAgain, err := f.store.DeleteConversation("w13b-conv-del", owner); err != nil || deletedAgain {
		t.Fatalf("重复删除应返回 false: %v %v", deletedAgain, err)
	}
}

func TestW13BLoadContextTruncationArms(t *testing.T) {
	f := newChatFixture(t)
	owner := "w13b-owner"
	f.createConversation("w13b-conv-trunc", owner)
	f.seedTurns(owner, "w13b-conv-trunc", 4)
	// maxRows=2 时后缀窗口受限 → truncated。
	loaded, err := f.store.LoadModelContext("w13b-conv-trunc", owner, f.nowISO, 2, 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Complete {
		t.Fatalf("行预算受限应标记截断")
	}
	if loaded.TruncatedAt == nil || *loaded.TruncatedAt != "suffix_messages" {
		t.Fatalf("截断点不正确: %v", loaded.TruncatedAt)
	}
	// 极小字节预算。
	tiny, err := f.store.LoadModelContext("w13b-conv-trunc", owner, f.nowISO, 512, 8)
	if err != nil {
		t.Fatal(err)
	}
	if tiny.Complete || len(tiny.Suffix) != 0 {
		t.Fatalf("字节预算受限应为空后缀: %+v", tiny)
	}
}

