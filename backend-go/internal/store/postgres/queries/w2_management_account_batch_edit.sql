-- First-pass account batch edit query contract. The executable SQL lives beside the store
-- implementation so the dynamic enabled-field set remains explicit and transaction-bound.
-- name: LoadManagementAccountBatchEditContext :many
SELECT id, system_account_id, name, provider_code, protocol_code, protocol_version,
       type, status, concurrency_limit, priority, super_priority_enabled,
       fallback_enabled, schedulable, health_check_model, health_check_endpoint_mode,
       account_expires_at, availability_schedule_json, notes, config_revision
FROM juhe_business.accounts
WHERE deleted_at IS NULL AND id = ANY($1::text[])
  AND ($2::text = '' OR system_account_id = $2)
ORDER BY id;
