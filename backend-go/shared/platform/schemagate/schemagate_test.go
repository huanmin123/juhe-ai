package schemagate

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 五行求值顺序表场景代数验证（设计 §11.1）。
func TestEvaluateDecisionTable(t *testing.T) {
	binary := int64(contracts.SchemaRevision)
	cases := []struct {
		name       string
		dbRevision int64
		stepMax    int64
		ledger     bool
		wantOK     bool
		wantInMsg  []string
	}{
		{"fresh 未升级", 0, 0, false, false, []string{"尚未升级", "dry-run"}},
		{"正常完成", binary, binary, true, true, nil},
		{"升级中断在途", binary - 4, binary, true, false, []string{"上次升级未完成", "重跑"}},
		{"中断后换旧二进制", binary - 4, binary, true, false, []string{"上次升级未完成"}},
		{"完成态库领先", binary + 10, binary + 10, true, false, []string{"升级二进制"}},
		{"手工 DROP 账本", binary, 0, false, false, []string{"账本缺失"}},
	}
	for _, tc := range cases {
		decision := Evaluate(tc.dbRevision, tc.stepMax, tc.ledger)
		if decision.OK != tc.wantOK {
			t.Fatalf("%s: OK = %v, want %v（message=%q）", tc.name, decision.OK, tc.wantOK, decision.Message)
		}
		for _, fragment := range tc.wantInMsg {
			if !strings.Contains(decision.Message, fragment) {
				t.Fatalf("%s: 文案缺 %q（message=%q）", tc.name, fragment, decision.Message)
			}
		}
	}
}

// 顺序 1 必须先于顺序 2：中断在途（stepMax>dbRevision 且 dbRevision<binary）
// 的引导是「重跑 upgrade 续跑」而非「升级二进制」（F5 裁决口径）。
func TestEvaluateInFlightPrecedesStaleBinary(t *testing.T) {
	decision := Evaluate(int64(contracts.SchemaRevision) - 4, int64(contracts.SchemaRevision), true)
	if decision.OK || !strings.Contains(decision.Message, "重跑") {
		t.Fatalf("中断在途应引导重跑续跑: %+v", decision)
	}
}
