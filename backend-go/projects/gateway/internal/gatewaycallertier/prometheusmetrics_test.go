package gatewaycallertier

// Prometheus 指标族注册测试（M1 量化观测）：三个指标族按既有
// prometheusmetrics 模式注册（隔离实例），回调计数与层级样本落 exposition。
import (
	"strings"
	"testing"
)

func TestCallertierPrometheusFamiliesRecordCallbacks(t *testing.T) {
	metrics := newCallertierPrometheusMetrics()
	metrics.record("request_total", "ok")
	metrics.record("signal_total", string(SignalInvalidJSON))
	metrics.record("signal_total", string(SignalModelFilterFailed))
	metrics.record("error", "store_mutate")
	metrics.transition(2, 3)
	metrics.tier("key_1", 3)

	text := metrics.gatherText()
	for _, want := range []string{
		`juhe_ai_callertier_record_total{kind="ok",name="request_total",service="juhe-ai"}`,
		`juhe_ai_callertier_record_total{kind="invalid_json",name="signal_total",service="juhe-ai"}`,
		`juhe_ai_callertier_record_total{kind="model_filter_failed",name="signal_total",service="juhe-ai"}`,
		`juhe_ai_callertier_record_total{kind="store_mutate",name="error",service="juhe-ai"}`,
		`juhe_ai_callertier_transition_total{from="2",service="juhe-ai",to="3"}`,
		`juhe_ai_callertier_tier{api_key="key_1",service="juhe-ai"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("exposition 缺少 %s:\n%s", want, text)
		}
	}
}

// TestPrometheusMetricsSingletonAdapter：进程单例适配恒给出非 nil 回调，调用
// 不 panic（注册即生效，功能不设开关）。
func TestPrometheusMetricsSingletonAdapter(t *testing.T) {
	adapter := PrometheusMetrics()
	if adapter.RecordTotal == nil || adapter.TierGauge == nil || adapter.TransitionTotal == nil {
		t.Fatal("PrometheusMetrics 回调必须全部就绪")
	}
	adapter.RecordTotal("request_total", "ok")
	adapter.TierGauge("key_probe", 2)
	adapter.TransitionTotal(2, 2)
}
