package gatewayhybrid

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildHybridScoringRequestBodyMatchesNodeJSON(t *testing.T) {
	body := BuildHybridScoringRequestBody("gpt-scoring", "ctx-payload")
	raw := NodeJSONStringify(body)
	// Byte-exact shape contract: key order, temperature 0, max_tokens 240.
	if !strings.HasPrefix(raw, `{"model":"gpt-scoring","stream":false,"temperature":0,"max_tokens":240,"messages":[{"role":"system","content":"你是网关请求难度评分器`) {
		t.Fatalf("scoring body prefix mismatch: %.160s", raw)
	}
	if !strings.HasSuffix(raw, `,{"role":"user","content":"ctx-payload"}]}`) {
		t.Fatalf("scoring body suffix mismatch: %s", raw[len(raw)-80:])
	}
	// The system prompt carries the whole difficulty-scorer instruction set.
	if !strings.Contains(raw, "只输出 JSON：{\\\"level\\\":数字,\\\"confidence\\\":0到1,\\\"reason\\\":\\\"一句话\\\",\\\"factors\\\":[\\\"短标签\\\"]}。\"") {
		t.Fatal("scoring system prompt JSON instruction missing")
	}
	if !strings.Contains(raw, "不要按固定关键词、业务领域、技术栈、文件名、任务名称或题型机械分级") {
		t.Fatal("scoring system prompt anti-keyword line missing")
	}
}

func TestParseHybridScoringResponseOutcomes(t *testing.T) {
	fencedContent := "```json\n{\"level\":7,\"confidence\":0.8,\"reason\":\"复杂推理\",\"factors\":[\"多步推理\",\"严格格式\"]}\n```"
	validBody := NonStreamJSONBody{Status: "valid", Value: mustParseObject(t,
		`{"choices":[{"finish_reason":"stop","message":{"content":"`+jsonEscape(t, fencedContent)+`","reasoning_content":""}}]}`)}
	parsed, err := ParseHybridScoringResponse(validBody)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clampLevelFromAny(parsed.level) != 7 || parsed.confidence == nil || *parsed.confidence != 0.8 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.reason == nil || *parsed.reason != "复杂推理" {
		t.Fatalf("reason = %v", parsed.reason)
	}
	if len(parsed.factors) != 2 || parsed.factors[0] != "多步推理" {
		t.Fatalf("factors = %v", parsed.factors)
	}

	tests := []struct {
		name        string
		body        string
		contentType string
		wantError   string
	}{
		{
			name:      "invalid body status",
			body:      "",
			wantError: "混合评分模型未返回合法 JSON",
		},
		{
			name:      "non json status",
			body:      "hello world",
			wantError: "混合评分模型未返回合法 JSON",
		},
		{
			name:        "not_json content type without braces",
			body:        "hello",
			contentType: "text/plain",
			wantError:   "混合评分模型未返回合法 JSON",
		},
		{
			name:      "no json in content",
			body:      `{"choices":[{"message":{"content":"no markers here"}}]}`,
			wantError: "评分模型未返回 JSON",
		},
		{
			name:      "reasoning only without finish length",
			body:      `{"choices":[{"finish_reason":"stop","message":{"reasoning_content":"思考中"}}]}`,
			wantError: "评分模型只返回思考内容，未产生 JSON",
		},
		{
			name:      "reasoning only with finish length",
			body:      `{"choices":[{"finish_reason":"length","message":{"reasoning_content":"思考中"}}]}`,
			wantError: "评分模型只返回思考内容且达到输出上限，未产生 JSON",
		},
		{
			name:      "invalid level",
			body:      `{"choices":[{"message":{"content":"{\"level\":\"abc\"}"}}]}`,
			wantError: "评分模型返回的 level 无效",
		},
		{
			name:      "missing level",
			body:      `{"choices":[{"message":{"content":"{}"}}]}`,
			wantError: "评分模型返回的 level 无效",
		},
		{
			name:      "reasoning with blank content falls back to reasoning error",
			body:      `{"choices":[{"message":{"content":"   ","reasoning_content":"  "}}]}`,
			wantError: "评分模型未返回 JSON",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseHybridScoringResponse(ParseNonStreamJSONBody(testCase.body, testCase.contentType))
			if err == nil || err.Error() != testCase.wantError {
				t.Fatalf("error = %v, want %s", err, testCase.wantError)
			}
		})
	}
}

func mustParseObject(t *testing.T, source string) *OrderedJSON {
	t.Helper()
	parsed, err := ParseJSONOrdered([]byte(source))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	object, ok := parsed.(*OrderedJSON)
	if !ok {
		t.Fatalf("fixture is not an object: %s", source)
	}
	return object
}

// jsonEscape renders text as the body of a JSON string (NodeJSONStringify).
func jsonEscape(t *testing.T, text string) string {
	t.Helper()
	rendered := NodeJSONStringify(text)
	return rendered[1 : len(rendered)-1]
}

func TestParseHybridScoringResponseEdgeValues(t *testing.T) {
	// Confidence clamps to [0,1]; numeric strings coerce; factors dedupe/cap.
	body := NonStreamJSONBody{Status: "valid", Value: mustParseObject(t,
		`{"choices":[{"message":{"content":"{\"level\":\"11\",\"confidence\":2,\"reason\":\"r\",\"factors\":[\" a \",\"a\",\"\",\"b\"]}"}}]}`)}
	parsed, err := ParseHybridScoringResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clampLevelFromAny(parsed.level) != 10 {
		t.Fatalf("level clamp = %d", clampLevelFromAny(parsed.level))
	}
	if parsed.confidence == nil || *parsed.confidence != 1 {
		t.Fatalf("confidence = %v", parsed.confidence)
	}
	if len(parsed.factors) != 2 || parsed.factors[0] != "a" || parsed.factors[1] != "b" {
		t.Fatalf("factors = %v", parsed.factors)
	}

	// JSON null confidence coerces to 0 (Number(null)=0), missing stays undefined.
	nullBody := NonStreamJSONBody{Status: "valid", Value: mustParseObject(t,
		`{"choices":[{"message":{"content":"{\"level\":3,\"confidence\":null}"}}]}`)}
	nullParsed, err := ParseHybridScoringResponse(nullBody)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nullParsed.confidence == nil || *nullParsed.confidence != 0 {
		t.Fatalf("null confidence = %v", nullParsed.confidence)
	}
	missingBody := NonStreamJSONBody{Status: "valid", Value: mustParseObject(t,
		`{"choices":[{"message":{"content":"{\"level\":3}"}}]}`)}
	missingParsed, err := ParseHybridScoringResponse(missingBody)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if missingParsed.confidence != nil {
		t.Fatalf("missing confidence = %v", missingParsed.confidence)
	}
}

func TestExtractJSONObjectText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"fenced", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"embedded", "prefix {\"a\":1} suffix", `{"a":1}`},
		{"nested braces", `pre {"a":{"b":2}} post`, `{"a":{"b":2}}`},
		{"none", "no markers", ""},
		{"unbalanced", "{oops", ""},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ExtractJSONObjectText(testCase.text); got != testCase.want {
				t.Fatalf("ExtractJSONObjectText = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestBuildHybridScoringContextSanitization(t *testing.T) {
	view := &GatewayRequestView{
		Method:               "POST",
		Path:                 "/v1/chat/completions",
		OriginalModel:        "gpt-5",
		OriginalModelPresent: true,
	}
	body := mustParseObject(t, `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"n":5,"ok":true,"nothing":null}`)
	contextText := buildHybridScoringContext(view, body)
	want := `{"method":"POST","path":"/v1/chat/completions","originalModel":"gpt-5","body":{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"n":5,"ok":true,"nothing":null}}`
	if contextText != want {
		t.Fatalf("context = %s\nwant      %s", contextText, want)
	}
}

func TestBuildHybridScoringContextStringTruncation(t *testing.T) {
	view := &GatewayRequestView{Method: "POST", Path: "/x"}
	long := strings.Repeat("a", 5000)
	body := NewOrderedJSON()
	body.Set("text", long)
	contextText := buildHybridScoringContext(view, body)
	if !strings.Contains(contextText, "a") || len(contextText) > 20000 {
		t.Fatalf("context size unexpected: %d", len(contextText))
	}
	if !strings.HasSuffix(contextText, `...[truncated]"}}`) {
		t.Fatalf("truncation marker missing: ...%s", contextText[len(contextText)-40:])
	}
	// 4096 UTF-16 units are kept from the 5000-char string.
	if !strings.Contains(contextText, strings.Repeat("a", 4096)) {
		t.Fatal("expected 4096 kept chars")
	}
}

func TestBuildHybridScoringContextDepthAndArrayCaps(t *testing.T) {
	view := &GatewayRequestView{Method: "POST", Path: "/x"}
	// 60 array items: 50 kept + truncation marker.
	body := NewOrderedJSON()
	array := []any{}
	for index := 0; index < 60; index++ {
		array = append(array, float64(index))
	}
	body.Set("arr", array)
	contextText := buildHybridScoringContext(view, body)
	if !strings.Contains(contextText, "[10 items truncated]") {
		t.Fatalf("array truncation marker missing: %s", contextText)
	}
	// Depth 9 object: nested payload becomes [truncated].
	// Eight nested object levels: the level-8 container hits the depth cap.
	leaf := NewOrderedJSON()
	leaf.Set("l8", "deep")
	cursor := leaf
	for level := 7; level >= 1; level-- {
		parent := NewOrderedJSON()
		parent.Set("l"+strconv.Itoa(level), cursor)
		cursor = parent
	}
	body2 := NewOrderedJSON()
	body2.Set("deep", cursor)
	contextText2 := buildHybridScoringContext(view, body2)
	if !strings.Contains(contextText2, "[truncated]") {
		t.Fatalf("depth truncation missing: %s", contextText2)
	}
}

func TestBuildHybridScoringContextBodyStateFallback(t *testing.T) {
	view := &GatewayRequestView{
		Method: "POST",
		Path:   "/x",
		BodyState: &RequestBodyState{
			RawBodyBytes:    4096,
			ContentType:     "application/json",
			JSONParseStatus: "invalid_json",
		},
	}
	contextText := buildHybridScoringContext(view, nil)
	want := `{"method":"POST","path":"/x","body":{"_gatewayBody":{"rawBodyBytes":4096,"contentType":"application/json","jsonParseStatus":"invalid_json","omittedReason":"body_not_available"}}}`
	if contextText != want {
		t.Fatalf("context = %s\nwant      %s", contextText, want)
	}
	// Oversized raw body switches the omitted reason.
	view.BodyState.RawBodyBytes = 200 * 1024
	contextText = buildHybridScoringContext(view, nil)
	if !strings.Contains(contextText, `"omittedReason":"raw_body_exceeds_hybrid_scoring_parse_limit"`) {
		t.Fatalf("oversized omittedReason missing: %s", contextText)
	}
	// No state at all renders body:null.
	contextText = buildHybridScoringContext(&GatewayRequestView{Method: "POST", Path: "/x"}, nil)
	if contextText != `{"method":"POST","path":"/x","body":null}` {
		t.Fatalf("context = %s", contextText)
	}
}

func TestBuildHybridScoringCacheKeyDeterminismAndSensitivity(t *testing.T) {
	view := &GatewayRequestView{Method: "POST", Path: "/v1/x", RawBody: []byte(`{"model":"m"}`), ConversationKey: "conv"}
	record := APIKeyRecord{ID: "key-1", SystemAccountID: "sys-1"}
	config := hybridConfig()
	first := buildHybridScoringCacheKey(view, record, config, "/endpoint", "ctx")
	second := buildHybridScoringCacheKey(view, record, config, "/endpoint", "ctx")
	if first == "" || first != second {
		t.Fatalf("cache key not deterministic: %s vs %s", first, second)
	}
	if changed := buildHybridScoringCacheKey(view, record, config, "/endpoint", "other"); changed == first {
		t.Fatal("context digest should change the key")
	}
	if changed := buildHybridScoringCacheKey(&GatewayRequestView{Method: "GET", Path: "/v1/x"}, record, config, "/endpoint", "ctx"); changed == first {
		t.Fatal("method should change the key")
	}
	disabled := hybridConfig()
	disabled.ScoringCacheEnabled = false
	if key := buildHybridScoringCacheKey(view, record, disabled, "/endpoint", "ctx"); key != "" {
		t.Fatalf("disabled cache key = %s", key)
	}
	zeroTTL := hybridConfig()
	zeroTTL.ScoringCacheTTLSeconds = 0
	if key := buildHybridScoringCacheKey(view, record, zeroTTL, "/endpoint", "ctx"); key != "" {
		t.Fatalf("zero ttl cache key = %s", key)
	}
}

func newScoringServiceWith(clock Clock, dispatcher AuxiliaryDispatcher, recorder UsageRecorder, shared SharedJSONCache) *ScoringService {
	return NewScoringService(clock, dispatcher, recorder, shared, nil)
}

func scoringView() *GatewayRequestView {
	return &GatewayRequestView{
		Method:               "POST",
		Path:                 "/v1/chat/completions",
		ContentType:          "application/json",
		OriginalModel:        "gpt-5",
		OriginalModelPresent: true,
		RawBody:              []byte(`{"model":"gpt-5","messages":[]}`),
		BodyAvailable:        true,
	}
}

func TestScoreHybridGatewayRequestCacheHitAndDispatch(t *testing.T) {
	now := time.Now()
	dispatcher := &mockDispatcher{script: []dispatchOutcome{{
		success: successDispatch("acct-1", "group-1", 200,
			`{"choices":[{"message":{"content":"{\"level\":7,\"confidence\":0.9,\"reason\":\"complex\",\"factors\":[\"多步推理\"]}"}}]}`,
			gatewayprotoEmptyUsage()),
	}}}
	recorder := &mockRecorder{}
	shared := newMockSharedCache()
	service := newScoringServiceWith(testClock(&now), dispatcher, recorder, shared)
	config := hybridConfig()
	input := ScoreInput{View: scoringView(), APIKeyRecord: APIKeyRecord{ID: "key", SystemAccountID: "sys"}, Config: config, TraceID: "trace", Endpoint: "/ep"}

	result := service.Score(context.Background(), input)
	if result.CacheHit || result.Failed || result.Level != 7 || result.ScoringAccountID != "acct-1" || result.ScoringGroupID != "group-1" {
		t.Fatalf("result = %+v", result)
	}
	if result.StatusCode == nil || *result.StatusCode != 200 {
		t.Fatalf("statusCode = %v", result.StatusCode)
	}
	if dispatcher.dispatchCount() != 1 {
		t.Fatalf("dispatch count = %d", dispatcher.dispatchCount())
	}
	// Dispatch input contract: endpoint suffix, traffic source, limits.
	first := dispatcher.inputs[0]
	if first.TrafficSource != "hybrid_scoring" || first.TimeoutMs != 15000 || first.ResponseMaxBytes != 2*1024*1024 {
		t.Fatalf("dispatch input = %+v", first)
	}
	if first.NoAccountErrorCode != "no_scoring_account" || first.DispatchErrorCode != "hybrid_scoring_failed" || first.HTTPErrorCode != "hybrid_scoring_http_error" {
		t.Fatalf("dispatch error codes = %s/%s/%s", first.NoAccountErrorCode, first.DispatchErrorCode, first.HTTPErrorCode)
	}
	if first.NoAccountErrorMessage != "混合路由绑定分组池没有可用评分账户" || first.DispatchErrorMessage != "混合路由评分模型调用失败" || first.ResponseTooLargeMessage != "混合路由评分响应超过保护上限" {
		t.Fatalf("dispatch error messages = %s/%s/%s", first.NoAccountErrorMessage, first.DispatchErrorMessage, first.ResponseTooLargeMessage)
	}
	// Success finish + two usage records (success) happened.
	if len(dispatcher.finishLog) != 1 || !dispatcher.finishLog[0].success {
		t.Fatalf("finish log = %+v", dispatcher.finishLog)
	}
	if len(recorder.records) != 1 || !recorder.records[0].Success || recorder.records[0].Endpoint != "/ep#hybrid-scoring" {
		t.Fatalf("records = %+v", recorder.records)
	}
	// Shared cache write happened.
	if len(shared.values) != 1 {
		t.Fatalf("shared cache = %v", shared.values)
	}
	// Second call hits the LRU: no dispatch, no new usage records, cacheHit=true.
	cached := service.Score(context.Background(), input)
	if !cached.CacheHit || cached.Level != 7 || dispatcher.dispatchCount() != 1 {
		t.Fatalf("cached = %+v, dispatches = %d", cached, dispatcher.dispatchCount())
	}
	if len(recorder.records) != 1 {
		t.Fatalf("cache hit must not record usage: %+v", recorder.records)
	}
}

func TestScoreHybridGatewayRequestSharedCacheHitPopulatesLRU(t *testing.T) {
	now := time.Now()
	dispatcher := &mockDispatcher{}
	recorder := &mockRecorder{}
	shared := newMockSharedCache()
	entry := HybridScoringCacheEntry{Level: 4, Confidence: floatPtr(0.5), Factors: []string{"上下文跨度"}, Reason: strPtr("cached")}
	// Pre-seed with the deterministic key.
	config := hybridConfig()
	key := buildHybridScoringCacheKey(scoringView(), APIKeyRecord{ID: "key", SystemAccountID: "sys"}, config, "/ep", buildHybridScoringContext(scoringView(), parseHybridRequestBody(scoringView())))
	shared.values[key] = entry
	service := newScoringServiceWith(testClock(&now), dispatcher, recorder, shared)
	result := service.Score(context.Background(), ScoreInput{
		View: scoringView(), APIKeyRecord: APIKeyRecord{ID: "key", SystemAccountID: "sys"}, Config: config, TraceID: "trace", Endpoint: "/ep",
	})
	if !result.CacheHit || result.Level != 4 || result.Reason == nil || *result.Reason != "cached" {
		t.Fatalf("result = %+v", result)
	}
	if dispatcher.dispatchCount() != 0 {
		t.Fatal("shared cache hit must skip dispatch")
	}
}

func TestScoreHybridGatewayRequestDispatchFailures(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	input := ScoreInput{View: scoringView(), APIKeyRecord: APIKeyRecord{ID: "key", SystemAccountID: "sys"}, Config: config, TraceID: "trace", Endpoint: "/ep"}

	t.Run("no account failure without usage", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			failure: failureDispatch("no_scoring_account", "混合路由绑定分组池没有可用评分账户", "", "", 0, false),
		}}}
		recorder := &mockRecorder{}
		service := newScoringServiceWith(testClock(&now), dispatcher, recorder, nil)
		result := service.Score(context.Background(), input)
		if !result.Failed || result.ErrorCode != "no_scoring_account" || result.Level != config.ScoringFallbackMaxLevel {
			t.Fatalf("result = %+v", result)
		}
		if len(recorder.records) != 0 {
			t.Fatalf("records = %+v", recorder.records)
		}
	})

	t.Run("dispatch failure records usage", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			failure: failureDispatch("hybrid_scoring_failed", "boom", "acct-9", "group-9", 502, true),
		}}}
		recorder := &mockRecorder{}
		service := newScoringServiceWith(testClock(&now), dispatcher, recorder, nil)
		result := service.Score(context.Background(), input)
		if !result.Failed || result.ErrorCode != "hybrid_scoring_failed" || result.ScoringAccountID != "acct-9" {
			t.Fatalf("result = %+v", result)
		}
		if result.StatusCode == nil || *result.StatusCode != 502 {
			t.Fatalf("statusCode = %v", result.StatusCode)
		}
		if len(recorder.records) != 1 || recorder.records[0].Success {
			t.Fatalf("records = %+v", recorder.records)
		}
		record := recorder.records[0]
		if record.ErrorCode != "hybrid_scoring_failed" || record.ErrorMessage != "boom" || record.GroupID != "group-9" {
			t.Fatalf("record = %+v", record)
		}
	})

	t.Run("invalid model response fails with usage and finish", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			success: successDispatch("acct-1", "group-1", 200, `not-json`, gatewayprotoEmptyUsage()),
		}}}
		recorder := &mockRecorder{}
		service := newScoringServiceWith(testClock(&now), dispatcher, recorder, nil)
		result := service.Score(context.Background(), input)
		if !result.Failed || result.ErrorCode != "hybrid_scoring_failed" {
			t.Fatalf("result = %+v", result)
		}
		if result.ErrorMessage != "混合评分模型未返回合法 JSON" {
			t.Fatalf("errorMessage = %s", result.ErrorMessage)
		}
		if len(dispatcher.finishLog) != 1 || dispatcher.finishLog[0].success {
			t.Fatalf("finishLog = %+v", dispatcher.finishLog)
		}
		if len(recorder.records) != 1 || recorder.records[0].Success || recorder.records[0].ErrorMessage != "混合评分模型未返回合法 JSON" {
			t.Fatalf("records = %+v", recorder.records)
		}
		if !strings.Contains(recorder.records[0].ResponseSnapshot.Body, "not-json") {
			t.Fatalf("response snapshot body = %q", recorder.records[0].ResponseSnapshot.Body)
		}
	})

	t.Run("usage record failure degrades to warn only", func(t *testing.T) {
		dispatcher := &mockDispatcher{script: []dispatchOutcome{{
			success: successDispatch("acct-1", "group-1", 200,
				`{"choices":[{"message":{"content":"{\"level\":2}"}}]}`, gatewayprotoEmptyUsage()),
		}}}
		recorder := &mockRecorder{err: errUsageFailed{}}
		warns := []string{}
		service := NewScoringService(testClock(&now), dispatcher, recorder, nil, func(event, message string) {
			warns = append(warns, event)
		})
		result := service.Score(context.Background(), input)
		if result.Failed || result.Level != 2 {
			t.Fatalf("result = %+v", result)
		}
		if len(warns) != 1 || warns[0] != "hybrid_scoring_success_usage_record_failed" {
			t.Fatalf("warns = %v", warns)
		}
	})
}

type errUsageFailed struct{}

func (errUsageFailed) Error() string { return "usage write failed" }

func TestScoringCacheTTLBoundaries(t *testing.T) {
	if got := hybridScoringCacheTTLMs(0); got != 1 {
		t.Fatalf("ttl 0 = %d, want 1", got)
	}
	if got := hybridScoringCacheTTLMs(-5); got != 1 {
		t.Fatalf("ttl -5 = %d, want 1", got)
	}
	if got := hybridScoringCacheTTLMs(3600); got != 60*60*1000 {
		t.Fatalf("ttl 3600 = %d", got)
	}
	if got := hybridScoringCacheTTLMs(7200); got != 60*60*1000 {
		t.Fatalf("ttl 7200 clamps = %d", got)
	}
}
