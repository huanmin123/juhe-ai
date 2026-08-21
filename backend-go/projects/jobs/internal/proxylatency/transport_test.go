package proxylatency

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllowedTargetURL(t *testing.T) {
	for _, raw := range []string{"http://example.test/path", "https://example.test/path"} {
		if _, err := parseTargetURL(raw); err != nil {
			t.Fatalf("allowed target %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "ftp://example.test", "https://user:pass@example.test", "https://example.test/#fragment"} {
		if _, err := parseTargetURL(raw); err == nil {
			t.Fatalf("disallowed target %q accepted", raw)
		}
	}
}

func TestProbeItemNon2xxIsNeutralButPassed(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "unavailable")
	}))
	defer proxy.Close()

	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: "http://provider.example/v1/models",
		ProxyURL:  proxy.URL,
		Timeout:   time.Second,
	})
	if result.Status != ItemPassed || result.Outcome != OutcomeNeutral || result.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("result=%+v", result)
	}
}

func TestProbeItemUsesNodeProxyProbeHeaders(t *testing.T) {
	var observed http.Header
	var observedHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = r.Header.Clone()
		observedHost = r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	proxyURL := strings.Replace(proxy.URL, "http://", "http://probe-user:probe-pass@", 1)

	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: "http://provider.example/v1/models",
		ProxyURL:  proxyURL,
		Timeout:   time.Second,
	})
	if result.Status != ItemPassed || result.Outcome != OutcomeSuccess {
		t.Fatalf("result=%+v", result)
	}
	if observed.Get("Accept") != "application/json,text/plain,*/*" || observed.Get("User-Agent") != "juhe-ai-proxy-test/0.1" {
		t.Fatalf("Node probe identity headers=%v", observed)
	}
	if observed.Get("Connection") != "close" || observed.Get("Proxy-Connection") != "close" || observed.Get("Accept-Encoding") != "" {
		t.Fatalf("Node probe close headers=%v", observed)
	}
	if observedHost != "provider.example" {
		t.Fatalf("target host=%q", observedHost)
	}
	if observed.Get("Proxy-Authorization") != "Basic cHJvYmUtdXNlcjpwcm9iZS1wYXNz" {
		t.Fatalf("proxy authorization=%q", observed.Get("Proxy-Authorization"))
	}
}

func TestNodeProbeHeadersAreScopedToForwardProxyRequests(t *testing.T) {
	targetHTTP, err := url.Parse("http://provider.example/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	forwardProxy, err := url.Parse("http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, targetHTTP.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	applyNodeProbeHeaders(request, targetHTTP, forwardProxy)
	if request.Host != targetHTTP.Host || !request.Close || request.Header.Get("Connection") != "close" || request.Header.Get("Proxy-Connection") != "close" {
		t.Fatalf("forward proxy headers=%v host=%q close=%t", request.Header, request.Host, request.Close)
	}

	targetHTTPS, err := url.Parse("https://provider.example/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	tunneled, err := http.NewRequest(http.MethodGet, targetHTTPS.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	applyNodeProbeHeaders(tunneled, targetHTTPS, forwardProxy)
	if tunneled.Host != targetHTTPS.Host || tunneled.Close || tunneled.Header.Get("Connection") != "" || tunneled.Header.Get("Proxy-Connection") != "" {
		t.Fatalf("tunneled target leaked forward-proxy headers=%v host=%q close=%t", tunneled.Header, tunneled.Host, tunneled.Close)
	}
	if tunneled.Header.Get("Accept") != "application/json,text/plain,*/*" || tunneled.Header.Get("User-Agent") != "juhe-ai-proxy-test/0.1" {
		t.Fatalf("tunneled probe identity headers=%v", tunneled.Header)
	}
}

func TestProbeItemResponseOverflowDrainsAndPasses(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, maxResponseBodyBytes+1))
	}))
	defer proxy.Close()

	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: "http://provider.example/",
		ProxyURL:  proxy.URL,
		Timeout:   time.Second,
	})
	if result.Status != ItemPassed || result.Outcome != OutcomeSuccess || result.HTTPStatus != http.StatusOK {
		t.Fatalf("result=%+v", result)
	}
}

func TestProbeItemResponseOverflowWaitsForCompleteFraming(t *testing.T) {
	sentCap := make(chan struct{})
	releaseSuffix := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("proxy response does not support flushing")
			return
		}
		_, _ = w.Write(make([]byte, maxResponseBodyBytes))
		flusher.Flush()
		close(sentCap)
		<-releaseSuffix
		_, _ = w.Write([]byte("suffix"))
		flusher.Flush()
	}))
	defer proxy.Close()
	resultCh := make(chan ItemResult, 1)
	go func() {
		resultCh <- ProbeItem(context.Background(), ProbeRequest{TargetURL: "http://provider.example/", ProxyURL: proxy.URL, Timeout: time.Second})
	}()
	select {
	case <-sentCap:
	case <-time.After(time.Second):
		t.Fatal("proxy did not send capped body")
	}
	select {
	case result := <-resultCh:
		t.Fatalf("probe completed before response framing: %+v", result)
	default:
	}
	close(releaseSuffix)
	select {
	case result := <-resultCh:
		if result.Status != ItemPassed || result.Outcome != OutcomeSuccess {
			t.Fatalf("complete oversized response result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not complete after response end")
	}
}

func TestProbeItemHTTPSConnectHeaderLimit(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	connectLines := make(chan []string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		lines := make([]string, 0, 8)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			if line == "\r\n" {
				break
			}
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		close(accepted)
		connectLines <- lines
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 Connection Established\r\nX-Large: %s\r\n\r\n", strings.Repeat("a", 64*1024))
	}()
	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: target.URL,
		ProxyURL:  "http://" + listener.Addr().String(),
		Timeout:   time.Second,
	})
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("proxy did not receive CONNECT")
	}
	select {
	case lines := <-connectLines:
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "Proxy-Connection: close") {
			t.Fatalf("CONNECT request missing Node close header: %q", joined)
		}
		if !strings.Contains(joined, "Host: ") {
			t.Fatalf("CONNECT request missing target authority: %q", joined)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy CONNECT headers not captured")
	}
	if result.Status != ItemFailed || result.Outcome != OutcomeUpstreamFailure || targetHits.Load() != 0 {
		t.Fatalf("CONNECT header overflow result=%+v target_hits=%d", result, targetHits.Load())
	}
}

func TestProbeItemSocksTunnelOmitsForwardProxyHeaders(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestLines := make(chan []string, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		var greeting [2]byte
		if _, err := io.ReadFull(reader, greeting[:]); err != nil {
			return
		}
		methods := make([]byte, greeting[1])
		if _, err := io.ReadFull(reader, methods); err != nil {
			return
		}
		if _, err := connection.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
		var header [4]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return
		}
		switch header[3] {
		case 0x01:
			if _, err := io.CopyN(io.Discard, reader, net.IPv4len); err != nil {
				return
			}
		case 0x03:
			var length [1]byte
			if _, err := io.ReadFull(reader, length[:]); err != nil {
				return
			}
			if _, err := io.CopyN(io.Discard, reader, int64(length[0])); err != nil {
				return
			}
		default:
			return
		}
		var port [2]byte
		if _, err := io.ReadFull(reader, port[:]); err != nil {
			return
		}
		if _, err := connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80}); err != nil {
			return
		}
		lines := make([]string, 0, 8)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			if line == "\r\n" {
				break
			}
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		requestLines <- lines
		_, _ = fmt.Fprint(connection, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	}()

	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: "http://relay.example/v1/models",
		ProxyURL:  "socks5://" + listener.Addr().String(),
		Timeout:   time.Second,
	})
	if result.Status != ItemPassed || result.Outcome != OutcomeSuccess || result.HTTPStatus != http.StatusNoContent {
		t.Fatalf("SOCKS tunneled probe result=%+v", result)
	}
	select {
	case lines := <-requestLines:
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "Accept: application/json,text/plain,*/*") || !strings.Contains(joined, "User-Agent: juhe-ai-proxy-test/0.1") {
			t.Fatalf("SOCKS target missing probe identity headers: %q", joined)
		}
		if strings.Contains(joined, "Proxy-Connection:") || strings.Contains(joined, "Connection: close") || strings.Contains(joined, "Accept-Encoding:") {
			t.Fatalf("SOCKS target leaked forward-proxy close headers: %q", joined)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS target request not captured")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("SOCKS fixture did not finish")
	}
}

func TestProbeItemConnectionFailureIsUpstreamFailure(t *testing.T) {
	result := ProbeItem(context.Background(), ProbeRequest{
		TargetURL: "http://provider.example/",
		ProxyURL:  "http://127.0.0.1:1",
		Timeout:   time.Second,
	})
	if result.Status != ItemFailed || result.Outcome != OutcomeUpstreamFailure || result.ErrorCode != "transport" {
		t.Fatalf("result=%+v", result)
	}
}

func TestProbeItemCancellationIsUnknownTaskFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := ProbeItem(ctx, ProbeRequest{
		TargetURL: "https://provider.example/",
		ProxyURL:  "http://127.0.0.1:1",
		Timeout:   time.Second,
	})
	if result.Status != ItemUnknown || result.Outcome != OutcomeProbeTaskFailure || result.ErrorCode != "probe_cancelled" {
		t.Fatalf("result=%+v", result)
	}
}

func TestProbeItemRejectsMissingProxyInsteadOfFallingBack(t *testing.T) {
	result := ProbeItem(context.Background(), ProbeRequest{TargetURL: "https://provider.example/", Timeout: time.Second})
	if result.Status != ItemUnknown || result.Outcome != OutcomeProbeTaskFailure || result.ErrorCode != "proxy_required" {
		t.Fatalf("result=%+v", result)
	}
}
