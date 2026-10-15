package chat

// w9f 覆盖收尾（第四批）：会话映射校验、压缩服务分支、直驱鉴权兜底等。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---------------------------------------------------------------------------
// conversations.go：行映射与创建分支
// ---------------------------------------------------------------------------

func TestW9FMapConversationValidation(t *testing.T) {
	base := conversationRow{
		id: "c1", systemAccountID: "o", defaultImageModel: "gpt-image-2",
		lastMessageAt: "2026-03-10T08:00:00.000Z", createdAt: "2026-03-10T08:00:00.000Z",
		updatedAt: "2026-03-10T08:00:00.000Z",
	}
	if _, err := mapConversation(base); err != nil {
		t.Fatalf("base row: %v", err)
	}
	badModel := base
	badModel.defaultImageModel = "dall-e"
	if _, err := mapConversation(badModel); err == nil {
		t.Fatal("bad image model must fail")
	}
	for name, mutate := range map[string]func(*conversationRow){
		"lastMessageAt":   func(r *conversationRow) { r.lastMessageAt = "x" },
		"createdAt":       func(r *conversationRow) { r.createdAt = "x" },
		"updatedAt":       func(r *conversationRow) { r.updatedAt = "x" },
		"turnCount":       func(r *conversationRow) { r.userTurnCount = -1 },
		"messageRevision": func(r *conversationRow) { r.messageRevision = -1 },
	} {
		row := base
		mutate(&row)
		if _, err := mapConversation(row); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
	if _, err := normalizedImageModel("gpt-image-2"); err != nil {
		t.Fatalf("valid model: %v", err)
	}
	if _, err := normalizedImageModel("other"); err == nil {
		t.Fatal("invalid model must fail")
	}
	if optString("") != nil {
		t.Fatal("empty string is absent")
	}
	if optString("x") == nil {
		t.Fatal("value present")
	}
	if stringPtr("s") == nil {
		t.Fatal("stringPtr")
	}
}

func TestW9FCreateConversationGuards(t *testing.T) {
	fixture := newChatFixture(t)
	if _, err := fixture.store.CreateConversation(CreateConversationInput{SystemAccountID: "o", Now: "bad"}); err == nil {
		t.Fatal("invalid now must fail")
	}
	// 数量上限。
	for i := 0; i < 2; i++ {
		if _, err := fixture.store.CreateConversation(CreateConversationInput{
			SystemAccountID: "o2", Now: fixture.nowISO, MaxConversationsPerUser: 2,
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := fixture.store.CreateConversation(CreateConversationInput{
		SystemAccountID: "o2", Now: fixture.nowISO, MaxConversationsPerUser: 2,
	})
	if err == nil || err.Error() == "" {
		t.Fatalf("limit err = %v", err)
	}
	// ID 缺省时走 newID 生成路径。
	store, err := NewStore(fixture.db, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateConversation(CreateConversationInput{SystemAccountID: "o3", Now: fixture.nowISO, MaxConversationsPerUser: 5})
	if err != nil || created == nil || created.ID == "" {
		t.Fatalf("generated id = %+v/%v", created, err)
	}
}

// ---------------------------------------------------------------------------
// assets_store.go：错误类型与计数断言
// ---------------------------------------------------------------------------

func TestW9FAssetErrorMessages(t *testing.T) {
	if (&AssetQuotaExceededError{}).Error() == "" {
		t.Fatal("quota error message")
	}
	if (&AssetCountExceededError{}).Error() == "" {
		t.Fatal("count error message")
	}
}

// ---------------------------------------------------------------------------
// compaction_service.go：Schedule 与 runCompaction 的早退分支
// ---------------------------------------------------------------------------

func TestW9FCompactionScheduleFireAndForget(t *testing.T) {
	fixture := newChatFixture(t)
	executor := &mockExecutor{}
	compactions := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	// 会话不存在 → 异步 skipped，不 panic。
	compactions.Schedule(context.Background(), CompactionInput{ConversationID: "chat_conv_none", SystemAccountID: "o"})
	time.Sleep(80 * time.Millisecond)
}

func TestW9FCompactionCompactOnceMissingConversation(t *testing.T) {
	fixture := newChatFixture(t)
	executor := &mockExecutor{}
	compactions := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	result := compactions.CompactOnce(context.Background(), CompactionInput{ConversationID: "chat_conv_none", SystemAccountID: "o"})
	if result.Status != "skipped" {
		t.Fatalf("missing conversation = %+v", result)
	}
}

func TestW9FCompactionCompactOnceStoreFailure(t *testing.T) {
	fixture := newChatFixture(t)
	executor := &mockExecutor{}
	compactions := NewCompactionService(fixture.store, executor, func(text string) int { return len(text) / 4 }, func() string { return fixture.nowISO })
	_ = fixture.db.Close()
	result := compactions.CompactOnce(context.Background(), CompactionInput{ConversationID: "chat_conv_x", SystemAccountID: "o"})
	if result.Status != "failed" {
		t.Fatalf("store failure = %+v", result)
	}
}

// ---------------------------------------------------------------------------
// 零散纯函数
// ---------------------------------------------------------------------------

func TestW9FChatSmallPureHelpers(t *testing.T) {
	if sqlIntFromInt(nil) != nil {
		t.Fatal("nil int")
	}
	value := 7
	if got, ok := sqlIntFromInt(&value).(int); !ok || got != 7 {
		t.Fatalf("int passthrough = %v", got)
	}
	if nowWallclock().IsZero() {
		t.Fatal("wallclock")
	}
}

// ---------------------------------------------------------------------------
// routes.go：各 handler 的鉴权兜底（belt-and-suspenders → 500，Node 契约）
// ---------------------------------------------------------------------------

func TestW9FHandlerAuthFallbacksRender500(t *testing.T) {
	env := newGenerationEnv(t)
	env.fixture.createConversation("chat_conv_auth", routeTestOwner)
	rt := newChatRoutesForTest(env.deps)
	// patchConversation 先解析请求体再鉴权：需带合法载荷才能到达鉴权兜底。
	handlers := map[string]struct {
		body    string
		handler func(*httptest.ResponseRecorder, *http.Request)
	}{
		"imagePolicy":        {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.imagePolicy(w, r) }},
		"listConversations":  {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.listConversations(w, r) }},
		"getConversation":    {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.getConversation(w, r) }},
		"patchConversation":  {"{\"title\":\"x\"}", func(w *httptest.ResponseRecorder, r *http.Request) { rt.patchConversation(w, r) }},
		"deleteConversation": {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.deleteConversation(w, r) }},
		"clearConversation":  {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.clearConversation(w, r) }},
		"contextStatus":      {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.contextStatus(w, r) }},
		"attachStream":       {"", func(w *httptest.ResponseRecorder, r *http.Request) { rt.attachStream(w, r) }},
	}
	for name, item := range handlers {
		request := httptest.NewRequest("GET", "/x", strings.NewReader(item.body))
		request.SetPathValue("conversationId", "chat_conv_auth")
		recorder := httptest.NewRecorder()
		item.handler(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("%s unauth = %d (%s)", name, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("WWW-Authenticate"); got != "" {
			t.Fatalf("%s unexpected challenge", name)
		}
	}
	// listMessages / syncHead 的鉴权在参数校验之后。
	request := httptest.NewRequest("GET", "/x?limit=abc", nil)
	request.SetPathValue("conversationId", "chat_conv_auth")
	recorder := httptest.NewRecorder()
	rt.listMessages(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit abc = %d %s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest("GET", "/x?knownRevision=1", nil)
	request.SetPathValue("conversationId", "chat_conv_auth")
	recorder = httptest.NewRecorder()
	rt.syncHead(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("sync unauth = %d %s", recorder.Code, recorder.Body.String())
	}
	// 活跃轮次缺 started_at（需同时有助手消息引用）→ DomainError → 500。
	accepted := env.fixture.accept(routeTestOwner, "chat_conv_auth", "cmid-sync", "问题")
	if accepted == nil {
		t.Fatal("accept turn")
	}
	if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET active_started_at = NULL WHERE id = 'chat_conv_auth'`); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/x?knownRevision=0", nil)
	request.SetPathValue("conversationId", "chat_conv_auth")
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{SystemAccountID: routeTestOwner}))
	recorder = httptest.NewRecorder()
	rt.syncHead(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("sync bad active = %d %s", recorder.Code, recorder.Body.String())
	}
}
