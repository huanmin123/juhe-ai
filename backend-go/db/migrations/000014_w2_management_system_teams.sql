-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.system_teams (
  id text PRIMARY KEY,
  name text NOT NULL,
  description text,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_business.system_team_members (
  id text PRIMARY KEY,
  team_id text NOT NULL REFERENCES juhe_business.system_teams(id) ON DELETE CASCADE,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  member_role text NOT NULL DEFAULT 'member',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'removed')),
  joined_at timestamptz NOT NULL,
  removed_at timestamptz,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique
  ON juhe_business.system_teams(name);
CREATE INDEX IF NOT EXISTS idx_system_teams_name_lookup
  ON juhe_business.system_teams(name, id);
CREATE INDEX IF NOT EXISTS idx_system_teams_name_c_lookup
  ON juhe_business.system_teams((name COLLATE "C"), id);
CREATE INDEX IF NOT EXISTS idx_system_teams_list_order
  ON juhe_business.system_teams(status, updated_at DESC, name ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_system_team_members_team
  ON juhe_business.system_team_members(team_id, status);
CREATE INDEX IF NOT EXISTS idx_system_team_members_team_status_joined
  ON juhe_business.system_team_members(team_id, status, joined_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_system_team_members_account
  ON juhe_business.system_team_members(system_account_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique
  ON juhe_business.system_team_members(team_id, system_account_id)
  WHERE status = 'active';

-- +goose Down
-- no-op: system teams are business data.
