package legacybusinessimport

import (
	"context"
	"database/sql"
	"fmt"
)

// importGroupAuthorizationSettings carries the per-group authorization
// overrides. The legacy and target structures are identical.
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
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.group_authorization_settings
				(authorization_id, system_account_id, group_id, enabled, group_type, scheduling_policy_json,
				 created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			authorizationID, owner, groupID, enabled, groupType, scheduling, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入分组授权设置 %s: %w", groupID, err)
		}
		count++
	}
	report.Tables["group_authorization_settings"] = count
	return rows.Err()
}
