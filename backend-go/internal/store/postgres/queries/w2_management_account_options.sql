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
owner_account_rows AS (
  SELECT
    accounts.id,
    accounts.system_account_id,
    COALESCE(NULLIF(system_accounts.display_name, ''), NULLIF(system_accounts.username, ''), system_accounts.id) AS system_account_name,
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
      WHEN accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN accounts.status
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
    accounts.created_at
  FROM juhe_business.accounts AS accounts
  LEFT JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = accounts.system_account_id
  WHERE accounts.deleted_at IS NULL
    AND (
      sqlc.arg(system_account_id)::text = ''
      OR accounts.system_account_id = sqlc.arg(system_account_id)::text
    )
    AND (
      coalesce(array_length(sqlc.arg(ids)::text[], 1), 0) = 0
      OR accounts.id = ANY(sqlc.arg(ids)::text[])
    )
    AND (
      sqlc.arg(provider_code)::text = ''
      OR accounts.provider_code = sqlc.arg(provider_code)::text
    )
    AND (
      sqlc.arg(group_id)::text = ''
      OR EXISTS (
        SELECT 1
        FROM juhe_business.group_accounts AS group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = accounts.system_account_id
          AND group_accounts.group_id = sqlc.arg(group_id)::text
          AND group_accounts.enabled = true
      )
    )
    AND (
      coalesce(array_length(sqlc.arg(tag_ids)::text[], 1), 0) = 0
      OR EXISTS (
        SELECT 1
        FROM juhe_business.account_tag_bindings AS option_tag_bindings
        WHERE option_tag_bindings.account_id = accounts.id
          AND option_tag_bindings.system_account_id = accounts.system_account_id
          AND option_tag_bindings.tag_id = ANY(sqlc.arg(tag_ids)::text[])
      )
    )
    AND (
      sqlc.arg(account_type)::text = ''
      OR accounts.type = sqlc.arg(account_type)::text
    )
    AND (
      sqlc.arg(has_keyword)::boolean = false
      OR (
        accounts.name COLLATE "C" >= sqlc.arg(keyword)::text
        AND accounts.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      )
      OR (
        coalesce(array_length(sqlc.arg(keyword_terms)::text[], 1), 0) > 0
        AND accounts.id IN (
          SELECT account_name_contains_rows.account_id
          FROM account_name_contains_rows
        )
      )
    )
)
SELECT
  id,
  system_account_id,
  system_account_name,
  provider_code,
  provider_protocol_profile_id,
  protocol_code,
  protocol_version,
  name,
  type,
  effective_status AS status,
  account_expires_at
FROM owner_account_rows
WHERE (
    coalesce(array_length(sqlc.arg(statuses)::text[], 1), 0) = 0
    OR effective_status = ANY(sqlc.arg(statuses)::text[])
  )
  AND (
    sqlc.arg(schedulable)::text = ''
    OR sqlc.arg(schedulable)::text = 'all'
    OR (sqlc.arg(schedulable)::text = 'enabled' AND effective_schedulable = true)
    OR (sqlc.arg(schedulable)::text = 'disabled' AND effective_schedulable = false AND is_cooling = false)
    OR (sqlc.arg(schedulable)::text = 'cooling' AND is_cooling = true)
  )
ORDER BY priority ASC, created_at ASC, id ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;
