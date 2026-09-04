package gatewaybody

import (
	"strings"
	"testing"
)

func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }
func intPtr(v int) *int       { return &v }

func TestExtractJSONBodyMetadataTopLevelFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want JSONBodyMetadata
	}{
		{
			name: "model and stream",
			raw:  `{"model":"gpt-4o","stream":true}`,
			want: JSONBodyMetadata{Model: strPtr("gpt-4o"), Stream: boolPtr(true),
				ImageGeneration: false, ImageGenerationForced: false, StrictOutputRequirement: false},
		},
		{
			name: "stream false",
			raw:  `{"stream":false}`,
			want: JSONBodyMetadata{Stream: boolPtr(false)},
		},
		{
			name: "service tier valid token",
			raw:  `{"service_tier":"flex"}`,
			want: JSONBodyMetadata{ServiceTier: strPtr("flex")},
		},
		{
			name: "service tier invalid token is dropped",
			raw:  `{"service_tier":"has space"}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "reasoning effort field",
			raw:  `{"reasoning_effort":"high"}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("high")},
		},
		{
			name: "reasoning object effort",
			raw:  `{"reasoning":{"effort":"low"}}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("low")},
		},
		{
			name: "output_config effort",
			raw:  `{"output_config":{"effort":"medium"}}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("medium")},
		},
		{
			name: "reasoning object wins over field",
			raw:  `{"reasoning":{"effort":"low"},"reasoning_effort":"high"}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("low")},
		},
		{
			name: "field wins over output_config",
			raw:  `{"reasoning_effort":"high","output_config":{"effort":"medium"}}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("high")},
		},
		{
			name: "invalid effort token dropped",
			raw:  `{"reasoning_effort":"not a token!"}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "max tokens takes the max",
			raw:  `{"max_output_tokens":100,"max_tokens":250}`,
			want: JSONBodyMetadata{MaxOutputTokens: intPtr(250)},
		},
		{
			name: "max tokens negative dropped",
			raw:  `{"max_tokens":-5}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "max tokens fractional dropped",
			raw:  `{"max_tokens":1.5}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "max tokens exponent accepted",
			raw:  `{"max_tokens":1e3}`,
			want: JSONBodyMetadata{MaxOutputTokens: intPtr(1000)},
		},
		{
			name: "top level type image_generation forces image",
			raw:  `{"type":"image_generation"}`,
			want: JSONBodyMetadata{ImageGeneration: true, ImageGenerationForced: true},
		},
		{
			name: "response format truthy string",
			raw:  `{"response_format":{"type":"json_object"}}`,
			want: JSONBodyMetadata{StrictOutputRequirement: true},
		},
		{
			name: "tools truthy",
			raw:  `{"tools":[{"type":"function"}]}`,
			want: JSONBodyMetadata{StrictOutputRequirement: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJSONBodyMetadata([]byte(tt.raw))
			assertMetadata(t, got, tt.want)
		})
	}
}

func TestExtractJSONBodyMetadataImageTools(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want JSONBodyMetadata
	}{
		{
			name: "image generation tool in tools",
			raw:  `{"tools":[{"type":"function","function":{"name":"f"}},{"type":"image_generation"}]}`,
			want: JSONBodyMetadata{ImageGeneration: true, StrictOutputRequirement: true},
		},
		{
			name: "tool_choice string image_generation",
			raw:  `{"tool_choice":"image_generation"}`,
			want: JSONBodyMetadata{ImageGeneration: true, ImageGenerationForced: true, StrictOutputRequirement: true},
		},
		{
			name: "tool_choice object type image_generation",
			raw:  `{"tool_choice":{"type":"image_generation"}}`,
			want: JSONBodyMetadata{ImageGeneration: true, ImageGenerationForced: true, StrictOutputRequirement: true},
		},
		{
			name: "required choice with only image tools forces",
			raw:  `{"tool_choice":"required","tools":[{"type":"image_generation"}]}`,
			want: JSONBodyMetadata{ImageGeneration: true, ImageGenerationForced: true, StrictOutputRequirement: true},
		},
		{
			name: "required choice with mixed tools does not force",
			raw:  `{"tool_choice":"required","tools":[{"type":"image_generation"},{"type":"function"}]}`,
			want: JSONBodyMetadata{ImageGeneration: true, StrictOutputRequirement: true},
		},
		{
			name: "nested tools in tool_choice object",
			raw:  `{"tool_choice":{"type":"function","tools":[{"type":"image_generation"}]}}`,
			want: JSONBodyMetadata{ImageGeneration: true, StrictOutputRequirement: true},
		},
		{
			name: "string tool counted directly",
			raw:  `{"tools":"image_generation"}`,
			want: JSONBodyMetadata{ImageGeneration: true, StrictOutputRequirement: true},
		},
		{
			name: "non image tool only",
			raw:  `{"tools":[{"type":"function"}]}`,
			want: JSONBodyMetadata{StrictOutputRequirement: true},
		},
		{
			name: "empty tool type ignored",
			raw:  `{"tools":[{"type":"  "}]}`,
			want: JSONBodyMetadata{StrictOutputRequirement: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJSONBodyMetadata([]byte(tt.raw))
			assertMetadata(t, got, tt.want)
		})
	}
}

func TestExtractJSONBodyMetadataGenerationConfig(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want JSONBodyMetadata
	}{
		{
			name: "camel modalities image",
			raw:  `{"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
			want: JSONBodyMetadata{ImageGeneration: true},
		},
		{
			name: "snake modalities image",
			raw:  `{"generation_config":{"response_modalities":["image"]}}`,
			want: JSONBodyMetadata{ImageGeneration: true},
		},
		{
			name: "camel mime image",
			raw:  `{"generationConfig":{"responseMimeType":" Image/PNG "}}`,
			want: JSONBodyMetadata{ImageGeneration: true},
		},
		{
			name: "snake mime image",
			raw:  `{"generation_config":{"response_mime_type":"image/jpeg"}}`,
			want: JSONBodyMetadata{ImageGeneration: true},
		},
		{
			name: "camel preferred when object",
			raw:  `{"generationConfig":{"responseModalities":["TEXT"]},"generation_config":{"response_modalities":["IMAGE"]}}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "camel non object falls back to snake",
			raw:  `{"generationConfig":"text","generation_config":{"response_modalities":["IMAGE"]}}`,
			want: JSONBodyMetadata{ImageGeneration: true},
		},
		{
			name: "null modalities does not set image",
			raw:  `{"generationConfig":{"responseModalities":null}}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "camel thinking level",
			raw:  `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("high")},
		},
		{
			name: "snake thinking level",
			raw:  `{"generation_config":{"thinking_config":{"thinking_level":"low"}}}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("low")},
		},
		{
			name: "thinking level invalid token dropped",
			raw:  `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"nope!!"}}}`,
			want: JSONBodyMetadata{},
		},
		{
			name: "reasoning precedence over generation config",
			raw:  `{"reasoning":{"effort":"minimal"},"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`,
			want: JSONBodyMetadata{ReasoningEffort: strPtr("minimal")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJSONBodyMetadata([]byte(tt.raw))
			assertMetadata(t, got, tt.want)
		})
	}
}

func TestExtractJSONBodyMetadataCodexCompactionTrigger(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "top level type", raw: `{"type":"compaction_trigger"}`, want: true},
		{name: "nested object type", raw: `{"a":{"type":"compaction_trigger"}}`, want: true},
		{name: "deeply nested", raw: `{"a":[{"b":{"type":"compaction_trigger"}}]}`, want: true},
		{name: "plain type", raw: `{"type":"message"}`, want: false},
		{name: "type in array without object frame", raw: `{"a":["compaction_trigger"]}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJSONBodyMetadata([]byte(tt.raw))
			if got.CodexCompactionTrigger != tt.want {
				t.Fatalf("codexCompactionTrigger = %v, want %v", got.CodexCompactionTrigger, tt.want)
			}
			if got.InvalidJSON {
				t.Fatalf("unexpected invalidJson")
			}
		})
	}
}

func TestExtractJSONBodyMetadataInvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "truncated", raw: `{"model":"gpt-4o","stream":`},
		{name: "bad token", raw: `{"model":hehe}`},
		{name: "trailing content", raw: `{"model":"gpt-4o"} extra`},
		{name: "mismatched brackets", raw: `{"model":"gpt-4o"]`},
		{name: "empty input", raw: ``},
		{name: "lone string bad escape", raw: `{"a":"\x"}`},
		{name: "leading zero number", raw: `{"a":01}`},
		{name: "nan literal", raw: `{"a":NaN}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJSONBodyMetadata([]byte(tt.raw))
			if !got.InvalidJSON {
				t.Fatalf("expected invalidJson for %q", tt.raw)
			}
		})
	}

	t.Run("partial fields survive invalid json", func(t *testing.T) {
		got := ExtractJSONBodyMetadata([]byte(`{"model":"gpt-4o","stream":true,`))
		if !got.InvalidJSON {
			t.Fatalf("expected invalidJson")
		}
		if got.Model == nil || *got.Model != "gpt-4o" {
			t.Fatalf("model = %v, want gpt-4o", got.Model)
		}
		if got.Stream == nil || !*got.Stream {
			t.Fatalf("stream = %v, want true", got.Stream)
		}
	})
}

func TestExtractJSONBodyMetadataNonObjectRoots(t *testing.T) {
	tests := []string{`[1,2,3]`, `"text"`, `42`, `true`, `null`}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			got := ExtractJSONBodyMetadata([]byte(raw))
			assertMetadata(t, got, JSONBodyMetadata{})
		})
	}
}

func TestExtractJSONBodyMetadataKeyDecoding(t *testing.T) {
	t.Run("escaped top level key", func(t *testing.T) {
		// \u006d = m, so the key decodes to "model".
		got := ExtractJSONBodyMetadata([]byte(`{"\u006dodel":"gpt-4o"}`))
		if got.Model == nil || *got.Model != "gpt-4o" {
			t.Fatalf("model = %v, want gpt-4o", got.Model)
		}
	})
	t.Run("escaped nested type key triggers compaction", func(t *testing.T) {
		got := ExtractJSONBodyMetadata([]byte(`{"a":{"t\u0079pe":"compaction_trigger"}}`))
		if !got.CodexCompactionTrigger {
			t.Fatalf("expected compaction trigger via escaped key")
		}
	})
	t.Run("escaped model value decoded", func(t *testing.T) {
		got := ExtractJSONBodyMetadata([]byte(`{"model":"gpt\u002d4o"}`))
		if got.Model == nil || *got.Model != "gpt-4o" {
			t.Fatalf("model = %v, want gpt-4o", got.Model)
		}
	})
	t.Run("nested non-type keys are ignored", func(t *testing.T) {
		got := ExtractJSONBodyMetadata([]byte(`{"a":{"model":"nested"}}`))
		if got.Model != nil {
			t.Fatalf("model = %v, want nil", got.Model)
		}
	})
}

func TestExtractJSONBodyMetadataWhitespaceAndStructure(t *testing.T) {
	t.Run("whitespace heavy document", func(t *testing.T) {
		raw := "\n\t {  \"model\" : \"gpt-4o\" , \"stream\" : false }  \r\n"
		got := ExtractJSONBodyMetadata([]byte(raw))
		if got.Model == nil || *got.Model != "gpt-4o" || got.Stream == nil || *got.Stream {
			t.Fatalf("unexpected metadata: %+v", got)
		}
	})
	t.Run("deep nesting stays valid", func(t *testing.T) {
		depth := 600
		raw := strings.Repeat(`{"a":`, depth) + "1" + strings.Repeat("}", depth)
		got := ExtractJSONBodyMetadata([]byte(raw))
		if got.InvalidJSON {
			t.Fatalf("deep nesting should stay valid")
		}
	})
	t.Run("unicode escape to utf8 model", func(t *testing.T) {
		got := ExtractJSONBodyMetadata([]byte(`{"model":"gpt-4oé"}`))
		if got.Model == nil || *got.Model != "gpt-4oé" {
			t.Fatalf("model = %v", got.Model)
		}
	})
}

func assertMetadata(t *testing.T, got JSONBodyMetadata, want JSONBodyMetadata) {
	t.Helper()
	if !sameStringPtr(got.Model, want.Model) {
		t.Fatalf("model = %v, want %v", ptrValue(got.Model), ptrValue(want.Model))
	}
	if !sameBoolPtr(got.Stream, want.Stream) {
		t.Fatalf("stream = %v, want %v", ptrValue(got.Stream), ptrValue(want.Stream))
	}
	if !sameStringPtr(got.ServiceTier, want.ServiceTier) {
		t.Fatalf("serviceTier = %v, want %v", ptrValue(got.ServiceTier), ptrValue(want.ServiceTier))
	}
	if !sameStringPtr(got.ReasoningEffort, want.ReasoningEffort) {
		t.Fatalf("reasoningEffort = %v, want %v", ptrValue(got.ReasoningEffort), ptrValue(want.ReasoningEffort))
	}
	if !sameIntPtr(got.MaxOutputTokens, want.MaxOutputTokens) {
		t.Fatalf("maxOutputTokens = %v, want %v", ptrValue(got.MaxOutputTokens), ptrValue(want.MaxOutputTokens))
	}
	if got.ImageGeneration != want.ImageGeneration {
		t.Fatalf("imageGeneration = %v, want %v", got.ImageGeneration, want.ImageGeneration)
	}
	if got.ImageGenerationForced != want.ImageGenerationForced {
		t.Fatalf("imageGenerationForced = %v, want %v", got.ImageGenerationForced, want.ImageGenerationForced)
	}
	if got.StrictOutputRequirement != want.StrictOutputRequirement {
		t.Fatalf("strictOutputRequirement = %v, want %v", got.StrictOutputRequirement, want.StrictOutputRequirement)
	}
	if got.InvalidJSON != want.InvalidJSON {
		t.Fatalf("invalidJson = %v, want %v", got.InvalidJSON, want.InvalidJSON)
	}
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrValue[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}
