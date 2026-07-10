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

-- name: UpdateManagementSystemAccountStatus :one
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
), status_guard AS (
  SELECT
    current_account.id,
    current_account.username,
    current_account.display_name,
    current_account.description,
    current_account.role,
    current_account.status,
    current_account.must_change_password,
    current_account.image_generation_enabled,
    current_account.last_login_at,
    current_account.created_at,
    current_account.updated_at,
    current_account.role = 'super_admin'
      AND sqlc.arg(status)::text <> 'active'
      AND current_account.other_active_super_admin_count = 0 AS blocked_last_active_super_admin
  FROM current_account
), updated_account AS (
  UPDATE juhe_business.system_accounts AS system_accounts
  SET
    status = sqlc.arg(status)::text,
    updated_at = sqlc.arg(updated_at)::timestamptz
  FROM status_guard
  WHERE system_accounts.id = status_guard.id
    AND status_guard.blocked_last_active_super_admin = false
  RETURNING
    status_guard.id AS before_id,
    status_guard.username AS before_username,
    status_guard.display_name AS before_display_name,
    status_guard.description AS before_description,
    status_guard.role AS before_role,
    status_guard.status AS before_status,
    status_guard.must_change_password AS before_must_change_password,
    status_guard.image_generation_enabled AS before_image_generation_enabled,
    status_guard.last_login_at AS before_last_login_at,
    status_guard.created_at AS before_created_at,
    status_guard.updated_at AS before_updated_at,
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
    status_guard.id AS before_id,
    status_guard.username AS before_username,
    status_guard.display_name AS before_display_name,
    status_guard.description AS before_description,
    status_guard.role AS before_role,
    status_guard.status AS before_status,
    status_guard.must_change_password AS before_must_change_password,
    status_guard.image_generation_enabled AS before_image_generation_enabled,
    status_guard.last_login_at AS before_last_login_at,
    status_guard.created_at AS before_created_at,
    status_guard.updated_at AS before_updated_at,
    status_guard.id,
    status_guard.username,
    status_guard.display_name,
    status_guard.description,
    status_guard.role,
    status_guard.status,
    status_guard.must_change_password,
    status_guard.image_generation_enabled,
    status_guard.last_login_at,
    status_guard.created_at,
    status_guard.updated_at,
    true AS blocked_last_active_super_admin
  FROM status_guard
  WHERE status_guard.blocked_last_active_super_admin = true
), revoked_sessions AS (
  DELETE FROM juhe_business.system_sessions
  WHERE system_account_id IN (SELECT id FROM updated_account)
    AND sqlc.arg(status)::text = 'disabled'
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

-- name: UpdateManagementSystemAccountImageGeneration :one
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
    image_generation_enabled = sqlc.arg(image_generation_enabled)::boolean,
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
  updated_account.updated_at
FROM updated_account;

-- name: UpdateManagementSystemAccountProfile :one
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
), profile_guard AS (
  SELECT
    current_account.id,
    current_account.username,
    current_account.display_name,
    current_account.description,
    current_account.role,
    current_account.status,
    current_account.must_change_password,
    current_account.image_generation_enabled,
    current_account.last_login_at,
    current_account.created_at,
    current_account.updated_at,
    CASE
      WHEN sqlc.arg(has_role)::boolean THEN sqlc.arg(role)::text
      ELSE current_account.role
    END AS next_role,
    current_account.role = 'super_admin'
      AND current_account.status = 'active'
      AND CASE
        WHEN sqlc.arg(has_role)::boolean THEN sqlc.arg(role)::text
        ELSE current_account.role
      END <> 'super_admin'
      AND current_account.other_active_super_admin_count = 0 AS blocked_last_active_super_admin
  FROM current_account
), updated_account AS (
  UPDATE juhe_business.system_accounts AS system_accounts
  SET
    display_name = CASE
      WHEN sqlc.arg(has_display_name)::boolean THEN sqlc.arg(display_name)::text
      ELSE profile_guard.display_name
    END,
    description = CASE
      WHEN sqlc.arg(has_description)::boolean THEN sqlc.narg(description)::text
      ELSE profile_guard.description
    END,
    role = profile_guard.next_role,
    must_change_password = CASE
      WHEN profile_guard.next_role IN ('super_admin', 'admin') THEN false
      WHEN sqlc.arg(has_must_change_password)::boolean THEN sqlc.arg(must_change_password)::boolean
      ELSE profile_guard.must_change_password
    END,
    updated_at = sqlc.arg(updated_at)::timestamptz
  FROM profile_guard
  WHERE system_accounts.id = profile_guard.id
    AND profile_guard.blocked_last_active_super_admin = false
  RETURNING
    profile_guard.id AS before_id,
    profile_guard.username AS before_username,
    profile_guard.display_name AS before_display_name,
    profile_guard.description AS before_description,
    profile_guard.role AS before_role,
    profile_guard.status AS before_status,
    profile_guard.must_change_password AS before_must_change_password,
    profile_guard.image_generation_enabled AS before_image_generation_enabled,
    profile_guard.last_login_at AS before_last_login_at,
    profile_guard.created_at AS before_created_at,
    profile_guard.updated_at AS before_updated_at,
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
    profile_guard.id AS before_id,
    profile_guard.username AS before_username,
    profile_guard.display_name AS before_display_name,
    profile_guard.description AS before_description,
    profile_guard.role AS before_role,
    profile_guard.status AS before_status,
    profile_guard.must_change_password AS before_must_change_password,
    profile_guard.image_generation_enabled AS before_image_generation_enabled,
    profile_guard.last_login_at AS before_last_login_at,
    profile_guard.created_at AS before_created_at,
    profile_guard.updated_at AS before_updated_at,
    profile_guard.id,
    profile_guard.username,
    profile_guard.display_name,
    profile_guard.description,
    profile_guard.role,
    profile_guard.status,
    profile_guard.must_change_password,
    profile_guard.image_generation_enabled,
    profile_guard.last_login_at,
    profile_guard.created_at,
    profile_guard.updated_at,
    true AS blocked_last_active_super_admin
  FROM profile_guard
  WHERE profile_guard.blocked_last_active_super_admin = true
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
  blocked_account.blocked_last_active_super_admin
FROM blocked_account;
