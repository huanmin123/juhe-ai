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

-- name: RevokeManagementSessionByTokenHash :exec
DELETE FROM juhe_business.system_sessions
WHERE token_hash = $1;

-- name: UpdateManagementCurrentUserProfile :one
WITH current_account AS (
  SELECT
    id,
    display_name,
    status,
    role,
    must_change_password
  FROM juhe_business.system_accounts
  WHERE id = sqlc.arg(system_account_id)::text
  FOR UPDATE
), updated_account AS (
  UPDATE juhe_business.system_accounts AS system_accounts
  SET
    display_name = sqlc.arg(display_name)::text,
    updated_at = sqlc.arg(updated_at)::timestamptz
  FROM current_account
  WHERE system_accounts.id = current_account.id
    AND current_account.status = 'active'
    AND (
      current_account.role IN ('super_admin', 'admin')
      OR current_account.must_change_password = false
    )
  RETURNING
    current_account.display_name AS previous_display_name,
    system_accounts.id,
    system_accounts.username,
    system_accounts.display_name,
    system_accounts.role,
    system_accounts.must_change_password
)
SELECT
  previous_display_name,
  id,
  username,
  display_name,
  role,
  must_change_password
FROM updated_account;
