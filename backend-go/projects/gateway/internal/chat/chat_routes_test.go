package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// routeEnv hosts the chat routes on a kernel mux with a stub session
// middleware that derives the owner from X-Test-Owner (mirroring the
// requireAuth + forceSelfAccessScope mount).
type routeEnv struct {
	t      *testing.T
	server *httptest.Server
	fixture *chatFixture
}

func newRouteEnv(t *testing.T) *routeEnv {
	t.Helper()
	fixture := newChatFixture(t)
	_, clock := fixedChatClock()
	deps := &Deps{
		Store:                   fixture.store,
		MaxTurnsPerConversation: 100,
		Now:                     clock,
	}
	deps.RequireSession = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			owner := r.Header.Get("X-Test-Owner")
			if owner == "" {
				kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
				return
			}
			next.ServeHTTP(w, r.WithContext(authsys.WithAuthContext(r.Context(), &authsys.AuthContext{
				SystemAccountID: owner, Username: owner, DisplayName: owner, Role: "user",
			})))
		})
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Register(k, "/__aisys__/api/my-chat")
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &routeEnv{t: t, server: server, fixture: fixture}
}

type routeResponse struct {
	status  int
	body    []byte
	jsonMap map[string]any
}

func (env *routeEnv) do(method, path, owner string, body string, query ...string) routeResponse {
	env.t.Helper()
	target := env.server.URL + path
	if len(query) > 0 {
		target += "?" + strings.Join(query, "&")
	}
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		env.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if owner != "" {
		request.Header.Set("X-Test-Owner", owner)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		env.t.Fatal(err)
	}
	defer response.Body.Close()
	payload := make([]byte, 0)
	buffer := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buffer)
		payload = append(payload, buffer[:n]...)
		if readErr != nil {
			break
		}
	}
	parsed := map[string]any{}
	_ = json.Unmarshal(payload, &parsed)
	return routeResponse{status: response.StatusCode, body: payload, jsonMap: parsed}
}

func (r routeResponse) dataMap() map[string]any {
	data, _ := r.jsonMap["data"].(map[string]any)
	return data
}

func (r routeResponse) dataArray() []any {
	data, _ := r.jsonMap["data"].([]any)
	return data
}

func (r routeResponse) code() string {
	code, _ := r.jsonMap["code"].(string)
	return code
}

func (r routeResponse) message() string {
	message, _ := r.jsonMap["message"].(string)
	return message
}

func (r routeResponse) rawString() string { return string(r.body) }

const routeTestOwner = "owner-1"
const routeTestOther = "owner-2"

func TestRouteImagePolicy(t *testing.T) {
	env := newRouteEnv(t)
	unauthorized := env.do("GET", "/__aisys__/api/my-chat/image-policy", "", "")
	if unauthorized.status != http.StatusUnauthorized || unauthorized.message() != "请先登录" {
		t.Fatalf("expected 401 请先登录, got %d %s", unauthorized.status, unauthorized.rawString())
	}
	authorized := env.do("GET", "/__aisys__/api/my-chat/image-policy", routeTestOwner, "")
	if authorized.status != http.StatusOK {
		t.Fatalf("expected 200, got %d", authorized.status)
	}
	data := authorized.dataMap()
	input, _ := data["input"].(map[string]any)
	if input == nil || input["mimeType"] != "image/webp" || input["maxEdge"] != float64(1024) ||
		input["quality"] != float64(82) || input["maxBytes"] != float64(3*1024*1024) {
		t.Fatalf("unexpected image policy payload: %s", authorized.rawString())
	}
}

func TestRouteConversationCRUDMatrix(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"

	// Create through the store (POST /conversations awaits the provisioning
	// wave) and exercise the read/update/delete surface.
	created := env.fixture.createConversation("chat_conv_a", routeTestOwner)
	if created == nil {
		t.Fatal("fixture conversation missing")
	}

	// GET list: payload order + userTurnLimit contract.
	list := env.do("GET", prefix+"/conversations", routeTestOwner, "")
	if list.status != http.StatusOK {
		t.Fatalf("list failed: %d %s", list.status, list.rawString())
	}
	items := list.dataArray()
	if len(items) != 1 {
		t.Fatalf("expected one conversation, got %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != "chat_conv_a" || first["userTurnLimit"] != float64(100) || first["title"] != "新对话" {
		t.Fatalf("unexpected list item: %v", first)
	}
	// 越权：another owner sees nothing.
	otherList := env.do("GET", prefix+"/conversations", routeTestOther, "")
	if len(otherList.dataArray()) != 0 {
		t.Fatal("cross-owner list must be empty")
	}
	// limit clamp.
	clamped := env.do("GET", prefix+"/conversations", routeTestOwner, "", "limit=9999")
	if len(clamped.dataArray()) != 1 {
		t.Fatal("limit clamp failed")
	}

	// GET by id.
	got := env.do("GET", prefix+"/conversations/chat_conv_a", routeTestOwner, "")
	if got.status != http.StatusOK || got.dataMap()["id"] != "chat_conv_a" {
		t.Fatalf("get failed: %s", got.rawString())
	}
	tools, _ := got.dataMap()["toolCapabilities"].(map[string]any)
	if tools == nil {
		t.Fatal("toolCapabilities missing")
	}
	toolList, _ := tools["tools"].([]any)
	if len(toolList) != 2 {
		t.Fatalf("expected fallback tool list, got %v", tools)
	}
	missing := env.do("GET", prefix+"/conversations/chat_conv_missing", routeTestOwner, "")
	if missing.status != http.StatusNotFound || missing.rawString() != `{"message":"会话不存在"}` {
		t.Fatalf("expected bare 404, got %s", missing.rawString())
	}
	// 越权 get → 404.
	cross := env.do("GET", prefix+"/conversations/chat_conv_a", routeTestOther, "")
	if cross.status != http.StatusNotFound {
		t.Fatalf("cross-owner get must 404, got %d", cross.status)
	}

	// PATCH validation matrix.
	blankTitle := env.do("PATCH", prefix+"/conversations/chat_conv_a", routeTestOwner, `{"title":"   "}`)
	if blankTitle.status != http.StatusBadRequest || blankTitle.code() != "chat_invalid_request" || blankTitle.message() != "请输入会话标题" {
		t.Fatalf("unexpected blank title response: %s", blankTitle.rawString())
	}
	longTitle := env.do("PATCH", prefix+"/conversations/chat_conv_a", routeTestOwner, `{"title":"`+strings.Repeat("长", 61)+`"}`)
	if longTitle.message() != "会话标题最多 60 个字符" {
		t.Fatalf("unexpected long title response: %s", longTitle.rawString())
	}
	emptyBody := env.do("PATCH", prefix+"/conversations/chat_conv_a", routeTestOwner, `{}`)
	if emptyBody.message() != "没有可更新的会话字段" {
		t.Fatalf("unexpected empty body response: %s", emptyBody.rawString())
	}
	unknownKey := env.do("PATCH", prefix+"/conversations/chat_conv_a", routeTestOwner, `{"isPinned":true,"nope":1}`)
	if unknownKey.status != http.StatusBadRequest || unknownKey.code() != "chat_invalid_request" || unknownKey.message() != "请求参数无效" {
		t.Fatalf("unexpected unknown key response: %s", unknownKey.rawString())
	}
	// zod .trim() transforms before the store sees the value.
	pinned := env.do("PATCH", prefix+"/conversations/chat_conv_a", routeTestOwner, `{"isPinned":true,"title":" 我的新标题 "}`)
	if pinned.status != http.StatusOK || pinned.dataMap()["title"] != "我的新标题" {
		t.Fatalf("unexpected patch response: %s", pinned.rawString())
	}
	if pinned.dataMap()["isPinned"] != true {
		t.Fatal("isPinned must persist")
	}
	patchMissing := env.do("PATCH", prefix+"/conversations/chat_conv_missing", routeTestOwner, `{"isPinned":true}`)
	if patchMissing.status != http.StatusNotFound || patchMissing.rawString() != `{"message":"会话不存在"}` {
		t.Fatalf("unexpected patch missing response: %s", patchMissing.rawString())
	}

	// DELETE: 204 then 404; active turn → 409.
	env.fixture.accept(routeTestOwner, "chat_conv_a", "cmid-hot", "生成中")
	deleteActive := env.do("DELETE", prefix+"/conversations/chat_conv_a", routeTestOwner, "")
	if deleteActive.status != http.StatusConflict || deleteActive.code() != "chat_message_in_progress" {
		t.Fatalf("expected active turn conflict, got %s", deleteActive.rawString())
	}
	// The stream interruption path clears the turn; then delete succeeds.
	stopped := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"clientMessageId":"cmid-hot"}`)
	if stopped.status != http.StatusAccepted {
		t.Fatalf("stop failed: %s", stopped.rawString())
	}
	deleted := env.do("DELETE", prefix+"/conversations/chat_conv_a", routeTestOwner, "")
	if deleted.status != http.StatusNoContent || len(deleted.body) != 0 {
		t.Fatalf("expected 204 empty, got %d %s", deleted.status, deleted.rawString())
	}
	deleteAgain := env.do("DELETE", prefix+"/conversations/chat_conv_a", routeTestOwner, "")
	if deleteAgain.status != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", deleteAgain.status)
	}
}

func TestRouteClearConversation(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)
	env.fixture.seedTurns(routeTestOwner, "chat_conv_a", 1)

	cleared := env.do("POST", prefix+"/conversations/chat_conv_a/clear", routeTestOwner, "")
	if cleared.status != http.StatusOK || cleared.dataMap()["title"] != "新对话" {
		t.Fatalf("clear failed: %s", cleared.rawString())
	}
	var turnCount int
	if err := env.fixture.db.QueryRow(`SELECT user_turn_count FROM chat_conversations WHERE id = 'chat_conv_a'`).Scan(&turnCount); err != nil {
		t.Fatal(err)
	}
	if turnCount != 0 {
		t.Fatalf("expected zero turns after clear, got %d", turnCount)
	}
	missing := env.do("POST", prefix+"/conversations/chat_conv_missing/clear", routeTestOwner, "")
	if missing.status != http.StatusNotFound || missing.code() != "chat_conversation_not_found" {
		t.Fatalf("unexpected missing clear: %s", missing.rawString())
	}
	// Body must stay an empty object.
	badBody := env.do("POST", prefix+"/conversations/chat_conv_a/clear", routeTestOwner, `{"x":1}`)
	if badBody.status != http.StatusBadRequest || badBody.code() != "chat_invalid_request" || badBody.message() != "请求参数无效" {
		t.Fatalf("unexpected clear body response: %s", badBody.rawString())
	}
	// 越权 clear → 404 with code.
	cross := env.do("POST", prefix+"/conversations/chat_conv_a/clear", routeTestOther, "")
	if cross.status != http.StatusNotFound || cross.code() != "chat_conversation_not_found" {
		t.Fatalf("cross-owner clear must 404: %s", cross.rawString())
	}
}

func TestRouteListMessages(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)
	env.fixture.seedTurns(routeTestOwner, "chat_conv_a", 3)

	list := env.do("GET", prefix+"/conversations/chat_conv_a/messages", routeTestOwner, "")
	if list.status != http.StatusOK {
		t.Fatalf("list failed: %s", list.rawString())
	}
	if len(list.dataArray()) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(list.dataArray()))
	}
	paged := env.do("GET", prefix+"/conversations/chat_conv_a/messages", routeTestOwner, "", "beforeSequenceNo=5", "limit=2")
	if len(paged.dataArray()) != 2 {
		t.Fatalf("expected 2 paged messages: %s", paged.rawString())
	}
	first, _ := paged.dataArray()[0].(map[string]any)
	if first["sequenceNo"] != float64(3) {
		t.Fatalf("expected ascending reversed page starting at 3, got %v", first["sequenceNo"])
	}
	twoCursors := env.do("GET", prefix+"/conversations/chat_conv_a/messages", routeTestOwner, "", "beforeSequenceNo=5", "afterSequenceNo=1")
	if twoCursors.status != http.StatusBadRequest || twoCursors.message() != "消息游标只能指定一个" {
		t.Fatalf("unexpected cursor conflict: %s", twoCursors.rawString())
	}
	unknownKey := env.do("GET", prefix+"/conversations/chat_conv_a/messages", routeTestOwner, "", "bogus=1")
	if unknownKey.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown query key, got %d", unknownKey.status)
	}
	cross := env.do("GET", prefix+"/conversations/chat_conv_a/messages", routeTestOther, "")
	if cross.status != http.StatusInternalServerError {
		t.Fatalf("cross-owner message list maps to the store 会话不存在 fault, got %d", cross.status)
	}
}

func TestRouteSyncHead(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)
	env.fixture.seedTurns(routeTestOwner, "chat_conv_a", 2)
	var revision int64
	if err := env.fixture.db.QueryRow(`SELECT message_revision FROM chat_conversations WHERE id = 'chat_conv_a'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}

	noRevision := env.do("GET", prefix+"/conversations/chat_conv_a/sync", routeTestOwner, "")
	if noRevision.status != http.StatusBadRequest || noRevision.code() != "chat_invalid_request" || noRevision.message() != "请求参数无效" {
		t.Fatalf("expected localized 400, got %s", noRevision.rawString())
	}
	synced := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_a/sync", prefix), routeTestOwner, "", fmt.Sprintf("knownRevision=%d", revision))
	if synced.status != http.StatusOK {
		t.Fatalf("sync failed: %s", synced.rawString())
	}
	data := synced.dataMap()
	if data["unchanged"] != true || data["messageRevision"] != float64(revision) {
		t.Fatalf("unexpected sync payload: %s", synced.rawString())
	}
	if _, hasActiveTurn := data["activeTurn"]; hasActiveTurn {
		t.Fatal("no active turn expected")
	}
	tail, _ := data["tail"].([]any)
	if len(tail) != 2 {
		t.Fatalf("expected one complete tail turn, got %v", tail)
	}
	stale := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_a/sync", prefix), routeTestOwner, "", fmt.Sprintf("knownRevision=%d", revision-1))
	if stale.dataMap()["unchanged"] != false {
		t.Fatal("stale revision must report unchanged=false")
	}
	missing := env.do("GET", prefix+"/conversations/chat_conv_missing/sync", routeTestOwner, "", "knownRevision=0")
	if missing.status != http.StatusNotFound || missing.code() != "chat_conversation_not_found" {
		t.Fatalf("unexpected missing sync: %s", missing.rawString())
	}
	// Streaming turn surfaces the nested activeTurn object.
	env.fixture.accept(routeTestOwner, "chat_conv_a", "cmid-live", "进行中")
	live := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_a/sync", prefix), routeTestOwner, "", fmt.Sprintf("knownRevision=%d", revision))
	activeTurn, _ := live.dataMap()["activeTurn"].(map[string]any)
	if activeTurn == nil || activeTurn["turnId"] == nil || activeTurn["assistantMessageId"] == nil || activeTurn["startedAt"] == nil {
		t.Fatalf("expected nested activeTurn: %s", live.rawString())
	}
}

func TestRouteSubmissionStatus(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_a", "cmid-1", "问")

	streaming := env.do("GET", prefix+"/conversations/chat_conv_a/submissions/cmid-1", routeTestOwner, "")
	if streaming.status != http.StatusOK {
		t.Fatalf("submission lookup failed: %s", streaming.rawString())
	}
	data := streaming.dataMap()
	if data["state"] != "accepted" || data["runnerState"] != "missing" || data["assistantStatus"] != "streaming" {
		t.Fatalf("unexpected streaming submission: %s", streaming.rawString())
	}
	if _, hasError := data["errorCode"]; hasError {
		t.Fatal("no errorCode expected")
	}
	if _, hasServerTime := data["serverTime"]; !hasServerTime {
		t.Fatal("serverTime missing")
	}
	env.fixture.complete(routeTestOwner, "chat_conv_a", accepted.TurnID, "答")
	completed := env.do("GET", prefix+"/conversations/chat_conv_a/submissions/cmid-1", routeTestOwner, "")
	completedData := completed.dataMap()
	if completedData["runnerState"] != "terminal" || completedData["assistantStatus"] != "completed" {
		t.Fatalf("unexpected completed submission: %s", completed.rawString())
	}
	notFound := env.do("GET", prefix+"/conversations/chat_conv_a/submissions/nope", routeTestOwner, "")
	if notFound.dataMap()["state"] != "not_found" {
		t.Fatalf("expected not_found state: %s", notFound.rawString())
	}
	missingConversation := env.do("GET", prefix+"/conversations/chat_conv_missing/submissions/cmid-1", routeTestOwner, "")
	if missingConversation.status != http.StatusNotFound || missingConversation.code() != "chat_conversation_not_found" {
		t.Fatalf("unexpected missing conversation: %s", missingConversation.rawString())
	}
	// 越权: another owner gets 404 (conversation invisible).
	cross := env.do("GET", prefix+"/conversations/chat_conv_a/submissions/cmid-1", routeTestOther, "")
	if cross.status != http.StatusNotFound {
		t.Fatalf("cross-owner submission must 404, got %d", cross.status)
	}
}

func TestRouteContextStatus(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)
	status := env.do("GET", prefix+"/conversations/chat_conv_a/context-status", routeTestOwner, "")
	if status.status != http.StatusOK {
		t.Fatalf("context status failed: %s", status.rawString())
	}
	data := status.dataMap()
	if data["state"] != "ready" || data["usedTokens"] != float64(0) || data["ratio"] != float64(0) {
		t.Fatalf("unexpected status payload: %s", status.rawString())
	}
	if data["revision"] != float64(0) || data["attemptCount"] != float64(0) {
		t.Fatalf("unexpected counters: %s", status.rawString())
	}
	missing := env.do("GET", prefix+"/conversations/chat_conv_missing/context-status", routeTestOwner, "")
	if missing.status != http.StatusNotFound || missing.rawString() != `{"message":"会话不存在"}` {
		t.Fatalf("unexpected missing context status: %s", missing.rawString())
	}
	cross := env.do("GET", prefix+"/conversations/chat_conv_a/context-status", routeTestOther, "")
	if cross.status != http.StatusNotFound {
		t.Fatalf("cross-owner context status must 404, got %d", cross.status)
	}
}

func TestRouteStopMatrix(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)

	emptyBody := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{}`)
	if emptyBody.status != http.StatusBadRequest || emptyBody.message() != "缺少要停止的消息或轮次" {
		t.Fatalf("unexpected empty stop body: %s", emptyBody.rawString())
	}
	unknownKey := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"turnId":"t","x":1}`)
	if unknownKey.status != http.StatusBadRequest || unknownKey.code() != "chat_invalid_request" || unknownKey.message() != "请求参数无效" {
		t.Fatalf("unexpected unknown key: %s", unknownKey.rawString())
	}
	missingConversation := env.do("POST", prefix+"/conversations/chat_conv_missing/stop", routeTestOwner, `{"turnId":"t"}`)
	if missingConversation.status != http.StatusNotFound || missingConversation.code() != "chat_conversation_not_found" {
		t.Fatalf("unexpected missing stop: %s", missingConversation.rawString())
	}
	noMatch := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"clientMessageId":"gone"}`)
	if noMatch.status != http.StatusNotFound || noMatch.code() != "chat_generation_not_found" || noMatch.message() != "当前没有匹配的准备或生成任务" {
		t.Fatalf("unexpected no-match stop: %s", noMatch.rawString())
	}

	// Stop a live turn by clientMessageId → canceled + reservation release.
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_a", "cmid-1", "问")
	beforeStop := storageWindowTotal(t, env.fixture.db, routeTestOwner)
	stopped := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"clientMessageId":"cmid-1"}`)
	if stopped.status != http.StatusAccepted {
		t.Fatalf("stop failed: %s", stopped.rawString())
	}
	data := stopped.dataMap()
	if data["stopped"] != true || data["state"] != "canceled" || data["assistantStatus"] != "canceled" || data["turnId"] != accepted.TurnID {
		t.Fatalf("unexpected stop payload: %s", stopped.rawString())
	}
	afterStop := storageWindowTotal(t, env.fixture.db, routeTestOwner)
	if afterStop >= beforeStop {
		t.Fatalf("stop must release the reservation: %d -> %d", beforeStop, afterStop)
	}
	// Stopping again reports already_terminal.
	again := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"clientMessageId":"cmid-1"}`)
	if again.dataMap()["state"] != "already_terminal" || again.dataMap()["assistantStatus"] != "canceled" {
		t.Fatalf("unexpected second stop: %s", again.rawString())
	}

	// turnId/clientMessageId disagreement → 409 chat_turn_mismatch.
	mismatch := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"clientMessageId":"cmid-1","turnId":"chat_turn_other"}`)
	if mismatch.status != http.StatusConflict || mismatch.code() != "chat_turn_mismatch" || mismatch.message() != "要停止的轮次已变化" {
		t.Fatalf("unexpected mismatch stop: %s", mismatch.rawString())
	}
	// Unknown turnId → 404 当前没有匹配的生成任务.
	unknownTurn := env.do("POST", prefix+"/conversations/chat_conv_a/stop", routeTestOwner, `{"turnId":"chat_turn_unknown"}`)
	if unknownTurn.status != http.StatusNotFound || unknownTurn.message() != "当前没有匹配的生成任务" {
		t.Fatalf("unexpected unknown turn stop: %s", unknownTurn.rawString())
	}
}

func TestRouteAttachStream(t *testing.T) {
	env := newRouteEnv(t)
	prefix := "/__aisys__/api/my-chat"
	env.fixture.createConversation("chat_conv_a", routeTestOwner)
	env.fixture.seedTurns(routeTestOwner, "chat_conv_a", 1)

	// Finished turn id → chat_stream_terminal with the mismatch message.
	terminated := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_a/streams/turn-old", prefix), routeTestOwner, "")
	if terminated.status != http.StatusConflict || terminated.code() != "chat_stream_terminal" || terminated.message() != "要附着的轮次已结束或已变化" {
		t.Fatalf("unexpected terminal attach: %s", terminated.rawString())
	}

	// Active turn without a live runner → the store interrupts the turn;
	// Node returns already_terminal for a successful interruption, so the
	// route answers with the chat_stream_terminal contract message.
	// seedTurns already consumed cmid-1, so this accept uses a fresh id.
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_a", "cmid-live", "进行中")
	missing := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_a/streams/%s", prefix, accepted.TurnID), routeTestOwner, "")
	if missing.status != http.StatusConflict || missing.code() != "chat_stream_terminal" || missing.message() != "要附着的轮次已结束" {
		t.Fatalf("unexpected interrupted attach: %s", missing.rawString())
	}
	var status, errorCode string
	if err := env.fixture.db.QueryRow(`SELECT status, error_code FROM chat_messages WHERE id = ?`,
		accepted.AssistantMessage.ID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errorCode != "stream_interrupted" {
		t.Fatalf("expected stream_interrupted failure, got %s/%s", status, errorCode)
	}
	// Second attach: the interrupted turn cleared active_turn_id, so the
	// route short-circuits on the mismatch contract first (Node parity).
	again := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_a/streams/%s", prefix, accepted.TurnID), routeTestOwner, "")
	if again.status != http.StatusConflict || again.code() != "chat_stream_terminal" || again.message() != "要附着的轮次已结束或已变化" {
		t.Fatalf("unexpected second attach: %s", again.rawString())
	}
	// Missing conversation → 404 with code.
	unknown := env.do("GET", fmt.Sprintf("%s/conversations/chat_conv_missing/streams/t", prefix), routeTestOwner, "")
	if unknown.status != http.StatusNotFound || unknown.code() != "chat_conversation_not_found" {
		t.Fatalf("unexpected attach missing conversation: %s", unknown.rawString())
	}
}

func TestRouteRegistrationPrefix(t *testing.T) {
	fixture := newChatFixture(t)
	_, clock := fixedChatClock()
	deps := &Deps{Store: fixture.store, MaxTurnsPerConversation: 100, Now: clock}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Register(k, "")
	// Default prefix is the Node mount /__aisys__/api/my-chat.
	handler := k.Handler()
	request := httptest.NewRequest("GET", "/__aisys__/api/my-chat/image-policy", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("default prefix mount failed: %d %s", recorder.Code, recorder.Body.String())
	}
}

var _ = time.Now
