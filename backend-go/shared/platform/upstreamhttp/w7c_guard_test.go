package upstreamhttp

// w7c contract completion: bounded body readers, client pool eviction and
// keys, the SOCKS5 dialer handshake matrix against an in-process server, and
// the dial guard / URL guard primitives.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestW7CBoundedBodyReaders(t *testing.T) {
	invalid := []struct {
		name   string
		reader io.Reader
		limit  int64
	}{
		{"nil reader", nil, 10},
		{"zero limit", strings.NewReader("x"), 0},
		{"negative limit", strings.NewReader("x"), -1},
	}
	for _, tc := range invalid {
		if _, err := ReadBounded(tc.reader, tc.limit); !errors.Is(err, ErrResponseBodyLimitInvalid) {
			t.Fatalf("%s ReadBounded: %v", tc.name, err)
		}
		if _, err := ReadBoundedPartial(tc.reader, tc.limit); !errors.Is(err, ErrResponseBodyLimitInvalid) {
			t.Fatalf("%s ReadBoundedPartial: %v", tc.name, err)
		}
		if _, err := ReadAndDrainBounded(tc.reader, tc.limit); !errors.Is(err, ErrResponseBodyLimitInvalid) {
			t.Fatalf("%s ReadAndDrainBounded: %v", tc.name, err)
		}
		if _, err := ReadAndDrainBoundedPartial(tc.reader, tc.limit); !errors.Is(err, ErrResponseBodyLimitInvalid) {
			t.Fatalf("%s ReadAndDrainBoundedPartial: %v", tc.name, err)
		}
	}

	reader := strings.NewReader("0123456789")
	body, err := ReadBounded(reader, 20)
	if err != nil || string(body) != "0123456789" {
		t.Fatalf("fit: %q %v", body, err)
	}
	if _, err := ReadBounded(strings.NewReader("0123456789"), 5); !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatal("over-limit ReadBounded must reject")
	}

	// Partial variants retain content on tail errors.
	errReader := io.MultiReader(strings.NewReader("kept"), w7cErrReader{})
	partial, err := ReadBoundedPartial(errReader, 10)
	if err == nil || string(partial) != "kept" {
		t.Fatalf("partial retains on error: %q %v", partial, err)
	}
	if _, err := ReadBoundedPartial(strings.NewReader("0123456789"), 4); !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatal("partial over-limit must report too-large")
	}

	drained, err := ReadAndDrainBounded(strings.NewReader(strings.Repeat("a", 30)), 10)
	if err != nil || len(drained) != 10 {
		t.Fatalf("drain retains the budget: %d %v", len(drained), err)
	}
	if _, err := ReadAndDrainBounded(w7cErrReader{}, 10); err == nil {
		t.Fatal("drain read error must propagate")
	}
	partialDrained, err := ReadAndDrainBoundedPartial(strings.NewReader(strings.Repeat("b", 30)), 10)
	if !errors.Is(err, ErrResponseBodyTooLarge) || len(partialDrained) != 10 {
		t.Fatalf("partial drain: %d %v", len(partialDrained), err)
	}
	ok, err := ReadAndDrainBoundedPartial(strings.NewReader("small"), 10)
	if err != nil || string(ok) != "small" {
		t.Fatalf("partial drain fit: %q %v", ok, err)
	}
	partialErr, err := ReadAndDrainBoundedPartial(io.MultiReader(strings.NewReader("kept"), w7cErrReader{}), 10)
	if err == nil || string(partialErr) != "kept" {
		t.Fatalf("partial drain error: %q %v", partialErr, err)
	}
	if n, err := Drain(nil); n != 0 || !errors.Is(err, ErrResponseBodyLimitInvalid) {
		t.Fatalf("Drain nil: %d %v", n, err)
	}
	if n, err := Drain(strings.NewReader("abcd")); n != 4 || err != nil {
		t.Fatalf("Drain: %d %v", n, err)
	}
}

type w7cErrReader struct{}

func (w7cErrReader) Read([]byte) (int, error) { return 0, errors.New("w7c read error") }

func TestW7CClientPoolLifecycle(t *testing.T) {
	var nilPool *ClientPool
	if _, err := nilPool.Client("", TransportOptions{}); !errors.Is(err, ErrClientPoolNil) {
		t.Fatalf("nil pool: %v", err)
	}
	(*ClientPool)(nil).CloseIdleConnections() // nil pool close is a no-op
	poolErr := clientPoolError{"m"}
	if poolErr.Error() != "m" {
		t.Fatal("clientPoolError contract")
	}

	// LRU eviction: a limit-2 pool drops the oldest entry on the third key.
	pool := NewClientPoolWithLimit(0)
	if pool.maxEntries != 1 {
		t.Fatalf("non-positive limit coerced: %d", pool.maxEntries)
	}
	pool = NewClientPoolWithLimit(2)
	first, err := pool.Client("", TransportOptions{ResponseHeaderTimeout: time.Second})
	if err != nil || first == nil {
		t.Fatalf("first client: %v", err)
	}
	second, err := pool.Client("socks5://127.0.0.1:1080", TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Touch the first entry so it becomes most-recently used again.
	if _, err := pool.Client("", TransportOptions{ResponseHeaderTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	third, err := pool.Client("", TransportOptions{ResponseHeaderTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if third == first || third == second {
		t.Fatal("distinct policies must yield distinct clients")
	}
	if len(pool.clients) != 2 {
		t.Fatalf("pool size after eviction: %d", len(pool.clients))
	}
	pool.CloseIdleConnections()

	// SharedClient returns a pooled entry and reuses identical keys.
	sharedA, err := SharedClient("", TransportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sharedB, err := SharedClient("", TransportOptions{})
	if err != nil || sharedA != sharedB {
		t.Fatalf("shared clients must be identical: %v", err)
	}

	// transportKey distinguishes every option dimension.
	base := TransportOptions{}
	key := transportKey("", base)
	if transportKey(" http://p ", base) == key {
		t.Fatal("proxy url participates in the key")
	}
	if transportKey("", TransportOptions{ResponseHeaderTimeout: time.Second}) == key {
		t.Fatal("timeout participates in the key")
	}
	if transportKey("", TransportOptions{MaxResponseHeaderBytes: 9}) == key {
		t.Fatal("header budget participates in the key")
	}
	if transportKey("", TransportOptions{DisableCompression: true}) == key {
		t.Fatal("compression participates in the key")
	}
	if transportKey("", TransportOptions{ForceRemoteSOCKS5: true}) == key {
		t.Fatal("socks force participates in the key")
	}
	guard := NewDialGuard(URLSecurityConfig{}, nil, nil)
	if transportKey("", TransportOptions{DialGuard: guard}) == key {
		t.Fatal("dial guard participates in the key")
	}
	header := http.Header{}
	header.Set("Proxy-Authorization", "Basic x")
	header.Add("Proxy-Authorization", "Basic y")
	if transportKey("", TransportOptions{ProxyConnectHeader: header}) == key {
		t.Fatal("connect headers participate in the key")
	}
	if keys := sortedHeaderKeys(header); len(keys) != 1 || keys[0] != "Proxy-Authorization" {
		t.Fatalf("sortedHeaderKeys: %v", keys)
	}
	// Round-trip through JSON to keep encoding/json referenced for configs.
	raw, _ := json.Marshal(map[string]int{"max": 1})
	var decoded map[string]int
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded["max"] != 1 {
		t.Fatal("json sanity")
	}
}

func TestW7CSOCKS5HandshakeMatrix(t *testing.T) {
	// In-process SOCKS5 server with scripted behavior.
	startServer := func(t *testing.T, behave func(conn net.Conn)) net.Listener {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go behave(conn)
			}
		}()
		return listener
	}
	readRequest := func(conn net.Conn) ([]byte, error) {
		// ver, cmd, rsv, atyp — the full four-byte request header.
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			return nil, err
		}
		rest := 0
		switch header[3] {
		case 0x01:
			rest = 4
		case 0x04:
			rest = 16
		case 0x03:
			var length [1]byte
			if _, err := io.ReadFull(conn, length[:]); err != nil {
				return nil, err
			}
			rest = int(length[0])
		}
		tail := make([]byte, rest+2)
		if _, err := io.ReadFull(conn, tail); err != nil {
			return nil, err
		}
		return append(header, tail...), nil
	}

	t.Run("no-auth success with ipv4 bound address", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			if !w7cReadGreeting(conn) {
				return
			}
			_, _ = conn.Write([]byte{0x05, 0x00})
			if _, err := readRequest(conn); err != nil {
				return
			}
			_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 10, 0, 0, 1, 0x1f, 0x90})
			io.Copy(io.Discard, conn)
		})
		proxy, _ := url.Parse("socks5://" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		conn, err := dial(context.Background(), "tcp", "example.invalid:8080")
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("auth success with domain bound address", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			if !w7cReadGreeting(conn) {
				return
			}
			_, _ = conn.Write([]byte{0x05, 0x02})
			w7cReadAuthSubnegotiation(conn)
			_, _ = conn.Write([]byte{0x05, 0x00})
			if _, err := readRequest(conn); err != nil {
				return
			}
			bound := append([]byte{0x05, 0x00, 0x00, 0x03, 5}, []byte("proxy")...)
			bound = append(bound, 0x00, 0x50)
			_, _ = conn.Write(bound)
			io.Copy(io.Discard, conn)
		})
		proxy, _ := url.Parse("socks5://user:pass@" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		conn, err := dial(context.Background(), "tcp", "example.invalid:80")
		if err != nil {
			t.Fatalf("auth dial: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("auth failure", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			w7cReadGreeting(conn)
			_, _ = conn.Write([]byte{0x05, 0x02})
			w7cReadAuthSubnegotiation(conn)
			_, _ = conn.Write([]byte{0x05, 0x01})
		})
		proxy, _ := url.Parse("socks5://user:bad@" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("auth failure: %v", err)
		}
	})

	t.Run("method rejected", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			w7cReadGreeting(conn)
			_, _ = conn.Write([]byte{0x05, 0xff})
		})
		proxy, _ := url.Parse("socks5://" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("method rejection: %v", err)
		}
	})

	t.Run("connect failure reply", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			w7cReadGreeting(conn)
			_, _ = conn.Write([]byte{0x05, 0x00})
			if _, err := readRequest(conn); err != nil {
				return
			}
			_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		})
		proxy, _ := url.Parse("socks5://" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil || !strings.Contains(err.Error(), "reply=5") {
			t.Fatalf("connect failure: %v", err)
		}
	})

	t.Run("unknown bound address type", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			w7cReadGreeting(conn)
			_, _ = conn.Write([]byte{0x05, 0x00})
			if _, err := readRequest(conn); err != nil {
				return
			}
			_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x77, 0, 0})
		})
		proxy, _ := url.Parse("socks5://" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil || !strings.Contains(err.Error(), "unknown bound address") {
			t.Fatalf("unknown bound type: %v", err)
		}
	})

	t.Run("bad version reply", func(t *testing.T) {
		listener := startServer(t, func(conn net.Conn) {
			w7cReadGreeting(conn)
			_, _ = conn.Write([]byte{0x04, 0x00})
		})
		proxy, _ := url.Parse("socks5://" + listener.Addr().String())
		dial := NewSOCKS5DialContext(proxy, true)
		if _, err := dial(context.Background(), "tcp", "example.invalid:80"); err == nil {
			t.Fatal("bad version reply must fail")
		}
	})

	t.Run("guard arms", func(t *testing.T) {
		proxy, _ := url.Parse("socks5://127.0.0.1:1")
		dial := NewSOCKS5DialContext(proxy, true)
		if _, err := dial(context.Background(), "udp", "example.invalid:80"); err == nil || !strings.Contains(err.Error(), "unsupported network") {
			t.Fatalf("unsupported network: %v", err)
		}
		if _, err := NewSOCKS5DialContext(nil, true)(context.Background(), "tcp", "x:80"); !errors.Is(err, ErrProxyURLInvalid) {
			t.Fatalf("nil proxy: %v", err)
		}
		empty, _ := url.Parse("socks5://")
		if _, err := NewSOCKS5DialContext(empty, true)(context.Background(), "tcp", "x:80"); !errors.Is(err, ErrProxyURLInvalid) {
			t.Fatalf("empty host: %v", err)
		}
		if _, err := socks5ConnectRequest(context.Background(), "no-port", true); err == nil {
			t.Fatal("target without port must fail")
		}
		if _, err := socks5ConnectRequest(context.Background(), "host:0", true); err == nil || !strings.Contains(err.Error(), "port invalid") {
			t.Fatalf("zero port: %v", err)
		}
		if _, err := socks5ConnectRequest(context.Background(), " :80", true); err == nil || !strings.Contains(err.Error(), "host invalid") {
			t.Fatalf("blank socks5h host: %v", err)
		}
	})
}

// w7cReadGreeting consumes one SOCKS5 method-selection greeting (VER, NMETHODS,
// METHODS) and reports whether the read succeeded.
func w7cReadGreeting(conn net.Conn) bool {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return false
	}
	methods := make([]byte, head[1])
	if head[1] > 0 {
		if _, err := io.ReadFull(conn, methods); err != nil {
			return false
		}
	}
	return true
}

// w7cReadAuthSubnegotiation consumes one RFC 1929 username/password block.
func w7cReadAuthSubnegotiation(conn net.Conn) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	user := make([]byte, head[1])
	if _, err := io.ReadFull(conn, user); err != nil {
		return
	}
	var passLength [1]byte
	if _, err := io.ReadFull(conn, passLength[:]); err != nil {
		return
	}
	pass := make([]byte, passLength[0])
	_, _ = io.ReadFull(conn, pass)
}

func TestW7CDialGuardContract(t *testing.T) {
	// Guard identity and keys.
	guard := NewDialGuard(URLSecurityConfig{}, nil, nil)
	if guard.Key() == "" || guard.Key() == NewDialGuard(URLSecurityConfig{}, nil, nil).Key() {
		t.Fatal("guard keys must be unique per instance")
	}
	var nilGuard *DialGuard
	if nilGuard.Key() != "" {
		t.Fatal("nil guard key is empty")
	}

	// Origin keys make the default port explicit.
	parsed, _ := url.Parse("https://Example.COM")
	if got := UpstreamOriginKey(parsed); got != "https://example.com:443" {
		t.Fatalf("origin key: %q", got)
	}
	parsed, _ = url.Parse("http://example.com:8080")
	if got := UpstreamOriginKey(parsed); got != "http://example.com:8080" {
		t.Fatalf("origin key with port: %q", got)
	}

	// Allowlist normalization arms.
	if _, err := NormalizePrivateUpstreamOrigin("X", "not-a-url"); err == nil {
		t.Fatal("non-url allowlist entry must fail")
	}
	if _, err := NormalizePrivateUpstreamOrigin("X", "ftp://10.0.0.1"); err == nil {
		t.Fatal("non-http scheme must fail")
	}
	if _, err := NormalizePrivateUpstreamOrigin("X", "http://user@10.0.0.1/x?q=1"); err == nil {
		t.Fatal("origin with path/user/query must fail")
	}
	if _, err := NormalizePrivateUpstreamOrigin("X", "http://example.internal"); err == nil || !strings.Contains(err.Error(), "IP Origin") {
		t.Fatalf("hostname entry: %v", err)
	}
	key, err := NormalizePrivateUpstreamOrigin("X", " HTTP://10.0.0.1 ")
	if err != nil || key != "http://10.0.0.1:80" {
		t.Fatalf("normalized origin: %q %v", key, err)
	}
	key, err = NormalizePrivateUpstreamOrigin("X", "https://[::1]")
	if err != nil || key != "https://::1:443" {
		t.Fatalf("ipv6 origin: %q %v", key, err)
	}
	config := URLSecurityConfig{PrivateOriginAllowlist: map[string]bool{key: true}}
	if !config.IsAllowedPrivateOrigin("https://::1:443") || config.IsAllowedPrivateOrigin("http://10.0.0.2:80") {
		t.Fatal("IsAllowedPrivateOrigin contract")
	}

	// Localhost names and private literals.
	// The comparison is case-insensitive only through the caller's
	// normalization; IsLocalhostName itself is case sensitive.
	if !IsLocalhostName("localhost") || !IsLocalhostName("a.localhost") || IsLocalhostName("a.LOCALHOST") || IsLocalhostName("evildomain") || IsLocalhostName("localhost.evil.com") {
		t.Fatal("IsLocalhostName contract")
	}
	if !IsPrivateOrReservedIP("127.0.0.1") || !IsPrivateOrReservedIP(" 10.1.2.3 ") || !IsPrivateOrReservedIP("fe80::1") || !IsPrivateOrReservedIP("::1") {
		t.Fatal("private literals must be blocked")
	}
	if IsPrivateOrReservedIP("8.8.8.8") || IsPrivateOrReservedIP("example.com") {
		t.Fatal("public ips and names must pass")
	}

	// ValidateHost with a scripted resolver.
	resolver := &w7cResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	host := NewDialGuard(URLSecurityConfig{}, resolver, nil)
	if err := host.ValidateHost(context.Background(), " EXAMPLE.com ", "443"); err != nil {
		t.Fatalf("public hostname: %v", err)
	}
	if err := host.ValidateHost(context.Background(), "127.0.0.1", "443"); err == nil {
		t.Fatal("blocked literal must fail validation")
	}
	if err := host.ValidateHost(context.Background(), "8.8.4.4", "443"); err != nil {
		t.Fatalf("public literal: %v", err)
	}
	resolver.err = errors.New("resolver down")
	if _, err := host.DialContext(context.Background(), "tcp", "example.com:80"); !strings.Contains(err.Error(), "resolver down") {
		t.Fatalf("resolver error: %v", err)
	}
	if err := host.ValidateHost(context.Background(), "example.com", "443"); err == nil {
		t.Fatal("resolver failure must propagate")
	}

	// Allowlisted private hosts skip the range check entirely.
	allowGuard := NewDialGuard(URLSecurityConfig{
		AllowPrivateBaseUrls:   false,
		PrivateOriginAllowlist: map[string]bool{"http://127.0.0.1:443": true},
	}, resolver, nil)
	if !allowGuard.hostAllowlisted("127.0.0.1", "443") || !allowGuard.hostAllowlisted("127.0.0.1", "") {
		t.Fatal("allowlisted host and port forms")
	}
	// The allowlist only contains 127.0.0.1; other hosts are not covered,
	// and the portless form of an allowlisted default port still matches.
	if allowGuard.hostAllowlisted("10.0.0.9", "") || allowGuard.hostAllowlisted("10.0.0.9", "443") {
		t.Fatal("allowlist is host-or-host:port scoped")
	}
	// A host entry covers every port for that host.
	if !allowGuard.hostAllowlisted("127.0.0.1", "8080") {
		t.Fatal("host entry covers every port")
	}
	if err := allowGuard.ValidateHost(context.Background(), "127.0.0.1", "443"); err != nil {
		t.Fatalf("allowlisted validate: %v", err)
	}
	permissive := NewDialGuard(URLSecurityConfig{AllowPrivateBaseUrls: true}, resolver, nil)
	if !permissive.rangeCheckAllowed("127.0.0.1", "80") {
		t.Fatal("global allow covers everything")
	}

	// DialContext: invalid address, blocked literal, resolution with a private
	// address, empty resolution, and a successful validated dial.
	if _, _, err := net.SplitHostPort("no-port"); err == nil {
		t.Fatal("sanity")
	}
	if _, err := host.DialContext(context.Background(), "tcp", "invalid-address"); err == nil || !strings.Contains(err.Error(), "invalid address") {
		t.Fatalf("invalid address: %v", err)
	}
	if _, err := host.DialContext(context.Background(), "tcp", "192.168.1.1:80"); err == nil {
		t.Fatal("blocked literal dial must be refused")
	}
	resolver.addresses = []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}
	if _, err := host.DialContext(context.Background(), "tcp", "rebinded.example.com:80"); err == nil {
		t.Fatal("rebinded resolution must be refused")
	}
	resolver.addresses = nil
	resolver.err = nil
	if _, err := host.DialContext(context.Background(), "tcp", "empty.example.com:80"); err == nil || !strings.Contains(err.Error(), "no such host") {
		t.Fatalf("empty resolution: %v", err)
	}
	resolver.addresses = []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: nil}}
	if err := host.ValidateHost(context.Background(), "mixed.example.com", "80"); err == nil {
		t.Fatal("mixed resolution with a private address must fail")
	}
	// A successful dial through the guard: use the allowlisted loopback host
	// (a test environment cannot dial a public IP).
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			_ = conn.Close()
		}
	}()
	dialGuard := NewDialGuard(URLSecurityConfig{PrivateOriginAllowlist: map[string]bool{"http://127.0.0.1:443": true}}, resolver, nil)
	conn, err := dialGuard.DialContext(context.Background(), "tcp", "127.0.0.1:"+w7cPort(listener.Addr().String()))
	if err != nil {
		t.Fatalf("validated dial: %v", err)
	}
	_ = conn.Close()
}

func w7cPort(address string) string {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return "80"
	}
	return port
}

type w7cResolver struct {
	addresses []net.IPAddr
	err       error
}

func (r *w7cResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, r.err
}

func TestW7CJSONSanity(t *testing.T) {
	// Keep encoding/json referenced for the error-payload fixtures shared by
	// callers of this package.
	var value map[string]any
	if err := json.Unmarshal([]byte(`{"ok":true}`), &value); err != nil || !value["ok"].(bool) {
		t.Fatal("json sanity")
	}
}
