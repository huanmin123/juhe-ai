package gatewayusage

import (
	"strings"
	"testing"
)

// TestSessionAffinityMetrics 锁定账户亲和同层 latch 的指标面（设计 §8）：
// 重绑成功/失败按有界 reason 分档，跨账户切换、lease 命中/过期与 owner 等待
// 直方图可渲染；未知 reason 收敛为 other，避免标签基数失控。
func TestSessionAffinityMetrics(t *testing.T) {
	metrics := newPrometheusMetrics()
	metrics.recordSessionAffinityRebind("capacity", true)
	metrics.recordSessionAffinityRebind("capacity", false)
	metrics.recordSessionAffinityRebind("slot_fallback", true)
	metrics.recordSessionAffinityRebind("bogus", true)
	metrics.recordSessionAffinityCrossAccountSwitch()
	metrics.recordSessionAffinityBackSwitchPrevented()
	metrics.recordSessionAffinityLease(true)
	metrics.recordSessionAffinityLease(false)
	metrics.recordSessionAffinityAffinityWait(120)
	metrics.recordSessionAffinityAffinityWait(0) // zero wait is not observed
	metrics.recordSessionAffinitySlotWait(200)
	metrics.recordSessionAffinitySlotWait(0)
	metrics.recordSessionAffinitySlotFallback()
	metrics.recordSessionAffinitySlotEviction()
	metrics.recordSessionAffinitySlotFailure()

	render := metrics.gatherText()
	for _, want := range []string{
		"juhe_ai_gateway_session_affinity_rebind_total{",
		`reason="capacity"`,
		"juhe_ai_gateway_session_affinity_rebind_failed_total{",
		`reason="other"`,
		"juhe_ai_gateway_session_affinity_cross_account_switch_total{",
		"juhe_ai_gateway_session_affinity_back_switch_prevented_total{",
		"juhe_ai_gateway_session_affinity_lease_hit_total{",
		"juhe_ai_gateway_session_affinity_lease_expired_total{",
		"juhe_ai_gateway_session_affinity_affinity_wait_ms_bucket{",
		"juhe_ai_gateway_session_affinity_affinity_wait_ms_count{",
		"juhe_ai_gateway_session_affinity_slot_wait_ms_bucket{",
		"juhe_ai_gateway_session_affinity_slot_wait_ms_count{",
		"juhe_ai_gateway_session_affinity_slot_fallback_total{",
		"juhe_ai_gateway_session_affinity_slot_evictions_total{",
		"juhe_ai_gateway_session_affinity_slot_failed_total{",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("session affinity metric missing %q in:\n%s", want, render)
		}
	}
	if strings.Contains(render, `reason="bogus"`) {
		t.Fatalf("unbounded reason leaked into metric labels:\n%s", render)
	}
}
