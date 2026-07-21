-- name: ListManagementAccounts :many
WITH visible_accounts AS (
  SELECT
    accounts.id, accounts.system_account_id, system_accounts.display_name AS system_account_name,
    accounts.name, accounts.provider_code, accounts.type, accounts.status, accounts.schedulable,
    accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
    accounts.account_expires_at, accounts.last_used_at, accounts.updated_at,
    'owner'::text AS access_type, NULL::text AS account_authorization_id,
    NULL::text AS authorization_status, NULL::timestamptz AS authorization_expires_at
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.system_accounts AS system_accounts ON system_accounts.id = accounts.system_account_id
  WHERE accounts.deleted_at IS NULL
    AND accounts.authorization_instance_source_account_id IS NULL
    AND accounts.authorization_instance_authorization_id IS NULL
    AND accounts.authorization_instance_owner_system_account_id IS NULL
    AND (sqlc.arg(system_account_id)::text = '' OR accounts.system_account_id = sqlc.arg(system_account_id)::text)

  UNION ALL

  SELECT
    accounts.id, accounts.system_account_id, grantee_accounts.display_name AS system_account_name,
    accounts.name, source_accounts.provider_code, source_accounts.type, accounts.status, accounts.schedulable,
    source_accounts.concurrency_limit, accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
    accounts.account_expires_at, accounts.last_used_at, accounts.updated_at,
    'authorized'::text AS access_type, resource_authorizations.id AS account_authorization_id,
    resource_authorizations.status AS authorization_status, resource_authorizations.expires_at AS authorization_expires_at
  FROM juhe_business.accounts AS accounts
  INNER JOIN juhe_business.accounts AS source_accounts
    ON source_accounts.id = accounts.authorization_instance_source_account_id AND source_accounts.deleted_at IS NULL
  INNER JOIN juhe_business.resource_authorizations AS resource_authorizations
    ON resource_authorizations.id = accounts.authorization_instance_authorization_id
    AND resource_authorizations.resource_type = 'account'
    AND resource_authorizations.resource_id = source_accounts.id
    AND resource_authorizations.grantee_system_account_id = accounts.system_account_id
    AND resource_authorizations.status IN ('active', 'paused', 'expired')
  INNER JOIN juhe_business.system_accounts AS grantee_accounts ON grantee_accounts.id = accounts.system_account_id
  WHERE accounts.deleted_at IS NULL
    AND sqlc.arg(system_account_id)::text <> ''
    AND accounts.system_account_id = sqlc.arg(system_account_id)::text
)
SELECT
  visible_accounts.id, visible_accounts.system_account_id, visible_accounts.system_account_name,
  visible_accounts.name, visible_accounts.provider_code, visible_accounts.type, visible_accounts.status,
  visible_accounts.schedulable, visible_accounts.concurrency_limit, visible_accounts.priority,
  visible_accounts.super_priority_enabled, visible_accounts.fallback_enabled,
  visible_accounts.account_expires_at, visible_accounts.last_used_at, visible_accounts.access_type,
  visible_accounts.account_authorization_id, visible_accounts.authorization_status, visible_accounts.authorization_expires_at,
  coalesce(usage_stats.request_count, 0)::bigint AS request_count,
  coalesce(usage_stats.input_tokens, 0)::bigint AS input_tokens,
  coalesce(usage_stats.output_tokens, 0)::bigint AS output_tokens,
  coalesce(usage_stats.total_cost_usd, 0)::double precision AS total_cost,
  CASE WHEN coalesce(usage_stats.request_count, 0) > 0
    THEN round(usage_stats.success_count::numeric * 1000000 / usage_stats.request_count)::bigint
    ELSE NULL::bigint
  END AS quality_score
FROM visible_accounts
LEFT JOIN juhe_stats.usage_stats_totals AS usage_stats
  ON usage_stats.system_account_id = visible_accounts.system_account_id
  AND usage_stats.scope_type = CASE WHEN visible_accounts.access_type = 'authorized' THEN 'account_authorization' ELSE 'account' END
  AND usage_stats.scope_id = coalesce(visible_accounts.account_authorization_id, visible_accounts.id)
WHERE (sqlc.arg(keyword)::text = '' OR visible_accounts.name ILIKE '%' || sqlc.arg(keyword)::text || '%')
  AND (sqlc.arg(provider_code)::text = '' OR visible_accounts.provider_code = sqlc.arg(provider_code)::text)
  AND (sqlc.arg(account_type)::text = '' OR visible_accounts.type = sqlc.arg(account_type)::text)
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR visible_accounts.status = ANY(sqlc.arg(statuses)::text[]))
  AND (
    cardinality(sqlc.arg(tag_ids)::text[]) = 0
    OR (
      SELECT count(DISTINCT tag_bindings.tag_id)
      FROM juhe_business.account_tag_bindings AS tag_bindings
      WHERE tag_bindings.account_id = visible_accounts.id
        AND tag_bindings.system_account_id = visible_accounts.system_account_id
        AND tag_bindings.tag_id = ANY(sqlc.arg(tag_ids)::text[])
    ) = cardinality(sqlc.arg(tag_ids)::text[])
  )
  AND (
    sqlc.arg(schedulable)::text = 'all'
    OR (sqlc.arg(schedulable)::text = 'enabled' AND visible_accounts.schedulable)
    OR (sqlc.arg(schedulable)::text IN ('disabled', 'cooling') AND NOT visible_accounts.schedulable)
  )
  AND (
    sqlc.arg(group_id)::text = ''
    OR EXISTS (
      SELECT 1 FROM juhe_business.group_accounts AS group_accounts
      WHERE group_accounts.system_account_id = visible_accounts.system_account_id
        AND group_accounts.account_id = visible_accounts.id
        AND group_accounts.group_id = sqlc.arg(group_id)::text
        AND group_accounts.enabled = true
    )
  )
ORDER BY
  CASE WHEN sqlc.arg(sort_field)::text = 'priority' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.priority END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'priority' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.priority END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'superPriority' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.super_priority_enabled END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'superPriority' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.super_priority_enabled END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'fallback' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.fallback_enabled END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'fallback' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.fallback_enabled END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'qualityScore' AND sqlc.arg(sort_order)::text = 'asc' THEN
    CASE WHEN coalesce(usage_stats.request_count, 0) > 0 THEN usage_stats.success_count::numeric / usage_stats.request_count ELSE NULL END
  END ASC NULLS LAST,
  CASE WHEN sqlc.arg(sort_field)::text = 'qualityScore' AND sqlc.arg(sort_order)::text = 'desc' THEN
    CASE WHEN coalesce(usage_stats.request_count, 0) > 0 THEN usage_stats.success_count::numeric / usage_stats.request_count ELSE NULL END
  END DESC NULLS LAST,
  CASE WHEN sqlc.arg(sort_field)::text = 'name' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.name END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'name' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.name END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'type' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.type END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'type' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.type END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'providerCode' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.provider_code END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'providerCode' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.provider_code END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'systemAccount' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.system_account_name END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'systemAccount' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.system_account_name END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'concurrency' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.concurrency_limit END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'concurrency' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.concurrency_limit END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'status' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.status END ASC,
  CASE WHEN sqlc.arg(sort_field)::text = 'status' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.status END DESC,
  CASE WHEN sqlc.arg(sort_field)::text = 'accountExpiresAt' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.account_expires_at END ASC NULLS LAST,
  CASE WHEN sqlc.arg(sort_field)::text = 'accountExpiresAt' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.account_expires_at END DESC NULLS LAST,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'asc' THEN visible_accounts.last_used_at END ASC NULLS LAST,
  CASE WHEN sqlc.arg(sort_field)::text = 'lastUsedAt' AND sqlc.arg(sort_order)::text = 'desc' THEN visible_accounts.last_used_at END DESC NULLS LAST,
  visible_accounts.updated_at DESC, visible_accounts.id DESC
LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int;
