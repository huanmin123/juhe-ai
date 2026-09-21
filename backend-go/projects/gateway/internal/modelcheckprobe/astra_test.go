package modelcheckprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestAstraConstantsScopeTable(t *testing.T) {
	cases := []struct {
		model, profile, protocol string
		want                     bool
	}{
		{"gpt-6-astra", "full", "openai_responses", true},
		{"gpt-6-astra", "quick", "openai_responses", false},
		{"gpt-6-astra", "full", "openai_chat", false},
		{"gpt-5.6-sol", "full", "openai_responses", false},
		{"gpt-5.6-terra", "full", "openai_responses", false},
		{"gpt-6-astra-pro", "full", "openai_responses", false},
		{"", "full", "openai_responses", false},
	}
	for _, testCase := range cases {
		if got := ShouldRunAstraConstants(testCase.model, testCase.profile, testCase.protocol); got != testCase.want {
			t.Fatalf("ShouldRunAstraConstants(%q,%q,%q)=%v want=%v", testCase.model, testCase.profile, testCase.protocol, got, testCase.want)
		}
	}
}

var (
	astraMultiplyPattern = regexp.MustCompile(`(?i)Multiply it by ([2-9]), then divide it by ([2-9])`)
	astraAddPattern      = regexp.MustCompile(`(?i)Add ([2-9]), then subtract ([2-9])`)
	astraNoncePattern    = regexp.MustCompile(`Trace ([a-z0-9]{4,6})\. Reply with exactly: 32`)
	astraBudgetPattern   = regexp.MustCompile(`context budget = ([0-9]+)`)
)

func astraUserPrompt(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Input []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Input) == 0 || len(payload.Input[0].Content) == 0 {
		t.Fatalf("astra prompt 解析失败: body=%s", body)
	}
	return payload.Input[0].Content[0].Text
}

func astraSameFactor(t *testing.T, pattern *regexp.Regexp, text string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(text)
	if match == nil || match[1] != match[2] {
		t.Fatalf("乘除/加减必须使用同一随机数: text=%q match=%v", text, match)
	}
	return match[1]
}

func TestAstraConstantsRequestsMatchContract(t *testing.T) {
	requests, coverage, err := AstraConstantsRequestsForStream("gpt-6-astra", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 {
		t.Fatalf("requests=%d", len(requests))
	}
	coverageValue, coverageErr := strconv.Atoi(coverage)
	if coverageErr != nil || coverageValue < 60 || coverageValue > 99 {
		t.Fatalf("coverage=%q", coverage)
	}
	var traceNonce string
	for index, request := range requests {
		if request.Path != "/v1/responses" || request.ExpectedModel != "gpt-6-astra" || request.EndpointMode != modelcheckprofile.EndpointModeResponsesSSE {
			t.Fatalf("request %d routing=%+v", index, request)
		}
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatalf("request %d body: %v", index, err)
		}
		if payload["stream"] != true || payload["max_output_tokens"] != float64(16) || payload["store"] != false || payload["temperature"] != float64(0) {
			t.Fatalf("request %d bounded fields=%+v", index, payload)
		}
		if payload["instructions"] == "" {
			t.Fatalf("request %d missing instructions", index)
		}
		reasoning, ok := payload["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "high" {
			t.Fatalf("request %d reasoning=%+v", index, payload["reasoning"])
		}
		include, ok := payload["include"].([]any)
		if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("request %d include=%+v", index, payload["include"])
		}
		prompt := astraUserPrompt(t, request.Body)
		switch index {
		case 0:
			if !strings.Contains(prompt, "model generation number") {
				t.Fatalf("generation prompt=%q", prompt)
			}
			astraSameFactor(t, astraMultiplyPattern, prompt)
		case 1:
			if !strings.Contains(prompt, "context budget value in your instructions") {
				t.Fatalf("budget prompt=%q", prompt)
			}
			astraSameFactor(t, astraAddPattern, prompt)
		case 2:
			if !strings.HasPrefix(prompt, "Trace ") || !strings.HasSuffix(prompt, ". Reply with exactly: 32") {
				t.Fatalf("trace prompt=%q", prompt)
			}
			traceNonce = strings.TrimSuffix(strings.TrimPrefix(prompt, "Trace "), ". Reply with exactly: 32")
			if len(traceNonce) < 4 || len(traceNonce) > 6 {
				t.Fatalf("nonce length=%d", len(traceNonce))
			}
		case 3:
			instructions, _ := payload["instructions"].(string)
			if !strings.Contains(instructions, "synthetic record is authoritative: context budget = "+coverage) || !strings.Contains(instructions, "do not substitute another number") {
				t.Fatalf("coverage instructions=%q coverage=%q", instructions, coverage)
			}
			if !strings.Contains(prompt, "Read the context budget, multiply it by") {
				t.Fatalf("coverage prompt=%q", prompt)
			}
			astraSameFactor(t, astraMultiplyPattern, prompt)
		}
	}
	if traceNonce == "" {
		t.Fatal("trace nonce missing")
	}
}

func TestEvaluateAstraConstantsAllRoundsPass(t *testing.T) {
	results := []Result{
		{Success: true, HTTPStatus: 200, Output: "6"},
		{Success: true, HTTPStatus: 200, Output: "48"},
		{Success: true, HTTPStatus: 200, Output: "32"},
		{Success: true, HTTPStatus: 200, Output: " 87 "},
	}
	item := EvaluateAstraConstants("gpt-6-astra", results, "87")
	if item.Kind != "astra_constants" || item.Status != "passed" || item.Score != AstraConstantsMaxScore || item.MaxScore != AstraConstantsMaxScore {
		t.Fatalf("passed item=%#v", item)
	}
	if item.Evidence["scorePenalty"] != 0 || item.Evidence["hardAnomaly"] != false || item.Evidence["probeVersion"] != AstraConstantsProbeVersion {
		t.Fatalf("passed evidence=%+v", item.Evidence)
	}
	if item.Evidence["roundCount"] != 4 || item.Evidence["successCount"] != 4 || item.Evidence["requestFailureCount"] != 0 {
		t.Fatalf("passed evidence=%+v", item.Evidence)
	}
	observations := item.Evidence["observations"].([]AstraConstantsObservation)
	for index, observation := range observations {
		if observation.Classification != "current_success" || observation.HardAnomaly {
			t.Fatalf("observation %d=%+v", index, observation)
		}
	}
	if observations[0].Expected != "6" || observations[1].Expected != "48" || observations[2].Expected != "32" || observations[3].Expected != "87" {
		t.Fatalf("expected values=%+v", observations)
	}
	if observations[0].Round != "generation" || observations[1].Round != "context_budget" || observations[2].Round != "output_integrity" || observations[3].Round != "coverage" {
		t.Fatalf("round kinds=%+v", observations)
	}
}

func TestEvaluateAstraConstantsInternalValueMismatchIsHard(t *testing.T) {
	for index, output := range []string{"5", "无法解析"} {
		results := []Result{{Success: true, HTTPStatus: 200, Output: output}, {Success: true, HTTPStatus: 200, Output: "48"}, {Success: true, HTTPStatus: 200, Output: "32"}, {Success: true, HTTPStatus: 200, Output: "87"}}
		item := EvaluateAstraConstants("gpt-6-astra", results, "87")
		if item.Status != "failed" || item.Evidence["scorePenalty"] != AstraStrongPenalty || item.Evidence["hardAnomaly"] != true || item.Evidence["strongAnomaly"] != true {
			t.Fatalf("case %d item=%#v", index, item)
		}
		if item.MaxScore != 0 || item.Score != 0 {
			t.Fatalf("case %d 失败项不得进入评分分母: %#v", index, item)
		}
		observations := item.Evidence["observations"].([]AstraConstantsObservation)
		wantClassification := "internal_value_mismatch"
		if index == 1 {
			wantClassification = "internal_value_unreadable"
		}
		if observations[0].Classification != wantClassification || observations[0].HardAnomaly != true {
			t.Fatalf("case %d observation=%+v", index, observations[0])
		}
		if len(observations[0].Observed) > astraObservedBound {
			t.Fatalf("case %d observed=%q 超出保留上限", index, observations[0].Observed)
		}
	}
	summary := SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "astra_constants", Status: "failed", Evidence: map[string]any{"hardAnomaly": true, "scorePenalty": AstraStrongPenalty}},
	}, false, "full")
	if summary.Level != "suspicious" || summary.Message != "Astra 专项探针发现疑似响应替换或混用，建议结合其他证据复核" {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestEvaluateAstraConstantsWeakAndCoveragePenalties(t *testing.T) {
	base := func(trace, coverageOutput string) []Result {
		return []Result{
			{Success: true, HTTPStatus: 200, Output: "6"},
			{Success: true, HTTPStatus: 200, Output: "48"},
			{Success: true, HTTPStatus: 200, Output: trace},
			{Success: true, HTTPStatus: 200, Output: coverageOutput},
		}
	}
	traceWrong := EvaluateAstraConstants("gpt-6-astra", base("31", "87"), "87")
	if traceWrong.Status != "failed" || traceWrong.Evidence["scorePenalty"] != AstraWeakPenalty || traceWrong.Evidence["hardAnomaly"] != false {
		t.Fatalf("trace wrong=%#v", traceWrong)
	}
	observations := traceWrong.Evidence["observations"].([]AstraConstantsObservation)
	if observations[2].Classification != "compliance_anomaly" {
		t.Fatalf("trace observation=%+v", observations[2])
	}
	coverageWrong := EvaluateAstraConstants("gpt-6-astra", base("32", "86"), "87")
	if coverageWrong.Status != "failed" || coverageWrong.Evidence["scorePenalty"] != AstraCoveragePenalty || coverageWrong.Evidence["hardAnomaly"] != false {
		t.Fatalf("coverage wrong=%#v", coverageWrong)
	}
	observations = coverageWrong.Evidence["observations"].([]AstraConstantsObservation)
	if observations[3].Classification != "coverage_anomaly" {
		t.Fatalf("coverage observation=%+v", observations[3])
	}
	bothWrong := EvaluateAstraConstants("gpt-6-astra", base("31", "86"), "87")
	if bothWrong.Evidence["scorePenalty"] != AstraCoveragePenalty {
		t.Fatalf("多轮异常必须取最重一类: %#v", bothWrong)
	}
	summary := SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "astra_constants", Status: "failed", Evidence: map[string]any{"hardAnomaly": false, "scorePenalty": AstraWeakPenalty}},
	}, false, "full")
	if summary.Level != "likely" {
		t.Fatalf("弱异常不得硬短路 suspicious: %+v", summary)
	}
}

func TestEvaluateAstraConstantsTerminalAndPartialAreSkipped(t *testing.T) {
	terminal := EvaluateAstraConstants("gpt-6-astra", []Result{
		{Success: true, HTTPStatus: 200, Output: "6"},
		{Success: false, HTTPStatus: 503, RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{503, 503, 503}},
	}, "87")
	if terminal.Status != "skipped" || terminal.Evidence["terminalFailure"] != true || terminal.Evidence["excludedFromScoring"] != true || terminal.Evidence["evidenceInsufficient"] != true {
		t.Fatalf("terminal=%#v", terminal)
	}
	if terminal.Evidence["scorePenalty"] != 0 || terminal.Evidence["hardAnomaly"] != false {
		t.Fatalf("终局失败不得计罚分或硬异常: %#v", terminal)
	}
	partial := EvaluateAstraConstants("gpt-6-astra", []Result{
		{Success: true, HTTPStatus: 200, Output: "6"},
		{Success: false, HTTPStatus: 200},
	}, "87")
	if partial.Status != "skipped" || partial.Evidence["scorePenalty"] != 0 || partial.Evidence["hardAnomaly"] != false {
		t.Fatalf("partial=%#v", partial)
	}
	missing := EvaluateAstraConstants("gpt-6-astra", nil, "87")
	if missing.Status != "skipped" || missing.Evidence["reason"] != "astra_constants_probe_results_missing" || missing.Evidence["notApplicable"] == true {
		t.Fatalf("missing=%#v", missing)
	}
	invalidCoverage := EvaluateAstraConstants("gpt-6-astra", []Result{{Success: true, HTTPStatus: 200, Output: "6"}}, "100")
	if invalidCoverage.Status != "skipped" || invalidCoverage.Evidence["reason"] != "astra_constants_coverage_value_invalid" {
		t.Fatalf("invalid coverage=%#v", invalidCoverage)
	}
}

func TestEvaluateAstraConstantsSkippedEvidenceFields(t *testing.T) {
	item := EvaluateAstraConstants("gpt-5.6-sol", nil, "87")
	if item.Kind != "astra_constants" || item.Status != "skipped" {
		t.Fatalf("item=%#v", item)
	}
	if item.Evidence["evidenceInsufficient"] != true || item.Evidence["excludedFromScoring"] != true || item.Evidence["notApplicable"] != true || item.Evidence["reason"] != "astra_constants_scope_not_applicable" {
		t.Fatalf("skipped evidence=%+v", item.Evidence)
	}
	if item.Score != 0 || item.MaxScore != 0 {
		t.Fatalf("skipped 项不得计分: %#v", item)
	}
}

func TestAstraConstantsRandomizationPerRun(t *testing.T) {
	nonces := map[string]bool{}
	coverages := map[string]bool{}
	factors := map[string]bool{}
	for round := 0; round < 24; round++ {
		requests, coverage, err := AstraConstantsRequestsForStream("gpt-6-astra", false)
		if err != nil || len(requests) != 4 {
			t.Fatalf("round %d requests=%d err=%v", round, len(requests), err)
		}
		nonceMatch := astraNoncePattern.FindStringSubmatch(string(requests[2].Body))
		if nonceMatch == nil {
			t.Fatalf("trace prompt=%s", requests[2].Body)
		}
		nonces[nonceMatch[1]] = true
		coverages[coverage] = true
		factors[astraSameFactor(t, astraMultiplyPattern, astraUserPrompt(t, requests[0].Body))] = true
		budgetMatch := astraBudgetPattern.FindStringSubmatch(string(requests[3].Body))
		if budgetMatch == nil || budgetMatch[1] != coverage {
			t.Fatalf("coverage 指令与返回值不一致: match=%v coverage=%q", budgetMatch, coverage)
		}
	}
	if len(nonces) < 2 || len(coverages) < 2 || len(factors) < 2 {
		t.Fatalf("随机化失败: nonces=%v coverages=%v factors=%v", nonces, coverages, factors)
	}
}

func TestAstraRandomHelpersRanges(t *testing.T) {
	for round := 0; round < 32; round++ {
		nonce, err := randomAstraNonce()
		if err != nil || len(nonce) < 4 || len(nonce) > 6 {
			t.Fatalf("nonce=%q err=%v", nonce, err)
		}
		for _, symbol := range nonce {
			if !(symbol >= 'a' && symbol <= 'z') && !(symbol >= '0' && symbol <= '9') {
				t.Fatalf("nonce=%q 含非法字符 %q", nonce, symbol)
			}
		}
		coverage, err := randomAstraCoverage()
		if err != nil {
			t.Fatal(err)
		}
		value, parseErr := strconv.Atoi(coverage)
		if parseErr != nil || value < 60 || value > 99 {
			t.Fatalf("coverage=%q", coverage)
		}
		factor, err := randomAstraInt(2, 5)
		if err != nil || factor < 2 || factor > 5 {
			t.Fatalf("factor=%d err=%v", factor, err)
		}
	}
}

func TestSummarizeChecksAstraPenaltyAccumulatesAndClamps(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "juice", Status: "failed", Evidence: map[string]any{"scorePenalty": 80}},
		{Kind: "astra_constants", Status: "failed", Evidence: map[string]any{"hardAnomaly": true, "scorePenalty": AstraStrongPenalty}},
	}
	summary := SummarizeChecks(checks, false, "quick")
	if summary.Score != 0 {
		t.Fatalf("罚分累加后必须 clamp 到 0: %+v", summary)
	}
	// 只有 astra 弱罚分时按通用累加扣减，分数进入 quick 阶梯而非短路。
	summary = SummarizeChecks([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "astra_constants", Status: "failed", Evidence: map[string]any{"scorePenalty": AstraWeakPenalty}},
	}, false, "quick")
	if summary.Score != 92 {
		t.Fatalf("astra 罚分必须进入通用累加: %+v", summary)
	}
}

// astraEchoTransport 在全能正确上游之上拦截 Astra 专项四轮，回放观测契约的
// 期望输出；其余探针交给 wlEchoTransport。
type astraEchoTransport struct {
	inner *wlEchoTransport
}

func (t *astraEchoTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(strings.NewReader(string(body)))
	text := string(body)
	output := ""
	switch {
	case strings.Contains(text, "model generation number"):
		output = "6"
	case strings.Contains(text, "context budget value in your instructions"):
		output = "48"
	case astraNoncePattern.MatchString(astraUserPromptText(text)):
		output = "32"
	case strings.Contains(text, "synthetic record is authoritative"):
		match := astraBudgetPattern.FindStringSubmatch(text)
		if match == nil {
			return nil, fmt.Errorf("astra coverage instructions missing synthetic value")
		}
		output = match[1]
	default:
		return t.inner.RoundTrip(request)
	}
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	responseBody := fmt.Sprintf(`{"model":%q,"output_text":%q,"usage":{"input_tokens":10,"total_tokens":11}}`, payload.Model, output)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
}

// astraUserPromptText is the panic-free variant used inside transports.
func astraUserPromptText(body string) string {
	var payload struct {
		Input []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || len(payload.Input) == 0 || len(payload.Input[0].Content) == 0 {
		return ""
	}
	return payload.Input[0].Content[0].Text
}

func TestWlRunSuiteFullRunsAstraConstantsForAstra(t *testing.T) {
	// 业务契约：gpt-6-astra + full + responses 必须跑满 Astra 专项四轮，
	// Juice 对 Astra 保持 skipped+notApplicable，且专项项位于 Juice 之后。
	transport := &astraEchoTransport{inner: &wlEchoTransport{}}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:    "https://astra.example",
		Client:      &http.Client{Transport: transport},
		Model:       "gpt-6-astra",
		Profile:     "full",
		Protocol:    modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:   deterministicTokenizer{},
		ModelLimits: deterministicLimits{},
	}, time.Second)
	if err != nil {
		t.Fatalf("完整套件不应失败: %v", err)
	}
	juiceIndex, astraIndex := -1, -1
	for index, item := range items {
		switch unscopedKind(item.Kind) {
		case "juice":
			juiceIndex = index
			if item.Status != "skipped" || item.Evidence["notApplicable"] != true || item.Evidence["reason"] != "juice_scope_not_applicable" {
				t.Fatalf("astra 目标上 juice 必须 skipped: %+v", item)
			}
		case "astra_constants":
			astraIndex = index
			if item.Status != "passed" || item.Score != AstraConstantsMaxScore || item.MaxScore != AstraConstantsMaxScore {
				t.Fatalf("astra_constants=%#v", item)
			}
			if item.Evidence["scorePenalty"] != 0 || item.Evidence["probeVersion"] != AstraConstantsProbeVersion {
				t.Fatalf("astra_constants evidence=%+v", item.Evidence)
			}
		}
	}
	if juiceIndex < 0 || astraIndex < 0 || juiceIndex > astraIndex {
		t.Fatalf("专项项顺序错误: juice=%d astra=%d items=%+v", juiceIndex, astraIndex, items)
	}
	summary := SummarizeChecks(items, false, "full")
	if summary.Level == "suspicious" || summary.Level == "unavailable" {
		t.Fatalf("全过链路不得被判 suspicious/unavailable: %+v", summary)
	}
}

func TestWlRunSuiteFullEmitsAstraSkipForOtherModels(t *testing.T) {
	// 非 Astra 模型的 full 套件必须显式携带 astra_constants skipped 证据，
	// 且该证据不得破坏可信对比聚合的 formed/negative 判定。
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:    "https://sol.example",
		Client:      &http.Client{Transport: &wlEchoTransport{}},
		Model:       "gpt-5.6-sol",
		Profile:     "full",
		Protocol:    modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:   deterministicTokenizer{},
		ModelLimits: deterministicLimits{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if unscopedKind(item.Kind) == "astra_constants" {
			found = true
			if item.Status != "skipped" || item.Evidence["notApplicable"] != true || item.Evidence["reason"] != "astra_constants_scope_not_applicable" {
				t.Fatalf("astra_constants=%+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("缺少 astra_constants skipped 项: items=%+v", items)
	}
	formed, _, negative := comparisonEvidenceState(append([]Evaluation(nil), items...))
	if !formed || negative {
		t.Fatalf("sol 套件可信证据被 astra skip 破坏: formed=%v negative=%v", formed, negative)
	}
}
