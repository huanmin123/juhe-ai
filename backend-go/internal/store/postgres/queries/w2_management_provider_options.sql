-- name: ListManagementProviders :many
SELECT id, code, name, parent_code, description, enabled, default_supported_models_json
FROM juhe_business.providers
ORDER BY name ASC, code ASC
LIMIT 50;

-- name: ListManagementProviderOptionProviders :many
SELECT id, code, name, parent_code, description, enabled, default_supported_models_json
FROM juhe_business.providers
WHERE enabled = true
ORDER BY name ASC, code ASC
LIMIT 50;

-- name: ListManagementProviderOptionProfiles :many
SELECT
  id,
  provider_code,
  name,
  description,
  enabled,
  protocol_code,
  protocol_version,
  base_url,
  default_health_check_model,
  account_types_json,
  capabilities_json
FROM juhe_business.provider_protocol_profiles
WHERE provider_code = ANY(sqlc.arg(provider_codes)::text[])
ORDER BY provider_code ASC, updated_at DESC, id ASC
LIMIT 200;

-- name: ListManagementProviderOptionEndpointFamilies :many
SELECT
  ppf.profile_id,
  f.family_code,
  f.name,
  f.description
FROM juhe_business.provider_protocol_profile_families AS ppf
INNER JOIN juhe_business.provider_protocol_profiles AS profiles
  ON profiles.id = ppf.profile_id
INNER JOIN juhe_business.protocol_endpoint_families AS f
  ON f.protocol_code = profiles.protocol_code
  AND f.protocol_version = profiles.protocol_version
  AND f.family_code = ppf.family_code
WHERE ppf.profile_id = ANY(sqlc.arg(profile_ids)::text[])
  AND ppf.enabled = true
  AND f.enabled = true
ORDER BY ppf.profile_id ASC, f.family_code ASC;

-- name: ListManagementProviderDefaultHealthCheckModelPreferences :many
SELECT provider_code, model
FROM juhe_business.provider_default_health_check_models
WHERE system_account_id = sqlc.arg(system_account_id)
  AND sqlc.arg(system_account_id)::text <> ''
  AND provider_code = ANY(sqlc.arg(provider_codes)::text[])
ORDER BY provider_code ASC;

-- name: ListManagementProviderSystemDefaultHealthCheckModels :many
SELECT provider_code, model
FROM juhe_business.provider_system_default_health_check_models
WHERE provider_code = ANY(sqlc.arg(provider_codes)::text[])
ORDER BY provider_code ASC;

-- name: UpsertManagementProviderDefaultHealthCheckModelPreference :one
INSERT INTO juhe_business.provider_default_health_check_models (
  system_account_id, provider_code, model, created_at, updated_at
) VALUES (
  sqlc.arg(system_account_id), sqlc.arg(provider_code), sqlc.arg(model), now(), now()
)
ON CONFLICT (system_account_id, provider_code) DO UPDATE SET
  model = EXCLUDED.model,
  updated_at = EXCLUDED.updated_at
RETURNING provider_code, model;

-- name: UpsertManagementProviderSystemDefaultHealthCheckModel :one
INSERT INTO juhe_business.provider_system_default_health_check_models (
  provider_code, model, created_at, updated_at
) VALUES (
  sqlc.arg(provider_code), sqlc.arg(model), now(), now()
)
ON CONFLICT (provider_code) DO UPDATE SET
  model = EXCLUDED.model,
  updated_at = EXCLUDED.updated_at
RETURNING provider_code, model;
