-- name: UpdateManagementAPIKey :one
WITH input_values AS (
  SELECT
    sqlc.arg(api_key_id)::text AS api_key_id,
    sqlc.arg(owner_system_account_id)::text AS owner_system_account_id
),
current_target AS MATERIALIZED (
  SELECT
    api_keys.id,
    api_keys.system_account_id,
    system_accounts.display_name AS system_account_name,
    api_keys.name,
    api_keys.description,
    api_keys.key_prefix,
    api_keys.key_suffix,
    api_keys.status,
    api_keys.is_default,
    api_keys.route_strategy_id,
    route_strategies.name AS route_strategy_name,
    route_strategies.mode AS route_strategy_mode,
    route_strategies.status AS route_strategy_status,
    api_keys.expires_at,
    api_keys.quota_limits_json,
    api_keys.availability_schedule_json
  FROM juhe_business.api_keys AS api_keys
  INNER JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = api_keys.system_account_id
  INNER JOIN juhe_business.route_strategies AS route_strategies
    ON route_strategies.id = api_keys.route_strategy_id
    AND route_strategies.system_account_id = api_keys.system_account_id
  CROSS JOIN input_values
  WHERE api_keys.id = input_values.api_key_id
    AND (
      input_values.owner_system_account_id = ''
      OR api_keys.system_account_id = input_values.owner_system_account_id
    )
  FOR UPDATE OF api_keys
),
changed_route_target AS MATERIALIZED (
  SELECT
    route_strategies.id,
    route_strategies.name,
    route_strategies.mode,
    route_strategies.status
  FROM juhe_business.route_strategies AS route_strategies
  CROSS JOIN current_target
  WHERE sqlc.arg(has_route_strategy_id)::boolean
    AND sqlc.arg(route_strategy_id)::text <> current_target.route_strategy_id
    AND route_strategies.id = sqlc.arg(route_strategy_id)::text
    AND route_strategies.system_account_id = current_target.system_account_id
  FOR UPDATE OF route_strategies
),
mutation_decision AS (
  SELECT
    current_target.*,
    (
      sqlc.arg(has_route_strategy_id)::boolean
      AND sqlc.arg(route_strategy_id)::text <> current_target.route_strategy_id
    ) AS route_changed,
    changed_route_target.id AS changed_route_strategy_id,
    changed_route_target.name AS changed_route_strategy_name,
    changed_route_target.mode AS changed_route_strategy_mode,
    changed_route_target.status AS changed_route_strategy_status
  FROM current_target
  LEFT JOIN changed_route_target ON true
),
updated_api_key AS (
  UPDATE juhe_business.api_keys AS api_keys
  SET
    name = CASE WHEN sqlc.arg(has_name)::boolean
      THEN sqlc.arg(name)::text ELSE mutation_decision.name END,
    description = CASE WHEN sqlc.arg(has_description)::boolean
      THEN sqlc.narg(description)::text ELSE mutation_decision.description END,
    route_strategy_id = CASE WHEN sqlc.arg(has_route_strategy_id)::boolean
      THEN sqlc.arg(route_strategy_id)::text ELSE mutation_decision.route_strategy_id END,
    status = CASE WHEN sqlc.arg(has_status)::boolean
      THEN sqlc.arg(status)::text ELSE mutation_decision.status END,
    expires_at = CASE WHEN sqlc.arg(has_expires_at)::boolean
      THEN sqlc.narg(expires_at)::timestamptz ELSE mutation_decision.expires_at END,
    quota_limits_json = CASE WHEN sqlc.arg(has_quota_limits)::boolean
      THEN sqlc.narg(quota_limits_json)::text ELSE mutation_decision.quota_limits_json END,
    availability_schedule_json = CASE WHEN sqlc.arg(has_availability_schedule)::boolean
      THEN sqlc.narg(availability_schedule_json)::text
      ELSE mutation_decision.availability_schedule_json END,
    availability_schedule_next_check_at = CASE
      WHEN sqlc.arg(has_availability_schedule)::boolean
        THEN sqlc.narg(availability_schedule_next_check_at)::timestamptz
      ELSE api_keys.availability_schedule_next_check_at
    END,
    updated_at = sqlc.arg(updated_at)::timestamptz
  FROM mutation_decision
  WHERE api_keys.id = mutation_decision.id
    AND api_keys.system_account_id = mutation_decision.system_account_id
    AND NOT (mutation_decision.is_default AND mutation_decision.route_changed)
    AND (
      NOT mutation_decision.route_changed
      OR (
        mutation_decision.changed_route_strategy_id IS NOT NULL
        AND mutation_decision.changed_route_strategy_status = 'active'
      )
    )
  RETURNING
    api_keys.id,
    api_keys.system_account_id,
    api_keys.name,
    api_keys.description,
    api_keys.key_prefix,
    api_keys.key_suffix,
    api_keys.status,
    api_keys.is_default,
    api_keys.route_strategy_id,
    api_keys.expires_at,
    api_keys.quota_limits_json,
    api_keys.availability_schedule_json
),
hourly_window_upsert AS (
  INSERT INTO juhe_business.request_quota_hourly_window_configs (
    window_hours,
    created_at,
    updated_at
  )
  SELECT
    sqlc.narg(hourly_hours)::integer,
    sqlc.arg(updated_at)::timestamptz,
    sqlc.arg(updated_at)::timestamptz
  FROM updated_api_key
  WHERE sqlc.arg(has_quota_limits)::boolean
    AND sqlc.narg(hourly_hours)::integer IS NOT NULL
  ON CONFLICT (window_hours) DO UPDATE
  SET updated_at = EXCLUDED.updated_at
  RETURNING window_hours
)
SELECT
  current_target.id AS before_api_key_id,
  current_target.system_account_id AS before_system_account_id,
  current_target.system_account_name AS before_system_account_name,
  current_target.name AS before_name,
  current_target.description AS before_description,
  current_target.key_prefix AS before_key_prefix,
  current_target.key_suffix AS before_key_suffix,
  current_target.status AS before_status,
  current_target.is_default AS before_is_default,
  current_target.route_strategy_id AS before_route_strategy_id,
  current_target.route_strategy_name AS before_route_strategy_name,
  current_target.route_strategy_mode AS before_route_strategy_mode,
  current_target.route_strategy_status AS before_route_strategy_status,
  current_target.expires_at AS before_expires_at,
  current_target.quota_limits_json AS before_quota_limits_json,
  current_target.availability_schedule_json AS before_availability_schedule_json,
  updated_api_key.id AS after_api_key_id,
  updated_api_key.system_account_id AS after_system_account_id,
  current_target.system_account_name AS after_system_account_name,
  updated_api_key.name AS after_name,
  updated_api_key.description AS after_description,
  updated_api_key.key_prefix AS after_key_prefix,
  updated_api_key.key_suffix AS after_key_suffix,
  updated_api_key.status AS after_status,
  updated_api_key.is_default AS after_is_default,
  updated_api_key.route_strategy_id AS after_route_strategy_id,
  coalesce(
    CASE WHEN mutation_decision.route_changed
      THEN mutation_decision.changed_route_strategy_name
      ELSE mutation_decision.route_strategy_name
    END,
    ''
  )::text AS after_route_strategy_name,
  coalesce(
    CASE WHEN mutation_decision.route_changed
      THEN mutation_decision.changed_route_strategy_mode
      ELSE mutation_decision.route_strategy_mode
    END,
    ''
  )::text AS after_route_strategy_mode,
  coalesce(
    CASE WHEN mutation_decision.route_changed
      THEN mutation_decision.changed_route_strategy_status
      ELSE mutation_decision.route_strategy_status
    END,
    ''
  )::text AS after_route_strategy_status,
  updated_api_key.expires_at AS after_expires_at,
  updated_api_key.quota_limits_json AS after_quota_limits_json,
  updated_api_key.availability_schedule_json AS after_availability_schedule_json,
  coalesce(mutation_decision.route_changed, false)::boolean AS route_changed,
  coalesce(mutation_decision.is_default AND mutation_decision.route_changed, false)::boolean
    AS default_route_change,
  coalesce(
    NOT mutation_decision.route_changed
      OR mutation_decision.changed_route_strategy_id IS NOT NULL,
    false
  )::boolean AS route_found,
  coalesce(
    NOT mutation_decision.route_changed
      OR mutation_decision.changed_route_strategy_status = 'active',
    false
  )::boolean AS route_active,
  hourly_upsert.hourly_upsert_count
FROM input_values
LEFT JOIN current_target ON true
LEFT JOIN mutation_decision ON true
LEFT JOIN updated_api_key ON true
CROSS JOIN LATERAL (
  SELECT COUNT(*)::int AS hourly_upsert_count
  FROM hourly_window_upsert
) AS hourly_upsert;
