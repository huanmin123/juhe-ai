package gatewayusage

import (
	"strings"
	"testing"
)

func TestNormalizeUsageCapabilityToken(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"plain token", "default", "default"},
		{"uppercase allowed", "Priority", "Priority"},
		{"dots dashes underscores", "tier_x.1-b", "tier_x.1-b"},
		{"64 chars ok", strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{"65 chars rejected", strings.Repeat("a", 65), ""},
		{"leading digit missing", "_abc", ""},
		{"leading dot missing", ".abc", ""},
		{"surrounding whitespace", " default ", ""},
		{"inner whitespace ok only shape", "de fault", ""},
		{"not a string", 42, ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeUsageCapabilityToken(tt.value); got != tt.want {
				t.Fatalf("NormalizeUsageCapabilityToken(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestNormalizeUsageServiceTier(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"undefined falls back to default", nil, "default"},
		{"flex", "flex", "flex"},
		{"invalid falls back", "INVALID TIER", "default"},
		{"valid passthrough", "priority", "priority"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeUsageServiceTier(tt.value); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestResolveUsageServiceTiers(t *testing.T) {
	tests := []struct {
		name      string
		input     ResolveUsageServiceTiersInput
		requested string
		effective string
		billed    string
		hasReport bool
	}{
		{"all default", ResolveUsageServiceTiersInput{}, "default", "default", "default", false},
		{"requested only", ResolveUsageServiceTiersInput{RequestedServiceTier: "flex"}, "flex", "flex", "flex", false},
		{"effective overrides", ResolveUsageServiceTiersInput{RequestedServiceTier: "default", EffectiveServiceTier: "flex"}, "default", "flex", "flex", false},
		{"reported wins billed", ResolveUsageServiceTiersInput{RequestedServiceTier: "default", ReportedServiceTier: "priority"}, "default", "default", "priority", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := ResolveUsageServiceTiers(tt.input)
			if facts.RequestedServiceTier != tt.requested || facts.EffectiveServiceTier != tt.effective || facts.BilledServiceTier != tt.billed {
				t.Fatalf("got %+v want requested=%s effective=%s billed=%s", facts, tt.requested, tt.effective, tt.billed)
			}
			if facts.HasReportedTier != tt.hasReport {
				t.Fatalf("HasReportedTier = %v want %v", facts.HasReportedTier, tt.hasReport)
			}
		})
	}
}

func TestNormalizeOpenAIGatewayTrafficSource(t *testing.T) {
	valid := []string{
		"gateway", "manual_account_test", "account_health_check",
		"runtime_recovery_probe", "cooldown_retest", "hybrid_scoring",
		"hybrid_quality_scoring",
	}
	for _, source := range valid {
		t.Run("valid "+source, func(t *testing.T) {
			got, err := NormalizeOpenAIGatewayTrafficSource(source)
			if err != nil || got != source {
				t.Fatalf("got %q err %v", got, err)
			}
		})
	}
	t.Run("undefined defaults to gateway", func(t *testing.T) {
		got, err := NormalizeOpenAIGatewayTrafficSource(nil)
		if err != nil || got != TrafficSourceGateway {
			t.Fatalf("got %q err %v", got, err)
		}
	})
	t.Run("invalid throws Chinese error", func(t *testing.T) {
		_, err := NormalizeOpenAIGatewayTrafficSource("sneaky")
		if err == nil || err.Error() != "非法网关流量来源：sneaky" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTrafficSourceClassifications(t *testing.T) {
	tests := []struct {
		source      string
		probe       bool
		diagnostic  bool
		cooldown    bool
	}{
		{TrafficSourceGateway, false, false, false},
		{TrafficSourceManualAccountTest, false, true, false},
		{TrafficSourceAccountHealthCheck, true, true, false},
		{TrafficSourceRuntimeRecoveryProbe, true, true, false},
		{TrafficSourceCooldownRetest, true, true, true},
		{TrafficSourceHybridScoring, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			if got := IsAccountProbeTrafficSource(tt.source); got != tt.probe {
				t.Fatalf("probe = %v want %v", got, tt.probe)
			}
			if got := IsAccountDiagnosticTrafficSource(tt.source); got != tt.diagnostic {
				t.Fatalf("diagnostic = %v want %v", got, tt.diagnostic)
			}
			if got := IsCooldownRetestTrafficSource(tt.source); got != tt.cooldown {
				t.Fatalf("cooldown = %v want %v", got, tt.cooldown)
			}
		})
	}
}

func TestClassifyGatewayUpstreamFailure(t *testing.T) {
	status429 := 429
	status401 := 401
	status502 := 502
	status400 := 400
	tests := []struct {
		name           string
		input          GatewayUpstreamFailureClassificationInput
		failureClass   string
		reason         string
		classification string
	}{
		{"request phase transport", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamRequest}, FailureClassTransport, MetricReasonTransport, "upstream_transport_failure"},
		{"request phase quota code", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamRequest, ErrorCode: "insufficient_quota"}, FailureClassTransport, MetricReasonQuota, "upstream_transport_failure"},
		{"response phase opaque", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse}, FailureClassOpaqueUpstreamResponse, MetricReasonUnknownClass, "opaque_upstream_response_failure"},
		{"response 429", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, StatusCode: &status429}, FailureClassOpaqueUpstreamResponse, MetricReasonRateLimit, "opaque_upstream_response_failure"},
		{"response 401", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, StatusCode: &status401}, FailureClassOpaqueUpstreamResponse, MetricReasonAuth, "opaque_upstream_response_failure"},
		{"response 502", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, StatusCode: &status502}, FailureClassOpaqueUpstreamResponse, MetricReasonUpstream5xx, "opaque_upstream_response_failure"},
		{"response 400", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, StatusCode: &status400}, FailureClassOpaqueUpstreamResponse, MetricReasonUpstream4xx, "opaque_upstream_response_failure"},
		{"timeout code wins over status", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, StatusCode: &status502, ErrorCode: "ETIMEDOUT"}, FailureClassOpaqueUpstreamResponse, MetricReasonTimeout, "opaque_upstream_response_failure"},
		{"rate limit code", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, ErrorCode: "rate_limit_exceeded"}, FailureClassOpaqueUpstreamResponse, MetricReasonRateLimit, "opaque_upstream_response_failure"},
		{"auth code", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, ErrorCode: "permission_denied"}, FailureClassOpaqueUpstreamResponse, MetricReasonAuth, "opaque_upstream_response_failure"},
		{"protocol code", GatewayUpstreamFailureClassificationInput{Phase: FailurePhaseUpstreamResponse, ErrorCode: "upstream_protocol_failure"}, FailureClassOpaqueUpstreamResponse, MetricReasonProtocol, "opaque_upstream_response_failure"},
		{"unknown phase", GatewayUpstreamFailureClassificationInput{Phase: "elsewhere"}, FailureClassUnknown, MetricReasonUnknownClass, "unknown_failure_phase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyGatewayUpstreamFailure(tt.input)
			if got.FailureClass != tt.failureClass || got.MetricReasonClass != tt.reason || got.ClassificationReason != tt.classification {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestSanitizeDiagnosticString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"url credentials", "https://user:secret@example.com/path", "https://[redacted]@example.com/path"},
		{"bearer", "Authorization: Bearer abcdef1234567890", "Authorization: [redacted] [redacted]"},
		{"short bearer untouched", "Bearer abc", "Bearer abc"},
		{"sk key", "key is sk-abcdefghijklmnop", "key is sk-[redacted]"},
		{"juis key", "token juis_abcdefghijklmnop", "token juis_[redacted]"},
		{"quoted double", `{"api_key":"supersecret"}`, `{"api_key":"[redacted]"}`},
		{"quoted single", `{'password':'hunter2'}`, `{'password':'[redacted]'}`},
		{"quoted with escape", `{"token":"a\"b12345"}`, `{"token":"[redacted]"}`},
		{"bare assignment", "password=hunter2secret", "password=[redacted]"},
		{"bare with spaces", "token = abc123def456;", "token = [redacted];"},
		{"plain text untouched", "hello world", "hello world"},
		{"non sensitive quoted", `{"name":"value"}`, `{"name":"value"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeDiagnosticString(tt.input); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeDiagnosticValue(t *testing.T) {
	t.Run("sensitive field redacted", func(t *testing.T) {
		value := map[string]any{"api_key": "secret", "name": "ok"}
		sanitized := SanitizeDiagnosticValue(value).(map[string]any)
		if sanitized["api_key"] != "[redacted]" || sanitized["name"] != "ok" {
			t.Fatalf("got %v", sanitized)
		}
	})
	t.Run("field name normalization", func(t *testing.T) {
		value := map[string]any{"Set-Cookie": "a=b"}
		sanitized := SanitizeDiagnosticValue(value).(map[string]any)
		if sanitized["Set-Cookie"] != "[redacted]" {
			t.Fatalf("got %v", sanitized)
		}
	})
	t.Run("array cap", func(t *testing.T) {
		items := make([]any, diagnosticMaxArrayItems+2)
		for index := range items {
			items[index] = 1
		}
		sanitized := SanitizeDiagnosticValue(items).([]any)
		if len(sanitized) != diagnosticMaxArrayItems+1 {
			t.Fatalf("len = %d", len(sanitized))
		}
		if sanitized[diagnosticMaxArrayItems] != "[truncated:102]" {
			t.Fatalf("marker = %v", sanitized[diagnosticMaxArrayItems])
		}
	})
	t.Run("object key cap", func(t *testing.T) {
		value := map[string]any{}
		for index := 0; index < diagnosticMaxObjectKeys+5; index++ {
			value[itoa(index)] = index
		}
		sanitized := SanitizeDiagnosticValue(value).(map[string]any)
		if sanitized["__truncated__"] != true {
			t.Fatalf("expected __truncated__ marker")
		}
	})
	t.Run("depth cap", func(t *testing.T) {
		var nested any = "leaf"
		for index := 0; index < 12; index++ {
			nested = []any{nested}
		}
		sanitized := SanitizeDiagnosticValue(nested).([]any)
		// Arrays at depth 0..7 wrap; the array visited at depth 8 collapses
		// to the truncation marker (Node sanitizeValue depth contract).
		want := strings.Repeat("[", 8) + "[truncated]" + strings.Repeat("]", 8)
		got, _ := SanitizeDiagnosticValue(nested).(any)
		_ = got
		rendered := renderDiagnostic(sanitized)
		if rendered != want {
			t.Fatalf("got %q want %q", rendered, want)
		}
	})
}

func renderDiagnostic(value any) string {
	switch typed := value.(type) {
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			parts[index] = renderDiagnostic(item)
		}
		return "[" + strings.Join(parts, "") + "]"
	case string:
		return typed
	default:
		return ""
	}
}

func TestEstimateJSONLikeBytes(t *testing.T) {
	t.Run("strings and structure", func(t *testing.T) {
		value := NewOrderedObject().Set("a", "bc").Set("list", []any{"x", nil, true})
		// Node arithmetic: {2 + [key a:4 + "bc":4 + sep:1] + [key list:7 +
		// array:(2 + 3+1 + 4+1 + 4+1) + sep:1]} = 35.
		got := EstimateJSONLikeBytes(value, EstimateJSONLikeBytesOptions{})
		want := 2 + (4 + 4 + 1) + (7 + (2 + (3 + 1) + (4 + 1) + (4 + 1)) + 1)
		if got != want {
			t.Fatalf("got %d want %d", got, want)
		}
	})
	t.Run("max bytes clamp", func(t *testing.T) {
		got := EstimateJSONLikeBytes("a very long string indeed", EstimateJSONLikeBytesOptions{MaxBytes: 10})
		if got != 10 {
			t.Fatalf("got %d want 10", got)
		}
	})
	t.Run("max nodes stop", func(t *testing.T) {
		value := []any{"a", "b", "c", "d"}
		got := EstimateJSONLikeBytes(value, EstimateJSONLikeBytesOptions{MaxNodes: 2})
		// root node counts, then first item; "a" contributes 3 bytes before limit.
		if got != 3 {
			t.Fatalf("got %d want 3", got)
		}
	})
	t.Run("buffer bytes", func(t *testing.T) {
		got := EstimateJSONLikeBytes([]byte("abcd"), EstimateJSONLikeBytesOptions{})
		if got != 4 {
			t.Fatalf("got %d want 4", got)
		}
	})
	t.Run("struct mirrors node object", func(t *testing.T) {
		input := UsageRecordInput{TraceID: "t", TrafficSource: "gateway", Success: true}
		got := EstimateJSONLikeBytes(input, EstimateJSONLikeBytesOptions{})
		if got <= 0 {
			t.Fatalf("got %d", got)
		}
	})
}

func TestRFC3339Helpers(t *testing.T) {
	t.Run("canonicalize with offset", func(t *testing.T) {
		got, ok := canonicalizeRFC3339Instant("2024-03-05T10:20:30+08:00")
		if !ok || got != "2024-03-05T02:20:30.000Z" {
			t.Fatalf("got %q ok=%v", got, ok)
		}
	})
	t.Run("reject bare datetime", func(t *testing.T) {
		if _, ok := canonicalizeRFC3339Instant("2024-03-05T10:20:30"); ok {
			t.Fatal("expected rejection")
		}
	})
	t.Run("reject impossible date", func(t *testing.T) {
		if _, ok := canonicalizeRFC3339Instant("2024-02-30T10:20:30Z"); ok {
			t.Fatal("expected rejection")
		}
	})
	t.Run("required error copy", func(t *testing.T) {
		_, err := requiredRFC3339Instant("not-a-time", "使用记录 createdAt")
		if err == nil || err.Error() != "使用记录 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("milliseconds", func(t *testing.T) {
		got, ok := rfc3339InstantMilliseconds("1970-01-01T00:00:01.500Z")
		if !ok || got != 1500 {
			t.Fatalf("got %d ok=%v", got, ok)
		}
	})
}

func TestSliceStringByUTF8Bytes(t *testing.T) {
	// "中文abc": 中文 are 3 bytes each.
	if got := sliceStringByUTF8Bytes("中文abc", 6); got != "中文" {
		t.Fatalf("got %q", got)
	}
	if got := sliceStringByUTF8Bytes("中文abc", 7); got != "中文a" {
		t.Fatalf("got %q", got)
	}
	if got := sliceStringByUTF8Bytes("中文abc", 0); got != "" {
		t.Fatalf("got %q", got)
	}
}
