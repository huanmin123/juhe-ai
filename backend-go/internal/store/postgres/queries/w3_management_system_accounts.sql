-- name: ResetManagementSystemAccountPassword :one
WITH current_account AS (
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
  WHERE id = sqlc.arg(system_account_id)::text
  FOR UPDATE
), updated_account AS (
  UPDATE juhe_business.system_accounts AS system_accounts
  SET
    password_hash = sqlc.arg(password_hash)::text,
    must_change_password = CASE
      WHEN current_account.role IN ('super_admin', 'admin') THEN false
      WHEN sqlc.arg(has_must_change_password)::boolean THEN sqlc.arg(must_change_password)::boolean
      ELSE current_account.must_change_password
    END,
    updated_at = sqlc.arg(updated_at)::timestamptz
  FROM current_account
  WHERE system_accounts.id = current_account.id
  RETURNING
    current_account.id AS before_id,
    current_account.username AS before_username,
    current_account.display_name AS before_display_name,
    current_account.description AS before_description,
    current_account.role AS before_role,
    current_account.status AS before_status,
    current_account.must_change_password AS before_must_change_password,
    current_account.image_generation_enabled AS before_image_generation_enabled,
    current_account.last_login_at AS before_last_login_at,
    current_account.created_at AS before_created_at,
    current_account.updated_at AS before_updated_at,
    system_accounts.id,
    system_accounts.username,
    system_accounts.display_name,
    system_accounts.description,
    system_accounts.role,
    system_accounts.status,
    system_accounts.must_change_password,
    system_accounts.image_generation_enabled,
    system_accounts.last_login_at,
    system_accounts.created_at,
    system_accounts.updated_at
), revoked_sessions AS (
  DELETE FROM juhe_business.system_sessions
  WHERE system_account_id IN (SELECT id FROM updated_account)
  RETURNING id
)
SELECT
  updated_account.before_id,
  updated_account.before_username,
  updated_account.before_display_name,
  updated_account.before_description,
  updated_account.before_role,
  updated_account.before_status,
  updated_account.before_must_change_password,
  updated_account.before_image_generation_enabled,
  updated_account.before_last_login_at,
  updated_account.before_created_at,
  updated_account.before_updated_at,
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
  (SELECT count(*)::int FROM revoked_sessions) AS revoked_session_count
FROM updated_account;
