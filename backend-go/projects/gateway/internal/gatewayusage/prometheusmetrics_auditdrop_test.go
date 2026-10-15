package gatewayusage

import (
	"strings"
	"testing"
)

// 2026-09-18 加固：F3/F4 producer 的队列饱和丢弃计数接入 Prometheus 暴露面。
// 组合根在启动期注入访问器；nil（未注入）时指标族不输出。
// client_golang 注册不可撤销、计数不可复位，因此测试绑定独立 Registry 的
// 实例构造（取代旧 RenderPrometheusMetrics 全局渲染与复位模式）。
func TestAuditAndOperationDroppedTotalScrapeTimeCounterFunc(t *testing.T) {
	metrics := newPrometheusMetrics()

	// 未注入：不输出对应指标族。
	render := metrics.gatherText()
	if strings.Contains(render, "juhe_ai_audit_log_captured_dropped_total") {
		t.Fatal("未注入访问器时不得输出审计丢弃指标")
	}
	if strings.Contains(render, "juhe_ai_operation_log_dropped_total") {
		t.Fatal("未注入访问器时不得输出操作日志丢弃指标")
	}

	metrics.setAuditCapturedDroppedTotal(func() int64 { return 7 })
	metrics.setOperationLogDroppedTotal(func() int64 { return 3 })
	render = metrics.gatherText()
	for _, want := range []string{
		"# TYPE juhe_ai_audit_log_captured_dropped_total counter",
		`juhe_ai_audit_log_captured_dropped_total{service="juhe-ai"} 7`,
		"# TYPE juhe_ai_operation_log_dropped_total counter",
		`juhe_ai_operation_log_dropped_total{service="juhe-ai"} 3`,
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("render missing %q:\n%s", want, render)
		}
	}

	// 后续注入只换访问器，不重复注册（client_golang 重复注册会 panic）。
	metrics.setAuditCapturedDroppedTotal(func() int64 { return 9 })
	render = metrics.gatherText()
	if !strings.Contains(render, `juhe_ai_audit_log_captured_dropped_total{service="juhe-ai"} 9`) {
		t.Fatalf("换访问器后未读到新值:\n%s", render)
	}
}
