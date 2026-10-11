// quizdiagnosis.go 实现题库提交门禁的单题同步诊断（检测题库提交门禁
// 设计）：与 RunQuizFamily 同构的两跳链路（隔离上下文作答 + 独立裁判），
// 但裁判指令换成诊断契约——除 pass/fail 结论外还产出题面对齐判定、逐要点
// 覆盖结论与简短理由。请求失败/超时沿用家族终态语义，转为
// verdict=request_failed 的正常结果返回，不作为 error 上抛。
package modelcheckprobe

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// quizDiagnosisInstructions 是提交门禁单题诊断的裁判指令。与题库裁判
// （quizGradingInstructions）同经 applyQuizGradingInstructions 注入协议
// 槽位，但要求输出 questionAlignment、keyPointResults 与 reason 的完整
// JSON 契约。
const quizDiagnosisInstructions = `You are diagnosing one model answer against a reference answer for a question bank submission gate. Respond with ONLY a JSON object {"verdict":"pass" or "fail","questionAlignment":"aligned" or "misaligned","keyPointResults":[{"point":"<the key point text>","met":true or false,"note":"<short note>"}],"reason":"<short reason>"}. pass means the answer reaches the same conclusion/result as the reference answer (wording may differ). questionAlignment is "aligned" when the question asks about the same subject the reference answer covers, otherwise "misaligned". keyPointResults must contain one entry per listed key point, and met is true only when the answer covers that point. Output JSON only.`

// quizDiagnosisGraderNoteLimit 截断裁判 reason（留证字段不无限增长）。
const quizDiagnosisGraderNoteLimit = 200

// QuizKeyPointResult 是诊断裁判输出的单条要点覆盖结论。
type QuizKeyPointResult struct {
	Point string `json:"point"`
	Met   bool   `json:"met"`
	Note  string `json:"note,omitempty"`
}

// QuizDiagnosis 是提交门禁单题诊断的聚合结果。Verdict 取值
// pass|fail|invalid|request_failed（invalid = 200 但 JSON 非法或结论不可
// 判；request_failed = 重试阶梯耗尽/超时的家族终态）。
type QuizDiagnosis struct {
	Verdict         string
	Alignment       string
	KeyPointResults []QuizKeyPointResult
	AnswerExcerpt   string
	GraderNote      string
	HTTPStatus      int
	LatencyMs       int64
	ErrorMessage    string
}

// RunQuizDiagnosis 对单题套件执行两跳诊断：作答跳与 RunQuizFamily 同参
// （tunedBasic + quizMaxOutputTokens，ItemKey "custom_quiz"），裁判跳复用
// 裁判正文结构但注入诊断指令。input.QuizQuestions 必须恰为一题。返回
// error 仅表示请求构建/执行链本身失败；上游请求失败与超时按家族终态转为
// Verdict=request_failed 的正常结果。
func RunQuizDiagnosis(ctx context.Context, input Suite, timeout time.Duration) (QuizDiagnosis, error) {
	if len(input.QuizQuestions) != 1 {
		return QuizDiagnosis{}, errors.New("J3b quiz diagnosis requires exactly one question")
	}
	question := input.QuizQuestions[0]
	_, stream, err := input.probeMode()
	if err != nil {
		return QuizDiagnosis{}, err
	}
	upstreamProtocol := input.UpstreamProtocol
	if upstreamProtocol == "" {
		upstreamProtocol = input.Protocol
	}
	upstreamMode := strings.TrimSpace(input.UpstreamEndpointMode)
	if upstreamMode == "" {
		upstreamMode = modelcheckprofile.EndpointModeForProtocol(upstreamProtocol, stream)
	}
	run, familyTerminal := input.familyRunner(timeout)
	var latencyMs int64
	answerRequest, answerErr := input.tunedBasic(input.Model, question.Question, upstreamMode, stream, quizMaxOutputTokens, 0)
	if answerErr != nil {
		return QuizDiagnosis{}, answerErr
	}
	answerRequest.ItemKey = "custom_quiz"
	answerResult, answerExecuteErr := run(ctx, answerRequest)
	if answerExecuteErr != nil {
		return QuizDiagnosis{}, answerExecuteErr
	}
	latencyMs += answerResult.Duration.Milliseconds()
	if familyTerminal() {
		return quizDiagnosisRequestFailed(answerResult, latencyMs), nil
	}
	gradeRequest, gradeErr := input.quizDiagnosisRequest(question, answerResult.Output, upstreamProtocol, upstreamMode, stream)
	if gradeErr != nil {
		return QuizDiagnosis{}, gradeErr
	}
	gradeRequest.ItemKey = "custom_quiz"
	gradeResult, gradeExecuteErr := run(ctx, gradeRequest)
	if gradeExecuteErr != nil {
		return QuizDiagnosis{}, gradeExecuteErr
	}
	latencyMs += gradeResult.Duration.Milliseconds()
	if familyTerminal() {
		return quizDiagnosisRequestFailed(gradeResult, latencyMs), nil
	}
	return parseQuizDiagnosis(question, answerResult.Output, gradeResult, latencyMs), nil
}

// quizDiagnosisRequest 与题库裁判跳同构（quizGradingRequest 共用正文拼装
// quizGradingPrompt），仅把协议槽位指令换成诊断契约。
func (s Suite) quizDiagnosisRequest(question QuizQuestion, answer string, protocol modelcheckprofile.Protocol, mode string, stream bool) (Request, error) {
	request, err := s.tunedBasic(s.Model, quizGradingPrompt(question, answer), mode, stream, quizMaxOutputTokens, 0)
	if err != nil {
		return Request{}, err
	}
	return applyQuizGradingInstructions(request, protocol, quizDiagnosisInstructions)
}

// parseQuizDiagnosis 解析 200 诊断裁判输出。顶层 verdict 与 parseQuizVerdict
// 同口径：pass/fail 为有效判定，JSON 非法或其他取值为 invalid；
// questionAlignment/keyPointResults/reason 可选，缺失分别归 unknown/空/空。
func parseQuizDiagnosis(question QuizQuestion, answer string, grade Result, latencyMs int64) QuizDiagnosis {
	verdict, reason, _ := parseQuizVerdict(grade.Output)
	diagnosis := QuizDiagnosis{
		Verdict:       verdict,
		AnswerExcerpt: answerExcerpt(answer),
		GraderNote:    truncateRunes(reason, quizDiagnosisGraderNoteLimit),
		HTTPStatus:    grade.HTTPStatus,
		LatencyMs:     latencyMs,
	}
	if value := parseJSONObject(grade.Output); value != nil {
		diagnosis.Alignment = quizDiagnosisAlignment(value["questionAlignment"])
		diagnosis.KeyPointResults = parseQuizKeyPointResults(value["keyPointResults"])
	}
	if diagnosis.Alignment == "" {
		diagnosis.Alignment = "unknown"
	}
	return diagnosis
}

// quizDiagnosisAlignment 归一化 questionAlignment 取值；缺失或非法归
// unknown（空串在上层同样归 unknown）。
func quizDiagnosisAlignment(value any) string {
	text, _ := value.(string)
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "aligned":
		return "aligned"
	case "misaligned":
		return "misaligned"
	default:
		return "unknown"
	}
}

// parseQuizKeyPointResults 解析可选的 keyPointResults 数组；缺失或元素
// 非对象时返回 nil（要点缺失容错：不虚构覆盖结论）。
func parseQuizKeyPointResults(value any) []QuizKeyPointResult {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	results := make([]QuizKeyPointResult, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		point, _ := object["point"].(string)
		note, _ := object["note"].(string)
		met, _ := object["met"].(bool)
		results = append(results, QuizKeyPointResult{Point: strings.TrimSpace(point), Met: met, Note: strings.TrimSpace(note)})
	}
	if len(results) == 0 {
		return nil
	}
	return results
}

// quizDiagnosisRequestFailed 把家族终态（重试阶梯耗尽/超时）转成正常结果：
// verdict=request_failed，alignment 归 unknown（响应契约枚举无空串，与
// parseQuizDiagnosis 的缺失归一口径一致），HTTPStatus/ErrorMessage（截断）
// 留证，不返回 error。
func quizDiagnosisRequestFailed(result Result, latencyMs int64) QuizDiagnosis {
	failure := QuizDiagnosis{Verdict: "request_failed", Alignment: "unknown", HTTPStatus: result.HTTPStatus, LatencyMs: latencyMs}
	if strings.TrimSpace(result.ErrorMessage) != "" {
		failure.ErrorMessage = truncateRunes(result.ErrorMessage, answerExcerptLimit)
	}
	return failure
}

// truncateRunes 先去首尾空白，再按 rune 截断到上限（诊断 GraderNote 与
// 错误文案留证共用）。
func truncateRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return string(runes)
}
