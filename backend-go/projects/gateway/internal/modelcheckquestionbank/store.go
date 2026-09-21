// Package modelcheckquestionbank 实现模型检测自定义题库域（计划 §1/§2/§5）：
// 全局共享题目的存储与审核流（store.go）、提交查重相似度算法
// （similarity.go）与 HTTP handlers（http.go）。端点挂载与双前缀授权由
// modelcheckowner 的端点分发表在后续 Wave 委托完成，本包只交付可被挂载的
// 导出 API。题库是全局共享数据，不做系统账户（ManagementScope）过滤，
// 仅用 Actor 身份做创建者判定与"approved 全员可见"过滤。
package modelcheckquestionbank

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/idgen"
)

// 题目审核状态，与 schema CHECK 约束一致。
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// 题目字段长度限制（rune 粒度，计划 §1）。
const (
	TitleMaxRunes           = 100
	QuestionTextMaxRunes    = 2000
	ReferenceAnswerMaxRunes = 4000
	KeyPointsMaxCount       = 10
	KeyPointMaxRunes        = 50
	RejectReasonMaxRunes    = 500
)

const questionBankTable = "model_check_question_bank"

// 列表与选项的分页/截断默认值与上限（照仓库 parsePage / account options
// 的 1..100 惯例，选项默认 50）。
const (
	defaultListPageSize = 20
	maxListPageSize     = 100
	defaultOptionsLimit = 50
	maxOptionsLimit     = 100
)

// Actor 是题库操作者身份。题库全局共享，仅用系统账户 ID 做创建者判定
// 与提交来源（created_scope）记录。
type Actor struct {
	SystemAccountID string
}

// Question 是题库题目的完整行（含 title_norm、reviewed_by 等不出 HTTP
// 契约的内部字段；HTTP 契约由 http.go 的 QuestionView 承载）。时间一律
// 为 UTC RFC3339Nano 文本，空字符串表示无值。
type Question struct {
	ID              string
	Title           string
	TitleNorm       string
	QuestionText    string
	ReferenceAnswer string
	KeyPoints       []string
	Status          string
	RejectReason    string
	CreatedBy       string
	CreatedScope    string
	ReviewedBy      string
	ReviewedAt      string
	CreatedAt       string
	UpdatedAt       string
}

// CreateQuestionInput 是提交题目的入参。
type CreateQuestionInput struct {
	Title           string
	QuestionText    string
	ReferenceAnswer string
	// KeyPoints 可选；nil/空 = 无评分要点。
	KeyPoints []string
}

// UpdateQuestionInput 是修改题目的入参（全量替换语义）。
type UpdateQuestionInput struct {
	Title           string
	QuestionText    string
	ReferenceAnswer string
	KeyPoints       []string
}

// QuestionOption 是选择器专用选项（仅 approved 题目）。
type QuestionOption struct {
	ID    string
	Title string
}

// QuestionFilter 是列表过滤条件。Admin=false 时按自助面可见性过滤：
// (status='approved' OR created_by=Actor)。
type QuestionFilter struct {
	Status   string // "" 或 pending/approved/rejected
	Keyword  string // title/question_text LIKE
	Actor    Actor  // 自助面可见性主体
	Admin    bool   // 管理员视角：全量
	Page     int
	PageSize int
}

// Store 是题库的存储实现，pg/SQLite 双方言（照 BusinessQualityManager
// 的 table()+bind() 惯例）。
type Store struct {
	db       *sql.DB
	postgres bool
}

// NewStore 构造题库 Store。postgres=true 时表名带 juhe_business 前缀且
// 占位符改写为 $N；false 为 SQLite。
func NewStore(db *sql.DB, postgres bool) (*Store, error) {
	if db == nil {
		return nil, errors.New("模型检测题库数据库未初始化")
	}
	return &Store{db: db, postgres: postgres}, nil
}

// Create 提交题目：服务端生成 id，status=pending；入库前执行标题查重与
// 内容相似度查重（范围 pending+approved，计划 §2）。
func (s *Store) Create(ctx context.Context, actor Actor, input CreateQuestionInput) (Question, error) {
	actorID := strings.TrimSpace(actor.SystemAccountID)
	if actorID == "" {
		return Question{}, newStatusError(statusUnauthorized, "缺少操作者身份，请先登录")
	}
	title, questionText, referenceAnswer, keyPoints, err := normalizeQuestionFields(
		input.Title, input.QuestionText, input.ReferenceAnswer, input.KeyPoints)
	if err != nil {
		return Question{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Question{}, err
	}
	defer tx.Rollback()
	if err := s.checkDuplicateInTx(ctx, tx, title, questionText, ""); err != nil {
		return Question{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := newQuestionID()
	keyPointsValue, err := keyPointsJSONValue(keyPoints)
	if err != nil {
		return Question{}, err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table(questionBankTable)+` (id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at) VALUES (?,?,?,?,?,?,`+questionStatusPendingLiteral+`,NULL,?,?,NULL,NULL,?,?)`),
		id, title, NormalizeText(title), questionText, referenceAnswer, keyPointsValue, actorID, actorID, now, now)
	if err != nil {
		return Question{}, err
	}
	if err := tx.Commit(); err != nil {
		return Question{}, err
	}
	return s.GetByID(ctx, id)
}

// questionStatusPendingLiteral 复用 status='pending' 的 SQL 字面量。
const questionStatusPendingLiteral = `'pending'`

// GetByID 读取单题；不存在时返回 404 StatusError。
func (s *Store) GetByID(ctx context.Context, id string) (Question, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at FROM `+s.table(questionBankTable)+` WHERE id=?`), strings.TrimSpace(id))
	question, err := scanQuestion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Question{}, newStatusError(statusNotFound, "题目不存在")
	}
	return question, err
}

// List 按过滤条件分页列出题目（created_at DESC, id DESC）。Admin=false
// 时只返回 approved 与本人提交；Status 过滤与可见性 AND 组合。
func (s *Store) List(ctx context.Context, filter QuestionFilter) ([]Question, int, error) {
	if filter.Status != "" && !validStatus(filter.Status) {
		return nil, 0, newStatusError(statusBadRequest, "题库状态筛选无效")
	}
	actorID := strings.TrimSpace(filter.Actor.SystemAccountID)
	if !filter.Admin && actorID == "" {
		return nil, 0, newStatusError(statusUnauthorized, "缺少操作者身份，请先登录")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 5)
	if !filter.Admin {
		conditions = append(conditions, `(status = ? OR created_by = ?)`)
		args = append(args, StatusApproved, actorID)
	}
	if filter.Status != "" {
		conditions = append(conditions, `status = ?`)
		args = append(args, filter.Status)
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		pattern := likePattern(keyword)
		conditions = append(conditions, `(title LIKE ? ESCAPE '\' OR question_text LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	where := " WHERE 1=1"
	for _, condition := range conditions {
		where += " AND " + condition
	}
	var total int
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table(questionBankTable)+where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	listArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at FROM `+s.table(questionBankTable)+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`), listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Question, 0, pageSize)
	for rows.Next() {
		question, err := scanQuestion(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, question)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Update 修改题目。仅 pending/rejected 可改：rejected 改后回到 pending 并
// 清空驳回/审核字段；approved 锁定不可编辑（409）。修改者必须是创建者或
// 管理员；改题面后按 pending+approved 重新查重（排除自身）。
func (s *Store) Update(ctx context.Context, id string, actor Actor, admin bool, input UpdateQuestionInput) (Question, error) {
	actorID := strings.TrimSpace(actor.SystemAccountID)
	if actorID == "" {
		return Question{}, newStatusError(statusUnauthorized, "缺少操作者身份，请先登录")
	}
	title, questionText, referenceAnswer, keyPoints, err := normalizeQuestionFields(
		input.Title, input.QuestionText, input.ReferenceAnswer, input.KeyPoints)
	if err != nil {
		return Question{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Question{}, err
	}
	defer tx.Rollback()
	current, err := s.questionInTx(ctx, tx, strings.TrimSpace(id))
	if err != nil {
		return Question{}, err
	}
	if !admin && current.CreatedBy != actorID {
		return Question{}, newStatusError(statusForbidden, "无权修改该题目")
	}
	if current.Status == StatusApproved {
		return Question{}, newStatusError(statusConflict, "已通过审核的题目不可编辑")
	}
	if err := s.checkDuplicateInTx(ctx, tx, title, questionText, current.ID); err != nil {
		return Question{}, err
	}
	keyPointsValue, err := keyPointsJSONValue(keyPoints)
	if err != nil {
		return Question{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if current.Status == StatusRejected {
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table(questionBankTable)+` SET title=?,title_norm=?,question_text=?,reference_answer=?,key_points_json=?,status=`+questionStatusPendingLiteral+`,reject_reason=NULL,reviewed_by=NULL,reviewed_at=NULL,updated_at=? WHERE id=?`),
			title, NormalizeText(title), questionText, referenceAnswer, keyPointsValue, now, current.ID)
	} else {
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table(questionBankTable)+` SET title=?,title_norm=?,question_text=?,reference_answer=?,key_points_json=?,updated_at=? WHERE id=?`),
			title, NormalizeText(title), questionText, referenceAnswer, keyPointsValue, now, current.ID)
	}
	if err != nil {
		return Question{}, err
	}
	if err := tx.Commit(); err != nil {
		return Question{}, err
	}
	return s.GetByID(ctx, current.ID)
}

// Delete 硬删除题目。管理员可删任意题目；创建者只能删自己的
// pending/rejected 题目（D6），approved 题目仅管理员可删。
func (s *Store) Delete(ctx context.Context, id string, actor Actor, admin bool) error {
	actorID := strings.TrimSpace(actor.SystemAccountID)
	if actorID == "" {
		return newStatusError(statusUnauthorized, "缺少操作者身份，请先登录")
	}
	current, err := s.getForPermissionCheck(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if !admin && current.CreatedBy != actorID {
		return newStatusError(statusForbidden, "无权删除该题目")
	}
	if !admin && current.Status == StatusApproved {
		return newStatusError(statusForbidden, "已通过审核的题目仅管理员可删除")
	}
	res, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.table(questionBankTable)+` WHERE id=?`), current.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return newStatusError(statusNotFound, "题目不存在")
	}
	return nil
}

// Review 审核题目（管理面）：approve 仅允许 pending→approved 并清空驳回
// 理由；reject 允许 pending→rejected 并记录必填理由，admin=true 时额外
// 允许 approved→rejected（驳回下架，同样记录 reject_reason 与
// reviewed_by/at）；rejected→approve、approved→approve 与一切重复审核被
// 状态 CAS 拒绝，影响行数为 0 时返回 409"题目已被审核或不存在"。
// pending 路径不要求 admin（与历史语义一致，自助面防御由 handler 层保证）。
func (s *Store) Review(ctx context.Context, id string, reviewer Actor, admin bool, action string, reason string) (Question, error) {
	reviewerID := strings.TrimSpace(reviewer.SystemAccountID)
	if reviewerID == "" {
		return Question{}, newStatusError(statusUnauthorized, "缺少操作者身份，请先登录")
	}
	action = strings.TrimSpace(action)
	if action != "approve" && action != "reject" {
		return Question{}, newStatusError(statusBadRequest, "审核动作无效")
	}
	reason = strings.TrimSpace(reason)
	if action == "reject" {
		if reason == "" {
			return Question{}, newStatusError(statusBadRequest, "驳回理由不能为空")
		}
		if len([]rune(reason)) > RejectReasonMaxRunes {
			return Question{}, newStatusError(statusBadRequest, "驳回理由不能超过 %d 字", RejectReasonMaxRunes)
		}
	} else {
		reason = ""
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	questionID := strings.TrimSpace(id)
	var res sql.Result
	var err error
	if action == "approve" {
		res, err = s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table(questionBankTable)+` SET status='approved',reject_reason=NULL,reviewed_by=?,reviewed_at=?,updated_at=? WHERE id=? AND status=`+questionStatusPendingLiteral),
			reviewerID, now, now, questionID)
	} else if admin {
		res, err = s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table(questionBankTable)+` SET status='rejected',reject_reason=?,reviewed_by=?,reviewed_at=?,updated_at=? WHERE id=? AND status IN (`+questionStatusPendingLiteral+`,'approved')`),
			reason, reviewerID, now, now, questionID)
	} else {
		res, err = s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table(questionBankTable)+` SET status='rejected',reject_reason=?,reviewed_by=?,reviewed_at=?,updated_at=? WHERE id=? AND status=`+questionStatusPendingLiteral),
			reason, reviewerID, now, now, questionID)
	}
	if err != nil {
		return Question{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Question{}, newStatusError(statusConflict, "题目已被审核或不存在")
	}
	return s.GetByID(ctx, questionID)
}

// ListByIDs 按 id 批量取题（检测配置运行时解析用），可选 status 过滤
// （通常传 approved）；失效 id 不出现在结果中。空入参返回空切片。
func (s *Store) ListByIDs(ctx context.Context, ids []string, status string) ([]Question, error) {
	cleaned := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
	}
	if len(cleaned) == 0 {
		return []Question{}, nil
	}
	if status != "" && !validStatus(status) {
		return nil, newStatusError(statusBadRequest, "题库状态筛选无效")
	}
	query := `SELECT id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at FROM ` + s.table(questionBankTable) + ` WHERE id IN (` + placeholders(len(cleaned)) + `)`
	args := make([]any, 0, len(cleaned)+1)
	for _, id := range cleaned {
		args = append(args, id)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Question, 0, len(cleaned))
	for rows.Next() {
		question, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, question)
	}
	return items, rows.Err()
}

// ListOptions 是选择器专用选项列表：仅 approved，按标题关键字过滤，
// created_at DESC 截断 limit 条。
func (s *Store) ListOptions(ctx context.Context, keyword string, limit int) ([]QuestionOption, error) {
	if limit < 1 {
		limit = defaultOptionsLimit
	}
	if limit > maxOptionsLimit {
		limit = maxOptionsLimit
	}
	query := `SELECT id,title FROM ` + s.table(questionBankTable) + ` WHERE status='approved'`
	args := make([]any, 0, 2)
	if kw := strings.TrimSpace(keyword); kw != "" {
		query += ` AND title LIKE ? ESCAPE '\'`
		args = append(args, likePattern(kw))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := make([]QuestionOption, 0)
	for rows.Next() {
		var option QuestionOption
		if err := rows.Scan(&option.ID, &option.Title); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

// checkDuplicateInTx 在事务内执行提交查重：title_norm 与 pending+approved
// 题目精确相等 → 409"标题与现有题目重复"；题面与 pending+approved 逐一
// Jaccard，最高分 ≥ SimilarityRejectThreshold → 409 提示最相似题目标题。
// excludeID 供 Update 排除自身。
func (s *Store) checkDuplicateInTx(ctx context.Context, tx *sql.Tx, title, questionText, excludeID string) error {
	query := `SELECT id,title,title_norm,question_text FROM ` + s.table(questionBankTable) + ` WHERE status IN (` + questionStatusPendingLiteral + `, 'approved')`
	args := make([]any, 0, 1)
	if excludeID != "" {
		query += ` AND id <> ?`
		args = append(args, excludeID)
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return err
	}
	titleNorm := NormalizeText(title)
	candidates := make([]StoredQuestionText, 0)
	defer rows.Close()
	for rows.Next() {
		var id, existingTitle, existingTitleNorm, existingText string
		if err := rows.Scan(&id, &existingTitle, &existingTitleNorm, &existingText); err != nil {
			return err
		}
		if existingTitleNorm == titleNorm {
			return newStatusError(statusConflict, "标题与现有题目重复")
		}
		candidates = append(candidates, StoredQuestionText{ID: id, Title: existingTitle, Text: existingText})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, bestTitle, _, found := FindMostSimilar(questionText, candidates); found {
		return newStatusError(statusConflict, "题目内容与现有题目《%s》过于相似", bestTitle)
	}
	return nil
}

// getForPermissionCheck 读取删除前的状态与创建者；不存在返回 404。
func (s *Store) getForPermissionCheck(ctx context.Context, id string) (Question, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at FROM `+s.table(questionBankTable)+` WHERE id=?`), id)
	question, err := scanQuestion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Question{}, newStatusError(statusNotFound, "题目不存在")
	}
	return question, err
}

// questionInTx 是事务内的单题读取；不存在返回 404。
func (s *Store) questionInTx(ctx context.Context, tx *sql.Tx, id string) (Question, error) {
	row := tx.QueryRowContext(ctx, s.bind(`SELECT id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,reviewed_by,reviewed_at,created_at,updated_at FROM `+s.table(questionBankTable)+` WHERE id=?`), id)
	question, err := scanQuestion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Question{}, newStatusError(statusNotFound, "题目不存在")
	}
	return question, err
}

func (s *Store) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

// bind 把 SQL 中的 ? 占位符改写为 PostgreSQL 的 $N（照
// BusinessQualityManager.bind 惯例）。
func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	i := 0
	for _, r := range query {
		if r == '?' {
			i++
			b.WriteString("$")
			b.WriteString(strconv.Itoa(i))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// placeholders 生成 n 个逗号分隔的 ? 占位符。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func validStatus(status string) bool {
	return status == StatusPending || status == StatusApproved || status == StatusRejected
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultListPageSize
	}
	if pageSize > maxListPageSize {
		pageSize = maxListPageSize
	}
	return page, pageSize
}

// likePattern 转义 LIKE 通配符后构造 %keyword% 模式，配
// `ESCAPE '\'`（PG 与 SQLite 语义一致）。
func likePattern(keyword string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
	return "%" + escaped + "%"
}

// normalizeQuestionFields 校验并归一化题目字段（中文错误，rune 粒度，
// 计划 §1 的长度契约）。
func normalizeQuestionFields(title, questionText, referenceAnswer string, keyPoints []string) (string, string, string, []string, error) {
	title = strings.TrimSpace(title)
	questionText = strings.TrimSpace(questionText)
	referenceAnswer = strings.TrimSpace(referenceAnswer)
	if title == "" {
		return "", "", "", nil, newStatusError(statusBadRequest, "题目标题不能为空")
	}
	if len([]rune(title)) > TitleMaxRunes {
		return "", "", "", nil, newStatusError(statusBadRequest, "题目标题不能超过 %d 字", TitleMaxRunes)
	}
	if questionText == "" {
		return "", "", "", nil, newStatusError(statusBadRequest, "题目内容不能为空")
	}
	if len([]rune(questionText)) > QuestionTextMaxRunes {
		return "", "", "", nil, newStatusError(statusBadRequest, "题目内容不能超过 %d 字", QuestionTextMaxRunes)
	}
	if referenceAnswer == "" {
		return "", "", "", nil, newStatusError(statusBadRequest, "参考答案不能为空")
	}
	if len([]rune(referenceAnswer)) > ReferenceAnswerMaxRunes {
		return "", "", "", nil, newStatusError(statusBadRequest, "参考答案不能超过 %d 字", ReferenceAnswerMaxRunes)
	}
	if len(keyPoints) > KeyPointsMaxCount {
		return "", "", "", nil, newStatusError(statusBadRequest, "评分要点最多 %d 条", KeyPointsMaxCount)
	}
	normalizedPoints := make([]string, 0, len(keyPoints))
	for _, point := range keyPoints {
		point = strings.TrimSpace(point)
		if point == "" {
			return "", "", "", nil, newStatusError(statusBadRequest, "评分要点不能为空")
		}
		if len([]rune(point)) > KeyPointMaxRunes {
			return "", "", "", nil, newStatusError(statusBadRequest, "单条评分要点不能超过 %d 字", KeyPointMaxRunes)
		}
		normalizedPoints = append(normalizedPoints, point)
	}
	if len(normalizedPoints) == 0 {
		normalizedPoints = nil
	}
	return title, questionText, referenceAnswer, normalizedPoints, nil
}

// keyPointsJSONValue 把要点编码为 JSON 数组文本；空切片编码为 SQL NULL。
func keyPointsJSONValue(keyPoints []string) (any, error) {
	if len(keyPoints) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(keyPoints)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

// scanQuestion 扫描完整题目行；key_points_json/reject_reason/reviewed_by/
// reviewed_at 为 NULL 时归零值。
func scanQuestion(row interface{ Scan(...any) error }) (Question, error) {
	var question Question
	var keyPointsJSON, rejectReason, reviewedBy, reviewedAt sql.NullString
	err := row.Scan(&question.ID, &question.Title, &question.TitleNorm, &question.QuestionText, &question.ReferenceAnswer, &keyPointsJSON, &question.Status, &rejectReason, &question.CreatedBy, &question.CreatedScope, &reviewedBy, &reviewedAt, &question.CreatedAt, &question.UpdatedAt)
	if err != nil {
		return Question{}, err
	}
	question.KeyPoints = []string{}
	if keyPointsJSON.Valid && strings.TrimSpace(keyPointsJSON.String) != "" {
		var points []string
		if err := json.Unmarshal([]byte(keyPointsJSON.String), &points); err != nil {
			return Question{}, err
		}
		if points != nil {
			question.KeyPoints = points
		}
	}
	if rejectReason.Valid {
		question.RejectReason = rejectReason.String
	}
	if reviewedBy.Valid {
		question.ReviewedBy = reviewedBy.String
	}
	if reviewedAt.Valid {
		question.ReviewedAt = reviewedAt.String
	}
	return question, nil
}

// newQuestionID 沿用仓库 id 惯例：<prefix>-<24hex>（shared/platform/idgen）。
func newQuestionID() string {
	return "mcq-" + idgen.RandomHex(12)
}
