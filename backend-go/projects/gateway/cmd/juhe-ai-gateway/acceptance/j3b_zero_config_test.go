package acceptance

// X05 场景 1 变体（2026-09-21 模型检测默认常驻）：fresh 环境不加任何
// J3b env——handoff/readiness 家族与 Redis 全部缺省 → 自动认领 + 进程内
// memory 准入。证明：健康面 j3bReady=true、管理 listener 挂载模型检测
// 路由、未认证请求被拒、seed 管理员会话可直接调用模型检测 API
// （modelcheckauth 复用同一业务库会话事实）。
import (
	"io"
	"net/http"
	"testing"
)

func TestAcceptanceFreshSQLiteBootModelCheckZeroConfig(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{J3bPinnedListener: true})

	health := waitForProcessHealthReady(t, fixture)
	if health["j3bReady"] != true {
		t.Fatalf("zero-config /health j3bReady=%v want true: %#v", health["j3bReady"], health)
	}

	// 未认证访问必须被 J3b 管理鉴权拒绝，证明路由挂在独立 listener 且
	// 鉴权前置生效。
	unauthorized, err := http.Get(fixture.j3bURL + "/model-checks/run/active")
	if err != nil {
		t.Fatalf("GET j3b run/active without session: %v", err)
	}
	_, _ = io.Copy(io.Discard, unauthorized.Body)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized && unauthorized.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated j3b status=%d want 401/403", unauthorized.StatusCode)
	}

	// seed 管理员登录主入口后，同一会话 cookie（按 host 匹配、不分端口）
	// 直接访问 J3b listener 的模型检测 API。
	client := newClient(t, fixture.baseURL)
	client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": acceptanceAdminPassword}, wantStatus(http.StatusOK))
	request, err := http.NewRequest(http.MethodGet, fixture.j3bURL+"/model-checks/run/active", nil)
	if err != nil {
		t.Fatalf("build authenticated j3b request: %v", err)
	}
	status, _ := client.doRequest(request, wantStatus(http.StatusOK))
	if status != http.StatusOK {
		t.Fatalf("authenticated j3b run/active status=%d", status)
	}
}
