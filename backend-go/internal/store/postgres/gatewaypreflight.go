package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const gatewayPreflightAPIKeySQL = `
SELECT
  api_keys.id,
  api_keys.system_account_id,
  api_keys.status,
  api_keys.expires_at,
  api_keys.quota_limits_json,
  system_accounts.status AS system_account_status,
  system_accounts.image_generation_enabled,
  route_strategies.id AS route_strategy_id,
  route_strategies.status AS route_strategy_status,
  route_strategies.mode AS route_strategy_mode,
  route_strategies.config_json AS route_strategy_config_json
FROM juhe_business.api_keys AS api_keys
INNER JOIN juhe_business.system_accounts AS system_accounts
  ON system_accounts.id = api_keys.system_account_id
INNER JOIN juhe_business.route_strategies AS route_strategies
  ON route_strategies.id = api_keys.route_strategy_id
  AND route_strategies.system_account_id = api_keys.system_account_id
WHERE api_keys.key_hash = $1
LIMIT 1
`

const gatewayPreflightBindingsSQL = `
SELECT
  route_strategy_groups.id,
  $1::text AS api_key_id,
  route_strategy_groups.system_account_id,
  route_strategy_groups.group_id,
  route_strategy_groups.priority,
  route_strategy_groups.weight,
  route_strategy_groups.status,
  groups.provider_code,
  groups.enabled AS group_enabled,
  CASE
    WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN NULL
    ELSE group_authorization.expires_at
  END AS access_expires_at,
  route_strategy_groups.created_at
FROM juhe_business.route_strategies AS route_strategies
INNER JOIN juhe_business.route_strategy_groups AS route_strategy_groups
  ON route_strategy_groups.route_strategy_id = route_strategies.id
  AND route_strategy_groups.system_account_id = route_strategies.system_account_id
INNER JOIN juhe_business.groups AS groups
  ON groups.id = route_strategy_groups.group_id
LEFT JOIN juhe_business.resource_authorizations AS group_authorization
  ON group_authorization.resource_type = 'group'
  AND group_authorization.resource_id = groups.id
  AND group_authorization.resource_owner_system_account_id = groups.system_account_id
  AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
  AND group_authorization.status = 'active'
  AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > $2)
LEFT JOIN juhe_business.group_authorization_settings AS group_authorization_settings
  ON group_authorization_settings.authorization_id = group_authorization.id
  AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
  AND group_authorization_settings.group_id = groups.id
WHERE route_strategies.id = $3
  AND route_strategies.system_account_id = $4
  AND route_strategies.status = 'active'
  AND route_strategy_groups.status = 'active'
  AND groups.enabled = true
  AND (
    groups.system_account_id = route_strategy_groups.system_account_id
    OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, true) = true)
  )
ORDER BY route_strategy_groups.priority ASC, route_strategy_groups.created_at ASC, route_strategy_groups.id ASC
LIMIT $5
`

const gatewayPreflightSettingsSQL = `
SELECT key, value_json
FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key = ANY($1::text[])
ORDER BY key ASC
LIMIT $2
`

var gatewayPreflightSettingKeys = []string{
	"gatewayTextRawBodyLimitMegabytes",
	"defaultTemporaryUnschedulableMinutes",
	"temporaryUnschedulableRetryIntervalSeconds",
	"temporaryUnschedulableRetryAttempts",
	"textFirstResponseTimeoutSeconds",
	"textStreamIdleTimeoutSeconds",
	"textUncommittedAttemptMaxLifetimeSeconds",
	"imageFirstResponseTimeoutSeconds",
	"imageStreamIdleTimeoutSeconds",
	"imageUncommittedAttemptMaxLifetimeSeconds",
	"noAvailableAccountWaitTimeoutSeconds",
	"streamFailureThresholdCount",
	"streamFailureThresholdWindowMinutes",
}

func (s *Store) LoadGatewayPreflightAPIKey(ctx context.Context, keyHash string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	var row port.GatewayPreflightAPIKeyRecord
	var expiresAt pgtype.Timestamptz
	var quotaLimitsJSON pgtype.Text
	var routeConfigJSON pgtype.Text
	err := s.pool.QueryRow(ctx, gatewayPreflightAPIKeySQL, keyHash).Scan(
		&row.ID,
		&row.SystemAccountID,
		&row.APIKeyStatus,
		&expiresAt,
		&quotaLimitsJSON,
		&row.SystemAccountStatus,
		&row.SystemAccountImageGenerationEnabled,
		&row.RouteStrategyID,
		&row.RouteStrategyStatus,
		&row.RouteStrategyMode,
		&routeConfigJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.GatewayPreflightAPIKeyRecord{}, false, nil
	}
	if err != nil {
		return port.GatewayPreflightAPIKeyRecord{}, false, fmt.Errorf("load gateway preflight api key: %w", err)
	}
	if expiresAt.Valid {
		value := expiresAt.Time.UTC()
		row.ExpiresAt = &value
	}
	limits, err := managementAuthorizationLimitsFromJSON(quotaLimitsJSON)
	if err != nil {
		return port.GatewayPreflightAPIKeyRecord{}, false, fmt.Errorf("parse gateway preflight api key quota limits: %w", err)
	}
	row.QuotaLimits = limits
	if routeConfigJSON.Valid {
		value := routeConfigJSON.String
		row.RouteStrategyConfigJSON = &value
	}
	return row, true, nil
}

func (s *Store) ListGatewayPreflightBindings(
	ctx context.Context,
	apiKeyID string,
	routeStrategyID string,
	systemAccountID string,
	now time.Time,
	limit int,
) ([]port.GatewayPreflightBindingRecord, error) {
	if limit <= 0 {
		return []port.GatewayPreflightBindingRecord{}, nil
	}
	if limit > 20 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, gatewayPreflightBindingsSQL, apiKeyID, now.UTC(), routeStrategyID, systemAccountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list gateway preflight bindings: %w", err)
	}
	defer rows.Close()
	items := make([]port.GatewayPreflightBindingRecord, 0, limit)
	for rows.Next() {
		var item port.GatewayPreflightBindingRecord
		var accessExpiresAt pgtype.Timestamptz
		if err := rows.Scan(&item.ID, &item.APIKeyID, &item.SystemAccountID, &item.GroupID, &item.Priority, &item.Weight, &item.Status, &item.ProviderCode, &item.GroupEnabled, &accessExpiresAt, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan gateway preflight binding: %w", err)
		}
		if accessExpiresAt.Valid {
			value := accessExpiresAt.Time.UTC()
			item.AccessExpiresAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gateway preflight bindings: %w", err)
	}
	return items, nil
}

func (s *Store) LoadGatewayPreflightSettings(ctx context.Context) (port.GatewayPreflightSettingsRecord, error) {
	rows, err := s.pool.Query(ctx, gatewayPreflightSettingsSQL, gatewayPreflightSettingKeys, len(gatewayPreflightSettingKeys))
	if err != nil {
		return port.GatewayPreflightSettingsRecord{}, fmt.Errorf("load gateway preflight settings: %w", err)
	}
	defer rows.Close()
	values := make(map[string]string, len(gatewayPreflightSettingKeys))
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return port.GatewayPreflightSettingsRecord{}, fmt.Errorf("scan gateway preflight setting: %w", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return port.GatewayPreflightSettingsRecord{}, fmt.Errorf("iterate gateway preflight settings: %w", err)
	}
	return gatewayPreflightSettingsFromValues(values)
}

func gatewayPreflightSettingsFromValues(values map[string]string) (port.GatewayPreflightSettingsRecord, error) {
	read := func(key string, minValue int, maxValue int) (int, error) {
		raw, ok := values[key]
		if !ok {
			return 0, fmt.Errorf("系统设置缺少字段：%s", key)
		}
		return parseIntegerSettingValue(raw, key, minValue, maxValue)
	}
	var result port.GatewayPreflightSettingsRecord
	var err error
	if result.GatewayTextRawBodyLimitMegabytes, err = read("gatewayTextRawBodyLimitMegabytes", 1, 64); err != nil {
		return result, err
	}
	if result.DefaultTemporaryUnschedulableMinutes, err = read("defaultTemporaryUnschedulableMinutes", 1, 1440); err != nil {
		return result, err
	}
	if result.TemporaryUnschedulableRetryIntervalSeconds, err = read("temporaryUnschedulableRetryIntervalSeconds", 0, 3600); err != nil {
		return result, err
	}
	if result.TemporaryUnschedulableRetryAttempts, err = read("temporaryUnschedulableRetryAttempts", 0, 10); err != nil {
		return result, err
	}
	if result.TextFirstResponseTimeoutSeconds, err = read("textFirstResponseTimeoutSeconds", 10, 3600); err != nil {
		return result, err
	}
	if result.TextStreamIdleTimeoutSeconds, err = read("textStreamIdleTimeoutSeconds", 1, 3600); err != nil {
		return result, err
	}
	if result.TextUncommittedAttemptMaxLifetimeSeconds, err = read("textUncommittedAttemptMaxLifetimeSeconds", 60, 86400); err != nil {
		return result, err
	}
	if result.ImageFirstResponseTimeoutSeconds, err = read("imageFirstResponseTimeoutSeconds", 10, 3600); err != nil {
		return result, err
	}
	if result.ImageStreamIdleTimeoutSeconds, err = read("imageStreamIdleTimeoutSeconds", 1, 3600); err != nil {
		return result, err
	}
	if result.ImageUncommittedAttemptMaxLifetimeSeconds, err = read("imageUncommittedAttemptMaxLifetimeSeconds", 60, 86400); err != nil {
		return result, err
	}
	if result.NoAvailableAccountWaitTimeoutSeconds, err = read("noAvailableAccountWaitTimeoutSeconds", 10, 3600); err != nil {
		return result, err
	}
	if result.StreamFailureThresholdCount, err = read("streamFailureThresholdCount", 1, 100); err != nil {
		return result, err
	}
	if result.StreamFailureThresholdWindowMinutes, err = read("streamFailureThresholdWindowMinutes", 1, 1440); err != nil {
		return result, err
	}
	return result, nil
}

var _ port.GatewayPreflightReader = (*Store)(nil)
