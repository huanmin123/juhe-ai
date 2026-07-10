-- name: UpdateManagementSystemAccount :one
WITH locked_active_super_admins AS MATERIALIZED (
  SELECT id
  FROM juhe_business.system_accounts
  WHERE role = 'super_admin'
    AND status = 'active'
  ORDER BY id
  FOR UPDATE
), active_super_admin_guard AS MATERIALIZED (
  SELECT count(*) FILTER (WHERE id <> sqlc.arg(system_account_id)::text)::int AS other_active_super_admin_count
  FROM locked_active_super_admins
), current_account AS (
  SELECT
    system_accounts.id,
    system_accounts.username,
    system_accounts.display_name,
    system_accounts.description,
    system_accounts.role,
    system_accounts.status,
    system_accounts.password_hash,
    system_accounts.must_change_password,
    system_accounts.image_generation_enabled,
    system_accounts.last_login_at,
    system_accounts.created_at,
    system_accounts.updated_at,
    active_super_admin_guard.other_active_super_admin_count
  FROM active_super_admin_guard
  JOIN juhe_business.system_accounts AS system_accounts
    ON system_accounts.id = sqlc.arg(system_account_id)::text
  FOR UPDATE OF system_accounts
), next_account AS (
  SELECT
    current_account.id,
    current_account.username,
    current_account.display_name,
    current_account.description,
    current_account.role,
    current_account.status,
    current_account.password_hash,
    current_account.must_change_password,
    current_account.image_generation_enabled,
    current_account.last_login_at,
    current_account.created_at,
    current_account.updated_at,
    current_account.other_active_super_admin_count,
    CASE
      WHEN sqlc.arg(has_display_name)::boolean THEN sqlc.arg(display_name)::text
      ELSE current_account.display_name
    END AS next_display_name,
    CASE
      WHEN sqlc.arg(has_description)::boolean THEN sqlc.narg(description)::text
      ELSE current_account.description
    END AS next_description,
    CASE
      WHEN sqlc.arg(has_role)::boolean THEN sqlc.arg(role)::text
      ELSE current_account.role
    END AS next_role,
    CASE
      WHEN sqlc.arg(has_status)::boolean THEN sqlc.arg(status)::text
      ELSE current_account.status
    END AS next_status,
    CASE
      WHEN sqlc.arg(has_image_generation_enabled)::boolean THEN sqlc.arg(image_generation_enabled)::boolean
      ELSE current_account.image_generation_enabled
    END AS next_image_generation_enabled
  FROM current_account
), update_guard AS (
  SELECT
    next_account.id,
    next_account.username,
    next_account.display_name,
    next_account.description,
    next_account.role,
    next_account.status,
    next_account.password_hash,
    next_account.must_change_password,
    next_account.image_generation_enabled,
    next_account.last_login_at,
    next_account.created_at,
    next_account.updated_at,
    next_account.next_display_name,
    next_account.next_description,
    next_account.next_role,
    next_account.next_status,
    CASE
      WHEN next_account.next_role IN ('super_admin', 'admin') THEN false
      WHEN sqlc.arg(has_must_change_password)::boolean THEN sqlc.arg(must_change_password)::boolean
      ELSE next_account.must_change_password
    END AS next_must_change_password,
    next_account.next_image_generation_enabled,
    next_account.role = 'super_admin'
      AND (
        next_account.next_role <> 'super_admin'
        OR next_account.next_status <> 'active'
      )
      AND next_account.other_active_super_admin_count = 0 AS blocked_last_active_super_admin
  FROM next_account
), updated_account AS (
  UPDATE juhe_business.system_accounts AS system_accounts
  SET
    display_name = update_guard.next_display_name,
    description = update_guard.next_description,
    role = update_guard.next_role,
    status = update_guard.next_status,
    password_hash = CASE
      WHEN sqlc.arg(has_password)::boolean THEN sqlc.arg(password_hash)::text
      ELSE update_guard.password_hash
    END,
    must_change_password = update_guard.next_must_change_password,
    image_generation_enabled = update_guard.next_image_generation_enabled,
    updated_at = sqlc.arg(updated_at)::timestamptz
  FROM update_guard
  WHERE system_accounts.id = update_guard.id
    AND update_guard.blocked_last_active_super_admin = false
  RETURNING
    update_guard.id AS before_id,
    update_guard.username AS before_username,
    update_guard.display_name AS before_display_name,
    update_guard.description AS before_description,
    update_guard.role AS before_role,
    update_guard.status AS before_status,
    update_guard.must_change_password AS before_must_change_password,
    update_guard.image_generation_enabled AS before_image_generation_enabled,
    update_guard.last_login_at AS before_last_login_at,
    update_guard.created_at AS before_created_at,
    update_guard.updated_at AS before_updated_at,
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
    system_accounts.updated_at,
    false AS blocked_last_active_super_admin
), blocked_account AS (
  SELECT
    update_guard.id AS before_id,
    update_guard.username AS before_username,
    update_guard.display_name AS before_display_name,
    update_guard.description AS before_description,
    update_guard.role AS before_role,
    update_guard.status AS before_status,
    update_guard.must_change_password AS before_must_change_password,
    update_guard.image_generation_enabled AS before_image_generation_enabled,
    update_guard.last_login_at AS before_last_login_at,
    update_guard.created_at AS before_created_at,
    update_guard.updated_at AS before_updated_at,
    update_guard.id,
    update_guard.username,
    update_guard.display_name,
    update_guard.description,
    update_guard.role,
    update_guard.status,
    update_guard.must_change_password,
    update_guard.image_generation_enabled,
    update_guard.last_login_at,
    update_guard.created_at,
    update_guard.updated_at,
    true AS blocked_last_active_super_admin
  FROM update_guard
  WHERE update_guard.blocked_last_active_super_admin = true
), revoked_sessions AS (
  DELETE FROM juhe_business.system_sessions
  WHERE system_account_id IN (SELECT id FROM updated_account)
    AND (
      sqlc.arg(has_password)::boolean
      OR (
        sqlc.arg(has_status)::boolean
        AND sqlc.arg(status)::text = 'disabled'
      )
    )
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
  (SELECT count(*)::int FROM revoked_sessions) AS revoked_session_count,
  updated_account.blocked_last_active_super_admin
FROM updated_account
UNION ALL
SELECT
  blocked_account.before_id,
  blocked_account.before_username,
  blocked_account.before_display_name,
  blocked_account.before_description,
  blocked_account.before_role,
  blocked_account.before_status,
  blocked_account.before_must_change_password,
  blocked_account.before_image_generation_enabled,
  blocked_account.before_last_login_at,
  blocked_account.before_created_at,
  blocked_account.before_updated_at,
  blocked_account.id,
  blocked_account.username,
  blocked_account.display_name,
  blocked_account.description,
  blocked_account.role,
  blocked_account.status,
  blocked_account.must_change_password,
  blocked_account.image_generation_enabled,
  blocked_account.last_login_at,
  blocked_account.created_at,
  blocked_account.updated_at,
  0::int AS revoked_session_count,
  blocked_account.blocked_last_active_super_admin
FROM blocked_account;
