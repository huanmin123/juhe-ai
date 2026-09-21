package modelcheckowner

import (
	"context"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
)

// QuestionBankReader 是运行时解析题库题目的最小端口：把配置里的
// customQuestionIds 过滤为仍然存在且 status='approved' 的题目。
// *modelcheckquestionbank.Store 满足该接口；测试用 fake 实现。
type QuestionBankReader interface {
	ListByIDs(ctx context.Context, ids []string, status string) ([]modelcheckquestionbank.Question, error)
}

var _ QuestionBankReader = (*modelcheckquestionbank.Store)(nil)

// resolveQuizQuestions 把配置的题目 id 解析为题库家族入参。返回的第一个
// 值是 QuizRequested：只要配置了题目 id 即为 true（解析后可能为空——全部
// 失效或端口缺失时仍置 true，让家族产出 quiz_questions_unavailable 的
// skipped 项，不进分母也不扣分，计划 §4）。题库读取失败是运行时自身依赖
// 故障，按 fail-closed 返回错误终止本次运行，不静默跳过已配置的质量门。
func resolveQuizQuestions(ctx context.Context, reader QuestionBankReader, configuredIds []string) (bool, []modelcheckprobe.QuizQuestion, error) {
	if len(configuredIds) == 0 {
		return false, nil, nil
	}
	questions := make([]modelcheckprobe.QuizQuestion, 0, len(configuredIds))
	if reader != nil {
		resolved, err := reader.ListByIDs(ctx, configuredIds, modelcheckquestionbank.StatusApproved)
		if err != nil {
			return true, nil, err
		}
		for _, question := range resolved {
			questions = append(questions, modelcheckprobe.QuizQuestion{
				ID:              question.ID,
				Title:           question.Title,
				Question:        question.QuestionText,
				ReferenceAnswer: question.ReferenceAnswer,
				KeyPoints:       question.KeyPoints,
			})
		}
	}
	return true, questions, nil
}

// customQuizItemKey 是题库项的 durable itemKey：Kind 相同的多道题必须以
// questionId 区分；空题库 skipped 项没有 questionId，落固定段 unavailable。
func customQuizItemKey(evidence map[string]any) string {
	questionID, _ := evidence["questionId"].(string)
	if strings.TrimSpace(questionID) == "" {
		return "custom_quiz:unavailable"
	}
	return "custom_quiz:" + questionID
}

// customQuizAwareItemKey 保持既有 ItemKey（=Kind）惯例不变，仅对题库项
// 改用 custom_quiz:<questionId>，避免同 Kind 多题共享同一 itemKey。
func customQuizAwareItemKey(evaluation modelcheckprobe.Evaluation) string {
	if evaluation.Kind == "custom_quiz" {
		return customQuizItemKey(evaluation.Evidence)
	}
	return evaluation.Kind
}

// quizAggregateItem 是 resultSummary.customQuiz.items[] 的元素契约
// （前端 model-checks 类型已按此实现）：verdict 取 passed/failed/
// unavailable，reason 与证据一致（中文）。
type quizAggregateItem struct {
	QuestionID string `json:"questionId"`
	Title      string `json:"title"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
}

// quizAggregate 是 resultSummary.customQuiz 的形状：
// { enabled, score, maxScore:31, deduction, items[] }。
type quizAggregate struct {
	Enabled   bool                `json:"enabled"`
	Score     int                 `json:"score"`
	MaxScore  int                 `json:"maxScore"`
	Deduction int                 `json:"deduction"`
	Items     []quizAggregateItem `json:"items"`
}

// buildCustomQuizSummary 从扫描终态的 evaluations 中聚合题库环节小计。
// 扣分口径与 SummarizeChecks 一致：仅 failed 且非 excludedFromScoring、
// 非 requestFailure 的项按 MaxScore-Score 扣减；第二返回值为 false 表示
// 本次运行未配置题库（家族未执行），调用方不得写入 customQuiz 键。
func buildCustomQuizSummary(evaluations []modelcheckprobe.Evaluation, quizRequested bool) (quizAggregate, bool) {
	if !quizRequested {
		return quizAggregate{}, false
	}
	aggregate := quizAggregate{Enabled: true, MaxScore: modelcheckprobe.QuizMaxPool, Score: modelcheckprobe.QuizMaxPool, Items: []quizAggregateItem{}}
	for _, evaluation := range evaluations {
		if evaluation.Kind != "custom_quiz" {
			continue
		}
		item := quizAggregateItem{Verdict: "unavailable"}
		if evaluation.Evidence != nil {
			item.QuestionID, _ = evaluation.Evidence["questionId"].(string)
			item.Title, _ = evaluation.Evidence["questionTitle"].(string)
			item.Reason, _ = evaluation.Evidence["reason"].(string)
			if verdict, ok := evaluation.Evidence["verdict"].(string); ok {
				switch verdict {
				case "pass":
					item.Verdict = "passed"
				case "fail", "invalid":
					item.Verdict = "failed"
				}
			}
		}
		aggregate.Items = append(aggregate.Items, item)
		if evaluation.Status == "failed" && evaluation.MaxScore > 0 && !quizEvidenceExcludesScoring(evaluation.Evidence) {
			aggregate.Deduction += evaluation.MaxScore - evaluation.Score
		}
	}
	aggregate.Score = modelcheckprobe.QuizMaxPool - aggregate.Deduction
	if aggregate.Score < 0 {
		aggregate.Score = 0
	}
	return aggregate, true
}

// quizEvidenceExcludesScoring 与 summary.go 的请求失败豁免同口径：请求失败
// 或显式 excludedFromScoring 的项不参与扣分。
func quizEvidenceExcludesScoring(evidence map[string]any) bool {
	if evidence == nil {
		return false
	}
	excluded, _ := evidence["excludedFromScoring"].(bool)
	requestFailure, _ := evidence["requestFailure"].(bool)
	return excluded || requestFailure
}
