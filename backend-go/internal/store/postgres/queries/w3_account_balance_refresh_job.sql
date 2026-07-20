-- name: ListAccountBalanceRefreshRecoveryCandidates :many
SELECT id, system_account_id, config_revision, credentials_encrypted,
       balance_query_config_json::text, balance_query_next_refresh_at,
       updated_at, proxy_profile_id
FROM juhe_business.accounts
WHERE status = 'active'
  AND schedulable = 1
  AND type = 'api_key'
  AND balance_query_enabled = 1
  AND balance_query_next_refresh_at IS NULL
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL
ORDER BY id ASC
LIMIT $1;

-- name: ListAccountBalanceRefreshDueCandidates :many
SELECT id, system_account_id, config_revision, credentials_encrypted,
       balance_query_config_json::text, balance_query_next_refresh_at,
       updated_at, proxy_profile_id
FROM juhe_business.accounts
WHERE status = 'active'
  AND schedulable = 1
  AND type = 'api_key'
  AND balance_query_enabled = 1
  AND balance_query_next_refresh_at IS NOT NULL
  AND balance_query_next_refresh_at <= $1
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL
ORDER BY balance_query_next_refresh_at ASC, id ASC
LIMIT $2;
