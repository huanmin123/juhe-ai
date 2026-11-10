package gatewayclientip

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeClientIPForStatsTable(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantNil       bool
		wantClientIP  string
		wantAggregate string
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
