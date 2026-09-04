package gatewayusage

import (
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ ms int64 }

func (c fixedClock) Now() time.Time { return time.UnixMilli(c.ms) }

type countingIDFactory struct {
	lastCreatedAt string
	count         int
}

func (f *countingIDFactory) GenerateUsageRecordID(createdAt string) string {
	f.count++
	f.lastCreatedAt = createdAt
	return "usage_test_id_" + itoa(f.count)
}

func TestNormalizeUsageRecordInputDefaults(t *testing.T) {
	clock := fixedClock{ms: 1700000000123}
	factory := &countingIDFactory{}
	normalized, err := NormalizeUsageRecordInput(UsageRecordInput{
		TraceID:       "t1",
		TrafficSource: "gateway",
		Success:       true,
	}, clock, factory)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if normalized.CreatedAt != "2023-11-14T22:13:20.123Z" {
		t.Fatalf("createdAt = %q", normalized.CreatedAt)
	}
	if normalized.ID != "usage_test_id_1" || factory.count != 1 {
		t.Fatalf("id = %q factory = %+v", normalized.ID, factory)
	}
	if factory.lastCreatedAt != normalized.CreatedAt {
		t.Fatalf("factory got createdAt %q", factory.lastCreatedAt)
	}
}

func TestNormalizeUsageRecordInputInvalidCreatedAt(t *testing.T) {
	_, err := NormalizeUsageRecordInput(UsageRecordInput{
		TraceID:       "t1",
		TrafficSource: "gateway",
		Success:       true,
		CreatedAt:     "not-a-time",
	}, fixedClock{ms: 0}, nil)
	if err == nil || err.Error() != "使用记录 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeUsageRecordInputScopeRules(t *testing.T) {
	validTeamAccount := func() UsageRecordInput {
		return UsageRecordInput{
			TraceID:                          "t",
			TrafficSource:                    "gateway",
			Success:                          true,
			AccountID:                        " acc1 ",
			AccountOwnerSystemAccountID:      "sys1",
			AccountAccessType:                AccountAccessTypeAccountAuthorized,
			AccountAuthorizationID:           "authz1",
			AccountAuthorizationSourceType:   AuthorizationSourceTypeTeam,
			AccountAuthorizationSourceTeamID: "team1",
		}
	}
	tests := []struct {
		name        string
		mutate      func(input *UsageRecordInput)
		wantCleared bool
	}{
		{"valid team account", func(*UsageRecordInput) {}, false},
		{"team source without team id clears", func(input *UsageRecordInput) { input.AccountAuthorizationSourceTeamID = "" }, true},
		{"manual source with team id clears", func(input *UsageRecordInput) {
			input.AccountAuthorizationSourceType = AuthorizationSourceTypeManual
		}, true},
		{"authorized without authz id clears", func(input *UsageRecordInput) { input.AccountAuthorizationID = "" }, true},
		{"invalid access type clears", func(input *UsageRecordInput) { input.AccountAccessType = "weird" }, true},
		{"whitespace trimmed", func(input *UsageRecordInput) {}, false},
		{"owner type drops authz fields", func(input *UsageRecordInput) {
			input.AccountAccessType = AccountAccessTypeOwner
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validTeamAccount()
			tt.mutate(&input)
			normalized, err := NormalizeUsageRecordInput(input, fixedClock{ms: 0}, nil)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantCleared && normalized.AccountID != "" {
				t.Fatalf("expected account scope cleared, got %+v", normalized)
			}
			if !tt.wantCleared {
				if normalized.AccountID != "acc1" {
					t.Fatalf("account id = %q", normalized.AccountID)
				}
				if normalized.AccountAccessType == AccountAccessTypeOwner {
					if normalized.AccountAuthorizationID != "" {
						t.Fatalf("owner must drop authz id: %+v", normalized)
					}
				}
			}
		})
	}
}

func TestNormalizeUsageRecordInputGroupRules(t *testing.T) {
	t.Run("valid authorized group", func(t *testing.T) {
		normalized, err := NormalizeUsageRecordInput(UsageRecordInput{
			TraceID:                      "t",
			TrafficSource:                "gateway",
			Success:                      true,
			GroupID:                      "g1",
			GroupOwnerSystemAccountID:    "sys1",
			GroupAccessType:              GroupAccessTypeAuthorized,
			GroupAuthorizationID:         "ga1",
			GroupAuthorizationSourceType: AuthorizationSourceTypeManual,
		}, fixedClock{ms: 0}, nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if normalized.GroupID != "g1" || normalized.GroupAuthorizationID != "ga1" {
			t.Fatalf("got %+v", normalized)
		}
	})
	t.Run("missing owner clears group", func(t *testing.T) {
		normalized, _ := NormalizeUsageRecordInput(UsageRecordInput{
			TraceID:          "t",
			TrafficSource:    "gateway",
			Success:          true,
			GroupID:          "g1",
			GroupAccessType:  GroupAccessTypeAuthorized,
		}, fixedClock{ms: 0}, nil)
		if normalized.GroupID != "" || normalized.GroupAccessType != "" {
			t.Fatalf("expected cleared, got %+v", normalized)
		}
	})
	t.Run("group_authorized account requires authorized group", func(t *testing.T) {
		normalized, _ := NormalizeUsageRecordInput(UsageRecordInput{
			TraceID:                     "t",
			TrafficSource:               "gateway",
			Success:                     true,
			AccountID:                   "a1",
			AccountOwnerSystemAccountID: "sys1",
			AccountAccessType:           AccountAccessTypeGroupAuthorized,
			GroupID:                     "g1",
			GroupOwnerSystemAccountID:   "sys1",
			GroupAccessType:             GroupAccessTypeOwner,
		}, fixedClock{ms: 0}, nil)
		if normalized.AccountID != "" || normalized.AccountAccessType != "" {
			t.Fatalf("expected account scope cleared, got %+v", normalized)
		}
		if normalized.GroupID != "g1" {
			t.Fatalf("group scope must survive, got %+v", normalized)
		}
	})
}

func TestBoundUsageRecordSnapshot(t *testing.T) {
	t.Run("nil stays nil", func(t *testing.T) {
		if BoundUsageRecordSnapshot(nil) != nil {
			t.Fatal("expected nil")
		}
	})
	t.Run("string truncation suffix", func(t *testing.T) {
		long := strings.Repeat("a", usageSnapshotMaxStringBytes+100)
		bounded := BoundUsageRecordSnapshot(long).(string)
		if !strings.HasSuffix(bounded, " bytes]") || !strings.Contains(bounded, "...[truncated ") {
			t.Fatalf("missing suffix: %d chars, tail %q", len(bounded), bounded[len(bounded)-30:])
		}
	})
	t.Run("array item cap", func(t *testing.T) {
		items := make([]any, usageSnapshotMaxArrayItems+3)
		for index := range items {
			items[index] = "x"
		}
		bounded := BoundUsageRecordSnapshot(items).([]any)
		if len(bounded) != usageSnapshotMaxArrayItems+1 {
			t.Fatalf("len = %d", len(bounded))
		}
		last := bounded[usageSnapshotMaxArrayItems].(string)
		if last != "[3 items truncated]" {
			t.Fatalf("marker = %q", last)
		}
	})
	t.Run("object key cap marks _truncated", func(t *testing.T) {
		object := NewOrderedObject()
		for index := 0; index < usageSnapshotMaxObjectKeys+2; index++ {
			object.Set("k"+itoa(index), index)
		}
		bounded := BoundUsageRecordSnapshot(object).(*OrderedObject)
		if bounded.Get("_truncated") != true {
			t.Fatal("expected _truncated marker")
		}
		if bounded.Len() != usageSnapshotMaxObjectKeys+1 {
			t.Fatalf("keys = %d", bounded.Len())
		}
	})
	t.Run("depth truncation", func(t *testing.T) {
		var nested any = "leaf"
		for index := 0; index < 10; index++ {
			nested = []any{nested}
		}
		bounded := BoundUsageRecordSnapshot(nested).([]any)
		rendered := renderDiagnostic(bounded)
		if !strings.Contains(rendered, "[depth_truncated]") {
			t.Fatalf("got %q", rendered)
		}
	})
	t.Run("buffer descriptor", func(t *testing.T) {
		bounded := BoundUsageRecordSnapshot([]byte("0123456789")).(*OrderedObject)
		if bounded.Get("_buffer") != true || bounded.Get("bytes") != 10 || bounded.Get("truncated") != false {
			t.Fatalf("got %v", bounded.Keys())
		}
	})
	t.Run("bytes budget marks _truncated", func(t *testing.T) {
		object := NewOrderedObject()
		object.Set("big", strings.Repeat("a", usageSnapshotMaxStringBytes*3))
		object.Set("small", "value")
		bounded := BoundUsageRecordSnapshot(object).(*OrderedObject)
		if bounded.Get("_truncated") != true {
			t.Fatal("expected _truncated after byte budget")
		}
	})
	t.Run("circular array", func(t *testing.T) {
		item := make([]any, 1)
		item[0] = item
		bounded := BoundUsageRecordSnapshot(item).([]any)
		if bounded[0] != "[circular]" {
			t.Fatalf("got %v", bounded[0])
		}
	})
}

func TestFailedUpstreamAttemptAttribution(t *testing.T) {
	tests := []struct {
		url         string
		override    UsageFailureAttribution
		want        UsageFailureAttribution
	}{
		{"https://api.example.com/v1", "", FailureAttributionAccountUpstream},
		{"concurrency:limit", "", FailureAttributionGatewayCapacity},
		{"proxy:http://proxy:8080", "", FailureAttributionAccountDependency},
		{"account:preparation", "", FailureAttributionAccountDependency},
		{"openai-oauth-codex:local-validation", "", FailureAttributionAccountDependency},
		{"gateway:local-validation", "", FailureAttributionAccountDependency},
		{"gateway:policy-rejected", "", FailureAttributionGatewayPolicy},
		{"account:unschedulable", "", FailureAttributionGatewayPolicy},
		{"anything", "downstream_closed", FailureAttributionDownstreamClosed},
	}
	for _, tt := range tests {
		t.Run(tt.url+"→"+tt.want, func(t *testing.T) {
			if got := failedUpstreamAttemptAttribution(tt.url, tt.override); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestBuildGatewayLogErrorMessage(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := BuildGatewayLogErrorMessage("")
		if got.ErrorMessage != "" || got.ErrorMessageBytes != 0 || got.ErrorMessageTruncated {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("within budget", func(t *testing.T) {
		text := "网关失败"
		got := BuildGatewayLogErrorMessage(text)
		if got.ErrorMessage != text || got.ErrorMessageBytes != len(text) || got.ErrorMessageTruncated {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("truncated with suffix", func(t *testing.T) {
		text := strings.Repeat("a", gatewayLogErrorMessageMaxBytes+100)
		got := BuildGatewayLogErrorMessage(text)
		if !got.ErrorMessageTruncated {
			t.Fatal("expected truncated")
		}
		if got.ErrorMessageBytes != len(text) {
			t.Fatalf("bytes = %d", got.ErrorMessageBytes)
		}
		if !strings.HasSuffix(got.ErrorMessage, " bytes]") {
			t.Fatalf("suffix missing: %q", got.ErrorMessage[len(got.ErrorMessage)-40:])
		}
		if len(got.ErrorMessage) > gatewayLogErrorMessageMaxBytes {
			t.Fatalf("log message %d exceeds budget", len(got.ErrorMessage))
		}
	})
}

func TestEstimateUsageRecordBytes(t *testing.T) {
	input := UsageRecordInput{TraceID: "t", TrafficSource: "gateway", Success: true}
	got := EstimateUsageRecordBytes(input, 1024*1024)
	if got <= 256 {
		t.Fatalf("got %d", got)
	}
}

func TestHeadersToObject(t *testing.T) {
	header := map[string][]string{
		"Single": {"value"},
		"Multi":  {"a", "b"},
		"Empty":  {},
	}
	got := HeadersToObject(header)
	if got["Single"] != "value" {
		t.Fatalf("single = %v", got["Single"])
	}
	multi, ok := got["Multi"].([]any)
	if !ok || len(multi) != 2 || multi[0] != "a" || multi[1] != "b" {
		t.Fatalf("multi = %v", got["Multi"])
	}
	if _, exists := got["Empty"]; exists {
		t.Fatal("empty header must be skipped")
	}
}

func TestBuildUsageRequestSnapshot(t *testing.T) {
	t.Run("body state wins over raw body", func(t *testing.T) {
		rawBody := NewOrderedObject()
		rawBody.Set("service_tier", "flex")
		rawBody.Set("reasoning_effort", "high")
		snapshot := BuildUsageRequestSnapshot(BuildUsageRequestSnapshotInput{
			Method:      "POST",
			Path:        "/v1/chat/completions",
			OriginalURL: "/v1/chat/completions?x=1",
			TraceID:     "trace-1",
			BodyState:   &RequestSnapshotBodyState{ServiceTier: "priority"},
			RawBody:     rawBody,
			Headers:     map[string]any{"content-type": "application/json"},
		})
		if snapshot.RequestedServiceTier != "priority" {
			t.Fatalf("tier = %q", snapshot.RequestedServiceTier)
		}
		if snapshot.RequestedReasoningEffort != "high" {
			t.Fatalf("effort = %q", snapshot.RequestedReasoningEffort)
		}
		if snapshot.Body != rawBody {
			t.Fatal("body must fall back to raw body without summary")
		}
	})
	t.Run("nested reasoning effort", func(t *testing.T) {
		rawBody := NewOrderedObject()
		nested := NewOrderedObject()
		nested.Set("effort", "low")
		rawBody.Set("reasoning", nested)
		snapshot := BuildUsageRequestSnapshot(BuildUsageRequestSnapshotInput{
			Method:  "POST",
			Path:    "/v1/responses",
			TraceID: "trace-2",
			RawBody: rawBody,
		})
		if snapshot.RequestedReasoningEffort != "low" {
			t.Fatalf("effort = %q", snapshot.RequestedReasoningEffort)
		}
		if snapshot.RequestedServiceTier != "default" {
			t.Fatalf("tier default expected, got %q", snapshot.RequestedServiceTier)
		}
	})
	t.Run("summary wins over raw body", func(t *testing.T) {
		summary := NewOrderedObject().Set("omitted", true)
		snapshot := BuildUsageRequestSnapshot(BuildUsageRequestSnapshotInput{
			Method:      "POST",
			Path:        "/v1/x",
			TraceID:     "trace-3",
			BodySummary: summary,
		})
		if snapshot.Body != summary {
			t.Fatal("summary must win")
		}
	})
}

func TestBuildGatewayErrorResponseSnapshot(t *testing.T) {
	payload := NewOrderedObject()
	err0 := NewOrderedObject()
	err0.Set("message", "boom")
	err0.Set("type", "invalid_request_error")
	payload.Set("error", err0)
	status := 502
	snapshot := BuildGatewayErrorResponseSnapshot(status, payload, &UpstreamAttempt{
		AccountID:    "acc1",
		AccountName:  "account one",
		UpstreamURL:  "https://upstream/v1",
		Status:       &status,
		Message:      "upstream said no",
		ResponseBodyText: "raw",
	})
	if snapshot.ErrorMessage != "boom" || snapshot.GeneratedBy != GeneratedByGateway {
		t.Fatalf("got %+v", snapshot)
	}
	if snapshot.Headers["content-type"] != "application/json; charset=utf-8" {
		t.Fatalf("headers = %v", snapshot.Headers)
	}
	if !strings.Contains(snapshot.BodyText, "\"message\":\"boom\"") {
		t.Fatalf("bodyText = %q", snapshot.BodyText)
	}
	if snapshot.LastUpstream == nil || snapshot.LastUpstream.AccountID != "acc1" || snapshot.LastUpstream.StatusCode != &status {
		t.Fatalf("last attempt = %+v", snapshot.LastUpstream)
	}
}
