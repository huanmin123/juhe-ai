package postgres

const managementAccountCreateSQL = `
WITH selected_profile AS (
  SELECT profiles.protocol_code, profiles.protocol_version, profiles.default_health_check_model
  FROM juhe_business.provider_protocol_profiles profiles
  JOIN juhe_business.providers providers ON providers.code = profiles.provider_code
  WHERE profiles.id = $3 AND profiles.provider_code = $2
    AND profiles.enabled = true AND providers.enabled = true
    AND profiles.account_types_json::jsonb ? $5
), selected_group AS (
  SELECT groups.id
  FROM juhe_business.groups groups
  WHERE groups.id = NULLIF($17, '') AND groups.system_account_id = $1
    AND groups.provider_code = $2 AND groups.enabled = true
)
INSERT INTO juhe_business.accounts (
  id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
  name, type, status, credentials_encrypted, credential_fingerprint, concurrency_limit, priority,
  super_priority_enabled, fallback_enabled, schedulable, availability_schedule_json, health_check_model,
  health_check_endpoint_mode, proxy_profile_id, account_expires_at, temporary_unavailable_continuous_probe_enabled,
  notes, created_at, updated_at
)
SELECT $4, $1, $2, $3, selected_profile.protocol_code, selected_profile.protocol_version,
  $6, $5, $7, $8, $9, $10, $11, $12, $13, $14, $15, COALESCE(NULLIF($16, ''), selected_profile.default_health_check_model),
  $18, NULLIF($19, ''), $20, $21, NULLIF($22, ''), $23, $24
FROM selected_profile
WHERE ($17 = '' OR EXISTS (SELECT 1 FROM selected_group))
RETURNING id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code,
  protocol_version, type, status, credential_fingerprint, concurrency_limit, priority,
  super_priority_enabled, fallback_enabled, schedulable, health_check_model, health_check_endpoint_mode,
  proxy_profile_id, account_expires_at, availability_schedule_json, notes, created_at, updated_at`

const managementAccountCreateSupportedModelSQL = `INSERT INTO juhe_business.account_supported_models (account_id, provider_code, model, created_at) VALUES ($1, $2, $3, $4)`
const managementAccountCreateGroupBindingSQL = `INSERT INTO juhe_business.group_accounts (system_account_id, group_id, account_id, local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, true, $7, $7)`
