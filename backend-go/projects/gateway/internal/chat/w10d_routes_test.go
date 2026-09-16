package chat

// w10d 覆盖收尾：streamTurn 深层分支、compactionTrigger 分支、资产路由错误路径。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const w10dCompactionPath = "/__aisys__/api/my-chat/conversations/conv_w10d_r/context/compactions"

// w10dAuthReq 构造带测试鉴权上下文的请求。
func w10dAuthReq(method, path string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(""))
	if method == "POST" {
		request = httptest.NewRequest(method, path, strings.NewReader(`{"model":"gpt-5"}`))
	}
	return request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
}

// TestW10DStreamTurnImageModalityMismatch 覆盖 input_image + 文本专用模型的 476 分支。
func TestW10DStreamTurnImageModalityMismatch(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("conv_w10d_r", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	payload := `{"clientMessageId":"img-1","content":"看图","model":"gpt-5-mini","contentBlocks":[{"type":"input_text","text":"看图"},{"type":"input_image","assetId":"` + w9fHexAssetID(1) + `"}]}`
	recorder := w9fInvokeStream(rt, "conv_w10d_r", payload)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("图片+文本专用模型 = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestW10DCompactionTriggerRouteBranches 覆盖 compactionTrigger 分支。
func TestW10DCompactionTriggerRouteBranches(t *testing.T) {
	t.Run("未选模型", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_w10d_r", routeTestOwner)
		if response := env.do("POST", w10dCompactionPath, routeTestOwner, `{}`); response.status != http.StatusBadRequest {
			t.Fatalf("空模型 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("未知键", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_w10d_r", routeTestOwner)
		if response := env.do("POST", w10dCompactionPath, routeTestOwner, `{"model_id":"x"}`); response.status != http.StatusBadRequest {
			t.Fatalf("未知键 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("会话不存在", func(t *testing.T) {
		env := newGenerationEnv(t)
		path := "/__aisys__/api/my-chat/conversations/chat_conv_none/context/compactions"
		if response := env.do("POST", path, routeTestOwner, `{"model":"gpt-5"}`); response.status != http.StatusNotFound {
			t.Fatalf("会话不存在 = %d %s", response.status, response.rawString())
		}
	})
	t.Run("压缩中已运行", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_w10d_r", routeTestOwner)
		rt := newChatRoutesForTest(env.deps)
		action := rt.claimAction("conv_w10d_r", routeTestOwner, "compacting")
		recorder := httptest.NewRecorder()
		request := w10dAuthReq("POST", w10dCompactionPath)
		request.SetPathValue("conversationId", "conv_w10d_r")
		rt.compactionTrigger(recorder, request)
		rt.deleteActionIfMatches("conv_w10d_r", action.token)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("压缩中 = %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("清空中冲突", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_w10d_r", routeTestOwner)
		rt := newChatRoutesForTest(env.deps)
		action := rt.claimAction("conv_w10d_r", routeTestOwner, "clearing")
		recorder := httptest.NewRecorder()
		request := w10dAuthReq("POST", w10dCompactionPath)
		request.SetPathValue("conversationId", "conv_w10d_r")
		rt.compactionTrigger(recorder, request)
		rt.deleteActionIfMatches("conv_w10d_r", action.token)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("清空中 = %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("活动轮次冲突", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_w10d_r", routeTestOwner)
		rt := newChatRoutesForTest(env.deps)
		if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'chat_turn_live', active_started_at = '2026-03-10T08:00:00.000Z' WHERE id = 'conv_w10d_r'`); err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		request := w10dAuthReq("POST", w10dCompactionPath)
		request.SetPathValue("conversationId", "conv_w10d_r")
		rt.compactionTrigger(recorder, request)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("活动轮次 = %d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("压缩服务缺失", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_w10d_r", routeTestOwner)
		env.deps.Compactions = nil
		rt := newChatRoutesForTest(env.deps)
		recorder := httptest.NewRecorder()
		request := w10dAuthReq("POST", w10dCompactionPath)
		request.SetPathValue("conversationId", "conv_w10d_r")
		rt.compactionTrigger(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("服务缺失 = %d %s", recorder.Code, recorder.Body.String())
		}
	})
}

// TestW10DAssetRouteErrorBranches 覆盖资产路由校验/错误分支。
func TestW10DAssetRouteErrorBranches(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("conv_w10d_r", routeTestOwner)
	server := mountAssetMuxW3(t, env)
	base := server.URL + "/conversations/conv_w10d_r/assets/chat_asset_00000000000000000000000000000001/content"
	for _, q := range []string{"?variant=bogus", "?download=2", "?foo=1"} {
		request, err := http.NewRequest("GET", base+q, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Test-Owner", routeTestOwner)
		response := doRequestW3(t, request)
		if response.status != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", q, response.status, response.rawString())
		}
	}
	// 资产不存在 → 404。
	request, err := http.NewRequest("GET", base, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test-Owner", routeTestOwner)
	response := doRequestW3(t, request)
	if response.status != http.StatusNotFound {
		t.Fatalf("资产不存在 = %d %s", response.status, response.rawString())
	}
}
