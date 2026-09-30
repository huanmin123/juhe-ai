package cleanuprepo

import (
	"fmt"
	"testing"
)

// 渲染辅助：以 logicallyDeleteAccountsTx（BUG-0239 修复处）相同输入复现三条
// 语句的占位符渲染；listFn 注入生产 placeholderList（修复版）或 BindIn（旧写法），
// 保证测试与生产 Bind/placeholderList/BindIn 同构。
func renderLogicallyDeleteUpdate(db *DB, count int, listFn func(int) string) string {
	return db.Bind(fmt.Sprintf(`
      UPDATE %s
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          deleted_at = ?,
          deleted_by = ?,
          updated_at = ?
      WHERE deleted_at IS NULL
        AND id IN (%s)
		`, db.Table("juhe_business", "accounts"), listFn(count)))
}

func renderLogicallyDeleteSelect(db *DB, count int, listFn func(int) string) string {
	return db.Bind(fmt.Sprintf(
		`SELECT id FROM %s WHERE deleted_at = ? AND id IN (%s)`,
		db.Table("juhe_business", "accounts"), listFn(count)))
}

func renderLogicallyDeleteTombstone(db *DB, count int, listFn func(int) string) string {
	lockSuffix := ""
	if db.Postgres {
		lockSuffix = " FOR UPDATE"
	}
	return db.Bind(fmt.Sprintf(`
      SELECT id, config_revision, dispatch_revision
      FROM %s
      WHERE deleted_at = ?
        AND id IN (%s)
        AND provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid')
        AND type IN ('api_key', 'oauth', 'google_oauth')
      ORDER BY id ASC%s
		`, db.Table("juhe_business", "accounts"), listFn(count), lockSuffix))
}

var logicallyDeleteChunkSizes = []int{1, 2, 3, 7}

// 回归（BUG-0239，生产 delete-account-cleanup 孤儿账户逻辑删除链）：三条
// 语句的 IN 列表必须用未编号的 `?` 序列（placeholderList）让外层 Bind 统一
// 编号；BindIn 已产出 $n，前置 `?` 再经 Bind 会从 $1 重编并与 IN 列表撞号，
// pgx 按最大序号计参数即报 "mismatched param and argument count"。
// 传参顺序（UPDATE 3+n、两条 SELECT 1+n）与生产 append 顺序一致。
func TestLogicallyDeleteAccountsUpdatePlaceholderNumbering(t *testing.T) {
	db := &DB{Postgres: true}
	for _, count := range logicallyDeleteChunkSizes {
		rendered := renderLogicallyDeleteUpdate(db, count, placeholderList)
		maxOrdinal, leftover := placeholderCounts(rendered)
		if maxOrdinal != 3+count {
			t.Fatalf("UPDATE IN(%d)：服务端参数序号 %d != 传参 %d（渲染结果：%s）", count, maxOrdinal, 3+count, rendered)
		}
		if leftover != 0 {
			t.Fatalf("UPDATE IN(%d)：残留未编号 `?` %d 个（渲染结果：%s）", count, leftover, rendered)
		}
	}
	// 对照：旧写法（BindIn 预编号 + 外层 Bind 重编）必须暴露撞号，防止回退。
	for _, count := range logicallyDeleteChunkSizes {
		collided := renderLogicallyDeleteUpdate(db, count, db.BindIn)
		maxOrdinal, _ := placeholderCounts(collided)
		if maxOrdinal == 3+count {
			t.Fatalf("UPDATE IN(%d)：旧写法不应通过，BindIn+Bind 撞号时最大序号 %d 不应等于传参 %d", count, maxOrdinal, 3+count)
		}
	}
}

func TestLogicallyDeleteAccountsSelectPlaceholderNumbering(t *testing.T) {
	db := &DB{Postgres: true}
	for _, count := range logicallyDeleteChunkSizes {
		rendered := renderLogicallyDeleteSelect(db, count, placeholderList)
		maxOrdinal, leftover := placeholderCounts(rendered)
		if maxOrdinal != 1+count {
			t.Fatalf("SELECT IN(%d)：服务端参数序号 %d != 传参 %d（渲染结果：%s）", count, maxOrdinal, 1+count, rendered)
		}
		if leftover != 0 {
			t.Fatalf("SELECT IN(%d)：残留未编号 `?` %d 个（渲染结果：%s）", count, leftover, rendered)
		}
	}
	// 对照：旧写法必须暴露撞号（前置 `?` 重编为 $1 与 IN 列表 $1 撞号，
	// 最大序号退化为 n 而非 n+1）。
	for _, count := range logicallyDeleteChunkSizes {
		collided := renderLogicallyDeleteSelect(db, count, db.BindIn)
		maxOrdinal, _ := placeholderCounts(collided)
		if maxOrdinal == 1+count {
			t.Fatalf("SELECT IN(%d)：旧写法不应通过，BindIn+Bind 撞号时最大序号 %d 不应等于传参 %d", count, maxOrdinal, 1+count)
		}
	}
}

func TestLogicallyDeleteAccountsTombstonePlaceholderNumbering(t *testing.T) {
	db := &DB{Postgres: true}
	// lockSuffix（FOR UPDATE）无占位符，不参与编号。
	for _, count := range logicallyDeleteChunkSizes {
		rendered := renderLogicallyDeleteTombstone(db, count, placeholderList)
		maxOrdinal, leftover := placeholderCounts(rendered)
		if maxOrdinal != 1+count {
			t.Fatalf("tombstone IN(%d)：服务端参数序号 %d != 传参 %d（渲染结果：%s）", count, maxOrdinal, 1+count, rendered)
		}
		if leftover != 0 {
			t.Fatalf("tombstone IN(%d)：残留未编号 `?` %d 个（渲染结果：%s）", count, leftover, rendered)
		}
	}
	for _, count := range logicallyDeleteChunkSizes {
		collided := renderLogicallyDeleteTombstone(db, count, db.BindIn)
		maxOrdinal, _ := placeholderCounts(collided)
		if maxOrdinal == 1+count {
			t.Fatalf("tombstone IN(%d)：旧写法不应通过，BindIn+Bind 撞号时最大序号 %d 不应等于传参 %d", count, maxOrdinal, 1+count)
		}
	}
}
