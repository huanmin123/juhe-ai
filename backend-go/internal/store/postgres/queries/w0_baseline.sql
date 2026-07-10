-- name: ListBaselineSchemas :many
SELECT nspname::text AS schema_name
FROM pg_catalog.pg_namespace
WHERE nspname = ANY($1::text[])
ORDER BY nspname;

-- name: ListPublicGlobalSettings :many
SELECT key, value_json
FROM juhe_business.global_settings
WHERE key IN ('appName', 'appIcon')
ORDER BY key ASC;

-- name: ListSystemAPIRateLimitSettings :many
SELECT key, value_json
FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key IN (
    'systemApiRateLimitIpReadPerMinute',
    'systemApiRateLimitIpReadBurstPer10Seconds',
    'systemApiRateLimitIpWritePerMinute',
    'systemApiRateLimitIpWriteBurstPer10Seconds',
    'systemApiRateLimitUserReadPerMinute',
    'systemApiRateLimitUserWritePerMinute'
  )
ORDER BY key ASC;
