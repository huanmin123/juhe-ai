package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// dialGuardResolver 把任意主机解析为固定地址集，供 T164 的 strict 模式断言
// 使用（不触真实 DNS）。
type dialGuardResolver struct {
	addresses []net.IPAddr
}

func (r dialGuardResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, nil
}

type dialGuardCountingClient struct {
	calls    int
	response *http.Response
}

func (c *dialGuardCountingClient) Do(*http.Request) (*http.Response, error) {
	c.calls++
	return c.response, nil
}

type dialGuardCountingGate struct {
	*fakeGate
	admits int
}

func (g *dialGuardCountingGate) AdmitForeground(ctx context.Context, capability keymodelruntime.Capability, attemptID string) (keymodelruntime.ForegroundDecision, keymodelruntime.ForegroundPermit, uint64, error) {
	g.admits++
	return g.fakeGate.AdmitForeground(ctx, capability, attemptID)
}

func dialGuardOKClient() *dialGuardCountingClient {
	return &dialGuardCountingClient{response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}}
}

func dialGuardRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		t.Fatalf("build request %q: %v", target, err)
	}
	return request
}

// TestDispatchRejectsUnsafeTargetBeforeAdmission 钉住 T164：模型检测探针
// 的 Dispatcher 在 key-model/熔断准入之前完成目标校验，被拒绝的目标不得
// 计入账户传输失败，也不得发出请求。
func TestDispatchRejectsUnsafeTargetBeforeAdmission(t *testing.T) {
	strict := sharedupstreamhttp.NewDialGuard(
		sharedupstreamhttp.URLSecurityConfig{},
		dialGuardResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		nil,
	)
	gate := &dialGuardCountingGate{fakeGate: &fakeGate{admitted: true}}
	client := dialGuardOKClient()
	dispatcher := Dispatcher{Client: client, KeyModel: gate, DialGuard: strict}

	_, err := dispatcher.Dispatch(context.Background(), Request{
		HTTP:      dialGuardRequest(t, "http://rebind.example/v1/chat/completions"),
		AttemptID: "attempt",
	})
	var resolvedErr *sharedupstreamhttp.UnsafeResolvedUpstreamURLError
	if !errors.As(err, &resolvedErr) {
		t.Fatalf("Dispatch error = %v, want UnsafeResolvedUpstreamURLError", err)
	}
	if client.calls != 0 {
		t.Fatalf("unsafe target reached the HTTP client %d times", client.calls)
	}
	if gate.admits != 0 {
		t.Fatalf("unsafe target consumed %d key-model admissions", gate.admits)
	}
}

// TestDispatchValidatesProxyClientTarget 钉住代理分支：代理解析目标，
// transport guard 只校验代理主机，因此 Dispatcher 必须在发送前自行校验
// 目标 host，且拒绝发生在代理 client 被调用之前。
func TestDispatchValidatesProxyClientTarget(t *testing.T) {
	strict := sharedupstreamhttp.NewDialGuard(
		sharedupstreamhttp.URLSecurityConfig{},
		dialGuardResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		nil,
	)
	proxyClient := dialGuardOKClient()
	dispatcher := Dispatcher{Client: dialGuardOKClient(), KeyModel: &fakeGate{admitted: true}, DialGuard: strict}
	_, err := dispatcher.Dispatch(context.Background(), Request{
		HTTP:      dialGuardRequest(t, "http://rebind.example/v1/chat/completions"),
		Client:    proxyClient,
		AttemptID: "attempt",
	})
	var resolvedErr *sharedupstreamhttp.UnsafeResolvedUpstreamURLError
	if !errors.As(err, &resolvedErr) {
		t.Fatalf("proxied Dispatch error = %v, want UnsafeResolvedUpstreamURLError", err)
	}
	if proxyClient.calls != 0 {
		t.Fatalf("unsafe target reached the proxy client %d times", proxyClient.calls)
	}
}

func TestDispatchRejectsUnsafeTargetForms(t *testing.T) {
	strict := sharedupstreamhttp.NewDialGuard(
		sharedupstreamhttp.URLSecurityConfig{},
		dialGuardResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}},
		nil,
	)
	cases := []struct {
		name   string
		target string
	}{
		{name: "loopback literal", target: "http://127.0.0.1:8080/v1"},
		{name: "metadata literal", target: "http://169.254.169.254/latest/meta-data"},
		{name: "cgnat literal", target: "http://100.100.100.200/v1"},
		{name: "non-http scheme", target: "file:///etc/passwd"},
		{name: "missing host", target: "http:///v1"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := dialGuardOKClient()
			dispatcher := Dispatcher{Client: client, KeyModel: &fakeGate{admitted: true}, DialGuard: strict}
			_, err := dispatcher.Dispatch(context.Background(), Request{
				HTTP:      dialGuardRequest(t, testCase.target),
				AttemptID: "attempt",
			})
			if err == nil {
				t.Fatalf("Dispatch(%q) accepted an unsafe target", testCase.target)
			}
			if client.calls != 0 {
				t.Fatalf("Dispatch(%q) reached the HTTP client %d times", testCase.target, client.calls)
			}
		})
	}
}

// TestDispatchDialGuardPassthrough 保持既有放行条件：默认 allow-private、
// 显式 allowlist 与 nil guard（测试/嵌入形态）都不改变请求路径。
func TestDispatchDialGuardPassthrough(t *testing.T) {
	resolver := dialGuardResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	cases := []struct {
		name  string
		guard *sharedupstreamhttp.DialGuard
	}{
		{
			name: "allow private",
			guard: sharedupstreamhttp.NewDialGuard(
				sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true}, resolver, nil),
		},
		{
			name: "allowlisted origin",
			guard: sharedupstreamhttp.NewDialGuard(
				sharedupstreamhttp.URLSecurityConfig{PrivateOriginAllowlist: map[string]bool{"http://127.0.0.1:8080": true}}, resolver, nil),
		},
		{name: "nil guard"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := dialGuardOKClient()
			dispatcher := Dispatcher{Client: client, KeyModel: &fakeGate{admitted: true}, DialGuard: testCase.guard}
			result, err := dispatcher.Dispatch(context.Background(), Request{
				HTTP:      dialGuardRequest(t, "http://127.0.0.1:8080/v1/chat/completions"),
				AttemptID: "attempt",
			})
			if err != nil {
				t.Fatalf("Dispatch = %v, want nil", err)
			}
			if client.calls != 1 {
				t.Fatalf("HTTP client calls = %d, want 1", client.calls)
			}
			_ = result.CloseResponse()
		})
	}
}
