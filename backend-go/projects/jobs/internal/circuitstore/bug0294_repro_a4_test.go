// BUG-0294 / A4 红绿回归（由最小复现定性载体转正）。
//
// 档案：docs/bug/问题-0294-迁移期行为存疑按现状断言未建档清点.md A4。
// 原注释：rows_hydrate_helpers_test.go:57（关联 final_edges_test.go:169）。
// 现象：buildBasePayload 把 sql.NullInt64 形状的 configRevision 直接传入
// numberValue，switch 不识别结构体类型，行值静默回退默认 1
// （0291 同类：null 静默回退）。
//
// 定性结论：确认缺陷，已修复（入参前解包 NullInt64）。
// 修复形态：新增 nullInt64Number（listavailability_sources.go）——Valid=true
// 透传真实行值，NULL 才落默认 1；两处同型调用点一并替换：
//   - listavailability_rows.go buildBasePayload 的 "configRevision"；
//   - listavailability_hydrate.go balanceSnapshotMatchesConfiguration 的
//     configRevision 入参。
//
// 同族排查：numberValue 其余调用点入参均为 any 动态类型（driver 直返列值，
// switch 本为其设计）或原生数值，无同型；managementRow 的
// lastHealthCheckStatusCode / cooldownRetestLastStatusCode 两个 NullInt64
// 不进 numberValue。
// 影响面（修复前）：hydrate 载荷 configRevision 恒 1，gateway 侧
// gatewaycircuit/probe.go 以 revision 组探测运行态 key（availability:<scope>:<kind>:r%d）
// 的修订维度失真：不同修订共享同一 key，修订驱动的缓存隔离/失效无法推进。
package circuitstore

import (
	"database/sql"
	"testing"
)

// TestBug0294A4ConfigRevisionNullInt64PassesThrough 断言 Valid 行值透传、
// NULL 落默认 1、原生类型对照不变。
func TestBug0294A4ConfigRevisionNullInt64PassesThrough(t *testing.T) {
	// 修复主体：Valid=true 的行值 4 必须原样透传到 base payload。
	row := managementRow{
		id:             "acc-bug0294-a4",
		name:           "A4 回归账户",
		providerCode:   "openai",
		accountType:    "api_key",
		configRevision: sql.NullInt64{Int64: 4, Valid: true},
	}
	payload := buildBasePayload(row, nil, nil)
	if got := payload["configRevision"]; got != float64(4) {
		t.Fatalf("Valid configRevision=4 必须透传，实际 %v", got)
	}

	// helper 语义：更大行值同样透传（修订推进维度不被钳制）。
	if got := nullInt64Number(sql.NullInt64{Int64: 1073741827, Valid: true}, 1); got != 1073741827 {
		t.Fatalf("nullInt64Number 大行值必须透传，实际 %v", got)
	}

	// 对照一：NULL 行回退默认 1（fallback=1 的本意，行为保持）。
	nullRow := row
	nullRow.configRevision = sql.NullInt64{}
	if got := buildBasePayload(nullRow, nil, nil)["configRevision"]; got != float64(1) {
		t.Fatalf("NULL configRevision 应回退默认 1，实际 %v", got)
	}
	if got := nullInt64Number(sql.NullInt64{}, 1); got != 1 {
		t.Fatalf("nullInt64Number(NULL) 应回退默认 1，实际 %v", got)
	}

	// 对照二：原生 int64 经 numberValue 数值化不变（偏差点在类型形状，
	// numberValue 对原生类型的既有容错矩阵不受本修复影响）。
	if got := numberValue(int64(4), 1); got != 4 {
		t.Fatalf("对照：原生 int64(4) 应回退外数值化为 4，实际 %v", got)
	}
}
