// 安全策略实体（API Key 安全策略与出站内容脱敏设计 §4.1，批A 存储）内置行
// 种子：全局只读内置行「默认安全策略」，固定 id、system_account_id=NULL、
// built_in=1、rules_json='{"rules":[]}'（空规则集 = 不处理 = 请求原样转发，
// 现有网关行为逐字节不变），作为全局例外供所有 owner 读取与绑定，不得挂在
// 真实用户名下再按 scope 过滤。实体 CRUD 与 API Key 绑定（五写入口矩阵）在
// gateway internal/securitypolicies 与 internal/apikeys（同调度偏好范式）。
//
// 写入通道（两条，共用同一权威值）：
//   - maintenance --seed：SeedPostgresDefaults 在内置调度偏好 seed 之后幂等
//    预置；
//   - maintenance --ensure-schema 补种臂：SeedBuiltinSecurityPolicyPostgres，
//     保证存量库「升级 = 跑 ensure-schema 即得内置行」（ensure 臂原本不执行
//     完整 seed）。
//
// upsert 语义沿用 pgSeedBuiltinSchedulingPreferenceUpsert 先例：固定 id 冲突
// 时仅在目标行仍是内置行（built_in=1）时把权威字段恢复为 seed 值——内置行业
// 务写入口一律拒绝（应用层 403），该 upsert 要么插入新行、要么把被外部改动
// 过的内置行拉回权威值；created_at / updated_at 不在更新列内，重复执行对已一
// 致的行不推进时间戳；若固定 id 被自定义行（built_in=0）占用则不触碰，交由
// 服务层暴露。

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 内置安全策略行的固定身份与权威值（契约固定，不得改动——改动即种子契约变
// 更，需同步 API Key 安全策略与出站内容脱敏设计 §4.1 与 gateway
// internal/securitypolicies、internal/apikeys 及前端 api 常量；同源锚点改动
// 即多处同改）。
const (
	BuiltinSecurityPolicyID   = "secpol-builtin-default"
	BuiltinSecurityPolicyName = "默认安全策略"
)

// pgSeedBuiltinSecurityPolicyUpsert 是 PG 方言的恢复式 upsert（表名带
// juhe_business 前缀，pgSeedBuiltinSchedulingPreferenceUpsert 先例）。参数依
// 次为：id、name、created_at、updated_at；system_account_id / rules_json /
// built_in 为语句内字面权威值。
const pgSeedBuiltinSecurityPolicyUpsert = `
        INSERT INTO "juhe_business"."security_policies"
          (id, system_account_id, name, description, rules_json, built_in, created_at, updated_at)
        VALUES ($1, NULL, $2, NULL, '{"rules":[]}', 1, $3::timestamptz, $4::timestamptz)
        ON CONFLICT (id) DO UPDATE SET
          system_account_id = EXCLUDED.system_account_id,
          name = EXCLUDED.name,
          description = EXCLUDED.description,
          rules_json = EXCLUDED.rules_json,
          built_in = EXCLUDED.built_in
          WHERE "juhe_business"."security_policies".built_in = 1
      `

// SeedBuiltinSecurityPolicyPostgres 把内置安全策略行恢复式补种进一个已应用
// PostgreSQL schema 的业务库，供 maintenance --ensure-schema 补种臂调用；语义
// 与调度偏好内置行种子一致（同源权威值、同恢复式幂等）。
func SeedBuiltinSecurityPolicyPostgres(ctx context.Context, db *sql.DB) error {
	now := seedTimestamp(time.Now())
	if _, err := db.ExecContext(ctx, pgSeedBuiltinSecurityPolicyUpsert,
		BuiltinSecurityPolicyID, BuiltinSecurityPolicyName, now, now); err != nil {
		return fmt.Errorf("seed builtin security policy (postgres): %w", err)
	}
	return nil
}
