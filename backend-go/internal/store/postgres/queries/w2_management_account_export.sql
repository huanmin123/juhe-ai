-- Account export reads are intentionally bounded by the service's import limit.
-- The Go store keeps the cursor in `after_id` and applies all owner/filter predicates
-- before fetching the next fixed batch. Credentials remain encrypted until the service.
SELECT
  accounts.id,
  accounts.name,
  accounts.provider_code,
  accounts.provider_protocol_profile_id,
  accounts.protocol_code,
  accounts.protocol_version,
  accounts.type,
  accounts.status,
  accounts.system_account_id,
  accounts.credentials_encrypted,
  COALESCE(group_binding.group_id, ''),
  COALESCE(group_binding.group_name, ''),
  COALESCE(accounts.proxy_profile_id, ''),
  COALESCE(proxy_profiles.name, ''),
  COALESCE(proxy_profiles.type, ''),
  COALESCE(proxy_profiles.host, ''),
  COALESCE(proxy_profiles.port, 0),
  COALESCE(proxy_profiles.username, ''),
  COALESCE(proxy_profiles.password_encrypted, ''),
  COALESCE(proxy_profiles.description, ''),
  COALESCE(proxy_profiles.enabled, false),
  accounts.concurrency_limit,
  accounts.priority,
  accounts.super_priority_enabled,
  accounts.fallback_enabled,
  accounts.schedulable,
  COALESCE(supported_models.models_json, '[]'),
  accounts.health_check_model,
  accounts.health_check_endpoint_mode,
  COALESCE(accounts.temporary_unavailable_continuous_probe_enabled, 1) = 1,
  COALESCE(model_mappings.mappings_json, '[]'),
  COALESCE(account_tags.tags_json, '[]'),
  COALESCE(to_jsonb(accounts.account_expires_at) #>> '{}', ''),
  COALESCE(accounts.availability_schedule_json, ''),
  COALESCE(accounts.notes, ''),
  COUNT(*) OVER() AS matched_count
FROM juhe_business.accounts AS accounts
LEFT JOIN LATERAL (
  SELECT group_accounts.group_id, groups.name AS group_name
  FROM juhe_business.group_accounts AS group_accounts
  INNER JOIN juhe_business.groups AS groups
    ON groups.id = group_accounts.group_id
   AND groups.system_account_id = group_accounts.system_account_id
  WHERE group_accounts.account_id = accounts.id
    AND group_accounts.system_account_id = accounts.system_account_id
    AND group_accounts.enabled = true
  ORDER BY group_accounts.updated_at DESC, group_accounts.group_id
  LIMIT 1
) AS group_binding ON true
LEFT JOIN juhe_business.proxy_profiles AS proxy_profiles
  ON proxy_profiles.id = accounts.proxy_profile_id
LEFT JOIN LATERAL (
  SELECT jsonb_agg(account_supported_models.model ORDER BY account_supported_models.model)::text AS models_json
  FROM juhe_business.account_supported_models AS account_supported_models
  WHERE account_supported_models.account_id = accounts.id
) AS supported_models ON true
LEFT JOIN LATERAL (
  SELECT jsonb_agg(jsonb_build_object(
    'sourceModel', account_model_mappings.source_model,
    'sourceEndpointFamily', account_model_mappings.source_endpoint_family,
    'upstreamModel', account_model_mappings.upstream_model,
    'upstreamEndpointFamily', account_model_mappings.upstream_endpoint_family,
    'enabled', account_model_mappings.enabled
  ) ORDER BY account_model_mappings.source_model, account_model_mappings.source_endpoint_family)::text AS mappings_json
  FROM juhe_business.account_model_mappings AS account_model_mappings
  WHERE account_model_mappings.account_id = accounts.id
) AS model_mappings ON true
LEFT JOIN LATERAL (
  SELECT jsonb_agg(account_tags.name ORDER BY account_tags.name, account_tags.id)::text AS tags_json
  FROM juhe_business.account_tag_bindings AS tag_bindings
  INNER JOIN juhe_business.account_tags AS account_tags ON account_tags.id = tag_bindings.tag_id
  WHERE tag_bindings.account_id = accounts.id
    AND tag_bindings.system_account_id = accounts.system_account_id
) AS account_tags ON true
WHERE accounts.deleted_at IS NULL
  AND accounts.authorization_instance_source_account_id IS NULL
  AND accounts.authorization_instance_authorization_id IS NULL
  AND accounts.authorization_instance_owner_system_account_id IS NULL
  AND accounts.authorization_instance_source_account_id IS NULL
  AND ($1 = '' OR accounts.system_account_id = $1)
  AND ($2::text[] IS NULL OR accounts.id = ANY($2::text[]))
  AND ($3 = '' OR accounts.name ILIKE '%' || $3 || '%' OR accounts.id ILIKE '%' || $3 || '%')
  AND ($4 = '' OR accounts.provider_code = $4)
  AND ($5 = '' OR EXISTS (
    SELECT 1 FROM juhe_business.group_accounts ga
    WHERE ga.account_id = accounts.id AND ga.system_account_id = accounts.system_account_id
      AND ga.group_id = $5 AND ga.enabled = true
  ))
  AND ($6::text[] IS NULL OR EXISTS (
    SELECT 1 FROM juhe_business.account_tag_bindings atb
    WHERE atb.account_id = accounts.id AND atb.system_account_id = accounts.system_account_id
      AND atb.tag_id = ANY($6::text[])
  ))
  AND ($7 = '' OR accounts.type = $7)
  AND ($8::text[] IS NULL OR accounts.status = ANY($8::text[]))
  AND ($9 = '' OR ($9 = 'enabled' AND accounts.schedulable = true)
    OR ($9 = 'disabled' AND accounts.schedulable = false)
    OR ($9 = 'cooling' AND accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > now()))
  AND ($10 = '' OR accounts.id > $10)
ORDER BY accounts.id
LIMIT $11;
