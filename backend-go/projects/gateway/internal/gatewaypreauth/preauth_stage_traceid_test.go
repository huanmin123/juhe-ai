package gatewaypreauth

// runtime_resolution 阶段 traceId 的 kernel-first 取值回归：defer 曾恒取
// Observability.TraceID()（进程级单例语义下为空串），现改走既有 helper
// observedTraceID（kernel.Context(req.HTTP).TraceID -> Observability.TraceID()
// -> CreateTraceID()），req 为 nil 时置空串。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// stageLogsByName copies the recorded stages of one name under the lock.
func stageLogsByName(o *fakeObservability, name string) []loggedStage {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := []loggedStage{}
	for _, stage := range o.stages {
		if stage.stage == name {
			out = append(out, stage)
		}
	}
	return out
}

// stageTraceRequest 让 req.HTTP 经过 kernel.RequestContextMiddleware，携带
// X-Trace-Id 派生的固定 traceId（/v1 链入口同款绑定方式）。
func stageTraceRequest(t *testing.T, method, target, traceID string) *GatewayRequest {
	t.Helper()
	var captured *GatewayRequest
	handler := kernel.RequestContextMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = &GatewayRequest{HTTP: r, RemoteAddr: "203.0.113.9:44556", ClientIP: "203.0.113.9"}
	}))
	request := httptest.NewRequest(method, target, nil)
	request.Header.Set("X-Trace-Id", traceID)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if captured == nil {
		t.Fatal("kernel 中间件未产出请求")
	}
	return captured
}

func TestPreResolveGatewayRuntimeStageTraceID(t *testing.T) {
	t.Run("kernel 上下文优先", func(t *testing.T) {
		service, obs, _ := newTestService(t, nil)
		req := stageTraceRequest(t, "GET", "/v1/models", "trace_preauth_kernel")
		writer := NewTrackingWriter(httptest.NewRecorder())
		called := false
		if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() { called = true }); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !called {
			t.Fatal("models 快路应调用 next")
		}
		stages := stageLogsByName(obs, "runtime_resolution")
		if len(stages) != 1 {
			t.Fatalf("runtime_resolution stage 数 = %d: %+v", len(stages), obs.stages)
		}
		if got := stages[0].fields["traceId"]; got != "trace_preauth_kernel" {
			t.Fatalf("traceId = %v, want kernel 上下文的固定 traceId", got)
		}
	})
	t.Run("无 kernel 上下文回落非空", func(t *testing.T) {
		service, obs, _ := newTestService(t, nil)
		req, _, writer := newTestRequest("GET", "/v1/models")
		if err := service.PreResolveGatewayRuntime(context.Background(), writer, req, func() {}); err != nil {
			t.Fatalf("err = %v", err)
		}
		stages := stageLogsByName(obs, "runtime_resolution")
		if len(stages) != 1 {
			t.Fatalf("runtime_resolution stage 数 = %d: %+v", len(stages), obs.stages)
		}
		if got, _ := stages[0].fields["traceId"].(string); got == "" {
			t.Fatal("无 kernel 上下文时 traceId 应回落生成，不得为空串")
		}
	})
}
