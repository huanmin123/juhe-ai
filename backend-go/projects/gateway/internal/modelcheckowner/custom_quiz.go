package modelcheckowner

import (
	"context"
	"fmt"
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
// unavailable，reason 与证据一致（中文）。reason 组装前提：纯请求失败
// 证据（quizRequestFailureEvaluation 生成）没有 reason 键，reason 由
// error/重试证据现场翻译为"请求未完成：…"，不再留空导致前端只能显示
// "-"；家族终端包装（terminalFamilyEvaluation）会给已完成题的证据追加
// requestFailure 等标志但保留其 reason/verdict，reason 非空时永远优先
// 于请求失败组装，不产生 verdict=passed 而 reason="请求未完成"的矛盾。
type quizAggregateItem struct {
	QuestionID string `json:"questionId"`
	Title      string `json:"title"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
}

// quizAggregate 是 resultSummary.customQuiz 的形状：
// { enabled, executed, score, maxScore:31, deduction, items[] }。
type quizAggregate struct {
	Enabled bool `json:"enabled"`
	// Executed 表示本次运行是否实际执行了作答题（存在任何拿到判定的题）。
	// 未执行（题目全部不可用或全部请求失败转 skipped）时 Score 必须为 0，
	// 不得把"没做"展示成满分；MaxScore 保留 31 作为展示分母。
	Executed  bool                `json:"executed"`
	Score     int                 `json:"score"`
	MaxScore  int                 `json:"maxScore"`
	Deduction int                 `json:"deduction"`
	Items     []quizAggregateItem `json:"items"`
}

// buildCustomQuizSummary 从扫描终态的 evaluations 中聚合题库环节小计。
// 扣分口径与 SummarizeChecks 一致：仅 failed 且非 excludedFromScoring、
// 非 requestFailure 的项按 MaxScore-Score 扣减；第二返回值为 false 表示
// 本次运行未配置题库（家族未执行），调用方不得写入 customQuiz 键。
// 未执行任何作答题（无 verdict）时 Score 固定为 0，有执行题时 Score=
// QuizMaxPool-Deduction（下限 0）。
func buildCustomQuizSummary(evaluations []modelcheckprobe.Evaluation, quizRequested bool) (quizAggregate, bool) {
	if !quizRequested {
		return quizAggregate{}, false
	}
	aggregate := quizAggregate{Enabled: true, MaxScore: modelcheckprobe.QuizMaxPool, Items: []quizAggregateItem{}}
	for _, evaluation := range evaluations {
		if evaluation.Kind != "custom_quiz" {
			continue
		}
		item := quizAggregateItem{Verdict: "unavailable"}
		if evaluation.Evidence != nil {
			item.QuestionID, _ = evaluation.Evidence["questionId"].(string)
			item.Title, _ = evaluation.Evidence["questionTitle"].(string)
			reason, _ := evaluation.Evidence["reason"].(string)
			if requestFailure, _ := evaluation.Evidence["requestFailure"].(bool); requestFailure && strings.TrimSpace(reason) == "" {
				// 纯请求失败形态（quizRequestFailureEvaluation）没有 reason 键，
				// 由 error/重试证据翻译中文 reason；verdict 键在该形态下不
				// 存在，下方映射保持 unavailable，不影响扣分豁免口径。
				item.Reason = quizRequestFailureReason(evaluation.Evidence)
			} else {
				// reason 非空（trim 后）永远优先：家族终端包装
				// （terminalFamilyEvaluation）会给已判完题的证据追加
				// requestFailure 等标志但保留其原 reason/verdict，套用
				// "请求未完成"组装会与 verdict 仍为 passed/failed 自相矛盾。
				item.Reason = reason
			}
			if verdict, ok := evaluation.Evidence["verdict"].(string); ok {
				switch verdict {
				case "pass":
					item.Verdict = "passed"
				case "fail", "invalid":
					item.Verdict = "failed"
				}
			}
		}
		// 拿到 pass/fail/invalid 判定的项即实际执行（作答且完成对比判定）；
		// skipped（题目不可用、请求失败）与异常 verdict 不算执行。
		if item.Verdict == "passed" || item.Verdict == "failed" {
			aggregate.Executed = true
		}
		aggregate.Items = append(aggregate.Items, item)
		if evaluation.Status == "failed" && evaluation.MaxScore > 0 && !quizEvidenceExcludesScoring(evaluation.Evidence) {
			aggregate.Deduction += evaluation.MaxScore - evaluation.Score
		}
	}
	if aggregate.Executed {
		aggregate.Score = modelcheckprobe.QuizMaxPool - aggregate.Deduction
		if aggregate.Score < 0 {
			aggregate.Score = 0
		}
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

// quizRequestFailureReason 把请求失败形态的证据组装为中文 reason：error 先
// 经 quizRequestFailurePhrase 翻译为短语，无短语时固定"请求未完成"；有重试
// 证据（retryMaxAttempts>0）时追加"（尝试 N/M 次）"，N=已试次数+1（最后一次
// 失败也计入），M=配置的最大尝试次数。
func quizRequestFailureReason(evidence map[string]any) string {
	reason := "请求未完成"
	if phrase := quizRequestFailurePhrase(evidence); phrase != "" {
		reason = "请求未完成：" + phrase
	}
	if maxAttempts := quizEvidenceNumber(evidence, "retryMaxAttempts"); maxAttempts > 0 {
		attempts := quizEvidenceNumber(evidence, "retryAttemptCount") + 1
		reason += fmt.Sprintf("（尝试 %d/%d 次）", attempts, maxAttempts)
	}
	return reason
}

// quizRequestFailurePhrase 把 modelcheckprobe 生成的 error 原文（probe.go 的
// "J3b ..." 分类）翻译为用户可读中文短语；error 缺失或为空返回空短语，未知
// 非空 error 保留原文，不吞掉上游信息。
func quizRequestFailurePhrase(evidence map[string]any) string {
	message, _ := evidence["error"].(string)
	switch message {
	case "":
		return ""
	case "J3b probe timed out":
		return "上游超时"
	case "J3b probe canceled":
		return "探针已取消"
	case "J3b upstream response read failed":
		return "上游响应读取失败"
	case "J3b upstream response exceeded limit":
		return "上游响应超限"
	case "J3b OAuth Codex response stream incomplete":
		return "OAuth Codex 响应流未完成"
	}
	if rest, ok := strings.CutPrefix(message, "J3b upstream request failed: "); ok {
		return "上游请求失败：" + rest
	}
	if rest, ok := strings.CutPrefix(message, "J3b upstream returned HTTP "); ok {
		return "上游返回 HTTP " + rest
	}
	return message
}

// quizEvidenceNumber 安全读取证据中的数字：withRetryEvidence 运行时直写 int，
// JSON 还原后为 float64，两者都覆盖；缺失或非数字返回 0。
func quizEvidenceNumber(evidence map[string]any, key string) int {
	switch value := evidence[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}
