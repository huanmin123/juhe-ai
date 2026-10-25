package modelcheckowner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
)

// qbFakeTestRunner 是提交门禁测试执行器替身：固定返回 pass 结论与账户名，
// 供挂载层端到端验证 POST /question-bank/test 双前缀可达。
type qbFakeTestRunner struct{}

func (qbFakeTestRunner) TestQuestion(ctx context.Context, input modelcheckquestionbank.TestQuestionInput) (modelcheckquestionbank.TestQuestionResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		panic("TestQuestion 必须在带截止时间的 ctx 下调用")
	}
	return modelcheckquestionbank.TestQuestionResult{Verdict: "pass", Alignment: "aligned", AccountName: "测试账户甲", LatencyMs: 42}, nil
}

// qbQuestionBankFixture 构造全局共享题库 store（内存 SQLite）与 admin/self
// 两个 HTTPHandlers 实例（照 compose/main 的装配形态：同 store、双 adminMode、
// 同一测试执行器）。
func qbQuestionBankFixture(t *testing.T) (*modelcheckquestionbank.Store, *modelcheckquestionbank.HTTPHandlers, *modelcheckquestionbank.HTTPHandlers) {
	t.Helper()
	db, err := openPGTestDB(t)
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
		is_builtin INTEGER NOT NULL DEFAULT 0,
		reviewed_by TEXT,
		reviewed_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		last_test_id TEXT,
		last_test_json TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	// 提交门禁测试记录表（maintenance SQLite DDL 同构最小形状）。
	if _, err := db.Exec(`CREATE TABLE model_check_question_tests (
		id TEXT PRIMARY KEY,
		created_by TEXT NOT NULL,
		account_id TEXT NOT NULL,
		account_name TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL,
		draft_hash TEXT NOT NULL,
		verdict TEXT NOT NULL CHECK (verdict IN ('pass', 'fail', 'invalid', 'request_failed')),
		alignment TEXT NOT NULL DEFAULT '' CHECK (alignment IN ('', 'aligned', 'misaligned', 'unknown')),
		diagnosis_json TEXT,
		http_status INTEGER,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	// 管理面 attachCreatorNames 解析提交人显示名，需要 system_accounts 表。
	if _, err := db.Exec(`CREATE TABLE system_accounts (
		id TEXT PRIMARY KEY,
		display_name TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_accounts (id, display_name) VALUES ('sys-1', '提交人甲'), ('sys-2', '提交人乙')`); err != nil {
		t.Fatal(err)
	}
	store, err := modelcheckquestionbank.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	runner := qbFakeTestRunner{}
	admin := modelcheckquestionbank.NewHTTPHandlers(store, nil, time.Now, true, runner)
	self := modelcheckquestionbank.NewHTTPHandlers(store, nil, time.Now, false, runner)
	return store, admin, self
}

// qbSeedPassingTest 按内容造一条 pass 测试记录并返回 testId（提交门禁
// 通过路径造数；keyPoints 恒 nil 与挂载测试的提交体一致）。
func qbSeedPassingTest(t *testing.T, store *modelcheckquestionbank.Store, actor, questionText, referenceAnswer string) string {
	t.Helper()
	qbSeedCounter++
	testID := fmt.Sprintf("mcqt-mount-%d", qbSeedCounter)
	if err := store.InsertTestRecord(context.Background(), modelcheckquestionbank.TestRecord{
		ID: testID, CreatedBy: actor, AccountID: "acct-1", AccountName: "测试账户甲", Model: "gpt-5.6",
		DraftHash: modelcheckquestionbank.DraftHash(questionText, referenceAnswer, nil),
		Verdict:   "pass", Alignment: "aligned", LatencyMs: 42,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	return testID
}

var qbSeedCounter int

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

	// 自助面 sys-1 先同步测试再提交题目（pending；提交门禁端到端）。
	tested := qbSend(t, selfMount, http.MethodPost, "/question-bank/test", `{"questionText":"自有题面：2+2=?","referenceAnswer":"4","accountId":"acct-1","model":"gpt-5.6"}`)
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"testId":"mcqt-`) || !strings.Contains(tested.Body.String(), `"accountName":"测试账户甲"`) {
		t.Fatalf("self POST /question-bank/test status=%d body=%s", tested.Code, tested.Body.String())
	}
	testedViaAdmin := qbSend(t, adminMount, http.MethodPost, "/question-bank/test", `{"questionText":"管理面前缀同样可达。","referenceAnswer":"可达","accountId":"acct-1","model":"gpt-5.6"}`)
	if testedViaAdmin.Code != http.StatusOK || !strings.Contains(testedViaAdmin.Body.String(), `"testId":"mcqt-`) {
		t.Fatalf("admin POST /question-bank/test status=%d body=%s", testedViaAdmin.Code, testedViaAdmin.Body.String())
	}
	selfTestID := qbSeedPassingTest(t, store, "sys-1", "自有题面：2+2=?", "4")
	created := qbSend(t, selfMount, http.MethodPost, "/question-bank", `{"title":"自有类目","questionText":"自有题面：2+2=?","referenceAnswer":"4","testId":"`+selfTestID+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("self create status=%d body=%s", created.Code, created.Body.String())
	}
	view := qbDecodeData(t, created.Body.String())
	ownID, _ := view["id"].(string)
	if ownID == "" {
		t.Fatalf("self create body=%s", created.Body.String())
	}
	if lastTest, ok := view["lastTest"].(map[string]any); !ok || lastTest["testId"] != selfTestID {
		t.Fatalf("created lastTest = %v", view["lastTest"])
	}

	// sys-2 直接经 store 提交并自审一题 approved、留一题 pending。
	approvedText := "他人题面：1+1=?"
	approvedTest := qbSeedPassingTest(t, store, "sys-2", approvedText, "2")
	approved, err := store.Create(context.Background(), modelcheckquestionbank.Actor{SystemAccountID: "sys-2"}, modelcheckquestionbank.CreateQuestionInput{Title: "他人类目一", QuestionText: approvedText, ReferenceAnswer: "2", TestID: approvedTest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Review(context.Background(), approved.ID, modelcheckquestionbank.Actor{SystemAccountID: "sys-2"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}
	foreignText := "他人待审题面"
	foreignTest := qbSeedPassingTest(t, store, "sys-2", foreignText, "x")
	foreignPending, err := store.Create(context.Background(), modelcheckquestionbank.Actor{SystemAccountID: "sys-2"}, modelcheckquestionbank.CreateQuestionInput{Title: "他人类目二", QuestionText: foreignText, ReferenceAnswer: "x", TestID: foreignTest})
	if err != nil {
		t.Fatal(err)
	}

	// 自助面列表：approved（他人）+ 本人提交可见，他人 pending 不可见
	//（题库可见性按 actor 判定，与 ManagementScope 无关）。
	list := qbGet(t, selfMount, http.MethodGet, "/question-bank")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), approved.ID) || !strings.Contains(list.Body.String(), ownID) || strings.Contains(list.Body.String(), foreignPending.ID) {
		t.Fatalf("self list status=%d body=%s", list.Code, list.Body.String())
	}
	// 他人 approved 详情：题面与参考答案/要点全员可见（题库共享契约）。
	foreign := qbGet(t, selfMount, http.MethodGet, "/question-bank/"+approved.ID)
	if foreign.Code != http.StatusOK || !strings.Contains(foreign.Body.String(), `"referenceAnswer"`) || !strings.Contains(foreign.Body.String(), `"keyPoints"`) {
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

	// 未认证 401：授权失败的请求不得触达题库 handlers。BUG-0256 后
	// 错误映射按哨兵分流，"未认证"必须用 ErrLoginRequired 哨兵表达
	//（非哨兵自定义错误按新契约归 500）。
	unauthorized := qbMountHandler(t, admin, self, false)
	unauthorized.Authorize = func(context.Context, *http.Request) (string, error) { return "", modelcheckauth.ErrLoginRequired }
	if status := qbGet(t, unauthorized, http.MethodGet, "/question-bank"); status.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d, want 401", status.Code)
	}

	// owner 未接线（nil handlers）→ 503；/test 字面路径同样不受 /{id}
	// 前缀分支吞噬（404 会伪装成"路由存在但题库未接线"）。
	unwired := qbMountHandler(t, nil, nil, false)
	if status := qbGet(t, unwired, http.MethodGet, "/question-bank"); status.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired question bank status=%d, want 503", status.Code)
	}
	if status := qbSend(t, unwired, http.MethodPost, "/question-bank/test", `{}`); status.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired question bank test status=%d, want 503", status.Code)
	}
}
