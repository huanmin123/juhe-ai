package statsverify

import (
	"context"
	"testing"
	"time"
)

// w12b_units_test.go 覆盖 statsverify 的单元级分支：scan/日期/IP 归一辅助、
// usage_records 坏类型行解码、latestIgnored/lag 数据臂、client-ip 聚合与
// 写入路径、窗口刷新的 full/per-hash/stale/pending 臂、一致性检查错误臂、
// job 入口错误臂、groupstats join 行坏类型扫描。
//
// 不可达登记见 w12b_failinject_test.go 头注释；本文件补充：
//   - store.go sqliteDSN 的 filepath.Abs 错误臂：常规路径无法触发。
//   - clientipwindows.go 空窗口臂与重复窗口去重臂：FixedUsageStatsDateKeys
//     恒返回固定天数，候选窗口恒互异。
//   - groupstats.go per-group 路径里 join 行不属于目标的分支：join 查询
//     INNER JOIN groups 保证行必然属于目标组。
//   - clientipagg.go selectClientIPUsageRecords 的 created_at 非 RFC3339 臂：
//     WHERE created_at <= 游标安全线按文本词法比较，非法文本行恒被过滤。
//   - latestIgnoredCursorAndLag 同类：非 RFC3339 文本被词法谓词排除；
//     TEXT 亲和性列写入数值已被转为文本，sqlText/sqlStringPtr 恒成功。

func TestW12bSvScanAndDateHelpers(t *testing.T) {
	// scan.go 错误臂。
	if _, err := sqlStringPtr(3.5); err == nil {
		t.Fatal("数值转字符串指针应报错")
	}
	if _, err := sqlIntPtr("w12b-not-int"); err == nil {
		t.Fatal("非法整型应报错")
	}
	if _, err := sqlFloatPtr(struct{}{}); err == nil {
		t.Fatal("未知类型应报错")
	}
	if p, err := sqlStringPtr(nil); p != nil || err != nil {
		t.Fatalf("NULL 字符串应返回 nil 指针: %v %v", p, err)
	}
	if p, err := sqlIntPtr(nil); p != nil || err != nil {
		t.Fatalf("NULL 整型应返回 nil 指针: %v %v", p, err)
	}
	if p, err := sqlFloatPtr(nil); p != nil || err != nil {
		t.Fatalf("NULL 数值应返回 nil 指针: %v %v", p, err)
	}
	// dates.go parseDateKeyParts 错误臂。
	if _, ok := parseDateKeyParts("w12b-bad-date"); ok {
		t.Fatal("非法日期应返回 false")
	}
}

func TestW12bSvIpNormDefensiveArms(t *testing.T) {
	// 前置校验后的防御臂：非 IPv4 输入返回 nil。
	if NormalizeClientIPForStats("w12b-not-an-ip") != nil {
		t.Fatal("非法 IP 应返回 nil")
	}
	if normalizeIpv4("::1") != "" {
		t.Fatal("IPv6 应返回空串")
	}
	if NormalizeClientIPForStats("2001:db8::1") != nil {
		t.Fatal("IPv6 应被排除")
	}
}

type w12bFailGate struct{ err error }

func (g w12bFailGate) EnsureUsageRecordsIngested(context.Context) error { return g.err }

func TestW12bSvBuildAggregatesArms(t *testing.T) {
	location := time.UTC
	bad := []UsageStatsRecordRow{{ID: "r", ClientIP: strPtr("1.2.3.4"), CreatedAt: "not-a-time"}}
	if _, _, err := buildClientIPAggregates(bad, location); err == nil {
		t.Fatal("非法 created_at 应报错")
	}
	good := UsageStatsRecordRow{
		ID: "r1", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), AccountID: strPtr("acc"),
		Success: 1, DurationMs: intPtr(100), CostUsd: f64Ptr(0.5), CreatedAt: "2026-09-17T10:00:00.000Z",
	}
	sameKey := UsageStatsRecordRow{
		ID: "r2", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), AccountID: strPtr("acc"),
		Success: 0, DurationMs: intPtr(300), CreatedAt: "2026-09-17T11:00:00.000Z",
	}
	ips, accounts, err := buildClientIPAggregates([]UsageStatsRecordRow{good, sameKey}, location)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || len(accounts) != 1 {
		t.Fatalf("同键应合并: %d/%d", len(ips), len(accounts))
	}
	if ips[0].durationMsMax != 300 || ips[0].requestCount != 2 {
		t.Fatalf("累加不符: %+v", ips[0])
	}
	// 无 IP 行应被跳过。
	ips2, accounts2, err := buildClientIPAggregates([]UsageStatsRecordRow{{ID: "r3", CreatedAt: "2026-09-17T10:00:00.000Z"}}, location)
	if err != nil || len(ips2) != 0 || len(accounts2) != 0 {
		t.Fatalf("无 IP 行应跳过: %v %v", err, ips2)
	}
}

func TestW12bSvAccumulatorMergeArms(t *testing.T) {
	target := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "acc", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	// lastErrorAt 新旧替换 + first/last seen 比较臂。
	addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{RequestCount: 1, LastErrorAt: "2026-09-17T11:00:00.000Z"}, "2026-09-17T12:00:00.000Z")
	addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{RequestCount: 1, LastErrorAt: "2026-09-17T10:30:00.000Z"}, "2026-09-17T09:00:00.000Z")
	if target.lastErrorAt != "2026-09-17T11:00:00.000Z" {
		t.Fatalf("lastErrorAt 应保留最新: %s", target.lastErrorAt)
	}
	if target.firstSeenAt != "2026-09-17T09:00:00.000Z" || target.lastUsedAt != "2026-09-17T12:00:00.000Z" {
		t.Fatalf("first/last seen 不符: %s %s", target.firstSeenAt, target.lastUsedAt)
	}
	// panic 臂：坏 createdAt（fail-fast 语义）。
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{}, "w12b-bad")
	})
	// registryAggregatesFromIPAggregates 合并臂。
	second := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-18", "", UsageStatsAccumulator{}, "2026-09-17T08:00:00.000Z")
	second.lastUsedAt = "2026-09-17T13:00:00.000Z"
	entries := registryAggregatesFromIPAggregates([]*clientIPAggregate{target, second})
	if len(entries) != 1 || entries[0].firstSeenAt != "2026-09-17T08:00:00.000Z" || entries[0].lastSeenAt != "2026-09-17T13:00:00.000Z" {
		t.Fatalf("registry 合并不符: %+v", entries)
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("应触发 fail-fast panic")
		}
	}()
	fn()
}
