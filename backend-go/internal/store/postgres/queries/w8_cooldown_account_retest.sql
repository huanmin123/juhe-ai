-- First-pass scheduler shell. sqlc generation is intentionally deferred.
-- name: listCooldownAccountRetestCandidates :many
SELECT
  a.id, a.name, a.config_revision, a.cooldown_until, a.priority, a.created_at,
  a.cooldown_retest_observation_started_at, a.system_account_id,
  ga.group_id, a.health_check_model, a.health_check_endpoint_mode
FROM juhe_business.accounts AS a
JOIN LATERAL (
  SELECT group_id
  FROM juhe_business.group_accounts
  WHERE account_id = a.id AND system_account_id = a.system_account_id AND enabled = true
    AND (a.authorization_instance_authorization_id IS NULL OR account_authorization_id = a.authorization_instance_authorization_id)
  ORDER BY updated_at DESC, group_id ASC
  LIMIT 1
) AS ga ON true
LEFT JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = a.authorization_instance_authorization_id
WHERE a.deleted_at IS NULL
  AND a.type IN ('api_key', 'oauth', 'google_oauth')
  AND a.health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')
  AND a.status IN ('temporary_unavailable', 'rate_limited')
  AND a.schedulable = true
  AND a.cooldown_until IS NOT NULL AND a.cooldown_until <= $1
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $1)
  AND (a.authorization_instance_authorization_id IS NULL OR (ra.status = 'active' AND (ra.expires_at IS NULL OR ra.expires_at > $1)))
  AND ($2::timestamptz IS NULL OR (a.cooldown_until, a.priority, a.created_at, a.id) > ($2, $3, $4, $5))
ORDER BY a.cooldown_until, a.priority, a.created_at, a.id
LIMIT $6;

-- name: findCooldownAccountRetestCandidate :one
SELECT a.id, a.name, a.config_revision, a.cooldown_until, a.priority, a.created_at,
  a.cooldown_retest_observation_started_at, a.system_account_id, ga.group_id,
  a.health_check_model, a.health_check_endpoint_mode
FROM juhe_business.accounts AS a
JOIN LATERAL (
  SELECT group_id FROM juhe_business.group_accounts
  WHERE account_id = a.id AND system_account_id = a.system_account_id AND enabled = true
    AND (a.authorization_instance_authorization_id IS NULL OR account_authorization_id = a.authorization_instance_authorization_id)
  ORDER BY updated_at DESC, group_id ASC LIMIT 1
) AS ga ON true
LEFT JOIN juhe_business.resource_authorizations AS ra ON ra.id = a.authorization_instance_authorization_id
WHERE a.id = $1 AND a.deleted_at IS NULL
  AND a.type IN ('api_key', 'oauth', 'google_oauth')
  AND a.health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')
  AND a.status IN ('temporary_unavailable', 'rate_limited')
  AND a.schedulable = true AND a.cooldown_until IS NOT NULL AND a.cooldown_until <= $2
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $2)
  AND (a.authorization_instance_authorization_id IS NULL OR (ra.status = 'active' AND (ra.expires_at IS NULL OR ra.expires_at > $2)));
