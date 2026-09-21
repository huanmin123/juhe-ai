package modelcheckowner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckquestionbank"
)

// fakeQuizReader 是运行时题目解析端口的 fake：只回已通过 id，顺序保持。
type fakeQuizReader struct {
	approved map[string]modelcheckquestionbank.Question
	err      error
}

func (f *fakeQuizReader) ListByIDs(_ context.Context, ids []string, status string) ([]modelcheckquestionbank.Question, error) {
	if f.err != nil {
		return nil, f.err
	}
	questions := make([]modelcheckquestionbank.Question, 0, len(ids))
	for _, id := range ids {
		if question, ok := f.approved[id]; ok && question.Status == status {
			questions = append(questions, question)
		}
	}
	return questions, nil
}

// TestResolveQuizQuestions 覆盖未配置/端口缺失/题目失效三种边界。
func TestResolveQuizQuestions(t *testing.T) {
	requested, questions, err := resolveQuizQuestions(context.Background(), nil, nil)
	if err != nil || requested || questions != nil {
		t.Fatalf("unconfigured requested=%v questions=%v err=%v", requested, questions, err)
	}
	requested, _, err = resolveQuizQuestions(context.Background(), nil, []string{"q1"})
	if err != nil || !requested {
		t.Fatalf("configured without port must stay requested=true (skipped family), err=%v", err)
	}
	if len(questions) != 0 {
		t.Fatalf("configured without port questions=%v", questions)
	}
	reader := &fakeQuizReader{approved: map[string]modelcheckquestionbank.Question{
		"q1": {ID: "q1", Title: "题一", QuestionText: "题面一", ReferenceAnswer: "答一", Status: modelcheckquestionbank.StatusApproved},
	}}
	requested, questions, err = resolveQuizQuestions(context.Background(), reader, []string{"q1", "gone"})
	if err != nil || !requested || len(questions) != 1 || questions[0].Title != "题一" {
		t.Fatalf("resolve requested=%v questions=%+v err=%v", requested, questions, err)
	}
	reader.err = errQuizReaderBoom
	if _, _, err := resolveQuizQuestions(context.Background(), reader, []string{"q1"}); err == nil {
		t.Fatal("question bank read failure must fail the run (fail-closed)")
	}
}

var errQuizReaderBoom = &quizReaderError{}

type quizReaderError struct{}

func (*quizReaderError) Error() string { return "boom" }

// TestBuildCustomQuizSummary 钉死 customQuiz 聚合契约：verdict 映射、扣分
// 口径（仅 failed 且非请求失败）、空题库 unavailable 行、未配置不出现。
func TestBuildCustomQuizSummary(t *testing.T) {
	if _, present := buildCustomQuizSummary(nil, false); present {
		t.Fatal("unconfigured run must not surface customQuiz")
	}
	evaluations := []modelcheckprobe.Evaluation{
		{Kind: "custom_quiz", Status: "passed", Score: 16, MaxScore: 16, Evidence: map[string]any{"questionId": "q1", "questionTitle": "题一", "verdict": "pass", "reason": "判定通过"}},
		{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 15, Evidence: map[string]any{"questionId": "q2", "questionTitle": "题二", "verdict": "fail", "reason": "判定不符"}},
		{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 10, Evidence: map[string]any{"questionId": "q3", "questionTitle": "题三", "verdict": "invalid", "reason": "模型未能输出有效对比判定"}},
		{Kind: "custom_quiz", Status: "skipped", Score: 0, MaxScore: 10, Evidence: map[string]any{"questionId": "q4", "questionTitle": "题四", "requestFailure": true, "excludedFromScoring": true}},
	}
	summary, present := buildCustomQuizSummary(evaluations, true)
	if !present || !summary.Enabled || summary.MaxScore != 31 {
		t.Fatalf("summary=%+v present=%v", summary, present)
	}
	// q2 判定不符扣 15；q3 invalid→failed 扣 10；q4 请求失败不扣。
	if summary.Deduction != 25 || summary.Score != 6 {
		t.Fatalf("deduction=%d score=%d, want 25/6", summary.Deduction, summary.Score)
	}
	if len(summary.Items) != 4 {
		t.Fatalf("items=%+v", summary.Items)
	}
	expectVerdicts := []string{"passed", "failed", "failed", "unavailable"}
	for index, expect := range expectVerdicts {
		if summary.Items[index].Verdict != expect {
			t.Fatalf("item %d verdict=%s, want %s (%+v)", index, summary.Items[index].Verdict, expect, summary.Items[index])
		}
	}
	if summary.Items[3].Reason != "" || summary.Items[3].QuestionID != "q4" {
		t.Fatalf("request failure item keeps its identity: %+v", summary.Items[3])
	}
	// 空题库：家族产出单条 skipped unavailable 行。
	empty, present := buildCustomQuizSummary([]modelcheckprobe.Evaluation{{Kind: "custom_quiz", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "quiz_questions_unavailable"}}}, true)
	if !present || empty.Score != 31 || empty.Deduction != 0 || len(empty.Items) != 1 {
		t.Fatalf("empty bank summary=%+v", empty)
	}
	if empty.Items[0].QuestionID != "" || empty.Items[0].Title != "" || empty.Items[0].Verdict != "unavailable" || empty.Items[0].Reason != "quiz_questions_unavailable" {
		t.Fatalf("empty bank item=%+v", empty.Items[0])
	}
	// 扣分不会把环节得分打成负数。
	negative, _ := buildCustomQuizSummary([]modelcheckprobe.Evaluation{{Kind: "custom_quiz", Status: "failed", Score: 0, MaxScore: 40, Evidence: map[string]any{"questionId": "q", "verdict": "fail"}}}, true)
	if negative.Score != 0 {
		t.Fatalf("clamped score=%d", negative.Score)
	}
}

func TestCustomQuizAwareItemKey(t *testing.T) {
	if got := customQuizAwareItemKey(modelcheckprobe.Evaluation{Kind: "custom_quiz", Evidence: map[string]any{"questionId": "q1"}}); got != "custom_quiz:q1" {
		t.Fatalf("itemKey=%s", got)
	}
	if got := customQuizAwareItemKey(modelcheckprobe.Evaluation{Kind: "custom_quiz"}); got != "custom_quiz:unavailable" {
		t.Fatalf("empty bank itemKey=%s", got)
	}
	if got := customQuizAwareItemKey(modelcheckprobe.Evaluation{Kind: "stability"}); got != "stability" {
		t.Fatalf("non quiz itemKey=%s", got)
	}
}

// qbQuizRuntimeFixture 构造可运行的 Runtime：上游对作答/对比两跳返回可控
// 判定（对比跳按题面标记返回 fail/pass），题库走 fake reader。
func qbQuizRuntimeFixture(t *testing.T, questions []modelcheckprobe.QuizQuestion) (*Runtime, *Store) {
	t.Helper()
	store := newRuntimeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "Reference answer:") && strings.Contains(string(body), "QUESTION-ONE"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"{\"verdict\":\"fail\",\"reason\":\"答案与参考不符\"}","usage":{"total_tokens":2}}`))
		case strings.Contains(string(body), "Reference answer:") && strings.Contains(string(body), "QUESTION-TWO"):
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"{\"verdict\":\"pass\",\"reason\":\"结论一致\"}","usage":{"total_tokens":2}}`))
		default:
			_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
		}
	}))
	t.Cleanup(server.Close)
	approved := make(map[string]modelcheckquestionbank.Question, len(questions))
	for _, question := range questions {
		approved[question.ID] = modelcheckquestionbank.Question{ID: question.ID, Title: question.Title, QuestionText: question.Question, ReferenceAnswer: question.ReferenceAnswer, Status: modelcheckquestionbank.StatusApproved}
	}
	runtime := &Runtime{
		Store: store, OwnerID: "gateway-quiz",
		Now: func() time.Time { return time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC) },
		Resolve: func(context.Context, RunRequest) (Target, error) {
			return Target{Endpoint: server.URL, Protocol: modelcheckprofile.ProtocolOpenAIResponses, Prompt: "hello", UpstreamModel: "gpt-5.6-sol", DispatchRevision: 1}, nil
		},
		QuestionBank: &fakeQuizReader{approved: approved},
	}
	return runtime, store
}

func qbDurableSummary(t *testing.T, store *Store, runID string) map[string]any {
	t.Helper()
	var raw string
	if err := store.db.QueryRow(`SELECT result_summary_json FROM model_check_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

func qbCustomQuizFromSummary(t *testing.T, summary map[string]any) (map[string]any, bool) {
	t.Helper()
	value, present := summary["customQuiz"]
	if !present {
		return nil, false
	}
	object, _ := value.(map[string]any)
	if object == nil {
		t.Fatalf("customQuiz=%T, want object", value)
	}
	return object, true
}

// TestQBRuntimeRunsCustomQuizFamily 覆盖手动触发：Suite 产出 custom_quiz 项、
// 扣分聚合、itemKey、policy snapshot 快照。
func TestQBRuntimeRunsCustomQuizFamily(t *testing.T) {
	runtime, store := qbQuizRuntimeFixture(t, []modelcheckprobe.QuizQuestion{
		{ID: "q1", Title: "题一", Question: "QUESTION-ONE：1+1=?", ReferenceAnswer: "2"},
		{ID: "q2", Title: "题二", Question: "QUESTION-TWO：天空颜色", ReferenceAnswer: "蓝色"},
	})
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", TriggerKind: "manual", CustomQuestionIds: []string{"q1", "q2"}})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	summary := qbDurableSummary(t, store, result.RunID)
	quiz, present := qbCustomQuizFromSummary(t, summary)
	if !present {
		t.Fatalf("configured run must surface customQuiz: keys=%v", summary)
	}
	if enabled, _ := quiz["enabled"].(bool); !enabled {
		t.Fatalf("customQuiz=%+v", quiz)
	}
	// n=2 → [16,15]；q1 fail 扣 16、q2 pass 不扣 → 31-16=15。
	if score, _ := quiz["score"].(float64); int(score) != 15 {
		t.Fatalf("score=%v, want 15", quiz["score"])
	}
	if deduction, _ := quiz["deduction"].(float64); int(deduction) != 16 {
		t.Fatalf("deduction=%v, want 16", quiz["deduction"])
	}
	items, _ := quiz["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	first, _ := items[0].(map[string]any)
	if first["questionId"] != "q1" || first["verdict"] != "failed" || first["title"] != "题一" {
		t.Fatalf("item0 qid=%v verdict=%v title=%v reason=%v", first["questionId"], first["verdict"], first["title"], first["reason"])
	}
	second, _ := items[1].(map[string]any)
	if second["verdict"] != "passed" {
		t.Fatalf("item1=%+v", second)
	}
	// durable items：题库项 itemKey 为 custom_quiz:<questionId>。
	rows, err := store.db.Query(`SELECT item_key,status,evidence_summary_json FROM model_check_items WHERE run_id=? AND item_key LIKE 'custom_quiz:%' ORDER BY item_key`, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys := map[string]string{}
	for rows.Next() {
		var key, status, evidence string
		if err := rows.Scan(&key, &status, &evidence); err != nil {
			t.Fatal(err)
		}
		keys[key] = status + "|" + evidence
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("custom quiz durable items=%v", keys)
	}
	for _, key := range []string{"custom_quiz:q1", "custom_quiz:q2"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing item key %s in %v", key, keys)
		}
	}
	if strings.Contains(keys["custom_quiz:q1"], "参考答案") || strings.Contains(keys["custom_quiz:q1"], `"referenceAnswer"`) {
		t.Fatalf("evidence leaked reference material: %s", keys["custom_quiz:q1"])
	}
	// policy_snapshot_json 自然快照题目 id。
	var snapshot string
	if err := store.db.QueryRow(`SELECT policy_snapshot_json FROM model_check_runs WHERE id=?`, result.RunID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "customQuestionIds") {
		t.Fatalf("policy snapshot=%s", snapshot)
	}
}

// TestQBRuntimeUnconfiguredOmitsCustomQuiz：未配置时零 custom 项且
// resultSummary 不含 customQuiz 键（前端按可缺失容错）。
func TestQBRuntimeUnconfiguredOmitsCustomQuiz(t *testing.T) {
	runtime, store := qbQuizRuntimeFixture(t, []modelcheckprobe.QuizQuestion{{ID: "q1", Title: "题一", Question: "QUESTION-ONE", ReferenceAnswer: "2"}})
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", TriggerKind: "manual"})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	summary := qbDurableSummary(t, store, result.RunID)
	if _, present := qbCustomQuizFromSummary(t, summary); present {
		t.Fatal("unconfigured run must not surface customQuiz")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id=? AND item_key LIKE 'custom_quiz:%'`, result.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unconfigured custom items=%d err=%v", count, err)
	}
}

// TestQBRuntimeScheduledCarriesFrozenQuestions：scheduled 触发与 manual 行为
// 一致，题目 id 来自 claim 时冻结的 payload（此处直接以请求入参模拟冻结后
// 的 payload 字段），失效 id 被过滤后仍产出 skipped unavailable 项。
func TestQBRuntimeScheduledCarriesFrozenQuestions(t *testing.T) {
	runtime, store := qbQuizRuntimeFixture(t, nil)
	result, err := runtime.Run(context.Background(), RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "sys", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", ConfigRevision: "cfg-1", PolicyRevision: "1", TriggerKind: "scheduled", ScheduleID: "sch-1", CustomQuestionIds: []string{"deleted-question"}})
	if err != nil || result.Status != string(RunCompleted) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	summary := qbDurableSummary(t, store, result.RunID)
	quiz, present := qbCustomQuizFromSummary(t, summary)
	if !present {
		t.Fatal("scheduled run with frozen ids must surface customQuiz even when all ids became invalid")
	}
	if score, _ := quiz["score"].(float64); int(score) != 31 {
		t.Fatalf("invalid-only ids must not deduct: quiz=%+v", quiz)
	}
	items, _ := quiz["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%+v", items)
	}
	only, _ := items[0].(map[string]any)
	if only["verdict"] != "unavailable" || only["reason"] != "quiz_questions_unavailable" || only["questionId"] != "" {
		t.Fatalf("item qid=%v verdict=%v reason=%v", only["questionId"], only["verdict"], only["reason"])
	}
	var itemKey string
	if err := store.db.QueryRow(`SELECT item_key FROM model_check_items WHERE run_id=? AND item_key LIKE 'custom_quiz:%'`, result.RunID).Scan(&itemKey); err != nil || itemKey != "custom_quiz:unavailable" {
		t.Fatalf("itemKey=%q err=%v", itemKey, err)
	}
}

// qbCapturingRunner 捕获 SchedulerRunExecutor.Execute 传给 Runtime 的最终
// 请求（字段覆写发生在 Build 之后，Runtime 收到的才是执行入参）。
type qbCapturingRunner struct {
	captured RunRequest
}

func (r *qbCapturingRunner) Run(_ context.Context, request RunRequest) (RunResult, error) {
	r.captured = request
	return RunResult{Status: string(RunCompleted), RunID: "run-sch"}, nil
}

// TestQBSchedulerExecutorFreezesCustomQuestionIds：payload 的题库字段按不可
// 变契约进入执行请求（scheduled 与 recovery 共用同一字段拷贝）。
func TestQBSchedulerExecutorFreezesCustomQuestionIds(t *testing.T) {
	payloadJSON := `{"systemAccountId":"sys","actorSystemAccountId":"sys","targetType":"account","targetId":"acct","model":"gpt-5.6-sol","profile":"quick","providerCode":"openai","threshold":70,"penaltyAction":"fallback","configRevision":"3","dispatchRevision":6,"sourceConfigRevision":"3","sourceDispatchRevision":6,"policyRevision":"2","probeSetVersion":"p1","identityKey":"k","scheduleId":"sch","ownerId":"gateway-1","scheduleRevision":3,"intervalMinutes":60,"customQuestionIds":["q1","q2"]}`
	for _, kind := range []SchedulerKind{SchedulerScheduled, SchedulerQualityRecovery} {
		payload := payloadJSON
		if kind == SchedulerQualityRecovery {
			// 恢复臂需要 lease 元数据；题目字段保持不变。
			payload = strings.Replace(payloadJSON, `"ownerId":"gateway-1"`, `"ownerId":"gateway-1","enforcementId":"enf","generation":2,"recoveryIntervalMinutes":15`, 1)
		}
		runner := &qbCapturingRunner{}
		executor := &SchedulerRunExecutor{
			Runtime: runner,
			Build: func(context.Context, ScheduledPayload) (RunRequest, error) {
				return RunRequest{}, nil
			},
			Scheduled: func(context.Context, ScheduledPayload, RunResult) error { return nil },
			Recovery:  func(context.Context, RecoveryPayload, bool) error { return nil },
		}
		if err := executor.Execute(context.Background(), ScheduleTask{Kind: kind, OwnerID: "gateway-1", Payload: []byte(payload)}); err != nil {
			t.Fatalf("%s execute: %v", kind, err)
		}
		if len(runner.captured.CustomQuestionIds) != 2 || runner.captured.CustomQuestionIds[0] != "q1" || runner.captured.CustomQuestionIds[1] != "q2" {
			t.Fatalf("%s request lost frozen question ids: %+v", kind, runner.captured)
		}
	}
	// 不携带题库字段的旧 payload 必须保持空（不启用题库家族）。
	runner := &qbCapturingRunner{}
	executor := &SchedulerRunExecutor{
		Runtime:   runner,
		Build:     func(context.Context, ScheduledPayload) (RunRequest, error) { return RunRequest{}, nil },
		Scheduled: func(context.Context, ScheduledPayload, RunResult) error { return nil },
	}
	legacy := strings.Replace(payloadJSON, `,"customQuestionIds":["q1","q2"]`, "", 1)
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerScheduled, OwnerID: "gateway-1", Payload: []byte(legacy)}); err != nil {
		t.Fatal(err)
	}
	if len(runner.captured.CustomQuestionIds) != 0 {
		t.Fatalf("legacy payload must not configure quiz: %+v", runner.captured)
	}
}

func dumpQuizItem(item map[string]any) string {
	encoded, _ := json.Marshal(item)
	return string(encoded)
}
