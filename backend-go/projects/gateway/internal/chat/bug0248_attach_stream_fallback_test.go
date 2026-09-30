package chat

// BUG-0248（第 4 项）回归：attachStream 不得忽略 AttachStream 返回值。
// 订阅窗口内 runner 恰好终态被移除（Subscribe 失败返回 false、未写任何
// 响应字节）时，路由必须回落既有终态判定输出 409，而不是静默返回空 200。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

type b0248StubRunner struct{}

func (b0248StubRunner) Abort() bool { return false }

// b0248AlwaysActiveRegistry 模拟 Get 命中后订阅窗口内 runner 被移除的竞态：
// Get 恒报活跃，真正的失败由 AttachStream 返回 false 表达。
type b0248AlwaysActiveRegistry struct{}

func (b0248AlwaysActiveRegistry) Snapshot(ownerID, conversationID, turnID string) GenerationSnapshot {
	return GenerationSnapshot{State: "running"}
}

func (b0248AlwaysActiveRegistry) Get(ownerID, conversationID, turnID string) (GenerationRunner, bool) {
	return b0248StubRunner{}, true
}

func newB0248Routes(t *testing.T, attach AttachStreamHandler) (*chatRoutes, *chatFixture) {
	t.Helper()
	fixture := newChatFixture(t)
	deps := &Deps{
		Store:        fixture.store,
		Generations:  b0248AlwaysActiveRegistry{},
		AttachStream: attach,
	}
	return newChatRoutesForTest(deps), fixture
}

func invokeB0248Attach(rt *chatRoutes, conversationID, turnID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/attach", nil)
	request.SetPathValue("conversationId", conversationID)
	request.SetPathValue("turnId", turnID)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
	recorder := httptest.NewRecorder()
	rt.attachStream(recorder, request)
	return recorder
}

// TestBug0248AttachStreamFalseFallsBackToRunnerMissing：AttachStream 返回
// false 且 store 无轮次记录 → 409 chat_stream_runner_missing（修复前为空 200）。
func TestBug0248AttachStreamFalseFallsBackToRunnerMissing(t *testing.T) {
	rt, fixture := newB0248Routes(t, func(w http.ResponseWriter, r *http.Request, identity GenerationIdentity) bool {
		return false
	})
	fixture.createConversation("b0248_conv", routeTestOwner)
	if _, err := fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'b0248_turn', active_started_at = ? WHERE id = 'b0248_conv'`, fixture.nowISO); err != nil {
		t.Fatalf("set active turn: %v", err)
	}
	recorder := invokeB0248Attach(rt, "b0248_conv", "b0248_turn")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_stream_runner_missing") {
		t.Fatalf("AttachStream=false 应回落 409 runner_missing，实际 = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("响应体不得为空")
	}
}

// TestBug0248AttachStreamFalseFallsBackToTerminal：AttachStream 返回 false
// 且轮次在 store 已终态 → 409 chat_stream_terminal（终态回落判定生效）。
func TestBug0248AttachStreamFalseFallsBackToTerminal(t *testing.T) {
	rt, fixture := newB0248Routes(t, func(w http.ResponseWriter, r *http.Request, identity GenerationIdentity) bool {
		return false
	})
	fixture.createConversation("b0248_conv_t", routeTestOwner)
	accepted := fixture.accept(routeTestOwner, "b0248_conv_t", "cmid-b0248", "问题")
	if accepted == nil {
		t.Fatal("accept turn")
	}
	if _, err := fixture.db.Exec(`UPDATE chat_messages SET status = 'completed', storage_reserved_bytes = 0 WHERE conversation_id = 'b0248_conv_t' AND turn_id = ? AND role = 'assistant'`, accepted.TurnID); err != nil {
		t.Fatalf("mark assistant completed: %v", err)
	}
	recorder := invokeB0248Attach(rt, "b0248_conv_t", accepted.TurnID)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_stream_terminal") {
		t.Fatalf("AttachStream=false 且轮次已终态应 409 chat_stream_terminal，实际 = %d %q", recorder.Code, recorder.Body.String())
	}
}

// TestBug0248AttachStreamTrueKeepsHandlerResponse：AttachStream 返回 true 时
// 路由不得回落覆盖处理器已写的响应。
func TestBug0248AttachStreamTrueKeepsHandlerResponse(t *testing.T) {
	rt, fixture := newB0248Routes(t, func(w http.ResponseWriter, r *http.Request, identity GenerationIdentity) bool {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("attached"))
		return true
	})
	fixture.createConversation("b0248_conv_ok", routeTestOwner)
	if _, err := fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'b0248_turn_ok', active_started_at = ? WHERE id = 'b0248_conv_ok'`, fixture.nowISO); err != nil {
		t.Fatalf("set active turn: %v", err)
	}
	recorder := invokeB0248Attach(rt, "b0248_conv_ok", "b0248_turn_ok")
	if recorder.Code != http.StatusOK || recorder.Body.String() != "attached" {
		t.Fatalf("AttachStream=true 应保留处理器响应，实际 = %d %q", recorder.Code, recorder.Body.String())
	}
}
