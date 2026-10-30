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
		wantOK     bool
		wantInMsg  []string
	}{
		{"fresh 未升级", 0, 0, false, []string{"尚未升级", "dry-run"}},
		{"正常完成", binary, binary, true, nil},
		{"升级中断在途", binary - 4, binary, false, []string{"上次升级未完成", "重跑"}},
		{"中断后换旧二进制", binary - 4, binary, false, []string{"上次升级未完成"}},
		{"完成态库领先", binary + 10, binary + 10, false, []string{"升级二进制"}},
		{"手工 DROP 账本", binary, 0, false, []string{"账本缺失"}},
		{"手工 TRUNCATE 账本（表在行空）", binary, 0, false, []string{"账本缺失"}},
		{"空号中断（库有收敛史、账本为空）", binary - 4, 0, false, []string{"结构未收敛完成", "续跑"}},
	}
	for _, tc := range cases {
		decision := Evaluate(tc.dbRevision, tc.stepMax)
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
	decision := Evaluate(int64(contracts.SchemaRevision)-4, int64(contracts.SchemaRevision))
	if decision.OK || !strings.Contains(decision.Message, "重跑") {
		t.Fatalf("中断在途应引导重跑续跑: %+v", decision)
	}
}

// 顺序 3 先于顺序 4：fresh（dbRevision=0, stepMax=0）必须落「尚未升级」引导
// dry-run，而不是空号续跑文案——空号分支以「库有收敛史（dbRevision>0）」为前提。
func TestEvaluateFreshKeepsDryRunGuidance(t *testing.T) {
	decision := Evaluate(0, 0)
	if decision.OK || !strings.Contains(decision.Message, "dry-run") {
		t.Fatalf("fresh 未升级应引导 dry-run 预演: %+v", decision)
	}
}
