-- +goose Up
-- Node now persists the scheduled policy and the applied-enforcement policy as
-- durable snapshots. Add the columns nullable first so an upgraded version 85
-- database never substitutes column defaults for the policy that governed the
-- existing row.
ALTER TABLE juhe_business.model_quality_schedules
  ADD COLUMN IF NOT EXISTS profile text;
ALTER TABLE juhe_business.model_quality_schedules
  ADD COLUMN IF NOT EXISTS penalty_threshold integer;
ALTER TABLE juhe_business.model_quality_schedules
  ADD COLUMN IF NOT EXISTS penalty_action text;
ALTER TABLE juhe_business.model_quality_schedules
  ADD COLUMN IF NOT EXISTS recovery_interval_minutes integer;

ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS config_source text;
ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS config_source_id text;
ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS profile text;
ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS penalty_threshold integer;
ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS recovery_interval_minutes integer;
ALTER TABLE juhe_business.account_quality_enforcements
  ADD COLUMN IF NOT EXISTS recovery_model text;

-- A schedule used the system-account policy before schedules owned an explicit
-- policy snapshot. Preserve any already-populated valid values (for a partially
-- upgraded database), otherwise freeze the current policy. A missing policy is
-- the documented revision-zero default.
WITH resolved_schedule_policy AS (
  SELECT
    schedule.id,
    COALESCE(
      CASE WHEN schedule.profile IN ('quick', 'full') THEN schedule.profile END,
      CASE WHEN policy.profile IN ('quick', 'full') THEN policy.profile END,
      'quick'
    ) AS profile,
    COALESCE(
      CASE WHEN schedule.penalty_threshold BETWEEN 40 AND 100 THEN schedule.penalty_threshold END,
      CASE WHEN policy.penalty_threshold BETWEEN 40 AND 100 THEN policy.penalty_threshold END,
      70
    ) AS penalty_threshold,
    COALESCE(
      CASE
        WHEN schedule.penalty_action IN ('disable', 'fallback', 'quality_isolate')
          THEN schedule.penalty_action
      END,
      CASE
        WHEN policy.penalty_action IN ('disable', 'fallback', 'quality_isolate')
          THEN policy.penalty_action
      END,
      'fallback'
    ) AS penalty_action,
    COALESCE(
      CASE
        WHEN schedule.recovery_interval_minutes BETWEEN 10 AND 10080
          THEN schedule.recovery_interval_minutes
      END,
      CASE
        WHEN policy.recovery_interval_minutes BETWEEN 10 AND 10080
          THEN policy.recovery_interval_minutes
      END,
      10
    ) AS recovery_interval_minutes
  FROM juhe_business.model_quality_schedules AS schedule
  LEFT JOIN juhe_business.model_quality_policies AS policy
    ON policy.system_account_id = schedule.system_account_id
)
UPDATE juhe_business.model_quality_schedules AS schedule
SET
  profile = resolved.profile,
  penalty_threshold = resolved.penalty_threshold,
  penalty_action = resolved.penalty_action,
  recovery_interval_minutes = resolved.recovery_interval_minutes
FROM resolved_schedule_policy AS resolved
WHERE resolved.id = schedule.id;

-- policy_snapshot_json is produced by Node JSON serialization, but the column
-- historically had no database JSON constraint. Parse it through a temporary
-- migration helper so one malformed legacy row cannot abort the whole upgrade.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION juhe_business.migration_000086_try_jsonb(input_text text)
RETURNS jsonb
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $function$
BEGIN
  RETURN input_text::jsonb;
EXCEPTION WHEN others THEN
  RETURN NULL;
END;
$function$;
-- +goose StatementEnd

WITH run_facts AS (
  SELECT
    run.id,
    run.schedule_id,
    run.model,
    run.profile,
    juhe_business.migration_000086_try_jsonb(run.policy_snapshot_json) AS policy_snapshot
  FROM juhe_dataset.model_check_runs AS run
), enforcement_sources AS (
  SELECT
    enforcement.account_id,
    enforcement.config_source AS existing_config_source,
    NULLIF(BTRIM(enforcement.config_source_id), '') AS existing_config_source_id,
    enforcement.profile AS existing_profile,
    enforcement.penalty_threshold AS existing_penalty_threshold,
    enforcement.recovery_interval_minutes AS existing_recovery_interval_minutes,
    NULLIF(BTRIM(enforcement.recovery_model), '') AS existing_recovery_model,
    facts.schedule_id AS run_schedule_id,
    facts.model AS run_model,
    facts.profile AS run_profile,
    facts.policy_snapshot,
    schedule.id AS schedule_id,
    schedule.model AS schedule_model,
    schedule.profile AS schedule_profile,
    schedule.penalty_threshold AS schedule_penalty_threshold,
    schedule.recovery_interval_minutes AS schedule_recovery_interval_minutes,
    policy.profile AS policy_profile,
    policy.penalty_threshold AS policy_penalty_threshold,
    policy.recovery_interval_minutes AS policy_recovery_interval_minutes
  FROM juhe_business.account_quality_enforcements AS enforcement
  LEFT JOIN run_facts AS facts
    ON facts.id = enforcement.trigger_run_id
  LEFT JOIN juhe_business.model_quality_schedules AS schedule
    ON schedule.id = COALESCE(
      NULLIF(BTRIM(facts.policy_snapshot ->> 'scheduleId'), ''),
      NULLIF(BTRIM(facts.schedule_id), '')
    )
    AND schedule.system_account_id = enforcement.system_account_id
    AND schedule.account_id = enforcement.account_id
  LEFT JOIN juhe_business.model_quality_policies AS policy
    ON policy.system_account_id = enforcement.system_account_id
), enforcement_with_source AS (
  SELECT
    source.*,
    CASE
      WHEN source.existing_config_source IN ('manual', 'schedule')
        THEN source.existing_config_source
      WHEN (source.policy_snapshot ->> 'configSource') IN ('manual', 'schedule')
        THEN (source.policy_snapshot ->> 'configSource')
      WHEN COALESCE(
        NULLIF(BTRIM(source.policy_snapshot ->> 'scheduleId'), ''),
        NULLIF(BTRIM(source.run_schedule_id), '')
      ) IS NOT NULL
        THEN 'schedule'
      ELSE 'manual'
    END AS resolved_config_source,
    COALESCE(
      source.existing_config_source_id,
      NULLIF(BTRIM(source.policy_snapshot ->> 'scheduleId'), ''),
      NULLIF(BTRIM(source.run_schedule_id), '')
    ) AS resolved_config_source_id
  FROM enforcement_sources AS source
), resolved_enforcement AS (
  SELECT
    source.account_id,
    source.resolved_config_source AS config_source,
    CASE
      WHEN source.resolved_config_source = 'schedule' THEN source.resolved_config_source_id
      ELSE NULL
    END AS config_source_id,
    COALESCE(
      CASE WHEN source.existing_profile IN ('quick', 'full') THEN source.existing_profile END,
      CASE WHEN (source.policy_snapshot ->> 'profile') IN ('quick', 'full') THEN (source.policy_snapshot ->> 'profile') END,
      CASE WHEN source.run_profile IN ('quick', 'full') THEN source.run_profile END,
      CASE WHEN source.schedule_profile IN ('quick', 'full') THEN source.schedule_profile END,
      CASE WHEN source.policy_profile IN ('quick', 'full') THEN source.policy_profile END,
      'quick'
    ) AS profile,
    COALESCE(
      CASE
        WHEN source.existing_penalty_threshold BETWEEN 40 AND 100
          THEN source.existing_penalty_threshold
      END,
      CASE
        WHEN jsonb_typeof(source.policy_snapshot -> 'threshold') = 'number'
          AND (source.policy_snapshot ->> 'threshold') ~ '^[0-9]+$'
          THEN CASE
            WHEN (source.policy_snapshot ->> 'threshold')::numeric BETWEEN 40 AND 100
              THEN (source.policy_snapshot ->> 'threshold')::integer
          END
      END,
      CASE
        WHEN source.schedule_penalty_threshold BETWEEN 40 AND 100
          THEN source.schedule_penalty_threshold
      END,
      CASE
        WHEN source.policy_penalty_threshold BETWEEN 40 AND 100
          THEN source.policy_penalty_threshold
      END,
      70
    ) AS penalty_threshold,
    COALESCE(
      CASE
        WHEN source.existing_recovery_interval_minutes BETWEEN 10 AND 10080
          THEN source.existing_recovery_interval_minutes
      END,
      CASE
        WHEN jsonb_typeof(source.policy_snapshot -> 'recoveryIntervalMinutes') = 'number'
          AND (source.policy_snapshot ->> 'recoveryIntervalMinutes') ~ '^[0-9]+$'
          THEN CASE
            WHEN (source.policy_snapshot ->> 'recoveryIntervalMinutes')::numeric BETWEEN 10 AND 10080
              THEN (source.policy_snapshot ->> 'recoveryIntervalMinutes')::integer
          END
      END,
      CASE
        WHEN source.schedule_recovery_interval_minutes BETWEEN 10 AND 10080
          THEN source.schedule_recovery_interval_minutes
      END,
      CASE
        WHEN source.policy_recovery_interval_minutes BETWEEN 10 AND 10080
          THEN source.policy_recovery_interval_minutes
      END,
      10
    ) AS recovery_interval_minutes,
    COALESCE(
      source.existing_recovery_model,
      NULLIF(BTRIM(source.run_model), ''),
      NULLIF(BTRIM(source.schedule_model), '')
    ) AS recovery_model
  FROM enforcement_with_source AS source
)
UPDATE juhe_business.account_quality_enforcements AS enforcement
SET
  config_source = resolved.config_source,
  config_source_id = resolved.config_source_id,
  profile = resolved.profile,
  penalty_threshold = resolved.penalty_threshold,
  recovery_interval_minutes = resolved.recovery_interval_minutes,
  recovery_model = resolved.recovery_model
FROM resolved_enforcement AS resolved
WHERE resolved.account_id = enforcement.account_id;

DROP FUNCTION juhe_business.migration_000086_try_jsonb(text);

ALTER TABLE juhe_business.model_quality_schedules
  ALTER COLUMN profile SET DEFAULT 'quick',
  ALTER COLUMN profile SET NOT NULL,
  ALTER COLUMN penalty_threshold SET DEFAULT 70,
  ALTER COLUMN penalty_threshold SET NOT NULL,
  ALTER COLUMN penalty_action SET DEFAULT 'fallback',
  ALTER COLUMN penalty_action SET NOT NULL,
  ALTER COLUMN recovery_interval_minutes SET DEFAULT 10,
  ALTER COLUMN recovery_interval_minutes SET NOT NULL;

ALTER TABLE juhe_business.model_quality_schedules
  DROP CONSTRAINT IF EXISTS model_quality_schedules_profile_check,
  DROP CONSTRAINT IF EXISTS model_quality_schedules_penalty_threshold_check,
  DROP CONSTRAINT IF EXISTS model_quality_schedules_penalty_action_check,
  DROP CONSTRAINT IF EXISTS model_quality_schedules_recovery_interval_minutes_check;

ALTER TABLE juhe_business.model_quality_schedules
  ADD CONSTRAINT model_quality_schedules_profile_check
    CHECK (profile IN ('quick', 'full')),
  ADD CONSTRAINT model_quality_schedules_penalty_threshold_check
    CHECK (penalty_threshold BETWEEN 40 AND 100),
  ADD CONSTRAINT model_quality_schedules_penalty_action_check
    CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
  ADD CONSTRAINT model_quality_schedules_recovery_interval_minutes_check
    CHECK (recovery_interval_minutes BETWEEN 10 AND 10080);

ALTER TABLE juhe_business.account_quality_enforcements
  ALTER COLUMN config_source SET DEFAULT 'manual',
  ALTER COLUMN config_source SET NOT NULL,
  ALTER COLUMN profile SET DEFAULT 'quick',
  ALTER COLUMN profile SET NOT NULL,
  ALTER COLUMN penalty_threshold SET DEFAULT 70,
  ALTER COLUMN penalty_threshold SET NOT NULL,
  ALTER COLUMN recovery_interval_minutes SET DEFAULT 10,
  ALTER COLUMN recovery_interval_minutes SET NOT NULL;

ALTER TABLE juhe_business.account_quality_enforcements
  DROP CONSTRAINT IF EXISTS account_quality_enforcements_config_source_check,
  DROP CONSTRAINT IF EXISTS account_quality_enforcements_profile_check,
  DROP CONSTRAINT IF EXISTS account_quality_enforcements_penalty_threshold_check,
  DROP CONSTRAINT IF EXISTS account_quality_enforcements_recovery_interval_minutes_check;

ALTER TABLE juhe_business.account_quality_enforcements
  ADD CONSTRAINT account_quality_enforcements_config_source_check
    CHECK (config_source IN ('manual', 'schedule')),
  ADD CONSTRAINT account_quality_enforcements_profile_check
    CHECK (profile IN ('quick', 'full')),
  ADD CONSTRAINT account_quality_enforcements_penalty_threshold_check
    CHECK (penalty_threshold BETWEEN 40 AND 100),
  ADD CONSTRAINT account_quality_enforcements_recovery_interval_minutes_check
    CHECK (recovery_interval_minutes BETWEEN 10 AND 10080);

-- +goose Down
-- Forward-only shared-schema safety fence. Node master reads and writes these
-- snapshots immediately, so a binary rollback must retain both columns and
-- migrated facts. Retirement requires a separately reviewed forward migration.
SELECT 1;
