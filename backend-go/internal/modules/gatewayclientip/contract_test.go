package gatewayclientip

import (
	"net/netip"
	"strings"
	"testing"
)

func TestResolveIgnoresForwardedChainWhenProxyIsNotTrusted(t *testing.T) {
	result := Resolve(Input{
		RemoteAddress: "10.0.0.10:4567",
		ForwardedFor:  []string{"203.0.113.10, 198.51.100.20"},
	}, Trust{})

	assertAddress(t, "client", result.ClientIP, "10.0.0.10")
	if result.Source != SourceRemote {
		t.Fatalf("Source = %q, want %q", result.Source, SourceRemote)
	}
	if result.ForwardedTrusted {
		t.Fatal("ForwardedTrusted = true, want false")
	}
}

func TestResolveTrustAllUsesLeftmostForwardedHop(t *testing.T) {
	result := Resolve(Input{
		RemoteAddress: "10.0.0.10:4567",
		ForwardedFor: []string{
			"203.0.113.10, 198.51.100.20",
			"::ffff:192.0.2.30",
		},
	}, Trust{Enabled: true, All: true})

	assertAddress(t, "client", result.ClientIP, "203.0.113.10")
	assertAddress(t, "remote", result.RemoteIP, "10.0.0.10")
	if result.Source != SourceForwardedFor || !result.ForwardedTrusted {
		t.Fatalf("source/trusted = %q/%v", result.Source, result.ForwardedTrusted)
	}
	want := []string{"203.0.113.10", "198.51.100.20", "192.0.2.30"}
	if len(result.ForwardedChain) != len(want) {
		t.Fatalf("ForwardedChain = %#v, want %v", result.ForwardedChain, want)
	}
	for index, hop := range result.ForwardedChain {
		if !hop.Valid || hop.Address.String() != want[index] {
			t.Fatalf("ForwardedChain[%d] = %+v, want valid %s", index, hop, want[index])
		}
	}
}

func TestResolveUsesTrustedHopCountFromNearestProxy(t *testing.T) {
	input := Input{
		RemoteAddress: "10.0.0.10:4567",
		ForwardedFor:  []string{"203.0.113.10, 198.51.100.20"},
	}

	assertAddress(t, "one hop", Resolve(input, Trust{Enabled: true, Hops: 1}).ClientIP, "198.51.100.20")
	assertAddress(t, "two hops", Resolve(input, Trust{Enabled: true, Hops: 2}).ClientIP, "203.0.113.10")
	assertAddress(t, "clamped hops", Resolve(input, Trust{Enabled: true, Hops: 16}).ClientIP, "203.0.113.10")
}

func TestResolveDoesNotPromotePastMalformedSelectedHop(t *testing.T) {
	input := Input{
		RemoteAddress: "10.0.0.10:4567",
		ForwardedFor:  []string{"203.0.113.10, malformed-hop"},
	}

	result := Resolve(input, Trust{Enabled: true, Hops: 1})
	assertAddress(t, "client", result.ClientIP, "10.0.0.10")
	if result.Source != SourceRemote || result.ForwardedTrusted {
		t.Fatalf("source/trusted = %q/%v, want remote/false", result.Source, result.ForwardedTrusted)
	}
	if len(result.ForwardedChain) != 2 || result.ForwardedChain[1].Valid {
		t.Fatalf("ForwardedChain = %#v, want malformed hop preserved", result.ForwardedChain)
	}

	result = Resolve(input, Trust{Enabled: true, Hops: 2})
	assertAddress(t, "two-hop client", result.ClientIP, "203.0.113.10")
}

func TestResolveRejectsOversizedForwardedChainInsteadOfGrowingWithoutBound(t *testing.T) {
	hops := make([]string, MaxForwardedHops+1)
	for index := range hops {
		hops[index] = "203.0.113.10"
	}
	result := Resolve(Input{
		RemoteAddress: "10.0.0.10:4567",
		ForwardedFor:  []string{strings.Join(hops, ",")},
	}, Trust{Enabled: true, All: true})

	assertAddress(t, "client", result.ClientIP, "10.0.0.10")
	if !result.ForwardedRejected || result.ForwardedTrusted {
		t.Fatalf("rejected/trusted = %v/%v, want true/false", result.ForwardedRejected, result.ForwardedTrusted)
	}
	if len(result.ForwardedChain) != MaxForwardedHops {
		t.Fatalf("ForwardedChain length = %d, want bounded %d", len(result.ForwardedChain), MaxForwardedHops)
	}
}

func TestResolveNormalizesSocketAndForwardedAddressForms(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		remote bool
		want   string
	}{
		{name: "ipv4 port", value: "203.0.113.10:1234", remote: true, want: "203.0.113.10"},
		{name: "mapped ipv4", value: "::ffff:203.0.113.10", want: "203.0.113.10"},
		{name: "bracketed ipv6", value: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "canonical ipv6", value: "2001:0db8:0000::0001", want: "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := Input{RemoteAddress: "10.0.0.10:4567", ForwardedFor: []string{tc.value}}
			trust := Trust{Enabled: true, All: true}
			if tc.remote {
				input = Input{RemoteAddress: tc.value}
				trust = Trust{}
			}
			assertAddress(t, "client", Resolve(input, trust).ClientIP, tc.want)
		})
	}
}

func TestPolicyLookupMatchesNodeIPv4IdentityContract(t *testing.T) {
	identity := Identity{Address: netip.MustParseAddr("203.0.113.9")}
	lookup, ok := identity.PolicyLookup()
	if !ok {
		t.Fatal("PolicyLookup() ok = false")
	}
	if lookup.ClientIP != "203.0.113.9" || lookup.AggregateIPKey != "203.0.113.9" || lookup.IPVersion != 4 {
		t.Fatalf("PolicyLookup() identity = %+v", lookup)
	}
	if lookup.IPHash != "1238ae70e54c7b3a7b287d070d543bf2ad7288a734688f5cad1cdfb44d9a76eb" {
		t.Fatalf("IPHash = %q", lookup.IPHash)
	}
	if lookup.BucketNo < 0 || lookup.BucketNo >= ClientIPRegistryBucketCount {
		t.Fatalf("BucketNo = %d", lookup.BucketNo)
	}

	for _, value := range []string{"2001:db8::1", "invalid IP"} {
		address, err := netip.ParseAddr(value)
		if err != nil {
			address = netip.Addr{}
		}
		if lookup, ok := (Identity{Address: address}).PolicyLookup(); ok || lookup != (PolicyLookup{}) {
			t.Fatalf("PolicyLookup(%q) = %+v, %v", value, lookup, ok)
		}
	}
}

func TestDecidePolicyUsesBlacklistPrecedenceAndRequiresKnownClient(t *testing.T) {
	known := Identity{Address: netip.MustParseAddr("203.0.113.9")}

	allow := DecidePolicy(PolicyInput{Identity: known, Allowlisted: true})
	if allow.Blocked || !allow.Allowlisted || allow.Reason != PolicyReasonAllowlisted {
		t.Fatalf("allow decision = %+v", allow)
	}

	blocked := DecidePolicy(PolicyInput{Identity: known, Allowlisted: true, Blacklisted: true})
	if !blocked.Blocked || blocked.Allowlisted || blocked.Reason != PolicyReasonBlacklisted {
		t.Fatalf("conflicting decision = %+v", blocked)
	}

	unknown := DecidePolicy(PolicyInput{Allowlisted: true, Blacklisted: true})
	if unknown.Blocked || unknown.Allowlisted || unknown.Reason != PolicyReasonUnknownClient {
		t.Fatalf("unknown decision = %+v", unknown)
	}
}

func assertAddress(t *testing.T, label string, got netip.Addr, want string) {
	t.Helper()
	if !got.IsValid() || got.String() != want {
		t.Fatalf("%s address = %q, want %q", label, got, want)
	}
}
