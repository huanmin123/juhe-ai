-- name: FindManagementSessionByTokenHash :one
SELECT
  ss.id,
  ss.token_hash,
  ss.expires_at,
  ss.last_seen_at,
  sa.id AS account_id,
  sa.username,
  sa.display_name,
  sa.role,
  sa.status,
  sa.must_change_password
FROM juhe_business.system_sessions AS ss
INNER JOIN juhe_business.system_accounts AS sa
  ON sa.id = ss.system_account_id
WHERE ss.token_hash = $1
LIMIT 1;
