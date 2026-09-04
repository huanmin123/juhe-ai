package statsverify

import "testing"

// TestNormalizeClientIPForStatsGolden locks the identity derivation against
// values derived from the Node source (client-ip-normalization.ts
// clientIpIdentity): ipHash = sha256("client-ip:" + aggregateIpKey),
// bucketNo = parseInt(ipHash[0:8], 16) % 4096. The golden digests were
// produced with the same primitive:
//
//	sha256("client-ip:1.2.3.4")           = e59c1b44a58270d4c90e0fbe75bd447babf294c0c250e5caa2e77a2a6b39f11e
//	sha256("client-ip:192.168.1.1")       = f891cdd2ba1d38fe75dae7b403fc4922b3541b61323bbdf3489b5b26a4910067
//	sha256("client-ip:255.255.255.255")   = 7d79e9619839b9eea9b34317014831d2f121c53293734282355093671765a590
func TestNormalizeClientIPForStatsGolden(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantNil     bool
		wantClient  string
		wantHash    string
		wantBucket  int
		wantVersion int
	}{
		{
			name:        "plain ipv4",
			input:       "1.2.3.4",
			wantClient:  "1.2.3.4",
			wantHash:    "e59c1b44a58270d4c90e0fbe75bd447babf294c0c250e5caa2e77a2a6b39f11e",
			wantBucket:  2884,
			wantVersion: 4,
		},
		{
			name:        "private ipv4",
			input:       "192.168.1.1",
			wantClient:  "192.168.1.1",
			wantHash:    "f891cdd2ba1d38fe75dae7b403fc4922b3541b61323bbdf3489b5b26a4910067",
			wantBucket:  3538,
			wantVersion: 4,
		},
		{
			name:        "max octets",
			input:       "255.255.255.255",
			wantClient:  "255.255.255.255",
			wantHash:    "7d79e9619839b9eea9b34317014831d2f121c53293734282355093671765a590",
			wantBucket:  2401,
			wantVersion: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeClientIPForStats(tc.input)
			if got == nil {
				t.Fatalf("expected normalized identity, got nil")
			}
			if got.ClientIP != tc.wantClient || got.AggregateIPKey != tc.wantClient {
				t.Fatalf("client=%q aggregate=%q, want %q", got.ClientIP, got.AggregateIPKey, tc.wantClient)
			}
			if got.IPHash != tc.wantHash {
				t.Fatalf("ipHash=%q, want %q", got.IPHash, tc.wantHash)
			}
			if got.BucketNo != tc.wantBucket {
				t.Fatalf("bucketNo=%d, want %d", got.BucketNo, tc.wantBucket)
			}
			if got.IPVersion != tc.wantVersion {
				t.Fatalf("ipVersion=%d, want %d", got.IPVersion, tc.wantVersion)
			}
		})
	}
}

// TestNormalizeClientIPForStats mirrors the plain-normalization branches of
// normalizePlainClientIp and the IPv4-only survivor rule
// (normalizeClientIpForStats returns undefined for everything that is not
// IPv4 after stripping, including IPv6 and IPv4-in-IPv6 forms).
func TestNormalizeClientIPForStats(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantNil bool
	}{
		{name: "empty", input: "", wantNil: true},
		{name: "blank", input: "   ", wantNil: true},
		{name: "trim", input: "  1.2.3.4  ", want: "1.2.3.4"},
		{name: "comma list takes first", input: "1.2.3.4, 5.6.7.8", want: "1.2.3.4"},
		{name: "zone index dropped", input: "1.2.3.4%eth0", want: "1.2.3.4"},
		{name: "brackets unwrapped", input: "[1.2.3.4]", want: "1.2.3.4"},
		{name: "ipv4 with port", input: "1.2.3.4:8443", want: "1.2.3.4"},
		{name: "ipv4-mapped prefix stripped", input: "::ffff:1.2.3.4", want: "1.2.3.4"},
		{name: "ipv4-mapped bracketed with port", input: "[::ffff:0102:0304]", wantNil: true},
		{name: "uppercase normalized", input: "AABB.", wantNil: true},
		// Node isIP rejects leading zeros (inet_pton semantics), so
		// "001.002.003.004" never survives normalization.
		{name: "leading zeros rejected", input: "001.002.003.004", wantNil: true},
		{name: "ipv6 rejected", input: "2001:db8::1", wantNil: true},
		{name: "ipv6 with brackets rejected", input: "[2001:db8::1]", wantNil: true},
		{name: "ipv6 with zone rejected", input: "fe80::1%eth0", wantNil: true},
		{name: "garbage rejected", input: "not-an-ip", wantNil: true},
		{name: "octet overflow rejected", input: "1.2.3.256", wantNil: true},
		{name: "empty first comma segment", input: ",1.2.3.4", wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeClientIPForStats(tc.input)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %q, got nil", tc.want)
			}
			if got.ClientIP != tc.want {
				t.Fatalf("clientIP=%q, want %q", got.ClientIP, tc.want)
			}
		})
	}
}

func TestNormalizeIPHash(t *testing.T) {
	valid := "e59c1b44a58270d4c90e0fbe75bd447babf294c0c250e5caa2e77a2a6b39f11e"
	if got := NormalizeIPHash(valid); got != valid {
		t.Fatalf("valid hash rejected: %q", got)
	}
	if got := NormalizeIPHash(" " + valid + " "); got != valid {
		t.Fatalf("trimmed hash mismatch: %q", got)
	}
	for _, invalid := range []string{"", "zz", "e59c", valid + "0"} {
		if got := NormalizeIPHash(invalid); got != "" {
			t.Fatalf("expected %q rejected, got %q", invalid, got)
		}
	}
}
