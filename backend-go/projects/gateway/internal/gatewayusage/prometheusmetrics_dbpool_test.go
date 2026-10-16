package gatewayusage

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// 2026-10-16 加固：主业务 PG 池 DBStats 接入 Prometheus 暴露面（六序列无标
// 签，四 gauge + 两累计 counter）。组合根在启动期注入 provider；SQLite 形态
// 不注入，整族缺席。client_golang 注册不可撤销，测试绑定独立 Registry 的实
// 例构造（与 F3/F4 丢弃计数同模式）。
func TestDBPoolStatsScrapeTimeSeries(t *testing.T) {
	metrics := newPrometheusMetrics()

	// 未注入：整族缺席。
	render := metrics.gatherText()
	if strings.Contains(render, "juhe_ai_gateway_db_pool_") {
		t.Fatal("未注入 provider 时不得输出连接池指标族")
	}

	metrics.setDBPoolStats(func() sql.DBStats {
		return sql.DBStats{
			OpenConnections:    3,
			InUse:              1,
			Idle:               2,
			MaxOpenConnections: 10,
			WaitCount:          7,
			WaitDuration:       1500 * time.Millisecond,
		}
	})
	render = metrics.gatherText()
	for _, want := range []string{
		"# TYPE juhe_ai_gateway_db_pool_open_connections gauge",
		"juhe_ai_gateway_db_pool_open_connections 3",
		"# TYPE juhe_ai_gateway_db_pool_in_use_connections gauge",
		"juhe_ai_gateway_db_pool_in_use_connections 1",
		"# TYPE juhe_ai_gateway_db_pool_idle_connections gauge",
		"juhe_ai_gateway_db_pool_idle_connections 2",
		"# TYPE juhe_ai_gateway_db_pool_max_open_connections gauge",
		"juhe_ai_gateway_db_pool_max_open_connections 10",
		"# TYPE juhe_ai_gateway_db_pool_wait_count_total counter",
		"juhe_ai_gateway_db_pool_wait_count_total 7",
		"# TYPE juhe_ai_gateway_db_pool_wait_duration_seconds_total counter",
		"juhe_ai_gateway_db_pool_wait_duration_seconds_total 1.5",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("render missing %q:\n%s", want, render)
		}
	}

	// 后续注入只换 provider，不重复注册（client_golang 重复注册会 panic）。
	metrics.setDBPoolStats(func() sql.DBStats {
		return sql.DBStats{OpenConnections: 4, InUse: 2, Idle: 2, MaxOpenConnections: 10, WaitCount: 9, WaitDuration: 3 * time.Second}
	})
	render = metrics.gatherText()
	for _, want := range []string{
		"juhe_ai_gateway_db_pool_open_connections 4",
		"juhe_ai_gateway_db_pool_wait_count_total 9",
		"juhe_ai_gateway_db_pool_wait_duration_seconds_total 3",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("换 provider 后未读到新值 %q:\n%s", want, render)
		}
	}
}
