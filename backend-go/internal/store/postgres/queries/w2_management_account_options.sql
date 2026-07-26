-- name: ListManagementAccountOptions :many
WITH account_name_candidate_terms AS MATERIALIZED (
  SELECT
    search.account_id,
    search.system_account_id,
    search.term
  FROM juhe_business.account_name_search_terms AS search
  WHERE coalesce(array_length(sqlc.arg(keyword_terms)::text[], 1), 0) > 0
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR search.system_account_id = sqlc.arg(system_account_id)::text
    )
    AND search.term = ANY(sqlc.arg(keyword_terms)::text[])
),
account_name_contains_rows AS (
  SELECT account_name_candidate_terms.account_id
  FROM account_name_candidate_terms
  INNER JOIN juhe_business.account_name_search_documents AS documents
    ON documents.account_id = account_name_candidate_terms.account_id
    AND documents.system_account_id = account_name_candidate_terms.system_account_id
  WHERE position(sqlc.arg(keyword_normalized)::text in documents.normalized_name) > 0
  GROUP BY account_name_candidate_terms.account_id
  HAVING COUNT(DISTINCT account_name_candidate_terms.term) = sqlc.arg(keyword_term_count)::int
),
account_rows AS (
  SELECT
    accounts.id,
    accounts.system_account_id,
    COALESCE(NULLIF(system_accounts.display_name, ''), NULLIF(system_accounts.username, ''), accounts.system_account_id) AS system_account_name,
    accounts.system_account_id AS owner_system_account_id,
    COALESCE(NULLIF(system_accounts.display_name, ''), NULLIF(system_accounts.username, ''), accounts.system_account_id) AS owner_system_account_name,
    accounts.provider_code,
    accounts.provider_protocol_profile_id,
    accounts.protocol_code,
    accounts.protocol_version,
    accounts.name,
    accounts.type,
    (CASE
      WHEN accounts.last_error_code = 'account_expired'
        OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= now())
      THEN 'disabled'
      WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN accounts.status
      WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > now() THEN 'temporary_unavailable'
      WHEN accounts.schedulable = false THEN 'disabled'
      ELSE accounts.status
    END)::text AS effective_status,
    CASE
      WHEN accounts.status = 'active'
        AND accounts.schedulable = true
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= now())
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > now())
        AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')
      THEN true
      ELSE false
    END AS effective_schedulable,
    CASE
      WHEN accounts.status IN ('rate_limited', 'temporary_unavailable')
        OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > now())
      THEN true
      ELSE false
    END AS is_cooling,
    accounts.account_expires_at,
    accounts.priority,
    accounts.created_at,
    'owner'::text AS access_type,
    NULL::text AS account_authorization_id,
    NULL::text AS authorization_status,
    NULL::timestamptz AS authorization_expires_at,
    NULL::text AS authorization_instance_source_account_id,
    NULL::text AS authorization_instance_owner_system_account_id,
    false AS has_active_manual_authorization_source
  FROM juhe_business.accounts AS accounts
  LEFT JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = accounts.system_account_id
  WHERE accounts.deleted_at IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR accounts.system_account_id = sqlc.arg(system_account_id)::text
    )

  UNION ALL

  SELECT
    accounts.id,
    accounts.system_account_id,
    COALESCE(NULLIF(viewer_accounts.display_name, ''), NULLIF(viewer_accounts.username, ''), accounts.system_account_id) AS system_account_name,
    COALESCE(accounts.authorization_instance_owner_system_account_id, resource_authorizations.resource_owner_system_account_id, '') AS owner_system_account_id,
    COALESCE(NULLIF(owner_accounts.display_name, ''), NULLIF(owner_accounts.username, ''), accounts.authorization_instance_owner_system_account_id, resource_authorizations.resource_owner_system_account_id, '') AS owner_system_account_name,
    accounts.provider_code,
    accounts.provider_protocol_profile_id,
    accounts.protocol_code,
    accounts.protocol_version,
    accounts.name,
    accounts.type,
    (CASE
      WHEN option_group_bindings.group_id IS NULL
        OR option_group_bindings.account_authorization_id IS NULL
        OR option_group_bindings.account_authorization_id <> resource_authorizations.id
      THEN 'disabled'
      WHEN resource_authorizations.status <> 'active'
        OR (resource_authorizations.expires_at IS NOT NULL AND resource_authorizations.expires_at <= now())
      THEN 'disabled'
      WHEN source_accounts.id IS NULL THEN 'disabled'
      WHEN source_accounts.last_error_code = 'account_expired'
        OR (source_accounts.account_expires_at IS NOT NULL AND source_accounts.account_expires_at <= now())
      THEN 'disabled'
      WHEN source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN source_accounts.status
      WHEN source_accounts.cooldown_until IS NOT NULL AND source_accounts.cooldown_until > now() THEN 'temporary_unavailable'
      WHEN source_accounts.schedulable = false THEN 'disabled'
      WHEN accounts.last_error_code = 'account_expired'
        OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= now())
      THEN 'disabled'
      WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated') THEN accounts.status
      WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > now() THEN 'temporary_unavailable'
      WHEN accounts.schedulable = false THEN 'disabled'
      ELSE accounts.status
    END)::text AS effective_status,
    CASE
      WHEN option_group_bindings.group_id IS NOT NULL
        AND option_group_bindings.account_authorization_id IS NOT NULL
        AND option_group_bindings.account_authorization_id = resource_authorizations.id
        AND resource_authorizations.status = 'active'
        AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > now())
        AND source_accounts.id IS NOT NULL
        AND source_accounts.status = 'active'
        AND source_accounts.schedulable = true
        AND (source_accounts.cooldown_until IS NULL OR source_accounts.cooldown_until <= now())
        AND (source_accounts.account_expires_at IS NULL OR source_accounts.account_expires_at > now())
        AND (source_accounts.last_error_code IS NULL OR source_accounts.last_error_code <> 'account_expired')
        AND accounts.status = 'active'
        AND accounts.schedulable = true
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= now())
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > now())
        AND (accounts.last_error_code IS NULL OR accounts.last_error_code <> 'account_expired')
      THEN true
      ELSE false
    END AS effective_schedulable,
    CASE
      WHEN option_group_bindings.group_id IS NOT NULL
        AND option_group_bindings.account_authorization_id IS NOT NULL
        AND option_group_bindings.account_authorization_id = resource_authorizations.id
        AND resource_authorizations.status = 'active'
        AND (resource_authorizations.expires_at IS NULL OR resource_authorizations.expires_at > now())
        AND source_accounts.id IS NOT NULL
        AND NOT (
          source_accounts.schedulable = false
          OR source_accounts.status IN ('pending_test', 'disabled', 'error', 'quality_isolated')
          OR source_accounts.last_error_code = 'account_expired'
          OR (source_accounts.account_expires_at IS NOT NULL AND source_accounts.account_expires_at <= now())
        )
        AND NOT (
          accounts.schedulable = false
          OR accounts.status IN ('pending_test', 'disabled', 'error', 'quality_isolated')
          OR accounts.last_error_code = 'account_expired'
          OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= now())
        )
        AND (
          source_accounts.status IN ('rate_limited', 'temporary_unavailable')
          OR (source_accounts.cooldown_until IS NOT NULL AND source_accounts.cooldown_until > now())
          OR accounts.status IN ('rate_limited', 'temporary_unavailable')
          OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > now())
        )
      THEN true
      ELSE false
    END AS is_cooling,
    accounts.account_expires_at,
    accounts.priority,
    accounts.created_at,
    'authorized'::text AS access_type,
    resource_authorizations.id AS account_authorization_id,
    resource_authorizations.status AS authorization_status,
    resource_authorizations.expires_at AS authorization_expires_at,
    accounts.authorization_instance_source_account_id,
    COALESCE(accounts.authorization_instance_owner_system_account_id, resource_authorizations.resource_owner_system_account_id) AS authorization_instance_owner_system_account_id,
    EXISTS (
      SELECT 1
      FROM juhe_business.resource_authorization_sources AS returnable_sources
      WHERE returnable_sources.authorization_id = resource_authorizations.id
        AND returnable_sources.source_type = 'manual'
        AND returnable_sources.status = 'active'
    ) AS has_active_manual_authorization_source
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.resource_authorizations AS resource_authorizations
    ON resource_authorizations.id = accounts.authorization_instance_authorization_id
  LEFT JOIN juhe_business.accounts AS source_accounts
    ON source_accounts.id = accounts.authorization_instance_source_account_id
    AND source_accounts.deleted_at IS NULL
  LEFT JOIN LATERAL (
    SELECT
      group_accounts.group_id,
      group_accounts.account_authorization_id
    FROM juhe_business.group_accounts AS group_accounts
    WHERE group_accounts.account_id = accounts.id
      AND group_accounts.system_account_id = sqlc.arg(system_account_id)::text
      AND group_accounts.enabled = true
      AND group_accounts.account_authorization_id = resource_authorizations.id
    ORDER BY group_accounts.created_at ASC, group_accounts.group_id ASC
    LIMIT 1
  ) AS option_group_bindings ON true
  LEFT JOIN juhe_business.system_accounts AS viewer_accounts
    ON viewer_accounts.id = accounts.system_account_id
  LEFT JOIN juhe_business.system_accounts AS owner_accounts
    ON owner_accounts.id = COALESCE(accounts.authorization_instance_owner_system_account_id, resource_authorizations.resource_owner_system_account_id)
  WHERE sqlc.arg(system_account_id)::text <> ''
    AND accounts.system_account_id = sqlc.arg(system_account_id)::text
    AND accounts.deleted_at IS NULL
    AND accounts.authorization_instance_authorization_id IS NOT NULL
    AND resource_authorizations.resource_type = 'account'
    AND resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
)
SELECT
  account_rows.id,
  account_rows.system_account_id,
  account_rows.system_account_name,
  account_rows.owner_system_account_id,
  account_rows.owner_system_account_name,
  account_rows.provider_code,
  account_rows.provider_protocol_profile_id,
  account_rows.protocol_code,
  account_rows.protocol_version,
  account_rows.name,
  account_rows.type,
  account_rows.effective_status AS status,
  account_rows.access_type,
  account_rows.account_authorization_id,
  account_rows.authorization_status,
  account_rows.authorization_expires_at,
  account_rows.authorization_instance_source_account_id,
  account_rows.authorization_instance_owner_system_account_id,
  account_rows.account_expires_at,
  account_rows.has_active_manual_authorization_source
FROM account_rows
WHERE true
  AND (
    coalesce(array_length(sqlc.arg(ids)::text[], 1), 0) = 0
    OR account_rows.id = ANY(sqlc.arg(ids)::text[])
  )
  AND (
    sqlc.arg(provider_code)::text = ''
    OR account_rows.provider_code = sqlc.arg(provider_code)::text
  )
  AND (
    sqlc.arg(group_id)::text = ''
    OR EXISTS (
      SELECT 1
      FROM juhe_business.group_accounts AS group_accounts
      WHERE group_accounts.account_id = account_rows.id
        AND group_accounts.system_account_id = account_rows.system_account_id
        AND group_accounts.group_id = sqlc.arg(group_id)::text
        AND group_accounts.enabled = true
        AND (
          (account_rows.access_type = 'owner' AND group_accounts.account_authorization_id IS NULL)
          OR (account_rows.access_type = 'authorized' AND group_accounts.account_authorization_id = account_rows.account_authorization_id)
        )
    )
  )
  AND (
    coalesce(array_length(sqlc.arg(tag_ids)::text[], 1), 0) = 0
    OR EXISTS (
      SELECT 1
      FROM juhe_business.account_tag_bindings AS option_tag_bindings
      WHERE option_tag_bindings.account_id = account_rows.id
        AND option_tag_bindings.system_account_id = account_rows.system_account_id
        AND option_tag_bindings.tag_id = ANY(sqlc.arg(tag_ids)::text[])
    )
  )
  AND (
    sqlc.arg(account_type)::text = ''
    OR account_rows.type = sqlc.arg(account_type)::text
  )
  AND (
    sqlc.arg(has_keyword)::boolean = false
    OR (
      account_rows.name COLLATE "C" >= sqlc.arg(keyword)::text
      AND account_rows.name COLLATE "C" < sqlc.arg(keyword_upper)::text
    )
    OR (
      coalesce(array_length(sqlc.arg(keyword_terms)::text[], 1), 0) > 0
      AND account_rows.id IN (
        SELECT account_name_contains_rows.account_id
        FROM account_name_contains_rows
      )
    )
  )
  AND (
    coalesce(array_length(sqlc.arg(statuses)::text[], 1), 0) = 0
    OR account_rows.effective_status = ANY(sqlc.arg(statuses)::text[])
  )
  AND (
    sqlc.arg(schedulable)::text = ''
    OR sqlc.arg(schedulable)::text = 'all'
    OR (sqlc.arg(schedulable)::text = 'enabled' AND account_rows.effective_schedulable = true)
    OR (sqlc.arg(schedulable)::text = 'disabled' AND account_rows.effective_schedulable = false AND account_rows.is_cooling = false)
    OR (sqlc.arg(schedulable)::text = 'cooling' AND account_rows.is_cooling = true)
  )
ORDER BY
  CASE WHEN account_rows.access_type = 'authorized' THEN 0 ELSE account_rows.priority END ASC,
  account_rows.created_at ASC,
  account_rows.id ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;
