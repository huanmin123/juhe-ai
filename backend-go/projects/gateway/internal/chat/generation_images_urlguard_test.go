package chat

// 生图 URL 下载的 SSRF 防护回归钉（upstream 回退 url 直接下载缺陷修复）：
// 回退默认客户端必须经 upstreamhttp.DialGuard 拒绝本机/内网/链路本地/保留
// 地址，且不跟随重定向；重定向若被跟随，guard 也必须在拨号前拒绝私网目标。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	upstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// TestChatImageURLDownloadRejectsPrivateTargets nil 注入回落默认客户端时，
// 指向私网/保留地址的 url 必须在建立连接前被拒绝，错误信息可定位。
func TestChatImageURLDownloadRejectsPrivateTargets(t *testing.T) {
	targets := []string{
		"http://127.0.0.1:1/img.png",               // 本机回环
		"http://10.255.255.1/img.png",              // 内网网段
		"http://169.254.169.254/latest/meta-data/", // 云元数据链路本地
		"http://[::1]:1/img.png",                   // IPv6 回环
	}
	for _, target := range targets {
		_, err := downloadGeneratedImage(context.Background(), nil, target)
		if err == nil {
			t.Fatalf("%s 应被拒绝", target)
		}
		var guardErr *upstreamhttp.UnsafeResolvedUpstreamURLError
		if !errors.As(err, &guardErr) {
			t.Fatalf("%s 应为 guard 保留网段拒绝: %v", target, err)
		}
		if !strings.Contains(err.Error(), "上游 Base URL 不能指向本机、内网、链路本地或保留地址") {
			t.Fatalf("%s 错误信息不可定位: %v", target, err)
		}
		if !strings.Contains(err.Error(), "图像生成 url 下载失败") {
			t.Fatalf("%s 缺少下载失败上下文: %v", target, err)
		}
	}
}

// TestChatImageURLDownloadDoesNotFollowRedirectToPrivateTarget 重定向不跟随：
// 302 到私网地址时按非 2xx 收敛为下载失败；对照用例证明若跟随重定向，
// guard 也会在拨号前拒绝私网目标。
func TestChatImageURLDownloadDoesNotFollowRedirectToPrivateTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://10.255.255.1/img.png", http.StatusFound)
			return
		}
		t.Errorf("不应有对 %s 的后续请求", r.URL.Path)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	// 允许列表只放行 httptest origin 本身：初始请求可达，重定向目标仍被拒。
	newClient := func(followRedirects bool) *http.Client {
		transport, transportErr := upstreamhttp.NewTransport("", upstreamhttp.TransportOptions{
			DialGuard: upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{
				PrivateOriginAllowlist: map[string]bool{upstreamhttp.UpstreamOriginKey(parsed): true},
			}, nil, nil),
		})
		if transportErr != nil {
			t.Fatal(transportErr)
		}
		client := &http.Client{Transport: transport, Timeout: 60 * time.Second}
		if !followRedirects {
			client.CheckRedirect = func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}
		}
		return client
	}

	t.Run("不跟随：302 收敛为下载失败", func(t *testing.T) {
		_, err := downloadGeneratedImage(context.Background(), newClient(false), server.URL+"/start")
		if err == nil || !strings.Contains(err.Error(), "图像生成 url 下载失败（HTTP 302）") {
			t.Fatalf("重定向应按 302 收敛为下载失败: %v", err)
		}
	})
	t.Run("对照：若跟随则 guard 拒绝私网目标", func(t *testing.T) {
		_, err := downloadGeneratedImage(context.Background(), newClient(true), server.URL+"/start")
		var guardErr *upstreamhttp.UnsafeResolvedUpstreamURLError
		if !errors.As(err, &guardErr) {
			t.Fatalf("跟随重定向拨号 10.x 应被 guard 拒绝: %v", err)
		}
	})
}

// TestChatImageURLDownloadClientContract 回退默认客户端契约：60s 超时、
// 不跟随重定向、transport 经 DialGuard 拨号。
func TestChatImageURLDownloadClientContract(t *testing.T) {
	if chatImageURLDownloadClient.Timeout != 60*time.Second {
		t.Fatalf("Timeout = %v, want 60s", chatImageURLDownloadClient.Timeout)
	}
	redirectErr := chatImageURLDownloadClient.CheckRedirect(&http.Request{}, []*http.Request{})
	if !errors.Is(redirectErr, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect 应返回 ErrUseLastResponse: %v", redirectErr)
	}
	transport, ok := chatImageURLDownloadClient.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatalf("transport 应带 DialContext: %T", chatImageURLDownloadClient.Transport)
	}
	if _, dialErr := transport.DialContext(context.Background(), "tcp", "127.0.0.1:1"); dialErr == nil {
		t.Fatal("guard 应拒绝本机回环拨号")
	} else {
		var guardErr *upstreamhttp.UnsafeResolvedUpstreamURLError
		if !errors.As(dialErr, &guardErr) {
			t.Fatalf("回环拨号应被 guard 拒绝: %v", dialErr)
		}
	}
}
