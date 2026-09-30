package chat

// BUG-0240 回归：FailChatTurn / CancelChatTurn 必须把 runner 终态化的
// ContentBlocksRaw（terminalizeAssistantBlocks 产物，含 tool_call /
// reasoning 块）透传给 finalizeTurn 落库；此前两个入口漏传该字段，失败/
// 取消轮次的 content_blocks_json 恒为 "[]"。同文件覆盖 raw 为空时的
// typed 序列化回归（nil blocks -> "[]"，与修复前行为一致）。

import (
	"database/sql"
	"testing"
)

func assistantBlocksJSON(t *testing.T, db *sql.DB, conversationID, turnID string) string {
	t.Helper()
	var persisted string
	if err := db.QueryRow(`SELECT content_blocks_json FROM chat_messages
		WHERE conversation_id = ? AND turn_id = ? AND role = 'assistant'`, conversationID, turnID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	return persisted
}

// failedRawBlocks mirrors terminalizeAssistantBlocks output for a failed turn:
// reasoning 与 tool_call 块被终态化为 status=failed。
const failedRawBlocks = `[{"type":"reasoning","blockId":"blk_rsn","order":1,"text":"思考中","status":"failed"},` +
	`{"type":"tool_call","blockId":"blk_tool","order":2,"callId":"call_1","toolType":"web_search","status":"failed",` +
	`"item":{"query":"kw","arguments":{"q":"x"},"failure":"provider_error"}}]`

const canceledRawBlocks = `[{"type":"reasoning","blockId":"blk_rsn","order":1,"text":"思考中","status":"canceled"},` +
	`{"type":"output_text","blockId":"blk_txt","order":2,"text":"部分回答","status":"canceled"}]`

func TestFailChatTurnPersistsContentBlocksRaw(t *testing.T) {
	f := newChatFixture(t)
	owner := "bug0240-owner"
	f.createConversation("bug0240-conv-fail", owner)
	accepted := f.accept(owner, "bug0240-conv-fail", "cmid-1", "问题")

	message, err := f.store.FailChatTurn(FailTurnInput{
		ConversationID:   "bug0240-conv-fail",
		SystemAccountID:  owner,
		TurnID:           accepted.TurnID,
		AssistantContent: "部分回答",
		ErrorCode:        "provider_error",
		ErrorMessage:     "上游错误",
		ContentBlocksRaw: []byte(failedRawBlocks),
		Now:              f.nowISO,
	})
	if err != nil {
		t.Fatalf("FailChatTurn 失败: %v", err)
	}
	if message == nil || message.Status != StatusFailed {
		t.Fatalf("应为 failed 终态: %+v", message)
	}
	persisted := assistantBlocksJSON(t, f.db, "bug0240-conv-fail", accepted.TurnID)
	if persisted != failedRawBlocks {
		t.Fatalf("content_blocks_json 应与输入 raw 一致:\n got: %s\nwant: %s", persisted, failedRawBlocks)
	}
	fact, err := f.store.FindTurnByClientMessageID("bug0240-conv-fail", owner, "cmid-1")
	if err != nil || fact == nil {
		t.Fatalf("回读轮次事实失败: %v", err)
	}
	if fact.AssistantStatus != StatusFailed || fact.ErrorCode == nil || *fact.ErrorCode != "provider_error" {
		t.Fatalf("failed 语义不应被破坏: %+v", fact)
	}
}

func TestCancelChatTurnPersistsContentBlocksRaw(t *testing.T) {
	f := newChatFixture(t)
	owner := "bug0240-owner"
	f.createConversation("bug0240-conv-cancel", owner)
	accepted := f.accept(owner, "bug0240-conv-cancel", "cmid-1", "问题")

	message, err := f.store.CancelChatTurn(CancelTurnInput{
		ConversationID:   "bug0240-conv-cancel",
		SystemAccountID:  owner,
		TurnID:           accepted.TurnID,
		AssistantContent: "部分回答",
		ContentBlocksRaw: []byte(canceledRawBlocks),
		Now:              f.nowISO,
	})
	if err != nil {
		t.Fatalf("CancelChatTurn 失败: %v", err)
	}
	if message == nil || message.Status != StatusCanceled {
		t.Fatalf("应为 canceled 终态: %+v", message)
	}
	persisted := assistantBlocksJSON(t, f.db, "bug0240-conv-cancel", accepted.TurnID)
	if persisted != canceledRawBlocks {
		t.Fatalf("content_blocks_json 应与输入 raw 一致:\n got: %s\nwant: %s", persisted, canceledRawBlocks)
	}
}

func TestFinalizeTurnEmptyRawFallsBackToTypedSerialization(t *testing.T) {
	f := newChatFixture(t)
	owner := "bug0240-owner"

	// FailChatTurn：raw 为空且 typed blocks 为 nil → "[]"（修复前行为）。
	f.createConversation("bug0240-conv-empty-fail", owner)
	failTurn := f.accept(owner, "bug0240-conv-empty-fail", "cmid-f", "问题")
	if _, err := f.store.FailChatTurn(FailTurnInput{
		ConversationID: "bug0240-conv-empty-fail", SystemAccountID: owner, TurnID: failTurn.TurnID,
		AssistantContent: "失败", ErrorCode: "x", ErrorMessage: "y", Now: f.nowISO,
	}); err != nil {
		t.Fatalf("FailChatTurn 失败: %v", err)
	}
	if persisted := assistantBlocksJSON(t, f.db, "bug0240-conv-empty-fail", failTurn.TurnID); persisted != "[]" {
		t.Fatalf("raw 为空应落 []: %s", persisted)
	}

	// CancelChatTurn：同上。
	f.createConversation("bug0240-conv-empty-cancel", owner)
	cancelTurn := f.accept(owner, "bug0240-conv-empty-cancel", "cmid-c", "问题")
	if _, err := f.store.CancelChatTurn(CancelTurnInput{
		ConversationID: "bug0240-conv-empty-cancel", SystemAccountID: owner, TurnID: cancelTurn.TurnID,
		AssistantContent: "取消", Now: f.nowISO,
	}); err != nil {
		t.Fatalf("CancelChatTurn 失败: %v", err)
	}
	if persisted := assistantBlocksJSON(t, f.db, "bug0240-conv-empty-cancel", cancelTurn.TurnID); persisted != "[]" {
		t.Fatalf("raw 为空应落 []: %s", persisted)
	}
}
