// 波次 w13g8：accounthealth projection.go 剩余臂批测（复用既有
// newProjectionFixture 业务库 fixture）：
//   - listProjectedOutcomes 的坏 payload 解码、observed_at 无效与列类型臂
//     （store 库直接写入坏行，独立 fixture 逐臂驱动）；
//   - Run 循环的单轮失败 warn 分支（坏 payload + 短超时退出）；
//   - normalizeProjectionCursor 的 outcomeId/observedAt 校验臂（纯函数）。
//
// 不可达语句归因（projection.go，schema 契约行为，不在测试侧强行驱动）：
//   - loadCursor 的 `!observedAtText.Valid || !outcomeID.Valid` 损坏臂
//     （394-400 区）与 advanceCursor 的 default 损坏臂（457-458 区）：
//     account_health_projection_cursors 表的 CHECK 约束
//     （observed_at 与 outcome_id 必须同时为 NULL 或同时非 NULL）在
//     INSERT/UPDATE 时都会强制完整游标行，半缺状态不可构造。
//
// 全部 SQLite 进程内、不依赖 PG；数据 ID 全部 w13g8- 前缀。
package accounthealth

import (
	"context"
	"strings"
	"testing"
	"time"
)

const w13g8OutcomeColumns = `(outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, payload)`

// w13g8InsertRawOutcome 向 store 库写入一行原始 outcome（payload/observed_at
// 可以是任意文本，用于驱动 listProjectedOutcomes 的解码臂）。
func w13g8InsertRawOutcome(t *testing.T, f *projectionFixture, outcomeID string, observedAt string, payload string) {
	t.Helper()
	if _, err := f.store.db.Exec(`INSERT INTO account_health_outcomes `+w13g8OutcomeColumns+`
VALUES (?, 'w13g8-req', 'acct-1', 'probe_success', ?, 1, 1, 1, ?)`, outcomeID, observedAt, payload); err != nil {
		t.Fatal(err)
	}
}

// TestW13G8ProjectionListDecodeArms 驱动 listProjectedOutcomes 的坏 payload
// 解码臂、observed_at 无效臂与 payload 列类型臂（每臂独立 fixture）。
func TestW13G8ProjectionListDecodeArms(t *testing.T) {
	t.Run("坏 payload 解码失败", func(t *testing.T) {
		f := newProjectionFixture(t)
		w13g8InsertRawOutcome(t, f, "w13g8-bad-payload", "2026-09-18T00:00:00Z", "{w13g8-not-json")
		if _, err := f.projector.DrainOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "解码 J1 outcome payload 失败") {
			t.Fatalf("坏 payload 必须报解码错误: %v", err)
		}
	})
	t.Run("observed_at 扫描失败", func(t *testing.T) {
		f := newProjectionFixture(t)
		// observed_at 非 RFC3339 → projectionDBTime 的 rows.Scan 错误路径。
		w13g8InsertRawOutcome(t, f, "w13g8-bad-time", "w13g8-not-a-time", "{}")
		if _, err := f.projector.DrainOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "observed_at") {
			t.Fatalf("无效 observed_at 必须在扫描处报错: %v", err)
		}
	})
	t.Run("payload 整型按文本解码", func(t *testing.T) {
		f := newProjectionFixture(t)
		// INTEGER 字面量落在 TEXT payload 列 → 驱动按文本返回，走解码失败臂
		// 而非列类型臂（documenting 驱动行为）。
		if _, err := f.store.db.Exec(`INSERT INTO account_health_outcomes `+w13g8OutcomeColumns+`
VALUES ('w13g8-bad-type', 'w13g8-req', 'acct-1', 'probe_success', '2026-09-18T00:00:00Z', 1, 1, 1, 123)`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.projector.DrainOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "解码 J1 outcome payload 失败") {
			t.Fatalf("整型 payload 必须按文本解码并报错: %v", err)
		}
	})
}

// TestW13G8ProjectionRunWarnArm 驱动 Run 循环的单轮失败 warn 分支：坏 payload
// 使 DrainOnce 每轮失败，Run 保留游标并以 ctx 取消收口。
func TestW13G8ProjectionRunWarnArm(t *testing.T) {
	f := newProjectionFixture(t)
	w13g8InsertRawOutcome(t, f, "w13g8-bad-payload", "2026-09-18T00:00:00Z", "{w13g8-not-json")
	runCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := f.projector.Run(runCtx); err == nil {
		t.Fatal("Run 必须以 ctx 取消收口")
	}
}

// TestW13G8NormalizeProjectionCursorArms 驱动 normalizeProjectionCursor 的
// outcomeId 与 observedAt 校验臂（纯函数）。
func TestW13G8NormalizeProjectionCursorArms(t *testing.T) {
	if _, err := normalizeProjectionCursor("2026-09-18T00:00:00Z", "  "); err == nil || !strings.Contains(err.Error(), "outcomeId 无效") {
		t.Fatalf("空 outcomeId 必须报错: %v", err)
	}
	long := strings.Repeat("w", 4097)
	if _, err := normalizeProjectionCursor("2026-09-18T00:00:00Z", long); err == nil || !strings.Contains(err.Error(), "outcomeId 无效") {
		t.Fatalf("越界 outcomeId 必须报错: %v", err)
	}
	if _, err := normalizeProjectionCursor("w13g8-not-a-time", "w13g8-outcome"); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("非法 observedAt 必须报错: %v", err)
	}
}
