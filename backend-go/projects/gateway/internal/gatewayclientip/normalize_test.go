package gatewayclientip

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeClientIPForStatsTable(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantNil        bool
		wantClientIP   string
		wantAggregate  string
	}{
		{name: "plain ipv4", input: "192.168.1.7", wantClientIP: "192.168.1.7", wantAggregate: "192.168.1.7"},
		{name: "trim whitespace", input: " 10.0.0.1 ", wantClientIP: "10.0.0.1", wantAggregate: "10.0.0.1"},
		{name: "comma list takes first", input: "10.0.0.2, 10.0.0.3", wantClientIP: "10.0.0.2", wantAggregate: "10.0.0.2"},
		{name: "zone index stripped", input: "10.0.0.4%eth0", wantClientIP: "10.0.0.4", wantAggregate: "10.0.0.4"},
		{name: "bracketed ipv4", input: "[10.0.0.5]", wantClientIP: "10.0.0.5", wantAggregate: "10.0.0.5"},
		{name: "ipv4 with port", input: "10.0.0.6:8080", wantClientIP: "10.0.0.6", wantAggregate: "10.0.0.6"},
		{name: "ipv6 mapped stripped", input: "::ffff:10.0.0.7", wantClientIP: "10.0.0.7", wantAggregate: "10.0.0.7"},
		{name: "ipv6 rejected", input: "2001:db8::1", wantNil: true},
		{name: "ipv6 with port rejected", input: "[2001:db8::1]:443", wantNil: true},
		{name: "garbage rejected", input: "not-an-ip", wantNil: true},
		{name: "empty rejected", input: "", wantNil: true},
		{name: "overflow octet rejected", input: "256.1.1.1", wantNil: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized := NormalizeClientIPForStats(tc.input)
			if tc.wantNil {
				if normalized != nil {
					t.Fatalf("expected nil, got %+v", normalized)
				}
				return
			}
			if normalized == nil {
				t.Fatalf("expected normalization, got nil")
			}
			if normalized.ClientIP != tc.wantClientIP || normalized.AggregateIPKey != tc.wantAggregate {
				t.Fatalf("clientIP=%q aggregate=%q, want %q/%q", normalized.ClientIP, normalized.AggregateIPKey, tc.wantClientIP, tc.wantAggregate)
			}
			if normalized.IPHash == "" || len(normalized.IPHash) != 64 {
				t.Fatalf("ipHash must be sha256 hex: %q", normalized.IPHash)
			}
			wantBucket := bucketOf(normalized.IPHash)
			if normalized.BucketNo != wantBucket {
				t.Fatalf("bucketNo=%d want %d", normalized.BucketNo, wantBucket)
			}
			if normalized.IPVersion != 4 {
				t.Fatalf("ipVersion=%d want 4", normalized.IPVersion)
			}
		})
	}
}

func TestNormalizeIPHashForRuntimeTable(t *testing.T) {
	valid := strings.Repeat("a", 64)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "valid uppercased", input: strings.Repeat("A", 64), want: valid},
		{name: "valid trimmed", input: " " + valid + " ", want: valid},
		{name: "short", input: strings.Repeat("a", 63), want: ""},
		{name: "non hex", input: strings.Repeat("g", 64), want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeIPHashForRuntime(tc.input); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveGroupSchedulingPolicyTable(t *testing.T) {
	tests := []struct {
		name       string
		value      map[string]any
		defaults   HighConcurrencyPolicyDefaults
		wantMaxWait int64
		wantMaxSize int
		wantPerKey  int
		wantLimit   int
		wantMode   string
		wantErr    string
	}{
		{
			name:        "defaults",
			value:       nil,
			defaults:    HighConcurrencyPolicyDefaults{MaxQueueSize: 40, PerAPIKeyQueueLimit: 40},
			wantMaxWait: 60_000, wantMaxSize: 40, wantPerKey: 40, wantLimit: 0, wantMode: "reject",
		},
		{
			name: "explicit values",
			value: map[string]any{
				"maxQueueWaitMs": float64(1_000), "maxQueueSize": float64(5), "perApiKeyQueueLimit": float64(2),
				"clientIpConcurrencyLimit": float64(3), "clientIpConcurrencyOverflowMode": "queue",
			},
			wantMaxWait: 1_000, wantMaxSize: 5, wantPerKey: 2, wantLimit: 3, wantMode: "queue",
		},
		{
			name:       "per key falls back to max queue size",
			value:      map[string]any{"maxQueueSize": float64(7)},
			wantMaxWait: 60_000, wantMaxSize: 7, wantPerKey: 7, wantLimit: 0, wantMode: "reject",
		},
		{
			name:    "invalid wait",
			value:   map[string]any{"maxQueueWaitMs": float64(3_600_001)},
			wantErr: "分组调度策略 maxQueueWaitMs 必须在 1-3600000 之间",
		},
		{
			name:    "non integer",
			value:   map[string]any{"maxQueueSize": 1.5},
			wantErr: "分组调度策略 maxQueueSize 必须是整数",
		},
		{
			name:    "invalid overflow mode",
			value:   map[string]any{"clientIpConcurrencyOverflowMode": "boom"},
			wantErr: "分组调度策略 clientIpConcurrencyOverflowMode 无效",
		},
		{
			name:    "per key above max",
			value:   map[string]any{"maxQueueSize": float64(3), "perApiKeyQueueLimit": float64(4)},
			wantErr: "分组调度策略 perApiKeyQueueLimit 必须在 1-3 之间",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := resolveGroupSchedulingPolicy(tc.value, tc.defaults)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err=%v want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy.MaxQueueWaitMs != tc.wantMaxWait || policy.MaxQueueSize != tc.wantMaxSize ||
				policy.PerAPIKeyQueueLimit != tc.wantPerKey || policy.ClientIPConcurrencyLimit != tc.wantLimit ||
				policy.ClientIPConcurrencyOverflowMode != tc.wantMode {
				t.Fatalf("policy=%+v", policy)
			}
		})
	}
}

func TestEffectiveImageLaneConcurrencyLimit(t *testing.T) {
	policy := GroupSchedulingPolicy{ImageLaneMaxConcurrency: 2}
	if got := EffectiveImageLaneConcurrencyLimit(5, policy); got != 2 {
		t.Fatalf("got %d want 2", got)
	}
	if got := EffectiveImageLaneConcurrencyLimit(1, policy); got != 1 {
		t.Fatalf("got %d want 1 (hard limit floor)", got)
	}
	if got := EffectiveImageLaneConcurrencyLimit(5, GroupSchedulingPolicy{}); got != 5 {
		t.Fatalf("got %d want 5 (no lane cap)", got)
	}
}

func TestRFC3339Helpers(t *testing.T) {
	millis, ok := rfc3339Millis("2026-09-04T12:00:00.500Z")
	if !ok {
		t.Fatal("expected parse")
	}
	parsed := time.UnixMilli(millis)
	if parsed.UTC().Format("2006-01-02T15:04:05.000") != "2026-09-04T12:00:00.500" {
		t.Fatalf("round trip mismatch: %s", parsed)
	}
	if _, ok := rfc3339Millis("2026-09-04T12:00:00"); ok {
		t.Fatal("bare datetime must fail")
	}
	if _, err := requiredRFC3339Millis("bogus", "Client-IP 策略 expiresAt"); err == nil ||
		err.Error() != "Client-IP 策略 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("err=%v", err)
	}
	if got := canonicalRFC3339(parsed); got != "2026-09-04T12:00:00.500Z" {
		t.Fatalf("canonical=%q", got)
	}
}

func TestBucketOfMatchesNodeFormula(t *testing.T) {
	// parseInt(ipHash.slice(0,8), 16) % 4096
	if got := bucketOf("00000fff" + strings.Repeat("0", 56)); got != 4095 {
		t.Fatalf("got %d", got)
	}
	if got := bucketOf("00001000" + strings.Repeat("0", 56)); got != 0 {
		t.Fatalf("got %d", got)
	}
}

func bucketOf(ipHash string) int {
	value := int64(0)
	for i := 0; i < 8; i++ {
		value = value*16 + int64(hexDigit(ipHash[i]))
	}
	return int(value % ClientIPRegistryBucketCount)
}

func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}
