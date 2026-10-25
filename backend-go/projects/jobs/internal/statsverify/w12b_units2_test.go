package statsverify

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// w12b_units2_test.go 补充 statsverify 剩余分支：join 行扫描的类型转换
// 错误臂（无类型 scratch 表绕过列亲和性）、按组脏协议的读取/删除注入、
// 缺失账户跳过臂、job 入口 nil Clock 与窗口刷新注入、LoadUsageStatsTimezone
// 空值臂、nil store 防御与 OpenStore 建表失败。
// 不可达登记见 w12b_failinject_test.go 头注释。

func TestW12bSvAccumulatorPanicArms(t *testing.T) {
	// firstSeenAt/lastUsedAt 损坏的 fail-fast 臂（调用方预归一化，直调触达）。
	target := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "w12b-bad-first")
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	})
	target2 := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	target2.lastUsedAt = "w12b-bad-last"
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target2, UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	})
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target2, UsageStatsAccumulator{LastErrorAt: "w12b-bad-error"}, "2026-09-17T10:00:00.000Z")
	})
}

func sqlOpenHelper(driverName, path string) (*sql.DB, error) {
	return sql.Open(driverName, "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
}

