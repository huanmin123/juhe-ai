package gatewaybody

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBodyPipelineConstants(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"large warning", GatewayJSONBodyLargeWarningBytes, 2 * 1024 * 1024},
		{"inline parse max", GatewayJSONBodyInlineParseMaxBytes, 256 * 1024},
		{"inline metadata scan max", GatewayJSONBodyInlineMetadataScanMaxBytes, 256 * 1024},
		{"default text limit", DefaultGatewayTextRawBodyLimitBytes, 16 * 1024 * 1024},
		{"text hard limit", GatewayTextRawBodyHardLimitBytes, 16 * 1024 * 1024},
		{"image hard limit", GatewayImageRawBodyHardLimitBytes, 64 * 1024 * 1024},
		{"gateway hard limit", GatewayRawBodyHardLimitBytes, 64 * 1024 * 1024},
		{"in-flight default", DefaultGatewayBodyInFlightMaxBytes, 256 * 1024 * 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("%s = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
	if GatewayTextRawBodyHardLimit != "16mb" || GatewayImageRawBodyHardLimit != "64mb" || GatewayRawBodyHardLimit != "64mb" {
		t.Fatalf("hard limit strings mismatch")
	}
}

func TestIsJSONContentType(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"application/json", true},
		{"application/JSON; charset=utf-8", true},
		{"text/plain; charset=utf-8", false},
		{"multipart/form-data; boundary=x", false},
		{"", false},
		{"application/x-ndjson", true},
	}
	for _, tt := range tests {
		if got := IsJSONContentType(tt.contentType); got != tt.want {
			t.Fatalf("IsJSONContentType(%q) = %v, want %v", tt.contentType, got, tt.want)
		}
	}
}

func TestGatewayTextRawBodyLimitBytes(t *testing.T) {
	tests := []struct {
		name       string
		megabytes  int
		configured bool
		want       int
	}{
		{"unconfigured", 0, false, 16 * 1024 * 1024},
		{"below min", 0, true, 16 * 1024 * 1024},
		{"min", 1, true, 1 * 1024 * 1024},
		{"max", 64, true, 64 * 1024 * 1024},
		{"above max", 65, true, 16 * 1024 * 1024},
		{"mid", 32, true, 32 * 1024 * 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GatewayTextRawBodyLimitBytes(tt.megabytes, tt.configured); got != tt.want {
				t.Fatalf("limit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCreateBodyState(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		state := CreateBodyState(BodyStateInput{ContentType: "application/json", JSONParseStatus: JSONParseStatusEmpty})
		if state.ServiceTier != "default" {
			t.Fatalf("serviceTier = %q, want default", state.ServiceTier)
		}
		if state.IsJSON != true || state.JSONParseStatus != JSONParseStatusEmpty {
			t.Fatalf("unexpected state: %+v", state)
		}
		if state.Model != nil || state.Stream != nil || state.MaxOutputTokens != nil {
			t.Fatalf("unexpected optional fields: %+v", state)
		}
		if state.ImageGeneration || state.ImageGenerationForced || state.StrictOutputRequirement {
			t.Fatalf("unexpected booleans: %+v", state)
		}
	})

	t.Run("parsed body fallbacks", func(t *testing.T) {
		parsed := map[string]any{
			"model":             "gpt-4o",
			"stream":            true,
			"service_tier":      "flex",
			"reasoning":         map[string]any{"effort": "high"},
			"max_output_tokens": float64(100),
			"max_tokens":        float64(50),
			"response_format":   map[string]any{"type": "json_object"},
			"tools":             []any{map[string]any{"type": "image_generation"}},
		}
		state := CreateBodyState(BodyStateInput{ContentType: "application/json", JSONParseStatus: JSONParseStatusParsed, ParsedBody: parsed})
		if state.Model == nil || *state.Model != "gpt-4o" {
			t.Fatalf("model = %v", state.Model)
		}
		if state.Stream == nil || !*state.Stream {
			t.Fatalf("stream = %v", state.Stream)
		}
		if state.ServiceTier != "flex" {
			t.Fatalf("serviceTier = %q", state.ServiceTier)
		}
		if state.ReasoningEffort == nil || *state.ReasoningEffort != "high" {
			t.Fatalf("reasoningEffort = %v", state.ReasoningEffort)
		}
		if state.MaxOutputTokens == nil || *state.MaxOutputTokens != 100 {
			t.Fatalf("maxOutputTokens = %v", state.MaxOutputTokens)
		}
		if state.ResponseFormat != nil {
			t.Fatalf("responseFormat = %v, want nil for object values", state.ResponseFormat)
		}
		// A plain image_generation tool entry counts as image generation
		// without the forced flag (forced requires tool_choice/type forcing).
		if !state.ImageGeneration || state.ImageGenerationForced || !state.StrictOutputRequirement {
			t.Fatalf("image flags = %+v", state)
		}
	})

	t.Run("input overrides win", func(t *testing.T) {
		model := "override"
		state := CreateBodyState(BodyStateInput{
			Model:           &model,
			ReasoningEffort: strPtr("low"),
			MaxOutputTokens: intPtr(7),
			ImageGeneration: boolPtr(true),
		})
		if state.Model == nil || *state.Model != "override" {
			t.Fatalf("model = %v", state.Model)
		}
		if state.ReasoningEffort == nil || *state.ReasoningEffort != "low" {
			t.Fatalf("reasoning = %v", state.ReasoningEffort)
		}
		if state.MaxOutputTokens == nil || *state.MaxOutputTokens != 7 {
			t.Fatalf("maxOutputTokens = %v", state.MaxOutputTokens)
		}
		if !state.ImageGeneration || state.ImageGenerationForced {
			t.Fatalf("image flags = %+v", state)
		}
	})

	t.Run("falsy strict output values", func(t *testing.T) {
		parsed := map[string]any{"tools": nil, "response_format": 0.0, "tool_choice": ""}
		state := CreateBodyState(BodyStateInput{ParsedBody: parsed})
		if state.StrictOutputRequirement {
			t.Fatalf("falsy values must not set strictOutputRequirement")
		}
		parsed2 := map[string]any{"tool_choice": []any{}}
		state2 := CreateBodyState(BodyStateInput{ParsedBody: parsed2})
		if !state2.StrictOutputRequirement {
			t.Fatalf("arrays are truthy in JavaScript")
		}
	})
}

func TestNormalizeUsageCapabilityToken(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"flex", true},
		{"default", true},
		{"scale.auto", true},
		{"a", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{" Flex", false},
		{"flex ", false},
		{"", false},
		{"-flex", false},
		{"_flex", false},
		{"Flex", true},
	}
	for _, tt := range tests {
		got, ok := NormalizeUsageReasoningEffort(tt.value)
		if ok != tt.want {
			t.Fatalf("NormalizeUsageReasoningEffort(%q) ok = %v, want %v", tt.value, ok, tt.want)
		}
		if ok && got != tt.value {
			t.Fatalf("token = %q, want %q", got, tt.value)
		}
	}
	if tier := NormalizeUsageServiceTierValue("flex"); tier != "flex" {
		t.Fatalf("tier = %q", tier)
	}
	if tier := NormalizeUsageServiceTierValue(3.5); tier != "default" {
		t.Fatalf("tier default = %q", tier)
	}
	if tier := NormalizeUsageServiceTierValue(nil); tier != "default" {
		t.Fatalf("tier nil = %q", tier)
	}
}

func TestInFlightLimiter(t *testing.T) {
	t.Run("acquire and release", func(t *testing.T) {
		limiter := NewInFlightLimiter()
		limiter.SetMaxBytesForTest(1000)
		lease, ok := limiter.TryAcquire(600, 0)
		if !ok || lease == nil {
			t.Fatalf("expected lease")
		}
		_, ok = limiter.TryAcquire(600, 0)
		if ok {
			t.Fatalf("expected rejection over the budget")
		}
		state := limiter.State(0)
		if state.RejectedCount != 1 || state.CurrentBytes != 600 || state.RequestCount != 1 {
			t.Fatalf("state = %+v", state)
		}
		lease.Release()
		lease.Release() // idempotent
		state = limiter.State(0)
		if state.CurrentBytes != 0 || state.RequestCount != 0 {
			t.Fatalf("state after release = %+v", state)
		}
	})

	t.Run("zero byte requests pass without lease", func(t *testing.T) {
		limiter := NewInFlightLimiter()
		lease, ok := limiter.TryAcquire(0, 0)
		if !ok || lease != nil {
			t.Fatalf("zero bytes must pass without a lease")
		}
	})

	t.Run("per request cap", func(t *testing.T) {
		limiter := NewInFlightLimiter()
		if _, ok := limiter.TryAcquire(300_000_000, 0); ok {
			t.Fatalf("expected rejection above the default 256mb budget")
		}
	})

	t.Run("configured max bytes", func(t *testing.T) {
		limiter := NewInFlightLimiter()
		if _, ok := limiter.TryAcquire(64*1024*1024+1, 64*1024*1024); ok {
			t.Fatalf("expected rejection above the configured max")
		}
		if state := limiter.State(64 * 1024 * 1024); state.MaxBytes != 64*1024*1024 {
			t.Fatalf("maxBytes = %d", state.MaxBytes)
		}
	})

	t.Run("test override clears", func(t *testing.T) {
		limiter := NewInFlightLimiter()
		limiter.SetMaxBytesForTest(100)
		if state := limiter.State(0); state.MaxBytes != 100 {
			t.Fatalf("maxBytes = %d", state.MaxBytes)
		}
		limiter.ClearMaxBytesForTest()
		if state := limiter.State(0); state.MaxBytes != DefaultGatewayBodyInFlightMaxBytes {
			t.Fatalf("maxBytes = %d", state.MaxBytes)
		}
	})
}

func TestImageGenerationToolInspection(t *testing.T) {
	tests := []struct {
		name          string
		body          any
		images        int
		nonImages     int
		forced        bool
		hint          bool
		forcesRequest bool
	}{
		{
			name: "nil body",
			body: nil,
		},
		{
			name:   "string tool",
			body:   map[string]any{"tools": "image_generation"},
			images: 1, hint: true,
		},
		{
			name:   "forced tool choice",
			body:   map[string]any{"tool_choice": "image_generation"},
			forced: true, hint: true, forcesRequest: true,
		},
		{
			name: "mixed tools with required choice",
			body: map[string]any{
				"tool_choice": "required",
				"tools":       []any{"image_generation", "function"},
			},
			images: 1, nonImages: 1, hint: true,
		},
		{
			name: "required choice with only images",
			body: map[string]any{
				"tool_choice": "required",
				"tools":       []any{"image_generation"},
			},
			images: 1, forced: true, hint: true, forcesRequest: true,
		},
		{
			name:   "top level type",
			body:   map[string]any{"type": "image_generation"},
			forced: true, hint: true, forcesRequest: true,
		},
		{
			name:   "nested tools in tool_choice",
			body:   map[string]any{"tool_choice": map[string]any{"tools": []any{"image_generation"}}},
			images: 1, hint: true,
		},
		{
			name: "depth capped",
			body: deepNestedTool(6),
			hint: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inspection := InspectImageGenerationTools(tt.body)
			if inspection.ImageToolCount != tt.images || inspection.NonImageToolCount != tt.nonImages || inspection.ForcedImageGeneration != tt.forced {
				t.Fatalf("inspection = %+v", inspection)
			}
			if RequestBodyHasImageGenerationHint(tt.body) != tt.hint {
				t.Fatalf("hint mismatch")
			}
			if RequestBodyForcesImageGeneration(tt.body) != tt.forcesRequest {
				t.Fatalf("forces mismatch")
			}
		})
	}
}

func deepNestedTool(depth int) any {
	var current any = "image_generation"
	for i := 0; i < depth; i++ {
		current = map[string]any{"nested": []any{current}}
	}
	return map[string]any{"tools": current}
}

func TestDowngradeAutoImageGenerationToolsInBody(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		result, _ := DowngradeAutoImageGenerationToolsInBody(nil)
		if result.Downgraded || result.Reason != DowngradeReasonNotJSONObject {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("forced tool not downgraded", func(t *testing.T) {
		result, _ := DowngradeAutoImageGenerationToolsInBody(map[string]any{"tool_choice": "image_generation"})
		if result.Downgraded || result.Reason != DowngradeReasonForcedImageGenerationTool {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("no tools", func(t *testing.T) {
		result, _ := DowngradeAutoImageGenerationToolsInBody(map[string]any{"model": "gpt-4o"})
		if result.Downgraded || result.Reason != DowngradeReasonNoAutoImageGenerationTool {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("auto tools removed", func(t *testing.T) {
		body := map[string]any{
			"tools":       []any{map[string]any{"type": "function"}, map[string]any{"type": "image_generation"}, "image_generation"},
			"tool_choice": map[string]any{"tools": []any{"image_generation"}},
		}
		result, nextBody := DowngradeAutoImageGenerationToolsInBody(body)
		if !result.Downgraded || result.RemovedToolCount != 3 || result.Reason != DowngradeReasonAutoImageGenerationToolRemoved {
			t.Fatalf("result = %+v", result)
		}
		tools := nextBody["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools after downgrade = %#v", tools)
		}
		if _, ok := nextBody["tool_choice"].(map[string]any)["tools"]; ok {
			t.Fatalf("tool_choice tools should be deleted when empty")
		}
	})
	t.Run("all tools removed deletes the key", func(t *testing.T) {
		body := map[string]any{"tools": []any{"image_generation"}}
		result, nextBody := DowngradeAutoImageGenerationToolsInBody(body)
		if !result.Downgraded || result.RemovedToolCount != 1 {
			t.Fatalf("result = %+v", result)
		}
		if _, ok := nextBody["tools"]; ok {
			t.Fatalf("tools key should be deleted")
		}
	})
}

func TestReplaceGatewayJSONBodyAndSummary(t *testing.T) {
	req := &Request{State: &BodyState{ContentType: "application/json"}}

	t.Run("model replacement needs a body", func(t *testing.T) {
		if ReplaceGatewayJSONBodyModel(req, "gpt-4o-mini", nil) {
			t.Fatalf("replacement without a body must fail")
		}
	})

	ReplaceGatewayJSONBody(req, map[string]any{
		"model": "gpt-4o",
		"tools": []any{map[string]any{"type": "function"}},
	})
	if req.State.JSONParseStatus != JSONParseStatusParsed {
		t.Fatalf("status = %v", req.State.JSONParseStatus)
	}
	if req.State.ContentType != "application/json" {
		t.Fatalf("contentType = %q", req.State.ContentType)
	}
	// Parsed body carries the tool so strict output becomes true.
	if !req.State.StrictOutputRequirement {
		t.Fatalf("expected strict output requirement")
	}
	summary := BuildGatewayRequestBodySummary(req)
	if summary != nil {
		t.Fatalf("summary under the warning threshold must be nil, got %#v", summary)
	}

	// Grow the body past the warning threshold.
	bigModel := make([]byte, GatewayJSONBodyLargeWarningBytes+16)
	for i := range bigModel {
		bigModel[i] = 'a'
	}
	if !ReplaceGatewayJSONBodyModel(req, string(bigModel), nil) {
		t.Fatalf("expected model replacement")
	}
	summary = BuildGatewayRequestBodySummary(req)
	if summary == nil {
		t.Fatalf("expected summary above the warning threshold")
	}
	body := summary["_gatewayBody"].(map[string]any)
	if body["rawBodyBytes"] != len(req.RawBody) || body["jsonParseStatus"] != "parsed" {
		t.Fatalf("summary = %#v", body)
	}
	if body["model"] != string(bigModel) {
		t.Fatalf("summary model missing")
	}

	t.Run("empty model rejected", func(t *testing.T) {
		if ReplaceGatewayJSONBodyModel(req, "   ", nil) {
			t.Fatalf("empty model must be rejected")
		}
	})

	t.Run("content type header fallback", func(t *testing.T) {
		fresh := &Request{ContentTypeHeader: "application/json; charset=utf-8"}
		ReplaceGatewayJSONBody(fresh, map[string]any{"a": 1.0})
		if fresh.State.ContentType != "application/json; charset=utf-8" {
			t.Fatalf("contentType = %q", fresh.State.ContentType)
		}
	})

	t.Run("serialized body survives marshal round trip", func(t *testing.T) {
		decoded := map[string]any{}
		if err := json.Unmarshal(req.RawBody, &decoded); err != nil {
			t.Fatalf("raw body is not valid JSON: %v", err)
		}
	})
}

func TestGatewayRequestBodyForcesImageGenerationAndDowngrade(t *testing.T) {
	scanned := &Request{RawBody: []byte(`{"tools":[{"type":"image_generation"}]}`)}
	scanned.State = CreateBodyState(BodyStateInput{
		RawBody:         scanned.RawBody,
		ContentType:     "application/json",
		JSONParseStatus: JSONParseStatusScannedJSON,
	})
	// A scanned request has no parsed body (gatewayJsonObjectBody(undefined)
	// in Node): neither the state flag nor the body inspection forces image.
	if GatewayRequestBodyForcesImageGeneration(scanned) {
		t.Fatalf("scanned request must not force before materialization")
	}
	if result := DowngradeGatewayAutoImageGenerationTool(scanned); result.Downgraded || result.Reason != DowngradeReasonNotJSONObject {
		t.Fatalf("downgrade without a body = %+v", result)
	}

	req := &Request{RawBody: []byte(`{"tools":[{"type":"image_generation"}]}`)}
	parsed := map[string]any{"tools": []any{map[string]any{"type": "image_generation"}}}
	req.Body = parsed
	req.parsedAvailable = true
	req.parsedBody = parsed
	req.State = CreateBodyState(BodyStateInput{ContentType: "application/json"})
	result := DowngradeGatewayAutoImageGenerationTool(req)
	if !result.Downgraded || result.RemovedToolCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, ok := GatewayJSONObjectBody(req)["tools"]; ok {
		t.Fatalf("tools should be removed from the live body")
	}
	if req.State.JSONParseStatus != JSONParseStatusParsed {
		t.Fatalf("status = %v", req.State.JSONParseStatus)
	}
}

func TestSerializedBodyAssociation(t *testing.T) {
	body := map[string]any{"a": 1.0, "html": "<b>&</b>"}
	serialized := SerializeGatewayJSONObject(body)
	if string(serialized.Raw) != `{"a":1,"html":"<b>&</b>"}` {
		t.Fatalf("raw = %s (HTML escaping must follow JSON.stringify)", serialized.Raw)
	}
	if serialized.IsCodexHistorySanitized() {
		t.Fatalf("sanitized flag should start false")
	}
	serialized.MarkCodexHistorySanitized()
	if !serialized.IsCodexHistorySanitized() {
		t.Fatalf("sanitized flag should be set")
	}
	bound := BindGatewaySerializedJSONObject([]byte(`{"a":1}`), body)
	if bound.Parsed["a"] != 1.0 || len(bound.Parsed) != len(body) {
		t.Fatalf("binding mismatch")
	}
}
