-- name: SystemAPIClientIPAllowlisted :one
SELECT EXISTS (
  SELECT 1
  FROM juhe_stats.client_ip_policies AS policies
  WHERE policies.ip_hash = sqlc.arg(ip_hash)::text
    AND policies.policy_type = 'allowlist'
    AND policies.status = 'active'
    AND (
      policies.expires_at IS NULL
      OR policies.expires_at > sqlc.arg(now_at)::text
    )
  LIMIT 1
) AS allowlisted;
