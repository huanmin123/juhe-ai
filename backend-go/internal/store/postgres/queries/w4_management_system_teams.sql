-- name: CreateManagementSystemTeam :one
INSERT INTO juhe_business.system_teams (
  id, name, description, status, created_by, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text,
  sqlc.arg(name)::text,
  sqlc.narg(description)::text,
  sqlc.arg(status)::text,
  sqlc.arg(created_by)::text,
  sqlc.arg(created_at)::timestamptz,
  sqlc.arg(updated_at)::timestamptz
)
RETURNING
  id,
  name,
  description,
  status,
  created_by,
  created_at,
  updated_at;

-- name: FindManagementSystemTeamForUpdate :one
SELECT
  teams.id,
  teams.name,
  teams.description,
  teams.status,
  teams.created_by,
  teams.created_at,
  teams.updated_at
FROM juhe_business.system_teams AS teams
WHERE teams.id = sqlc.arg(team_id)::text
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR EXISTS (
      SELECT 1
      FROM juhe_business.system_team_members AS scoped_members
      WHERE scoped_members.team_id = teams.id
        AND scoped_members.system_account_id = sqlc.arg(system_account_id)::text
        AND scoped_members.status = 'active'
    )
  )
LIMIT 1
FOR UPDATE OF teams;

-- name: UpdateManagementSystemTeam :one
UPDATE juhe_business.system_teams
SET
  name = CASE WHEN sqlc.arg(has_name)::bool THEN sqlc.arg(name)::text ELSE name END,
  description = CASE
    WHEN sqlc.arg(has_description)::bool THEN sqlc.narg(description)::text
    ELSE description
  END,
  status = CASE WHEN sqlc.arg(has_status)::bool THEN sqlc.arg(status)::text ELSE status END,
  updated_at = sqlc.arg(updated_at)::timestamptz
WHERE id = sqlc.arg(team_id)::text
RETURNING
  id,
  name,
  description,
  status,
  created_by,
  created_at,
  updated_at;

-- name: ListManagementSystemTeams :many
SELECT
  teams.id,
  teams.name,
  teams.description,
  teams.status,
  teams.created_by,
  teams.created_at,
  teams.updated_at
FROM juhe_business.system_teams AS teams
WHERE (
    sqlc.arg(system_account_id)::text = ''
    OR EXISTS (
      SELECT 1
      FROM juhe_business.system_team_members AS scoped_members
      WHERE scoped_members.team_id = teams.id
        AND scoped_members.system_account_id = sqlc.arg(system_account_id)::text
        AND scoped_members.status = 'active'
    )
  )
  AND (
    sqlc.arg(keyword)::text = ''
    OR (
      teams.name COLLATE "C" >= sqlc.arg(keyword)::text
      AND teams.name COLLATE "C" < sqlc.arg(keyword_upper)::text
      AND starts_with(teams.name, sqlc.arg(keyword)::text)
    )
  )
ORDER BY teams.status ASC, teams.updated_at DESC, teams.name ASC, teams.id ASC
LIMIT sqlc.arg(row_limit)::int
OFFSET sqlc.arg(row_offset)::int;

-- name: ListManagementSystemTeamMemberCounts :many
SELECT
  team_id,
  COUNT(*) FILTER (WHERE status = 'active')::bigint AS active_member_count
FROM juhe_business.system_team_members
WHERE team_id = ANY(sqlc.arg(team_ids)::text[])
GROUP BY team_id
ORDER BY team_id ASC;

-- name: FindManagementSystemTeam :one
SELECT
  teams.id,
  teams.name,
  teams.description,
  teams.status,
  teams.created_by,
  teams.created_at,
  teams.updated_at,
  COALESCE(counts.active_member_count, 0)::bigint AS active_member_count
FROM juhe_business.system_teams AS teams
LEFT JOIN (
  SELECT
    team_id,
    COUNT(*) FILTER (WHERE status = 'active') AS active_member_count
  FROM juhe_business.system_team_members
  WHERE team_id = sqlc.arg(team_id)::text
  GROUP BY team_id
) AS counts ON counts.team_id = teams.id
WHERE teams.id = sqlc.arg(team_id)::text
  AND (
    sqlc.arg(system_account_id)::text = ''
    OR EXISTS (
      SELECT 1
      FROM juhe_business.system_team_members AS scoped_members
      WHERE scoped_members.team_id = teams.id
        AND scoped_members.system_account_id = sqlc.arg(system_account_id)::text
        AND scoped_members.status = 'active'
    )
  )
LIMIT 1;

-- name: ListManagementSystemTeamMembers :many
SELECT
  id,
  team_id,
  system_account_id,
  system_account_name,
  username,
  member_role,
  status,
  joined_at,
  removed_at,
  created_at,
  updated_at
FROM (
  SELECT
    members.id,
    members.team_id,
    members.system_account_id,
    accounts.display_name AS system_account_name,
    accounts.username,
    members.member_role,
    members.status,
    members.joined_at,
    members.removed_at,
    members.created_at,
    members.updated_at,
    ROW_NUMBER() OVER (
      PARTITION BY members.team_id
      ORDER BY members.status ASC, members.joined_at ASC, members.id ASC
    ) AS team_member_rank
  FROM juhe_business.system_team_members AS members
  INNER JOIN juhe_business.system_accounts AS accounts
    ON accounts.id = members.system_account_id
  WHERE members.team_id = sqlc.arg(team_id)::text
    AND members.status = 'active'
) AS ranked_team_members
WHERE team_member_rank <= 500
ORDER BY status ASC, joined_at ASC, id ASC;
