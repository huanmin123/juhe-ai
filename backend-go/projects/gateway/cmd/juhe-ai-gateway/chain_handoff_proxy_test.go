package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 反向代理透传验证（T121 本地等价物）：非流式交接必须先提交 200 传输头再
// 断连，代理才会把「已开始响应但正文截断」透传给客户端；直接在未提交任何
// 响应时断连会被代理合成为 502（客户端收到错误响应，不是断连）。
func TestClientHandoffThroughReverseProxyStaysTruncated(t *testing.T) {
	newProxy := func(t *testing.T, backendHandler http.HandlerFunc) *httptest.Server {
		t.Helper()
		backend := httptest.NewServer(backendHandler)
		t.Cleanup(backend.Close)
		target, err := url.Parse(backend.URL)
		if err != nil {
			t.Fatalf("parse backend url: %v", err)
		}
		front := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
		t.Cleanup(front.Close)
		return front
	}

	t.Run("先提交200头再断连保持截断", func(t *testing.T) {
		front := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
			res := gatewaypreauth.NewTrackingWriter(w)
			if interrupted, _ := interruptDownstreamConnection(res); !interrupted {
				t.Error("底层真实连接必须可 Hijack")
			}
		})
		resp, err := http.Get(front.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d，提交 200 后断连不得被代理改写为错误状态", resp.StatusCode)
		}
		if _, readErr := io.ReadAll(resp.Body); readErr == nil {
			t.Fatal("代理必须把截断正文透传给客户端（网络级读错误）")
		}
	})

	t.Run("未提交响应直接断连被代理合成502", func(t *testing.T) {
		front := newProxy(t, func(w http.ResponseWriter, _ *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("底层真实连接必须可 Hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		})
		resp, err := http.Get(front.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d，未提交响应的 pre-commit 断连应被代理合成为 502", resp.StatusCode)
		}
	})
}
