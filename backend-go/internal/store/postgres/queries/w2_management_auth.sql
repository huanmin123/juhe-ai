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

-- name: FindManagementCurrentUserProfile :one
SELECT
  id,
  username,
  display_name,
  description,
  role,
  status,
  must_change_password,
  image_generation_enabled,
  last_login_at,
  created_at,
  updated_at
FROM juhe_business.system_accounts
WHERE id = $1
LIMIT 1;

-- name: TouchManagementSession :exec
UPDATE juhe_business.system_sessions
SET last_seen_at = sqlc.arg(last_seen_at)::timestamptz
WHERE id = sqlc.arg(session_id)::text
  AND last_seen_at < sqlc.arg(cutoff)::timestamptz;

-- name: FindManagementSystemAccountPasswordByUsername :one
SELECT
  id,
  username,
  status,
  password_hash
FROM juhe_business.system_accounts
WHERE lower(username) = lower(sqlc.arg(username)::text)
LIMIT 1;

-- name: CompleteManagementLogin :one
WITH updated_account AS (
  UPDATE juhe_business.system_accounts
  SET
    last_login_at = sqlc.arg(logged_in_at)::timestamptz,
    updated_at = sqlc.arg(logged_in_at)::timestamptz
  WHERE id = sqlc.arg(system_account_id)::text
    AND status = 'active'
    AND password_hash = sqlc.arg(verified_password_hash)::text
  RETURNING
    id,
    username,
    display_name,
    description,
    role,
    status,
    must_change_password,
    image_generation_enabled,
    last_login_at,
    created_at,
    updated_at
), inserted_session AS (
  INSERT INTO juhe_business.system_sessions (
    id,
    system_account_id,
    token_hash,
    expires_at,
    created_at,
    last_seen_at
  )
  SELECT
    sqlc.arg(session_id)::text,
    updated_account.id,
    sqlc.arg(token_hash)::text,
    sqlc.arg(expires_at)::timestamptz,
    sqlc.arg(logged_in_at)::timestamptz,
    sqlc.arg(logged_in_at)::timestamptz
  FROM updated_account
  RETURNING
    id AS session_id,
    expires_at AS session_expires_at
)
SELECT
  updated_account.id,
  updated_account.username,
  updated_account.display_name,
  updated_account.description,
  updated_account.role,
  updated_account.status,
  updated_account.must_change_password,
  updated_account.image_generation_enabled,
  updated_account.last_login_at,
  updated_account.created_at,
  updated_account.updated_at,
  inserted_session.session_id,
  inserted_session.session_expires_at
FROM updated_account
CROSS JOIN inserted_session;

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

-- name: UpdateManagementCurrentUserPassword :one
UPDATE juhe_business.system_accounts
SET
  password_hash = sqlc.arg(password_hash)::text,
  must_change_password = false,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(system_account_id)::text
  AND status = 'active'
RETURNING
  id,
  username,
  display_name,
  description,
  role,
  status,
  must_change_password,
  image_generation_enabled,
  last_login_at,
  created_at,
  updated_at;

-- name: RevokeOtherManagementSessionsForAccount :exec
DELETE FROM juhe_business.system_sessions
WHERE system_account_id = $1
  AND id <> $2;
