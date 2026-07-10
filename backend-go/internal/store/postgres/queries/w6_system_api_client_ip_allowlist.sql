-- name: FindSystemAPIClientIPAllowlistPolicy :one
SELECT policies.id, policies.expires_at
FROM juhe_stats.client_ip_policies AS policies
INNER JOIN juhe_stats.client_ip_registry AS registry
  ON registry.ip_hash = policies.ip_hash
WHERE policies.ip_hash = sqlc.arg(ip_hash)::text
  AND policies.policy_type = 'allowlist'
  AND policies.status = 'active'
  AND (
    policies.expires_at IS NULL
    OR policies.expires_at > sqlc.arg(now_at)::text
  )
ORDER BY policies.created_at DESC, policies.id DESC
LIMIT 1;
