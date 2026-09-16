package chat

// w9f 覆盖收尾（第二批）：直驱 stream/消息/同步路由的校验与冲突分支，
// 生产逻辑零改动（writeStreamRouteError 缺陷修复单列）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const w9fPrefix = "/__aisys__/api/my-chat"

// w9fInvokeStream 用共享的 rt 直驱 streamTurn，注入鉴权上下文。
func w9fInvokeStream(rt *chatRoutes, conversationID, payload string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/stream", strings.NewReader(payload))
	request.SetPathValue("conversationId", conversationID)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
	recorder := httptest.NewRecorder()
	rt.streamTurn(recorder, request)
	return recorder
}

// w9fHexAssetID 构造合法格式的资产 ID（chat_asset_<32 hex>）。
func w9fHexAssetID(seed byte) string {
	suffix := strings.Repeat("0", 31) + string(rune('a'+seed%26))
	return "chat_asset_" + suffix
}

func TestW9FStreamRouteBodyValidationCover(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_w9f", routeTestOwner)
	// 非法 JSON / 非对象 / 必填缺失。
	if response := env.streamPost("chat_conv_w9f", routeTestOwner, `{bad`); response.status != http.StatusBadRequest {
		t.Fatalf("bad json = %d %s", response.status, response.rawString())
	}
	if response := env.streamPost("chat_conv_w9f", routeTestOwner, `[1]`); response.status != http.StatusBadRequest {
		t.Fatalf("array body = %d %s", response.status, response.rawString())
	}
	for name, payload := range map[string]string{
		"缺clientMessageId": `{"content":"hi","model":"gpt-5"}`,
		"缺content":         `{"clientMessageId":"c1","model":"gpt-5"}`,
		"缺model":           `{"clientMessageId":"c1","content":"hi"}`,
	} {
		if response := env.streamPost("chat_conv_w9f", routeTestOwner, payload); response.status != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", name, response.status, response.rawString())
		}
	}
	// 内容字节数超过 192KiB（保持在内核 256KiB JSON 限制之下 → 命中 413 分支）。
	huge := `{"clientMessageId":"c1","content":"` + strings.Repeat("字", 70*1024) + `","model":"gpt-5"}`
	if response := env.streamPost("chat_conv_w9f", routeTestOwner, huge); response.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge content = %d %.120s", response.status, response.rawString())
	}
	// 中间件注入了鉴权但 handler 兜底校验缺失上下文：直驱无鉴权请求。
	rt := newChatRoutesForTest(env.deps)
	request := httptest.NewRequest("POST", "/stream", strings.NewReader(streamPayload("c1", "hi", "gpt-5")))
	request.SetPathValue("conversationId", "chat_conv_w9f")
	recorder := httptest.NewRecorder()
	rt.streamTurn(recorder, request)
	// Node 契约：requireChatAuth 抛普通 Error，走 handleChatRouteError 的
	// 500 兜底（belt-and-suspenders，正常被中间件拦截）。
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "请先登录") {
		t.Fatalf("auth fallback = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW9FStreamRouteConflictsCover(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_cf", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)

	// 模型能力信息不可用：目录为空 → modelOption 缺失。
	previousCatalog := env.deps.ModelCatalog
	env.deps.ModelCatalog = w9fEmptyModelCatalog{}
	recorder := w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-1", "hi", "gpt-5"))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "chat_model_capability_unavailable") {
		t.Fatalf("empty catalog = %d %s", recorder.Code, recorder.Body.String())
	}
	// 目录有模型但无可用账户 → 无对话路由。
	env.deps.ModelCatalog = w9fCatalogOnlyMock{}
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-1b", "hi", "gpt-5"))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no accounts = %d %s", recorder.Code, recorder.Body.String())
	}
	env.deps.ModelCatalog = previousCatalog

	// 温度与 Top P 互斥。
	payload := `{"clientMessageId":"cf-2","content":"hi","model":"gpt-5","generationParameters":{"temperature":0.5,"topP":0.9}}`
	recorder = w9fInvokeStream(rt, "chat_conv_cf", payload)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("temp+topP = %d %s", recorder.Code, recorder.Body.String())
	}

	// 轮次上限。
	previous := env.deps.MaxTurnsPerConversation
	env.deps.MaxTurnsPerConversation = 0
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-3", "hi", "gpt-5"))
	env.deps.MaxTurnsPerConversation = previous
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_turn_limit_exceeded") {
		t.Fatalf("turn limit = %d %s", recorder.Code, recorder.Body.String())
	}

	// replaceTurnId 不可替换。
	recorder = w9fInvokeStream(rt, "chat_conv_cf", `{"clientMessageId":"cf-4","replaceTurnId":"chat_turn_missing","content":"hi","model":"gpt-5"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("missing replace = %d %s", recorder.Code, recorder.Body.String())
	}

	// 活跃轮次 → 普通冲突与 replace 冲突两种 code。
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = 'chat_turn_live', active_started_at = '2026-03-10T08:00:00.000Z' WHERE id = 'chat_conv_cf'`); err != nil {
		t.Fatalf("set active turn: %v", err)
	}
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-5", "hi", "gpt-5"))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_message_in_progress") {
		t.Fatalf("active turn = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = w9fInvokeStream(rt, "chat_conv_cf", `{"clientMessageId":"cf-6","replaceTurnId":"chat_turn_x","content":"hi","model":"gpt-5"}`)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_replace_conflict") {
		t.Fatalf("replace conflict = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET active_turn_id = NULL, active_started_at = NULL WHERE id = 'chat_conv_cf'`); err != nil {
		t.Fatalf("clear active turn: %v", err)
	}

	// 清空/压缩动作互斥。
	action := rt.claimAction("chat_conv_cf", routeTestOwner, "compacting")
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-7", "hi", "gpt-5"))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_context_compacting") {
		t.Fatalf("compacting = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deleteActionIfMatches("chat_conv_cf", action.token)
	action = rt.claimAction("chat_conv_cf", routeTestOwner, "clearing")
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-8", "hi", "gpt-5"))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_conversation_clearing") {
		t.Fatalf("clearing = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deleteActionIfMatches("chat_conv_cf", action.token)

	// 已有准备占位。
	prep := rt.claimPreparation("chat_conv_cf", routeTestOwner, "cf-other")
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-9", "hi", "gpt-5"))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_message_in_progress") {
		t.Fatalf("prep taken = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deletePreparationIfMatches("chat_conv_cf", prep.token)

	// gateway key 校验失败。
	previousKeys := env.deps.GatewayKeys
	env.deps.GatewayKeys = w9fFailingGatewayKeys{}
	recorder = w9fInvokeStream(rt, "chat_conv_cf", streamPayload("cf-10", "hi", "gpt-5"))
	env.deps.GatewayKeys = previousKeys
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("gateway fail = %d %s", recorder.Code, recorder.Body.String())
	}

	// 不存在的图片资产（合法格式）→ 422 chat_asset_unavailable（w9f 缺陷修复后契约）。
	payload = `{"clientMessageId":"cf-11","content":"hi","model":"gpt-5","contentBlocks":[{"type":"input_text","text":"hi"},{"type":"input_image","assetId":"` + w9fHexAssetID(1) + `"}]}`
	recorder = w9fInvokeStream(rt, "chat_conv_cf", payload)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "chat_asset_unavailable") {
		t.Fatalf("missing asset = %d %s", recorder.Code, recorder.Body.String())
	}
}

type w9fFailingGatewayKeys struct{}

func (w9fFailingGatewayKeys) ValidateGatewayKey(string) (*GatewayKeyView, error) {
	return nil, errW9FGatewayDown{}
}

type errW9FGatewayDown struct{}

func (errW9FGatewayDown) Error() string { return "gateway down" }

// w9fEmptyModelCatalog：无账户也无目录。
type w9fEmptyModelCatalog struct{}

func (w9fEmptyModelCatalog) ListAccountsForGroup(string, string, string, string) []ChatTransportAccount {
	return nil
}

func (w9fEmptyModelCatalog) ListProviderCatalog(string, string) []ProviderModelCatalogItem {
	return nil
}

// w9fCatalogOnlyMock：目录有模型但账户为空。
type w9fCatalogOnlyMock struct{}

func (w9fCatalogOnlyMock) ListAccountsForGroup(string, string, string, string) []ChatTransportAccount {
	return nil
}

func (w9fCatalogOnlyMock) ListProviderCatalog(providerCode, _ string) []ProviderModelCatalogItem {
	return []ProviderModelCatalogItem{{
		Model: "gpt-5", ProviderCode: providerCode,
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
	}}
}

func TestW9FClearConversationConflicts(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_clr", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	invoke := func(payload string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/clear", strings.NewReader(payload))
		request.SetPathValue("conversationId", "chat_conv_clr")
		request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
		recorder := httptest.NewRecorder()
		rt.clearConversation(recorder, request)
		return recorder
	}

	action := rt.claimAction("chat_conv_clr", routeTestOwner, "compacting")
	if recorder := invoke(""); recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_context_compacting") {
		t.Fatalf("compacting clear = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deleteActionIfMatches("chat_conv_clr", action.token)

	action = rt.claimAction("chat_conv_clr", routeTestOwner, "clearing")
	if recorder := invoke(""); recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_conversation_clearing") {
		t.Fatalf("clearing clear = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deleteActionIfMatches("chat_conv_clr", action.token)

	prep := rt.claimPreparation("chat_conv_clr", routeTestOwner, "cmid-clr")
	if recorder := invoke(""); recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "chat_message_in_progress") {
		t.Fatalf("prep clear = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deletePreparationIfMatches("chat_conv_clr", prep.token)

	// 非空对象体 → 未知键；非对象体 → 400。
	if recorder := invoke(`{"x":1}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("clear unknown key = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := invoke(`3`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("clear bad body = %d %s", recorder.Code, recorder.Body.String())
	}
	// 正常清空。
	if recorder := invoke(""); recorder.Code != http.StatusOK {
		t.Fatalf("clear ok = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW9FListMessagesQueryValidation(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_msg", routeTestOwner)
	base := w9fPrefix + "/conversations/chat_conv_msg/messages"
	cases := []struct {
		name   string
		query  string
		status int
	}{
		{"未知键", "?bogus=1", 400},
		{"游标非数字", "?beforeSequenceNo=abc", 400},
		{"游标过小", "?afterSequenceNo=0", 400},
		{"游标过大", "?fromSequenceNo=2147483648", 400},
		{"双游标", "?beforeSequenceNo=1&afterSequenceNo=2", 400},
		{"limit 过小", "?limit=0", 400},
		{"limit 过大", "?limit=101", 400},
		{"合法 before 游标", "?beforeSequenceNo=5&limit=10", 200},
		{"合法 from 游标", "?fromSequenceNo=1", 200},
	}
	for _, item := range cases {
		response := env.do("GET", base+item.query, routeTestOwner, "")
		if response.status != item.status {
			t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
		}
	}
	if response := env.do("GET", base, "", ""); response.status != http.StatusUnauthorized {
		t.Fatalf("unauth list = %d", response.status)
	}
}

func TestW9FSyncHeadValidation(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_sync", routeTestOwner)
	base := w9fPrefix + "/conversations/chat_conv_sync/sync"
	cases := []struct {
		name   string
		query  string
		status int
	}{
		{"未知键", "?extra=1", 400},
		{"缺 knownRevision", "", 400},
		{"非数字", "?knownRevision=abc", 400},
		{"负数", "?knownRevision=-1", 400},
		{"超过上限", "?knownRevision=9007199254740992", 400},
		{"合法", "?knownRevision=0", 200},
	}
	for _, item := range cases {
		response := env.do("GET", base+item.query, routeTestOwner, "")
		if response.status != item.status {
			t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
		}
	}
	response := env.do("GET", w9fPrefix+"/conversations/chat_conv_none/sync?knownRevision=0", routeTestOwner, "")
	if response.status != http.StatusNotFound {
		t.Fatalf("missing sync = %d %s", response.status, response.rawString())
	}
}

func TestW9FSubmissionStatusValidation(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_sub", routeTestOwner)
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
	longConversation := strings.Repeat("c", 121)
	if recorder := invoke(longConversation, "cmid"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("long conversation = %d", recorder.Code)
	}
	if recorder := invoke("chat_conv_sub", strings.Repeat("m", 101)); recorder.Code != http.StatusBadRequest {
		t.Fatalf("long client message = %d", recorder.Code)
	}
	recorder := invoke("chat_conv_sub", "cmid-unknown")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"not_found"`) {
		t.Fatalf("unknown submission = %d %s", recorder.Code, recorder.Body.String())
	}
	// 未鉴权。
	request := httptest.NewRequest("GET", "/submission", nil)
	request.SetPathValue("conversationId", "chat_conv_sub")
	request.SetPathValue("clientMessageId", "cmid")
	recorder = httptest.NewRecorder()
	rt.submissionStatus(recorder, request)
	// 与 Node 一致：兜底鉴权失败走 500（见 requireChatAuth 契约注释）。
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth submission = %d", recorder.Code)
	}
}

func TestW9FGetConversationToolCapabilities(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_tool", routeTestOwner)
	previous := env.deps.ToolCapabilit
	env.deps.ToolCapabilit = func(conversation *Conversation, ownerID string) any {
		return map[string]any{"model": conversation.LastModel, "tools": []any{}}
	}
	response := env.do("GET", w9fPrefix+"/conversations/chat_conv_tool", routeTestOwner, "")
	env.deps.ToolCapabilit = previous
	if response.status != http.StatusOK || !strings.Contains(response.rawString(), "toolCapabilities") {
		t.Fatalf("tool capabilities = %d %s", response.status, response.rawString())
	}
	if response := env.do("PATCH", w9fPrefix+"/conversations/chat_conv_tool", routeTestOwner, `{"defaultImageModel":5}`); response.status != http.StatusBadRequest {
		t.Fatalf("bad image model = %d %s", response.status, response.rawString())
	}
	if response := env.do("PATCH", w9fPrefix+"/conversations/chat_conv_tool", routeTestOwner, `{bad`); response.status != http.StatusBadRequest {
		t.Fatalf("patch bad json = %d", response.status)
	}
	if response := env.do("PATCH", w9fPrefix+"/conversations/chat_conv_tool", routeTestOwner, `"str"`); response.status != http.StatusBadRequest {
		t.Fatalf("patch non-object = %d", response.status)
	}
}
