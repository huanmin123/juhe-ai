package legacybusinessimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// importSystemAccounts copies every owner row verbatim: the surviving columns
// are identical to the legacy ones, including the password-hash envelope, so
// logins survive the cutover unchanged. The seeded sys_admin row already
// exists; it is updated in place instead of being inserted twice.
func importSystemAccounts(ctx context.Context, source *sql.DB, tx *sql.Tx) (map[string]struct{}, error) {
	rows, err := source.QueryContext(ctx, `
		SELECT id, username, display_name, COALESCE(description,''), role, status, password_hash,
		       must_change_password, image_generation_enabled, ai_account_limit, request_limits_json,
		       last_login_at, created_at, updated_at, expedited_account_limit
		FROM juhe_business.system_accounts`)
	if err != nil {
		return nil, fmt.Errorf("读取旧系统账户: %w", err)
	}
	defer rows.Close()
	owners := map[string]struct{}{}
	count := 0
	for rows.Next() {
		var id, username, displayName, description, role, status, passwordHash string
		var mustChange, imageGen int
		var aiLimit, expeditedLimit sql.NullInt64
		var requestLimits, lastLogin sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&id, &username, &displayName, &description, &role, &status, &passwordHash,
			&mustChange, &imageGen, &aiLimit, &requestLimits, &lastLogin, &createdAt, &updatedAt, &expeditedLimit); err != nil {
			return nil, fmt.Errorf("扫描旧系统账户: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.system_accounts
				(id, username, display_name, description, role, status, password_hash, must_change_password,
				 image_generation_enabled, ai_account_limit, expedited_account_limit, request_limits_json,
				 last_login_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (id) DO UPDATE SET
				username=EXCLUDED.username, display_name=EXCLUDED.display_name, description=EXCLUDED.description,
				role=EXCLUDED.role, status=EXCLUDED.status, password_hash=EXCLUDED.password_hash,
				must_change_password=EXCLUDED.must_change_password,
				image_generation_enabled=EXCLUDED.image_generation_enabled,
				ai_account_limit=EXCLUDED.ai_account_limit, expedited_account_limit=EXCLUDED.expedited_account_limit,
				request_limits_json=EXCLUDED.request_limits_json, last_login_at=EXCLUDED.last_login_at,
				updated_at=EXCLUDED.updated_at`,
			id, username, displayName, description, role, status, passwordHash,
			mustChange, imageGen, aiLimit, expeditedLimit, requestLimits, lastLogin, createdAt, updatedAt); err != nil {
			return nil, fmt.Errorf("写入系统账户 %s: %w", id, err)
		}
		owners[id] = struct{}{}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历旧系统账户: %w", err)
	}
	_ = count
	return owners, nil
}

// importProxyProfiles carries the outbound proxy definitions. The encrypted
// password envelope is copied byte-for-byte (same JUHE_AI_SECRET).
func importProxyProfiles(ctx context.Context, source *sql.DB, tx *sql.Tx, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, system_account_id, name, COALESCE(description,''), type, host, port,
		       COALESCE(username,''), password_encrypted, enabled, test_status, latency_ms,
		       COALESCE(outbound_ip,''), COALESCE(outbound_region,''), COALESCE(last_test_message,''),
		       last_tested_at, created_at, updated_at
		FROM juhe_business.proxy_profiles`)
	if err != nil {
		return fmt.Errorf("读取旧代理配置: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, owner, name, description, kind, host, username, testStatus string
		var password sql.NullString
		var port int
		var enabled bool
		var latency sql.NullInt64
		var outboundIP, outboundRegion, lastMessage, lastTested sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&id, &owner, &name, &description, &kind, &host, &port, &username, &password,
			&enabled, &testStatus, &latency, &outboundIP, &outboundRegion, &lastMessage, &lastTested,
			&createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧代理配置: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.proxy_profiles
				(id, system_account_id, name, description, type, host, port, username, password_encrypted,
				 enabled, test_status, latency_ms, outbound_ip, outbound_region, last_test_message,
				 last_tested_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			id, owner, name, description, kind, host, port, username, password,
			enabled, testStatus, latency, outboundIP, outboundRegion, lastMessage,
			lastTested, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入代理配置 %s: %w", id, err)
		}
		count++
	}
	report.Tables["proxy_profiles"] = count
	return rows.Err()
}

// importSchedulingPreferences carries the per-owner dispatch preferences. The
// built-in row is seeded already; the upsert keeps its identity while adopting
// any user edits made in production.
func importSchedulingPreferences(ctx context.Context, source *sql.DB, tx *sql.Tx, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, system_account_id, name, chat_mode, COALESCE(chat_speed_first_config_json,'{}'),
		       built_in, created_at, updated_at
		FROM juhe_business.scheduling_preferences`)
	if err != nil {
		return fmt.Errorf("读取旧调度偏好: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, name, chatMode, speedConfig, createdAt, updatedAt string
		var owner sql.NullString
		var builtIn int
		if err := rows.Scan(&id, &owner, &name, &chatMode, &speedConfig, &builtIn, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧调度偏好: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.scheduling_preferences
				(id, system_account_id, name, chat_mode, chat_speed_first_config_json, built_in, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO UPDATE SET
				system_account_id=EXCLUDED.system_account_id, name=EXCLUDED.name,
				chat_mode=EXCLUDED.chat_mode, chat_speed_first_config_json=EXCLUDED.chat_speed_first_config_json,
				built_in=EXCLUDED.built_in, updated_at=EXCLUDED.updated_at`,
			id, owner, name, chatMode, speedConfig, builtIn, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入调度偏好 %s: %w", id, err)
		}
		count++
	}
	report.Tables["scheduling_preferences"] = count
	return rows.Err()
}

// importTeams carries teams and their memberships.
func importTeams(ctx context.Context, source *sql.DB, tx *sql.Tx, report *Report) error {
	teamRows, err := source.QueryContext(ctx, `
		SELECT id, name, COALESCE(description,''), status, created_by, created_at, updated_at
		FROM juhe_business.system_teams`)
	if err != nil {
		return fmt.Errorf("读取旧团队: %w", err)
	}
	defer teamRows.Close()
	teams := 0
	for teamRows.Next() {
		var id, name, description, status, createdBy, createdAt, updatedAt string
		if err := teamRows.Scan(&id, &name, &description, &status, &createdBy, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧团队: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.system_teams (id, name, description, status, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, name, description, status, createdBy, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入团队 %s: %w", id, err)
		}
		teams++
	}
	if err := teamRows.Err(); err != nil {
		return fmt.Errorf("遍历旧团队: %w", err)
	}
	report.Tables["system_teams"] = teams

	memberRows, err := source.QueryContext(ctx, `
		SELECT id, team_id, system_account_id, member_role, status, joined_at, removed_at, created_by, created_at, updated_at
		FROM juhe_business.system_team_members`)
	if err != nil {
		return fmt.Errorf("读取旧团队成员: %w", err)
	}
	defer memberRows.Close()
	members := 0
	for memberRows.Next() {
		var id, teamID, owner, role, status, joinedAt, createdBy, createdAt, updatedAt string
		var removedAt sql.NullString
		if err := memberRows.Scan(&id, &teamID, &owner, &role, &status, &joinedAt, &removedAt, &createdBy, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧团队成员: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.system_team_members
				(id, team_id, system_account_id, member_role, status, joined_at, removed_at, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id, teamID, owner, role, status, joinedAt, removedAt, createdBy, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入团队成员 %s: %w", id, err)
		}
		members++
	}
	report.Tables["system_team_members"] = members
	return memberRows.Err()
}

// importGroups copies groups, dropping the retired provider dimension and
// normalising the default flag: the target enforces one default group per
// owner (partial unique index), while the legacy schema allowed several. The
// kept default is the lowest group id per owner, which is stable across reruns.
func importGroups(ctx context.Context, source *sql.DB, tx *sql.Tx, owners map[string]struct{}, report *Report) (map[string]struct{}, error) {
	rows, err := source.QueryContext(ctx, `
		SELECT id, system_account_id, name, COALESCE(description,''), enabled, is_default, group_type,
		       scheduling_policy_json, created_at, updated_at
		FROM juhe_business.groups
		ORDER BY system_account_id, id`)
	if err != nil {
		return nil, fmt.Errorf("读取旧分组: %w", err)
	}
	defer rows.Close()
	type groupRow struct {
		id, owner, name, description, groupType, createdAt, updatedAt string
		enabled, isDefault                                            int
		schedulingPolicy                                              sql.NullString
	}
	var collected []groupRow
	for rows.Next() {
		var row groupRow
		if err := rows.Scan(&row.id, &row.owner, &row.name, &row.description, &row.enabled, &row.isDefault,
			&row.groupType, &row.schedulingPolicy, &row.createdAt, &row.updatedAt); err != nil {
			return nil, fmt.Errorf("扫描旧分组: %w", err)
		}
		collected = append(collected, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历旧分组: %w", err)
	}

	// Keep the first default seen per owner (rows are ordered by owner, id).
	keptDefault := map[string]bool{}
	ids := map[string]struct{}{}
	droppedDefaults := 0
	for _, row := range collected {
		isDefault := row.isDefault
		if isDefault == 1 {
			if keptDefault[row.owner] {
				isDefault = 0
				droppedDefaults++
			} else {
				keptDefault[row.owner] = true
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.groups
				(id, system_account_id, name, description, enabled, is_default, group_type, group_kind,
				 scheduling_policy_json, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'standard',$8,$9,$10)
			ON CONFLICT (id) DO UPDATE SET
				name=EXCLUDED.name, description=EXCLUDED.description, enabled=EXCLUDED.enabled,
				is_default=EXCLUDED.is_default, group_type=EXCLUDED.group_type,
				scheduling_policy_json=EXCLUDED.scheduling_policy_json, updated_at=EXCLUDED.updated_at`,
			row.id, row.owner, row.name, row.description, row.enabled, isDefault, row.groupType,
			row.schedulingPolicy, row.createdAt, row.updatedAt); err != nil {
			return nil, fmt.Errorf("写入分组 %s: %w", row.id, err)
		}
		ids[row.id] = struct{}{}
	}
	report.Tables["groups"] = len(collected)
	if droppedDefaults > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("默认分组归一化：%d 个多余 is_default 行已降级（新 schema 每 owner 仅允许一个默认分组）", droppedDefaults))
	}
	return ids, nil
}

// importRouteStrategies copies strategies and their group bindings. The retired
// "merge" mode is already absent from production; anything unexpected aborts.
func importRouteStrategies(ctx context.Context, source *sql.DB, tx *sql.Tx, groups map[string]struct{}, report *Report) (map[string]struct{}, error) {
	rows, err := source.QueryContext(ctx, `
		SELECT id, system_account_id, name, COALESCE(description,''), mode, status, is_default,
		       config_json, created_at, updated_at
		FROM juhe_business.route_strategies`)
	if err != nil {
		return nil, fmt.Errorf("读取旧策略路由: %w", err)
	}
	defer rows.Close()
	ids := map[string]struct{}{}
	count := 0
	for rows.Next() {
		var id, owner, name, description, mode, status, createdAt, updatedAt string
		var isDefault int
		var config sql.NullString
		if err := rows.Scan(&id, &owner, &name, &description, &mode, &status, &isDefault, &config, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描旧策略路由: %w", err)
		}
		switch mode {
		case "normal", "weighted", "failover", "round_robin":
		default:
			return nil, fmt.Errorf("策略路由 %s 使用已退役模式 %q；请先运行 --migrate-merge-super-groups", id, mode)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.route_strategies
				(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (id) DO UPDATE SET
				name=EXCLUDED.name, description=EXCLUDED.description, mode=EXCLUDED.mode,
				status=EXCLUDED.status, is_default=EXCLUDED.is_default, config_json=EXCLUDED.config_json,
				updated_at=EXCLUDED.updated_at`,
			id, owner, name, description, mode, status, isDefault, config, createdAt, updatedAt); err != nil {
			return nil, fmt.Errorf("写入策略路由 %s: %w", id, err)
		}
		ids[id] = struct{}{}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历旧策略路由: %w", err)
	}
	report.Tables["route_strategies"] = count

	bindingRows, err := source.QueryContext(ctx, `
		SELECT id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
		FROM juhe_business.route_strategy_groups`)
	if err != nil {
		return nil, fmt.Errorf("读取旧路由绑定: %w", err)
	}
	defer bindingRows.Close()
	bindings := 0
	for bindingRows.Next() {
		var id, strategyID, owner, groupID, status, createdAt, updatedAt string
		var priority, weight int
		if err := bindingRows.Scan(&id, &strategyID, &owner, &groupID, &priority, &weight, &status, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描旧路由绑定: %w", err)
		}
		if _, ok := groups[groupID]; !ok {
			return nil, fmt.Errorf("路由绑定 %s 引用不存在的分组 %s", id, groupID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.route_strategy_groups
				(id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (id) DO UPDATE SET
				group_id=EXCLUDED.group_id, priority=EXCLUDED.priority, weight=EXCLUDED.weight,
				status=EXCLUDED.status, updated_at=EXCLUDED.updated_at`,
			id, strategyID, owner, groupID, priority, weight, status, createdAt, updatedAt); err != nil {
			return nil, fmt.Errorf("写入路由绑定 %s: %w", id, err)
		}
		bindings++
	}
	report.Tables["route_strategy_groups"] = bindings
	return ids, bindingRows.Err()
}

// importTags copies tag definitions and their account bindings.
func importTags(ctx context.Context, source *sql.DB, tx *sql.Tx, accounts map[string]struct{}, owners map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, system_account_id, name, created_at, updated_at FROM juhe_business.account_tags`)
	if err != nil {
		return fmt.Errorf("读取旧账户标签: %w", err)
	}
	defer rows.Close()
	tags := 0
	for rows.Next() {
		var id, owner, name, createdAt, updatedAt string
		if err := rows.Scan(&id, &owner, &name, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧账户标签: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_tags (id, system_account_id, name, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5)`, id, owner, name, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入账户标签 %s: %w", id, err)
		}
		tags++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧账户标签: %w", err)
	}
	report.Tables["account_tags"] = tags

	bindingRows, err := source.QueryContext(ctx, `
		SELECT account_id, tag_id, system_account_id, created_at FROM juhe_business.account_tag_bindings`)
	if err != nil {
		return fmt.Errorf("读取旧标签绑定: %w", err)
	}
	defer bindingRows.Close()
	bindings := 0
	skipped := 0
	for bindingRows.Next() {
		var accountID, tagID, owner, createdAt string
		if err := bindingRows.Scan(&accountID, &tagID, &owner, &createdAt); err != nil {
			return fmt.Errorf("扫描旧标签绑定: %w", err)
		}
		if _, ok := accounts[accountID]; !ok {
			// Soft-deleted accounts are intentionally not migrated.
			skipped++
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_tag_bindings (account_id, tag_id, system_account_id, created_at)
			VALUES ($1,$2,$3,$4)`, accountID, tagID, owner, createdAt); err != nil {
			return fmt.Errorf("写入标签绑定 %s: %w", accountID, err)
		}
		bindings++
	}
	report.Tables["account_tag_bindings"] = bindings
	if skipped > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("标签绑定：%d 条指向未迁移账户，已跳过", skipped))
	}
	return bindingRows.Err()
}

// importAPIKeys copies API keys (hash and encrypted secret verbatim, so live
// clients keep working) after validating their strategy reference.
func importAPIKeys(ctx context.Context, source *sql.DB, tx *sql.Tx, strategies map[string]struct{}, owners map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, system_account_id, route_strategy_id, name, COALESCE(description,''), key_hash, key_prefix,
		       key_suffix, key_secret_encrypted, status, is_default, purpose, expires_at,
		       quota_limits_json, availability_schedule_json, availability_schedule_next_check_at,
		       last_used_at, created_at, updated_at, scheduling_preference_id
		FROM juhe_business.api_keys`)
	if err != nil {
		return fmt.Errorf("读取旧 API Key: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, owner, strategyID, name, description, keyHash, prefix, suffix, secret, status, purpose string
		var isDefault int
		var expiresAt, quota, availability, availabilityNext, lastUsed, preference sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&id, &owner, &strategyID, &name, &description, &keyHash, &prefix, &suffix, &secret,
			&status, &isDefault, &purpose, &expiresAt, &quota, &availability, &availabilityNext,
			&lastUsed, &createdAt, &updatedAt, &preference); err != nil {
			return fmt.Errorf("扫描旧 API Key: %w", err)
		}
		if _, ok := strategies[strategyID]; !ok {
			return fmt.Errorf("API Key %s 引用不存在的策略路由 %s", id, strategyID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.api_keys
				(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
				 key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json,
				 availability_schedule_json, availability_schedule_next_check_at, last_used_at,
				 created_at, updated_at, scheduling_preference_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
			ON CONFLICT (id) DO UPDATE SET
				name=EXCLUDED.name, description=EXCLUDED.description, key_hash=EXCLUDED.key_hash,
				key_prefix=EXCLUDED.key_prefix, key_suffix=EXCLUDED.key_suffix,
				key_secret_encrypted=EXCLUDED.key_secret_encrypted, status=EXCLUDED.status,
				is_default=EXCLUDED.is_default, expires_at=EXCLUDED.expires_at,
				quota_limits_json=EXCLUDED.quota_limits_json, scheduling_preference_id=EXCLUDED.scheduling_preference_id,
				updated_at=EXCLUDED.updated_at`,
			id, owner, strategyID, name, description, keyHash, prefix, suffix, secret, status, isDefault,
			purpose, expiresAt, quota, availability, availabilityNext, lastUsed, createdAt, updatedAt,
			preference); err != nil {
			return fmt.Errorf("写入 API Key %s: %w", id, err)
		}
		count++
	}
	report.Tables["api_keys"] = count
	return rows.Err()
}

// importSettings copies the owner-level and global settings maps. Rows whose
// key already exists from the seed are overwritten so production values win.
func importSettings(ctx context.Context, source *sql.DB, tx *sql.Tx, owners map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `SELECT system_account_id, key, value_json, updated_at FROM juhe_business.system_settings`)
	if err != nil {
		return fmt.Errorf("读取旧系统设置: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var owner, key, value, updatedAt string
		if err := rows.Scan(&owner, &key, &value, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧系统设置: %w", err)
		}
		if _, ok := owners[owner]; !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (system_account_id, key) DO UPDATE SET value_json=EXCLUDED.value_json, updated_at=EXCLUDED.updated_at`,
			owner, key, value, updatedAt); err != nil {
			return fmt.Errorf("写入系统设置 %s/%s: %w", owner, key, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧系统设置: %w", err)
	}
	report.Tables["system_settings"] = count

	globalRows, err := source.QueryContext(ctx, `SELECT key, value_json, updated_at FROM juhe_business.global_settings`)
	if err != nil {
		return fmt.Errorf("读取旧全局设置: %w", err)
	}
	defer globalRows.Close()
	global := 0
	for globalRows.Next() {
		var key, value, updatedAt string
		if err := globalRows.Scan(&key, &value, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧全局设置: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.global_settings (key, value_json, updated_at)
			VALUES ($1,$2,$3)
			ON CONFLICT (key) DO UPDATE SET value_json=EXCLUDED.value_json, updated_at=EXCLUDED.updated_at`,
			key, value, updatedAt); err != nil {
			return fmt.Errorf("写入全局设置 %s: %w", key, err)
		}
		global++
	}
	report.Tables["global_settings"] = global
	return globalRows.Err()
}

// importAnnouncementsAndPolicies carries announcements and response inspection
// policies. The policy provider_code column is retired: its value is dropped.
func importAnnouncementsAndPolicies(ctx context.Context, source *sql.DB, tx *sql.Tx, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at
		FROM juhe_business.announcements`)
	if err != nil {
		return fmt.Errorf("读取旧公告: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, title, content, level, status, createdBy, createdAt, updatedAt string
		var updatedBy, publishedAt sql.NullString
		if err := rows.Scan(&id, &title, &content, &level, &status, &createdBy, &updatedBy, &publishedAt, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧公告: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.announcements
				(id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id, title, content, level, status, createdBy, updatedBy, publishedAt, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入公告 %s: %w", id, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧公告: %w", err)
	}
	report.Tables["announcements"] = count

	policyRows, err := source.QueryContext(ctx, `
		SELECT id, name, enabled, priority, scope_type, protocol_code, match_json, action, notes, created_at, updated_at
		FROM juhe_business.response_inspection_policies`)
	if err != nil {
		return fmt.Errorf("读取旧响应检查策略: %w", err)
	}
	defer policyRows.Close()
	policies := 0
	for policyRows.Next() {
		var id, name, scopeType, protocol, match, action, createdAt, updatedAt string
		var enabled, priority int
		var notes sql.NullString
		if err := policyRows.Scan(&id, &name, &enabled, &priority, &scopeType, &protocol, &match, &action, &notes, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧响应检查策略: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.response_inspection_policies
				(id, name, enabled, priority, scope_type, protocol_code, match_json, action, notes, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			id, name, enabled, priority, scopeType, protocol, match, action, notes, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入响应检查策略 %s: %w", id, err)
		}
		policies++
	}
	report.Tables["response_inspection_policies"] = policies
	return policyRows.Err()
}

// importExternalSources carries the public-API integration sources and their
// tokens (hash plus encrypted secret copied verbatim).
func importExternalSources(ctx context.Context, source *sql.DB, tx *sql.Tx, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, name, status, scopes_json, rate_limits_json, expires_at, notes, last_used_at, created_at, updated_at
		FROM juhe_business.external_integration_sources`)
	if err != nil {
		return fmt.Errorf("读取旧外部来源: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, name, status, scopes, rateLimits, createdAt, updatedAt string
		var expiresAt, notes, lastUsed sql.NullString
		if err := rows.Scan(&id, &name, &status, &scopes, &rateLimits, &expiresAt, &notes, &lastUsed, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧外部来源: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.external_integration_sources
				(id, name, status, scopes_json, rate_limits_json, expires_at, notes, last_used_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (id) DO UPDATE SET
				name=EXCLUDED.name, status=EXCLUDED.status, scopes_json=EXCLUDED.scopes_json,
				rate_limits_json=EXCLUDED.rate_limits_json, expires_at=EXCLUDED.expires_at,
				notes=EXCLUDED.notes, updated_at=EXCLUDED.updated_at`,
			id, name, status, scopes, rateLimits, expiresAt, notes, lastUsed, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入外部来源 %s: %w", id, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧外部来源: %w", err)
	}
	report.Tables["external_integration_sources"] = count

	tokenRows, err := source.QueryContext(ctx, `
		SELECT id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix,
		       status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at
		FROM juhe_business.external_integration_source_tokens`)
	if err != nil {
		return fmt.Errorf("读取旧外部来源 Token: %w", err)
	}
	defer tokenRows.Close()
	tokens := 0
	for tokenRows.Next() {
		var id, sourceID, name, hash, secret, prefix, suffix, status, scopes, createdAt, updatedAt string
		var expiresAt, lastUsed, revokedAt sql.NullString
		if err := tokenRows.Scan(&id, &sourceID, &name, &hash, &secret, &prefix, &suffix, &status, &scopes,
			&expiresAt, &lastUsed, &createdAt, &updatedAt, &revokedAt); err != nil {
			return fmt.Errorf("扫描旧外部来源 Token: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.external_integration_source_tokens
				(id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix,
				 status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (id) DO UPDATE SET
				name=EXCLUDED.name, token_hash=EXCLUDED.token_hash,
				token_secret_encrypted=EXCLUDED.token_secret_encrypted, token_prefix=EXCLUDED.token_prefix,
				token_suffix=EXCLUDED.token_suffix, status=EXCLUDED.status, scopes_json=EXCLUDED.scopes_json,
				expires_at=EXCLUDED.expires_at, revoked_at=EXCLUDED.revoked_at, updated_at=EXCLUDED.updated_at`,
			id, sourceID, name, hash, secret, prefix, suffix, status, scopes, expiresAt, lastUsed,
			createdAt, updatedAt, revokedAt); err != nil {
			return fmt.Errorf("写入外部来源 Token %s: %w", id, err)
		}
		tokens++
	}
	report.Tables["external_integration_source_tokens"] = tokens
	return tokenRows.Err()
}

// importQuotaWindows carries the hourly request-quota window configuration.
func importQuotaWindows(ctx context.Context, source *sql.DB, tx *sql.Tx, report *Report) error {
	rows, err := source.QueryContext(ctx, `SELECT window_hours, created_at, updated_at FROM juhe_business.request_quota_hourly_window_configs`)
	if err != nil {
		return fmt.Errorf("读取旧配额窗口: %w", err)
	}
	defer rows.Close()
	configs := 0
	for rows.Next() {
		var hours int
		var createdAt, updatedAt string
		if err := rows.Scan(&hours, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧配额窗口: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.request_quota_hourly_window_configs (window_hours, created_at, updated_at)
			VALUES ($1,$2,$3) ON CONFLICT (window_hours) DO NOTHING`, hours, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入配额窗口 %d: %w", hours, err)
		}
		configs++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧配额窗口: %w", err)
	}
	report.Tables["request_quota_hourly_window_configs"] = configs

	bindingRows, err := source.QueryContext(ctx, `
		SELECT system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
		FROM juhe_business.request_quota_hourly_window_scope_bindings`)
	if err != nil {
		return fmt.Errorf("读取旧配额窗口绑定: %w", err)
	}
	defer bindingRows.Close()
	bindings := 0
	for bindingRows.Next() {
		var owner, scopeType, scopeID, sourceType, sourceID, createdAt, updatedAt string
		var hours int
		if err := bindingRows.Scan(&owner, &scopeType, &scopeID, &sourceType, &sourceID, &hours, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧配额窗口绑定: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.request_quota_hourly_window_scope_bindings
				(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			owner, scopeType, scopeID, sourceType, sourceID, hours, createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入配额窗口绑定 %s: %w", scopeID, err)
		}
		bindings++
	}
	report.Tables["request_quota_hourly_window_scope_bindings"] = bindings
	return bindingRows.Err()
}

// helper kept for future table additions.
func requireOwner(owners map[string]struct{}, id string) error {
	if _, ok := owners[id]; !ok {
		return fmt.Errorf("引用不存在或未迁移的系统账户 %s", id)
	}
	return nil
}

var _ = errors.New
var _ = sort.Strings
