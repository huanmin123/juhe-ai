package modelcheckquestionbank

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// fakeAuditSink 是审计 sink 测试替身（Mock 优先）。
type fakeAuditSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *fakeAuditSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *fakeAuditSink) recorded() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry{}, s.entries...)
}

// fakeTestRunner 是 TestRunner 端口的测试替身（Mock 优先）：记录入参并
// 返回预置结果或错误。
type fakeTestRunner struct {
	mu     sync.Mutex
	inputs []TestQuestionInput
	result TestQuestionResult
	err    error
}

func (r *fakeTestRunner) TestQuestion(ctx context.Context, input TestQuestionInput) (TestQuestionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	if _, ok := ctx.Deadline(); !ok {
		panic("TestQuestion 必须在带截止时间的 ctx 下调用")
	}
	if r.err != nil {
		return TestQuestionResult{}, r.err
	}
	return r.result, nil
}

func (r *fakeTestRunner) recorded() []TestQuestionInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TestQuestionInput{}, r.inputs...)
}

// handlerHarness 是 handlers 测试基建：临时 SQLite store + 管理/自助两个
// handler 实例 + 假审计 sink + 带假 runner 的自助实例（ServeTest 用）。
type handlerHarness struct {
	store      *Store
	db         *sql.DB
	admin      *HTTPHandlers
	self       *HTTPHandlers
	withRunner *HTTPHandlers
	runner     *fakeTestRunner
	sink       *fakeAuditSink
	adminActor *authsys.AuthContext
}

func newHandlerHarness(t *testing.T) *handlerHarness {
	t.Helper()
	store, db := newTestStore(t)
	sink := &fakeAuditSink{}
	adminActor := &authsys.AuthContext{SystemAccountID: "sys-admin", Username: "admin", DisplayName: "管理员", Role: "super_admin"}
	runner := &fakeTestRunner{}
	return &handlerHarness{
		store:      store,
		db:         db,
		admin:      NewHTTPHandlers(store, sink, nil, true, nil),
		self:       NewHTTPHandlers(store, sink, nil, false, nil),
		withRunner: NewHTTPHandlers(store, sink, nil, false, runner),
		runner:     runner,
		sink:       sink,
		adminActor: adminActor,
	}
}

// dispatch 模拟 Wave2 的挂载形态：/question-bank 为集合端点（GET 列表、
// POST 创建），/question-bank/{id} 为条目端点，/question-bank/{id}/review
// 剥离后缀后按条目审核，/question-bank/options 为选择器。
func dispatch(handler *HTTPHandlers, w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasSuffix(path, "/review") {
		handler.ServeReview(w, r)
		return
	}
	segment := path[strings.LastIndex(path, "/")+1:]
	if segment == "options" {
		handler.ServeOptions(w, r)
		return
	}
	if segment == "by-ids" {
		handler.ServeByIds(w, r)
		return
	}
	if segment == "question-bank" || segment == "q" {
		switch r.Method {
		case http.MethodGet:
			handler.ServeList(w, r)
		case http.MethodPost:
			handler.ServeCreate(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	}
	// 提交门禁测试端点：字面路径 test，须先于 /{id} 条目路由。
	if segment == "test" {
		if r.Method == http.MethodPost {
			handler.ServeTest(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	if segment != "" {
		switch r.Method {
		case http.MethodGet:
			handler.ServeDetail(w, r)
		case http.MethodPatch:
			handler.ServeUpdate(w, r)
		case http.MethodDelete:
			handler.ServeDelete(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	}
	http.NotFound(w, r)
}

// call 以指定身份调用 handler 并返回响应。
func call(t *testing.T, handler *HTTPHandlers, auth *authsys.AuthContext, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, http.NoBody)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(context.Background(), auth))
	}
	recorder := httptest.NewRecorder()
	dispatch(handler, recorder, request)
	return recorder
}

func aliceAuth() *authsys.AuthContext {
	return &authsys.AuthContext{SystemAccountID: "sys-alice", Username: "alice", DisplayName: "甲", Role: "user"}
}

func bobAuth() *authsys.AuthContext {
	return &authsys.AuthContext{SystemAccountID: "sys-bob", Username: "bob", DisplayName: "乙", Role: "user"}
}

func createBody(title, text, answer string, points []string) string {
	payload := map[string]any{"title": title, "questionText": text, "referenceAnswer": answer}
	if points != nil {
		payload["keyPoints"] = points
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// createBodySeeded 先按内容造一条 pass 测试记录，再返回带 testId 的创建
// 入参 JSON（门禁通过路径用；查重/校验 409/400 路径不需要凭据）。
func createBodySeeded(t *testing.T, h *handlerHarness, actor Actor, title, text, answer string, points []string) string {
	t.Helper()
	testID := seedPassingTest(t, h.store, actor, text, answer, points)
	payload := map[string]any{"title": title, "questionText": text, "referenceAnswer": answer, "testId": testID}
	if points != nil {
		payload["keyPoints"] = points
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// patchBodySeeded 同 createBodySeeded，供修改重提交的门禁通过路径。
func patchBodySeeded(t *testing.T, h *handlerHarness, actor Actor, title, text, answer string, points []string) string {
	return createBodySeeded(t, h, actor, title, text, answer, points)
}

// mustCreateQuestion 通过 self handler 提交题目并返回 data JSON。
func mustCreateQuestion(t *testing.T, h *handlerHarness, auth *authsys.AuthContext, title, text string) map[string]any {
	t.Helper()
	actor := Actor{SystemAccountID: auth.SystemAccountID}
	recorder := call(t, h.self, auth, http.MethodPost, "/__aisys__/api/my-model-checks/question-bank",
		createBodySeeded(t, h, actor, title, text, "参考答案："+title, []string{"要点"}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create %q status = %d body=%s", title, recorder.Code, recorder.Body.String())
	}
	return decodeData(t, recorder)
}

func decodeData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, recorder.Body.String())
	}
	return payload.Data
}

func decodeList(t *testing.T, recorder *httptest.ResponseRecorder) (items []map[string]any, total int) {
	t.Helper()
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v body=%s", err, recorder.Body.String())
	}
	return payload.Data.Items, payload.Data.Total
}

func TestHandlersUnauthenticatedRequestsAreRejected(t *testing.T) {
	h := newHandlerHarness(t)
	for _, tc := range []struct {
		name    string
		handler *HTTPHandlers
		method  string
		target  string
	}{
		{"列表", h.self, http.MethodGet, "/q"},
		{"创建", h.self, http.MethodPost, "/q"},
		{"详情", h.self, http.MethodGet, "/q/mcq-x"},
		{"修改", h.self, http.MethodPatch, "/q/mcq-x"},
		{"删除", h.self, http.MethodDelete, "/q/mcq-x"},
		{"审核", h.admin, http.MethodPost, "/q/mcq-x/review"},
		{"选项", h.self, http.MethodGet, "/q/options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := call(t, tc.handler, nil, tc.method, tc.target, "")
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
		})
	}
}

func TestHandlersCreateContractAndDedup(t *testing.T) {
	h := newHandlerHarness(t)

	t.Run("201 与契约字段", func(t *testing.T) {
		recorder := call(t, h.self, aliceAuth(), http.MethodPost, "/q", createBodySeeded(t, h, actorAlice, "什么是降智？", "请描述降智表现。", "回答缓慢且质量差。", []string{"要点1", "要点2"}))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		data := decodeData(t, recorder)
		for _, key := range []string{"id", "title", "questionText", "referenceAnswer", "keyPoints", "status", "rejectReason", "createdBy", "createdAt", "updatedAt", "reviewedAt", "reviewedBy", "lastTest"} {
			if _, ok := data[key]; !ok {
				t.Fatalf("契约缺少字段 %q: %v", key, data)
			}
		}
		if data["status"] != StatusPending || data["createdBy"] != "sys-alice" {
			t.Fatalf("data = %v", data)
		}
		if data["rejectReason"] != nil || data["reviewedAt"] != nil {
			t.Fatalf("新题 rejectReason/reviewedAt 必须为 null: %v", data)
		}
		if data["reviewedBy"] != nil {
			t.Fatalf("自助面新题 reviewedBy 必须为 null: %v", data)
		}
		lastTest, ok := data["lastTest"].(map[string]any)
		if !ok || lastTest["testId"] == "" || lastTest["accountName"] != "种子账户" || lastTest["model"] != "gpt-seed" || lastTest["passedAt"] == "" {
			t.Fatalf("新题必须透出门禁测试摘要 lastTest: %v", data["lastTest"])
		}
		points, ok := data["keyPoints"].([]any)
		if !ok || len(points) != 2 {
			t.Fatalf("keyPoints = %v", data["keyPoints"])
		}
	})

	t.Run("无 keyPoints 时有权视角渲染空数组", func(t *testing.T) {
		recorder := call(t, h.self, aliceAuth(), http.MethodPost, "/q", createBodySeeded(t, h, actorAlice, "第二题", "另一个内容题目。", "答案", nil))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d", recorder.Code)
		}
		data := decodeData(t, recorder)
		points, ok := data["keyPoints"].([]any)
		if !ok || len(points) != 0 {
			t.Fatalf("创建者视角无要点时 keyPoints 应为空数组: %v", data["keyPoints"])
		}
	})

	t.Run("重复标题 409", func(t *testing.T) {
		recorder := call(t, h.self, bobAuth(), http.MethodPost, "/q", createBody("什么是降智", "完全不同的另一段题目内容。", "答案", nil))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "标题与现有题目重复") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	})

	t.Run("相似内容 409 含最相似标题", func(t *testing.T) {
		recorder := call(t, h.self, bobAuth(), http.MethodPost, "/q", createBody("第三题标题", "请描述降智表现呀", "答案", nil))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "过于相似") || !strings.Contains(recorder.Body.String(), "什么是降智？") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	})

	t.Run("参数校验 400", func(t *testing.T) {
		for name, body := range map[string]string{
			"空标题":     `{"title":"","questionText":"内容","referenceAnswer":"答案"}`,
			"缺字段":     `{"title":"只有标题"}`,
			"未知字段":    `{"title":"标题","questionText":"内容","referenceAnswer":"答案","hacker":1}`,
			"非 JSON":  `not-json`,
			"多个 JSON": `{"title":"标题","questionText":"内容","referenceAnswer":"答案"} {"x":1}`,
		} {
			recorder := call(t, h.self, aliceAuth(), http.MethodPost, "/q", body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d body=%s", name, recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "题库题目参数无效") && !strings.Contains(recorder.Body.String(), "不能为空") {
				t.Fatalf("%s: body = %s", name, recorder.Body.String())
			}
		}
	})

	t.Run("创建审计", func(t *testing.T) {
		entries := h.sink.recorded()
		if len(entries) == 0 {
			t.Fatal("创建必须写审计")
		}
		last := entries[len(entries)-1]
		if last.Module != "model_check_question_bank" || last.Action != "create" || last.OperationKey != "model_check_question_bank.create" {
			t.Fatalf("entry = %+v", last)
		}
		if last.ActorSystemAccountID != "sys-alice" || last.Mode != "self" || last.ResourceType != "model_check_question" {
			t.Fatalf("entry = %+v", last)
		}
	})
}

func TestHandlersDetailPermission(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	bob := bobAuth()
	aliceQuestion := mustCreateQuestion(t, h, alice, "甲的题目", "甲提交的题目内容。")
	questionID := aliceQuestion["id"].(string)
	if _, err := h.store.Review(context.Background(), questionID, Actor{SystemAccountID: "sys-admin"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	bobQuestion := mustCreateQuestion(t, h, bob, "乙的待审", "乙提交的待审内容。")
	bobPendingID := bobQuestion["id"].(string)

	t.Run("approved 非创建者题面与答案全员可见", func(t *testing.T) {
		recorder := call(t, h.self, bob, http.MethodGet, "/q/"+questionID, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		data := decodeData(t, recorder)
		if data["referenceAnswer"] != "参考答案：甲的题目" {
			t.Fatalf("非创建者应看到参考答案: %v", data)
		}
		if points, ok := data["keyPoints"].([]any); !ok || len(points) == 0 {
			t.Fatalf("非创建者应看到评分要点: %v", data)
		}
		if data["questionText"] != "甲提交的题目内容。" {
			t.Fatalf("题面应可见: %v", data)
		}
	})

	t.Run("创建者可见答案", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q/"+questionID, "")
		data := decodeData(t, recorder)
		if data["referenceAnswer"] != "参考答案：甲的题目" {
			t.Fatalf("创建者应看到答案: %v", data)
		}
	})

	t.Run("管理员可见答案", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q/"+questionID, "")
		data := decodeData(t, recorder)
		if data["referenceAnswer"] == nil {
			t.Fatalf("管理员应看到答案: %v", data)
		}
	})

	t.Run("reviewedBy 仅管理面透出", func(t *testing.T) {
		// 管理面 detail：审核后题目透出审核人系统账户 id。
		adminDetail := decodeData(t, call(t, h.admin, h.adminActor, http.MethodGet, "/q/"+questionID, ""))
		if adminDetail["reviewedBy"] != "sys-admin" {
			t.Fatalf("管理面 detail reviewedBy = %v, want sys-admin", adminDetail["reviewedBy"])
		}
		// 自助面同请求（创建者视角）：reviewedBy 恒为 null。
		selfDetail := decodeData(t, call(t, h.self, alice, http.MethodGet, "/q/"+questionID, ""))
		if selfDetail["reviewedBy"] != nil {
			t.Fatalf("自助面 detail reviewedBy 必须为 null: %v", selfDetail["reviewedBy"])
		}
		// 管理面 list：审核后题目非空、未审核题目为 null。
		items, _ := decodeList(t, call(t, h.admin, h.adminActor, http.MethodGet, "/q", ""))
		if len(items) != 2 {
			t.Fatalf("admin list = %d items", len(items))
		}
		for _, item := range items {
			if item["status"] == StatusApproved {
				if item["reviewedBy"] != "sys-admin" {
					t.Fatalf("管理面列表 approved 题 reviewedBy = %v", item["reviewedBy"])
				}
			} else if item["reviewedBy"] != nil {
				t.Fatalf("管理面列表未审核题 reviewedBy 必须为 null: %v", item["reviewedBy"])
			}
		}
		// 未审核题管理面 detail reviewedBy 为 null。
		pending := decodeData(t, call(t, h.admin, h.adminActor, http.MethodGet, "/q/"+bobPendingID, ""))
		if pending["reviewedBy"] != nil {
			t.Fatalf("未审核题 reviewedBy 必须为 null: %v", pending["reviewedBy"])
		}
	})

	t.Run("他人 pending 题目按不存在处理", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q/"+bobPendingID, "")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", recorder.Code)
		}
		// 管理员可见他人 pending。
		if recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q/"+bobPendingID, ""); recorder.Code != http.StatusOK {
			t.Fatalf("admin status = %d", recorder.Code)
		}
	})

	t.Run("不存在的题目 404", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q/mcq-missing", "")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
}

func TestHandlersListScopeAndContract(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	bob := bobAuth()
	mustCreateQuestion(t, h, alice, "甲的待审", "甲的待审内容。")
	approved := mustCreateQuestion(t, h, bob, "乙的已过", "乙的已过内容。")
	approvedID := approved["id"].(string)
	if _, err := h.store.Review(context.Background(), approvedID, Actor{SystemAccountID: "sys-admin"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}

	t.Run("自助列表 = approved + 本人", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		items, total := decodeList(t, recorder)
		if total != 2 || len(items) != 2 {
			t.Fatalf("alice list = %d items, total %d", len(items), total)
		}
	})

	t.Run("自助列表答案全员可见", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q", "")
		items, _ := decodeList(t, recorder)
		for _, item := range items {
			if item["referenceAnswer"] == nil {
				t.Fatalf("列表答案全员可见，不应缺失: %v", item)
			}
		}
	})

	t.Run("管理员全量且答案完整", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q?status=pending", "")
		items, total := decodeList(t, recorder)
		if total != 1 || len(items) != 1 {
			t.Fatalf("admin pending list = %d/%d", len(items), total)
		}
		if items[0]["referenceAnswer"] == nil {
			t.Fatalf("管理员列表应有答案: %v", items[0])
		}
	})

	t.Run("参数校验", func(t *testing.T) {
		if recorder := call(t, h.self, alice, http.MethodGet, "/q?status=draft", ""); recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid status = %d", recorder.Code)
		}
		if recorder := call(t, h.self, alice, http.MethodGet, "/q?page=0", ""); recorder.Code != http.StatusBadRequest {
			t.Fatalf("page=0 = %d", recorder.Code)
		}
		if recorder := call(t, h.self, alice, http.MethodGet, "/q?pageSize=101", ""); recorder.Code != http.StatusBadRequest {
			t.Fatalf("pageSize=101 = %d", recorder.Code)
		}
		if recorder := call(t, h.self, alice, http.MethodGet, "/q?keyword="+strings.Repeat("长", 101), ""); recorder.Code != http.StatusBadRequest {
			t.Fatalf("long keyword = %d", recorder.Code)
		}
	})
}

func TestHandlersUpdateAndDeletePermissions(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	bob := bobAuth()
	admin := Actor{SystemAccountID: "sys-admin"}
	question := mustCreateQuestion(t, h, alice, "待改题", "待改的题目内容。")
	questionID := question["id"].(string)

	t.Run("非创建者修改 403", func(t *testing.T) {
		recorder := call(t, h.self, bob, http.MethodPatch, "/q/"+questionID, createBody("劫持标题", "劫持内容", "劫持答案", nil))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("创建者修改成功并写审计", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodPatch, "/q/"+questionID, patchBodySeeded(t, h, actorAlice, "修改后的标题", "修改后的题目内容。", "修改后的答案", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		data := decodeData(t, recorder)
		if data["title"] != "修改后的标题" || data["status"] != StatusPending {
			t.Fatalf("data = %v", data)
		}
		entries := h.sink.recorded()
		if len(entries) == 0 || entries[len(entries)-1].Action != "update" {
			t.Fatalf("entries = %+v", entries)
		}
	})

	t.Run("approved 修改 409", func(t *testing.T) {
		if _, err := h.store.Review(context.Background(), questionID, admin, true, "approve", ""); err != nil {
			t.Fatal(err)
		}
		recorder := call(t, h.self, alice, http.MethodPatch, "/q/"+questionID, createBody("再改", "再改内容", "再改答案", nil))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d", recorder.Code)
		}
	})

	t.Run("rejected 创建者修改后回 pending", func(t *testing.T) {
		// CAS 只允许 pending→rejected，驳回流用独立题目验证。
		revisable := mustCreateQuestion(t, h, alice, "回炉题", "回炉的题目内容。")
		revisableID := revisable["id"].(string)
		if _, err := h.store.Review(context.Background(), revisableID, admin, true, "reject", "请修订"); err != nil {
			t.Fatal(err)
		}
		recorder := call(t, h.self, alice, http.MethodPatch, "/q/"+revisableID, patchBodySeeded(t, h, actorAlice, "回炉后的标题", "回炉后修订的题目内容。", "回炉的答案", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		data := decodeData(t, recorder)
		if data["status"] != StatusPending || data["rejectReason"] != nil || data["reviewedAt"] != nil {
			t.Fatalf("rejected→编辑应清空审核字段并回 pending: %v", data)
		}
	})

	t.Run("创建者删除自己的 pending/rejected；approved 仅管理员", func(t *testing.T) {
		deletable := mustCreateQuestion(t, h, alice, "可删题", "创建者可删的题目。")
		deletableID := deletable["id"].(string)
		if recorder := call(t, h.self, bob, http.MethodDelete, "/q/"+deletableID, ""); recorder.Code != http.StatusForbidden {
			t.Fatalf("他人删除 = %d", recorder.Code)
		}
		if recorder := call(t, h.self, alice, http.MethodDelete, "/q/"+deletableID, ""); recorder.Code != http.StatusNoContent {
			t.Fatalf("本人删除 = %d", recorder.Code)
		}
		if recorder := call(t, h.self, alice, http.MethodDelete, "/q/"+deletableID, ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("重复删除 = %d", recorder.Code)
		}

		approved := mustCreateQuestion(t, h, alice, "已过题", "已过审的题目。")
		approvedID := approved["id"].(string)
		if _, err := h.store.Review(context.Background(), approvedID, admin, true, "approve", ""); err != nil {
			t.Fatal(err)
		}
		if recorder := call(t, h.self, alice, http.MethodDelete, "/q/"+approvedID, ""); recorder.Code != http.StatusForbidden {
			t.Fatalf("创建者删 approved = %d", recorder.Code)
		}
		before := len(h.sink.recorded())
		if recorder := call(t, h.admin, h.adminActor, http.MethodDelete, "/q/"+approvedID, ""); recorder.Code != http.StatusNoContent {
			t.Fatalf("管理员删 approved = %d", recorder.Code)
		}
		entries := h.sink.recorded()
		if len(entries) <= before || entries[len(entries)-1].Action != "delete" || entries[len(entries)-1].Mode != "admin" {
			t.Fatalf("删除审计缺失: %+v", entries)
		}
	})
}

func TestHandlersReviewEndpoint(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	question := mustCreateQuestion(t, h, alice, "送审题目", "送审的题目内容。")
	questionID := question["id"].(string)

	t.Run("自助实例审核 403", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodPost, "/q/"+questionID+"/review", `{"action":"approve"}`)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d", recorder.Code)
		}
	})

	t.Run("动作与理由校验 400", func(t *testing.T) {
		if recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"publish"}`); recorder.Code != http.StatusBadRequest {
			t.Fatalf("publish = %d", recorder.Code)
		}
		if recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"reject"}`); recorder.Code != http.StatusBadRequest {
			t.Fatalf("reject 无理由 = %d", recorder.Code)
		}
		if recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{}`); recorder.Code != http.StatusBadRequest {
			t.Fatalf("empty = %d", recorder.Code)
		}
	})

	t.Run("reject 成功并写审计", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"reject","reason":"题目质量不足"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		data := decodeData(t, recorder)
		if data["status"] != StatusRejected || data["rejectReason"] != "题目质量不足" || data["reviewedAt"] == nil {
			t.Fatalf("data = %v", data)
		}
		if data["reviewedBy"] != "sys-admin" {
			t.Fatalf("审核响应 reviewedBy = %v, want sys-admin", data["reviewedBy"])
		}
		entries := h.sink.recorded()
		last := entries[len(entries)-1]
		if last.Action != "reject" || last.OperationKey != "model_check_question_bank.review" || last.Mode != "admin" {
			t.Fatalf("entry = %+v", last)
		}
	})

	t.Run("再次审核 409", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"approve"}`)
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "题目已被审核或不存在") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("approve 路径", func(t *testing.T) {
		another := mustCreateQuestion(t, h, alice, "再送审", "再送审的题目内容。")
		anotherID := another["id"].(string)
		recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+anotherID+"/review", `{"action":"approve"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		data := decodeData(t, recorder)
		if data["status"] != StatusApproved || data["reviewedAt"] == nil {
			t.Fatalf("data = %v", data)
		}
		if data["rejectReason"] != nil {
			t.Fatalf("approve 后 rejectReason 应为 null: %v", data)
		}
	})
}

func TestHandlersReviewDelistApproved(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	admin := Actor{SystemAccountID: "sys-admin"}
	question := mustCreateQuestion(t, h, alice, "下架送审题", "用于下架测试的题目。")
	questionID := question["id"].(string)
	if _, err := h.store.Review(context.Background(), questionID, admin, true, "approve", ""); err != nil {
		t.Fatal(err)
	}

	t.Run("自助实例 reject approved 必须 403", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodPost, "/q/"+questionID+"/review", `{"action":"reject","reason":"创建者想下架"}`)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "仅管理员可以审核题目") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
		// 题目保持 approved。
		recorder = call(t, h.admin, h.adminActor, http.MethodGet, "/q/"+questionID, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("detail status = %d", recorder.Code)
		}
		if data := decodeData(t, recorder); data["status"] != StatusApproved {
			t.Fatalf("自助 reject 不应改变状态: %v", data["status"])
		}
	})

	t.Run("管理员 reject approved 下架成功", func(t *testing.T) {
		before := len(h.sink.recorded())
		recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"reject","reason":"配置引用过多，先下架"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		data := decodeData(t, recorder)
		if data["status"] != StatusRejected || data["rejectReason"] != "配置引用过多，先下架" || data["reviewedAt"] == nil {
			t.Fatalf("data = %v", data)
		}
		entries := h.sink.recorded()
		if len(entries) <= before {
			t.Fatal("下架必须写审计")
		}
		last := entries[len(entries)-1]
		if last.Action != "reject" || last.Summary != "驳回下架检测题目：下架送审题" {
			t.Fatalf("entry summary = %+v", last)
		}
		statusChange := false
		for _, change := range last.Changes {
			if change.Field == "status" && change.Before == StatusApproved && change.After == StatusRejected {
				statusChange = true
			}
		}
		if !statusChange {
			t.Fatalf("审计缺少 approved→rejected 状态变化: %+v", last.Changes)
		}
		// 下架后再次审核 → 409。
		recorder = call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"approve"}`)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("delisted re-approve = %d", recorder.Code)
		}
	})
}

func TestHandlersServeByIds(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	admin := Actor{SystemAccountID: "sys-admin"}
	pending := mustCreateQuestion(t, h, alice, "待审不可回显", "待审内容。")
	rejected := mustCreateQuestion(t, h, alice, "驳回不可回显", "驳回内容。")
	approvedA := mustCreateQuestion(t, h, alice, "回显题目甲", "已过内容甲。")
	approvedB := mustCreateQuestion(t, h, alice, "回显题目乙", "已过内容乙。")
	approvedAID := approvedA["id"].(string)
	approvedBID := approvedB["id"].(string)
	if _, err := h.store.Review(context.Background(), approvedAID, admin, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Review(context.Background(), approvedBID, admin, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Review(context.Background(), rejected["id"].(string), admin, true, "reject", "不通过"); err != nil {
		t.Fatal(err)
	}

	t.Run("仅 approved 且契约只含 id/title", func(t *testing.T) {
		ids := strings.Join([]string{approvedAID, approvedBID, pending["id"].(string), rejected["id"].(string), "mcq-missing"}, ",")
		recorder := call(t, h.self, alice, http.MethodGet, "/q/by-ids?ids="+ids, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Data.Items) != 2 {
			t.Fatalf("items = %v（未通过/失效 id 必须被过滤）", payload.Data.Items)
		}
		got := map[string]string{}
		for _, item := range payload.Data.Items {
			if item["id"] != approvedAID && item["id"] != approvedBID {
				t.Fatalf("意外 id: %v", item)
			}
			if title, _ := item["title"].(string); title == "" {
				t.Fatalf("title 缺失: %v", item)
			}
			for key := range item {
				if key != "id" && key != "title" {
					t.Fatalf("by-ids 契约只含 id/title，发现 %q（题面/答案不得泄漏）: %v", key, item)
				}
			}
			got[item["id"].(string)] = item["title"].(string)
		}
		if got[approvedAID] != "回显题目甲" {
			t.Fatalf("title 回显错误: %v", got)
		}
	})

	t.Run("重复 id 去重", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q/by-ids?ids="+approvedAID+","+approvedAID, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		var payload struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Data.Items) != 1 {
			t.Fatalf("items = %v, want 去重后 1 条", payload.Data.Items)
		}
	})

	t.Run("超过 20 个 id 返回 400", func(t *testing.T) {
		ids := make([]string, 0, byIDsLimit+1)
		for i := 0; i <= byIDsLimit; i++ {
			ids = append(ids, fmt.Sprintf("mcq-extra-%02d", i))
		}
		recorder := call(t, h.self, alice, http.MethodGet, "/q/by-ids?ids="+strings.Join(ids, ","), "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "题库按 id 查询数量不能超过 20") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	})

	t.Run("空 ids 返回空 items", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q/by-ids", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `"items":[]`) {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	})

	t.Run("未登录 401", func(t *testing.T) {
		recorder := call(t, h.self, nil, http.MethodGet, "/q/by-ids?ids="+approvedAID, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
}

func TestHandlersOptionsEndpoint(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	mustCreateQuestion(t, h, alice, "未通过的待审题", "待审内容。")
	approved := mustCreateQuestion(t, h, alice, "可选的已过题目", "已过内容。")
	approvedID := approved["id"].(string)
	if _, err := h.store.Review(context.Background(), approvedID, Actor{SystemAccountID: "sys-admin"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}

	recorder := call(t, h.self, aliceAuth(), http.MethodGet, "/q/options?keyword=可选", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 1 {
		t.Fatalf("options = %v", payload.Data.Items)
	}
	if payload.Data.Items[0]["id"] != approvedID || payload.Data.Items[0]["title"] != "可选的已过题目" {
		t.Fatalf("option = %v", payload.Data.Items[0])
	}
	for key, value := range payload.Data.Items[0] {
		if key != "id" && key != "title" {
			t.Fatalf("选项契约只含 id/title，发现 %q=%v", key, value)
		}
	}

	if recorder := call(t, h.self, aliceAuth(), http.MethodGet, "/q/options?limit=0", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 = %d", recorder.Code)
	}
}

func TestHandlersInternalErrorsRender500(t *testing.T) {
	h := newHandlerHarness(t)
	h.db.Close()
	recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "服务器内部错误") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestPathIDExtraction(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://svc/__aisys__/api/model-checks/question-bank/mcq-abc", nil)
	if got := pathID(request); got != "mcq-abc" {
		t.Fatalf("pathID = %q", got)
	}
	review := httptest.NewRequest(http.MethodPost, "http://svc/__aisys__/api/model-checks/question-bank/mcq-abc/review", nil)
	if got := reviewPathID(review); got != "mcq-abc" {
		t.Fatalf("reviewPathID = %q", got)
	}
	root := httptest.NewRequest(http.MethodGet, "http://svc/", nil)
	if got := pathID(root); got != "" {
		t.Fatalf("empty path id = %q", got)
	}
}

// 防止 errors 包在重构中被误删（StatusError 契约测试）。
func TestStatusErrorMessage(t *testing.T) {
	err := newStatusError(statusConflict, "题目内容与现有题目《%s》过于相似", "甲")
	if err.Error() != "题目内容与现有题目《甲》过于相似" {
		t.Fatalf("message = %q", err.Error())
	}
	var target *StatusError
	if !errors.As(err, &target) || target.Status != statusConflict {
		t.Fatalf("errors.As failed: %+v", target)
	}
}

func TestHandlersCreatedByNameContract(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	question := mustCreateQuestion(t, h, alice, "提交者契约题", "提交者契约内容。")
	questionID := question["id"].(string)
	if _, err := h.db.Exec(`INSERT INTO system_accounts (id, display_name) VALUES (?, ?)`, alice.SystemAccountID, "甲"); err != nil {
		t.Fatal(err)
	}

	t.Run("管理面列表与详情透出 createdByName", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("admin list status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		items, _ := decodeList(t, recorder)
		if len(items) != 1 || items[0]["createdByName"] != "甲" {
			t.Fatalf("admin list createdByName = %v (items=%v)", items[0]["createdByName"], items)
		}
		detail := call(t, h.admin, h.adminActor, http.MethodGet, "/q/"+questionID, "")
		if detail.Code != http.StatusOK {
			t.Fatalf("detail status = %d", detail.Code)
		}
		if got := decodeData(t, detail)["createdByName"]; got != "甲" {
			t.Fatalf("detail createdByName = %v", got)
		}
	})

	t.Run("自助面省略 createdByName 字段", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodGet, "/q", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("self list status = %d", recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "createdByName") {
			t.Fatalf("自助面响应不得包含 createdByName: %s", recorder.Body.String())
		}
		selfDetail := call(t, h.self, alice, http.MethodGet, "/q/"+questionID, "")
		if selfDetail.Code != http.StatusOK {
			t.Fatalf("self detail status = %d", selfDetail.Code)
		}
		if strings.Contains(selfDetail.Body.String(), "createdByName") {
			t.Fatalf("自助面详情响应不得包含 createdByName: %s", selfDetail.Body.String())
		}
	})

	t.Run("审核响应同样透出 createdByName", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodPost, "/q/"+questionID+"/review", `{"action":"approve"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("review status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if got := decodeData(t, recorder)["createdByName"]; got != "甲" {
			t.Fatalf("review createdByName = %v", got)
		}
	})

	t.Run("名称解析失败按整体失败返回", func(t *testing.T) {
		if _, err := h.db.Exec(`DROP TABLE system_accounts`); err != nil {
			t.Fatal(err)
		}
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q", "")
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("dropped-table list status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "解析题库提交者名称失败") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	})
}

func TestHandlersBuiltinQuestionContract(t *testing.T) {
	h := newHandlerHarness(t)
	if _, err := h.db.Exec(`INSERT INTO model_check_question_bank (id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,is_builtin,reviewed_by,reviewed_at,created_at,updated_at) VALUES ('mcq-builtin-ui','内置·展示题','内置展示题','内置题面内容。','内置参考答案。','["要点一"]','approved',NULL,'system','system',1,NULL,NULL,'2026-10-09T00:00:00Z','2026-10-09T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	t.Run("管理面列表透出 isBuiltin 与提交人", func(t *testing.T) {
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, "/q", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		items, total := decodeList(t, recorder)
		if total != 1 || items[0]["isBuiltin"] != true {
			t.Fatalf("items = %v", items)
		}
		if items[0]["createdByName"] != nil {
			t.Fatalf("system 来源无显示名时应省略 createdByName: %v", items[0])
		}
	})

	t.Run("自助面同样透出 isBuiltin", func(t *testing.T) {
		recorder := call(t, h.self, aliceAuth(), http.MethodGet, "/q", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		items, _ := decodeList(t, recorder)
		if len(items) != 1 || items[0]["isBuiltin"] != true {
			t.Fatalf("items = %v", items)
		}
		if items[0]["createdByName"] != nil {
			t.Fatalf("自助面不得透出 createdByName: %v", items[0])
		}
	})

	t.Run("管理员删除内置题 403", func(t *testing.T) {
		if recorder := call(t, h.admin, h.adminActor, http.MethodDelete, "/q/mcq-builtin-ui", ""); recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

func testRequestBody(text, answer string, points []string) string {
	payload := map[string]any{"questionText": text, "referenceAnswer": answer, "accountId": "acct-9", "model": "gpt-x"}
	if points != nil {
		payload["keyPoints"] = points
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestHandlersServeTest 覆盖提交门禁同步测试端点：认证、runner 未接线
// 503、body 校验 400、runner error 400 透传、成功 200 落库 + 审计 +
// Outcome 契约，以及测试凭据供 Create 门禁消费的端到端链路。
func TestHandlersServeTest(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()

	t.Run("未登录 401", func(t *testing.T) {
		recorder := call(t, h.withRunner, nil, http.MethodPost, "/q/test", testRequestBody("题面。", "答案。", nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
	})

	t.Run("runner 未接线 503", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodPost, "/q/test", testRequestBody("题面。", "答案。", nil))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "题目测试未完成接线") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("body 校验 400", func(t *testing.T) {
		for name, body := range map[string]string{
			"非 JSON": `not-json`,
			"未知字段":   `{"questionText":"题面","referenceAnswer":"答案","accountId":"a","model":"m","hacker":1}`,
			"空题面":    `{"questionText":" ","referenceAnswer":"答案","accountId":"a","model":"m"}`,
			"空账户":    `{"questionText":"题面","referenceAnswer":"答案","accountId":" ","model":"m"}`,
			"空模型":    `{"questionText":"题面","referenceAnswer":"答案","accountId":"a","model":""}`,
			"要点超量":   testRequestBody("题面。", "答案。", make([]string, KeyPointsMaxCount+1)),
		} {
			recorder := call(t, h.withRunner, alice, http.MethodPost, "/q/test", body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d body=%s", name, recorder.Code, recorder.Body.String())
			}
		}
		if inputs := h.runner.recorded(); len(inputs) != 0 {
			t.Fatalf("校验失败不得触达 runner: %v", inputs)
		}
	})

	t.Run("runner error 400 透传原文", func(t *testing.T) {
		h.runner.err = errors.New("J3b Business account does not exist or is outside scope")
		recorder := call(t, h.withRunner, alice, http.MethodPost, "/q/test", testRequestBody("题面。", "答案。", nil))
		// 中文前缀带 CJK，保证原始错误文案不被 kernel 本地化替换。
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "题目测试执行失败") || !strings.Contains(recorder.Body.String(), "outside scope") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		h.runner.err = nil
	})

	t.Run("成功 200 落库并返回 Outcome", func(t *testing.T) {
		h.runner.result = TestQuestionResult{
			Verdict: TestVerdictPass, Alignment: TestAlignmentAligned,
			KeyPointResults: []TestKeyPointResult{{Point: "要点", Met: true, Note: "覆盖"}},
			AnswerExcerpt:   "回答摘录", GraderNote: "结论一致", LatencyMs: 456, HTTPStatus: 200,
			AccountName: "测试账户甲",
		}
		recorder := call(t, h.withRunner, alice, http.MethodPost, "/q/test", testRequestBody("  题面内容。  ", "答案内容。", []string{"要点"}))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		testID, _ := payload.Data["testId"].(string)
		if !strings.HasPrefix(testID, "mcqt-") {
			t.Fatalf("testId = %v", payload.Data["testId"])
		}
		if payload.Data["verdict"] != TestVerdictPass || payload.Data["questionAlignment"] != TestAlignmentAligned {
			t.Fatalf("data = %v", payload.Data)
		}
		if payload.Data["accountName"] != "测试账户甲" || payload.Data["testedAt"] == nil {
			t.Fatalf("data = %v", payload.Data)
		}
		// Outcome 契约必须回填被测账户 id 与模型（前端渲染依赖）。
		if payload.Data["accountId"] != "acct-9" || payload.Data["model"] != "gpt-x" {
			t.Fatalf("data = %v", payload.Data)
		}
		inputs := h.runner.recorded()
		if len(inputs) == 0 {
			t.Fatal("runner 必须被调用")
		}
		input := inputs[len(inputs)-1]
		if input.SystemAccountID != "sys-alice" || input.AccountID != "acct-9" || input.Model != "gpt-x" ||
			input.QuestionText != "题面内容。" || input.ReferenceAnswer != "答案内容。" || len(input.KeyPoints) != 1 {
			t.Fatalf("runner input = %+v", input)
		}
		// 记录落库：verdict/account/模型/draft hash 与请求内容一致。
		record, err := h.store.FindUsableTest(context.Background(), actorAlice, testID, DraftHash("题面内容。", "答案内容。", []string{"要点"}), time.Now().UTC())
		if err != nil {
			t.Fatalf("落库记录必须能通过门禁校验: %v", err)
		}
		if record.AccountName != "测试账户甲" || record.Model != "gpt-x" || record.LatencyMs != 456 {
			t.Fatalf("record = %+v", record)
		}
		if !strings.Contains(record.DiagnosisJSON, `"verdict":"pass"`) {
			t.Fatalf("diagnosis_json = %q", record.DiagnosisJSON)
		}
		// 审计：module 同题库、action "test"、Changes 含 verdict/accountId/model。
		entries := h.sink.recorded()
		last := entries[len(entries)-1]
		if last.Module != "model_check_question_bank" || last.Action != "test" || last.OperationKey != "model_check_question_bank.test" {
			t.Fatalf("entry = %+v", last)
		}
		fields := map[string]string{}
		for _, change := range last.Changes {
			fields[change.Field] = change.After
		}
		if fields["verdict"] != TestVerdictPass || fields["accountId"] != "acct-9" || fields["model"] != "gpt-x" {
			t.Fatalf("changes = %+v", last.Changes)
		}

		// 端到端：同一内容凭 ServeTest 返回的 testId 提交题目 → 201 且透出
		// lastTest。
		createdPayload := map[string]any{"title": "凭据题", "questionText": "题面内容。", "referenceAnswer": "答案内容。", "keyPoints": []string{"要点"}, "testId": testID}
		createdBody, err := json.Marshal(createdPayload)
		if err != nil {
			t.Fatal(err)
		}
		created := call(t, h.self, alice, http.MethodPost, "/q", string(createdBody))
		if created.Code != http.StatusCreated {
			t.Fatalf("create with served testId status = %d body=%s", created.Code, created.Body.String())
		}
		view := decodeData(t, created)
		lastTest, ok := view["lastTest"].(map[string]any)
		if !ok || lastTest["testId"] != testID || lastTest["accountName"] != "测试账户甲" {
			t.Fatalf("created lastTest = %v", view["lastTest"])
		}
	})

	t.Run("request_failed 结果照常 200 落库", func(t *testing.T) {
		h.runner.result = TestQuestionResult{Verdict: TestVerdictRequestFailed, Alignment: TestAlignmentUnknown, HTTPStatus: 503, ErrorMessage: "stage failure"}
		recorder := call(t, h.withRunner, alice, http.MethodPost, "/q/test", testRequestBody("请求失败题面。", "请求失败答案。", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		testID, _ := payload.Data["testId"].(string)
		if testID == "" {
			t.Fatalf("data = %v", payload.Data)
		}
		// request_failed 记录存在但不能作为提交凭据。
		_, err := h.store.FindUsableTest(context.Background(), actorAlice, testID, DraftHash("请求失败题面。", "请求失败答案。", nil), time.Now().UTC())
		if !errors.Is(err, ErrTestNotPassed) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestHandlersSubmissionGateErrors 覆盖 Create/Update 门禁错误的 HTTP
// 映射：缺 testId 与哨兵错误分别渲染对应 400 中文文案。
func TestHandlersSubmissionGateErrors(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()

	t.Run("Create 缺 testId 400", func(t *testing.T) {
		recorder := call(t, h.self, alice, http.MethodPost, "/q", createBody("无凭据题", "无凭据提交的独立内容。", "答案", nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "缺少测试记录 testId") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("Create 内容不一致 400", func(t *testing.T) {
		testID := seedPassingTest(t, h.store, actorAlice, "测试内容甲。", "测试答案甲", nil)
		payload := map[string]any{"title": "不一致题", "questionText": "提交内容与测试完全不同，足够独特。", "referenceAnswer": "答案", "testId": testID}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		recorder := call(t, h.self, alice, http.MethodPost, "/q", string(encoded))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "测试记录与题目内容不一致") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("Create 非本人 testId 400", func(t *testing.T) {
		testID := seedPassingTest(t, h.store, actorBob, "乙的测试内容。", "乙的答案", nil)
		payload := map[string]any{"title": "盗用凭据题", "questionText": "乙的测试内容。", "referenceAnswer": "乙的答案", "testId": testID}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		recorder := call(t, h.self, alice, http.MethodPost, "/q", string(encoded))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "测试记录不存在") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("Update 缺 testId 400", func(t *testing.T) {
		question := mustCreateQuestion(t, h, alice, "待改无凭据题", "待改无凭据的题目内容。")
		recorder := call(t, h.self, alice, http.MethodPatch, "/q/"+question["id"].(string), createBody("新标题", "新内容。", "新答案", nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "缺少测试记录 testId") {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}
