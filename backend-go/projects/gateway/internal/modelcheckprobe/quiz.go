package modelcheckprobe

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// QuizMaxPool is the fixed deduction pool shared by every custom quiz family.
// Each question deducts its own weight from the normalized suite score, so a
// single wrong answer with one configured question costs the full pool.
const QuizMaxPool = 31

// quizMaxOutputTokens is the per-hop output budget for both quiz requests.
// Answering and grading both need far more room than the 16-token probes.
const quizMaxOutputTokens = 1024

// answerExcerptLimit bounds the durable answer excerpt. It stays below the
// storage sanitize cap so the excerpt is never truncated again downstream.
const answerExcerptLimit = 400

// quizGradingInstructions is the fixed grading prompt for the second hop. It
// is sent in a brand-new isolated context that never carries the answer hop's
// message structure.
const quizGradingInstructions = "You are grading one answer against a reference answer. Respond with ONLY a JSON object {\"verdict\":\"pass\" or \"fail\",\"reason\":\"<short reason>\"}. pass means the answer matches the reference (same conclusion/result; wording may differ)."

// QuizQuestion is one owner-resolved approved bank question. Reference
// material flows only into the grading request and never into evidence.
type QuizQuestion struct {
	ID              string
	Title           string
	Question        string
	ReferenceAnswer string
	KeyPoints       []string
}

// quizWeights splits the fixed 31-point pool across n questions: floor(31/n)
// each, with the remainder distributed to the first questions. n=1 -> [31],
// n=2 -> [16,15], n=3 -> [11,10,10].
func quizWeights(n int) []int {
	weights := make([]int, n)
	base := QuizMaxPool / n
	remainder := QuizMaxPool % n
	for index := range weights {
		weight := base
		if index < remainder {
			weight++
		}
		weights[index] = weight
	}
	return weights
}

// RunQuizFamily executes the custom quiz family: each question is answered in
// one isolated context, then graded against its reference answer in a second
// fully isolated context. Any non-200 after the retry boundary is terminal for
// the whole family (family convention), and the reference answer never reaches
// evidence.
func RunQuizFamily(ctx context.Context, input Suite, timeout time.Duration) ([]Evaluation, error) {
	if len(input.QuizQuestions) == 0 {
		return []Evaluation{Evaluation{Kind: "custom_quiz", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "quiz_questions_unavailable"}}}, nil
	}
	_, stream, err := input.probeMode()
	if err != nil {
		return nil, err
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
	weights := quizWeights(len(input.QuizQuestions))
	items := make([]Evaluation, 0, len(input.QuizQuestions))
	for index, question := range input.QuizQuestions {
		answerRequest, answerErr := input.tunedBasic(input.Model, question.Question, upstreamMode, stream, quizMaxOutputTokens, 0)
		if answerErr != nil {
			return nil, answerErr
		}
		answerResult, answerExecuteErr := run(ctx, answerRequest)
		if answerExecuteErr != nil {
			return nil, answerExecuteErr
		}
		if familyTerminal() {
			return appendTerminalQuizItems(items, quizRequestFailureEvaluation(question, answerResult)), nil
		}
		gradeRequest, gradeErr := input.quizGradingRequest(question, answerResult.Output, upstreamProtocol, upstreamMode, stream)
		if gradeErr != nil {
			return nil, gradeErr
		}
		gradeResult, gradeExecuteErr := run(ctx, gradeRequest)
		if gradeExecuteErr != nil {
			return nil, gradeExecuteErr
		}
		if familyTerminal() {
			return appendTerminalQuizItems(items, quizRequestFailureEvaluation(question, gradeResult)), nil
		}
		verdict, judgeReason, valid := parseQuizVerdict(gradeResult.Output)
		items = append(items, quizEvaluation(question, weights[index], answerResult.Output, verdict, judgeReason, valid))
	}
	return items, nil
}

// appendTerminalQuizItems applies the terminal family semantics to every
// produced item plus the failing item and stops the family.
func appendTerminalQuizItems(items []Evaluation, failing Evaluation) []Evaluation {
	for index := range items {
		items[index] = terminalFamilyEvaluation(items[index])
	}
	return append(items, terminalFamilyEvaluation(failing))
}

// quizGradingRequest builds the second hop: a brand-new request that carries
// the question, the model's own answer text, and the reference answer, with
// the fixed grading instructions mapped to the protocol's system slot.
func (s Suite) quizGradingRequest(question QuizQuestion, answer string, protocol modelcheckprofile.Protocol, mode string, stream bool) (Request, error) {
	var builder strings.Builder
	builder.WriteString("Question:\n")
	builder.WriteString(question.Question)
	builder.WriteString("\n\nAnswer to grade:\n")
	builder.WriteString(answer)
	builder.WriteString("\n\nReference answer:\n")
	builder.WriteString(question.ReferenceAnswer)
	if len(question.KeyPoints) > 0 {
		builder.WriteString("\n\nGrading key points:\n")
		for _, point := range question.KeyPoints {
			builder.WriteString("- ")
			builder.WriteString(point)
			builder.WriteString("\n")
		}
	}
	request, err := s.tunedBasic(s.Model, builder.String(), mode, stream, quizMaxOutputTokens, 0)
	if err != nil {
		return Request{}, err
	}
	return applyQuizGradingInstructions(request, protocol, quizGradingInstructions)
}

// applyQuizGradingInstructions places the grading instructions into the
// protocol's system slot so every provider receives the same JSON contract
// without relying on protocol-specific structured output modes.
func applyQuizGradingInstructions(request Request, protocol modelcheckprofile.Protocol, instructions string) (Request, error) {
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		payload["instructions"] = instructions
	case modelcheckprofile.ProtocolOpenAIChat:
		messages, _ := payload["messages"].([]any)
		payload["messages"] = append([]any{map[string]any{"role": "system", "content": instructions}}, messages...)
	case modelcheckprofile.ProtocolAnthropic:
		payload["system"] = instructions
	case modelcheckprofile.ProtocolGeminiNative:
		payload["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": instructions}}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	request.Body = body
	return request, nil
}

// parseQuizVerdict extracts the deterministic verdict from a 200 grading
// response. Malformed JSON or a non pass/fail verdict is reported invalid; per
// the structured-output convention a 200 is never retried.
func parseQuizVerdict(output string) (verdict, judgeReason string, valid bool) {
	value := parseJSONObject(output)
	if value == nil {
		return "invalid", "", false
	}
	text, _ := value["verdict"].(string)
	reason, _ := value["reason"].(string)
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "pass":
		return "pass", reason, true
	case "fail":
		return "fail", reason, true
	default:
		return "invalid", reason, false
	}
}

// quizEvaluation projects one graded question. The reference answer and key
// points are deliberately excluded from evidence.
func quizEvaluation(question QuizQuestion, weight int, answer, verdict, judgeReason string, valid bool) Evaluation {
	evidence := map[string]any{
		"questionId":    question.ID,
		"questionTitle": question.Title,
		"verdict":       verdict,
		"answerExcerpt": answerExcerpt(answer),
	}
	status, score := "failed", 0
	reason := "模型未能输出有效对比判定"
	if valid && verdict == "pass" {
		status, score = "passed", weight
		reason = "模型自对比判定通过"
	} else if valid && verdict == "fail" {
		reason = "模型自对比判定不符"
	}
	if valid && strings.TrimSpace(judgeReason) != "" {
		reason = strings.TrimSpace(judgeReason)
	}
	evidence["reason"] = reason
	return Evaluation{Kind: "custom_quiz", Status: status, Score: score, MaxScore: weight, Evidence: evidence}
}

// quizRequestFailureEvaluation keeps the request-failure convention for a hop
// that exhausted the retry boundary: skipped, request-failure evidence, never
// a scored failure.
func quizRequestFailureEvaluation(question QuizQuestion, result Result) Evaluation {
	evidence := map[string]any{
		"questionId":           question.ID,
		"questionTitle":        question.Title,
		"success":              false,
		"requestFailure":       true,
		"excludedFromScoring":  true,
		"evidenceInsufficient": true,
		"httpStatus":           result.HTTPStatus,
	}
	if result.ErrorMessage != "" {
		evidence["error"] = result.ErrorMessage
	}
	return withRetryEvidence(Evaluation{Kind: "custom_quiz", Status: "skipped", Evidence: evidence}, result)
}

func answerExcerpt(answer string) string {
	runes := []rune(strings.TrimSpace(answer))
	if len(runes) > answerExcerptLimit {
		return string(runes[:answerExcerptLimit])
	}
	return string(runes)
}
