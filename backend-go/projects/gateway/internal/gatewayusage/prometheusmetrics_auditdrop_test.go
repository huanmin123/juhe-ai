package gatewayusage

import (
	"strings"
	"testing"
)

// 2026-09-18 加固：F3 审计 producer 的队列饱和丢弃计数接入 Prometheus 渲染。
// 组合根在启动期注入访问器；nil（未注入）时指标族不输出。
func TestRenderPrometheusMetricsAuditDroppedTotal(t *testing.T) {
	defer SetAuditCapturedDroppedTotal(nil)

	// 未注入：不输出该指标族。
	if strings.Contains(RenderPrometheusMetrics(), "juhe_ai_audit_log_captured_dropped_total") {
		t.Fatal("未注入访问器时不得输出审计丢弃指标")
	}

	SetAuditCapturedDroppedTotal(func() int64 { return 7 })
	render := RenderPrometheusMetrics()
	if !strings.Contains(render, "# TYPE juhe_ai_audit_log_captured_dropped_total counter") {
		t.Fatalf("缺少 TYPE 行: %s", render)
	}
	if !strings.Contains(render, `juhe_ai_audit_log_captured_dropped_total{service="juhe-ai"} 7`) {
		t.Fatalf("缺少计数行: %s", render)
	}
}
