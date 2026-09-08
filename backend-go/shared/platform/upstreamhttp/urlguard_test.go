package upstreamhttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsPrivateOrReservedIPMirrorRanges(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "100.127.255.255",
		"127.0.0.1", "169.254.1.2", "172.16.0.1", "172.31.255.255",
		"192.0.0.1", "192.0.2.1", "192.88.99.1", "192.168.1.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff00::1",
		"64:ff9b::1.2.3.4", "100::1", "2001::1", "2001:db8::1", "2002:0a00:0001::",
		"::ffff:10.0.0.1",
	}
	for _, address := range blocked {
		if !IsPrivateOrReservedIP(address) {
			t.Fatalf("IsPrivateOrReservedIP(%q) = false, want true", address)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "100.128.0.1", "172.32.0.1", "198.20.0.1", "2606:4700::1", "2001:4860:4860::8888"}
	for _, address := range allowed {
		if IsPrivateOrReservedIP(address) {
			t.Fatalf("IsPrivateOrReservedIP(%q) = true, want false", address)
		}
	}
	// Non-IP tokens are never decided by this helper.
	for _, token := range []string{"localhost", "example.com", ""} {
		if IsPrivateOrReservedIP(token) {
			t.Fatalf("IsPrivateOrReservedIP(%q) = true, want false", token)
		}
	}
}

func TestNormalizePrivateUpstreamOrigin(t *testing.T) {
	key, err := NormalizePrivateUpstreamOrigin("allowlist", "http://192.168.1.10:8080")
	if err != nil || key != "http://192.168.1.10:8080" {
		t.Fatalf("normalize key = %q, err = %v", key, err)
	}
	// Default port normalization.
	key, err = NormalizePrivateUpstreamOrigin("allowlist", "https://10.0.0.1")
	if err != nil {
		t.Fatalf("https default port: %v", err)
	}
	if key != "https://10.0.0.1:443" {
		t.Fatalf("https key = %q", key)
	}
	// Domain entries and non-origin forms are refused.
	for _, bad := range []string{"http://internal.example.com", "ftp://10.0.0.1", "http://10.0.0.1/x", "10.0.0.1"} {
		if _, err := NormalizePrivateUpstreamOrigin("allowlist", bad); err == nil {
			t.Fatalf("NormalizePrivateUpstreamOrigin(%q) accepted", bad)
		}
	}
}

type fakeResolver struct {
	addresses []net.IPAddr
	err       error
	hosts     []string
}

func (f *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	f.hosts = append(f.hosts, host)
	if f.err != nil {
		return nil, f.err
	}
	return f.addresses, nil
}

func TestDialGuardValidateHostBlocksPrivateResolution(t *testing.T) {
	resolver := &fakeResolver{addresses: []net.IPAddr{
		{IP: net.ParseIP("93.184.216.34")},
		{IP: net.ParseIP("10.0.0.5")},
	}}
	guard := NewDialGuard(URLSecurityConfig{}, resolver, nil)
	err := guard.ValidateHost(context.Background(), "rebind.example", "443")
	var resolvedErr *UnsafeResolvedUpstreamURLError
	if !errors.As(err, &resolvedErr) {
		t.Fatalf("ValidateHost err = %v, want UnsafeResolvedUpstreamURLError", err)
	}
	if resolvedErr.Message != UnsafeUpstreamURLMessage {
		t.Fatalf("message = %q", resolvedErr.Message)
	}
	// IP-literal private hosts are refused without DNS.
	resolver2 := &fakeResolver{}
	guard2 := NewDialGuard(URLSecurityConfig{}, resolver2, nil)
	if err := guard2.ValidateHost(context.Background(), ("127.0.0.1"), "80"); err == nil {
		t.Fatal("private literal accepted")
	}
	if len(resolver2.hosts) != 0 {
		t.Fatalf("literal host resolved anyway: %v", resolver2.hosts)
	}
	// allowPrivateBaseUrls skips validation entirely.
	guard3 := NewDialGuard(URLSecurityConfig{AllowPrivateBaseUrls: true}, resolver, nil)
	if err := guard3.ValidateHost(context.Background(), "anything.example", "443"); err != nil {
		t.Fatalf("allowPrivate ValidateHost = %v", err)
	}
}

func TestDialGuardAllowlistedPrivateOriginDials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	allowlist, allowErr := NormalizePrivateUpstreamOrigin("allowlist", "http://"+host+":"+port)
	if allowErr != nil {
		t.Fatalf("normalize loopback origin: %v", allowErr)
	}
	guard := NewDialGuard(URLSecurityConfig{
		PrivateOriginAllowlist: map[string]bool{allowlist: true},
	}, &fakeResolver{}, nil)
	// Prepare-time validation: allowlisted origin clears without DNS.
	if err := guard.ValidateHost(context.Background(), host, port); err != nil {
		t.Fatalf("allowlisted ValidateHost = %v", err)
	}
	// Dial-time: the allowlisted (loopback) target is dialed plainly.
	conn, dialErr := guard.DialContext(context.Background(), "tcp", net.JoinHostPort(host, port))
	if dialErr != nil {
		t.Fatalf("allowlisted DialContext = %v", dialErr)
	}
	_ = conn.Close()
}

func TestDialGuardDialContextRejectsBlockedResolution(t *testing.T) {
	resolver := &fakeResolver{addresses: []net.IPAddr{{IP: net.ParseIP("fd00::5")}}}
	guard := NewDialGuard(URLSecurityConfig{}, resolver, nil)
	_, err := guard.DialContext(context.Background(), "tcp", "internal.example:8443")
	var resolvedErr *UnsafeResolvedUpstreamURLError
	if !errors.As(err, &resolvedErr) {
		t.Fatalf("DialContext err = %v, want UnsafeResolvedUpstreamURLError", err)
	}
	// AllowPrivateBaseUrls falls back to plain dialing.
	plainGuard := NewDialGuard(URLSecurityConfig{AllowPrivateBaseUrls: true}, resolver, nil)
	if _, err := plainGuard.DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("expected dial failure (nothing listens), not a guard error")
	}
}

func TestDialGuardKeysAreDistinct(t *testing.T) {
	first := NewDialGuard(URLSecurityConfig{}, nil, nil)
	second := NewDialGuard(URLSecurityConfig{}, nil, nil)
	if first.Key() == "" || first.Key() == second.Key() {
		t.Fatalf("guard keys must be unique, got %q / %q", first.Key(), second.Key())
	}
	var nilGuard *DialGuard
	if nilGuard.Key() != "" {
		t.Fatal("nil guard key must be empty")
	}
}
