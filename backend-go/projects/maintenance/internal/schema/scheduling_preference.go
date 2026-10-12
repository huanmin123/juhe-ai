// 调度偏好独立实体（调度偏好独立实体 PLAN-20261012T033000000Z P1，接口契约
// 「调度偏好」章）内置行种子：全局只读内置行「对话·成本优先」，固定 id、
// system_account_id=NULL、built_in=1、chat_mode='cost_first'、enabled=1，
// 作为全局例外供所有 owner 读取与绑定，不得挂在真实用户名下再按 scope 过滤。
//
// 写入通道（两条，共用同一恢复式 upsert 语句、同一权威值）：
//   - maintenance --seed：seedPostgresDefaults / SeedSQLiteDefaults 在既有
//     系统内置资源顺序位（系统内置题库题之后）幂等预置；
//   - maintenance --ensure-schema 补种臂：SeedBuiltinSchedulingPreferenceSQLite
//     / SeedBuiltinSchedulingPreferencePostgres，保证存量库「升级 = 跑
//     ensure-schema 即得内置行」（ensure 臂原本不执行完整 seed）。
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
// 变更，需同步接口契约「调度偏好」章与计划 D1）。
const (
	BuiltinSchedulingPreferenceID   = "schpref-builtin-chat-cost-first"
	BuiltinSchedulingPreferenceName = "对话·成本优先"
)

// pgSeedBuiltinSchedulingPreferenceUpsert 是 PG 方言的恢复式 upsert（表名带
// juhe_business 前缀，pgSeedBuiltinQuestionUpsert 先例）；sqSeedBuiltinSchedulingPreferenceUpsert
// 是 SQLite 方言同构语句。参数依次为：id、name、created_at、updated_at；
// system_account_id / chat_mode / chat_speed_first_config_json / built_in /
// enabled 为语句内字面权威值。
const pgSeedBuiltinSchedulingPreferenceUpsert = `
        INSERT INTO "juhe_business"."scheduling_preferences"
          (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, enabled, created_at, updated_at)
        VALUES ($1, NULL, $2, 'cost_first', NULL, 1, 1, $3, $4)
        ON CONFLICT (id) DO UPDATE SET
          system_account_id = EXCLUDED.system_account_id,
          name = EXCLUDED.name,
          chat_mode = EXCLUDED.chat_mode,
          chat_speed_first_config_json = EXCLUDED.chat_speed_first_config_json,
          built_in = EXCLUDED.built_in,
          enabled = EXCLUDED.enabled
          WHERE "juhe_business"."scheduling_preferences".built_in = 1
      `

const sqSeedBuiltinSchedulingPreferenceUpsert = `
    INSERT INTO scheduling_preferences
      (id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, enabled, created_at, updated_at)
    VALUES (?, NULL, ?, 'cost_first', NULL, 1, 1, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      system_account_id=excluded.system_account_id,
      name=excluded.name,
      chat_mode=excluded.chat_mode,
      chat_speed_first_config_json=excluded.chat_speed_first_config_json,
      built_in=excluded.built_in,
      enabled=excluded.enabled
      WHERE scheduling_preferences.built_in=1
  `

// SeedBuiltinSchedulingPreferenceSQLite 把内置调度偏好行恢复式补种进一个已
// 应用 business schema 的 SQLite 业务库，供 maintenance --ensure-schema 补种
// 臂调用（存量库升级 = 跑 ensure-schema 即得内置行）；--seed 链在
// SeedSQLiteDefaults 内走同一 upsert 语句，幂等可重入。
func SeedBuiltinSchedulingPreferenceSQLite(ctx context.Context, db *sql.DB) error {
	now := seedTimestamp(time.Now())
	if _, err := db.ExecContext(ctx, sqSeedBuiltinSchedulingPreferenceUpsert,
		BuiltinSchedulingPreferenceID, BuiltinSchedulingPreferenceName, now, now); err != nil {
		return fmt.Errorf("seed builtin scheduling preference (sqlite): %w", err)
	}
	return nil
}

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
