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
	"strings"
	"testing"
)

const w13g8OutcomeColumns = `(outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, payload)`

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
