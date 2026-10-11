// testgate.go 承载题库提交门禁（检测题库提交门禁设计）：提交前测试记录
// （model_check_question_tests）的存储、题目草稿内容哈希与 TestRunner 端口。
// 门禁语义：创建（POST）与修改重提交（PATCH）必须携带 testId，指向一条
// verdict='pass'、内容哈希一致、24 小时内、本人创建的测试记录；同步测试
// 端点 ServeTest 由 owner 分发表按双前缀委托挂载。
package modelcheckquestionbank

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/idgen"
)

// questionTestsTable 与题库同库（juhe_business）。
const questionTestsTable = "model_check_question_tests"

// 测试判定取值，与 schema CHECK 约束一致。request_failed 是"上游请求失败"
// 的正常结果（重试阶梯耗尽/超时），不是执行器 error。
const (
	TestVerdictPass          = "pass"
	TestVerdictFail          = "fail"
	TestVerdictInvalid       = "invalid"
	TestVerdictRequestFailed = "request_failed"
)

// 题面对齐取值（裁判输出 questionAlignment 的归一化结果；缺失归 unknown，
// 落库允许空串 = schema 默认值，仅历史行出现）。
const (
	TestAlignmentAligned    = "aligned"
	TestAlignmentMisaligned = "misaligned"
	TestAlignmentUnknown    = "unknown"
)

// testValidityWindow 是测试记录作为提交凭据的有效窗口。
const testValidityWindow = 24 * time.Hour

// TestRecord 是一条提交前测试记录的完整行。HTTPStatus 为 0 表示无 HTTP
// 状态（直连失败），落库写 NULL；DiagnosisJSON 空串落 NULL。
type TestRecord struct {
	ID            string
	CreatedBy     string
	AccountID     string
	AccountName   string
	Model         string
	DraftHash     string
	Verdict       string
	Alignment     string
	DiagnosisJSON string
	HTTPStatus    int
	LatencyMs     int64
	CreatedAt     string
}

// TestQuestionInput 是测试执行器入参：以 SystemAccountID 身份解析
// AccountID 指向的 AI 账户并对 Model 发起单题两跳诊断。
type TestQuestionInput struct {
	SystemAccountID string
	AccountID       string
	Model           string
	QuestionText    string
	ReferenceAnswer string
	KeyPoints       []string
}

// TestKeyPointResult 是单条评分要点的覆盖结论。
type TestKeyPointResult struct {
	Point string `json:"point"`
	Met   bool   `json:"met"`
	Note  string `json:"note,omitempty"`
}

// TestQuestionResult 是一次测试的完整诊断结果。error 仅表示"无法执行"
// （账户解析失败等），上游请求失败是 Verdict=request_failed 的正常结果。
// AccountName 由 runner 从解析目标回填，供落库与响应透出被测账户名。
type TestQuestionResult struct {
	Verdict         string               `json:"verdict"`           // pass|fail|invalid|request_failed
	Alignment       string               `json:"questionAlignment"` // aligned|misaligned|unknown
	KeyPointResults []TestKeyPointResult `json:"keyPointResults"`
	AnswerExcerpt   string               `json:"answerExcerpt,omitempty"`
	GraderNote      string               `json:"graderNote,omitempty"`
	VerdictReason   string               `json:"verdictReason,omitempty"`
	HTTPStatus      int                  `json:"httpStatus,omitempty"`
	LatencyMs       int64                `json:"latencyMs"`
	ErrorMessage    string               `json:"error,omitempty"`
	AccountName     string               `json:"accountName,omitempty"`
}

// TestQuestionOutcome 是 ServeTest 的响应契约：测试记录 id + 完整结果 +
// 被测账户 id/模型（回填自测试记录）+ 被测账户名 + 测试时间。
type TestQuestionOutcome struct {
	TestID string `json:"testId"`
	TestQuestionResult
	AccountID   string    `json:"accountId"`
	AccountName string    `json:"accountName"`
	Model       string    `json:"model"`
	TestedAt    time.Time `json:"testedAt"`
}

// TestRunner 是题库包消费侧的测试执行器端口（实现在 modelcheckowner：
// BusinessTargetSource 解析账户 + probe RunQuizDiagnosis 单题诊断）。
// consumer-side 接口避免 owner→questionbank 的反向依赖（owner 已 import
// questionbank）。
type TestRunner interface {
	TestQuestion(ctx context.Context, input TestQuestionInput) (TestQuestionResult, error)
}

// DraftHash 计算题目草稿内容哈希（提交门禁的"内容一致"判定依据）：
// sha256(questionText + "\n" + referenceAnswer + "\n" + join(keyPoints, "\n"))
// 的 hex。keyPoints 必须传归一化后的原序要点（normalizeQuestionFields 输出）。
func DraftHash(questionText, referenceAnswer string, keyPoints []string) string {
	hash := sha256.Sum256([]byte(questionText + "\n" + referenceAnswer + "\n" + strings.Join(keyPoints, "\n")))
	return hex.EncodeToString(hash[:])
}

// InsertTestRecord 落一条测试记录。verdict 不限 pass：失败/无效/请求失败
// 结果同样留档，供用户查看诊断与 FindUsableTest 的 not-passed 拒绝语义。
func (s *Store) InsertTestRecord(ctx context.Context, record TestRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		return errors.New("测试记录缺少 id")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table(questionTestsTable)+` (id,created_by,account_id,account_name,model,draft_hash,verdict,alignment,diagnosis_json,http_status,latency_ms,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
		record.ID, record.CreatedBy, record.AccountID, record.AccountName, record.Model, record.DraftHash,
		record.Verdict, record.Alignment, nullTextValue(record.DiagnosisJSON), nullIntValue(record.HTTPStatus),
		record.LatencyMs, record.CreatedAt)
	return err
}

// FindUsableTest 按"本人创建"读取测试记录并执行门禁判定：verdict 非 pass、
// draft_hash 不一致、created_at 距 now 超过 24 小时分别返回可区分的哨兵
// 错误（ErrTestNotPassed/ErrTestDraftMismatch/ErrTestExpired），不存在或
// 不属于本人返回 ErrTestNotFound，供 HTTP 层映射 400 中文文案。
func (s *Store) FindUsableTest(ctx context.Context, actor Actor, testID, draftHash string, now time.Time) (TestRecord, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT id,created_by,account_id,account_name,model,draft_hash,verdict,alignment,COALESCE(diagnosis_json,''),COALESCE(http_status,0),latency_ms,created_at FROM `+s.table(questionTestsTable)+` WHERE id=? AND created_by=?`),
		strings.TrimSpace(testID), strings.TrimSpace(actor.SystemAccountID))
	record, err := scanTestRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TestRecord{}, ErrTestNotFound
	}
	if err != nil {
		return TestRecord{}, err
	}
	if record.Verdict != TestVerdictPass {
		return TestRecord{}, ErrTestNotPassed
	}
	if record.DraftHash != draftHash {
		return TestRecord{}, ErrTestDraftMismatch
	}
	createdAt, err := time.Parse(time.RFC3339Nano, record.CreatedAt)
	if err != nil {
		return TestRecord{}, fmt.Errorf("解析测试记录时间 %q: %w", record.CreatedAt, err)
	}
	if now.Sub(createdAt) > testValidityWindow {
		return TestRecord{}, ErrTestExpired
	}
	return record, nil
}

// requireSubmissionTestID 校验 testId 必填（门禁第一道：请求缺凭据时在
// 触达数据库前即按 400 语义拒绝）。
func requireSubmissionTestID(testID string) (string, error) {
	testID = strings.TrimSpace(testID)
	if testID == "" {
		return "", newStatusError(statusBadRequest, "缺少测试记录 testId，请先完成提交前测试")
	}
	return testID, nil
}

// lastTestJSONValue 把通过的测试记录压缩为题目视图透出的 lastTest 摘要
// JSON（落库到 model_check_question_bank.last_test_json）。
func lastTestJSONValue(record TestRecord) (any, error) {
	encoded, err := json.Marshal(QuestionLastTestView{
		TestID: record.ID, AccountID: record.AccountID, AccountName: record.AccountName,
		Model: record.Model, PassedAt: record.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

const testRecordSelectColumns = "id,created_by,account_id,account_name,model,draft_hash,verdict,alignment,COALESCE(diagnosis_json,''),COALESCE(http_status,0),latency_ms,created_at"

func scanTestRecord(row interface{ Scan(...any) error }) (TestRecord, error) {
	var record TestRecord
	err := row.Scan(&record.ID, &record.CreatedBy, &record.AccountID, &record.AccountName, &record.Model,
		&record.DraftHash, &record.Verdict, &record.Alignment, &record.DiagnosisJSON, &record.HTTPStatus,
		&record.LatencyMs, &record.CreatedAt)
	return record, err
}

// nullTextValue / nullIntValue 把空串与零值映射为 SQL NULL
// （diagnosis_json、http_status 的可空契约）。
func nullTextValue(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullIntValue(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

// newTestRecordID 沿用仓库 id 惯例：<prefix>-<24hex>。
func newTestRecordID() string {
	return "mcqt-" + idgen.RandomHex(12)
}
