package jobregistry

import (
	"slices"
	"testing"
)

// TestCircuitLedgerProjectionWrites 锁定账户电路两任务的业务库 ledger 写面
// 登记：恢复扫描投影与孤儿结清/Cleanup 从 2026-10-08 起写 incidents/outbox，
// 登记缺失会让审计口径低估写范围。
func TestCircuitLedgerProjectionWrites(t *testing.T) {
	want := map[string][]string{
		"account-circuit-control-plane-maintenance": {
			"business:account_circuit_incidents", "business:account_circuit_outbox", "runtime:account_circuit",
		},
		"account-circuit-recovery": {
			"business:account_circuit_incidents", "business:account_circuit_outbox", "runtime:account_circuit",
		},
	}
	entries := map[string][]string{}
	for _, entry := range ScheduledEntries() {
		entries[entry.JobName] = entry.Writes
	}
	for jobName, writes := range want {
		got, ok := entries[jobName]
		if !ok {
			t.Fatalf("%s 未登记", jobName)
		}
		for _, write := range writes {
			if !slices.Contains(got, write) {
				t.Fatalf("%s Writes 缺 %s: %v", jobName, write, got)
			}
		}
	}
}
