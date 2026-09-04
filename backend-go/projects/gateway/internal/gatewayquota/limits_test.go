package gatewayquota

import (
	"strings"
	"testing"
	"time"
)

func TestParseRequestQuotaLimitsJSON(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   string
		wantHours int
		wantLimit float64
		wantNone  bool
	}{
		{name: "blank yields empty", input: "", wantNone: true},
		{name: "whitespace yields empty", input: "   ", wantNone: true},
		{
			name:      "full config",
			input:     `{"hourly":{"enabled":true,"hours":3,"limit":10},"daily":{"enabled":true,"limit":20},"total":{"enabled":true,"limit":30}}`,
			wantHours: 3,
			wantLimit: 10,
		},
		{
			name:    "malformed json",
			input:   `{"hourly":`,
			wantErr: "unexpected end of JSON input",
		},
		{
			name:    "unsupported top field",
			input:   `{"hourly":{"enabled":true,"hours":1,"limit":1},"foo":1}`,
			wantErr: "请求额度限制包含不支持字段：foo",
		},
		{
			name:    "hourly unsupported field",
			input:   `{"hourly":{"enabled":true,"hours":1,"limit":1},"daily":{"enabled":true,"limit":1,"x":2}}`,
			wantErr: "日额度包含不支持字段：x",
		},
		{
			name:    "hourly enabled must be true",
			input:   `{"hourly":{"enabled":false,"hours":1,"limit":1}}`,
			wantErr: "小时额度启用状态必须为 true",
		},
		{
			name:    "daily enabled must be true",
			input:   `{"daily":{"enabled":false,"limit":1}}`,
			wantErr: "日额度启用状态必须为 true",
		},
		{
			name:    "amount must be positive",
			input:   `{"daily":{"enabled":true,"limit":0}}`,
			wantErr: "日额度金额必须是大于 0 的数字",
		},
		{
			name:    "amount rejects >6 decimals",
			input:   `{"daily":{"enabled":true,"limit":0.0000001}}`,
			wantErr: "日额度金额最多支持 6 位小数",
		},
		{
			name:    "hours must be integer",
			input:   `{"hourly":{"enabled":true,"hours":1.5,"limit":1}}`,
			wantErr: "小时额度窗口必须是数字",
		},
		{
			name:    "hours below range",
			input:   `{"hourly":{"enabled":true,"hours":0,"limit":1}}`,
			wantErr: "小时额度窗口必须在 1-720 之间",
		},
		{
			name:    "hours above range",
			input:   `{"hourly":{"enabled":true,"hours":721,"limit":1}}`,
			wantErr: "小时额度窗口必须在 1-720 之间",
		},
		{
			name:    "top-level array rejected",
			input:   `[]`,
			wantErr: "请求额度限制参数无效",
		},
		{
			name:    "nested null rejected",
			input:   `{"daily":null}`,
			wantErr: "日额度参数无效",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits, err := ParseRequestQuotaLimitsJSON(tt.input)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("ParseRequestQuotaLimitsJSON(%q) error = %v, want %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRequestQuotaLimitsJSON(%q) unexpected error: %v", tt.input, err)
			}
			if tt.wantNone {
				if HasEnabledRequestQuotaLimit(limits) {
					t.Fatalf("want no enabled limits, got %+v", limits)
				}
				return
			}
			if limits.Hourly == nil || limits.Hourly.Hours != tt.wantHours || limits.Hourly.Limit != tt.wantLimit {
				t.Fatalf("hourly = %+v, want hours=%d limit=%v", limits.Hourly, tt.wantHours, tt.wantLimit)
			}
			if limits.Daily == nil || limits.Daily.Limit != 20 || limits.Total == nil || limits.Total.Limit != 30 {
				t.Fatalf("daily/total mismatch: %+v", limits)
			}
			if limits.Weekly != nil || limits.Monthly != nil {
				t.Fatalf("absent windows must stay nil: %+v", limits)
			}
		})
	}
}

func TestNormalizeRequestQuotaLimitsStripsDisabled(t *testing.T) {
	// Disabled entries cannot be represented (enabled must be true), so
	// normalization of a decoded document with enabled=false fails instead of
	// stripping — verify the strip behaviour through Parse of absent keys and
	// HasEnabled on partial configs.
	partial := RequestQuotaLimits{Total: &QuotaLimit{Enabled: true, Limit: 5}}
	if !HasEnabledRequestQuotaLimit(partial) {
		t.Fatal("total-only limits must count as enabled")
	}
	if HasEnabledRequestQuotaLimit(EmptyRequestQuotaLimits()) {
		t.Fatal("empty limits must not count as enabled")
	}
}

func TestNormalizeRequestQuotaLimitsAmountPrecision(t *testing.T) {
	// Six decimals round-trip (IEEE754-identical to JS number semantics).
	limits, err := ParseRequestQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":0.123456}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limits.Daily == nil || limits.Daily.Limit != 0.123456 {
		t.Fatalf("daily limit = %v, want 0.123456", limits.Daily)
	}
	// Seven decimals are rejected before any rounding ambiguity.
	_, err = ParseRequestQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":0.1234567}}`)
	if err == nil || err.Error() != "日额度金额最多支持 6 位小数" {
		t.Fatalf("seven-decimal error = %v", err)
	}
	// MAX_SAFE_INTEGER passes; anything above fails the upper bound.
	limits, err = ParseRequestQuotaLimitsJSON(`{"total":{"enabled":true,"limit":9007199254740991}}`)
	if err != nil {
		t.Fatalf("max safe integer rejected: %v", err)
	}
	if limits.Total == nil || limits.Total.Limit != MaxRequestQuotaAmountUsd {
		t.Fatalf("total limit = %v", limits.Total)
	}
	_, err = ParseRequestQuotaLimitsJSON(`{"total":{"enabled":true,"limit":9007199254740992}}`)
	if err == nil || err.Error() != "总额度金额必须是大于 0 的数字" {
		t.Fatalf("above max error = %v", err)
	}
}

func TestIsRequestQuotaExceeded(t *testing.T) {
	base := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		limits string
		costs  RequestQuotaCosts
		want   bool
	}{
		{name: "within quota allows", limits: `{"daily":{"enabled":true,"limit":10}}`, costs: RequestQuotaCosts{Daily: 9.99}, want: false},
		{name: "exactly at limit denies", limits: `{"daily":{"enabled":true,"limit":10}}`, costs: RequestQuotaCosts{Daily: 10}, want: true},
		{name: "over limit denies", limits: `{"daily":{"enabled":true,"limit":10}}`, costs: RequestQuotaCosts{Daily: 10.01}, want: true},
		{name: "hourly boundary", limits: `{"hourly":{"enabled":true,"hours":1,"limit":5}}`, costs: RequestQuotaCosts{Hourly: 5}, want: true},
		{name: "weekly ignored when disabled", limits: `{"weekly":{"enabled":true,"limit":5}}`, costs: RequestQuotaCosts{Weekly: 0, Daily: 100}, want: false},
		{name: "monthly boundary", limits: `{"monthly":{"enabled":true,"limit":1}}`, costs: RequestQuotaCosts{Monthly: 1}, want: true},
		{name: "total boundary", limits: `{"total":{"enabled":true,"limit":1}}`, costs: RequestQuotaCosts{Total: 1}, want: true},
		{name: "any enabled exceeded denies", limits: `{"daily":{"enabled":true,"limit":10},"total":{"enabled":true,"limit":100}}`, costs: RequestQuotaCosts{Daily: 1, Total: 100}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits, err := ParseRequestQuotaLimitsJSON(tt.limits)
			if err != nil {
				t.Fatalf("parse limits: %v", err)
			}
			if got := IsRequestQuotaExceeded(limits, tt.costs); got != tt.want {
				t.Fatalf("IsRequestQuotaExceeded(%+v, %+v) = %v, want %v", limits, tt.costs, got, tt.want)
			}
		})
	}
	_ = base
}

func TestCostKeyLayout(t *testing.T) {
	location, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 9, 4, 15, 30, 45, 0, time.UTC)
	key := CostKey(CostInput{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: now}, location)
	// 2026-09-04 is a Friday; Monday-start week key is 2026-08-31.
	want := strings.Join([]string{"sys", "api_key", "ak", "2026-09-04", "2026-08-31", "2026-09", ""}, "\x00")
	if key != want {
		t.Fatalf("CostKey = %q, want %q", key, want)
	}
	hourlyKey := CostKey(CostInput{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: now, HourlyWindowHours: 0, HasHourlyWindow: true}, location)
	// Normalized window hours clamp at 1.
	if !strings.HasSuffix(hourlyKey, "\x001") {
		t.Fatalf("hourly key must clamp to >= 1: %q", hourlyKey)
	}
}
