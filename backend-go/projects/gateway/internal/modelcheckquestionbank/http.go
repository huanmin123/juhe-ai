package modelcheckquestionbank

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// 题库 HTTP 层的查询参数上限（照 modelcheckowner keyword ≤100 与
// 分页 1..100 的既有惯例）与请求体上限。
const (
	maxKeywordRunes     = 100
	byIDsLimit          = 20
	questionBankMaxBody = 512 << 10
)

// QuestionView 是题库对外 JSON 契约（前端并行开发依赖，字段名不得改动）：
//
//	{ id, title, questionText, referenceAnswer, keyPoints, status, rejectReason,
//	  createdBy, createdAt, updatedAt, reviewedAt }
//
// 无权限时的处理约定（本包唯一契约解释）：referenceAnswer 与 keyPoints
// 选择【省略字段】——referenceAnswer 为必填非空，omitempty 即等价于
// "无权限"；keyPoints 用 *[]string 区分"无权限省略"与"有权但为空数组
// （渲染 []）"。rejectReason/reviewedAt 无值渲染 null。titleNorm 与
// reviewedBy 不进对外契约。
type QuestionView struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	QuestionText    string    `json:"questionText"`
	ReferenceAnswer string    `json:"referenceAnswer,omitempty"`
	KeyPoints       *[]string `json:"keyPoints,omitempty"`
	Status          string    `json:"status"`
	RejectReason    *string   `json:"rejectReason"`
	CreatedBy       string    `json:"createdBy"`
	CreatedAt       string    `json:"createdAt"`
	UpdatedAt       string    `json:"updatedAt"`
	ReviewedAt      *string   `json:"reviewedAt"`
}

// QuestionList 是列表契约：{ items, total, page, pageSize }。
type QuestionList struct {
	Items    []QuestionView `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}

// QuestionOptionView 是选项契约元素 { id, title }；选项列表契约：
// { items: [...] }。
type QuestionOptionView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type OptionsList struct {
	Items []QuestionOptionView `json:"items"`
}

// HTTPHandlers 承载题库八个 HTTP 端点（供 Wave2 挂载）：
//
//	ServeList    GET    …/question-bank?status=&keyword=&page=&pageSize=
//	ServeCreate  POST   …/question-bank
//	ServeDetail  GET    …/question-bank/{id}
//	ServeUpdate  PATCH  …/question-bank/{id}
//	ServeDelete  DELETE …/question-bank/{id}
//	ServeReview  POST   …/question-bank/{id}/review
//	ServeOptions GET    …/question-bank/options?keyword=&limit=
//	ServeByIds   GET    …/question-bank/by-ids?ids=a,b,c
//
// 方法与路径由挂载方负责；handler 只认"路径末段 = 题目 id"（review 预先
// 剥离 /review 后缀），两种挂载形态（ServeMux 通配或前缀委托）都成立，
// 但 by-ids/options 为字面路径，挂载方须让它们优先于 /{id} 条目路由。
// 认证同样由挂载方完成：handler 从 authsys 会话上下文取当前 actor。
// adminMode=true 的实例（管理面前缀）返回全量并可审核（含驳回下架
// approved 题目）；false 的实例（自助面前缀）只返回 approved + 本人提交，
// 且参考答案/要点仅本人题目可见，审核一律 403。写操作接
// authsys.OperationLogSink 审计。题库是全局共享数据，不做
// ManagementScope 系统账户过滤。
type HTTPHandlers struct {
	store     *Store
	audit     authsys.OperationLogSink
	clock     func() time.Time
	adminMode bool
}

// NewHTTPHandlers 构造题库 handlers。clock 是显式注入的时钟：当前 JSON
// 契约不含服务器时间戳（时间一律来自 Store 写入值），该注入点保留给
// 挂载方统一时钟与后续契约扩展，nil 时回退 time.Now。
func NewHTTPHandlers(store *Store, audit authsys.OperationLogSink, clock func() time.Time, adminMode bool) *HTTPHandlers {
	if clock == nil {
		clock = time.Now
	}
	return &HTTPHandlers{store: store, audit: audit, clock: clock, adminMode: adminMode}
}

// ServeList 列出题目。管理员视角返回全部状态并支持 status 筛选；自助
// 视角固定返回 approved + 本人提交（status/keyword 过滤与其 AND 组合）。
func (h *HTTPHandlers) ServeList(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := h.requireActor(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	status := strings.TrimSpace(query.Get("status"))
	if status != "" && !validStatus(status) {
		kernel.WriteBadRequest(w, "题库状态筛选无效")
		return
	}
	keyword, ok := h.readKeyword(w, query.Get("keyword"))
	if !ok {
		return
	}
	page, pageSize, ok := readPagination(w, query)
	if !ok {
		return
	}
	items, total, err := h.store.List(r.Context(), QuestionFilter{
		Status: status, Keyword: keyword, Actor: actor, Admin: h.adminMode,
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	views := make([]QuestionView, 0, len(items))
	for _, question := range items {
		views = append(views, questionView(question, h.canSeeAnswer(question, actor)))
	}
	kernel.WriteOK(w, QuestionList{Items: views, Total: total, Page: page, PageSize: pageSize}, "")
}

// ServeCreate 提交题目：201 + {data: Question}（创建状态码照 announcements
// POST 惯例），查重冲突返回 409。
func (h *HTTPHandlers) ServeCreate(w http.ResponseWriter, r *http.Request) {
	actor, auth, ok := h.requireActor(w, r)
	if !ok {
		return
	}
	body, ok := decodeQuestionBody(w, r)
	if !ok {
		return
	}
	question, err := h.store.Create(r.Context(), actor, CreateQuestionInput{
		Title: body.Title, QuestionText: body.QuestionText,
		ReferenceAnswer: body.ReferenceAnswer, KeyPoints: body.KeyPoints,
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	h.recordAudit(auth, r, "create", "model_check_question_bank.create", question.ID, question.Title,
		"提交检测题目："+question.Title, []authsys.OperationLogChange{
			{Field: "title", Label: "标题", After: question.Title},
			{Field: "status", Label: "状态", After: question.Status},
		})
	kernel.WriteJSON(w, http.StatusCreated, map[string]any{"data": questionView(question, true)})
}

// ServeDetail 查看单题：approved 题面全员可读；pending/rejected 题目仅
// 创建者与管理员可见（其他人一律 404，不泄露存在性）；参考答案/要点仅
// 创建者与管理员可见。
func (h *HTTPHandlers) ServeDetail(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := h.requireActor(w, r)
	if !ok {
		return
	}
	id := pathID(r)
	if id == "" {
		kernel.WriteNotFound(w, "题目不存在")
		return
	}
	question, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	if !h.adminMode && question.CreatedBy != actor.SystemAccountID && question.Status != StatusApproved {
		kernel.WriteNotFound(w, "题目不存在")
		return
	}
	kernel.WriteOK(w, questionView(question, h.canSeeAnswer(question, actor)), "")
}

// ServeUpdate 修改题目（创建者或管理员，仅 pending/rejected）。
func (h *HTTPHandlers) ServeUpdate(w http.ResponseWriter, r *http.Request) {
	actor, auth, ok := h.requireActor(w, r)
	if !ok {
		return
	}
	id := pathID(r)
	if id == "" {
		kernel.WriteNotFound(w, "题目不存在")
		return
	}
	body, ok := decodeQuestionBody(w, r)
	if !ok {
		return
	}
	question, err := h.store.Update(r.Context(), id, actor, h.adminMode, UpdateQuestionInput{
		Title: body.Title, QuestionText: body.QuestionText,
		ReferenceAnswer: body.ReferenceAnswer, KeyPoints: body.KeyPoints,
	})
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	h.recordAudit(auth, r, "update", "model_check_question_bank.update", question.ID, question.Title,
		"更新检测题目："+question.Title, []authsys.OperationLogChange{
			{Field: "title", Label: "标题", After: question.Title},
			{Field: "status", Label: "状态", After: question.Status},
		})
	kernel.WriteOK(w, questionView(question, true), "")
}

// ServeDelete 硬删除题目（管理员任意；创建者限本人 pending/rejected）。
// 成功返回 204（照 announcements DELETE 惯例）。
func (h *HTTPHandlers) ServeDelete(w http.ResponseWriter, r *http.Request) {
	actor, auth, ok := h.requireActor(w, r)
	if !ok {
		return
	}
	id := pathID(r)
	if id == "" {
		kernel.WriteNotFound(w, "题目不存在")
		return
	}
	// 预取标题用于审计命名；不存在时由 Delete 给出同样的 404。
	title := ""
	if question, err := h.store.GetByID(r.Context(), id); err == nil {
		title = question.Title
	}
	if err := h.store.Delete(r.Context(), id, actor, h.adminMode); err != nil {
		h.writeStoreError(w, err)
		return
	}
	changes := []authsys.OperationLogChange{}
	summary := "删除检测题目"
	if title != "" {
		summary = "删除检测题目：" + title
		changes = append(changes, authsys.OperationLogChange{
			Field: "deleted", Label: "删除状态", BeforeValue: false, AfterValue: true,
		})
	}
	h.recordAudit(auth, r, "delete", "model_check_question_bank.delete", id, title, summary, changes)
	w.WriteHeader(http.StatusNoContent)
}

// ServeReview 审核题目（仅管理面实例）：{action:"approve"|"reject",
// reason?}；reject 必填理由；状态 CAS，双审/已不存在返回 409。approve 仅
// 允许 pending→approved；reject 额外允许 approved→rejected（驳回下架）。
func (h *HTTPHandlers) ServeReview(w http.ResponseWriter, r *http.Request) {
	actor, auth, ok := h.requireActor(w, r)
	if !ok {
		return
	}
	if !h.adminMode {
		kernel.WriteError(w, http.StatusForbidden, "仅管理员可以审核题目")
		return
	}
	id := reviewPathID(r)
	if id == "" {
		kernel.WriteNotFound(w, "题目不存在")
		return
	}
	body, ok := decodeReviewBody(w, r)
	if !ok {
		return
	}
	// 预取审核前状态用于区分"驳回"与"驳回下架"审计文案；不存在时由
	// Review 的 CAS 给出同样的 409。
	previousStatus := ""
	if question, err := h.store.GetByID(r.Context(), id); err == nil {
		previousStatus = question.Status
	}
	question, err := h.store.Review(r.Context(), id, actor, h.adminMode, body.Action, body.Reason)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	summary := "审核通过检测题目：" + question.Title
	action := "approve"
	changes := []authsys.OperationLogChange{
		{Field: "status", Label: "状态", Before: previousStatus, After: question.Status},
	}
	if body.Action == "reject" {
		action = "reject"
		summary = "驳回检测题目：" + question.Title
		if previousStatus == StatusApproved {
			summary = "驳回下架检测题目：" + question.Title
		}
		changes = append(changes, authsys.OperationLogChange{Field: "rejectReason", Label: "驳回理由", After: body.Reason})
	}
	h.recordAudit(auth, r, action, "model_check_question_bank.review", question.ID, question.Title, summary, changes)
	kernel.WriteOK(w, questionView(question, true), "")
}

// ServeOptions 是选择器专用端点：仅 approved，返回 {items:[{id,title}]}。
func (h *HTTPHandlers) ServeOptions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.requireActor(w, r); !ok {
		return
	}
	keyword, ok := h.readKeyword(w, r.URL.Query().Get("keyword"))
	if !ok {
		return
	}
	limit := defaultOptionsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxOptionsLimit {
			kernel.WriteBadRequest(w, "题库选项 limit 必须是 1 到 100 的整数")
			return
		}
		limit = parsed
	}
	options, err := h.store.ListOptions(r.Context(), keyword, limit)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	views := make([]QuestionOptionView, 0, len(options))
	for _, option := range options {
		views = append(views, QuestionOptionView{ID: option.ID, Title: option.Title})
	}
	kernel.WriteOK(w, OptionsList{Items: views}, "")
}

// ServeByIds 按已选 id 批量回显题目标签（选择器编辑态）：GET
// …/question-bank/by-ids?ids=a,b,c。ids 以逗号分隔、去重后上限
// byIDsLimit 个；仅返回 status='approved' 题目的 {items:[{id,title}]}，
// 不返回题面与参考答案；失效 id（被删/未通过）静默过滤，空/缺失 ids
// 返回空 items。挂载方须让该字面路径优先于 /{id} 条目路由。
func (h *HTTPHandlers) ServeByIds(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.requireActor(w, r); !ok {
		return
	}
	ids := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	if raw := strings.TrimSpace(r.URL.Query().Get("ids")); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > byIDsLimit {
		kernel.WriteBadRequest(w, "题库按 id 查询数量不能超过 20")
		return
	}
	questions, err := h.store.ListByIDs(r.Context(), ids, StatusApproved)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	views := make([]QuestionOptionView, 0, len(questions))
	for _, question := range questions {
		views = append(views, QuestionOptionView{ID: question.ID, Title: question.Title})
	}
	kernel.WriteOK(w, OptionsList{Items: views}, "")
}

func (h *HTTPHandlers) requireActor(w http.ResponseWriter, r *http.Request) (Actor, *authsys.AuthContext, bool) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil || strings.TrimSpace(auth.SystemAccountID) == "" {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录后再操作题库")
		return Actor{}, nil, false
	}
	return Actor{SystemAccountID: strings.TrimSpace(auth.SystemAccountID)}, auth, true
}

// canSeeAnswer 决定参考答案/要点是否进入响应：管理员或题目创建者可见。
func (h *HTTPHandlers) canSeeAnswer(question Question, actor Actor) bool {
	return h.adminMode || question.CreatedBy == actor.SystemAccountID
}

func (h *HTTPHandlers) readKeyword(w http.ResponseWriter, raw string) (string, bool) {
	keyword := strings.TrimSpace(raw)
	if len([]rune(keyword)) > maxKeywordRunes {
		kernel.WriteBadRequest(w, "题库关键字过长")
		return "", false
	}
	return keyword, true
}

func (h *HTTPHandlers) writeStoreError(w http.ResponseWriter, err error) {
	var status *StatusError
	if errors.As(err, &status) {
		kernel.WriteError(w, status.Status, status.Message)
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

func (h *HTTPHandlers) recordAudit(auth *authsys.AuthContext, r *http.Request, action, operationKey, resourceID, resourceTitle, summary string, changes []authsys.OperationLogChange) {
	if h.audit == nil || auth == nil {
		return
	}
	mode := "self"
	if h.adminMode {
		mode = "admin"
	}
	h.audit.Record(authsys.OperationLogEntry{
		ActorSystemAccountID:          auth.SystemAccountID,
		ActorUsername:                 auth.Username,
		ActorDisplayName:              auth.DisplayName,
		ActorRole:                     auth.Role,
		OperationScopeSystemAccountID: auth.SystemAccountID,
		Mode:                          mode,
		Module:                        "model_check_question_bank",
		Action:                        action,
		OperationKey:                  operationKey,
		ResourceType:                  "model_check_question",
		ResourceID:                    resourceID,
		ResourceName:                  resourceTitle,
		Summary:                       summary,
		VisibilityScope:               "admin_only",
		DetailLevel:                   "full",
		Changes:                       changes,
	}, r)
}

// questionView 按权限渲染对外契约；includeAnswer=false 时
// referenceAnswer/keyPoints 省略字段（见 QuestionView 契约注释）。
func questionView(question Question, includeAnswer bool) QuestionView {
	view := QuestionView{
		ID:           question.ID,
		Title:        question.Title,
		QuestionText: question.QuestionText,
		Status:       question.Status,
		CreatedBy:    question.CreatedBy,
		CreatedAt:    question.CreatedAt,
		UpdatedAt:    question.UpdatedAt,
	}
	if question.RejectReason != "" {
		rejectReason := question.RejectReason
		view.RejectReason = &rejectReason
	}
	if question.ReviewedAt != "" {
		reviewedAt := question.ReviewedAt
		view.ReviewedAt = &reviewedAt
	}
	if includeAnswer {
		view.ReferenceAnswer = question.ReferenceAnswer
		keyPoints := question.KeyPoints
		if keyPoints == nil {
			keyPoints = []string{}
		}
		view.KeyPoints = &keyPoints
	}
	return view
}

// pathID 取路径最后一段作为题目 id；reviewPathID 先剥离 /review 后缀。
func pathID(r *http.Request) string {
	return lastPathSegment(strings.TrimSuffix(r.URL.Path, "/"))
}

func reviewPathID(r *http.Request) string {
	return lastPathSegment(strings.TrimSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/review"))
}

func lastPathSegment(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

// questionBody 是 Create/Update 共用入参契约：
// { title, questionText, referenceAnswer, keyPoints? }。
type questionBody struct {
	Title           string   `json:"title"`
	QuestionText    string   `json:"questionText"`
	ReferenceAnswer string   `json:"referenceAnswer"`
	KeyPoints       []string `json:"keyPoints,omitempty"`
}

// decodeQuestionBody 照 modelcheckowner decodeOwnerJSON 惯例：单个 JSON
// 对象、拒绝未知字段、512KB 上限；无效一律 400"题库题目参数无效"。
func decodeQuestionBody(w http.ResponseWriter, r *http.Request) (questionBody, bool) {
	var body questionBody
	decoder := json.NewDecoder(io.LimitReader(r.Body, questionBankMaxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		kernel.WriteBadRequest(w, "题库题目参数无效")
		return questionBody{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		kernel.WriteBadRequest(w, "题库题目参数无效")
		return questionBody{}, false
	}
	return body, true
}

// reviewBody 是审核入参契约：{ action: "approve"|"reject", reason? }。
type reviewBody struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

func decodeReviewBody(w http.ResponseWriter, r *http.Request) (reviewBody, bool) {
	var body reviewBody
	decoder := json.NewDecoder(io.LimitReader(r.Body, questionBankMaxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		kernel.WriteBadRequest(w, "题库审核参数无效")
		return reviewBody{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		kernel.WriteBadRequest(w, "题库审核参数无效")
		return reviewBody{}, false
	}
	return body, true
}

// readPagination 校验 page/pageSize 查询参数（page ≥1，1 ≤ pageSize ≤ 100）。
func readPagination(w http.ResponseWriter, query url.Values) (int, int, bool) {
	page := 1
	pageSize := defaultListPageSize
	if raw := query.Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			kernel.WriteBadRequest(w, "题库分页参数无效")
			return 0, 0, false
		}
		page = parsed
	}
	if raw := query.Get("pageSize"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxListPageSize {
			kernel.WriteBadRequest(w, "题库分页参数无效")
			return 0, 0, false
		}
		pageSize = parsed
	}
	return page, pageSize, true
}
