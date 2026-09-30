package gatewaycodex

// BUG-0241 留档收口：compactpreflight 成功臂质量终态结算接线锁。

import (
	"os"
	"strings"
	"testing"
)

// TestBug0241CompactSuccessTerminalWired：compactpreflight 在 exchange 读回
// 成功后必须按 truncated / UpstreamOK 分派 completed_response /
// incomplete_response / upstream_response_failure 并调用
// recordExchangeTerminal（对齐 Node compact-preflight.ts:125-134：读回完成
// 后立即结算本交换的质量终态）；接线被移除时本 needle 失败。
func TestBug0241CompactSuccessTerminalWired(t *testing.T) {
	raw, err := os.ReadFile("compactpreflight.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	source := string(raw)
	for _, needle := range []string{
		"s.recordExchangeTerminal(exchange, input, successTerminal)",
		`successTerminal.OutcomeClass = "incomplete_response"`,
		`successTerminal.OutcomeClass = "upstream_response_failure"`,
	} {
		if !strings.Contains(source, needle) {
			t.Fatalf("compactpreflight.go 成功臂结算缺失接线：%s", needle)
		}
	}
}
