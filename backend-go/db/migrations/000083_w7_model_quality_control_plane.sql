-- +goose Up
-- This migration intentionally matches the physical TEXT/INTEGER shape of the
-- current Node PostgreSQL owner. CREATE TABLE IF NOT EXISTS must be safe on an
-- already-initialized Node database; Go adapters retain strong domain types by
-- parsing and validating values at the persistence boundary. No SQLite writer
-- implementation or JSON decision payload is copied into the Go schema.
CREATE TABLE IF NOT EXISTS juhe_business.model_quality_policies (
  system_account_id text PRIMARY KEY
    REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  profile text NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
  manual_enforcement_enabled integer NOT NULL DEFAULT 1
    CHECK (manual_enforcement_enabled IN (0, 1)),
  penalty_threshold integer NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
  penalty_action text NOT NULL DEFAULT 'fallback'
    CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
  recovery_interval_minutes integer NOT NULL DEFAULT 10
    CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
  created_at text NOT NULL,
  updated_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_business.model_quality_schedules (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  account_id text NOT NULL,
  model text NOT NULL CHECK (btrim(model) <> ''),
  interval_minutes integer NOT NULL DEFAULT 60
    CHECK (interval_minutes BETWEEN 10 AND 10080),
  enabled integer NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  next_run_at text NOT NULL,
  last_run_id text,
  last_run_at text,
  last_run_status text
    CHECK (last_run_status IS NULL OR last_run_status IN ('completed', 'failed', 'canceled')),
  lease_owner text,
  lease_token text,
  lease_until text,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  UNIQUE (system_account_id, account_id),
  FOREIGN KEY (system_account_id)
    REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (account_id, system_account_id)
    REFERENCES juhe_business.accounts(id, system_account_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS juhe_business.account_quality_enforcements (
  account_id text PRIMARY KEY,
  system_account_id text NOT NULL,
  enforcement_id text NOT NULL UNIQUE CHECK (btrim(enforcement_id) <> ''),
  generation integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'cleared')),
  action text NOT NULL CHECK (action IN ('disable', 'fallback', 'quality_isolate')),
  trigger_run_id text NOT NULL CHECK (btrim(trigger_run_id) <> ''),
  policy_revision integer NOT NULL CHECK (policy_revision >= 0),
  account_config_revision integer NOT NULL CHECK (account_config_revision >= 1),
  before_status text NOT NULL CHECK (btrim(before_status) <> ''),
  after_status text NOT NULL CHECK (btrim(after_status) <> ''),
  fallback_was_enabled integer NOT NULL DEFAULT 0 CHECK (fallback_was_enabled IN (0, 1)),
  super_priority_was_enabled integer NOT NULL DEFAULT 0 CHECK (super_priority_was_enabled IN (0, 1)),
  started_at text NOT NULL,
  recovery_due_at text,
  recovery_lease_owner text,
  recovery_lease_token text,
  recovery_lease_until text,
  last_recovery_run_id text,
  cleared_at text,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  FOREIGN KEY (system_account_id)
    REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (account_id, system_account_id)
    REFERENCES juhe_business.accounts(id, system_account_id) ON DELETE CASCADE
);

-- Node can initialize a fresh database before Go, and its lease writers do not
-- populate fencing tokens. Add columns only: neither fresh nor existing tables
-- receive a token CHECK during coexistence. The Go store must require a
-- non-empty token in every claim/complete CAS; a later Node-removal migration
-- can validate the owner/token/until triple after all Node writers are gone.
ALTER TABLE juhe_business.model_quality_schedules
  ADD COLUMN IF NOT EXISTS lease_token text;
ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS recovery_lease_token text;

-- This is a retained, independent failure fact. It intentionally has no FK to
-- a run so model-check retention cannot erase a health outage retrospectively.
-- Its physical representation stays compatible with the Node stats owner.
CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_health_hourly (
  account_id text NOT NULL,
  system_account_id text NOT NULL,
  provider_code text NOT NULL CHECK (btrim(provider_code) <> ''),
  stat_hour text NOT NULL,
  observed_at text NOT NULL,
  model_check_run_id text NOT NULL CHECK (btrim(model_check_run_id) <> ''),
  model text NOT NULL CHECK (btrim(model) <> ''),
  profile text NOT NULL CHECK (profile IN ('quick', 'full')),
  score integer NOT NULL,
  threshold integer NOT NULL CHECK (threshold BETWEEN 40 AND 100),
  level text NOT NULL,
  error_code text,
  error_message text,
  updated_at text NOT NULL,
  PRIMARY KEY (account_id, stat_hour)
);

CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_due
  ON juhe_business.model_quality_schedules (enabled, next_run_at, id);
CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_scope
  ON juhe_business.model_quality_schedules (system_account_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_recovery
  ON juhe_business.account_quality_enforcements (state, action, recovery_due_at, account_id);
CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_scope
  ON juhe_business.account_quality_enforcements (system_account_id, updated_at DESC, account_id);
CREATE INDEX IF NOT EXISTS idx_account_quality_health_hourly_scope
  ON juhe_stats.account_quality_health_hourly (system_account_id, stat_hour, account_id);

-- +goose Down
-- This is a forward-only shared-schema migration. Its executable Down is a
-- deliberate safety fence: a binary rollback retains the facts that the still
-- running Node owner reads. Retirement requires a separately reviewed forward
-- migration after Node ownership has been removed.
SELECT 1;
