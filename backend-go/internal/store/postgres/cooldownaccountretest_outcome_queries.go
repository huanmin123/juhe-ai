package postgres

const findCooldownAccountRetestOutcomeAuthorizationSQL = `
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
`

const lockCooldownAccountRetestOutcomeAccountsSQL = `
WITH target_identity AS (
  SELECT authorization_instance_source_account_id AS source_account_id
  FROM juhe_business.accounts
  WHERE id = $1::text
)
SELECT
  accounts.id,
  accounts.system_account_id,
  accounts.status,
  accounts.schedulable,
  accounts.account_expires_at,
  accounts.config_revision,
  accounts.dispatch_revision,
  accounts.cooldown_until,
  accounts.last_error_code,
  accounts.last_error_message,
  accounts.cooldown_retest_failure_count,
  accounts.cooldown_retest_observation_started_at,
  accounts.cooldown_retest_generation,
  accounts.temporary_unavailable_continuous_probe_enabled,
  accounts.authorization_instance_source_account_id,
  accounts.authorization_instance_authorization_id,
  accounts.authorization_instance_owner_system_account_id,
  accounts.provider_code IS NOT NULL,
  accounts.deleted_at IS NOT NULL
FROM juhe_business.accounts AS accounts
WHERE accounts.id = $1::text
   OR accounts.id = (SELECT source_account_id FROM target_identity)
   OR ($2::boolean
       AND accounts.authorization_instance_source_account_id = $1::text
       AND accounts.deleted_at IS NULL)
ORDER BY accounts.id ASC
LIMIT $3::integer
FOR UPDATE OF accounts`

const findCooldownAccountRetestOutcomeBindingSQL = `
SELECT group_accounts.group_id
FROM juhe_business.group_accounts AS group_accounts
WHERE group_accounts.account_id = $1::text
  AND group_accounts.system_account_id = $2::text
  AND group_accounts.enabled = true
  AND group_accounts.account_authorization_id IS NOT DISTINCT FROM NULLIF($3::text, '')
ORDER BY group_accounts.group_id ASC
LIMIT 1`

const selectCooldownAccountRetestOutcomeNowSQL = `SELECT clock_timestamp()`

const findCooldownAccountRetestOutcomeReplaySQL = `
SELECT event_id, event_type, account_id, account_runtime_key, transition_id, dispatch_revision
FROM juhe_business.account_circuit_outbox
WHERE projection_key = $1::text
  AND dedupe_key = $2::text
LIMIT 1`

const restoreCooldownAccountRetestOutcomeSQL = `
UPDATE juhe_business.accounts
SET status = 'active',
    schedulable = true,
    cooldown_until = NULL,
    last_error_code = NULL,
    last_error_message = NULL,
    last_error_trace_id = NULL,
    cooldown_retest_failure_count = 0,
    cooldown_retest_observation_started_at = NULL,
    cooldown_retest_generation = NULL,
    cooldown_retest_last_at = NULL,
    cooldown_retest_last_status_code = NULL,
    dispatch_revision = dispatch_revision + 1,
    updated_at = $2::timestamptz
WHERE id = $1::text
  AND deleted_at IS NULL
  AND status IN ('temporary_unavailable', 'rate_limited')
  AND schedulable = true
  AND config_revision = $3::integer
  AND dispatch_revision = $4::bigint
  AND cooldown_retest_observation_started_at = $5::timestamptz
  AND cooldown_retest_generation = $6::text
  AND (account_expires_at IS NULL OR account_expires_at > $2::timestamptz)
  AND EXISTS (
    SELECT 1
    FROM juhe_business.group_accounts AS group_accounts
    WHERE group_accounts.account_id = accounts.id
      AND group_accounts.system_account_id = $7::text
      AND group_accounts.enabled = true
      AND group_accounts.account_authorization_id IS NOT DISTINCT FROM NULLIF($8::text, '')
  )
  AND (
    ($8::text = ''
      AND accounts.authorization_instance_source_account_id IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND accounts.authorization_instance_owner_system_account_id IS NULL)
    OR
    ($8::text <> ''
      AND accounts.authorization_instance_source_account_id = $9::text
      AND accounts.authorization_instance_authorization_id = $8::text
      AND accounts.authorization_instance_owner_system_account_id = $10::text
      AND EXISTS (
        SELECT 1
        FROM juhe_business.resource_authorizations AS resource_authorizations
        WHERE resource_authorizations.id = $8::text
          AND resource_authorizations.resource_type = 'account'
          AND resource_authorizations.resource_id = $9::text
          AND resource_authorizations.resource_owner_system_account_id = $10::text
          AND resource_authorizations.grantee_system_account_id = $7::text
          AND resource_authorizations.status = 'active'
          AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $2::timestamptz)
      ))
  )
RETURNING dispatch_revision`

const deferCooldownAccountRetestOutcomeSQL = `
UPDATE juhe_business.accounts
SET cooldown_until = $2::timestamptz,
    dispatch_revision = dispatch_revision + 1,
    updated_at = $3::timestamptz
WHERE id = $1::text
  AND deleted_at IS NULL
  AND status IN ('temporary_unavailable', 'rate_limited')
  AND schedulable = true
  AND (cooldown_until IS NULL OR cooldown_until < $2::timestamptz)
  AND config_revision = $4::integer
  AND dispatch_revision = $5::bigint
  AND cooldown_retest_observation_started_at = $6::timestamptz
  AND cooldown_retest_generation = $7::text
  AND (account_expires_at IS NULL OR account_expires_at > $3::timestamptz)
  AND EXISTS (
    SELECT 1
    FROM juhe_business.group_accounts AS group_accounts
    WHERE group_accounts.account_id = accounts.id
      AND group_accounts.system_account_id = $8::text
      AND group_accounts.enabled = true
      AND group_accounts.account_authorization_id IS NOT DISTINCT FROM NULLIF($9::text, '')
  )
  AND (
    ($9::text = ''
      AND accounts.authorization_instance_source_account_id IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND accounts.authorization_instance_owner_system_account_id IS NULL)
    OR
    ($9::text <> ''
      AND accounts.authorization_instance_source_account_id = $10::text
      AND accounts.authorization_instance_authorization_id = $9::text
      AND accounts.authorization_instance_owner_system_account_id = $11::text
      AND EXISTS (
        SELECT 1
        FROM juhe_business.resource_authorizations AS resource_authorizations
        WHERE resource_authorizations.id = $9::text
          AND resource_authorizations.resource_type = 'account'
          AND resource_authorizations.resource_id = $10::text
          AND resource_authorizations.resource_owner_system_account_id = $11::text
          AND resource_authorizations.grantee_system_account_id = $8::text
          AND resource_authorizations.status = 'active'
          AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $3::timestamptz)
      ))
  )
RETURNING dispatch_revision`

const failCooldownAccountRetestOutcomeSQL = `
UPDATE juhe_business.accounts
SET status = CASE WHEN $2::boolean THEN 'error' ELSE status END,
    schedulable = CASE WHEN $2::boolean THEN false ELSE true END,
    cooldown_until = $3::timestamptz,
    last_error_code = $4::text,
    last_error_message = $5::text,
    last_error_trace_id = NULLIF($6::text, ''),
    cooldown_retest_failure_count = $7::integer,
    cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, $8::timestamptz),
    cooldown_retest_last_at = $9::timestamptz,
    cooldown_retest_last_status_code = $10::integer,
    stream_failure_count = 0,
    stream_failure_window_started_at = NULL,
    dispatch_revision = dispatch_revision + 1,
    updated_at = $9::timestamptz
WHERE id = $1::text
  AND deleted_at IS NULL
  AND status = $11::text
  AND schedulable = true
  AND config_revision = $12::integer
  AND dispatch_revision = $13::bigint
  AND cooldown_retest_observation_started_at = $14::timestamptz
  AND cooldown_retest_generation = $15::text
  AND (account_expires_at IS NULL OR account_expires_at > $9::timestamptz)
  AND EXISTS (
    SELECT 1
    FROM juhe_business.group_accounts AS group_accounts
    WHERE group_accounts.account_id = accounts.id
      AND group_accounts.system_account_id = $16::text
      AND group_accounts.enabled = true
      AND group_accounts.account_authorization_id IS NOT DISTINCT FROM NULLIF($17::text, '')
  )
  AND (
    ($17::text = ''
      AND accounts.authorization_instance_source_account_id IS NULL
      AND accounts.authorization_instance_authorization_id IS NULL
      AND accounts.authorization_instance_owner_system_account_id IS NULL)
    OR
    ($17::text <> ''
      AND accounts.authorization_instance_source_account_id = $18::text
      AND accounts.authorization_instance_authorization_id = $17::text
      AND accounts.authorization_instance_owner_system_account_id = $19::text
      AND EXISTS (
        SELECT 1
        FROM juhe_business.resource_authorizations AS resource_authorizations
        WHERE resource_authorizations.id = $17::text
          AND resource_authorizations.resource_type = 'account'
          AND resource_authorizations.resource_id = $18::text
          AND resource_authorizations.resource_owner_system_account_id = $19::text
          AND resource_authorizations.grantee_system_account_id = $16::text
          AND resource_authorizations.status = 'active'
          AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > $9::timestamptz)
      ))
  )
RETURNING dispatch_revision`

const advanceCooldownAccountRetestFamilySQL = `
WITH batch AS (
  SELECT
    items.account_id,
    items.expected_dispatch_revision,
    items.transition_id,
    items.event_id
  FROM jsonb_to_recordset($1::jsonb) AS items(
    account_id text,
    expected_dispatch_revision bigint,
    transition_id text,
    event_id text
  )
), updated AS (
  UPDATE juhe_business.accounts AS accounts
  SET dispatch_revision = accounts.dispatch_revision + 1,
      updated_at = $2::timestamptz
  FROM batch
  WHERE accounts.id = batch.account_id
    AND accounts.dispatch_revision = batch.expected_dispatch_revision
    AND accounts.deleted_at IS NULL
  RETURNING accounts.id, accounts.dispatch_revision, batch.transition_id, batch.event_id
), inserted AS (
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
  )
  SELECT
    updated.event_id,
    $4::text,
    'dispatch:' || updated.transition_id,
    'dispatch_revision_changed',
    updated.id,
    updated.id,
    updated.transition_id,
    updated.dispatch_revision,
    'pending',
    $3::bigint,
    0,
    $3::bigint,
    $3::bigint
  FROM updated
  RETURNING account_id
)
SELECT
  (SELECT count(*) FROM batch),
  (SELECT count(*) FROM updated),
  (SELECT count(*) FROM inserted)`

const insertCooldownAccountRetestOutcomeOutboxSQL = `
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
  $4::text,
  $5::text,
  $6::bigint,
  'pending',
  $7::bigint,
  0,
  $7::bigint,
  $7::bigint
)`

const markCooldownAccountRetestOutcomeStatsDirtySQL = `
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
ORDER BY group_accounts.group_id ASC
ON CONFLICT (group_id) DO UPDATE SET
  reason = EXCLUDED.reason,
  updated_at = EXCLUDED.updated_at`
