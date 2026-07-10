package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestClientIPResolverUsesRemoteAddrByDefault(t *testing.T) {
	resolver := newClientIPResolver(config.Config{TrustProxy: "false"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.10:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")

	if got, want := resolver.FromRequest(req), "10.0.0.10"; got != want {
		t.Fatalf("FromRequest() = %q, want %q", got, want)
	}
}

func TestClientIPResolverTrustZeroUsesRemoteAddr(t *testing.T) {
	resolver := newClientIPResolver(config.Config{TrustProxy: "0"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.10:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")

	if got, want := resolver.FromRequest(req), "10.0.0.10"; got != want {
		t.Fatalf("FromRequest() = %q, want %q", got, want)
	}
}

func TestClientIPResolverTrustAllUsesFirstForwardedFor(t *testing.T) {
	resolver := newClientIPResolver(config.Config{TrustProxy: "true"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.10:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20")

	if got, want := resolver.FromRequest(req), "203.0.113.10"; got != want {
		t.Fatalf("FromRequest() = %q, want %q", got, want)
	}
}

func TestClientIPResolverUsesTrustedHopCount(t *testing.T) {
	for _, tc := range []struct {
		trustProxy string
		want       string
	}{
		{trustProxy: "1", want: "198.51.100.20"},
		{trustProxy: "2", want: "203.0.113.10"},
	} {
		t.Run(tc.trustProxy, func(t *testing.T) {
			resolver := newClientIPResolver(config.Config{TrustProxy: tc.trustProxy})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.10:4567"
			req.Header.Set("X-Forwarded-For", "203.0.113.10, 198.51.100.20")

			if got := resolver.FromRequest(req); got != tc.want {
				t.Fatalf("FromRequest() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPResolverNormalizesAddresses(t *testing.T) {
	cases := map[string]string{
		"203.0.113.10:1234":       "203.0.113.10",
		"::ffff:203.0.113.10":     "203.0.113.10",
		"[2001:db8::1]:443":       "2001:db8::1",
		"2001:0db8:0000::0001":    "2001:db8::1",
		"not an ip":               "",
		"203.0.113.10:not-a-port": "",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := normalizeClientIPAddress(input); got != want {
				t.Fatalf("normalizeClientIPAddress() = %q, want %q", got, want)
			}
		})
	}
}

func TestForwardedForIPsSkipsInvalidValues(t *testing.T) {
	header := http.Header{}
	header.Add("X-Forwarded-For", "203.0.113.10, garbage")
	header.Add("X-Forwarded-For", "::ffff:198.51.100.20")

	got := forwardedForIPs(header)
	if len(got) != 2 || got[0] != "203.0.113.10" || got[1] != "198.51.100.20" {
		t.Fatalf("forwardedForIPs() = %#v", got)
	}
}
