package gatewaycallertier

import (
	"strconv"

	"github.com/huanminabc/juhe-ai/backend-go-platform/prommetrics"
)

// Prometheus 指标族（M1 量化观测，调用方分级调度设计 §8）：注册模式对齐
// internal/gatewayusage/prometheusmetrics.go（独立 registry + 进程单例 +
// 组合根经导出入口取回调适配）。固定 service 标签与既有网关指标族一致。
//
// 名字与标签集（设计 §8 观测面板）：
//   - juhe_ai_callertier_record_total{name,kind}：name=request_total|
//     signal_total|error，kind=ok|信号种类|错误类别；
//   - juhe_ai_callertier_transition_total{from,to}：影子层级转换计数；
//   - juhe_ai_callertier_tier{api_key}：按 Key 的当前影子层级（量级=活跃
//     Key 数，当前规模个位数可接受）。
const callertierPrometheusServiceName = "juhe-ai"

// callertierPrometheusServiceLabels 固定 service 标签（与 gatewayusage 指标
// 族同约定）。
var callertierPrometheusServiceLabels = map[string]string{"service": callertierPrometheusServiceName}

// callertierPrometheusMetrics 持有本族 registry 与已注册指标。
type callertierPrometheusMetrics struct {
	registry        *prommetrics.Registry
	recordTotal     prommetrics.CounterVec
	transitionTotal prommetrics.CounterVec
	tierGauge       prommetrics.GaugeVec
}

func newCallertierPrometheusMetrics() *callertierPrometheusMetrics {
	registry := prommetrics.NewRegistry()
	return &callertierPrometheusMetrics{
		registry: registry,
		recordTotal: prommetrics.NewCounterVec(registry,
			"juhe_ai_callertier_record_total",
			"Caller tier shadow records grouped by record name and signal or error kind.",
			callertierPrometheusServiceLabels, []string{"name", "kind"}),
		transitionTotal: prommetrics.NewCounterVec(registry,
			"juhe_ai_callertier_transition_total",
			"Caller tier shadow tier transitions grouped by from and to tier.",
			callertierPrometheusServiceLabels, []string{"from", "to"}),
		tierGauge: prommetrics.NewGaugeVec(registry,
			"juhe_ai_callertier_tier",
			"Current shadow tier per API key (1 premium .. 4 restricted).",
			callertierPrometheusServiceLabels, []string{"api_key"}),
	}
}

// callertierPrometheusDefault 是进程单例；组合根在链装配时经
// PrometheusMetrics 取回调适配，指标随 /__aisys__/metrics 暴露。
var callertierPrometheusDefault = newCallertierPrometheusMetrics()

// PrometheusMetrics 返回把 Recorder 指标回调接到进程注册表的适配。零值回调
// 语义保持 Metrics 的 no-op 约定——本适配恒非 nil，注册即生效（功能不设
// 开关）。可重复调用；每次返回同一单例上的新适配值。
func PrometheusMetrics() Metrics {
	return Metrics{
		RecordTotal:     callertierPrometheusDefault.record,
		TierGauge:       callertierPrometheusDefault.tier,
		TransitionTotal: callertierPrometheusDefault.transition,
	}
}

func (m *callertierPrometheusMetrics) record(name, kind string) {
	m.recordTotal.WithLabelValues(name, kind).Inc()
}

func (m *callertierPrometheusMetrics) tier(apiKeyID string, tier int) {
	m.tierGauge.WithLabelValues(apiKeyID).Set(float64(tier))
}

func (m *callertierPrometheusMetrics) transition(from, to int) {
	m.transitionTotal.WithLabelValues(strconv.Itoa(from), strconv.Itoa(to)).Inc()
}

// gatherText 渲染实例 registry 的 classic exposition（测试做内容级断言；
// gather 错误带原因浮出）。
func (m *callertierPrometheusMetrics) gatherText() string {
	text, err := m.registry.GatherText()
	if err != nil {
		return "<prometheus gather error: " + err.Error() + ">"
	}
	return text
}
