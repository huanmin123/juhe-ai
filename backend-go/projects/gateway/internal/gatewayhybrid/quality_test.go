package gatewayhybrid

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func TestShouldTriggerHybridQualityInspectionBranches(t *testing.T) {
	base := func() QualityTriggerInput {
		return QualityTriggerInput{
			View:             &GatewayRequestView{Method: "POST", Path: "/x"},
			Config:           hybridConfig(),
			TargetRoute:      routestrategiesRoute(1, 5),
			ResponseBodyText: "content",
		}
	}
	tests := []struct {
		name        string
		mutate      func(input QualityTriggerInput) QualityTriggerInput
		mutateConf  func(config *routestrategies.HybridRoutingConfig)
		wantTrigger bool
		wantReason  string
	}{
		{
			name:        "disabled",
			mutateConf:  func(config *routestrategies.HybridRoutingConfig) { config.QualityInspection.Enabled = false },
			wantTrigger: false, wantReason: "quality_inspection_disabled",
		},
		{
			name: "always_for_hybrid",
			mutateConf: func(config *routestrategies.HybridRoutingConfig) {
				config.QualityInspection.TriggerMode = "always_for_hybrid"
			},
			wantTrigger: true, wantReason: "always_for_hybrid",
		},
		{
			name: "quality_first_only matched",
			mutateConf: func(config *routestrategies.HybridRoutingConfig) {
				config.QualityInspection.TriggerMode = "quality_first_only"
			},
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.Config.QualityPreference = "quality_first"
				return input
			},
			wantTrigger: true, wantReason: "quality_first_preference",
		},
		{
			name: "quality_first_only not matched",
			mutateConf: func(config *routestrategies.HybridRoutingConfig) {
				config.QualityInspection.TriggerMode = "quality_first_only"
			},
			wantTrigger: false, wantReason: "quality_first_only_not_matched",
		},
		{
			name: "risk_based quality_first preference",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.Config.QualityPreference = "quality_first"
				return input
			},
			wantTrigger: true, wantReason: "quality_first_preference",
		},
		{
			name: "strict output via response_format",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.View.ParsedBody = mustParseObject(t, `{"response_format":{"type":"json_object"}}`)
				return input
			},
			wantTrigger: true, wantReason: "strict_output_requirement",
		},
		{
			name: "strict output via tools",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.View.ParsedBody = mustParseObject(t, `{"tools":[]}`)
				return input
			},
			wantTrigger: true, wantReason: "strict_output_requirement",
		},
		{
			name: "strict output via tool_choice",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.View.ParsedBody = mustParseObject(t, `{"tool_choice":"auto"}`)
				return input
			},
			wantTrigger: true, wantReason: "strict_output_requirement",
		},
		{
			name: "strict output via body state",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.View.BodyState = &RequestBodyState{StrictOutputRequirement: true}
				return input
			},
			wantTrigger: true, wantReason: "strict_output_requirement",
		},
		{
			name: "strict output via image generation state",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.View.BodyState = &RequestBodyState{ImageGenerationForced: boolPtr(true)}
				return input
			},
			wantTrigger: true, wantReason: "strict_output_requirement",
		},
		{
			name: "empty response body",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.ResponseBodyText = "   \n"
				return input
			},
			wantTrigger: true, wantReason: "empty_response_body",
		},
		{
			name:        "low route level",
			wantTrigger: true, wantReason: "low_or_mid_route_level",
		},
		{
			name: "max trigger level boundary",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.TargetRoute = routestrategiesRoute(6, 10)
				return input
			},
			wantTrigger: true, wantReason: "low_or_mid_route_level",
		},
		{
			name: "above max trigger level",
			mutate: func(input QualityTriggerInput) QualityTriggerInput {
				input.TargetRoute = routestrategiesRoute(7, 10)
				return input
			},
			wantTrigger: false, wantReason: "low_risk_request",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input := base()
			if testCase.mutateConf != nil {
				testCase.mutateConf(input.Config)
			}
			if testCase.mutate != nil {
				input = testCase.mutate(input)
			}
			trigger := ShouldTriggerHybridQualityInspection(input)
			if trigger.Triggered != testCase.wantTrigger || trigger.Reason != testCase.wantReason {
				t.Fatalf("trigger = %+v, want (%v, %s)", trigger, testCase.wantTrigger, testCase.wantReason)
			}
		})
	}
}

func routestrategiesRoute(minLevel, maxLevel int) routestrategies.HybridLevelRoute {
	return routestrategies.HybridLevelRoute{MinLevel: minLevel, MaxLevel: maxLevel, TargetModel: "m", Enabled: true}
}

func TestParseHybridQualityResponseOutcomes(t *testing.T) {
	valid := ParseNonStreamJSONBody(
		`{"choices":[{"message":{"content":"{\"pass\":false,\"score\":85,\"confidence\":0.7,\"failureType\":\"low_quality\",\"reason\":\"不完整\",\"retryRecommendation\":\"upgrade_next_level\"}"}}]}`,
		"application/json")
	parsed, err := ParseHybridQualityResponse(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Pass || parsed.Score != 85 || parsed.FailureType != "low_quality" || parsed.RetryRecommendation != "upgrade_next_level" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Reason == nil || *parsed.Reason != "不完整" {
		t.Fatalf("reason = %v", parsed.Reason)
	}

	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{"invalid status", "", "质量评分模型未返回合法 JSON"},
		{"no json content", `{"choices":[{"message":{"content":"nothing"}}]}`, "质量评分模型未返回 JSON"},
		{"unparseable json", `{"choices":[{"message":{"content":"{bad"}}]}`, "质量评分模型未返回 JSON"},
		{"array payload", `{"choices":[{"message":{"content":"[]"}}]}`, "质量评分模型未返回 JSON"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseHybridQualityResponse(ParseNonStreamJSONBody(testCase.body, "application/json"))
			if err == nil || err.Error() != testCase.wantError {
				t.Fatalf("error = %v, want %s", err, testCase.wantError)
			}
		})
	}
}

func TestParseHybridQualityResponseScoreBoundaries(t *testing.T) {
	// Score clamps to 0..100; pass defaults false; retryRecommendation
	// normalizes to upgrade_next_level for unknown values.
	tests := []struct {
		name       string
		content    string
		wantPass   bool
		wantScore  float64
		wantRetry  string
		wantDanger bool
	}{
		{"score above 100 clamps", `{"pass":true,"score":150}`, true, 100, "accept", false},
		{"score below 0 clamps", `{"pass":false,"score":-3}`, false, 0, "upgrade_next_level", false},
		{"missing score with pass", `{"pass":true}`, true, 100, "accept", false},
		{"missing score failing", `{"pass":false}`, false, 0, "upgrade_next_level", false},
		{"invalid retry falls back", `{"pass":false,"retryRecommendation":"whatever"}`, false, 0, "upgrade_next_level", false},
		{"unsafe failure type", `{"pass":false,"score":10,"failureType":"unsafe_or_policy"}`, false, 10, "upgrade_next_level", true},
		{"unknown failure type dropped", `{"pass":false,"score":10,"failureType":"made_up"}`, false, 10, "upgrade_next_level", false},
		{"pass overrides retry", `{"pass":true,"retryRecommendation":"return_error"}`, true, 100, "accept", false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := ParseNonStreamJSONBody(
				`{"choices":[{"message":{"content":"`+jsonEscape(t, testCase.content)+`"}}]}`,
				"application/json")
			parsed, err := ParseHybridQualityResponse(body)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if parsed.Pass != testCase.wantPass || parsed.Score != testCase.wantScore || parsed.RetryRecommendation != testCase.wantRetry {
				t.Fatalf("parsed = %+v", parsed)
			}
			if (parsed.FailureType == "unsafe_or_policy") != testCase.wantDanger {
				t.Fatalf("failureType = %+v", parsed)
			}
		})
	}
}

func TestResolveHybridQualityAction(t *testing.T) {
	config := hybridConfig().QualityInspection
	tests := []struct {
		name  string
		result *HybridQualityScoreResult
		want  string
	}{
		{"pass accepts", &HybridQualityScoreResult{Pass: true}, "accept"},
		{"unsafe returns error", &HybridQualityScoreResult{Pass: false, HasFailureType: true, FailureType: "unsafe_or_policy", RetryRecommendation: "upgrade_next_level"}, "return_error"},
		{"return_error recommendation", &HybridQualityScoreResult{Pass: false, RetryRecommendation: "return_error"}, "return_error"},
		{"failure action fallback", &HybridQualityScoreResult{Pass: false, RetryRecommendation: "upgrade_next_level"}, "repair_then_upgrade"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ResolveHybridQualityAction(testCase.result, config); got != testCase.want {
				t.Fatalf("action = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestInspectHybridGatewayQualityFlows(t *testing.T) {
	now := time.Now()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys"}
	config := hybridConfig()

	happyBody := `{"choices":[{"message":{"content":"{\"pass\":false,\"score\":20,\"failureType\":\"low_quality\",\"reason\":\"不完整\",\"retryRecommendation\":\"upgrade_next_level\"}"}}]}`

	t.Run("not triggered returns pass", func(t *testing.T) {
		dispatcher := &mockDispatcher{}
		service := NewQualityInspectionService(testClock(&now), dispatcher, &mockRecorder{}, nil)
		outcome := service.Inspect(context.Background(), QualityInspectInput{
			View: &GatewayRequestView{Method: "POST", Path: "/x"}, APIKeyRecord: record, Config: config,
			Scoring: HybridScoringResult{Level: 3}, TargetRoute: routestrategiesRoute(7, 10),
			TargetModel: "gpt-5", ResponseBodyText: "ok", Endpoint: "/ep",
		})
		if outcome.Triggered || !outcome.Pass || outcome.TriggerReason != "low_risk_request" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if dispatcher.dispatchCount() != 0 {
			t.Fatal("must not dispatch when not triggered")
		}
	})

	t.Run("success inspection records usage and resolves action", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			success: successDispatch("quality-1", "group-q", 200, happyBody, gatewayprotoEmptyUsage()),
		}}}
		recorder := &mockRecorder{}
		service := NewQualityInspectionService(testClock(&now), dispatcher, recorder, nil)
		outcome := service.Inspect(context.Background(), QualityInspectInput{
			View: &GatewayRequestView{Method: "POST", Path: "/x"}, APIKeyRecord: record, Config: config,
			Scoring: HybridScoringResult{Level: 3}, TargetRoute: routestrategiesRoute(1, 5),
			TargetModel: "gpt-5-mini", ResponseBodyText: "answer", Endpoint: "/ep",
		})
		if !outcome.Triggered || outcome.Pass || outcome.ActualAction != "repair_then_upgrade" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if outcome.QualityAccountID != "quality-1" || outcome.TriggerReason != "low_or_mid_route_level" {
			t.Fatalf("outcome = %+v", outcome)
		}
		first := dispatcher.inputs[0]
		if first.TrafficSource != "hybrid_quality_scoring" || first.NoAccountErrorCode != "no_quality_scoring_account" {
			t.Fatalf("dispatch input = %+v", first)
		}
		if first.DispatchErrorMessage != "混合路由质量评分模型调用失败" || first.ResponseTooLargeMessage != "混合路由质量评分响应超过保护上限" {
			t.Fatalf("dispatch messages = %s/%s", first.DispatchErrorMessage, first.ResponseTooLargeMessage)
		}
		if !strings.Contains(NodeJSONStringify(first.Body), `"max_tokens":220`) {
			t.Fatal("quality body must use max_tokens 220")
		}
		if len(recorder.records) != 1 || !recorder.records[0].Success || recorder.records[0].TrafficSource != "hybrid_quality_scoring" {
			t.Fatalf("records = %+v", recorder.records)
		}
		if recorder.records[0].Endpoint != "/ep#hybrid-quality-scoring" {
			t.Fatalf("endpoint = %s", recorder.records[0].Endpoint)
		}
		if len(dispatcher.finishLog) != 1 || !dispatcher.finishLog[0].success {
			t.Fatalf("finishLog = %+v", dispatcher.finishLog)
		}
	})

	t.Run("dispatch failure pass_through", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			failure: failureDispatch("no_quality_scoring_account", "混合路由绑定分组池没有可用质量评分账户", "", "", 0, false),
		}}}
		service := NewQualityInspectionService(testClock(&now), dispatcher, &mockRecorder{}, nil)
		outcome := service.Inspect(context.Background(), QualityInspectInput{
			View: &GatewayRequestView{Method: "POST", Path: "/x"}, APIKeyRecord: record, Config: config,
			Scoring: HybridScoringResult{Level: 3}, TargetRoute: routestrategiesRoute(1, 5),
			TargetModel: "gpt-5-mini", ResponseBodyText: "answer", Endpoint: "/ep",
		})
		if !outcome.Triggered || outcome.TriggerReason != "quality_scoring_unavailable" || !outcome.Pass {
			t.Fatalf("outcome = %+v", outcome)
		}
		if outcome.ActualAction != "pass_through" || outcome.ErrorCode != "no_quality_scoring_account" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if outcome.Result == nil || outcome.Result.Pass || outcome.Result.Score != 0 || outcome.Result.RetryRecommendation != "return_error" {
			t.Fatalf("result = %+v", outcome.Result)
		}
		if outcome.Result.Reason == nil || *outcome.Result.Reason != "混合路由绑定分组池没有可用质量评分账户" {
			t.Fatalf("reason = %v", outcome.Result.Reason)
		}
	})

	t.Run("dispatch failure return_error", func(t *testing.T) {
		config2 := hybridConfig()
		config2.QualityInspection.UnavailableAction = "return_error"
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			failure: failureDispatch("hybrid_quality_scoring_failed", "boom", "q-acct", "group-q", 500, true),
		}}}
		recorder := &mockRecorder{}
		service := NewQualityInspectionService(testClock(&now), dispatcher, recorder, nil)
		outcome := service.Inspect(context.Background(), QualityInspectInput{
			View: &GatewayRequestView{Method: "POST", Path: "/x"}, APIKeyRecord: record, Config: config2,
			Scoring: HybridScoringResult{Level: 3}, TargetRoute: routestrategiesRoute(1, 5),
			TargetModel: "gpt-5-mini", ResponseBodyText: "answer", Endpoint: "/ep",
		})
		if outcome.Pass || outcome.ActualAction != "return_error" || outcome.QualityAccountID != "q-acct" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if len(recorder.records) != 1 || recorder.records[0].Success {
			t.Fatalf("records = %+v", recorder.records)
		}
	})

	t.Run("invalid scoring response is unavailable", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			success: successDispatch("q-acct", "group-q", 200, `garbage`, gatewayprotoEmptyUsage()),
		}}}
		service := NewQualityInspectionService(testClock(&now), dispatcher, &mockRecorder{}, nil)
		outcome := service.Inspect(context.Background(), QualityInspectInput{
			View: &GatewayRequestView{Method: "POST", Path: "/x"}, APIKeyRecord: record, Config: config,
			Scoring: HybridScoringResult{Level: 3}, TargetRoute: routestrategiesRoute(1, 5),
			TargetModel: "gpt-5-mini", ResponseBodyText: "answer", Endpoint: "/ep",
		})
		if outcome.TriggerReason != "quality_scoring_unavailable" || outcome.ErrorCode != "hybrid_quality_scoring_failed" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if outcome.ErrorMessage != "质量评分模型未返回合法 JSON" {
			t.Fatalf("errorMessage = %s", outcome.ErrorMessage)
		}
		if len(dispatcher.finishLog) != 1 || dispatcher.finishLog[0].success {
			t.Fatalf("finishLog = %+v", dispatcher.finishLog)
		}
	})
}

func TestBuildHybridQualityRequestBodyMatchesNodeJSON(t *testing.T) {
	config := hybridConfig().QualityInspection
	body := BuildHybridQualityRequestBody(config, "ctx")
	raw := NodeJSONStringify(body)
	if !strings.HasPrefix(raw, `{"model":"gpt-scoring","stream":false,"temperature":0,"max_tokens":220,"messages":[{"role":"system","content":"你是网关响应质量评分器`) {
		t.Fatalf("quality body prefix mismatch: %.160s", raw)
	}
	if !strings.HasSuffix(raw, `,{"role":"user","content":"ctx"}]}`) {
		t.Fatalf("quality body suffix mismatch")
	}
}

func TestBuildHybridQualityContextTruncatedVariant(t *testing.T) {
	view := &GatewayRequestView{Method: "POST", Path: "/x", OriginalModel: "m", OriginalModelPresent: true}
	scoring := HybridScoringResult{Level: 3, Confidence: floatPtr(0.4), Reason: strPtr("中等"), Defaulted: true}
	small := QualityInspectInput{
		View: view, Config: hybridConfig(), Scoring: scoring,
		TargetModel: "gpt-5-mini", ResponseBodyText: "resp",
	}
	requestBody := mustParseObject(t, `{"prompt":"hi"}`)
	contextText := buildHybridQualityContext(small, requestBody, "low_or_mid_route_level")
	if !strings.HasPrefix(contextText, `{"method":"POST","path":"/x","originalOrCurrentModel":"m","targetModel":"gpt-5-mini","triggerReason":"low_or_mid_route_level","routeScoring":{"level":3,"confidence":0.4,"reason":"中等","defaulted":true},"request":{"prompt":"hi"},"response":"resp"}`) {
		t.Fatalf("context = %s", contextText)
	}
	// Missing confidence/reason drop from routeScoring.
	scoring2 := HybridScoringResult{Level: 4, Defaulted: false}
	contextText2 := buildHybridQualityContext(QualityInspectInput{
		View: view, Config: hybridConfig(), Scoring: scoring2,
		TargetModel: "t", ResponseBodyText: "r",
	}, nil, "empty_response_body")
	if !strings.Contains(contextText2, `"routeScoring":{"level":4,"defaulted":false}`) {
		t.Fatalf("routeScoring = %s", contextText2)
	}
	// Without a body state the request key is dropped (JS undefined).
	if strings.Contains(contextText2, `"request":`) || !strings.HasSuffix(contextText2, `"response":"r"}`) {
		t.Fatalf("summary context = %s", contextText2)
	}
}
