package main

// 生图 URL 下载最终目标校验（targetGuardRoundTripper，方案 A / BUG-0296）
// 的行为矩阵：校验必须发生在代理收到请求之前（strict 配置下私网目标的代
// 理命中计数为 0）、allowlist 与 AllowPrivateBaseUrls 的放行语义、公网字
// 面量不受影响、禁跟随重定向、SOCKS5H 场景校验先于拨号、非 http/https
// scheme 拒绝、域名目标走 guard 的解析半。stub 代理只记录不转发，全部用
// 例不产生真实出站流量。

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// recordingStubProxy 是记录型 stub HTTP 正向代理：handler 记录代理视角的
// 请求 URI（正向代理收到绝对 URI）与命中计数，固定返回 200 stub 响应，
// 不向任何目标转发。
type recordingStubProxy struct {
	server *httptest.Server
	mu     sync.Mutex
	hits   int
	uris   []string
}

func newRecordingStubProxy(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *recordingStubProxy {
	t.Helper()
	stub := &recordingStubProxy{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.hits++
		stub.uris = append(stub.uris, r.URL.String())
		stub.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("stub-image-bytes"))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *recordingStubProxy) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *recordingStubProxy) requestURIs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uris...)
}

// newTargetGuardTestClient 起一个记录型 stub 代理并按 guard 装配被测 client。
func newTargetGuardTestClient(t *testing.T, guard *upstreamhttp.DialGuard) (*http.Client, *recordingStubProxy) {
	t.Helper()
	stub := newRecordingStubProxy(t, nil)
	proxyURL, err := url.Parse(stub.server.URL)
	if err != nil {
		t.Fatalf("解析 stub 代理地址 %s: %v", stub.server.URL, err)
	}
	client, err := newTargetGuardProxyClient(proxyURL, guard)
	if err != nil {
		t.Fatalf("装配目标校验代理客户端: %v", err)
	}
	return client, stub
}

func TestChatImageProxyTargetGuardBlocksPrivateTargetBeforeProxy(t *testing.T) {
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{}, nil, nil)
	client, stub := newTargetGuardTestClient(t, guard)

	resp, err := client.Get("http://10.255.255.1:8080/private-image.png")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("私网字面量目标必须被拒绝，实际拿到响应 status=%d", resp.StatusCode)
	}
	var unsafe *upstreamhttp.UnsafeResolvedUpstreamURLError
	if !errors.As(err, &unsafe) {
		t.Fatalf("错误类型 = %T (%v), want *upstreamhttp.UnsafeResolvedUpstreamURLError", err, err)
	}
	if stub.hitCount() != 0 {
		t.Fatalf("stub 代理命中计数 = %d, want 0（校验必须发生在代理收到请求之前）", stub.hitCount())
	}
}

func TestChatImageProxyTargetGuardWrapsWithoutMutatingPoolEntry(t *testing.T) {
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{}, nil, nil)
	client, stub := newTargetGuardTestClient(t, guard)

	pooled, err := upstreamhttp.SharedClient(stub.server.URL, upstreamhttp.TransportOptions{DialGuard: guard})
	if err != nil {
		t.Fatalf("借用池条目: %v", err)
	}
	if pooled.Transport == client.Transport {
		t.Fatal("包装 client 的 Transport 必须是新增包装层，不得就地替换池条目的 Transport")
	}
	if pooled.CheckRedirect == nil {
		t.Fatal("池条目 CheckRedirect 不得被包装操作改写为 nil")
	}
}

func TestChatImageProxyTargetGuardAllowsAllowlistedPrivateTarget(t *testing.T) {
	stub := newRecordingStubProxy(t, nil)
	target := newRecordingStubProxy(t, nil) // 仅取私网地址作为目标；stub 代理不转发，目标服务器不会收到请求
	origin, err := upstreamhttp.NormalizePrivateUpstreamOrigin("测试 allowlist", target.server.URL)
	if err != nil {
		t.Fatalf("归一化 allowlist origin: %v", err)
	}
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{
		PrivateOriginAllowlist: map[string]bool{origin: true},
	}, nil, nil)
	proxyURL, err := url.Parse(stub.server.URL)
	if err != nil {
		t.Fatalf("解析 stub 代理地址: %v", err)
	}
	client, err := newTargetGuardProxyClient(proxyURL, guard)
	if err != nil {
		t.Fatalf("装配目标校验代理客户端: %v", err)
	}

	targetURI := target.server.URL + "/private-image.png"
	resp, err := client.Get(targetURI)
	if err != nil {
		t.Fatalf("allowlist 命中的私网目标必须放行: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取 stub 响应体: %v", err)
	}
	if string(body) != "stub-image-bytes" {
		t.Fatalf("响应体 = %q, want stub 固定响应", body)
	}
	if stub.hitCount() != 1 {
		t.Fatalf("stub 代理命中计数 = %d, want 1", stub.hitCount())
	}
	if uris := stub.requestURIs(); len(uris) != 1 || uris[0] != targetURI {
		t.Fatalf("代理视角请求 URI = %v, want [%s]（正向代理收到绝对 URI）", uris, targetURI)
	}
}

func TestChatImageProxyTargetGuardAllowsPrivateWhenDeploymentAllows(t *testing.T) {
	// 生产 env 默认形态：JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS 未显式收紧。
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}, nil, nil)
	client, stub := newTargetGuardTestClient(t, guard)

	resp, err := client.Get("http://10.255.255.1:8080/private-image.png")
	if err != nil {
		t.Fatalf("AllowPrivateBaseUrls=true 必须放行私网目标: %v", err)
	}
	defer resp.Body.Close()
	if stub.hitCount() != 1 {
		t.Fatalf("stub 代理命中计数 = %d, want 1", stub.hitCount())
	}
}

func TestChatImageProxyTargetGuardAllowsPublicLiteralTarget(t *testing.T) {
	// 目标保持严格配置下的公网字面量；allowlist 只用于放行 stub 代理主机本
	// 身（httptest 监听回环地址，连接层 guard 校验 socket 对端即代理主机，
	// 测试环境无法提供公网代理）。
	stub := newRecordingStubProxy(t, nil)
	proxyOrigin, err := upstreamhttp.NormalizePrivateUpstreamOrigin("测试代理放行", stub.server.URL)
	if err != nil {
		t.Fatalf("归一化 stub 代理 origin: %v", err)
	}
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{
		PrivateOriginAllowlist: map[string]bool{proxyOrigin: true},
	}, nil, nil)
	proxyURL, err := url.Parse(stub.server.URL)
	if err != nil {
		t.Fatalf("解析 stub 代理地址: %v", err)
	}
	client, err := newTargetGuardProxyClient(proxyURL, guard)
	if err != nil {
		t.Fatalf("装配目标校验代理客户端: %v", err)
	}

	resp, err := client.Get("http://93.184.216.34/img.png")
	if err != nil {
		t.Fatalf("公网字面量目标不得被拦截: %v", err)
	}
	defer resp.Body.Close()
	if stub.hitCount() != 1 {
		t.Fatalf("stub 代理命中计数 = %d, want 1", stub.hitCount())
	}
}

func TestChatImageProxyTargetGuardDoesNotFollowRedirect(t *testing.T) {
	stub := newRecordingStubProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://10.255.255.1:9/redirected.png")
		w.WriteHeader(http.StatusFound)
	})
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}, nil, nil)
	proxyURL, err := url.Parse(stub.server.URL)
	if err != nil {
		t.Fatalf("解析 stub 代理地址: %v", err)
	}
	client, err := newTargetGuardProxyClient(proxyURL, guard)
	if err != nil {
		t.Fatalf("装配目标校验代理客户端: %v", err)
	}

	resp, err := client.Get(stub.server.URL + "/first.png")
	if err != nil {
		t.Fatalf("请求 stub 代理: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302（包装 client 必须继承池条目不跟随重定向的契约）", resp.StatusCode)
	}
	if stub.hitCount() != 1 {
		t.Fatalf("stub 代理命中计数 = %d, want 1（302 不得产生第二次请求）", stub.hitCount())
	}
}

func TestChatImageProxyTargetGuardBlocksPrivateTargetBeforeSOCKSDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动 SOCKS listener: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	var connCount atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connCount.Add(1)
			conn.Close()
		}
	}()

	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{}, nil, nil)
	proxyURL := &url.URL{Scheme: "socks5h", Host: listener.Addr().String()}
	client, err := newTargetGuardProxyClient(proxyURL, guard)
	if err != nil {
		t.Fatalf("装配 socks5h 目标校验客户端: %v", err)
	}

	_, err = client.Get("http://10.255.255.1:8080/private-image.png")
	var unsafe *upstreamhttp.UnsafeResolvedUpstreamURLError
	if !errors.As(err, &unsafe) {
		t.Fatalf("错误类型 = %T (%v), want *upstreamhttp.UnsafeResolvedUpstreamURLError（预检先于拨号）", err, err)
	}
	if got := connCount.Load(); got != 0 {
		t.Fatalf("SOCKS listener 连接计数 = %d, want 0（校验先于拨号）", got)
	}
}

func TestChatImageProxyTargetGuardRejectsUnsupportedScheme(t *testing.T) {
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{}, nil, nil)
	client, stub := newTargetGuardTestClient(t, guard)

	resp, err := client.Get("ftp://example.com/a.png")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("非 http/https 目标协议必须被拒绝，实际拿到响应 status=%d", resp.StatusCode)
	}
	var unsafe *upstreamhttp.UnsafeResolvedUpstreamURLError
	if !errors.As(err, &unsafe) {
		t.Fatalf("错误类型 = %T (%v), want *upstreamhttp.UnsafeResolvedUpstreamURLError", err, err)
	}
	if stub.hitCount() != 0 {
		t.Fatalf("stub 代理命中计数 = %d, want 0", stub.hitCount())
	}
}

func TestChatImageProxyTargetGuardResolvesDomainTarget(t *testing.T) {
	// localhost 域名目标走 guard 的解析半：本地解析结果落在封锁段即拒绝。
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{}, nil, nil)
	client, stub := newTargetGuardTestClient(t, guard)

	_, port, err := net.SplitHostPort(stub.server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("拆解 stub 代理监听地址: %v", err)
	}
	resp, err := client.Get("http://localhost:" + port + "/img.png")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("localhost 域名目标在严格配置下必须被拒绝，实际拿到响应 status=%d", resp.StatusCode)
	}
	var unsafe *upstreamhttp.UnsafeResolvedUpstreamURLError
	if !errors.As(err, &unsafe) {
		t.Fatalf("错误类型 = %T (%v), want *upstreamhttp.UnsafeResolvedUpstreamURLError", err, err)
	}
	if stub.hitCount() != 0 {
		t.Fatalf("stub 代理命中计数 = %d, want 0", stub.hitCount())
	}
}
