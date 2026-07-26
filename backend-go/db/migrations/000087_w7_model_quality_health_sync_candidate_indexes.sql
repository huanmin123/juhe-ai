-- +goose Up
-- The canonical due queue must be index ordered. Malformed timestamp facts are
-- kept out of that queue and receive their own one-shot bounded quarantine
-- index so a large corrupt backlog cannot turn each claim page into a scan.
DROP INDEX IF EXISTS juhe_dataset.idx_model_check_runs_quality_health_sync_due;

CREATE INDEX idx_model_check_runs_quality_health_sync_due
  ON juhe_dataset.model_check_runs (
    COALESCE(quality_health_sync_next_attempt_at, updated_at),
    updated_at,
    id
  )
  WHERE account_id IS NOT NULL
    AND status = 'completed'
    AND quality_health_sync_status = 'failed'
    AND updated_at ~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$'
    AND (
      quality_health_sync_next_attempt_at IS NULL
      OR quality_health_sync_next_attempt_at ~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$'
    )
    AND (
      quality_health_sync_claim_until IS NULL
      OR quality_health_sync_claim_until ~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$'
    )
    AND quality_health_sync_claim_epoch < 9223372036854775807
    AND quality_health_sync_attempt_count < 9223372036854775807;

CREATE INDEX idx_model_check_runs_quality_health_sync_invalid_time
  ON juhe_dataset.model_check_runs (id)
  WHERE account_id IS NOT NULL
    AND status = 'completed'
    AND quality_health_sync_status = 'failed'
    AND (
      updated_at IS NULL
      OR updated_at !~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$'
      OR (
        quality_health_sync_next_attempt_at IS NOT NULL
        AND quality_health_sync_next_attempt_at !~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$'
      )
      OR (
        quality_health_sync_claim_until IS NOT NULL
        AND quality_health_sync_claim_until !~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9][.][0-9]{3}Z$'
      )
    )
    AND quality_health_sync_last_error_class IS DISTINCT FROM 'invalid_durable_timestamp'
    AND quality_health_sync_claim_epoch < 9223372036854775807
    AND quality_health_sync_attempt_count < 9223372036854775807;

-- +goose Down
-- Forward-only shared-schema safety fence. Reverting the binary must retain
-- the bounded queue indexes used by a concurrently deployed worker.
SELECT 1;
