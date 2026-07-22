-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS dispatch_revision bigint NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS circuit_projection_revision bigint NOT NULL DEFAULT 0;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'juhe_business.accounts'::regclass
      AND conname = 'accounts_dispatch_revision_nonnegative_check'
  ) THEN
    ALTER TABLE juhe_business.accounts
      ADD CONSTRAINT accounts_dispatch_revision_nonnegative_check CHECK (dispatch_revision >= 1);
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'juhe_business.accounts'::regclass
      AND conname = 'accounts_circuit_projection_revision_check'
  ) THEN
    ALTER TABLE juhe_business.accounts
      ADD CONSTRAINT accounts_circuit_projection_revision_check
      CHECK (circuit_projection_revision >= 0 AND circuit_projection_revision <= dispatch_revision);
  END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS juhe_business.account_circuit_incidents (
  circuit_scope_key text PRIMARY KEY,
  account_id text NOT NULL,
  account_runtime_key text NOT NULL,
  scope_kind text NOT NULL CHECK (scope_kind IN ('account', 'key', 'protocol_model')),
  key_fingerprint text,
  protocol_code text,
  request_lane text,
  model_family text,
  incident_id text NOT NULL,
  parent_incident_id text,
  child_incident_ids_json text NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(child_incident_ids_json::jsonb) = 'array'),
  caused_by_terminal_outcome_id text,
  state text NOT NULL CHECK (state IN ('CLOSED', 'SUSPECT', 'OPEN', 'HALF_OPEN', 'RECOVERING', 'PERSISTING', 'SHADOWED_BY_PERSISTENT')),
  failure_scope text CHECK (failure_scope IN ('account', 'key', 'protocol_model')),
  generation integer NOT NULL CHECK (generation >= 0),
  dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
  ledger_revision bigint NOT NULL CHECK (ledger_revision >= 1),
  projected_ledger_revision bigint NOT NULL DEFAULT 0
    CHECK (projected_ledger_revision >= 0 AND projected_ledger_revision <= ledger_revision),
  transition_id text NOT NULL,
  cooldown_observation_generation integer NOT NULL DEFAULT 0 CHECK (cooldown_observation_generation >= 0),
  open_until_ms bigint,
  next_transition_at_ms bigint,
  lease_id text,
  lease_purpose text CHECK (lease_purpose IN ('confirmation', 'half_open', 'recovery', 'cooldown_retest', 'background_probe')),
  lease_owner_run_id text,
  lease_until_ms bigint,
  attempt_started_at_ms bigint,
  attempt_hard_deadline_ms bigint,
  upstream_attempt_observed integer NOT NULL DEFAULT 0 CHECK (upstream_attempt_observed IN (0, 1)),
  backoff_level integer NOT NULL DEFAULT 0 CHECK (backoff_level >= 0),
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  recovering_successes integer NOT NULL DEFAULT 0 CHECK (recovering_successes >= 0),
  last_failure_class text CHECK (last_failure_class IN ('connect_failed', 'timeout_before_complete', 'read_interrupted', 'incomplete_response', 'explicit_policy')),
  retained_until_ms bigint,
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL,
  FOREIGN KEY (account_id) REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
  CHECK (length(circuit_scope_key) BETWEEN 1 AND 2048),
  CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
  CHECK (length(incident_id) BETWEEN 1 AND 256),
  CHECK (length(transition_id) BETWEEN 1 AND 256),
  CHECK ((scope_kind = 'account' AND key_fingerprint IS NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL)
    OR (scope_kind = 'key' AND key_fingerprint IS NOT NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL)
    OR (scope_kind = 'protocol_model' AND key_fingerprint IS NULL AND protocol_code IS NOT NULL AND request_lane IS NOT NULL AND model_family IS NOT NULL)),
  CHECK ((state = 'CLOSED' AND retained_until_ms IS NOT NULL) OR (state <> 'CLOSED' AND retained_until_ms IS NULL))
);

CREATE TABLE IF NOT EXISTS juhe_business.account_circuit_outbox (
  event_id text PRIMARY KEY,
  projection_key text NOT NULL,
  dedupe_key text NOT NULL,
  event_type text NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
  account_id text NOT NULL,
  account_runtime_key text NOT NULL,
  circuit_scope_key text,
  incident_id text,
  transition_id text NOT NULL,
  dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
  generation integer,
  ledger_revision bigint,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
  available_at_ms bigint NOT NULL,
  claim_token text,
  claimed_by text,
  claim_until_ms bigint,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  last_error_class text,
  acknowledged_at_ms bigint,
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL,
  FOREIGN KEY (account_id) REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
  UNIQUE (projection_key, dedupe_key),
  CHECK (length(event_id) BETWEEN 1 AND 256),
  CHECK (length(projection_key) BETWEEN 1 AND 128),
  CHECK (length(dedupe_key) BETWEEN 1 AND 256),
  CHECK (length(account_runtime_key) BETWEEN 1 AND 1024),
  CHECK (length(transition_id) BETWEEN 1 AND 256),
  CHECK (last_error_class IS NULL OR length(last_error_class) BETWEEN 1 AND 64),
  CHECK ((event_type = 'dispatch_revision_changed' AND circuit_scope_key IS NULL AND incident_id IS NULL AND generation IS NULL AND ledger_revision IS NULL)
    OR (event_type = 'incident_changed' AND circuit_scope_key IS NOT NULL AND incident_id IS NOT NULL AND generation IS NOT NULL AND ledger_revision IS NOT NULL)),
  CHECK ((status = 'pending' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NULL)
    OR (status = 'processing' AND claim_token IS NOT NULL AND claimed_by IS NOT NULL AND claim_until_ms IS NOT NULL AND acknowledged_at_ms IS NULL)
    OR (status = 'dispatched' AND claim_token IS NULL AND claimed_by IS NULL AND claim_until_ms IS NULL AND acknowledged_at_ms IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_account
  ON juhe_business.account_circuit_incidents(account_id, updated_at_ms, circuit_scope_key);
CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_runtime_state
  ON juhe_business.account_circuit_incidents(account_runtime_key, state, updated_at_ms, circuit_scope_key);
CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_projection_gap
  ON juhe_business.account_circuit_incidents(updated_at_ms, circuit_scope_key)
  WHERE projected_ledger_revision < ledger_revision;
CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_closed_cleanup
  ON juhe_business.account_circuit_incidents(retained_until_ms, updated_at_ms, circuit_scope_key)
  WHERE state = 'CLOSED';
CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_account
  ON juhe_business.account_circuit_outbox(account_id, dispatch_revision, created_at_ms, event_id);
CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_scope
  ON juhe_business.account_circuit_outbox(circuit_scope_key, ledger_revision, created_at_ms, event_id)
  WHERE circuit_scope_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_claim
  ON juhe_business.account_circuit_outbox(status, available_at_ms, claim_until_ms, created_at_ms, event_id)
  WHERE status IN ('pending', 'processing');
CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_ack_cleanup
  ON juhe_business.account_circuit_outbox(acknowledged_at_ms, event_id)
  WHERE status = 'dispatched';

-- +goose Down
-- The ledger is a rebuildable control fact, but removing it on rollback would
-- make an older runtime fail-open. Keep the tables/columns until the next
-- forward schema explicitly retires the projector contract.
SELECT 1;
