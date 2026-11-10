package legacybusinessimport

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/groupspolicymigration"
)

// importGroupAuthorizationSettings carries the per-group authorization rows.
// 2026-11-09 授权覆盖退役（普通分组可选调度策略设计 §2.1/§4）：group_type 一律
// 归一 personal，scheduling_policy_json 恒 NULL——被授权方运行来源分组的两个
// 策略，不再有 per-grantee 覆盖；畸形策略列中断导入并保留原始错误，不静默
// 丢弃。
func importGroupAuthorizationSettings(ctx context.Context, source *sql.DB, tx *sql.Tx, groups map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
		       created_at, updated_at
		FROM juhe_business.group_authorization_settings`)
	if err != nil {
		return fmt.Errorf("读取旧分组授权设置: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var authorizationID, owner, groupID, groupType, createdAt, updatedAt string
		var enabled int
		var scheduling sql.NullString
		if err := rows.Scan(&authorizationID, &owner, &groupID, &enabled, &groupType, &scheduling,
			&createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧分组授权设置: %w", err)
		}
		if _, ok := groups[groupID]; !ok {
			continue
		}
		if _, reviewReason := groupspolicymigration.NormalizeRow(groupType, scheduling,
			groupspolicymigration.DefaultGlobalConcurrencyMax); reviewReason != "" {
			return fmt.Errorf("分组授权设置 %s（group %s）调度策略需要人工复核（%s），先在旧库修正后重跑: %s",
				authorizationID, groupID, reviewReason, nullStringText(scheduling))
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.group_authorization_settings
				(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
				 created_at, updated_at)
			VALUES ($1,$2,$3,$4,'personal',NULL,$5,$6)`,
			authorizationID, owner, groupID, enabled, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入分组授权设置 %s: %w", groupID, err)
		}
		count++
	}
	report.Tables["group_authorization_settings"] = count
	return rows.Err()
}
