-- name: LoadAccountBalanceAutoDetectCandidate :one
SELECT id, system_account_id, config_revision, credentials_encrypted,
       COALESCE(proxy_profile_id, '') AS proxy_profile_id
FROM juhe_business.accounts
WHERE id = sqlc.arg(account_id)::text
  AND config_revision = sqlc.arg(config_revision)::int
  AND status = 'active'
  AND schedulable = true
  AND type = 'api_key'
  AND balance_query_enabled = false
  AND balance_query_config_json = '{}'
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL
LIMIT 1;

-- name: EnableDetectedAccountBalanceConfig :execrows
UPDATE juhe_business.accounts
SET balance_query_enabled = true,
    balance_query_config_json = sqlc.arg(config_json)::text,
    balance_query_next_refresh_at = sqlc.arg(next_refresh_at)::timestamptz,
    updated_at = sqlc.arg(completed_at)::timestamptz
WHERE id = sqlc.arg(account_id)::text
  AND config_revision = sqlc.arg(expected_config_revision)::int
  AND status = 'active'
  AND schedulable = true
  AND type = 'api_key'
  AND balance_query_enabled = false
  AND balance_query_config_json = '{}'
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL;

-- name: UpsertDetectedAccountBalanceSnapshot :exec
INSERT INTO juhe_stats.account_usage_snapshots (
  system_account_id, account_id, kind, source, snapshot_json, refresh_status,
  last_attempt_at, last_success_at, next_refresh_after, last_error_message,
  updated_at, created_at
) VALUES (
  sqlc.arg(system_account_id)::text,
  sqlc.arg(account_id)::text,
  'relay_balance',
  'upstream_api',
  sqlc.arg(snapshot_json)::text,
  sqlc.arg(snapshot_status)::text,
  sqlc.arg(completed_at)::timestamptz,
  sqlc.arg(completed_at)::timestamptz,
  sqlc.arg(next_refresh_at)::timestamptz,
  NULL,
  sqlc.arg(completed_at)::timestamptz,
  sqlc.arg(completed_at)::timestamptz
)
ON CONFLICT (system_account_id, account_id, kind) DO UPDATE SET
  source = EXCLUDED.source,
  snapshot_json = EXCLUDED.snapshot_json,
  refresh_status = EXCLUDED.refresh_status,
  last_attempt_at = EXCLUDED.last_attempt_at,
  last_success_at = EXCLUDED.last_success_at,
  next_refresh_after = EXCLUDED.next_refresh_after,
  last_error_message = EXCLUDED.last_error_message,
  updated_at = EXCLUDED.updated_at;
