package upstreamurlpolicy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestValidateOpenAICompatibleBaseURLShapeAndPublicHTTPCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		config  Config
		wantErr string
	}{
		{name: "root", value: "https://api.openai.com"},
		{name: "custom prefix", value: "https://example.com/openai/v1"},
		{name: "public IPv4", value: "https://103.236.84.213:48222/v1"},
		{name: "public IPv6", value: "https://[2606:4700:4700::1111]/v1"},
		{name: "public HTTP compatibility", value: "http://103.236.84.213:48222/v1"},
		{name: "triple slash", value: "https:///api.openai.com/v1", wantErr: "协议后只能保留两个斜杠"},
		{name: "backslash", value: `https:\\api.openai.com\v1`, wantErr: "反斜杠"},
		{name: "userinfo", value: "https://user:pass@api.openai.com/v1", wantErr: "用户名或密码"},
		{name: "query", value: "https://api.openai.com/v1?x=1", wantErr: "查询参数"},
		{name: "fragment", value: "https://api.openai.com/v1#x", wantErr: "片段标识"},
		{name: "consecutive slash", value: "https://api.openai.com//v1", wantErr: "连续斜杠"},
		{name: "encoded slash", value: "https://api.openai.com/v1/%2f", wantErr: "编码后的斜杠"},
		{name: "encoded backslash", value: "https://api.openai.com/v1/%5c", wantErr: "编码后的斜杠"},
		{name: "bad escape", value: "https://api.openai.com/v1/%zz", wantErr: "路径编码无效"},
		{name: "dot", value: "https://api.openai.com/v1/%2e", wantErr: ". 或 .."},
		{name: "v1 endpoint", value: "https://api.openai.com/v1/responses", wantErr: "/v1 后的具体接口路径"},
		{name: "endpoint", value: "https://example.com/openai/chat/completions", wantErr: "不能填写具体接口路径"},
		{name: "port zero", value: "https://api.openai.com:0/v1", wantErr: "1 到 65535"},
		{name: "non numeric port", value: "https://api.openai.com:abc/v1", wantErr: "格式无效"},
		{name: "empty port", value: "https://api.openai.com:/v1", wantErr: "端口无效"},
		{name: "whitespace", value: "https://api.openai.com/v1 x", wantErr: "空白字符"},
		{name: "ftp", value: "ftp://api.openai.com/v1", wantErr: "只允许 http 或 https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ValidateOpenAICompatibleBaseURL(test.value, test.config)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateOpenAICompatibleBaseURL() error = %v", err)
				}
				if parsed == nil {
					t.Fatal("ValidateOpenAICompatibleBaseURL() URL = nil")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateOpenAICompatibleBaseURL() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestPrepareRequestURLRejectsEveryPrivateAndReservedRange(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.0.0.1", "192.0.2.1", "192.88.99.1", "192.168.1.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"::", "::1", "::7f00:1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::1", "64:ff9b:1::1", "100::1",
		"2001::1", "2001:db8::1", "2002::1", "fc00::1", "fe80::1", "ff00::1",
	}
	for _, value := range blocked {
		t.Run(value, func(t *testing.T) {
			_, err := PrepareRequestURL(context.Background(), "https://blocked.example/v1/responses", Config{
				Resolver: resolverReturning(value),
			})
			var unsafe *UnsafeURLError
			if !errors.As(err, &unsafe) || !strings.Contains(err.Error(), unsafeURLMessage) {
				t.Fatalf("PrepareRequestURL() error = %v, want UnsafeURLError", err)
			}
		})
	}
}

func TestValidateBaseURLRejectsLocalhostAndLegacyIPv4FormsBeforeDNS(t *testing.T) {
	t.Parallel()
	for _, host := range []string{
		"localhost", "service.localhost", "localhost.", "127.0.0.1.",
		"127.1", "2130706433", "0177.0.0.1", "0x7f.0.0.1", "0x7f000001",
	} {
		t.Run(host, func(t *testing.T) {
			_, err := ValidateOpenAICompatibleBaseURL("https://"+host+"/v1", Config{})
			if err == nil || !strings.Contains(err.Error(), unsafeURLMessage) {
				t.Fatalf("ValidateOpenAICompatibleBaseURL(%q) error = %v", host, err)
			}
		})
	}
}

func TestPrepareRequestURLFailsClosedWhenAnyDNSAddressIsUnsafe(t *testing.T) {
	t.Parallel()
	resolver := resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" || host != "mixed.example" {
			t.Fatalf("LookupNetIP(%q, %q)", network, host)
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	_, err := PrepareRequestURL(context.Background(), "https://mixed.example/v1/responses?stream=false", Config{Resolver: resolver})
	if err == nil || !strings.Contains(err.Error(), unsafeURLMessage) {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
}

func TestPrepareRequestURLFailsClosedOnDNSFailureOrEmptyResult(t *testing.T) {
	t.Parallel()
	lookupErr := errors.New("authoritative DNS failure")
	for name, resolver := range map[string]Resolver{
		"error": resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, lookupErr }),
		"empty": resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil }),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PrepareRequestURL(context.Background(), "https://dns.example/v1", Config{Resolver: resolver})
			if err == nil {
				t.Fatal("PrepareRequestURL() error = nil")
			}
			if name == "error" && !errors.Is(err, lookupErr) {
				t.Fatalf("PrepareRequestURL() error = %v, want wrapped resolver error", err)
			}
		})
	}
}

func TestPrepareRequestURLRejectsNilContext(t *testing.T) {
	t.Parallel()
	if _, err := PrepareRequestURL(nil, "https://api.openai.com/v1", Config{}); err == nil {
		t.Fatal("PrepareRequestURL(nil) error = nil")
	}
}

func TestPrepareRequestURLAndDialRespectCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareRequestURL(ctx, "https://api.example/v1", Config{Resolver: resolverReturning("8.8.8.8")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareRequestURL(canceled) error = %v", err)
	}
	plan, err := PrepareRequestURL(context.Background(), "https://api.example/v1", Config{Resolver: resolverReturning("8.8.8.8")})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	if _, err := plan.DialContext(ctx, "tcp", "api.example:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext(canceled) error = %v", err)
	}
}

func TestPrepareRequestURLAllowsExactPrivateOriginAndPinsLiteral(t *testing.T) {
	t.Parallel()
	plan, err := PrepareRequestURL(context.Background(), "http://127.0.0.1:8317/v1/responses", Config{
		PrivateBaseURLAllowlist: []string{"http://127.0.0.1:8317"},
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			t.Fatal("literal IP must not use DNS")
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	if got := plan.Addresses(); !slices.Equal(got, []netip.Addr{netip.MustParseAddr("127.0.0.1")}) {
		t.Fatalf("Addresses() = %v", got)
	}
	if _, err := PrepareRequestURL(context.Background(), "http://127.0.0.1:8318/v1/responses", Config{
		PrivateBaseURLAllowlist: []string{"http://127.0.0.1:8317"},
	}); err == nil {
		t.Fatal("different port unexpectedly allowed")
	}
}

func TestPrepareRequestURLAllowPrivateStillPinsDNS(t *testing.T) {
	t.Parallel()
	plan, err := PrepareRequestURL(context.Background(), "http://mock.internal/v1", Config{
		AllowPrivateBaseURLs: true,
		Resolver:             resolverReturning("10.0.0.9"),
	})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	if got := plan.Addresses(); !slices.Equal(got, []netip.Addr{netip.MustParseAddr("10.0.0.9")}) {
		t.Fatalf("Addresses() = %v", got)
	}
	if _, err := PrepareRequestURL(context.Background(), "http://mock.internal/v1", Config{
		AllowPrivateBaseURLs: true,
		Production:           true,
		Resolver:             resolverReturning("10.0.0.9"),
	}); err == nil || !strings.Contains(err.Error(), "生产环境不能启用") {
		t.Fatalf("production allow-private error = %v", err)
	}
}

func TestAllowlistValidationIsExactAndIPOnly(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"192.168.40.199",
		"http://private-upstream.example:8317",
		"http://192.168.40.199:8317/v1",
		"http://user:pass@192.168.40.199:8317",
		"ftp://192.168.40.199:8317",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := ValidateOpenAICompatibleBaseURL("https://api.openai.com/v1", Config{PrivateBaseURLAllowlist: []string{value}})
			if err == nil || !strings.Contains(err.Error(), "JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST") {
				t.Fatalf("invalid allowlist error = %v", err)
			}
		})
	}
	parsed, err := ValidateOpenAICompatibleBaseURL("http://127.0.0.1/v1", Config{
		PrivateBaseURLAllowlist: []string{"http://127.0.0.1:080"},
	})
	if err != nil || parsed == nil {
		t.Fatalf("default-port exact allowlist error = %v", err)
	}
}

func TestDialPlanUsesOnlyPinnedAddressesAndRejectsOriginChanges(t *testing.T) {
	t.Parallel()
	var attempts []string
	dialFailure := errors.New("dial failed")
	plan, err := PrepareRequestURL(context.Background(), "https://API.Example.:8443/v1/responses", Config{
		Resolver: resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
			if network != "ip" || host != "api.example." {
				t.Fatalf("LookupNetIP(%q, %q)", network, host)
			}
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}, nil
		}),
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			attempts = append(attempts, network+" "+address)
			return nil, dialFailure
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	addresses := plan.Addresses()
	addresses[0] = netip.MustParseAddr("127.0.0.1")
	if got := plan.Addresses()[0].String(); got != "8.8.8.8" {
		t.Fatalf("Addresses() exposed mutable storage: %s", got)
	}
	if _, err := plan.DialContext(context.Background(), "tcp", "api.example:8443"); !errors.Is(err, dialFailure) {
		t.Fatalf("DialContext() error = %v", err)
	}
	wantAttempts := []string{"tcp 8.8.8.8:8443", "tcp 1.1.1.1:8443"}
	if !slices.Equal(attempts, wantAttempts) {
		t.Fatalf("dial attempts = %v, want %v", attempts, wantAttempts)
	}
	before := len(attempts)
	for _, target := range []string{"evil.example:8443", "api.example:443", "127.0.0.1:8443"} {
		if _, err := plan.DialContext(context.Background(), "tcp", target); err == nil {
			t.Fatalf("DialContext(%q) error = nil", target)
		}
	}
	if len(attempts) != before {
		t.Fatalf("mismatched targets reached dialer: %v", attempts[before:])
	}
	if _, err := plan.DialContext(context.Background(), "udp", "api.example:8443"); err == nil {
		t.Fatal("unsupported network unexpectedly reached dialer")
	}
}

func TestDialPlanCanonicalizesExplicitPortBeforeDial(t *testing.T) {
	t.Parallel()
	var target string
	plan, err := PrepareRequestURL(context.Background(), "http://api.example:080/v1", Config{
		Resolver: resolverReturning("8.8.8.8"),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			target = "called"
			return nil, errors.New("stop")
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	plan.dial = func(_ context.Context, _ string, address string) (net.Conn, error) {
		target = address
		return nil, errors.New("stop")
	}
	_, _ = plan.DialContext(context.Background(), "tcp", "api.example:080")
	if target != "8.8.8.8:80" {
		t.Fatalf("dial target = %q", target)
	}
}

func TestDialPlanWorksWithHTTPTransportAndBlocksCrossOriginRedirect(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	var hits atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(request.Host, "upstream.example:") {
			t.Errorf("request Host = %q", request.Host)
		}
		response.Header().Set("Location", "http://evil.example/escaped")
		response.WriteHeader(http.StatusFound)
	})}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		if serveErr := <-serverDone; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			t.Errorf("Serve() error = %v", serveErr)
		}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	var lookups atomic.Int32
	plan, err := PrepareRequestURL(context.Background(), "http://upstream.example:"+port+"/start", Config{
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}),
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:"+port {
				t.Errorf("pinned dial address = %q", address)
			}
			dialer := &net.Dialer{}
			return dialer.DialContext(ctx, network, listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	transport := &http.Transport{DialContext: plan.DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(plan.URL().String())
	if err == nil {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		t.Fatal("cross-origin redirect unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "与已验证 Origin") {
		t.Fatalf("redirect error = %v", err)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("DNS lookup count = %d, want 1", got)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1", got)
	}
}

func TestPlanURLReturnsCopyAndPreservesRequestQuery(t *testing.T) {
	t.Parallel()
	plan, err := PrepareRequestURL(context.Background(), "https://api.example/v1/responses?stream=false", Config{
		Resolver: resolverReturning("8.8.8.8"),
	})
	if err != nil {
		t.Fatalf("PrepareRequestURL() error = %v", err)
	}
	requestURL := plan.URL()
	if requestURL.RawQuery != "stream=false" {
		t.Fatalf("RawQuery = %q", requestURL.RawQuery)
	}
	requestURL.Host = "evil.example"
	if got := plan.URL().Hostname(); got != "api.example" {
		t.Fatalf("URL() exposed mutable state: %q", got)
	}
}

func resolverReturning(values ...string) Resolver {
	return resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		addresses := make([]netip.Addr, 0, len(values))
		for _, value := range values {
			addresses = append(addresses, netip.MustParseAddr(value))
		}
		return addresses, nil
	})
}
