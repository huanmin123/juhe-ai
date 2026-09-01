package modelcheckprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestRunTrustedComparisonUsesBothResolvedModels(t *testing.T) {
	newServer := func(model string) *httptest.Server {
		expectedModel := model
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatalf("model=%q err=%v", request.Model, err)
			}
			output := "OK-MODEL-CHECK"
			switch {
			case strings.Contains(string(body), "json_reasoning") || strings.Contains(string(body), "SIGMA"):
				output = `{"result":83,"tag":"SIGMA"}`
			case strings.Contains(string(body), "code_judgement") || strings.Contains(string(body), "const xs"):
				output = "ALPHA 4-7"
			case strings.Contains(string(body), "refusal_boundary") || strings.Contains(string(body), "绕过他人"):
				output = "DELTA 不能提供"
			case strings.Contains(string(body), "sequence_transform") || strings.Contains(string(body), "从小到大"):
				output = "THETA 4|7|9"
			case strings.Contains(string(body), "table_extract") || strings.Contains(string(body), "北区"):
				output = "IOTA 17 23"
			case strings.Contains(string(body), "style_compact") || strings.Contains(string(body), "向量数据库"):
				output = "向量数据库召回率衡量结果相关内容被找回的比例"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"` + expectedModel + `","output_text":` + mustJSONText(t, output) + `,"usage":{"total_tokens":2}}`))
		}))
	}
	target := newServer("gpt-5.6-sol")
	defer target.Close()
	comparison := newServer("gpt-5.6-terra")
	defer comparison.Close()
	targetTransport := &countingTransport{base: http.DefaultTransport}
	comparisonTransport := &countingTransport{base: http.DefaultTransport}
	items, err := RunSuite(context.Background(), Suite{Endpoint: target.URL, ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", Client: &http.Client{Transport: targetTransport}, Model: "gpt-5.6-sol", Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Tokenizer: deterministicTokenizer{}, ModelLimits: deterministicLimits{}, Comparison: &Suite{Endpoint: comparison.URL, ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", Client: &http.Client{Transport: comparisonTransport}, Model: "gpt-5.6-terra", Profile: "full", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Tokenizer: deterministicTokenizer{}, ModelLimits: deterministicLimits{}}}, time.Second)
	var distribution, cross Evaluation
	for _, item := range items {
		switch item.Kind {
		case "distribution_similarity":
			distribution = item
		case "comparison":
			cross = item
		}
	}
	if err != nil || distribution.Status != "passed" || cross.Status == "failed" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if targetTransport.requests < 10 || comparisonTransport.requests < 4 {
		t.Fatalf("full suite did not consistently use resolved clients: target=%d comparison=%d", targetTransport.requests, comparisonTransport.requests)
	}
}

func TestSuiteEndpointModeMustBeEnabledByTarget(t *testing.T) {
	suite := Suite{Endpoint: "https://example.test", Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesSSE, SupportedEndpointModes: []string{modelcheckprofile.EndpointModeResponsesJSON}}
	if _, _, err := suite.probeMode(); err == nil {
		t.Fatal("disabled endpoint mode must fail closed")
	}
	mode, stream, err := (Suite{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesSSE, SupportedEndpointModes: []string{modelcheckprofile.EndpointModeResponsesSSE}}).probeMode()
	if err != nil || mode != modelcheckprofile.EndpointModeResponsesSSE || !stream {
		t.Fatalf("mode=%q stream=%v err=%v", mode, stream, err)
	}
}

func TestSuiteOpenAIOAuthCodexSupportsResponsesJSONAndSSEOnly(t *testing.T) {
	for _, mode := range []string{modelcheckprofile.EndpointModeResponsesJSON, modelcheckprofile.EndpointModeResponsesSSE} {
		suite := Suite{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: mode, SupportedEndpointModes: []string{mode}, Adapter: AdapterOpenAIOAuthCodex}
		gotMode, _, err := suite.probeMode()
		if err != nil || gotMode != mode {
			t.Fatalf("mode=%q got=%q err=%v", mode, gotMode, err)
		}
	}
	for _, mode := range []string{modelcheckprofile.EndpointModeChatJSON, modelcheckprofile.EndpointModeChatSSE, "images_json", "interactions_json"} {
		suite := Suite{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: mode, SupportedEndpointModes: []string{mode}, Adapter: AdapterOpenAIOAuthCodex}
		if _, _, err := suite.probeMode(); err == nil {
			t.Fatalf("OAuth Codex mode %q must fail closed", mode)
		}
	}
}

func TestRunSuitePropagatesStreamingEndpointModeToCoreRequests(t *testing.T) {
	transport := &streamModeTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "gpt-5.6-sol",
		Profile:      "quick",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesSSE,
		Tokenizer:    deterministicTokenizer{},
	}, time.Second)
	if err != nil || len(items) == 0 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if transport.requests != 8 || transport.nonStreaming != 0 {
		t.Fatalf("requests=%d nonStreaming=%d", transport.requests, transport.nonStreaming)
	}
	for _, item := range items {
		if item.Kind == "responses_stream" {
			if item.Status != "passed" || item.Evidence["outputMatches"] != true {
				t.Fatalf("independent stream evidence=%+v", item)
			}
			return
		}
	}
	t.Fatalf("independent stream item missing: %+v", items)
}

func TestRunSuiteStreamFailureStopsBeforeStructuredProbe(t *testing.T) {
	transport := &streamFailureTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "gpt-5.6-sol",
		Profile:      "quick",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesSSE,
		Tokenizer:    deterministicTokenizer{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if transport.requests != 2 {
		t.Fatalf("terminal stream failure must stop structured probe: requests=%d", transport.requests)
	}
	for _, item := range items {
		if item.Kind == "structured_output" {
			t.Fatalf("structured probe ran after terminal stream failure: %+v", items)
		}
		if item.Kind == "responses_stream" {
			if item.Status != "skipped" || item.Evidence["requestFailure"] != true {
				t.Fatalf("stream failure evidence=%+v", item)
			}
			return
		}
	}
	t.Fatalf("stream failure item missing: %+v", items)
}

type streamFailureTransport struct {
	requests int
}

func (t *streamFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(body), "STREAM-OK") {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"stream unavailable"}`)), Request: request}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.6-sol\",\"output_text\":\"OK-MODEL-CHECK\"}}\n\ndata: [DONE]\n")), Request: request}, nil
}

func TestRunSuiteUsesMappedUpstreamProtocolAndEndpointMode(t *testing.T) {
	transport := &mappedEndpointTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:             "https://example.test",
		Client:               &http.Client{Transport: transport},
		Model:                "gpt-5.6-terra",
		Profile:              "quick",
		Protocol:             modelcheckprofile.ProtocolOpenAIResponses,
		UpstreamProtocol:     modelcheckprofile.ProtocolOpenAIChat,
		EndpointMode:         modelcheckprofile.EndpointModeResponsesJSON,
		UpstreamEndpointMode: modelcheckprofile.EndpointModeChatJSON,
		Tokenizer:            deterministicTokenizer{},
	}, time.Second)
	if err != nil || len(items) == 0 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if transport.requests != 4 || transport.wrongPath != 0 || transport.wrongModel != 0 {
		t.Fatalf("mapped upstream request shape not preserved: %+v", transport)
	}
	for _, item := range items {
		if item.Kind == "token_integrity" {
			if item.Status != "skipped" || item.Evidence["notApplicable"] != true || item.Evidence["excludedFromScoring"] != true {
				t.Fatalf("mapped chat token evidence=%+v", item)
			}
			return
		}
	}
	t.Fatalf("mapped chat token scope item missing: %+v", items)
}

func TestRunSuiteQuickIncludesOneTokenIntegrityRound(t *testing.T) {
	transport := &mappedEndpointTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "gpt-5.6-terra",
		Profile:      "quick",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesJSON,
		Tokenizer:    deterministicTokenizer{},
		Retry:        RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if transport.requests != 7 {
		t.Fatalf("quick request count=%d want=7", transport.requests)
	}
	found := false
	for _, item := range items {
		if item.Kind == "token_integrity" {
			found = true
			if item.Evidence["requestCount"] != 3 {
				t.Fatalf("quick token evidence=%+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("quick items=%+v", items)
	}
}

func TestRunSuiteQuickIncludesCrossModelAndTrustedAggregate(t *testing.T) {
	targetTransport := &comparisonTransport{expectedAuthorization: "Bearer target", expectedModel: "gpt-5.6-sol"}
	comparisonTransport := &comparisonTransport{expectedAuthorization: "Bearer comparison", expectedModel: "gpt-5.6-terra"}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:                  "https://target.example",
		Headers:                   http.Header{"Authorization": []string{"Bearer target"}},
		Client:                    &http.Client{Transport: targetTransport},
		Model:                     "gpt-5.6-sol",
		Profile:                   "quick",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Tokenizer:                 deterministicTokenizer{},
		Comparison: &Suite{
			Endpoint:                  "https://comparison.example",
			Headers:                   http.Header{"Authorization": []string{"Bearer comparison"}},
			Client:                    &http.Client{Transport: comparisonTransport},
			Model:                     "gpt-5.6-terra",
			Profile:                   "quick",
			Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
			ProviderProtocolProfileID: "profile_openai_openai_v1",
			Tokenizer:                 deterministicTokenizer{},
		},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	foundTargetCross, foundComparisonCross, foundAggregate := false, false, false
	for _, item := range items {
		switch item.Kind {
		case "cross_model":
			foundTargetCross = true
		case "trusted_comparison.cross_model":
			foundComparisonCross = true
		case "trusted_comparison.comparison":
			foundAggregate = true
		}
	}
	if !foundTargetCross || !foundComparisonCross || !foundAggregate {
		t.Fatalf("quick trusted comparison items=%+v", items)
	}
	if targetTransport.requests != 7 || comparisonTransport.requests != 7 {
		t.Fatalf("quick trusted comparison must use core+token+cross for both accounts: target=%d comparison=%d", targetTransport.requests, comparisonTransport.requests)
	}
}

func TestQuickQualityScoreCountsFailedAndWarningItems(t *testing.T) {
	score, maxScore := quickQualityScore([]Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10},
		{Kind: "structured_output", Status: "failed", Score: 0, MaxScore: 20},
		{Kind: "tool_calling", Status: "warning", Score: 4, MaxScore: 30},
		{Kind: "cross_model", Status: "skipped", Score: 0, MaxScore: 40},
	})
	if score != 14 || maxScore != 60 {
		t.Fatalf("quick quality score=%d/%d, want 14/60 (failed and warning items must remain in the denominator)", score, maxScore)
	}
}

func TestRunSuiteOnlyRunsTokenAndIdentityForResponsesProfile(t *testing.T) {
	transport := &orderedProbeTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "claude-opus-5",
		Profile:      "full",
		ProviderCode: "anthropic",
		Protocol:     modelcheckprofile.ProtocolAnthropic,
		EndpointMode: modelcheckprofile.EndpointModeMessagesJSON,
		Tokenizer:    deterministicTokenizer{},
		ModelLimits:  deterministicLimits{},
		Retry:        RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	foundToken, foundIdentity := false, false
	for _, item := range items {
		switch unscopedKind(item.Kind) {
		case "token_integrity":
			foundToken = true
			if item.Status != "skipped" || item.Evidence["notApplicable"] != true || item.Evidence["excludedFromScoring"] != true {
				t.Fatalf("non-Responses token evidence=%+v", item)
			}
		case "identity_observation":
			foundIdentity = true
			if item.Status != "skipped" || item.Evidence["notApplicable"] != true || item.Evidence["excludedFromScoring"] != true {
				t.Fatalf("non-Responses identity evidence=%+v", item)
			}
		}
	}
	if !foundToken || !foundIdentity {
		t.Fatalf("non-Responses scope items missing: %+v", items)
	}
	for _, kind := range transport.kinds {
		if kind == "token" || kind == "identity" {
			t.Fatalf("non-Responses profile issued %s request: %v", kind, transport.kinds)
		}
	}
}

func TestRunSuiteCoreFailureStopsBeforeExtensionsAndKeepsEvidence(t *testing.T) {
	transport := &coreFailureTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "gpt-5.6-sol",
		Profile:      "quick",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesJSON,
		Tokenizer:    deterministicTokenizer{},
		Retry:        RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if transport.requests != 3 || len(items) != 2 || items[0].Kind != "protocol_basic" || items[1].Kind != "usage_shape" {
		t.Fatalf("requests=%d items=%+v", transport.requests, items)
	}
	if items[0].Evidence["success"] != false {
		t.Fatalf("failed core evidence=%+v", items[0])
	}
}

type coreFailureTransport struct {
	requests int
}

func (t *coreFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unavailable"}`)), Request: request}, nil
}

func TestRunSuiteFullOrdersBehaviorLongContextBeforeStability(t *testing.T) {
	transport := &orderedProbeTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "claude-opus-5",
		Profile:      "full",
		ProviderCode: "anthropic",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesJSON,
		Tokenizer:    deterministicTokenizer{},
		ModelLimits:  deterministicLimits{},
		Retry:        RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("full suite returned no items")
	}
	positions := map[string]int{}
	for index, kind := range transport.kinds {
		if _, ok := positions[kind]; !ok {
			positions[kind] = index
		}
	}
	if !(positions["behavior"] < positions["long_context"] && positions["long_context"] < positions["stability"]) {
		t.Fatalf("probe order=%v", transport.kinds)
	}
}

func TestRunSuiteStabilityFailureStopsTokenAndPreservesGateEvidence(t *testing.T) {
	transport := &stabilityFailureTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:     "https://example.test",
		Client:       &http.Client{Transport: transport},
		Model:        "claude-opus-5",
		Profile:      "full",
		ProviderCode: "anthropic",
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesJSON,
		Tokenizer:    deterministicTokenizer{},
		ModelLimits:  deterministicLimits{},
		Retry:        RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	foundToken := false
	for _, kind := range transport.kinds {
		foundToken = foundToken || kind == "token"
	}
	if foundToken {
		t.Fatalf("token probe must stop after terminal stability failure: %v", transport.kinds)
	}
	if transport.stabilityRequests != 3 {
		t.Fatalf("stability requests=%d", transport.stabilityRequests)
	}
	found := false
	for _, item := range items {
		if item.Kind == "stability" {
			found = true
			if item.Status != "skipped" || item.Evidence["terminalFailure"] != true {
				t.Fatalf("stability evidence=%+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("items=%+v", items)
	}
}

type stabilityFailureTransport struct {
	kinds             []string
	stabilityRequests int
}

func (t *stabilityFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	textBody := string(body)
	kind := "core"
	status := http.StatusOK
	output := "OK-MODEL-CHECK"
	switch {
	case strings.Contains(textBody, "VECTOR"):
		kind = "stability"
		t.stabilityRequests++
		status = http.StatusServiceUnavailable
	case strings.Contains(textBody, "Controlled token integrity probe"):
		kind = "token"
	}
	t.kinds = append(t.kinds, kind)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"claude-opus-5","output_text":"` + output + `","usage":{"input_tokens":2}}`)), Request: request}, nil
}

type orderedProbeTransport struct {
	kinds []string
}

func (t *orderedProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	textBody := string(body)
	kind := "core"
	switch {
	case strings.Contains(textBody, "Controlled token integrity probe"):
		kind = "token"
	case strings.Contains(textBody, "NEEDLE-"):
		kind = "long_context"
	case strings.Contains(textBody, "VECTOR"):
		kind = "stability"
	case strings.Contains(textBody, "QUARTZ") || strings.Contains(textBody, "并发控制") || strings.Contains(textBody, "ZETA"):
		kind = "behavior"
	}
	t.kinds = append(t.kinds, kind)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"claude-opus-5","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":2}}`)), Request: request}, nil
}

type mappedEndpointTransport struct {
	requests, wrongPath, wrongModel int
}

func (t *mappedEndpointTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	if request.URL.Path != "/v1/chat/completions" {
		t.wrongPath++
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Model != "gpt-5.6-terra" && payload.Model != "gpt-5.6-sol" {
		t.wrongModel++
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"` + payload.Model + `","choices":[{"message":{"content":"OK-MODEL-CHECK"}}],"usage":{"total_tokens":2}}`)), Request: request}, nil
}

type streamModeTransport struct {
	requests, nonStreaming int
}

func (t *streamModeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(body), `"stream":true`) {
		t.nonStreaming++
	}
	output := "OK-MODEL-CHECK"
	if strings.Contains(string(body), "STREAM-OK") {
		output = "STREAM-OK"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.6-sol\",\"output_text\":\"" + output + "\"}}\n\ndata: [DONE]\n")), Request: request}, nil
}

func TestRunTrustedComparisonKeepsOwnersDistinctWhenEndpointMatches(t *testing.T) {
	targetTransport := &comparisonTransport{expectedAuthorization: "Bearer target", expectedModel: "gpt-5.6-sol"}
	comparisonTransport := &comparisonTransport{expectedAuthorization: "Bearer comparison", expectedModel: "gpt-5.6-terra"}
	items, err := RunTrustedComparison(context.Background(), Suite{
		Endpoint:                  "https://shared.example",
		Headers:                   http.Header{"Authorization": []string{"Bearer target"}},
		Client:                    &http.Client{Transport: targetTransport},
		Model:                     "gpt-5.6-sol",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
	}, Suite{
		Endpoint:                  "https://shared.example",
		Headers:                   http.Header{"Authorization": []string{"Bearer comparison"}},
		Client:                    &http.Client{Transport: comparisonTransport},
		Model:                     "gpt-5.6-terra",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Status != "passed" || items[1].Status != "passed" {
		t.Fatalf("items=%#v", items)
	}
	if targetTransport.requests != 7 || comparisonTransport.requests != 7 {
		t.Fatalf("owners crossed or requests missing: target=%d comparison=%d", targetTransport.requests, comparisonTransport.requests)
	}
}

func TestRunTrustedComparisonRunsComparisonFullSuiteWithoutRecursion(t *testing.T) {
	targetTransport := &comparisonTransport{expectedAuthorization: "Bearer target", expectedModel: "gpt-5.6-sol"}
	comparisonTransport := &fullSuiteTrackingTransport{expectedAuthorization: "Bearer comparison", expectedModel: "gpt-5.6-terra", families: make(map[string]int)}
	items, err := RunTrustedComparison(context.Background(), Suite{
		Endpoint:                  "https://shared.example",
		Headers:                   http.Header{"Authorization": []string{"Bearer target"}},
		Client:                    &http.Client{Transport: targetTransport},
		Model:                     "gpt-5.6-sol",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:                 deterministicTokenizer{},
		ModelLimits:               deterministicLimits{},
	}, Suite{
		Endpoint:                  "https://shared.example",
		Headers:                   http.Header{"Authorization": []string{"Bearer comparison"}},
		Client:                    &http.Client{Transport: comparisonTransport},
		Model:                     "gpt-5.6-terra",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		SupportedModels:           []string{"gpt-5.6-terra"},
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		// The comparison target deliberately omits owner-wide snapshots. The
		// trusted runner must copy the target's snapshots before its full suite.
		// A nested comparison must be ignored by the full comparison run.
		Comparison: &Suite{Endpoint: "https://must-not-run.example", Model: "gpt-5.6-luna", Protocol: modelcheckprofile.ProtocolOpenAIResponses, Profile: "full"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 4 || items[len(items)-2].Kind != "distribution_similarity" || items[len(items)-1].Kind != "comparison" {
		t.Fatalf("trusted result=%+v", items)
	}
	comparisonFamilies := 0
	for _, item := range items {
		if strings.HasPrefix(item.Kind, "trusted_comparison.") {
			comparisonFamilies++
		}
	}
	if comparisonFamilies < 8 {
		t.Fatalf("comparison full-suite evidence was discarded: items=%+v", items)
	}
	for _, family := range []string{"behavior", "long_context", "stability", "token_integrity", "identity", "juice"} {
		if comparisonTransport.families[family] == 0 {
			t.Fatalf("comparison full suite did not run %s: requests=%d families=%v", family, comparisonTransport.requests, comparisonTransport.families)
		}
	}
	if comparisonTransport.requests < 30 {
		t.Fatalf("comparison full suite request count=%d", comparisonTransport.requests)
	}
}

type comparisonTransport struct {
	expectedAuthorization string
	expectedModel         string
	requests              int
}

type fullSuiteTrackingTransport struct {
	expectedAuthorization string
	expectedModel         string
	requests              int
	families              map[string]int
}

func (t *fullSuiteTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	if got := request.Header.Get("Authorization"); got != t.expectedAuthorization {
		return nil, fmt.Errorf("authorization=%q want=%q", got, t.expectedAuthorization)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Model != t.expectedModel {
		return nil, fmt.Errorf("model=%q want=%q", payload.Model, t.expectedModel)
	}
	textBody := string(body)
	switch {
	case strings.Contains(textBody, "Controlled token integrity probe"):
		t.families["token_integrity"]++
	case strings.Contains(textBody, "NEEDLE-"):
		t.families["long_context"]++
	case strings.Contains(textBody, "VECTOR"):
		t.families["stability"]++
	case strings.Contains(textBody, "CANARY-"):
		t.families["identity"]++
	case strings.Contains(textBody, "Valid Channels") || strings.Contains(textBody, "Reply with exactly: 32") || strings.Contains(textBody, "Reply with exactly: 48"):
		t.families["juice"]++
	case strings.Contains(textBody, "QUARTZ") || strings.Contains(textBody, "并发") || strings.Contains(textBody, "ZETA"):
		t.families["behavior"]++
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"` + t.expectedModel + `","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":10,"total_tokens":2}}`)), Request: request}, nil
}

func (t *comparisonTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	if got := request.Header.Get("Authorization"); got != t.expectedAuthorization {
		return nil, fmt.Errorf("authorization=%q want=%q", got, t.expectedAuthorization)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Model != t.expectedModel {
		return nil, fmt.Errorf("model=%q want=%q", payload.Model, t.expectedModel)
	}
	output := mustJSONTextForDistribution(body)
	if strings.Contains(string(body), "CROSS-MODEL-OK") {
		encoded, _ := json.Marshal("CROSS-MODEL-OK")
		output = string(encoded)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"` + t.expectedModel + `","output_text":` + output + `,"usage":{"total_tokens":2}}`)), Request: request}, nil
}

func mustJSONTextForDistribution(body []byte) string {
	output := "OK-MODEL-CHECK"
	text := string(body)
	switch {
	case strings.Contains(text, "json_reasoning") || strings.Contains(text, "SIGMA"):
		output = `{"result":83,"tag":"SIGMA"}`
	case strings.Contains(text, "code_judgement") || strings.Contains(text, "const xs"):
		output = "ALPHA 4-7"
	case strings.Contains(text, "refusal_boundary") || strings.Contains(text, "绕过他人"):
		output = "DELTA 不能提供"
	case strings.Contains(text, "sequence_transform") || strings.Contains(text, "从小到大"):
		output = "THETA 4|7|9"
	case strings.Contains(text, "table_extract") || strings.Contains(text, "北区"):
		output = "IOTA 17 23"
	case strings.Contains(text, "style_compact") || strings.Contains(text, "向量数据库"):
		output = "向量数据库召回率衡量结果相关内容被找回的比例"
	}
	encoded, _ := json.Marshal(output)
	return string(encoded)
}

type countingTransport struct {
	base     http.RoundTripper
	requests int
}

func (t *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	return t.base.RoundTrip(request)
}

func TestRunSelfCrossModelUsesPairedModelOnSameEndpoint(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		requested = append(requested, request.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"` + request.Model + `","output_text":"CROSS-MODEL-OK","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	first := Result{Success: true, ObservedModel: "gpt-5.6-sol", Output: "OK-MODEL-CHECK"}
	item, err := RunSelfCrossModel(context.Background(), Suite{Endpoint: server.URL, Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses}, first, time.Second)
	if err != nil || item.Status != "passed" || len(requested) != 1 || requested[0] != "gpt-5.6-terra" {
		t.Fatalf("item=%+v requested=%v err=%v", item, requested, err)
	}
}

func mustJSONText(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
