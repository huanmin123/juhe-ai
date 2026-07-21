-- First-pass account balance contract. The Go runtime currently reads the existing
-- account and stats models; balance configuration columns and upstream adapters are
-- intentionally migrated in a later pass.
-- name: GetManagementAccountBalanceSnapshot :one
SELECT account_id, system_account_id, refresh_status, snapshot_json,
       COALESCE(next_refresh_after::text, ''), updated_at::text
FROM juhe_stats.account_usage_snapshots
WHERE account_id = sqlc.arg(account_id)::text
  AND kind = 'relay_balance'
  AND (sqlc.arg(system_account_id)::text = '' OR system_account_id = sqlc.arg(system_account_id)::text)
LIMIT 1;

-- name: GetManagementAccountBalanceCandidate :one
SELECT id, system_account_id, provider_code, protocol_code, protocol_version,
       type, credentials_encrypted
FROM juhe_business.accounts
WHERE id = sqlc.arg(account_id)::text
  AND (sqlc.arg(system_account_id)::text = '' OR system_account_id = sqlc.arg(system_account_id)::text)
  AND deleted_at IS NULL
  AND type = 'api_key'
LIMIT 1;
