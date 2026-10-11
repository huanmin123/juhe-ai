package modelcheckprobe

// quizdiagnosis_test.go 覆盖题库提交门禁单题诊断：verdict 解析口径、
// questionAlignment/keyPointResults/reason 可选容错、两跳时延合计与家族
// 终态（重试耗尽）转 request_failed 的正常结果语义。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func quizDiagnosisTestSuite(transport *quizTransport) Suite {
	suite := quizTestSuite("quick", transport)
	suite.QuizRequested = true
	suite.QuizQuestions = []QuizQuestion{quizTestQuestions()[0]}
	return suite
}

// TestRunQuizDiagnosisParsesVerdictAndAlignment 覆盖 pass/fail 的完整解析：
// 结论、对齐、要点结论、理由截断、作答摘录与两跳时延合计。
func TestRunQuizDiagnosisParsesVerdictAndAlignment(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass","questionAlignment":"aligned","keyPointResults":[{"point":"quiz-keypoint-必须提到圆周率-one","met":true,"note":"覆盖"},{"point":"缺证要点","met":false,"note":"未提及"}],"reason":"` + strings.Repeat("长", 300) + `"}`}
	diagnosis, err := RunQuizDiagnosis(context.Background(), quizDiagnosisTestSuite(transport), time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if diagnosis.Verdict != "pass" || diagnosis.Alignment != "aligned" {
		t.Fatalf("verdict/alignment = %q/%q", diagnosis.Verdict, diagnosis.Alignment)
	}
	if len(diagnosis.KeyPointResults) != 2 || !diagnosis.KeyPointResults[0].Met || diagnosis.KeyPointResults[1].Met {
		t.Fatalf("keyPointResults = %+v", diagnosis.KeyPointResults)
	}
	if len([]rune(diagnosis.GraderNote)) != quizDiagnosisGraderNoteLimit {
		t.Fatalf("graderNote 必须按 rune 截断到 %d: %d", quizDiagnosisGraderNoteLimit, len([]rune(diagnosis.GraderNote)))
	}
	if diagnosis.AnswerExcerpt != quizTestAnswerText {
		t.Fatalf("answerExcerpt = %q", diagnosis.AnswerExcerpt)
	}
	if diagnosis.HTTPStatus != 200 {
		t.Fatalf("httpStatus = %d", diagnosis.HTTPStatus)
	}
	if diagnosis.LatencyMs < 0 || diagnosis.ErrorMessage != "" {
		t.Fatalf("latency/error = %d/%q", diagnosis.LatencyMs, diagnosis.ErrorMessage)
	}
	if transport.answerRequests != 1 || transport.gradeRequests != 1 {
		t.Fatalf("两跳各请求一次: answer=%d grade=%d", transport.answerRequests, transport.gradeRequests)
	}
}

// TestRunQuizDiagnosisInvalidAndMissingOptionalFields 覆盖 invalid 口径与
// 可选字段缺失容错：非法 JSON / 非 pass-fail 结论 → invalid；有效结论但
// 缺失 questionAlignment/keyPointResults/reason 时分别归 unknown/空/空。
func TestRunQuizDiagnosisInvalidAndMissingOptionalFields(t *testing.T) {
	cases := []struct {
		name        string
		gradeOutput string
		wantVerdict string
	}{
		{"非法 JSON", `not-json-output`, "invalid"},
		{"非 pass-fail 结论", `{"verdict":"maybe","questionAlignment":"misaligned"}`, "invalid"},
		{"有效结论缺失可选字段", `{"verdict":"fail"}`, "fail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &quizTransport{gradeOutput: tc.gradeOutput}
			diagnosis, err := RunQuizDiagnosis(context.Background(), quizDiagnosisTestSuite(transport), time.Second)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if diagnosis.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q", diagnosis.Verdict, tc.wantVerdict)
			}
			if tc.gradeOutput == `{"verdict":"maybe","questionAlignment":"misaligned"}` {
				// 合法 JSON 的 questionAlignment 照常解析，仅结论 invalid。
				if diagnosis.Alignment != "misaligned" {
					t.Fatalf("alignment = %q", diagnosis.Alignment)
				}
			} else if diagnosis.Alignment != "unknown" {
				t.Fatalf("alignment = %q, want unknown", diagnosis.Alignment)
			}
			if tc.gradeOutput == `{"verdict":"fail"}` && len(diagnosis.KeyPointResults) != 0 {
				t.Fatalf("缺失要点必须容错为空: %+v", diagnosis.KeyPointResults)
			}
		})
	}
}

// TestRunQuizDiagnosisRequestFailedIsNormalResult 覆盖家族终态：作答跳或
// 裁判跳重试耗尽 → verdict=request_failed 携带 HTTPStatus/ErrorMessage 的
// 正常结果（不返回 error），且终态后不再发起新请求。
func TestRunQuizDiagnosisRequestFailedIsNormalResult(t *testing.T) {
	t.Run("作答跳失败", func(t *testing.T) {
		transport := &quizTransport{failAnswerWhenContains: quizTestQuestionText}
		diagnosis, err := RunQuizDiagnosis(context.Background(), quizDiagnosisTestSuite(transport), time.Second)
		if err != nil {
			t.Fatalf("request failure must be a normal result, got error: %v", err)
		}
		if diagnosis.Verdict != "request_failed" || diagnosis.HTTPStatus != 503 || diagnosis.ErrorMessage == "" {
			t.Fatalf("diagnosis = %+v", diagnosis)
		}
		// 响应契约枚举无空串：request_failed 终态同样归一 alignment=unknown。
		if diagnosis.Alignment != "unknown" {
			t.Fatalf("alignment = %q, want unknown", diagnosis.Alignment)
		}
		if transport.gradeRequests != 0 {
			t.Fatalf("作答跳终态后不得再发裁判请求: %d", transport.gradeRequests)
		}
	})
	t.Run("裁判跳失败", func(t *testing.T) {
		transport := &quizTransport{gradeStatus: 503}
		diagnosis, err := RunQuizDiagnosis(context.Background(), quizDiagnosisTestSuite(transport), time.Second)
		if err != nil {
			t.Fatalf("request failure must be a normal result, got error: %v", err)
		}
		if diagnosis.Verdict != "request_failed" || diagnosis.HTTPStatus != 503 {
			t.Fatalf("diagnosis = %+v", diagnosis)
		}
		if diagnosis.Alignment != "unknown" {
			t.Fatalf("alignment = %q, want unknown", diagnosis.Alignment)
		}
		if !strings.Contains(diagnosis.ErrorMessage, "stage failure") {
			t.Fatalf("errorMessage = %q", diagnosis.ErrorMessage)
		}
	})
}

// TestRunQuizDiagnosisRequiresExactlyOneQuestion 固化单题契约：0 或多题
// 入参是调用方编程错误，返回 error（区别于 request_failed 的正常结果）。
func TestRunQuizDiagnosisRequiresExactlyOneQuestion(t *testing.T) {
	transport := &quizTransport{gradeOutput: `{"verdict":"pass"}`}
	for _, count := range []int{0, 2} {
		suite := quizTestSuite("quick", transport)
		suite.QuizRequested = true
		suite.QuizQuestions = quizTestQuestions()[:count]
		if _, err := RunQuizDiagnosis(context.Background(), suite, time.Second); err == nil {
			t.Fatalf("%d questions must return an error", count)
		}
	}
}
