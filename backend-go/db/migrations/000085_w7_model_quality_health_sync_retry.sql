-- +goose Up
-- Keep the current Node model-check run fact available when Goose initializes
-- a fresh PostgreSQL database first. The physical TEXT/INTEGER representation
-- intentionally matches the Node dataset owner; Go adapters parse and validate
-- stronger domain types at the persistence boundary.
CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_runs (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  actor_system_account_id text NOT NULL,
  provider_code text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  target_name text,
  target_owner_system_account_id text,
  account_id text,
  group_id text,
  api_key_id text,
  model text NOT NULL,
  profile text NOT NULL DEFAULT 'quick',
  trigger_kind text NOT NULL DEFAULT 'manual'
    CHECK (trigger_kind IN ('manual', 'scheduled', 'quality_recovery')),
  schedule_id text,
  trusted_comparison_enabled integer NOT NULL DEFAULT 0,
  trusted_comparison_available integer NOT NULL DEFAULT 0,
  level text NOT NULL DEFAULT 'unavailable',
  score integer NOT NULL DEFAULT 0,
  max_score integer NOT NULL DEFAULT 100,
  status text NOT NULL DEFAULT 'running',
  message text NOT NULL DEFAULT '',
  trace_id text,
  probe_set_version text NOT NULL DEFAULT 'openai-model-check-v1',
  started_at text NOT NULL,
  finished_at text,
  duration_ms integer,
  request_summary_json text NOT NULL DEFAULT '{}',
  result_summary_json text NOT NULL DEFAULT '{}',
  policy_snapshot_json text NOT NULL DEFAULT '{}',
  quality_decision_json text NOT NULL DEFAULT '{}',
  quality_health_sync_status text
    CHECK (
      quality_health_sync_status IS NULL
      OR quality_health_sync_status IN ('applied', 'pending_retry', 'failed')
    ),
  error_code text,
  error_message text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);

-- Node remains able to read and write the fact table during coexistence. Go
-- adds only its retry fencing state. Time values stay canonical TEXT so Node
-- and Go compare the same millisecond UTC representation. Do not add an
-- owner/token/until triple CHECK until the Node writer has been retired.
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_claim_owner text;
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_claim_token text;
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_claim_epoch bigint NOT NULL DEFAULT 0
    CHECK (quality_health_sync_claim_epoch >= 0);
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_claim_until text;
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_next_attempt_at text;
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_attempt_count bigint NOT NULL DEFAULT 0
    CHECK (quality_health_sync_attempt_count >= 0);
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_last_error_class text;
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_last_error_message text;
ALTER TABLE juhe_dataset.model_check_runs
  ADD COLUMN IF NOT EXISTS quality_health_sync_updated_at text;

CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_due
  ON juhe_dataset.model_check_runs (
    COALESCE(quality_health_sync_next_attempt_at, updated_at),
    updated_at,
    id
  )
  WHERE account_id IS NOT NULL
    AND status = 'completed'
    AND quality_health_sync_status = 'failed';

-- +goose Down
-- Forward-only shared-schema safety fence. Binary rollback must retain the
-- Node facts and Go retry fencing state; retirement requires a separately
-- reviewed forward migration after ownership has moved completely to Go.
SELECT 1;
