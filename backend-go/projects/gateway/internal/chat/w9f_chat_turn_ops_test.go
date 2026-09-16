package chat

// w9f 覆盖收尾（第三批）：stop/contextStatus/attachStream/submission 直驱。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func w9fInvokeJSON(rt *chatRoutes, method, conversationID, payload string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/x", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
	recorder := httptest.NewRecorder()
	switch method {
	case "POST":
		rt.stopTurn(recorder, request)
	case "GET":
		rt.contextStatus(recorder, request)
	}
	return recorder
}

func TestW9FStopTurnValidationAndFlows(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_stop", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)

	// 非法 JSON / 非对象。
	if recorder := w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{bad`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", recorder.Code)
	}
	if recorder := w9fInvokeJSON(rt, "POST", "chat_conv_stop", `[]`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("array body = %d", recorder.Code)
	}
	// 字段类型错误。
	if recorder := w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{"turnId":5}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad turnId type = %d", recorder.Code)
	}
	// 未知键。
	if recorder := w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{"bogus":1}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d", recorder.Code)
	}
	// 缺少标识。
	if recorder := w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing identifier = %d", recorder.Code)
	}
	// 未鉴权兜底 → 500（Node 契约）。
	request := httptest.NewRequest("POST", "/x", strings.NewReader(`{"turnId":"t1"}`))
	request.SetPathValue("conversationId", "chat_conv_stop")
	recorder := httptest.NewRecorder()
	rt.stopTurn(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth stop = %d", recorder.Code)
	}
	// 会话不存在 → 404。
	recorder = w9fInvokeJSON(rt, "POST", "chat_conv_missing", `{"turnId":"t1"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing conversation = %d", recorder.Code)
	}
	// 未知 clientMessageId（无 turnId、无准备）→ 404 not found。
	recorder = w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{"clientMessageId":"cmid-none"}`)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "chat_generation_not_found") {
		t.Fatalf("no match = %d %s", recorder.Code, recorder.Body.String())
	}
	// 有匹配的准备 → 取消准备 202。
	prep := rt.claimPreparation("chat_conv_stop", routeTestOwner, "cmid-live")
	if prep == nil {
		t.Fatal("claim prep")
	}
	recorder = w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{"clientMessageId":"cmid-live"}`)
	if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), `"preparing"`) {
		t.Fatalf("cancel prep = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deletePreparationIfMatches("chat_conv_stop", prep.token)
	// 已取消的准备 → 再次取消失败 → 404。
	recorder = w9fInvokeJSON(rt, "POST", "chat_conv_stop", `{"clientMessageId":"cmid-none"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("second cancel = %d", recorder.Code)
	}
}

func TestW9FStopTurnAcceptedTurnFlows(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_stop2", routeTestOwner)
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_stop2", "cmid-accept", "问题")
	if accepted == nil {
		t.Fatal("accept turn")
	}
	rt := newChatRoutesForTest(env.deps)

	// turnId 与 clientMessageId 指向不同轮次 → 409 turn mismatch。
	recorder := w9fInvokeJSON(rt, "POST", "chat_conv_stop2",
		`{"turnId":"chat_turn_other","clientMessageId":"cmid-accept"}`)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_turn_mismatch") {
		t.Fatalf("mismatch = %d %s", recorder.Code, recorder.Body.String())
	}

	// clientMessageId 命中已接受轮次且未在运行 → 走存储取消。
	recorder = w9fInvokeJSON(rt, "POST", "chat_conv_stop2", `{"clientMessageId":"cmid-accept"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel accepted = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW9FSubmissionStatusAcceptedAndPreparing(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_sub2", routeTestOwner)
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_sub2", "cmid-sub", "问题")
	if accepted == nil {
		t.Fatal("accept turn")
	}
	rt := newChatRoutesForTest(env.deps)
	invoke := func(conversationID, clientMessageID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/submission", nil)
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("clientMessageId", clientMessageID)
		request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
		recorder := httptest.NewRecorder()
		rt.submissionStatus(recorder, request)
		return recorder
	}
	recorder := invoke("chat_conv_sub2", "cmid-sub")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"accepted"`) {
		t.Fatalf("accepted = %d %s", recorder.Code, recorder.Body.String())
	}
	// 准备中状态。
	prep := rt.claimPreparation("chat_conv_sub2", routeTestOwner, "cmid-prep")
	recorder = invoke("chat_conv_sub2", "cmid-prep")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"preparing"`) {
		t.Fatalf("preparing = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deletePreparationIfMatches("chat_conv_sub2", prep.token)
}

func TestW9FContextStatusAndAttachStream(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_ctx", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)

	// 200：有上下文头。
	recorder := w9fInvokeJSON(rt, "GET", "chat_conv_ctx", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("context status = %d %s", recorder.Code, recorder.Body.String())
	}
	// 404。
	recorder = w9fInvokeJSON(rt, "GET", "chat_conv_missing", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("context missing = %d", recorder.Code)
	}
	// 未鉴权 → 500。
	request := httptest.NewRequest("GET", "/x", nil)
	request.SetPathValue("conversationId", "chat_conv_ctx")
	recorder = httptest.NewRecorder()
	rt.contextStatus(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth context = %d", recorder.Code)
	}

	// attachStream：无活跃轮次 → 409 chat_stream_terminal。
	invokeAttach := func(conversationID, turnID string) *httptest.ResponseRecorder {
		attachRequest := httptest.NewRequest("GET", "/attach", nil)
		attachRequest.SetPathValue("conversationId", conversationID)
		attachRequest.SetPathValue("turnId", turnID)
		attachRequest = attachRequest.WithContext(authsys.WithAuthContext(attachRequest.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
		attachRecorder := httptest.NewRecorder()
		rt.attachStream(attachRecorder, attachRequest)
		return attachRecorder
	}
	if recorder := invokeAttach("chat_conv_ctx", "chat_turn_none"); recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_stream_terminal") {
		t.Fatalf("attach terminal = %d %s", recorder.Code, recorder.Body.String())
	}
	// 活跃轮次但 runner 缺失 → FailInterruptedTurn + chat_stream_runner_missing。
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'chat_turn_w9f_live', active_started_at = '2026-03-10T08:00:00.000Z' WHERE id = 'chat_conv_ctx'`); err != nil {
		t.Fatalf("set active: %v", err)
	}
	recorder = invokeAttach("chat_conv_ctx", "chat_turn_w9f_live")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_stream_runner_missing") {
		t.Fatalf("attach runner missing = %d %s", recorder.Code, recorder.Body.String())
	}
	// 会话不存在 → 404。
	if recorder := invokeAttach("chat_conv_missing", "t"); recorder.Code != http.StatusNotFound {
		t.Fatalf("attach missing = %d", recorder.Code)
	}
}
