// 波次 w14j：控制面剩余错误臂与方言无关分支收口。复用 w13g5 注入
// fixture（SQLite + 可回放失败注入），不触真实 PostgreSQL；PG 专属语句
// 差异由 w14j_circuit_pg_script_test.go 的脚本化方言 fixture 覆盖。
package circuitstore

import (
	"testing"

)

func TestW14JMapOutboxEventScopeToken(t *testing.T) {
	event := mapOutboxEvent(outboxClaimRow{
		eventID: "e", projectionKey: "p", eventType: "incident_changed",
		accountID: "a", accountRuntimeKey: "rk", transitionID: "tr", dispatchRevision: 3,
	})
	if event.CircuitScopeKey != "" || event.ClaimToken != "" {
		t.Fatalf("空 scope/token 不得映射: %+v", event)
	}
}
