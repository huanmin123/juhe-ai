package postgres

const lockGatewayAccountPolicyRowsSQL = `
SELECT
  id,
  system_account_id,
  status,
  schedulable,
  account_expires_at,
  config_revision,
  dispatch_revision,
  authorization_instance_source_account_id,
  authorization_instance_authorization_id,
  authorization_instance_owner_system_account_id,
  deleted_at
FROM juhe_business.accounts
WHERE id = ANY($1::text[])
   OR ($2::text <> '' AND authorization_instance_source_account_id = $2::text AND deleted_at IS NULL)
ORDER BY id ASC
FOR UPDATE`

const findGatewayAccountPolicyOutboxSQL = `
SELECT event_id, event_type, account_id, account_runtime_key, transition_id, dispatch_revision
FROM juhe_business.account_circuit_outbox
WHERE projection_key = $1::text
  AND dedupe_key = $2::text
LIMIT 1`

const lockGatewayAccountPolicyBindingSQL = `
SELECT true
FROM juhe_business.group_accounts AS group_accounts
WHERE group_accounts.group_id = $1::text
  AND group_accounts.account_id = $2::text
  AND group_accounts.system_account_id = $3::text
  AND group_accounts.enabled = true
  AND group_accounts.account_authorization_id IS NOT DISTINCT FROM NULLIF($4::text, '')
LIMIT 1
FOR UPDATE OF group_accounts`

const lockGatewayAccountPolicyAuthorizationSQL = `
SELECT true
FROM juhe_business.resource_authorizations AS resource_authorizations
WHERE resource_authorizations.id = $1::text
  AND resource_authorizations.resource_type = 'account'
  AND resource_authorizations.resource_id = $2::text
  AND resource_authorizations.resource_owner_system_account_id = $3::text
  AND resource_authorizations.grantee_system_account_id = $4::text
  AND resource_authorizations.status = 'active'
  AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $5::timestamptz)
LIMIT 1
FOR UPDATE OF resource_authorizations`

const applyGatewayAccountPolicyCooldownSQL = `
UPDATE juhe_business.accounts
SET status = $2::text,
    schedulable = true,
    cooldown_until = $3::timestamptz,
    last_error_code = NULL,
    last_error_message = $4::text,
    last_error_trace_id = NULLIF($5::text, ''),
    cooldown_retest_failure_count = 0,
    cooldown_retest_observation_started_at = $6::timestamptz,
    cooldown_retest_last_at = NULL,
    cooldown_retest_last_status_code = NULL,
    stream_failure_count = 0,
    stream_failure_window_started_at = NULL,
    dispatch_revision = dispatch_revision + 1,
    updated_at = $6::timestamptz
WHERE id = $1::text
  AND status = $7::text
  AND schedulable = true
  AND config_revision = $8::integer
  AND dispatch_revision = $9::bigint
  AND deleted_at IS NULL
  AND (account_expires_at IS NULL OR account_expires_at > $6::timestamptz)
RETURNING dispatch_revision`

const applyGatewayAccountPolicyDisableSQL = `
UPDATE juhe_business.accounts
SET status = 'error',
    schedulable = false,
    cooldown_until = NULL,
    last_error_code = 'upstream_failure',
    last_error_message = $2::text,
    last_error_trace_id = NULL,
    cooldown_retest_failure_count = 0,
    cooldown_retest_observation_started_at = NULL,
    cooldown_retest_last_at = NULL,
    cooldown_retest_last_status_code = NULL,
    stream_failure_count = 0,
    stream_failure_window_started_at = NULL,
    dispatch_revision = dispatch_revision + 1,
    updated_at = $3::timestamptz
WHERE id = $1::text
  AND status = $4::text
  AND schedulable = true
  AND config_revision = $5::integer
  AND dispatch_revision = $6::bigint
  AND deleted_at IS NULL
  AND (account_expires_at IS NULL OR account_expires_at > $3::timestamptz)
RETURNING dispatch_revision`

const insertGatewayAccountPolicyOutboxSQL = `
INSERT INTO juhe_business.account_circuit_outbox (
  event_id,
  projection_key,
  dedupe_key,
  event_type,
  account_id,
  account_runtime_key,
  transition_id,
  dispatch_revision,
  status,
  available_at_ms,
  attempt_count,
  created_at_ms,
  updated_at_ms
) VALUES (
  $1::text,
  $2::text,
  $3::text,
  'dispatch_revision_changed',
  $4::text,
  $5::text,
  $6::text,
  $7::bigint,
  'pending',
  $8::bigint,
  0,
  $8::bigint,
  $8::bigint
)`

const advanceGatewayAccountPolicyFamilyRevisionSQL = `
UPDATE juhe_business.accounts
SET dispatch_revision = dispatch_revision + 1,
    updated_at = $3::timestamptz
WHERE id = $1::text
  AND dispatch_revision = $2::bigint
  AND deleted_at IS NULL
RETURNING dispatch_revision`

const markGatewayAccountPolicyStatsDirtySQL = `
INSERT INTO juhe_business.group_account_stats_dirty (group_id, reason, updated_at)
SELECT DISTINCT group_accounts.group_id, $2::text, $3::timestamptz
FROM juhe_business.group_accounts AS group_accounts
WHERE group_accounts.account_id = $1::text
   OR group_accounts.account_id IN (
     SELECT accounts.id
     FROM juhe_business.accounts AS accounts
     WHERE accounts.authorization_instance_source_account_id = $1::text
       AND accounts.deleted_at IS NULL
   )
ON CONFLICT (group_id) DO UPDATE SET
  reason = EXCLUDED.reason,
  updated_at = EXCLUDED.updated_at`
