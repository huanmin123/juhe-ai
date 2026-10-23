// BUG-0294 / A13 红绿回归（由最小复现定性载体转正）。
//
// 档案：docs/bug/问题-0294-迁移期行为存疑按现状断言未建档清点.md A13。
// 原注释：w3_runner_test.go:346（「预算耗尽后 truncateUTF8(input, 0) 返回完整
// 原值，后续增量仍被全量追加，文本总量可越过 192 KiB 上限」）。
//
// 定性结论：确认缺陷，已修复（turns.go truncateUTF8 语义回正）。
// 修复语义（helper 终态）：truncateUTF8 对 maxBytes<=0 返回空串——「预算 0」
// 表示装不下任何内容而非「不裁剪」，与 generation_websearch.go 的
// truncateUTF8Bytes 同族语义一致；预算耗尽时 generation_runner.go 的
// appendTextLocked/appendReasoningLocked 以 delta=="" 早退，增量不再放行。
// 全包调用点核查：generation_runner.go:1427/:1428/:1455 为恒正常数 512，不受
// 影响；不存在「合法传 0 期望全量」的调用方。
// 影响面（修复前）：长回复流式场景文本块无上界增长，192KiB 文本预算可被
// 无限制越过（内存与下游 payload 无界）。
package chat

import (
	"context"
	"strings"
	"testing"
)

// TestBug0294A13TruncateUTF8ZeroBudgetYieldsEmpty 断言 0/负预算返回空串，
// 并保留正常裁剪行为对照。
func TestBug0294A13TruncateUTF8ZeroBudgetYieldsEmpty(t *testing.T) {
	// 预算 0：装不下任何内容 → 空串。
	if got := truncateUTF8("abc中文", 0); got != "" {
		t.Fatalf("0 预算必须返回空串，实际 %q", got)
	}

	// 192KiB 量级超预算输入在 0 上限下同样返回空串（不再全量放行）。
	over := strings.Repeat("a", chatGenerationTextMaxBytes+16)
	if got := truncateUTF8(over, 0); got != "" {
		t.Fatalf("0 上限下超预算输入必须返回空串，实际长度 %d", len(got))
	}

	// 对照：正常裁剪行为不变——整段放行、按字节截断且不撕裂 rune。
	if got := truncateUTF8("abc中文", 9); got != "abc中文" {
		t.Fatalf("预算内输入应原样返回，实际 %q", got)
	}
	if got := truncateUTF8("a中文b", 4); got != "a中" {
		t.Fatalf("超预算截断不撕裂 rune，实际 %q", got)
	}
}

// TestBug0294A13ExhaustedBudgetRejectsFurtherDelta 断言预算耗尽后
// （remaining=0）增量不再被追加：总量钳在 192 KiB 上限、不新增块。
func TestBug0294A13ExhaustedBudgetRejectsFurtherDelta(t *testing.T) {
	// 预算耗尽时 remainingTextBytes 产出 0（缺陷触发条件保持覆盖）。
	blocks := []*assistantBlock{{Type: "output_text", Text: strings.Repeat("a", chatGenerationTextMaxBytes)}}
	if got := remainingTextBytes(blocks, "output_text", chatGenerationTextMaxBytes); got != 0 {
		t.Fatalf("预算耗尽后 remaining 应为 0，实际 %d", got)
	}

	// 机制回归：走真实 runner 追加链。先灌满预算，再追加密集增量，
	// 总量必须保持 chatGenerationTextMaxBytes，不再越过上限。
	runner := NewChatGenerationRunner(ChatGenerationRunnerOptions{
		Identity: ChatGenerationIdentity{OwnerID: "o", ConversationID: "c", TurnID: "t", AssistantMessageID: "m"},
		Now:      func() string { return "2026-03-10T08:00:00.000Z" },
	}, context.Background(), func() {}, func() bool { return false })
	runner.currentState = "running"

	fill := strings.Repeat("a", 200*1024)
	runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &fill})
	extra := "extra-after-exhausted"
	runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &extra})
	runner.Publish("message.delta", nil, ChatGenerationProjectionUpdate{ContentTextDelta: &extra})

	snapshot := runner.timeline.Snapshot()
	total := 0
	textBlocks := 0
	for _, block := range snapshot.ContentBlocks {
		if block.Type == "output_text" {
			total += len(block.Text)
			textBlocks++
		}
	}
	if total != chatGenerationTextMaxBytes {
		t.Fatalf("预算耗尽后增量必须被拒绝：总量 %d，期望 %d", total, chatGenerationTextMaxBytes)
	}
	if textBlocks != 1 {
		t.Fatalf("预算耗尽后不得新增文本块，实际 %d 块", textBlocks)
	}
}
