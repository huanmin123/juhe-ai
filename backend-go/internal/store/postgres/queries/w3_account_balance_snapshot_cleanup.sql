-- name: DeleteAccountBalanceSnapshot :execrows
DELETE FROM juhe_stats.account_usage_snapshots
WHERE system_account_id = sqlc.arg(system_account_id)::text
  AND account_id = sqlc.arg(account_id)::text
  AND kind = 'relay_balance'
  AND updated_at <= sqlc.arg(updated_before)::timestamptz;
