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
