package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// BUG-0268 传导契约测试：Node 的网关协议匹配按"剥可选版本前缀"语义
// （openai/anthropic 剥 /v1、gemini 剥 /v1beta），Go 入口必须把根形态与
// /v1beta 形态放行进链，且**重写后的形态必须通过真实链协议门**
// （gatewayIsProtocolRequest）——stub 链只证明 mux 放行，不证明门受理
// （复审抓到的假绿盲区）；非协议路径保持 kernel 404 JSON 兜底，不得进链。

// recordingChain 记录链收到的每次请求路径（来源断言探针）。
type recordingChain struct {
	paths    []string
	methods  []string
	queries  []string
	statuses []int
}

func (c *recordingChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.paths = append(c.paths, r.URL.Path)
	c.methods = append(c.methods, r.Method)
	c.queries = append(c.queries, r.URL.RawQuery)
	w.WriteHeader(http.StatusOK)
}

// TestGatewayRootFormV1AdapterRewritesToCanonicalPrefix 验证 openai/
// anthropic/compat 族重写规则：根形态补 /v1，query/method 原样保留。
func TestGatewayRootFormV1AdapterRewritesToCanonicalPrefix(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{
			name: "根形态 responses",
			req:  httptest.NewRequest(http.MethodPost, "/responses", nil),
			want: "/v1/responses",
		},
		{
			name: "根形态 responses/compact",
			req:  httptest.NewRequest(http.MethodPost, "/responses/compact", nil),
			want: "/v1/responses/compact",
		},
		{
			name: "根形态 chat/completions 带 query",
			req:  httptest.NewRequest(http.MethodPost, "/chat/completions?stream=true", nil),
			want: "/v1/chat/completions",
		},
		{
			name: "根形态 anthropic messages",
			req:  httptest.NewRequest(http.MethodPost, "/messages", nil),
			want: "/v1/messages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingChain{}
			gatewayRootFormV1Adapter(rec).ServeHTTP(httptest.NewRecorder(), tc.req)
			if len(rec.paths) != 1 {
				t.Fatalf("链应恰好收到 1 次转发，实际 %d 次", len(rec.paths))
			}
			if rec.paths[0] != tc.want {
				t.Fatalf("重写路径错误：期望 %s，实际 %s", tc.want, rec.paths[0])
			}
			if rec.queries[0] != tc.req.URL.RawQuery {
				t.Fatalf("query 丢失：期望 %q，实际 %q", tc.req.URL.RawQuery, rec.queries[0])
			}
			if rec.methods[0] != tc.req.Method {
				t.Fatalf("method 丢失：期望 %s，实际 %s", tc.req.Method, rec.methods[0])
			}
		})
	}
}

// TestGatewayRootFormAdapterPreservesRawPathEscapes 真断言 EscapedPath：
// 段内 %2F 原始转义不得因 Path/RawPath 失配被重新编码丢失（实现若清空
// RawPath 该测试必须失败）。
func TestGatewayRootFormAdapterPreservesRawPathEscapes(t *testing.T) {
	var gotEscaped string
	rec := chainSpy(func(r *http.Request) { gotEscaped = r.URL.EscapedPath() })
	req := httptest.NewRequest(http.MethodPost, "http://host/models/a%2Fb:generateContent", nil)
	// 走 v1 adapter 的重写路径验证转义保留机制。
	gatewayRootFormV1Adapter(rec).ServeHTTP(httptest.NewRecorder(), req)
	if gotEscaped != "/v1/models/a%2Fb:generateContent" {
		t.Fatalf("EscapedPath 转义丢失或被改写：期望 /v1/models/a%%2Fb:generateContent，实际 %s", gotEscaped)
	}
}

func chainSpy(onRequest func(*http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		onRequest(r)
		w.WriteHeader(http.StatusOK)
	})
}

// TestGatewayRootFormsPassRealProtocolGate 门传导断言（真实链门，非 stub）：
// 每个放行形态经入口分派后的请求必须被 gatewayIsProtocolRequest 受理。
// v1 族断言重写后的请求过门；gemini 族断言原样形态过门。
func TestGatewayRootFormsPassRealProtocolGate(t *testing.T) {
	// v1 族：根形态经 v1 adapter 重写为 /v1 前缀后，门 openai 谓词读原始
	// RequestURI（contains，前缀无关）受理。
	v1Forms := []struct {
		method string
		uri    string
	}{
		{http.MethodPost, "/responses"},
		{http.MethodPost, "/responses/compact"},
		{http.MethodPost, "/chat/completions"},
		{http.MethodGet, "/models"},
		{http.MethodPost, "/embeddings"},
		{http.MethodPost, "/images/generations"},
		{http.MethodPost, "/audio/transcriptions"},
		{http.MethodPost, "/messages"},
		{http.MethodPost, "/messages/count_tokens"},
	}
	for _, tc := range v1Forms {
		gate := func(r *http.Request) bool {
			return gatewayIsProtocolRequest(gatewaypreauth.NewGatewayRequest(r))
		}
		t.Run("gate-v1 "+tc.method+" "+tc.uri, func(t *testing.T) {
			var forwarded *http.Request
			rec := chainSpy(func(r *http.Request) { forwarded = r })
			gatewayRootFormV1Adapter(rec).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.uri, nil))
			if forwarded == nil {
				t.Fatalf("未转发进链")
			}
			if !gate(forwarded) {
				t.Fatalf("重写后形态未通过真实协议门：%s %s → %s", tc.method, tc.uri, forwarded.URL.Path)
			}
		})
	}

	// compat 族（files/containers/vector_stores）：链内不走协议门，过门
	// 失败后由 chain.compat 分派器按 chainCompatFamilies 前缀服务
	//（chain_v1.go 门的 c.compat fallback，Go 既有分层）——断言重写后
	// 路径命中 compat 家族前缀。
	compatForms := []struct {
		method string
		uri    string
	}{
		{http.MethodGet, "/files"},
		{http.MethodGet, "/vector_stores/vs_123"},
		{http.MethodGet, "/containers"},
	}
	for _, tc := range compatForms {
		t.Run("gate-compat "+tc.method+" "+tc.uri, func(t *testing.T) {
			var forwarded *http.Request
			rec := chainSpy(func(r *http.Request) { forwarded = r })
			gatewayRootFormV1Adapter(rec).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.uri, nil))
			if forwarded == nil {
				t.Fatalf("未转发进链")
			}
			matched := false
			for _, family := range chainCompatFamilies {
				if forwarded.URL.Path == family || strings.HasPrefix(forwarded.URL.Path, family+"/") {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("重写后形态未命中 compat 家族：%s %s → %s", tc.method, tc.uri, forwarded.URL.Path)
			}
		})
	}

	// gemini 族：原样透传形态过门（gemini 谓词自剥 /v1beta；v1beta 前缀使
	// openai/anthropic 谓词不中，还原 Node 驱动优先级语义）。
	geminiForms := []struct {
		method string
		uri    string
	}{
		{http.MethodPost, "/models/gemini-2.0-flash:generateContent"},
		{http.MethodPost, "/models/gemini-2.0-flash:streamGenerateContent?alt=sse"},
		{http.MethodGet, "/v1beta/models"},
		{http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent"},
		{http.MethodGet, "/v1beta/interactions/abc"},
		{http.MethodGet, "/interactions/abc"},
	}
	for _, tc := range geminiForms {
		t.Run("gate-gemini "+tc.method+" "+tc.uri, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.uri, nil)
			// gemini 族原样透传：链收到的请求即原始请求。
			if !gatewayIsProtocolRequest(gatewaypreauth.NewGatewayRequest(req)) {
				t.Fatalf("原样形态未通过真实协议门：%s %s", tc.method, tc.uri)
			}
		})
	}

	// 反例：补 /v1 的 gemini 形态必须过不了门（锁定"不得补 /v1"的分流
	// 依据——gatewaygemini 谓词锚定 ^/models 不剥 /v1）。
	t.Run("gate-negative v1-prefixed gemini must not pass", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/models/gemini-2.0-flash:generateContent", nil)
		// RequestURI 原样 + URL 也为同形态（模拟若错误地补了 /v1 且
		// RequestURI 一起重写的形态）。
		req.RequestURI = "/v1/models/gemini-2.0-flash:generateContent"
		if gatewayIsProtocolRequest(gatewaypreauth.NewGatewayRequest(req)) {
			t.Fatalf("补 /v1 的 gemini 形态不应过门（分流依据失效）")
		}
	})
}

// TestMountGatewayRootFormsDispatch 三协议根形态经 kernel mux 分派进链，
// v1 族收到规范 /v1 路径、gemini 族收到原样路径；非协议路径落 kernel
// 404 兜底不进链（扫描器噪音/管理面探测隔离）。
func TestMountGatewayRootFormsDispatch(t *testing.T) {
	kern := kernel.New(kernel.Options{})
	rec := &recordingChain{}
	kern.Register("/v1", rec)
	kern.Register("/v1/", rec)
	mountGatewayRootForms(kern, rec)
	handler := kern.Handler()

	admitted := []struct {
		path   string
		method string
		want   string // 链收到的 URL.Path
	}{
		{"/responses", http.MethodPost, "/v1/responses"},
		{"/responses/compact", http.MethodPost, "/v1/responses/compact"},
		{"/chat/completions", http.MethodPost, "/v1/chat/completions"},
		{"/models", http.MethodGet, "/v1/models"},
		{"/models/gemini-2.0-flash:generateContent", http.MethodPost, "/models/gemini-2.0-flash:generateContent"},
		{"/embeddings", http.MethodPost, "/v1/embeddings"},
		{"/images/generations", http.MethodPost, "/v1/images/generations"},
		{"/audio/transcriptions", http.MethodPost, "/v1/audio/transcriptions"},
		{"/files", http.MethodGet, "/v1/files"},
		{"/vector_stores/vs_123", http.MethodGet, "/v1/vector_stores/vs_123"},
		{"/messages", http.MethodPost, "/v1/messages"},
		{"/messages/count_tokens", http.MethodPost, "/v1/messages/count_tokens"},
		{"/interactions/abc", http.MethodGet, "/interactions/abc"},
		{"/v1beta/models/gemini-2.0-flash:generateContent", http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent"},
	}
	for _, tc := range admitted {
		t.Run("admitted "+tc.method+" "+tc.path, func(t *testing.T) {
			before := len(rec.paths)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("期望链 200，实际 %d", w.Code)
			}
			if len(rec.paths) != before+1 {
				t.Fatalf("请求未进入链：%s %s", tc.method, tc.path)
			}
			if got := rec.paths[len(rec.paths)-1]; got != tc.want {
				t.Fatalf("链收到路径错误：期望 %s，实际 %s", tc.want, got)
			}
		})
	}

	rejected := []struct {
		path   string
		method string
	}{
		{"/wp", http.MethodGet},
		{"/.env", http.MethodGet},
		{"/.git/config", http.MethodGet},
		{"/wordpress", http.MethodGet},
		{"/some/random/path", http.MethodPost},
		{"/completions", http.MethodPost},
		{"/v2/responses", http.MethodPost},
	}
	for _, tc := range rejected {
		t.Run("rejected "+tc.method+" "+tc.path, func(t *testing.T) {
			before := len(rec.paths)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("期望 404 兜底，实际 %d", w.Code)
			}
			if len(rec.paths) != before {
				t.Fatalf("非协议路径不得进入链：%s %s", tc.method, tc.path)
			}
		})
	}
}

// TestMountGatewayRootFormsNilSafety 挂载入口的 nil 守卫（组合测试夹具
// 部分装配场景）。
func TestMountGatewayRootFormsNilSafety(t *testing.T) {
	mountGatewayRootForms(nil, nil) // 不得 panic
	rec := &recordingChain{}
	kern := kernel.New(kernel.Options{})
	mountGatewayRootForms(kern, nil) // 不得 panic、不注册
	mountGatewayRootForms(nil, rec)
	handler := kern.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/responses", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("nil chain 挂载不得生效，期望 404，实际 %d", w.Code)
	}
}
