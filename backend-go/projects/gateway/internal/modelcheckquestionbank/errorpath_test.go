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
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// 本文件补齐 http_test.go / store_test.go / similarity_test.go 未覆盖的
// 错误与边界路径：空路径 id 防御、JSON 解码失败、分页/limit 边界、缺
// 操作者身份、存储层错误上抛（连接失败、方言缺表、触发器注入的写失败、
// 损坏数据）与 nil 审计 sink 契约。

// assertNotStatusError 断言错误不是调用方可见的 StatusError：存储/驱动层
// 失败必须原样上抛（HTTP 层按 500 渲染），不得被包装成域错误。
func assertNotStatusError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var status *StatusError
	if errors.As(err, &status) {
		t.Fatalf("expected raw storage error, got StatusError %d/%q", status.Status, status.Message)
	}
}

func assertStatusErrorCode(t *testing.T, err error, wantStatus int) *StatusError {
	t.Helper()
	code := statusErrorCode(t, err)
	if code.Status != wantStatus {
		t.Fatalf("status = %d, want %d", code.Status, wantStatus)
	}
	return code
}

// buildAuthedRequest 是 call 的直连版本：绕过 dispatch 直接构造带认证
// 上下文的请求，供逐个调用 handler 的防御分支。
func buildAuthedRequest(t *testing.T, auth *authsys.AuthContext, method, target, body string) *http.Request {
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
	return request
}

// TestHandlersEmptyPathIDReturns404 覆盖条目 handler 的空 id 防御分支：
// 正常挂载不会路由到空 id，但 handler 必须以 404 拒绝而非继续读写。
func TestHandlersEmptyPathIDReturns404(t *testing.T) {
	h := newHandlerHarness(t)
	cases := []struct {
		name   string
		invoke func(w http.ResponseWriter, r *http.Request)
	}{
		{"详情", func(w http.ResponseWriter, r *http.Request) { h.admin.ServeDetail(w, r) }},
		{"修改", func(w http.ResponseWriter, r *http.Request) { h.admin.ServeUpdate(w, r) }},
		{"删除", func(w http.ResponseWriter, r *http.Request) { h.admin.ServeDelete(w, r) }},
		{"审核", func(w http.ResponseWriter, r *http.Request) { h.admin.ServeReview(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.invoke(recorder, buildAuthedRequest(t, h.adminActor, http.MethodGet, "http://svc/", ""))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s: status = %d body=%s", tc.name, recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "题目不存在") {
				t.Fatalf("body = %s", recorder.Body.String())
			}
		})
	}
}

// TestHandlersBodyDecodeErrors 覆盖 Update/Review 的 JSON 解码失败与尾随
// 内容拒绝（decodeQuestionBody 的错误分支已由创建参数校验覆盖）。
func TestHandlersBodyDecodeErrors(t *testing.T) {
	h := newHandlerHarness(t)
	recorder := call(t, h.self, aliceAuth(), http.MethodPatch, "/q/mcq-x", "not-json")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("update bad body = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "题库题目参数无效") {
		t.Fatalf("body = %s", recorder.Body.String())
	}

	recorder = call(t, h.admin, h.adminActor, http.MethodPost, "/q/mcq-x/review", "not-json")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("review bad body = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "题库审核参数无效") {
		t.Fatalf("body = %s", recorder.Body.String())
	}

	recorder = call(t, h.admin, h.adminActor, http.MethodPost, "/q/mcq-x/review", `{"action":"approve"} {"x":1}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("review trailing json = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "题库审核参数无效") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// TestHandlersOptionsKeywordAndLimit 覆盖选项端点的关键字上限、显式合法
// limit 赋值与正常返回。
func TestHandlersOptionsKeywordAndLimit(t *testing.T) {
	h := newHandlerHarness(t)
	alice := aliceAuth()
	approved := mustCreateQuestion(t, h, alice, "可选题目", "选项端点的已过内容。")
	approvedID := approved["id"].(string)
	if _, err := h.store.Review(context.Background(), approvedID, Actor{SystemAccountID: "sys-admin"}, true, "approve", ""); err != nil {
		t.Fatal(err)
	}

	recorder := call(t, h.self, alice, http.MethodGet, "/q/options?keyword="+strings.Repeat("长", 101), "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("long keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "题库关键字过长") {
		t.Fatalf("body = %s", recorder.Body.String())
	}

	recorder = call(t, h.self, alice, http.MethodGet, "/q/options?limit=1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("limit=1 = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0]["id"] != approvedID {
		t.Fatalf("items = %v", payload.Data.Items)
	}
}

// TestHandlersListPaginationEcho 覆盖 readPagination 的合法显式取值路径
// （page/pageSize 正常赋值并回显）。
func TestHandlersListPaginationEcho(t *testing.T) {
	h := newHandlerHarness(t)
	recorder := call(t, h.self, aliceAuth(), http.MethodGet, "/q?page=2&pageSize=10", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Page     int `json:"page"`
			PageSize int `json:"pageSize"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Page != 2 || payload.Data.PageSize != 10 {
		t.Fatalf("pagination echo = %+v", payload.Data)
	}
}

// TestHandlersStoreFailureRenders500 覆盖 options/by-ids 的存储错误渲染；
// 列表端点的 500 渲染由 TestHandlersInternalErrorsRender500 覆盖。
func TestHandlersStoreFailureRenders500(t *testing.T) {
	h := newHandlerHarness(t)
	h.db.Close()
	for _, tc := range []struct{ name, target string }{
		{"选项", "/q/options?limit=10"},
		{"按 id 批量", "/q/by-ids?ids=mcq-a"},
	} {
		recorder := call(t, h.admin, h.adminActor, http.MethodGet, tc.target, "")
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d body=%s", tc.name, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "服务器内部错误") {
			t.Fatalf("%s: body = %s", tc.name, recorder.Body.String())
		}
	}
}

// TestHandlersNilAuditSinkIsOptional 固化审计 sink 可选契约：未注入时
// 写操作照常成功（recordAudit 直接返回，不落审计）。
func TestHandlersNilAuditSinkIsOptional(t *testing.T) {
	store, _ := newTestStore(t)
	handlers := NewHTTPHandlers(store, nil, nil, false)
	recorder := call(t, handlers, aliceAuth(), http.MethodPost, "/q", createBody("无审计题目", "无审计提交的内容。", "答案", nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestQuestionViewNilKeyPointsRendersEmptyArray 固化视图契约：内存态
// Question 的 nil 要点在有权视角必须渲染为空数组而非 null；无权视角
// 必须省略答案与要点字段。
func TestQuestionViewNilKeyPointsRendersEmptyArray(t *testing.T) {
	view := questionView(Question{ID: "mcq-1", ReferenceAnswer: "答案"}, true)
	if view.KeyPoints == nil {
		t.Fatal("nil keyPoints 必须渲染为空数组")
	}
	if len(*view.KeyPoints) != 0 || view.ReferenceAnswer != "答案" {
		t.Fatalf("view = %+v", view)
	}
	restricted := questionView(Question{ID: "mcq-1", ReferenceAnswer: "答案", KeyPoints: []string{"要点"}}, false)
	if restricted.ReferenceAnswer != "" || restricted.KeyPoints != nil {
		t.Fatalf("无权视角必须省略答案与要点: %+v", restricted)
	}
}

// TestStatusErrorNilReceiverIsSafe 固化 nil 接收者安全契约。
func TestStatusErrorNilReceiverIsSafe(t *testing.T) {
	var missing *StatusError
	if missing.Error() != "" {
		t.Fatalf("nil StatusError.Error() = %q", missing.Error())
	}
}

// TestLengthPreFilterSkipsEmptyTexts 固化空文本无相似证据：两侧归一化后
// 为空时长度预过滤直接跳过，不进入 Jaccard。
func TestLengthPreFilterSkipsEmptyTexts(t *testing.T) {
	if lengthPreFilterPasses(nil, nil) {
		t.Fatal("两侧空序列必须跳过比较")
	}
	bestID, _, score, found := FindMostSimilar("  \n\t", []StoredQuestionText{{ID: "mcq-empty", Title: "空白题", Text: "　"}})
	if found || score != 0 || bestID != "" {
		t.Fatalf("空文本不得构成相似证据: id=%q score=%v found=%v", bestID, score, found)
	}
}

// TestStoreMissingActorUnauthorized 覆盖 store 各入口缺操作者身份的 401
// 防御（store 不依赖 handler 的认证前置）。
func TestStoreMissingActorUnauthorized(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	_, _, err := store.List(ctx, QuestionFilter{Page: 1, PageSize: 20})
	assertStatusErrorCode(t, err, statusUnauthorized)
	_, err = store.Update(ctx, "mcq-x", Actor{}, false, UpdateQuestionInput{Title: "标题", QuestionText: "内容", ReferenceAnswer: "答案"})
	assertStatusErrorCode(t, err, statusUnauthorized)
	err = store.Delete(ctx, "mcq-x", Actor{}, false)
	assertStatusErrorCode(t, err, statusUnauthorized)
	_, err = store.Review(ctx, "mcq-x", Actor{}, true, "approve", "")
	assertStatusErrorCode(t, err, statusUnauthorized)
}

// TestStoreUpdateInputValidation 覆盖 Update 的字段校验入口（与 Create
// 共用归一化，但调用点独立）。
func TestStoreUpdateInputValidation(t *testing.T) {
	store, _ := newTestStore(t)
	question := mustCreate(t, store, actorAlice, "待校验题", "待校验的题目内容。")
	_, err := store.Update(context.Background(), question.ID, actorAlice, false,
		UpdateQuestionInput{Title: " ", QuestionText: "内容", ReferenceAnswer: "答案"})
	code := assertStatusErrorCode(t, err, statusBadRequest)
	if code.Message != "题目标题不能为空" {
		t.Fatalf("message = %q", code.Message)
	}
}

// TestStoreReviewRejectReasonTooLong 覆盖驳回理由 rune 长度上限。
func TestStoreReviewRejectReasonTooLong(t *testing.T) {
	store, _ := newTestStore(t)
	question := mustCreate(t, store, actorAlice, "驳回上限题", "驳回理由上限测试内容。")
	_, err := store.Review(context.Background(), question.ID, Actor{SystemAccountID: "sys-admin"}, true,
		"reject", strings.Repeat("长", RejectReasonMaxRunes+1))
	code := assertStatusErrorCode(t, err, statusBadRequest)
	if code.Message != "驳回理由不能超过 500 字" {
		t.Fatalf("message = %q", code.Message)
	}
}

// TestStoreListByIDsInvalidStatus 覆盖批量取题的 status 过滤校验分支。
func TestStoreListByIDsInvalidStatus(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.ListByIDs(context.Background(), []string{"mcq-x"}, "draft")
	assertStatusErrorCode(t, err, statusBadRequest)
}

// TestStoreClosedDBErrorsPropagate 覆盖连接层失败原样上抛（BeginTx、
// COUNT、ListByIDs、ListOptions 各入口），HTTP 层按 500 渲染。
func TestStoreClosedDBErrorsPropagate(t *testing.T) {
	store, db := newTestStore(t)
	db.Close()
	ctx := context.Background()
	_, err := store.Create(ctx, actorAlice, CreateQuestionInput{Title: "标题", QuestionText: "内容", ReferenceAnswer: "答案"})
	assertNotStatusError(t, err)
	_, _, listErr := store.List(ctx, QuestionFilter{Admin: true, Page: 1, PageSize: 20})
	assertNotStatusError(t, listErr)
	_, err = store.Update(ctx, "mcq-x", actorAlice, false, UpdateQuestionInput{Title: "标题", QuestionText: "内容", ReferenceAnswer: "答案"})
	assertNotStatusError(t, err)
	_, err = store.ListByIDs(ctx, []string{"mcq-x"}, StatusApproved)
	assertNotStatusError(t, err)
	_, err = store.ListOptions(ctx, "", defaultOptionsLimit)
	assertNotStatusError(t, err)
}

// insertRawQuestion 直接写入题目行（绕过 Create 的查重），供分页边界与
// 损坏数据测试造数；createdAt 使用固定宽度纳秒保证字典序=时间序。
func insertRawQuestion(t *testing.T, db *sql.DB, id, title, text, status, createdAt string, keyPointsJSON any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO model_check_question_bank
		(id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,NULL,?,?,NULL,NULL,?,?)`,
		id, title, NormalizeText(title), text, "答案", keyPointsJSON, status, "sys-seed", "sys-seed", createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

// sameQuestionIDs 判断两个题目切片的 id 序列完全一致。
func sameQuestionIDs(a, b []Question) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// TestStorePaginationAndLimitClamp 固化 normalizePage 与选项 limit 的
// 全部三个边界：page<1 回退 1、pageSize<1 回退默认 20、pageSize>100
// 收敛 100；选项 limit<1 回退默认 50、>100 收敛 100。以 101 行造数保证
// 边界可观测（不归一化时行数会不同）。
func TestStorePaginationAndLimitClamp(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 101; i++ {
		insertRawQuestion(t, db, fmt.Sprintf("mcq-seed-%03d", i),
			fmt.Sprintf("造数题目%03d", i), fmt.Sprintf("造数内容%03d，彼此明显不同。", i),
			StatusApproved, fmt.Sprintf("2026-01-01T00:00:00.%09dZ", i), nil)
	}

	firstPage, _, err := store.List(ctx, QuestionFilter{Admin: true, Page: 1, PageSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	zeroPage, _, err := store.List(ctx, QuestionFilter{Admin: true, Page: 0, PageSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(zeroPage) != 5 || !sameQuestionIDs(firstPage, zeroPage) {
		t.Fatalf("page<1 必须等价 page=1: first=%d zero=%d", len(firstPage), len(zeroPage))
	}

	defaultSize, total, err := store.List(ctx, QuestionFilter{Admin: true, Page: 1, PageSize: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 101 || len(defaultSize) != defaultListPageSize {
		t.Fatalf("pageSize<1 必须回退 %d: got %d items (total %d)", defaultListPageSize, len(defaultSize), total)
	}

	clamped, _, err := store.List(ctx, QuestionFilter{Admin: true, Page: 1, PageSize: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(clamped) != maxListPageSize {
		t.Fatalf("pageSize>100 必须收敛 %d: got %d", maxListPageSize, len(clamped))
	}

	options, err := store.ListOptions(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != defaultOptionsLimit {
		t.Fatalf("limit<1 必须回退 %d: got %d", defaultOptionsLimit, len(options))
	}
	options, err = store.ListOptions(ctx, "", maxOptionsLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != maxOptionsLimit {
		t.Fatalf("limit>100 必须收敛 %d: got %d", maxOptionsLimit, len(options))
	}
}

// TestStoreCorruptKeyPointsJSONPropagates 固化损坏数据的错误上抛契约：
// key_points_json 非法 JSON 时所有读取入口必须返回错误而非静默丢要点。
func TestStoreCorruptKeyPointsJSONPropagates(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()
	insertRawQuestion(t, db, "mcq-corrupt", "损坏要点题", "损坏要点的题目内容。", StatusApproved, "2026-01-01T00:00:00.000000000Z", "not-json")
	_, err := store.GetByID(ctx, "mcq-corrupt")
	assertNotStatusError(t, err)
	_, _, err = store.List(ctx, QuestionFilter{Admin: true, Page: 1, PageSize: 20})
	assertNotStatusError(t, err)
	_, err = store.ListByIDs(ctx, []string{"mcq-corrupt"}, StatusApproved)
	assertNotStatusError(t, err)
}

// TestStorePostgresDialectMissingTable 固化 postgres 方言（juhe_business
// 前缀）缺表时的错误上抛：事务内查重查询失败必须中断提交而非静默放行。
func TestStorePostgresDialectMissingTable(t *testing.T) {
	_, db := newTestStore(t)
	pgStore, err := NewStore(db, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pgStore.Create(context.Background(), actorBob,
		CreateQuestionInput{Title: "方言题目", QuestionText: "方言查重路径的内容。", ReferenceAnswer: "答案"})
	assertNotStatusError(t, err)
}

// TestStoreExecFailuresRollBack 用 RAISE(ABORT) 触发器注入写失败，固化
// Create/Update/Delete/Review 四个写入口的执行错误原样上抛与无部分写入
// （事务回滚，原题不变）。
func TestStoreExecFailuresRollBack(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()
	mustCreate(t, store, actorAlice, "故障注入保留题", "故障注入保留的题目内容。")
	target := mustCreate(t, store, actorAlice, "故障注入目标题", "故障注入目标的题目内容。")
	for name, ddl := range map[string]string{
		"insert": `CREATE TRIGGER block_qb_insert BEFORE INSERT ON model_check_question_bank BEGIN SELECT RAISE(ABORT, 'blocked-insert'); END`,
		"update": `CREATE TRIGGER block_qb_update BEFORE UPDATE ON model_check_question_bank BEGIN SELECT RAISE(ABORT, 'blocked-update'); END`,
		"delete": `CREATE TRIGGER block_qb_delete BEFORE DELETE ON model_check_question_bank BEGIN SELECT RAISE(ABORT, 'blocked-delete'); END`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create trigger %s: %v", name, err)
		}
	}

	// Create：事务内查重通过后 INSERT 失败。
	_, err := store.Create(ctx, actorBob, CreateQuestionInput{Title: "故障注入新题", QuestionText: "故障注入新题的独立内容。", ReferenceAnswer: "答案"})
	assertNotStatusError(t, err)

	// Update：权限与查重通过后 UPDATE 失败，原题保持不变。
	_, err = store.Update(ctx, target.ID, actorAlice, false,
		UpdateQuestionInput{Title: "不应生效的新标题", QuestionText: "不应生效的新内容。", ReferenceAnswer: "答案"})
	assertNotStatusError(t, err)

	// Delete：权限校验通过后 DELETE 失败。
	err = store.Delete(ctx, target.ID, actorAlice, false)
	assertNotStatusError(t, err)

	// Review：CAS UPDATE 失败。
	_, err = store.Review(ctx, target.ID, Actor{SystemAccountID: "sys-admin"}, true, "approve", "")
	assertNotStatusError(t, err)

	final, err := store.GetByID(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Title != "故障注入目标题" || final.Status != StatusPending {
		t.Fatalf("失败写入不得改变原题: %+v", final)
	}
}
