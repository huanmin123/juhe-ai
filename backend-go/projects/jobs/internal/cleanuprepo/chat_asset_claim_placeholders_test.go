package cleanuprepo

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"
)

// 占位符计数辅助：返回渲染后 SQL 的最大 $n 序号与残留 `?` 数量。
// pgx 对含 $n 的 SQL 以最大序号为参数总数，与传参数量不符即报
// "mismatched param and argument count"。
func placeholderCounts(rendered string) (maxOrdinal, leftoverMarks int) {
	for i := 1; i <= 64; i++ {
		if strings.Contains(rendered, fmt.Sprintf("$%d", i)) {
			maxOrdinal = i
		}
	}
	leftoverMarks = strings.Count(rendered, "?")
	return maxOrdinal, leftoverMarks
}

// 回归（生产 chat-retention-cleanup 每轮 mismatched 失败的根因）：
// 资产认领 UPDATE 的 IN 列表必须用未编号的 `?` 序列（placeholderList）让
// 外层 Bind 统一编号；BindIn 已产出 $n 后再经 Bind/全局改写层会把 SET 的
// `?` 从 $1 重编并与 IN 列表撞号。
func TestChatAssetClaimUpdatePlaceholderNumbering(t *testing.T) {
	db := &DB{Postgres: true}
	for _, count := range []int{1, 2, 3, 7} {
		rendered := db.Bind(fmt.Sprintf(`
      UPDATE %s
      SET cleanup_status = 'claimed', cleanup_claim_id = ?, cleanup_claimed_at = ?,
          cleanup_attempt_count = cleanup_attempt_count + 1, cleanup_retry_at = NULL,
          cleanup_error_code = NULL, updated_at = ?
      WHERE id IN (%s)
		`, db.Table("juhe_chat", "chat_assets"), placeholderList(count)))
		maxOrdinal, leftover := placeholderCounts(rendered)
		if maxOrdinal != 3+count {
			t.Fatalf("IN(%d)：服务端参数序号 %d != 传参 %d（渲染结果：%s）", count, maxOrdinal, 3+count, rendered)
		}
		if leftover != 0 {
			t.Fatalf("IN(%d)：残留未编号 `?` %d 个（渲染结果：%s）", count, leftover, rendered)
		}
	}
	// 对照：旧写法（BindIn 预编号 + 外层 Bind 重编）必须暴露撞号，防止回退。
	collided := db.Bind(fmt.Sprintf(
		`UPDATE t SET a = ?, b = ?, c = ? WHERE id IN (%s)`, db.BindIn(3)))
	maxOrdinal, _ := placeholderCounts(collided)
	if maxOrdinal == 3+3 {
		t.Fatalf("旧写法不应通过：BindIn+Bind 撞号时最大序号应为 3，实际 %d", maxOrdinal)
	}
}

// 真实 PG 端到端（可选，设 JUHE_AI_POSTGRES_URL 时执行；走生产同构的
// pgpool rewriteDriver 路径）：修复后的认领 UPDATE 必须通过参数绑定阶段，
// 旧写法必须复现 mismatched。UPDATE 目标为不存在的 ID，零数据变更，事务回滚。
func TestChatAssetClaimUpdateAgainstPG(t *testing.T) {
	url := os.Getenv("JUHE_AI_POSTGRES_URL")
	if url == "" {
		t.Skip("JUHE_AI_POSTGRES_URL 未设置")
	}
	reg := pgpool.NewRegistry()
	handle, err := reg.Acquire("pgx", url, "repro-chat-claim", 2, 2)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = handle.Close() }()
	db := &DB{DB: handle.DB(), Postgres: true}
	ctx := context.Background()

	assetIDs := []string{"repro-nonexistent-1", "repro-nonexistent-2", "repro-nonexistent-3"}
	args := append([]any{"claim-id", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"}, stringSliceToAny(assetIDs)...)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	fixedSQL := db.Bind(fmt.Sprintf(`
      UPDATE %s
      SET cleanup_status = 'claimed', cleanup_claim_id = ?, cleanup_claimed_at = ?,
          cleanup_attempt_count = cleanup_attempt_count + 1, cleanup_retry_at = NULL,
          cleanup_error_code = NULL, updated_at = ?
      WHERE id IN (%s)
	`, db.Table("juhe_chat", "chat_assets"), placeholderList(len(assetIDs))))
	if _, err := tx.ExecContext(ctx, fixedSQL, args...); err != nil {
		t.Fatalf("修复版认领 UPDATE 不应失败: %v", err)
	}

	collidedSQL := db.Bind(fmt.Sprintf(
		`UPDATE %s SET cleanup_status = 'claimed', cleanup_claim_id = ?, updated_at = ? WHERE id IN (%s)`,
		db.Table("juhe_chat", "chat_assets"), db.BindIn(len(assetIDs))))
	_, err = tx.ExecContext(ctx, collidedSQL, append([]any{"claim-id", "2026-01-01T00:00:00.000Z"}, stringSliceToAny(assetIDs)...)...)
	if err == nil || !strings.Contains(err.Error(), "mismatched param and argument count") {
		t.Fatalf("旧写法应复现 mismatched，实际: %v", err)
	}
}
