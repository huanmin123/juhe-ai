// 调度偏好独立实体（调度偏好独立实体 PLAN-20261012T033000000Z P1，接口契约
// 「调度偏好」章；v2 用户裁决修订第 2/4 条）内置行种子：全局只读内置行
// 「默认调度偏好」，固定 id、system_account_id=NULL、built_in=1、
// chat_mode='cost_first'，作为全局例外供所有 owner 读取与绑定，不得挂在真
// 实用户名下再按 scope 过滤。固定 id 保留 P1 初名的不透明标识（迁移命令、
// 种子、前后端常量锚定该 ID，更名无行为收益）；展示名走 v2 通用「默认」定位，
// 存量库经恢复式 upsert 自动拉回新权威名。enabled 已随 v2 第 4 条退役
// （创建即启用，无停用态），本文件不再携带该列。
//
// 写入通道（两条，共用同一权威值）：
//   - maintenance --seed：SeedPostgresDefaults 在既有系统内置资源顺序位
//    （系统内置题库题之后）幂等预置；
//   - maintenance --ensure-schema 补种臂：SeedBuiltinSchedulingPreferencePostgres，
//     保证存量库「升级 = 跑 ensure-schema 即得内置行」（ensure 臂原本不执行
//     完整 seed）。SQLite 通道随 SQLite 存储形态退役删除
//     （PLAN-20261024T051925945Z B4）。
//
// upsert 语义沿用 pgSeedBuiltinQuestionUpsert 先例：固定 id 冲突时仅在目标行
// 仍是内置行（built_in=1）时把权威字段恢复为 seed 值——内置行业务写入口一律
// 拒绝（应用层 403），该 upsert 要么插入新行、要么把被外部改动过的内置行拉回
// 权威值；updated_at 不在更新列内，重复执行对已一致的行不推进 updated_at；
// 若固定 id 被自定义行（built_in=0）占用则不触碰，交由启动校验/服务层暴露。

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 内置调度偏好行的固定身份与权威值（契约固定，不得改动——改动即种子契约
// 变更，需同步接口契约「调度偏好」章与计划 D1；固定 id 保留 P1 不透明标识，
// 展示名随 v2 裁决改为「默认调度偏好」）。
const (
	BuiltinSchedulingPreferenceID   = "schpref-builtin-chat-cost-first"
	BuiltinSchedulingPreferenceName = "默认调度偏好"
)

// pgSeedBuiltinSchedulingPreferenceUpsert 是 PG 方言的恢复式 upsert（表名带
// juhe_business 前缀，pgSeedBuiltinQuestionUpsert 先例）。参数依次为：id、
// name、created_at、updated_at；system_account_id / chat_mode /
// chat_speed_first_config_json / built_in 为语句内字面权威值。
const pgSeedBuiltinSchedulingPreferenceUpsert = `
        INSERT INTO "juhe_business"."scheduling_preferences"
          (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
        VALUES ($1, NULL, $2, 'cost_first', NULL, 1, $3, $4)
        ON CONFLICT (id) DO UPDATE SET
          system_account_id = EXCLUDED.system_account_id,
          name = EXCLUDED.name,
          chat_mode = EXCLUDED.chat_mode,
          chat_speed_first_config_json = EXCLUDED.chat_speed_first_config_json,
          built_in = EXCLUDED.built_in
          WHERE "juhe_business"."scheduling_preferences".built_in = 1
      `

// SeedBuiltinSchedulingPreferencePostgres 把内置调度偏好行恢复式补种进一个已
// 应用 PostgreSQL schema 的业务库，供 maintenance --ensure-schema 补种臂调用；
// 语义与 SQLite 臂一致（同源权威值、同恢复式幂等）。
func SeedBuiltinSchedulingPreferencePostgres(ctx context.Context, db *sql.DB) error {
	now := seedTimestamp(time.Now())
	if _, err := db.ExecContext(ctx, pgSeedBuiltinSchedulingPreferenceUpsert,
		BuiltinSchedulingPreferenceID, BuiltinSchedulingPreferenceName, now, now); err != nil {
		return fmt.Errorf("seed builtin scheduling preference (postgres): %w", err)
	}
	return nil
}
