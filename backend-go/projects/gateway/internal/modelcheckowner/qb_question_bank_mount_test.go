package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
	_ "modernc.org/sqlite"
)

// qbQuestionBankFixture 构造全局共享题库 store（内存 SQLite）与 admin/self
// 两个 HTTPHandlers 实例（照 compose/main 的装配形态：同 store、双 adminMode）。
func qbQuestionBankFixture(t *testing.T) (*modelcheckquestionbank.Store, *modelcheckquestionbank.HTTPHandlers, *modelcheckquestionbank.HTTPHandlers) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/questionbank.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE model_check_question_bank (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		title_norm TEXT NOT NULL,
		question_text TEXT NOT NULL,
		reference_answer TEXT NOT NULL,
		key_points_json TEXT,
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
		reject_reason TEXT,
		created_by TEXT NOT NULL,
		created_scope TEXT NOT NULL,
		reviewed_by TEXT,
		reviewed_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	store, err := modelcheckquestionbank.NewStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	admin := modelcheckquestionbank.NewHTTPHandlers(store, nil, time.Now, true)
	self := modelcheckquestionbank.NewHTTPHandlers(store, nil, time.Now, false)
	return store, admin, self
}

// qbMountHandler 照 main.go 的 MountScoped 形态克隆 handler：admin 挂载
// AllowCrossAccount=true，self 挂载 ForceActorScope=true。
func qbMountHandler(t *testing.T, admin, self *modelcheckquestionbank.HTTPHandlers, allowCrossAccount bool) *HTTPHandler {
	t.Helper()
	base := &HTTPHandler{
		Service: fakeRunService{}, Active: modelcheckactive.NewRegistry(),
		Authorize: func(context.Context, *http.Request) (string, error) { return "sys-1", nil },
		Build: func(context.Context, string, RunCommand) (RunRequest, error) {
			return RunRequest{SystemAccountID: "sys-1", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6", Profile: "quick"}, nil
		},
		Heartbeat:         10 * time.Second,
		QuestionBankAdmin: admin,
		QuestionBankSelf:  self,
	}
	clone := *base
	clone.AllowCrossAccount = allowCrossAccount
	clone.ForceActorScope = !allowCrossAccount
	return &clone
}

func qbGet(t *testing.T, handler *HTTPHandler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func qbSend(t *testing.T, handler *HTTPHandler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, strings.NewReader(body)))
	return recorder
}

func qbDecodeData(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	data, _ := parsed["data"].(map[string]any)
	return data
}

// TestQBQuestionBankMountsAndGlobalVisibility 验证题库 8 端点双前缀可达、
// 全局共享不做 ManagementScope 过滤、固定段先于 {id} 匹配、未认证 401。
func TestQBQuestionBankMountsAndGlobalVisibility(t *testing.T) {
	store, admin, self := qbQuestionBankFixture(t)
	selfMount := qbMountHandler(t, admin, self, false)
	adminMount := qbMountHandler(t, admin, self, true)

	// 自助面 sys-1 提交题目（pending）。
	created := qbSend(t, selfMount, http.MethodPost, "/question-bank", `{"title":"自有类目","questionText":"自有题面：2+2=?","referenceAnswer":"4"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("self create status=%d body=%s", created.Code, created.Body.String())
	}
	view := qbDecodeData(t, created.Body.String())
	ownID, _ := view["id"].(string)
	if ownID == "" {
		t.Fatalf("self create body=%s", created.Body.String())
	}

	// sys-2 直接经 store 提交并自审一题 approved、留一题 pending。
	approved, err := store.Create(context.Background(), modelcheckquestionbank.Actor{SystemAccountID: "sys-2"}, modelcheckquestionbank.CreateQuestionInput{Title: "他人类目一", QuestionText: "他人题面：1+1=?", ReferenceAnswer: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Review(context.Background(), approved.ID, modelcheckquestionbank.Actor{SystemAccountID: "sys-2"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	foreignPending, err := store.Create(context.Background(), modelcheckquestionbank.Actor{SystemAccountID: "sys-2"}, modelcheckquestionbank.CreateQuestionInput{Title: "他人类目二", QuestionText: "他人待审题面", ReferenceAnswer: "x"})
	if err != nil {
		t.Fatal(err)
	}

	// 自助面列表：approved（他人）+ 本人提交可见，他人 pending 不可见
	//（题库可见性按 actor 判定，与 ManagementScope 无关）。
	list := qbGet(t, selfMount, http.MethodGet, "/question-bank")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), approved.ID) || !strings.Contains(list.Body.String(), ownID) || strings.Contains(list.Body.String(), foreignPending.ID) {
		t.Fatalf("self list status=%d body=%s", list.Code, list.Body.String())
	}
	// 他人 approved 详情：题面可读但参考答案字段省略（非创建者）。
	foreign := qbGet(t, selfMount, http.MethodGet, "/question-bank/"+approved.ID)
	if foreign.Code != http.StatusOK || strings.Contains(foreign.Body.String(), `"referenceAnswer"`) || strings.Contains(foreign.Body.String(), `"keyPoints"`) {
		t.Fatalf("self foreign detail status=%d body=%s", foreign.Code, foreign.Body.String())
	}

	// 管理面：全量列表（含 sys-2 pending）+ 审核自助面提交（同一张全局表）。
	adminList := qbGet(t, adminMount, http.MethodGet, "/question-bank")
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), foreignPending.ID) {
		t.Fatalf("admin list status=%d body=%s", adminList.Code, adminList.Body.String())
	}
	review := qbSend(t, adminMount, http.MethodPost, "/question-bank/"+foreignPending.ID+"/review", `{"action":"approve"}`)
	if review.Code != http.StatusOK || !strings.Contains(review.Body.String(), `"status":"approved"`) {
		t.Fatalf("admin review self-submitted question: status=%d body=%s", review.Code, review.Body.String())
	}

	// 固定段必须先于 {id}：options/by-ids 走专用端点而非 detail。
	if _, err := store.Review(context.Background(), ownID, modelcheckquestionbank.Actor{SystemAccountID: "sys-1"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	options := qbGet(t, selfMount, http.MethodGet, "/question-bank/options")
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), `"items"`) || !strings.Contains(options.Body.String(), ownID) {
		t.Fatalf("self options status=%d body=%s", options.Code, options.Body.String())
	}
	byIDs := qbGet(t, selfMount, http.MethodGet, "/question-bank/by-ids?ids="+approved.ID+","+ownID)
	if byIDs.Code != http.StatusOK || !strings.Contains(byIDs.Body.String(), approved.ID) || !strings.Contains(byIDs.Body.String(), ownID) {
		t.Fatalf("self by-ids status=%d body=%s", byIDs.Code, byIDs.Body.String())
	}

	// scope 豁免：管理面携带 systemAccountId 查询参数不影响题库可见性
	//（题库路由丢弃 ManagementScope，只按 handler 的 adminMode/actor 判定）。
	scoped := qbGet(t, adminMount, http.MethodGet, "/question-bank?systemAccountId=sys-2")
	if scoped.Code != http.StatusOK || !strings.Contains(scoped.Body.String(), foreignPending.ID) {
		t.Fatalf("admin scoped list status=%d body=%s", scoped.Code, scoped.Body.String())
	}

	// PATCH/DELETE（创建者撤回后重建再用管理员删除另一题，覆盖两个前缀方法）。
	updated := qbGet(t, selfMount, http.MethodPatch, "/question-bank/"+ownID)
	if updated.Code != http.StatusBadRequest {
		t.Fatalf("self patch without body must be 400, got %d", updated.Code)
	}
	deleted := qbGet(t, adminMount, http.MethodDelete, "/question-bank/"+ownID)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("admin delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}

	// 未认证 401：授权失败的请求不得触达题库 handlers。
	unauthorized := qbMountHandler(t, admin, self, false)
	unauthorized.Authorize = func(context.Context, *http.Request) (string, error) { return "", errUnauthorizedStub }
	if status := qbGet(t, unauthorized, http.MethodGet, "/question-bank"); status.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d, want 401", status.Code)
	}

	// owner 未接线（nil handlers）→ 503。
	unwired := qbMountHandler(t, nil, nil, false)
	if status := qbGet(t, unwired, http.MethodGet, "/question-bank"); status.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired question bank status=%d, want 503", status.Code)
	}
}

// errUnauthorizedStub 是未认证臂的可识别错误。
var errUnauthorizedStub = &stubUnauthorizedError{}

type stubUnauthorizedError struct{}

func (*stubUnauthorizedError) Error() string { return "unauthorized" }
