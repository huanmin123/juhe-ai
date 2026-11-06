package accountprobe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// dialGuardTestResolver 把任意主机解析为固定地址集，供 validateProbeTarget
// 的 strict 模式断言使用（不触真实 DNS）。
type dialGuardTestResolver struct {
	addresses []net.IPAddr
}

func (resolver dialGuardTestResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return resolver.addresses, nil
}

// TestValidateProbeTargetDialGuard 钉住 SEC-03 的发送前目标校验：
//   - strict 模式下目标域名解析到回环地址必须被拒绝（代理分支的 transport
//     guard 只校验代理主机，不能替代目标校验）；
//   - 默认 allow-private 模式保持既有出站行为；
//   - nil guard / nil request 安全放行（测试与嵌入式调用形态）。
func TestValidateProbeTargetDialGuard(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://blocked.example/v1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	strict := &Service{dialGuard: upstreamhttp.NewDialGuard(
		upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: false},
		dialGuardTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		nil,
	)}
	var resolvedErr *upstreamhttp.UnsafeResolvedUpstreamURLError
	if err := strict.validateProbeTarget(context.Background(), request); !errors.As(err, &resolvedErr) {
		t.Fatalf("strict validateProbeTarget = %v, want UnsafeResolvedUpstreamURLError", err)
	}

	permissive := &Service{dialGuard: upstreamhttp.NewDialGuard(
		upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true},
		dialGuardTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
		nil,
	)}
	if err := permissive.validateProbeTarget(context.Background(), request); err != nil {
		t.Fatalf("allow-private validateProbeTarget = %v, want nil", err)
	}

	if err := (&Service{}).validateProbeTarget(context.Background(), request); err != nil {
		t.Fatalf("nil guard validateProbeTarget = %v, want nil", err)
	}
	if err := strict.validateProbeTarget(context.Background(), nil); err != nil {
		t.Fatalf("nil request validateProbeTarget = %v, want nil", err)
	}
}
