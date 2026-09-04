package gatewaypreauth

import (
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
)

// 表驱动测试：metadata.ts 的提取行为逐字段对齐。

func TestExtractBearerToken(t *testing.T) {
	cases := []struct {
		name          string
		authorization string
		wantToken     string
		wantOK        bool
	}{
		{"缺失", "", "", false},
		{"无 Bearer 前缀", "Basic abc", "", false},
		{"标准 Bearer", "Bearer sk-abc", "sk-abc", true},
		{"小写 bearer", "bearer sk-abc", "sk-abc", true},
		{"多余空白", "Bearer   sk-abc  ", "sk-abc", true},
		{"空白 token", "Bearer    ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, ok := ExtractBearerToken(tc.authorization)
			if ok != tc.wantOK || token != tc.wantToken {
				t.Fatalf("got (%q, %v), want (%q, %v)", token, ok, tc.wantToken, tc.wantOK)
			}
		})
	}
}

func TestExtractClientIP(t *testing.T) {
	cases := []struct {
		name       string
		clientIP   string
		remoteAddr string
		want       string
		wantOK     bool
	}{
		{"IPv4", "203.0.113.9", "", "203.0.113.9", true},
		{"IPv4 带端口", "203.0.113.9:5678", "", "203.0.113.9", true},
		{"IPv6 拒绝", "2001:db8::1", "", "", false},
		{"映射 IPv6 剥离", "::ffff:203.0.113.9", "", "203.0.113.9", true},
		{"括号 IPv6 剥离后仍拒绝", "[2001:db8::1]:443", "", "", false},
		{"回退 remoteAddress", "", "198.51.100.7:1234", "198.51.100.7", true},
		{"空值", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &GatewayRequest{ClientIP: tc.clientIP, RemoteAddr: tc.remoteAddr}
			got, ok := ExtractClientIP(req)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestRequestModel(t *testing.T) {
	t.Run("Gemini 路径模型", func(t *testing.T) {
		req := newBareRequest("POST", "/v1beta/models/gemini-2.5-pro:generateContent?key=k")
		model, ok := RequestModel(req)
		if !ok || model != "gemini-2.5-pro" {
			t.Fatalf("got %q %v", model, ok)
		}
	})
	t.Run("Gemini 路径模型 URL 编码", func(t *testing.T) {
		req := newBareRequest("POST", "/models/my%20model:countTokens")
		model, ok := RequestModel(req)
		if !ok || model != "my model" {
			t.Fatalf("got %q %v", model, ok)
		}
	})
	t.Run("bodyState 模型优先于 body", func(t *testing.T) {
		state := "state-model"
		req := &GatewayRequest{
			HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil),
			Body: &gatewaybody.Request{State: &gatewaybody.BodyState{Model: &state}},
		}
		req.Body.Body = map[string]any{"model": "body-model"}
		model, ok := RequestModel(req)
		if !ok || model != "state-model" {
			t.Fatalf("got %q %v", model, ok)
		}
	})
	t.Run("body 模型回退", func(t *testing.T) {
		req := &GatewayRequest{HTTP: httptest.NewRequest("POST", "/v1/chat/completions", nil)}
		req.Body = &gatewaybody.Request{Body: map[string]any{"model": "body-model"}}
		model, ok := RequestModel(req)
		if !ok || model != "body-model" {
			t.Fatalf("got %q %v", model, ok)
		}
	})
	t.Run("缺失", func(t *testing.T) {
		req := newBareRequest("POST", "/v1/chat/completions")
		if _, ok := RequestModel(req); ok {
			t.Fatal("不应解析出模型")
		}
	})
}

func TestRequestStream(t *testing.T) {
	t.Run("bodyState stream false 不回退", func(t *testing.T) {
		streamFalse := false
		req := &GatewayRequest{HTTP: httptest.NewRequest("GET", "/v1beta/interactions/abc?stream=true", nil)}
		req.Body = &gatewaybody.Request{State: &gatewaybody.BodyState{Stream: &streamFalse}}
		if RequestStream(req) {
			t.Fatal("bodyState.stream=false 时应为 false")
		}
	})
	t.Run("Gemini interaction GET stream 查询", func(t *testing.T) {
		req := newBareRequest("GET", "/v1beta/interactions/abc?stream=true")
		if !RequestStream(req) {
			t.Fatal("应为 true")
		}
	})
	t.Run("Gemini interaction GET 非 stream 查询", func(t *testing.T) {
		req := newBareRequest("GET", "/v1beta/interactions/abc?stream=TRUE")
		if !RequestStream(req) {
			t.Fatal("大小写不敏感应为 true")
		}
	})
	t.Run("非 interactions 路径", func(t *testing.T) {
		req := newBareRequest("GET", "/v1/models?stream=true")
		if RequestStream(req) {
			t.Fatal("应为 false")
		}
	})
}

func TestRequestEndpoint(t *testing.T) {
	req := newBareRequest("post", "/v1/chat/completions?x=1")
	if got := RequestEndpoint(req); got != "POST /v1/chat/completions" {
		t.Fatalf("got %q", got)
	}
}

func TestQueryToken(t *testing.T) {
	req := newBareRequest("POST", "/v1beta/models/m:generateContent?key=abc")
	value, ok := queryToken(req, "key")
	if !ok || value != "abc" {
		t.Fatalf("got %q %v", value, ok)
	}
	if _, ok := queryToken(req, "missing"); ok {
		t.Fatal("缺失的 key 不应命中")
	}
	req2 := newBareRequest("POST", "/v1/chat/completions")
	if _, ok := queryToken(req2, "key"); ok {
		t.Fatal("无查询串不应命中")
	}
}

// newBareRequest builds a GatewayRequest with no body pipeline state.
func newBareRequest(method, target string) *GatewayRequest {
	req := httptest.NewRequest(method, target, nil)
	return &GatewayRequest{HTTP: req, RemoteAddr: "203.0.113.9:44556", ClientIP: "203.0.113.9"}
}
