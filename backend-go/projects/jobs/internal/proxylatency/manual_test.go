package proxylatency

import (
	"context"
	"encoding/json"
	"fmt"
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
	if report.TestedAt != "2026-08-23T00:00:05.123Z" {
		t.Fatalf("manual report must match Node millisecond ISO timestamp: %q", report.TestedAt)
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

func TestManualReportPreservesZeroPassedLatencyAndLegacyHTTPStatusMessage(t *testing.T) {
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "proxy-zero-latency", ProxyName: "Zero latency proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []ManualTarget{
			{Provider: "status", ProfileID: "profile-status", Name: "Status", URL: "https://status.example/v1"},
			{Provider: "failed", ProfileID: "profile-failed", Name: "Failed", URL: "https://failed.example/v1"},
			{Provider: "unknown", ProfileID: "profile-unknown", Name: "Unknown", URL: "https://unknown.example/v1"},
		},
	}
	report := request.Report(Outcome{
		ObservedAt: time.Date(2026, 8, 23, 0, 0, 1, 0, time.UTC),
		Items: []ItemResult{
			{Provider: "status", ProfileID: "profile-status", Status: ItemPassed, Outcome: OutcomeNeutral, HTTPStatus: 503, LatencyMS: 0, ErrorCode: "upstream_http_status"},
			{Provider: "failed", ProfileID: "profile-failed", Status: ItemFailed, Outcome: OutcomeUpstreamFailure, LatencyMS: 0},
			{Provider: "unknown", ProfileID: "profile-unknown", Status: ItemUnknown, Outcome: OutcomeProbeTaskFailure, LatencyMS: 0},
		},
	})
	if report.Items[0].LatencyMS == nil || *report.Items[0].LatencyMS != 0 {
		t.Fatalf("synthetic base must retain passed 0ms latency: %+v", report.Items[0])
	}
	if report.BaseLatencyMS == nil || *report.BaseLatencyMS != 0 {
		t.Fatalf("report base latency must retain passed 0ms: %v", report.BaseLatencyMS)
	}
	if report.Items[1].LatencyMS == nil || *report.Items[1].LatencyMS != 0 {
		t.Fatalf("passed provider item must retain 0ms latency: %+v", report.Items[1])
	}
	if report.Items[1].Message != "HTTP 503（传输完整，状态码仅供诊断）" {
		t.Fatalf("non-2xx manual message=%q", report.Items[1].Message)
	}
	if report.Items[2].LatencyMS != nil || report.Items[3].LatencyMS != nil {
		t.Fatalf("failed/unknown default 0ms must omit latency: failed=%+v unknown=%+v", report.Items[2], report.Items[3])
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		BaseLatencyMS *int64           `json:"baseLatencyMs"`
		Items         []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.BaseLatencyMS == nil || *payload.BaseLatencyMS != 0 {
		t.Fatalf("JSON baseLatencyMs=%v want 0: %s", payload.BaseLatencyMS, encoded)
	}
	if value, ok := payload.Items[1]["latencyMs"]; !ok || value != float64(0) {
		t.Fatalf("JSON passed latencyMs=%v want 0: %s", value, encoded)
	}
	for _, index := range []int{2, 3} {
		if _, ok := payload.Items[index]["latencyMs"]; ok {
			t.Fatalf("JSON item %d must omit default failed/unknown latency: %s", index, encoded)
		}
	}
}

func TestSyntheticBaseRoundsLatencyLikeNode(t *testing.T) {
	first := int64(10)
	second := int64(11)
	base := syntheticBase([]ProxyTestItem{
		{Status: ItemPassed, LatencyMS: &first},
		{Status: ItemPassed, LatencyMS: &second},
	}, 2)
	if base.LatencyMS == nil || *base.LatencyMS != 11 {
		t.Fatalf("synthetic base latency=%v want rounded 11", base.LatencyMS)
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

func TestManualRequestAllowsBlankProviderURLAsUnknownLikeNode(t *testing.T) {
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "proxy-manual", ProxyName: "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []ManualTarget{{Provider: "hybrid", ProfileID: "profile-hybrid", Name: "Hybrid", URL: ""}},
	}
	if err := request.Validate(25 * time.Second); err != nil {
		t.Fatalf("blank provider base URL must retain Node unknown-item behavior: %v", err)
	}
	draft := request.InputDraft(time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC), time.Minute)
	if len(draft.Targets) != 1 || draft.Targets[0].ProbeError != targetProbeErrorInvalidURL || draft.Targets[0].URL != "" {
		t.Fatalf("manual blank URL draft=%+v", draft.Targets)
	}
	report := request.Report(Outcome{ProxyID: request.ProxyID, ObservedAt: time.Date(2026, 8, 23, 0, 0, 1, 0, time.UTC), OverallStatus: OverallUnknown, Items: []ItemResult{{Provider: "hybrid", ProfileID: "profile-hybrid", Status: ItemUnknown, Outcome: OutcomeProbeTaskFailure, ErrorCode: targetProbeErrorInvalidURL}}})
	if len(report.Items) != 2 || report.Items[1].TargetURL != "" || report.Items[1].Message != "未形成真实代理检测请求：Invalid URL" {
		t.Fatalf("manual invalid provider report=%+v", report)
	}
}

func TestManualRequestMarksUnsupportedProviderURLUnknownWithoutRetainingItInTheIssuedInput(t *testing.T) {
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "proxy-manual", ProxyName: "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: []ManualTarget{{Provider: "unsupported", ProfileID: "profile-unsupported", Name: "Unsupported", URL: "ftp://provider.invalid/v1"}},
	}
	if err := request.Validate(25 * time.Second); err != nil {
		t.Fatalf("unsupported provider URL must remain an explicit Node-compatible unknown: %v", err)
	}
	draft := request.InputDraft(time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC), time.Minute)
	if len(draft.Targets) != 1 || draft.Targets[0].ProbeError != targetProbeErrorInvalidURL || draft.Targets[0].URL != "" {
		t.Fatalf("manual unsupported URL draft=%+v", draft.Targets)
	}
	report := request.Report(Outcome{ProxyID: request.ProxyID, ObservedAt: time.Date(2026, 8, 23, 0, 0, 1, 0, time.UTC), OverallStatus: OverallUnknown, Items: []ItemResult{{Provider: "unsupported", ProfileID: "profile-unsupported", Status: ItemUnknown, Outcome: OutcomeProbeTaskFailure, ErrorCode: targetProbeErrorInvalidURL}}})
	if len(report.Items) != 2 || report.Items[1].TargetURL != "ftp://provider.invalid/v1" || report.Items[1].Message != "未形成真实代理检测请求：不支持的目标协议：ftp:" {
		t.Fatalf("manual unsupported provider report=%+v", report)
	}
}

func TestLegacyManualTargetFailureMessageMatchesNodeURLOracle(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: "未形成真实代理检测请求：Invalid URL"},
		{name: "whitespace", raw: " \t ", want: "未形成真实代理检测请求：Invalid URL"},
		{name: "malformed HTTP", raw: "http://", want: "未形成真实代理检测请求：Invalid URL"},
		{name: "FTP", raw: "ftp://provider.invalid/v1", want: "未形成真实代理检测请求：不支持的目标协议：ftp:"},
		{name: "uppercase FTP", raw: "FTP://provider.invalid/v1", want: "未形成真实代理检测请求：不支持的目标协议：ftp:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := legacyManualTargetFailureMessage(test.raw); got != test.want {
				t.Fatalf("message=%q want=%q", got, test.want)
			}
		})
	}
}

func TestManualRequestDoesNotRetainNodeTargetCap(t *testing.T) {
	targets := make([]ManualTarget, 129)
	for index := range targets {
		targets[index] = ManualTarget{Provider: fmt.Sprintf("provider-%d", index), ProfileID: fmt.Sprintf("profile-%d", index), Name: fmt.Sprintf("Provider %d", index), URL: "https://example.invalid/v1"}
	}
	request := ManualRequest{
		SchemaVersion: 1, ProxyID: "proxy-manual", ProxyName: "Manual proxy",
		ConfigRevision: "2026-08-23T00:00:00.123Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080,
		Targets: targets,
	}
	if err := request.Validate(25 * time.Second); err != nil {
		t.Fatalf("Go manual request must not retain Node-era 128 target cap: %v", err)
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
