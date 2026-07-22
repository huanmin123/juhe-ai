package gatewayopenai

import (
	"errors"
	"strings"
	"testing"

	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

func TestParseNonStreamJSONChatCompletions(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{
		"service_tier":"priority",
		"choices":[{
			"message":{
				"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}],
				"reasoning_content":"reasoning"
			},
			"finish_reason":"stop"
		}],
		"usage":{
			"prompt_tokens":1200,
			"completion_tokens":150,
			"prompt_tokens_details":{"cached_tokens":400},
			"completion_tokens_details":{"reasoning_tokens":10}
		}
	}`), ParseOptions{
		Interpretation: InterpretOpenAI,
		EndpointFamily: EndpointChatCompletions,
	})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if result.Opaque {
		t.Fatal("exact OpenAI result must not be opaque")
	}
	assertUsage(t, result.Usage, Usage{
		ServiceTier:     "priority",
		InputTokens:     int64Pointer(1200),
		OutputTokens:    int64Pointer(150),
		CacheReadTokens: int64Pointer(400),
		ThinkingTokens:  int64Pointer(10),
	})
	if len(result.Frames) != 5 {
		t.Fatalf("frames = %#v, want content, reasoning, completion, usage, raw JSON", result.Frames)
	}
	assertFrame(t, result.Frames[0], Frame{
		Kind:           FrameOutputTextDone,
		EndpointFamily: EndpointChatCompletions,
		Text:           "hello world",
		FinishReason:   "stop",
		Status:         "stop",
		ChoiceIndex:    intPointer(0),
		JSONPaths:      []string{"choices.0.message.content"},
		VisibleOutput:  true,
	})
	assertFrame(t, result.Frames[1], Frame{
		Kind:           FrameOutputTextDone,
		EndpointFamily: EndpointChatCompletions,
		Text:           "reasoning",
		FinishReason:   "stop",
		Status:         "stop",
		ChoiceIndex:    intPointer(0),
		JSONPaths:      []string{"choices.0.message.reasoning_content"},
		VisibleOutput:  true,
	})
	if result.Frames[2].Kind != FrameCompleted || result.Frames[2].FinishReason != "stop" {
		t.Fatalf("completion frame = %#v", result.Frames[2])
	}
	if result.Frames[3].Kind != FrameUsage || result.Frames[3].Usage == nil {
		t.Fatalf("usage frame = %#v", result.Frames[3])
	}
	if result.Frames[4].Kind != FrameRawJSONPath || result.Frames[4].Document == nil {
		t.Fatalf("raw JSON frame = %#v", result.Frames[4])
	}
}

func TestParseNonStreamJSONResponsesAndDetailedUsage(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{
		"status":"completed",
		"output_text":"direct",
		"output":[{"content":[{"type":"output_text","text":"nested"},{"type":"other","value":"ignored"}]}],
		"usage":{
			"input_tokens":"100",
			"output_tokens":60.9,
			"input_tokens_details":{
				"cached_tokens":10,
				"image_tokens":25,
				"audio_tokens":30,
				"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}
			},
			"output_tokens_details":{"image_tokens":40,"audio_tokens":41,"reasoning_tokens":12},
			"output_image_count":2
		}
	}`), ParseOptions{
		Interpretation: InterpretOpenAI,
		EndpointFamily: EndpointResponses,
	})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	assertUsage(t, result.Usage, Usage{
		InputTokens:        int64Pointer(100),
		OutputTokens:       int64Pointer(60),
		CacheReadTokens:    int64Pointer(10),
		CacheWriteTokens:   int64Pointer(7),
		CacheWrite1hTokens: int64Pointer(4),
		ThinkingTokens:     int64Pointer(12),
		InputImageTokens:   int64Pointer(25),
		OutputImageTokens:  int64Pointer(40),
		InputAudioTokens:   int64Pointer(30),
		OutputAudioTokens:  int64Pointer(41),
		OutputImageCount:   int64Pointer(2),
	})
	if len(result.Frames) != 5 {
		t.Fatalf("frames = %#v, want direct, nested, completed, usage, raw JSON", result.Frames)
	}
	if result.Frames[0].Text != "direct" || result.Frames[1].Text != "nested" {
		t.Fatalf("output frames = %#v", result.Frames[:2])
	}
	if result.Frames[1].OutputIndex == nil || *result.Frames[1].OutputIndex != 0 ||
		result.Frames[1].ContentIndex == nil || *result.Frames[1].ContentIndex != 0 {
		t.Fatalf("nested output indices = %#v", result.Frames[1])
	}
}

func TestParseNonStreamJSONUsageAliasPrecedence(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{
		"service_tier":" flex ",
		"usage":{
			"input_tokens":10,
			"prompt_tokens":999,
			"output_tokens":20,
			"completion_tokens":999,
			"input_tokens_details":{
				"cache_write_tokens":9,
				"cache_write_1h_tokens":3,
				"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":100}
			},
			"prompt_tokens_details":{"cached_tokens":8},
			"prompt_cache_hit_tokens":7
		}
	}`), ParseOptions{Interpretation: InterpretOpenAI})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	assertUsage(t, result.Usage, Usage{
		InputTokens:        int64Pointer(10),
		OutputTokens:       int64Pointer(20),
		CacheReadTokens:    int64Pointer(8),
		CacheWriteTokens:   int64Pointer(9),
		CacheWrite1hTokens: int64Pointer(3),
	})
}

func TestParseNonStreamJSONCompatibleCacheAliases(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{"usage":{
		"input_tokens":700,
		"output_tokens":20,
		"claude_cache_creation_5_m_tokens":"6",
		"claude_cache_creation_1_h_tokens":4,
		"output_images":0
	}}`), ParseOptions{Interpretation: InterpretOpenAI})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	assertUsage(t, result.Usage, Usage{
		InputTokens:        int64Pointer(700),
		OutputTokens:       int64Pointer(20),
		CacheWriteTokens:   int64Pointer(10),
		CacheWrite1hTokens: int64Pointer(4),
	})
}

func TestParseNonStreamJSONErrorFrames(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{
		"error":{"code":"root_code","type":"root_type","message":"root message"},
		"response":{"error":{"code":"nested_code","message":"nested message"}}
	}`), ParseOptions{
		Interpretation: InterpretOpenAI,
		EndpointFamily: EndpointResponses,
	})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if len(result.Frames) != 3 {
		t.Fatalf("frames = %#v, want two error frames and raw JSON", result.Frames)
	}
	assertFrame(t, result.Frames[0], Frame{
		Kind:           FrameError,
		EndpointFamily: EndpointResponses,
		ErrorCode:      "root_code",
		ErrorType:      "root_type",
		ErrorMessage:   "root message",
		JSONPaths:      []string{"error"},
	})
	assertFrame(t, result.Frames[1], Frame{
		Kind:           FrameError,
		EndpointFamily: EndpointResponses,
		ErrorCode:      "nested_code",
		ErrorMessage:   "nested message",
		JSONPaths:      []string{"response.error"},
	})
}

func TestParseNonStreamJSONUnknownFamilyExtractsKnownShapes(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{
		"status":"completed",
		"output_text":"responses text",
		"choices":[{"message":{"content":"chat text"},"finish_reason":"stop"}]
	}`), ParseOptions{Interpretation: InterpretOpenAI})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if len(result.Frames) != 5 {
		t.Fatalf("frames = %#v, want chat text/completed, responses text/completed, and raw JSON", result.Frames)
	}
	if result.Frames[0].EndpointFamily != EndpointChatCompletions || result.Frames[0].Text != "chat text" ||
		result.Frames[2].EndpointFamily != EndpointResponses || result.Frames[2].Text != "responses text" {
		t.Fatalf("unknown-family extraction = %#v", result.Frames)
	}
}

func TestParseNonStreamJSONGenericIsOpaque(t *testing.T) {
	body := []byte(`{"error":{"code":"must_not_parse"},"usage":{"input_tokens":10}}`)
	result, err := ParseNonStreamJSON(body, ParseOptions{})
	if err != nil {
		t.Fatalf("opaque ParseNonStreamJSON() error = %v", err)
	}
	if !result.Opaque || result.Document != nil || !result.Usage.Empty() || len(result.Frames) != 0 {
		t.Fatalf("opaque result = %#v", result)
	}

	result, err = ParseNonStreamJSON([]byte(`not json`), ParseOptions{Interpretation: InterpretOpaque})
	if err != nil || !result.Opaque {
		t.Fatalf("opaque invalid body result = %#v, error = %v", result, err)
	}
}

func TestParseNonStreamJSONRetainsBoundedExactDocumentForPathInspection(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{
		"status":"completed",
		"metadata":{"blocked":{"reason":"policy"},"empty":"","disabled":false,"bom":"\uFEFF","nextLine":"\u0085"},
		"items":[{"state":"ready"}],
		"nil":null
	}`), ParseOptions{Interpretation: InterpretOpenAI, EndpointFamily: EndpointResponses})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if result.Document == nil {
		t.Fatal("exact OpenAI result must retain a bounded inspection document")
	}
	for _, path := range []string{"metadata", "metadata.blocked.reason", "items.0.state", "metadata.nextLine"} {
		if !result.Document.PathExists(path) {
			t.Fatalf("PathExists(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"", "metadata.missing", "metadata.empty", "metadata.disabled", "metadata.bom", "items.2", "nil"} {
		if result.Document.PathExists(path) {
			t.Fatalf("PathExists(%q) = true, want false", path)
		}
	}
	for index, frame := range result.Frames {
		if frame.Document != result.Document {
			t.Fatalf("frame %d document does not share the exact parsed document", index)
		}
	}
}

func TestParseNonStreamJSONExtractsRefusalAsVisibleOutput(t *testing.T) {
	tests := []struct {
		name   string
		family EndpointFamily
		body   string
		path   string
	}{
		{
			name:   "chat refusal",
			family: EndpointChatCompletions,
			body:   `{"choices":[{"message":{"refusal":"cannot comply"},"finish_reason":"stop"}]}`,
			path:   "choices.0.message.refusal",
		},
		{
			name:   "responses refusal",
			family: EndpointResponses,
			body:   `{"status":"completed","output":[{"content":[{"type":"refusal","refusal":"cannot comply"}]}]}`,
			path:   "output.0.content.0.refusal",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ParseNonStreamJSON([]byte(test.body), ParseOptions{
				Interpretation: InterpretOpenAI,
				EndpointFamily: test.family,
			})
			if err != nil {
				t.Fatalf("ParseNonStreamJSON() error = %v", err)
			}
			if len(result.Frames) < 2 || result.Frames[0].Kind != FrameOutputTextDone ||
				result.Frames[0].Text != "cannot comply" || !result.Frames[0].VisibleOutput ||
				strings.Join(result.Frames[0].JSONPaths, "") != test.path {
				t.Fatalf("refusal frames = %#v", result.Frames)
			}
		})
	}
}

func TestParseNonStreamJSONServiceTierWithoutUsageDoesNotInventUsagePath(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{"service_tier":"priority"}`), ParseOptions{
		Interpretation: InterpretOpenAI,
	})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if result.Usage.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", result.Usage.ServiceTier)
	}
	if len(result.Frames) != 1 || result.Frames[0].Kind != FrameRawJSONPath {
		t.Fatalf("service-tier-only frames = %#v, want raw JSON only", result.Frames)
	}
}

func TestParseNonStreamJSONPreservesLargeIntegerCountsExactly(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{"usage":{
		"input_tokens":9007199254740993,
		"output_tokens":"9223372036854775807.9",
		"prompt_cache_hit_tokens":9223372036854775808
	}}`), ParseOptions{Interpretation: InterpretOpenAI})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	assertUsage(t, result.Usage, Usage{
		InputTokens:  int64Pointer(9007199254740993),
		OutputTokens: int64Pointer(9223372036854775807),
	})
}

func TestParseNonStreamJSONRejectsInvalidExactPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "invalid", body: "{"},
		{name: "multiple values", body: `{} {}`},
		{name: "array", body: `[]`},
		{name: "null", body: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseNonStreamJSON([]byte(test.body), ParseOptions{Interpretation: InterpretOpenAI})
			if !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("error = %v, want ErrInvalidJSON", err)
			}
		})
	}
}

func TestParseNonStreamJSONEnforcesBoundsBeforeInterpretation(t *testing.T) {
	body := []byte(`{"padding":"` + strings.Repeat("x", 64) + `"}`)
	for _, interpretation := range []Interpretation{InterpretOpaque, InterpretOpenAI} {
		_, err := ParseNonStreamJSON(body, ParseOptions{
			Interpretation: interpretation,
			MaxBytes:       32,
		})
		if !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("interpretation %q error = %v, want ErrPayloadTooLarge", interpretation, err)
		}
	}

	_, err := ParseNonStreamJSON([]byte(`{}`), ParseOptions{
		Interpretation: InterpretOpenAI,
		MaxBytes:       MaxNonStreamJSONBytes + 1,
	})
	if !errors.Is(err, ErrInvalidMaxBytes) {
		t.Fatalf("error = %v, want ErrInvalidMaxBytes", err)
	}
}

func TestParseNonStreamJSONIgnoresInvalidUsageValues(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{"usage":{
		"input_tokens":-1,
		"output_tokens":"NaN",
		"prompt_tokens_details":{"cached_tokens":true},
		"output_image_count":0
	}}`), ParseOptions{Interpretation: InterpretOpenAI})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if !result.Usage.Empty() || len(result.Frames) != 1 || result.Frames[0].Kind != FrameRawJSONPath {
		t.Fatalf("invalid usage must leave only raw JSON semantics: %#v", result)
	}
}

func TestParseNonStreamJSONPreservesZeroUsageValues(t *testing.T) {
	result, err := ParseNonStreamJSON([]byte(`{"usage":{"input_tokens":0,"output_tokens":"0"}}`), ParseOptions{
		Interpretation: InterpretOpenAI,
	})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	assertUsage(t, result.Usage, Usage{
		InputTokens:  int64Pointer(0),
		OutputTokens: int64Pointer(0),
	})
	if len(result.Frames) != 2 || result.Frames[0].Kind != FrameUsage || result.Frames[1].Kind != FrameRawJSONPath {
		t.Fatalf("zero usage frame = %#v", result.Frames)
	}
}

func TestParseNonStreamJSONAcceptsRegistryEndpointFamily(t *testing.T) {
	endpointFamily := gatewayprotocol.EndpointResponses
	result, err := ParseNonStreamJSON([]byte(`{"status":"completed"}`), ParseOptions{
		Interpretation: InterpretOpenAI,
		EndpointFamily: endpointFamily,
	})
	if err != nil {
		t.Fatalf("ParseNonStreamJSON() error = %v", err)
	}
	if len(result.Frames) < 1 || result.Frames[0].EndpointFamily != gatewayprotocol.EndpointResponses {
		t.Fatalf("frames = %#v", result.Frames)
	}
}

func TestParseNonStreamJSONRejectsUnknownOptions(t *testing.T) {
	_, err := ParseNonStreamJSON([]byte(`{}`), ParseOptions{Interpretation: Interpretation("auto")})
	if !errors.Is(err, ErrInvalidInterpretation) {
		t.Fatalf("interpretation error = %v, want ErrInvalidInterpretation", err)
	}

	_, err = ParseNonStreamJSON([]byte(`{}`), ParseOptions{EndpointFamily: EndpointFamily("completions")})
	if !errors.Is(err, ErrInvalidEndpointFamily) {
		t.Fatalf("endpoint error = %v, want ErrInvalidEndpointFamily", err)
	}
}

func assertUsage(t *testing.T, got Usage, want Usage) {
	t.Helper()
	if got.ServiceTier != want.ServiceTier ||
		!equalInt64Pointer(got.InputTokens, want.InputTokens) ||
		!equalInt64Pointer(got.OutputTokens, want.OutputTokens) ||
		!equalInt64Pointer(got.CacheReadTokens, want.CacheReadTokens) ||
		!equalInt64Pointer(got.CacheWriteTokens, want.CacheWriteTokens) ||
		!equalInt64Pointer(got.CacheWrite1hTokens, want.CacheWrite1hTokens) ||
		!equalInt64Pointer(got.ThinkingTokens, want.ThinkingTokens) ||
		!equalInt64Pointer(got.InputImageTokens, want.InputImageTokens) ||
		!equalInt64Pointer(got.OutputImageTokens, want.OutputImageTokens) ||
		!equalInt64Pointer(got.InputAudioTokens, want.InputAudioTokens) ||
		!equalInt64Pointer(got.OutputAudioTokens, want.OutputAudioTokens) ||
		!equalInt64Pointer(got.OutputImageCount, want.OutputImageCount) {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func assertFrame(t *testing.T, got Frame, want Frame) {
	t.Helper()
	if got.Kind != want.Kind || got.EndpointFamily != want.EndpointFamily ||
		got.Text != want.Text || got.ErrorCode != want.ErrorCode || got.ErrorType != want.ErrorType ||
		got.ErrorMessage != want.ErrorMessage || got.FinishReason != want.FinishReason || got.Status != want.Status ||
		!equalIntPointer(got.ChoiceIndex, want.ChoiceIndex) || !equalIntPointer(got.OutputIndex, want.OutputIndex) ||
		!equalIntPointer(got.ContentIndex, want.ContentIndex) || strings.Join(got.JSONPaths, "\x00") != strings.Join(want.JSONPaths, "\x00") ||
		got.VisibleOutput != want.VisibleOutput {
		t.Fatalf("frame = %#v, want %#v", got, want)
	}
}

func equalInt64Pointer(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalIntPointer(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func int64Pointer(value int64) *int64 { return &value }

func intPointer(value int) *int { return &value }
