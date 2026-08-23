package proxylatency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManualRequestValidationFailsClosed(t *testing.T) {
	base := ManualRequest{
		SchemaVersion:  1,
		ProxyID:        "proxy-manual",
		ProxyName:      "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z",
		ProxyType:      "http",
		ProxyHost:      "127.0.0.1",
		ProxyPort:      8080,
		Targets: []ManualTarget{{
			Provider: "openai", ProfileID: "profile-openai", Name: "OpenAI", URL: "https://api.openai.com/v1",
		}},
		DeadlineMS: 25_000,
	}
	if err := base.Validate(25 * time.Second); err != nil {
		t.Fatalf("valid manual request rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ManualRequest)
		want   string
	}{
		{name: "duplicate provider", mutate: func(request *ManualRequest) {
			request.Targets = append(request.Targets, ManualTarget{Provider: "openai", ProfileID: "other", Name: "Other", URL: "https://example.com"})
		}, want: "重复"},
		{name: "deadline too long", mutate: func(request *ManualRequest) { request.DeadlineMS = 25_001 }, want: "deadline"},
		{name: "invalid config revision", mutate: func(request *ManualRequest) { request.ConfigRevision = "not-a-time" }, want: "config_revision"},
		{name: "password without username", mutate: func(request *ManualRequest) {
			request.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:iv:tag:ciphertext"}
		}, want: "username"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Targets = append([]ManualTarget(nil), base.Targets...)
			test.mutate(&request)
			if err := request.Validate(25 * time.Second); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestManualReportPreservesProviderTargetURLAndSyntheticOptionality(t *testing.T) {
	request := ManualRequest{
		SchemaVersion:  1,
		ProxyID:        "proxy-manual",
		ProxyName:      "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z",
		ProxyType:      "http",
		ProxyHost:      "127.0.0.1",
		ProxyPort:      8080,
		Targets: []ManualTarget{
			{Provider: "openai", ProfileID: "profile-openai", Name: "OpenAI", URL: "https://api.openai.com/v1"},
			{Provider: "gemini", ProfileID: "profile-gemini", Name: "Gemini", URL: "https://generativelanguage.googleapis.com/v1"},
		},
	}
	testedAt := time.Date(2026, 8, 23, 0, 0, 5, 123456000, time.UTC)
	report := request.Report(Outcome{
		ObservedAt: testedAt,
		Items: []ItemResult{
			{Provider: "openai", ProfileID: "profile-openai", Status: ItemPassed, Outcome: OutcomeSuccess, LatencyMS: 40, HTTPStatus: 200},
			{Provider: "gemini", ProfileID: "profile-gemini", Status: ItemUnknown, Outcome: OutcomeProbeTaskFailure, ErrorCode: "deadline"},
		},
	})
	if report.Status != OverallWarning || report.PassedCount != 1 || report.WarningCount != 1 || report.FailedCount != 0 {
		t.Fatalf("unexpected manual summary: %+v", report)
	}
	if report.Score != 90 || report.Grade != "A" || report.Message != "代理可用，存在 1 项告警" {
		t.Fatalf("unexpected manual score/grade/message: %+v", report)
	}
	if len(report.Items) != 3 || report.Items[0].TargetURL != "" {
		t.Fatalf("synthetic base item must be first and omit targetUrl: %+v", report.Items)
	}
	if report.Items[1].TargetURL != request.Targets[0].URL || report.Items[2].TargetURL != request.Targets[1].URL {
		t.Fatalf("provider targetUrl was lost: %+v", report.Items)
	}
	if report.Items[2].HTTPStatus != nil || report.Items[2].LatencyMS != nil {
		t.Fatalf("unknown provider item must omit httpStatus/latencyMs: %+v", report.Items[2])
	}
}

func TestManualInputDraftUsesManualTriggerAndDeadline(t *testing.T) {
	request := ManualRequest{
		SchemaVersion:  1,
		ProxyID:        "proxy-manual",
		ProxyName:      "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z",
		ProxyType:      "http",
		ProxyHost:      "127.0.0.1",
		ProxyPort:      8080,
		Targets:        []ManualTarget{{Provider: "openai", ProfileID: "profile-openai", Name: "OpenAI", URL: "https://api.openai.com/v1"}},
	}
	now := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	draft := request.InputDraft(now, 3*time.Second)
	if draft.Trigger != TriggerManual || draft.ExpiresAt.Sub(draft.IssuedAt) != 3*time.Second || len(draft.Targets) != 1 {
		t.Fatalf("manual draft mismatch: %+v", draft)
	}
}

func TestManualReportAllowsNoProviderSyntheticUnknown(t *testing.T) {
	request := ManualRequest{
		SchemaVersion:  1,
		ProxyID:        "proxy-no-provider",
		ProxyName:      "No provider proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z",
		ProxyType:      "http",
		ProxyHost:      "127.0.0.1",
		ProxyPort:      8080,
	}
	if err := request.Validate(25 * time.Second); err != nil {
		t.Fatalf("no-provider manual request should remain valid: %v", err)
	}
	report := request.Report(Outcome{ProxyID: request.ProxyID, ObservedAt: time.Date(2026, 8, 23, 0, 0, 5, 0, time.UTC), OverallStatus: OverallUnknown})
	if report.Status != OverallUnknown || report.Message != "代理检测未形成有效传输尝试" || len(report.Items) != 1 || report.Items[0].TargetURL != "" {
		t.Fatalf("unexpected no-provider manual report: %+v", report)
	}
}

func TestManualOutcomeRejectsUndeclaredProvider(t *testing.T) {
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "proxy-manual", ProxyName: "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []ManualTarget{{Provider: "openai", ProfileID: "profile-openai", Name: "OpenAI", URL: "https://api.openai.com/v1"}},
	}
	err := request.ValidateOutcome(Outcome{
		ProxyID: request.ProxyID, Trigger: TriggerManual, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "unknown", ProfileID: "profile-unknown", Status: ItemPassed, Outcome: OutcomeSuccess}},
	})
	if err == nil || !strings.Contains(err.Error(), "未声明") {
		t.Fatalf("undeclared provider must fail closed: %v", err)
	}
}

func TestManualOutcomeRejectsMissingProviderItem(t *testing.T) {
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "proxy-manual", ProxyName: "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []ManualTarget{
			{Provider: "openai", ProfileID: "profile-openai", Name: "OpenAI", URL: "https://api.openai.com/v1"},
			{Provider: "gemini", ProfileID: "profile-gemini", Name: "Gemini", URL: "https://generativelanguage.googleapis.com/v1"},
		},
	}
	err := request.ValidateOutcome(Outcome{
		ProxyID: request.ProxyID, Trigger: TriggerManual, OverallStatus: OverallPassed,
		Items: []ItemResult{{Provider: "openai", ProfileID: "profile-openai", Status: ItemPassed, Outcome: OutcomeSuccess}},
	})
	if err == nil || !strings.Contains(err.Error(), "未覆盖全部 provider") {
		t.Fatalf("missing provider item must fail closed: %v", err)
	}
}

func TestManualOutboundProbeUsesFirstSuccessfulFallback(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.String() == "" {
			t.Fatalf("unexpected outbound proxy request: method=%s url=%s", request.Method, request.URL.String())
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"success","query":"203.0.113.7","country":"中国","countryCode":"CN","regionName":"北京"}`))
	}))
	defer proxyServer.Close()
	info, ok := probeManualOutbound(context.Background(), proxyServer.URL, 2*time.Second)
	if !ok || info.IP != "203.0.113.7" || info.Region != "中国" {
		t.Fatalf("manual outbound fallback mismatch: ok=%v info=%+v", ok, info)
	}
}
