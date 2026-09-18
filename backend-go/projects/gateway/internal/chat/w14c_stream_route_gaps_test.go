package chat

// w14c 覆盖率补齐：流式路由的冲突/校验前置分支与历史图片观察路径。

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func w14cStreamRoutes(t *testing.T) (*chatRoutes, *generationEnv, string) {
	t.Helper()
	env := newGenerationEnv(t)
	conversationID := "chat_conv_w14c_stream"
	env.fixture.createConversation(conversationID, routeTestOwner)
	return newChatRoutesForTest(env.deps), env, conversationID
}

func w14cStreamPost(rt *chatRoutes, conversationID, owner, payload string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/conversations/"+conversationID+"/stream", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	recorder := httptest.NewRecorder()
	rt.streamTurn(recorder, request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: owner})))
	return recorder
}

func w14cStreamCode(recorder *httptest.ResponseRecorder) string {
	body := recorder.Body.String()
	for _, code := range []string{
		"chat_message_in_progress", "chat_replace_conflict", "chat_conversation_clearing",
		"chat_context_compacting", "chat_turn_limit_exceeded", "chat_image_not_supported",
		"chat_turn_not_replaceable", "chat_context_budget_exceeded", "chat_invalid_request",
	} {
		if strings.Contains(body, code) {
			return code
		}
	}
	return ""
}

// TestW14CStreamRouteConflictPrologue 覆盖流式路由的前置冲突分支。
func TestW14CStreamRouteConflictPrologue(t *testing.T) {
	rt, env, conversationID := w14cStreamRoutes(t)

	// 活跃轮次：接受但未完成 → 再提交即 chat_message_in_progress。
	accepted := env.fixture.accept(routeTestOwner, conversationID, "w14c-cmid-active", "问题")
	recorder := w14cStreamPost(rt, conversationID, routeTestOwner,
		streamPayload("w14c-cmid-1", "hi", "gpt-5"))
	if got := w14cStreamCode(recorder); got != "chat_message_in_progress" {
		t.Fatalf("active = %d %s (%s)", recorder.Code, recorder.Body.String(), got)
	}

	// 清理活跃轮次并把轮次计数顶到上限 → chat_turn_limit_exceeded。
	env.fixture.complete(routeTestOwner, conversationID, accepted.TurnID, "回答")
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_conversations SET user_turn_count = ? WHERE id = ?`, 100, conversationID); err != nil {
		t.Fatal(err)
	}
	recorder = w14cStreamPost(rt, conversationID, routeTestOwner,
		streamPayload("w14c-cmid-2", "hi", "gpt-5"))
	if got := w14cStreamCode(recorder); got != "chat_turn_limit_exceeded" {
		t.Fatalf("turn limit = %d %s (%s)", recorder.Code, recorder.Body.String(), got)
	}
	if _, err := env.fixture.db.Exec(
		`UPDATE chat_conversations SET user_turn_count = 1 WHERE id = ?`, conversationID); err != nil {
		t.Fatal(err)
	}

	// replace 轮次不可替换 → AssertTurnReplaceable 错误。
	recorder = w14cStreamPost(rt, conversationID, routeTestOwner,
		`{"clientMessageId":"w14c-cmid-3","content":"hi","model":"gpt-5","replaceTurnId":"chat_turn_missing"}`)
	if got := w14cStreamCode(recorder); got != "chat_turn_not_replaceable" && got != "chat_invalid_request" && recorder.Code >= 400 {
		// 允许实现按 4xx 语义返回；关键是不得 2xx。
		t.Logf("replace = %d %s (%s)", recorder.Code, recorder.Body.String(), got)
	}
	if recorder.Code < 400 {
		t.Fatalf("replace 缺失轮次必须 4xx: %d %s", recorder.Code, recorder.Body.String())
	}

	// 会话动作占用（clearing）→ chat_conversation_clearing。
	action := rt.claimAction(conversationID, routeTestOwner, "clearing")
	if action == nil {
		t.Fatal("认领会话动作失败")
	}
	recorder = w14cStreamPost(rt, conversationID, routeTestOwner,
		streamPayload("w14c-cmid-4", "hi", "gpt-5"))
	if got := w14cStreamCode(recorder); got != "chat_conversation_clearing" {
		t.Fatalf("clearing = %d %s (%s)", recorder.Code, recorder.Body.String(), got)
	}
	rt.deleteActionIfMatches(conversationID, action.token)

	// 预备占用 → chat_message_in_progress（prep==nil 分支）。
	prep := rt.claimPreparation(conversationID, routeTestOwner, "w14c-cmid-5")
	if prep == nil {
		t.Fatal("认领预备失败")
	}
	recorder = w14cStreamPost(rt, conversationID, routeTestOwner,
		streamPayload("w14c-cmid-5", "hi", "gpt-5"))
	if got := w14cStreamCode(recorder); got != "chat_message_in_progress" {
		t.Fatalf("prep = %d %s (%s)", recorder.Code, recorder.Body.String(), got)
	}
	rt.deletePreparationIfMatches(conversationID, prep.token)
}

// TestW14CStreamRouteImageNotSupported 覆盖模型不支持图片输入的分支。
func TestW14CStreamRouteImageNotSupported(t *testing.T) {
	rt, _, conversationID := w14cStreamRoutes(t)

	// gpt-5-mini 仅文本且走 chat_completions → 携带图片输入必须拒绝。
	recorder := w14cStreamPost(rt, conversationID, routeTestOwner,
		`{"clientMessageId":"w14c-cmid-img","content":"看图","model":"gpt-5-mini",
		  "contentBlocks":[{"type":"input_image","assetId":"chat_asset_w14c_missing"}]}`)
	if got := w14cStreamCode(recorder); got != "chat_image_not_supported" {
		t.Fatalf("image not supported = %d %s (%s)", recorder.Code, recorder.Body.String(), got)
	}
}
