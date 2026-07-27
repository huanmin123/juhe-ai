package postgres

const listCooldownAccountRetestCandidatesSQL = `
SELECT a.id, a.name, a.config_revision, a.dispatch_revision, a.cooldown_until, a.priority, a.created_at,
  a.cooldown_retest_observation_started_at, a.cooldown_retest_generation,
  CASE WHEN a.authorization_instance_authorization_id IS NOT NULL THEN source_accounts.config_revision END,
  a.system_account_id, ga.group_id,
  a.health_check_model, a.health_check_endpoint_mode
FROM juhe_business.accounts AS a
JOIN LATERAL (
  SELECT group_id
  FROM juhe_business.group_accounts
  WHERE account_id = a.id AND system_account_id = a.system_account_id AND enabled = true
    AND (a.authorization_instance_authorization_id IS NULL OR account_authorization_id = a.authorization_instance_authorization_id)
  ORDER BY updated_at DESC, group_id ASC
  LIMIT 1
) AS ga ON true
LEFT JOIN juhe_business.resource_authorizations AS ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = a.authorization_instance_source_account_id AND source_accounts.deleted_at IS NULL
WHERE a.deleted_at IS NULL
  AND a.type IN ('api_key', 'oauth', 'google_oauth')
  AND a.health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')
  AND a.status IN ('temporary_unavailable', 'rate_limited') AND a.schedulable = true
  AND a.cooldown_until IS NOT NULL AND a.cooldown_until <= $1
  AND a.cooldown_retest_observation_started_at IS NOT NULL
  AND a.cooldown_retest_generation IS NOT NULL AND btrim(a.cooldown_retest_generation) <> ''
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $1)
  AND (
    (a.authorization_instance_authorization_id IS NULL
      AND a.authorization_instance_source_account_id IS NULL
      AND a.authorization_instance_owner_system_account_id IS NULL)
    OR
    (a.authorization_instance_authorization_id IS NOT NULL
      AND a.authorization_instance_source_account_id IS NOT NULL
      AND a.authorization_instance_owner_system_account_id IS NOT NULL
      AND ra.id IS NOT NULL AND ra.resource_type = 'account'
      AND ra.resource_id = a.authorization_instance_source_account_id
      AND ra.resource_owner_system_account_id = source_accounts.system_account_id
      AND ra.grantee_system_account_id = a.system_account_id
      AND a.authorization_instance_owner_system_account_id = source_accounts.system_account_id
      AND ra.status = 'active' AND (ra.expires_at IS NULL OR ra.expires_at > $1)
      AND source_accounts.provider_code IS NOT NULL
      AND source_accounts.status = 'active' AND source_accounts.schedulable = true
      AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
      AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > $1)
      AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= $1))
  )
  AND ($2::timestamptz IS NULL OR (a.cooldown_until, a.priority, a.created_at, a.id) > ($2, $3, $4, $5))
ORDER BY a.cooldown_until ASC, a.priority ASC, a.created_at ASC, a.id ASC
LIMIT $6`

const findCooldownAccountRetestCandidateSQL = `
SELECT a.id, a.name, a.config_revision, a.dispatch_revision, a.cooldown_until, a.priority, a.created_at,
  a.cooldown_retest_observation_started_at, a.cooldown_retest_generation,
  CASE WHEN a.authorization_instance_authorization_id IS NOT NULL THEN source_accounts.config_revision END,
  a.system_account_id, ga.group_id,
  a.health_check_model, a.health_check_endpoint_mode
FROM juhe_business.accounts AS a
JOIN LATERAL (
  SELECT group_id
  FROM juhe_business.group_accounts
  WHERE account_id = a.id AND system_account_id = a.system_account_id AND enabled = true
    AND (a.authorization_instance_authorization_id IS NULL OR account_authorization_id = a.authorization_instance_authorization_id)
  ORDER BY updated_at DESC, group_id ASC LIMIT 1
) AS ga ON true
LEFT JOIN juhe_business.resource_authorizations AS ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts AS source_accounts
  ON source_accounts.id = a.authorization_instance_source_account_id AND source_accounts.deleted_at IS NULL
WHERE a.id = $1 AND a.deleted_at IS NULL
  AND a.type IN ('api_key', 'oauth', 'google_oauth')
  AND a.health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')
  AND a.status IN ('temporary_unavailable', 'rate_limited') AND a.schedulable = true
  AND a.cooldown_until IS NOT NULL AND a.cooldown_until <= $2
  AND a.cooldown_retest_observation_started_at IS NOT NULL
  AND a.cooldown_retest_generation IS NOT NULL AND btrim(a.cooldown_retest_generation) <> ''
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $2)
  AND (
    (a.authorization_instance_authorization_id IS NULL
      AND a.authorization_instance_source_account_id IS NULL
      AND a.authorization_instance_owner_system_account_id IS NULL)
    OR
    (a.authorization_instance_authorization_id IS NOT NULL
      AND a.authorization_instance_source_account_id IS NOT NULL
      AND a.authorization_instance_owner_system_account_id IS NOT NULL
      AND ra.id IS NOT NULL AND ra.resource_type = 'account'
      AND ra.resource_id = a.authorization_instance_source_account_id
      AND ra.resource_owner_system_account_id = source_accounts.system_account_id
      AND ra.grantee_system_account_id = a.system_account_id
      AND a.authorization_instance_owner_system_account_id = source_accounts.system_account_id
      AND ra.status = 'active' AND (ra.expires_at IS NULL OR ra.expires_at > $2)
      AND source_accounts.provider_code IS NOT NULL
      AND source_accounts.status = 'active' AND source_accounts.schedulable = true
      AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
      AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > $2)
      AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= $2))
  )`
