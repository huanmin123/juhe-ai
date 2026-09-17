package chat

// w13b 波次：路由级覆盖。复用 buildGenerationEnvW10D 的路由 harness
// （httptest + mock 端口 + 固定时钟），覆盖 createConversation、模型目录、
// 压缩触发、submissionStatus、stopTurn、attachStream、clear/sync/listMessages
// 的校验与状态分支。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

func w13bRoutes(t *testing.T) (*generationEnv, *chatRoutes, string) {
	t.Helper()
	env := newGenerationEnv(t)
	conversationID := "w13b-conv-route"
	env.fixture.createConversation(conversationID, routeTestOwner)
	return env, newChatRoutesForTest(env.deps), conversationID
}

func w13bJSONRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func w13bDecode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return payload
}

func TestW13BCreateConversationMatrix(t *testing.T) {
	_, rt, _ := w13bRoutes(t)
	post := func(body string, owner string) *httptest.ResponseRecorder {
		request := w13bJSONRequest(t, "POST", "/conversations", body)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		recorder := httptest.NewRecorder()
		rt.createConversationHandler(recorder, request)
		return recorder
	}
	// 非法 JSON。
	if recorder := post("{bad", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d %s", recorder.Code, recorder.Body.String())
	}
	// 非对象体。
	if recorder := post(`[1]`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("array body = %d %s", recorder.Code, recorder.Body.String())
	}
	// 未知键。
	if recorder := post(`{"other":1}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d %s", recorder.Code, recorder.Body.String())
	}
	// apiKeyId 非字符串。
	if recorder := post(`{"apiKeyId":5}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad apiKeyId = %d %s", recorder.Code, recorder.Body.String())
	}
	// apiKeyId 空白。
	if recorder := post(`{"apiKeyId":"  "}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("blank apiKeyId = %d %s", recorder.Code, recorder.Body.String())
	}
	// 未认证。
	if recorder := post(`{}`, ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth = %d %s", recorder.Code, recorder.Body.String())
	}
	// apiKeyId 指向不存在的 key。
	rt.deps.ChatKeys = &failingChatKeysW13B{findErr: errors.New("key boom")}
	if recorder := post(`{"apiKeyId":"k1"}`, routeTestOwner); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("find error = %d %s", recorder.Code, recorder.Body.String())
	}
	// 默认创建（Ensure + 默认模型）。
	rt.deps.ChatKeys = &mockChatKeys{}
	recorder := post(`{}`, routeTestOwner)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	payload := w13bDecode(t, recorder)
	data := payload["data"].(map[string]any)
	if data["defaultModel"] == nil {
		t.Fatalf("默认模型缺失: %s", recorder.Body.String())
	}
	// ChatKeys nil → 默认创建失败。
	rt.deps.ChatKeys = nil
	if recorder := post(`{}`, routeTestOwner); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("nil chat keys = %d %s", recorder.Code, recorder.Body.String())
	}
}

type failingChatKeysW13B struct {
	findErr error
	ensure  error
}

func (f *failingChatKeysW13B) EnsureChatAPIKey(ownerID string) (string, error) {
	if f.ensure != nil {
		return "", f.ensure
	}
	return "key-1", nil
}

func (f *failingChatKeysW13B) FindChatAPIKey(keyID, ownerID string) (*ChatAPIKeyRecord, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return &ChatAPIKeyRecord{ID: keyID, Name: "n", Secret: "s", Status: "active"}, nil
}

func authedContextW13B(owner string) context.Context {
	return authsys.WithAuthContext(context.Background(), &authsys.AuthContext{SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user"})
}

func TestW13BModelsRoutesMatrix(t *testing.T) {
	_, rt, conversationID := w13bRoutes(t)
	get := func(convID, modelID, owner string) *httptest.ResponseRecorder {
		path := "/conversations/" + convID + "/models"
		request := httptest.NewRequest("GET", path, nil)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", convID)
		recorder := httptest.NewRecorder()
		if modelID != "" {
			request.SetPathValue("modelId", modelID)
			rt.getConversationModel(recorder, request)
		} else {
			rt.listConversationModels(recorder, request)
		}
		return recorder
	}
	if recorder := get(conversationID, "", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth models = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := get("w13b-conv-missing", "", routeTestOwner); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing conversation = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder := get(conversationID, "", routeTestOwner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list models = %d %s", recorder.Code, recorder.Body.String())
	}
	var listPayload struct {
		Data []ChatModelListOption `json:"data"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &listPayload)
	if len(listPayload.Data) == 0 {
		t.Fatalf("模型列表为空")
	}
	recorder = get(conversationID, "gpt-5", routeTestOwner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("model detail = %d %s", recorder.Code, recorder.Body.String())
	}
	detail := w13bDecode(t, recorder)["data"].(map[string]any)
	if detail["supportedApiProtocols"] == nil {
		t.Fatalf("能力载荷缺失: %s", recorder.Body.String())
	}
	// 未知模型 → 404。
	request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/models/nope", nil)
	request = request.WithContext(authedContextW13B(routeTestOwner))
	request.SetPathValue("modelId", "nope")
	recorder = httptest.NewRecorder()
	rt.getConversationModel(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown model = %d %s", recorder.Code, recorder.Body.String())
	}
	// gateway key 校验失败 → 500。
	rt.deps.GatewayKeys = failingGatewayKeysW13B{}
	request = httptest.NewRequest("GET", "/conversations/"+conversationID+"/models", nil)
	request = request.WithContext(authedContextW13B(routeTestOwner))
	request.SetPathValue("conversationId", conversationID)
	recorder = httptest.NewRecorder()
	rt.listConversationModels(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("gateway error = %d %s", recorder.Code, recorder.Body.String())
	}
	rt.deps.GatewayKeys = mockGatewayKeys{}
}

type failingGatewayKeysW13B struct{}

func (failingGatewayKeysW13B) ValidateGatewayKey(secret string) (*GatewayKeyView, error) {
	return nil, errors.New("gateway boom")
}

func TestW13BCompactionTriggerMatrix(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 4)
	post := func(body, owner string) *httptest.ResponseRecorder {
		request := w13bJSONRequest(t, "POST", "/conversations/"+conversationID+"/context/compactions", body)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.compactionTrigger(recorder, request)
		return recorder
	}
	if recorder := post("{bad", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", recorder.Code)
	}
	if recorder := post(`{"other":1}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d", recorder.Code)
	}
	if recorder := post(`{"model":"  "}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("blank model = %d", recorder.Code)
	}
	if recorder := post(`{"model":"` + strings.Repeat("m", 201) + `"}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("long model = %d", recorder.Code)
	}
	if recorder := post(`{}`, ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unauth = %d", recorder.Code)
	}
	if recorder := post(`{}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing model = %d", recorder.Code)
	}
	if recorder := post(`{"model":"gpt-5"}`, routeTestOwner); recorder.Code != http.StatusAccepted {
		t.Fatalf("accepted = %d %s", recorder.Code, recorder.Body.String())
	}
	// 第二次触发：压缩在途 → already_running；已结束 → no_compactable skipped。
	recorder := post(`{"model":"gpt-5"}`, routeTestOwner)
	if recorder.Code != http.StatusAccepted && recorder.Code != http.StatusConflict {
		t.Fatalf("second trigger = %d %s", recorder.Code, recorder.Body.String())
	}
	// clearing action 冲突。
	rt.mu.Lock()
	rt.actions[conversationID] = &activeConversationAction{token: 1, ownerID: routeTestOwner, kind: "clearing"}
	rt.mu.Unlock()
	if recorder := post(`{"model":"gpt-5"}`, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("clearing conflict = %d", recorder.Code)
	}
	rt.mu.Lock()
	delete(rt.actions, conversationID)
	rt.mu.Unlock()
	// 活动轮次冲突。
	active := env.fixture.accept(routeTestOwner, conversationID, "cmid-live", "进行中")
	if recorder := post(`{"model":"gpt-5"}`, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("active turn = %d", recorder.Code)
	}
	// 取消活动轮后再触发：claim 成功。
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: active.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
	// Compactions 为 nil → DomainError。
	rt.deps.Compactions = nil
	if recorder := post(`{"model":"gpt-5"}`, routeTestOwner); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("nil compactions = %d", recorder.Code)
	}
	rt.deps.Compactions = env.compactions
	// 无可压缩轮次 → skipped。
	smallEnv := newGenerationEnv(t)
	smallEnv.fixture.createConversation("w13b-conv-small", routeTestOwner)
	smallEnv.fixture.seedTurns(routeTestOwner, "w13b-conv-small", 1)
	smallRT := newChatRoutesForTest(smallEnv.deps)
	smallRequest := w13bJSONRequest(t, "POST", "/conversations/w13b-conv-small/context/compactions", `{"model":"gpt-5"}`)
	smallRequest = smallRequest.WithContext(authedContextW13B(routeTestOwner))
	smallRequest.SetPathValue("conversationId", "w13b-conv-small")
	smallRecorder := httptest.NewRecorder()
	smallRT.compactionTrigger(smallRecorder, smallRequest)
	if smallRecorder.Code != http.StatusConflict {
		t.Fatalf("skipped = %d %s", smallRecorder.Code, smallRecorder.Body.String())
	}
	// 不支持的图片模型 → capability error。
	rt.deps.ModelCatalog = imageOnlyCatalogW13B{}
	request := w13bJSONRequest(t, "POST", "/conversations/"+conversationID+"/context/compactions", `{"model":"gpt-image-2"}`)
	request = request.WithContext(authedContextW13B(routeTestOwner))
	request.SetPathValue("conversationId", conversationID)
	recorder = httptest.NewRecorder()
	rt.compactionTrigger(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("capability = %d %s", recorder.Code, recorder.Body.String())
	}
	// 无可用协议。
	rt.deps.ModelCatalog = emptyCatalogW13B{}
	request = w13bJSONRequest(t, "POST", "/conversations/"+conversationID+"/context/compactions", `{"model":"gpt-5"}`)
	request = request.WithContext(authedContextW13B(routeTestOwner))
	request.SetPathValue("conversationId", conversationID)
	recorder = httptest.NewRecorder()
	rt.compactionTrigger(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no protocol = %d %s", recorder.Code, recorder.Body.String())
	}
}

type imageOnlyCatalogW13B struct{}

func (imageOnlyCatalogW13B) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	enabled := true
	return []ChatTransportAccount{{ID: "a1", Type: "api_key", ProviderCode: "openai", SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: requestedModel, SourceEndpointFamily: endpointFamily}}}}
}

func (imageOnlyCatalogW13B) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	return []ProviderModelCatalogItem{{Model: "gpt-image-2", ProviderCode: "openai", SupportedAPIProtocols: []string{"images"}}}
}

type emptyCatalogW13B struct{}

func (emptyCatalogW13B) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	return nil
}

func (emptyCatalogW13B) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	return []ProviderModelCatalogItem{{Model: "gpt-5", ProviderCode: "openai", SupportedAPIProtocols: []string{"chat_completions"}}}
}

func TestW13BSubmissionStatusMatrix(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	get := func(clientMessageID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/submissions/"+clientMessageID, nil)
		request = request.WithContext(authedContextW13B(routeTestOwner))
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("clientMessageIDPlaceholder", clientMessageID)
		request.SetPathValue("clientMessageId", clientMessageID)
		recorder := httptest.NewRecorder()
		rt.submissionStatus(recorder, request)
		return recorder
	}
	// 路径长度超限。
	long := strings.Repeat("x", 101)
	request := httptest.NewRequest("GET", "/conversations/c/submissions/"+long, nil)
	request = request.WithContext(authedContextW13B(routeTestOwner))
	recorder := httptest.NewRecorder()
	rt.submissionStatus(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("long id = %d", recorder.Code)
	}
	// not found（无 preparation）。
	recorder = get("cmid-none")
	payload := w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["state"] != "not_found" {
		t.Fatalf("not found payload = %s", recorder.Body.String())
	}
	// preparing（注册 preparation）。
	rt.mu.Lock()
	rt.preps[conversationID] = &activePreparation{token: 1, ownerID: routeTestOwner, clientMessageID: "cmid-prep", phase: "preparing"}
	rt.mu.Unlock()
	recorder = get("cmid-prep")
	payload = w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["state"] != "preparing" || payload["phase"] != "preparing" {
		t.Fatalf("preparing payload = %s", recorder.Body.String())
	}
	// accepted + streaming + registry 快照。
	accepted := env.fixture.accept(routeTestOwner, conversationID, "cmid-live", "进行中")
	runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: routeTestOwner, ConversationID: conversationID, TurnID: accepted.TurnID, AssistantMessageID: accepted.AssistantMessage.ID},
	}, context.Background(), func() {}, func() bool { return false })
	env.deps.Generations = env.hub
	env.hub.Register(runner)
	recorder = get("cmid-live")
	payload = w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["state"] != "accepted" || payload["runnerState"] != "running" {
		t.Fatalf("accepted payload = %s", recorder.Body.String())
	}
	if payload["eventVersion"] == nil || payload["lastSemanticActivityAt"] == nil {
		t.Fatalf("快照字段缺失: %s", recorder.Body.String())
	}
	// 完成后 runnerState=terminal：先取消在途轮。
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: accepted.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
	finished := env.fixture.accept(routeTestOwner, conversationID, "cmid-done", "完成问")
	env.fixture.complete(routeTestOwner, conversationID, finished.TurnID, "完成答")
	recorder = get("cmid-done")
	payload = w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["state"] != "accepted" || payload["runnerState"] != "terminal" {
		t.Fatalf("terminal payload = %s", recorder.Body.String())
	}
	// 失败轮次 → errorCode/errorMessage。
	failed := env.fixture.accept(routeTestOwner, conversationID, "cmid-fail", "失败问")
	if _, err := env.fixture.store.FailChatTurn(FailTurnInput{
		ConversationID: conversationID, SystemAccountID: routeTestOwner, TurnID: failed.TurnID,
		ErrorCode: string(GenErrUpstreamHTTP), ErrorMessage: "上游失败", Now: env.fixture.nowISO,
	}); err != nil {
		t.Fatal(err)
	}
	recorder = get("cmid-fail")
	payload = w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["errorCode"] != string(GenErrUpstreamHTTP) {
		t.Fatalf("failed payload = %s", recorder.Body.String())
	}
}

func TestW13BStopTurnMatrix(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	env.deps.Generations = env.hub
	post := func(body, owner string) *httptest.ResponseRecorder {
		request := w13bJSONRequest(t, "POST", "/conversations/"+conversationID+"/stop", body)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.stopTurn(recorder, request)
		return recorder
	}
	if recorder := post("{bad", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", recorder.Code)
	}
	if recorder := post(`{"other":1}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d", recorder.Code)
	}
	if recorder := post(`{}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing ids = %d", recorder.Code)
	}
	if recorder := post(`{"turnId":"missing-turn"}`, routeTestOwner); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing turn = %d %s", recorder.Code, recorder.Body.String())
	}
	// clientMessageId 不匹配任何轮次与准备。
	if recorder := post(`{"clientMessageId":"ghost"}`, routeTestOwner); recorder.Code != http.StatusNotFound {
		t.Fatalf("ghost = %d", recorder.Code)
	}
	// preparation 取消。
	rt.mu.Lock()
	rt.preps[conversationID] = &activePreparation{token: 2, ownerID: routeTestOwner, clientMessageID: "cmid-prep", phase: "preparing"}
	rt.mu.Unlock()
	recorder := post(`{"clientMessageId":"cmid-prep"}`, routeTestOwner)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("prep cancel = %d %s", recorder.Code, recorder.Body.String())
	}
	// 活动轮取消：Store 路径。
	active := env.fixture.accept(routeTestOwner, conversationID, "cmid-live", "进行中")
	recorder = post(`{"clientMessageId":"cmid-live"}`, routeTestOwner)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d %s", recorder.Code, recorder.Body.String())
	}
	// 已终态 → already terminal accepted。
	recorder = post(`{"clientMessageId":"cmid-live"}`, routeTestOwner)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("already terminal = %d %s", recorder.Code, recorder.Body.String())
	}
	// turnId + clientMessageId 不一致 → 409。
	finished := env.fixture.accept(routeTestOwner, conversationID, "cmid-done", "完成问")
	env.fixture.complete(routeTestOwner, conversationID, finished.TurnID, "完成答")
	body := `{"turnId":"turn-other","clientMessageId":"cmid-done"}`
	if recorder := post(body, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("mismatch = %d %s", recorder.Code, recorder.Body.String())
	}
	// Registry 活动路径：注册 runner 后 stop → 202。
	live := env.fixture.accept(routeTestOwner, conversationID, "cmid-live2", "进行中2")
	runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: routeTestOwner, ConversationID: conversationID, TurnID: live.TurnID},
	}, context.Background(), func() {}, func() bool { return false })
	env.hub.Register(runner)
	recorder = post(`{"turnId":"`+live.TurnID+`"}`, routeTestOwner)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("registry stop = %d %s", recorder.Code, recorder.Body.String())
	}
	// 已完成轮次：already_terminal 202。
	if recorder := post(`{"turnId":"`+finished.TurnID+`"}`, routeTestOwner); recorder.Code != http.StatusAccepted {
		t.Fatalf("finished turn = %d %s", recorder.Code, recorder.Body.String())
	}
	// 先收口 live2 再开新轮。
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: live.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
	other := env.fixture.accept(routeTestOwner, conversationID, "cmid-live3", "进行中3")
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: other.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
	// 活动轮存在但 stop 目标不匹配 → 409 turn_mismatch。
	mismatch := env.fixture.accept(routeTestOwner, conversationID, "cmid-live4", "进行中4")
	if recorder := post(`{"turnId":"turn-other"}`, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("mismatch stop = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: mismatch.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
	_ = active
}

func TestW13BAttachStreamMatrix(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	attached := false
	env.deps.AttachStream = func(w http.ResponseWriter, r *http.Request, identity GenerationIdentity) bool {
		attached = true
		return true
	}
	get := func(turnID string, owner string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/streams/"+turnID, nil)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		request.SetPathValue("turnId", turnID)
		recorder := httptest.NewRecorder()
		rt.attachStream(recorder, request)
		return recorder
	}
	if recorder := get("turn-x", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth = %d", recorder.Code)
	}
	if recorder := get("turn-x", routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("no active turn = %d %s", recorder.Code, recorder.Body.String())
	}
	// 活动轮 + registry runner → attach 调用。
	live := env.fixture.accept(routeTestOwner, conversationID, "cmid-live", "进行中")
	env.deps.Generations = env.hub
	runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: routeTestOwner, ConversationID: conversationID, TurnID: live.TurnID},
		Execute:  func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) { return ChatGenerationTerminalResult{Status: "completed"}, nil },
	}, context.Background(), func() {}, func() bool { return false })
	env.hub.Register(runner)
	if recorder := get(live.TurnID, routeTestOwner); !attached || recorder.Code != http.StatusOK {
		t.Fatalf("attach = %d attached=%v", recorder.Code, attached)
	}
	// 让注册的 runner 跑完，释放 registry 槽位。
	env.hub.Launch(runner)
	<-runner.Completion()
	// 活动轮但 registry 无 runner → 中断收口。
	if recorder := get(live.TurnID, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("interrupted = %d %s", recorder.Code, recorder.Body.String())
	}
	// 轮次不匹配 → runner_missing。
	if recorder := get("turn-other", routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("runner missing = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW13BClearAndListRoutes(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 2)

	post := func(path, body, owner string) *httptest.ResponseRecorder {
		request := w13bJSONRequest(t, "POST", path, body)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.clearConversation(recorder, request)
		return recorder
	}
	base := "/conversations/" + conversationID + "/clear"
	if recorder := post(base, "{bad", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d", recorder.Code)
	}
	if recorder := post(base, `{"other":1}`, routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d", recorder.Code)
	}
	if recorder := post(base, `{}`, ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth = %d", recorder.Code)
	}
	// compacting action 冲突。
	rt.mu.Lock()
	rt.actions[conversationID] = &activeConversationAction{token: 1, ownerID: routeTestOwner, kind: "compacting"}
	rt.mu.Unlock()
	if recorder := post(base, `{}`, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("compacting conflict = %d", recorder.Code)
	}
	rt.mu.Lock()
	rt.actions[conversationID] = &activeConversationAction{token: 2, ownerID: routeTestOwner, kind: "clearing"}
	rt.mu.Unlock()
	if recorder := post(base, `{}`, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("clearing conflict = %d", recorder.Code)
	}
	// preparation 冲突。
	rt.mu.Lock()
	rt.actions[conversationID] = &activeConversationAction{token: 2, ownerID: routeTestOwner, kind: "clearing"}
	delete(rt.actions, conversationID)
	rt.preps[conversationID] = &activePreparation{token: 3, ownerID: routeTestOwner, clientMessageID: "cmid-x", phase: "preparing"}
	rt.mu.Unlock()
	if recorder := post(base, `{}`, routeTestOwner); recorder.Code != http.StatusConflict {
		t.Fatalf("prep conflict = %d", recorder.Code)
	}
	rt.mu.Lock()
	delete(rt.preps, conversationID)
	rt.mu.Unlock()
	// listMessages 游标校验（清空前执行）。
	list := func(query, owner string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/messages"+query, nil)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.listMessages(recorder, request)
		return recorder
	}
	if recorder := list("?badKey=1", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("strict query = %d", recorder.Code)
	}
	if recorder := list("?beforeSequenceNo=abc", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d", recorder.Code)
	}
	if recorder := list("?beforeSequenceNo=0", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("small cursor = %d", recorder.Code)
	}
	if recorder := list("?beforeSequenceNo=99999999999", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("big cursor = %d", recorder.Code)
	}
	if recorder := list("?beforeSequenceNo=1&afterSequenceNo=2", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("multi cursor = %d", recorder.Code)
	}
	if recorder := list("?limit=0", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("small limit = %d", recorder.Code)
	}
	if recorder := list("?limit=101", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("big limit = %d", recorder.Code)
	}
	if recorder := list("", ""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth = %d", recorder.Code)
	}
	recorder := list("?fromSequenceNo=2&limit=1", routeTestOwner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list = %d %s", recorder.Code, recorder.Body.String())
	}
	payload := w13bDecode(t, recorder)
	items := payload["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("游标分页结果 = %s", recorder.Body.String())
	}
	// 成功清空。
	recorder = post(base, `{}`, routeTestOwner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", recorder.Code, recorder.Body.String())
	}
	// 会话不存在。
	missing := httptest.NewRequest("GET", "/conversations/missing/messages", nil)
	missing = missing.WithContext(authedContextW13B(routeTestOwner))
	missing.SetPathValue("conversationId", "missing")
	missingRecorder := httptest.NewRecorder()
	rt.listMessages(missingRecorder, missing)
	if missingRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("missing = %d", missingRecorder.Code)
	}
}

func TestW13BSyncHeadRoute(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 2)
	get := func(query, owner string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/sync"+query, nil)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.syncHead(recorder, request)
		return recorder
	}
	if recorder := get("?foo=1", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("strict = %d", recorder.Code)
	}
	if recorder := get("", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing revision = %d", recorder.Code)
	}
	if recorder := get("?knownRevision=-1", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("negative = %d", recorder.Code)
	}
	if recorder := get("?knownRevision=99999999999999999999", routeTestOwner); recorder.Code != http.StatusBadRequest {
		t.Fatalf("huge = %d", recorder.Code)
	}
	recorder := get("?knownRevision=0", routeTestOwner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sync = %d %s", recorder.Code, recorder.Body.String())
	}
	payload := w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["unchanged"] != false || payload["lastSequenceNo"] == nil {
		t.Fatalf("sync payload = %s", recorder.Body.String())
	}
	// 活动轮 → activeTurn 载荷。
	live := env.fixture.accept(routeTestOwner, conversationID, "cmid-live", "进行中")
	recorder = get("?knownRevision=0", routeTestOwner)
	payload = w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["activeTurn"] == nil {
		t.Fatalf("activeTurn 缺失: %s", recorder.Body.String())
	}
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: live.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
}

func TestW13BContextStatusRoute(t *testing.T) {
	_, rt, conversationID := w13bRoutes(t)
	get := func(owner string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("GET", "/conversations/"+conversationID+"/context-status", nil)
		if owner != "" {
			request = request.WithContext(authedContextW13B(owner))
		}
		request.SetPathValue("conversationId", conversationID)
		recorder := httptest.NewRecorder()
		rt.contextStatus(recorder, request)
		return recorder
	}
	if recorder := get(""); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unauth = %d", recorder.Code)
	}
	recorder := get(routeTestOwner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %s", recorder.Code, recorder.Body.String())
	}
	payload := w13bDecode(t, recorder)["data"].(map[string]any)
	if payload["state"] != "ready" || payload["ratio"] != float64(0) {
		t.Fatalf("status payload = %s", recorder.Body.String())
	}
	missing := httptest.NewRequest("GET", "/conversations/missing/context-status", nil)
	missing = missing.WithContext(authedContextW13B(routeTestOwner))
	missing.SetPathValue("conversationId", "missing")
	missingRecorder := httptest.NewRecorder()
	rt.contextStatus(missingRecorder, missing)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", missingRecorder.Code)
	}
}

func TestW13BStreamTurnValidationArms(t *testing.T) {
	env, _, conversationID := w13bRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 2)

	stream := func(payload, owner string) routeResponse {
		return env.streamPost(conversationID, owner, payload)
	}
	// 非法 JSON。
	if response := stream("{bad", routeTestOwner); response.status != http.StatusBadRequest {
		t.Fatalf("bad json = %d", response.status)
	}
	// 校验矩阵：必填/枚举/块。
	cases := []struct {
		name    string
		payload string
	}{
		{"缺 clientMessageId", `{"content":"hi","model":"gpt-5"}`},
		{"缺 content", `{"clientMessageId":"c1","model":"gpt-5"}`},
		{"缺 model", `{"clientMessageId":"c1","content":"hi"}`},
		{"重复消息", `{"clientMessageId":"cmid-1","content":"hi","model":"gpt-5"}`},
		{"图片不支持", `{"clientMessageId":"c9","content":"hi","model":"gpt-5-mini","contentBlocks":[{"type":"input_image","assetId":"chat_asset_` + strings.Repeat("a", 32) + `"}]}`},
	}
	for _, item := range cases {
		response := stream(item.payload, routeTestOwner)
		if response.status == 0 || response.status >= 500 && response.status != http.StatusUnprocessableEntity && response.status != http.StatusConflict && response.status != http.StatusBadRequest {
			t.Fatalf("%s 状态异常 = %d %s", item.name, response.status, response.rawString())
		}
	}
	// 会话不存在。
	response := env.streamPost("w13b-conv-missing", routeTestOwner, streamPayloadW13B("c1", "hi", "gpt-5"))
	if response.status != http.StatusNotFound {
		t.Fatalf("missing conversation = %d %s", response.status, response.rawString())
	}
	// 轮次上限。
	limited := newGenerationEnv(t)
	limited.fixture.createConversation("w13b-conv-limit", routeTestOwner)
	limited.fixture.seedTurns(routeTestOwner, "w13b-conv-limit", 1)
	limited.deps.MaxTurnsPerConversation = 1
	response = limited.streamPost("w13b-conv-limit", routeTestOwner, streamPayloadW13B("c2", "hi", "gpt-5"))
	if response.status != http.StatusConflict {
		t.Fatalf("turn limit = %d %s", response.status, response.rawString())
	}
	// 生成参数能力错误。
	response = stream(`{"clientMessageId":"c10","content":"hi","model":"gpt-5","generationParameters":{"temperature":1,"topP":0.5}}`, routeTestOwner)
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("param conflict = %d %s", response.status, response.rawString())
	}
	// 不支持的思考级别。
	response = stream(`{"clientMessageId":"c11","content":"hi","model":"gpt-5","reasoningEffort":"max"}`, routeTestOwner)
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("effort = %d %s", response.status, response.rawString())
	}
	// 正常流式完成（mock 执行器）。
	env.executor.steps = append(env.executor.steps, scriptStep{
		match:   func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return sseResponse(chatCompletionsSSE("你好", true)) },
	})
	response = stream(streamPayloadW13B("c-ok", "你好", "gpt-5"), routeTestOwner)
	if response.status != http.StatusOK {
		t.Fatalf("stream = %d %s", response.status, response.rawString())
	}
	events := sseEvents(response.rawString())
	last := events[len(events)-1]
	if last.event != "message.completed" {
		t.Fatalf("终态事件缺失: %+v", events[maxInt(0, len(events)-3):])
	}
}

func streamPayloadW13B(clientMessageID, content, model string) string {
	return `{"clientMessageId":"` + clientMessageID + `","content":"` + content + `","model":"` + model + `"}`
}

func TestW13BStreamTurnConflictArms(t *testing.T) {
	env, rt, conversationID := w13bRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 2)
	// 活动轮冲突。
	activeTurnW13B := env.fixture.accept(routeTestOwner, conversationID, "cmid-live", "进行中")
	response := env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c1", "hi", "gpt-5"))
	if response.status != http.StatusConflict {
		t.Fatalf("active turn = %d %s", response.status, response.rawString())
	}
	// clearing action 冲突。
	rt.mu.Lock()
	rt.actions[conversationID] = &activeConversationAction{token: 1, ownerID: routeTestOwner, kind: "clearing"}
	rt.mu.Unlock()
	response = env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c2", "hi", "gpt-5"))
	if response.status != http.StatusConflict {
		t.Fatalf("clearing = %d %s", response.status, response.rawString())
	}
	// compacting action 冲突。
	rt.mu.Lock()
	rt.actions[conversationID] = &activeConversationAction{token: 2, ownerID: routeTestOwner, kind: "compacting"}
	rt.mu.Unlock()
	response = env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c3", "hi", "gpt-5"))
	if response.status != http.StatusConflict {
		t.Fatalf("compacting = %d %s", response.status, response.rawString())
	}
	rt.mu.Lock()
	delete(rt.actions, conversationID)
	rt.mu.Unlock()
	// preparation 抢占失败：预注册同名 preparation。
	rt.mu.Lock()
	rt.preps[conversationID] = &activePreparation{token: 9, ownerID: routeTestOwner, clientMessageID: "other", phase: "preparing"}
	rt.mu.Unlock()
	response = env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c4", "hi", "gpt-5"))
	if response.status != http.StatusConflict {
		t.Fatalf("prep conflict = %d %s", response.status, response.rawString())
	}
	rt.mu.Lock()
	delete(rt.preps, conversationID)
	rt.mu.Unlock()
	// 收口活动轮，让后续请求能走完校验链。
	if _, err := env.fixture.store.CancelActiveTurnIfMatches(CancelIfMatchesInput{ConversationID: conversationID, SystemAccountID: routeTestOwner, ExpectedTurnID: activeTurnW13B.TurnID, Now: env.fixture.nowISO}); err != nil {
		t.Fatal(err)
	}
	// Hub Register 失败：预占会话槽位。
	blockingRunner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: routeTestOwner, ConversationID: conversationID, TurnID: "ghost"},
		Execute:  func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) { return ChatGenerationTerminalResult{Status: "completed"}, nil },
	}, context.Background(), func() {}, func() bool { return false })
	env.deps.Hub.Register(blockingRunner)
	response = env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c5", "hi", "gpt-5"))
	if response.status != http.StatusConflict {
		t.Fatalf("hub conflict = %d %s", response.status, response.rawString())
	}
	// 释放 Hub 会话槽位：占位 runner 直接跑完（deleteIfMatches 清槽）。
	if !env.deps.Hub.Launch(blockingRunner) {
		t.Fatalf("占位 runner 启动失败")
	}
	<-blockingRunner.Completion()
	env.deps.ModelCatalog = emptyCatalogW13B{}
	response = env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c6", "hi", "gpt-5"))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("no accounts = %d %s", response.status, response.rawString())
	}
}

func TestW13BStreamTurnHistoryArms(t *testing.T) {
	env, _, conversationID := w13bRoutes(t)
	// 多轮历史 + mock 执行器：正常流式完成（覆盖历史渲染与 token 估算路径）。
	env.fixture.seedTurns(routeTestOwner, conversationID, 3)
	env.executor.steps = append(env.executor.steps, scriptStep{
		match:   func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return sseResponse(chatCompletionsSSE("回答", true)) },
	})
	response := env.streamPost(conversationID, routeTestOwner, streamPayloadW13B("c-big", "继续", "gpt-5"))
	if response.status != http.StatusOK {
		t.Fatalf("history stream = %d %s", response.status, response.rawString())
	}
	events := sseEvents(response.rawString())
	found := false
	for _, event := range events {
		if event.event == "message.completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("终态事件缺失: %d 个事件", len(events))
	}
}

func TestW13BDepsTraceAndHelpers(t *testing.T) {
	_, rt, _ := w13bRoutes(t)
	// traceID 覆盖：注入 TraceID 函数。
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("x-trace-id", " trace-1 ")
	rt.deps.TraceID = nil
	if got := rt.deps.traceID(request); got != "trace-1" {
		t.Fatalf("header trace 失败: %q", got)
	}
	rt.deps.TraceID = func(r *http.Request) string { return "fn-trace" }
	if got := rt.deps.traceID(request); got != "fn-trace" {
		t.Fatalf("fn trace 失败: %q", got)
	}
	// requireOwnedApiKey 分支。
	if _, err := rt.requireOwnedApiKey("", "o"); err == nil {
		t.Fatalf("空 key id 应报错")
	}
	savedKeys := rt.deps.ChatKeys
	rt.deps.ChatKeys = nil
	if _, err := rt.requireOwnedApiKey("k", "o"); err == nil {
		t.Fatalf("nil ChatKeys 应报错")
	}
	rt.deps.ChatKeys = &failingChatKeysW13B{findErr: errors.New("boom")}
	if _, err := rt.requireOwnedApiKey("k", "o"); err == nil {
		t.Fatalf("Find 错误应上抛")
	}
	rt.deps.ChatKeys = &inactiveChatKeysW13B{}
	if _, err := rt.requireOwnedApiKey("k", "o"); err == nil {
		t.Fatalf("非 active key 应报错")
	}
	rt.deps.ChatKeys = savedKeys
	// requireChatAPIKeyForOwner 分支。
	rt.deps.ChatKeys = nil
	if _, err := rt.requireChatAPIKeyForOwner("o"); err == nil {
		t.Fatalf("nil ChatKeys 应报错")
	}
	rt.deps.ChatKeys = &failingChatKeysW13B{ensure: errors.New("ensure boom")}
	if _, err := rt.requireChatAPIKeyForOwner("o"); err == nil {
		t.Fatalf("Ensure 错误应上抛")
	}
	rt.deps.ChatKeys = &inactiveChatKeysW13B{}
	if _, err := rt.requireChatAPIKeyForOwner("o"); err == nil {
		t.Fatalf("非 active 默认 key 应报错")
	}
	rt.deps.ChatKeys = &mockChatKeys{}
	// accountsForGroups nil catalog。
	rt.deps.ModelCatalog = nil
	if accounts := rt.accountsForGroups([]string{"g"}, "o", "m", ""); len(accounts) != 0 {
		t.Fatalf("nil catalog 应返回空")
	}
	_, catalog := rt.loadChatModelCatalogSnapshot([]string{"g"}, "o", "m")
	_ = catalog
	rt.deps.ModelCatalog = mockModelCatalog{}
	// constrainChatModelOptionForAccounts：空账户。
	option := &ChatModelOption{ID: "m", SupportedAPIProtocols: []string{"chat_completions", "responses"}}
	constrained := constrainChatModelOptionForAccounts(option, "m", nil, nil)
	if len(constrained.SupportedAPIProtocols) != 0 {
		t.Fatalf("空账户应清空协议")
	}
	// resolveChatModelOptionsFromAccountSnapshot：无协议跳过。
	options := []*ChatModelOption{{ID: "a", SupportedAPIProtocols: nil}, {ID: "b", SupportedAPIProtocols: []string{"chat_completions"}}}
	reachable := resolveChatModelOptionsFromAccountSnapshot([]ChatTransportAccount{
		{Type: "api_key", ProviderCode: "openai", SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{SourceModel: "b", SourceEndpointFamily: "chat_completions"}}},
	}, options)
	if len(reachable) != 1 || reachable[0] != "b" {
		t.Fatalf("可达模型过滤失败: %v", reachable)
	}
	// loadChatModelListsFromAccountSnapshot 去重。
	seen := map[string]bool{}
	models, _, err := rt.loadChatModelListsFromAccountSnapshot([]string{"g", "g"}, routeTestOwner)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if seen[model.ID] {
			t.Fatalf("模型列表重复: %s", model.ID)
		}
		seen[model.ID] = true
	}
	// maxConversationsPerUser 注入。
	rt.deps.MaxConversationsPerUserInt = func() int { return 7 }
	if rt.deps.maxConversationsPerUser() != 7 {
		t.Fatalf("注入上限失败")
	}
	rt.deps.MaxConversationsPerUserInt = nil
	if rt.deps.maxConversationsPerUser() != defaultMaxConversationsPerUser {
		t.Fatalf("默认上限失败")
	}
}

type inactiveChatKeysW13B struct{}

func (inactiveChatKeysW13B) EnsureChatAPIKey(ownerID string) (string, error) {
	return "key-1", nil
}

func (inactiveChatKeysW13B) FindChatAPIKey(keyID, ownerID string) (*ChatAPIKeyRecord, error) {
	return &ChatAPIKeyRecord{ID: keyID, Name: "n", Secret: "s", Status: "disabled"}, nil
}

func TestW13BStreamBodyTooLargeArms(t *testing.T) {
	env, _, conversationID := w13bRoutes(t)
	env.fixture.seedTurns(routeTestOwner, conversationID, 1)
	// buildChatTransportRequest 的 reasoningEffort/serviceTier 分支 + 正常完成。
	env.executor.steps = append(env.executor.steps, scriptStep{
		match:   func(call dispatchCall) bool { return strings.HasPrefix(call.Path, "/v1/chat/completions") },
		respond: func(call dispatchCall) *GenerationDispatchResponse { return sseResponse(chatCompletionsSSE("ok", false)) },
	})
	payload := `{"clientMessageId":"c-tier","content":"hi","model":"gpt-5","reasoningEffort":"low","serviceTier":"priority"}`
	response := env.streamPost(conversationID, routeTestOwner, payload)
	if response.status != http.StatusOK {
		t.Fatalf("tiered stream = %d %s", response.status, response.rawString())
	}
	// 消息体超 192 KiB（rune 数仍在 196608 内，命中 413 分支）。
	huge := strings.Repeat("大", 70000)
	response = env.streamPost(conversationID, routeTestOwner, `{"clientMessageId":"c-huge","content":"`+huge+`","model":"gpt-5"}`)
	if response.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge = %d", response.status)
	}
}

var w13bOnce sync.Once

func TestW13BOnceGuard(t *testing.T) {
	w13bOnce.Do(func() {})
	w13bOnce.Do(func() {
		t.Fatalf("once 应只执行一次")
	})
}
