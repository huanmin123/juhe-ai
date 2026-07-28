-- +goose Up
WITH legacy_defaults AS (
  SELECT system_account_id
  FROM juhe_business.system_settings
  WHERE key IN ('accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes')
  GROUP BY system_account_id
  HAVING COUNT(*) FILTER (
    WHERE key = 'accountHealthCheckIntervalHours' AND value_json = '12'
  ) = 1
     AND COUNT(*) FILTER (
       WHERE key = 'accountHealthCheckJitterMinutes' AND value_json = '120'
     ) = 1
), updated_settings AS (
  UPDATE juhe_business.system_settings AS settings
  SET value_json = CASE settings.key
        WHEN 'accountHealthCheckIntervalHours' THEN '1'
        WHEN 'accountHealthCheckJitterMinutes' THEN '10'
      END,
      updated_at = now()
  FROM legacy_defaults
  WHERE settings.system_account_id = legacy_defaults.system_account_id
    AND settings.key IN ('accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes')
  RETURNING settings.system_account_id
), migrated_system_accounts AS (
  SELECT DISTINCT system_account_id
  FROM updated_settings
)
UPDATE juhe_business.accounts AS accounts
SET next_health_check_at = CASE
      WHEN accounts.next_health_check_at <= now() THEN
        now() + make_interval(secs => mod(abs(hashtextextended(accounts.id, 0)::numeric), 600)::integer)
      ELSE LEAST(
        accounts.next_health_check_at,
        now() + interval '1 hour'
          + make_interval(secs => mod(abs(hashtextextended(accounts.id, 0)::numeric), 600)::integer),
        GREATEST(
          COALESCE(accounts.last_health_success_at, accounts.last_health_check_at, accounts.created_at, now())
            + interval '1 hour'
            + make_interval(secs => mod(abs(hashtextextended(accounts.id, 0)::numeric), 600)::integer),
          now() + make_interval(secs => mod(abs(hashtextextended(accounts.id, 0)::numeric), 600)::integer)
        )
      )
    END,
    updated_at = now()
FROM migrated_system_accounts
WHERE accounts.system_account_id = migrated_system_accounts.system_account_id
  AND accounts.status = 'active'
  AND accounts.deleted_at IS NULL
  AND accounts.next_health_check_at IS NOT NULL;

-- +goose Down
WITH hourly_defaults AS (
  SELECT system_account_id
  FROM juhe_business.system_settings
  WHERE key IN ('accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes')
  GROUP BY system_account_id
  HAVING COUNT(*) FILTER (
    WHERE key = 'accountHealthCheckIntervalHours' AND value_json = '1'
  ) = 1
     AND COUNT(*) FILTER (
       WHERE key = 'accountHealthCheckJitterMinutes' AND value_json = '10'
     ) = 1
)
UPDATE juhe_business.system_settings AS settings
SET value_json = CASE settings.key
      WHEN 'accountHealthCheckIntervalHours' THEN '12'
      WHEN 'accountHealthCheckJitterMinutes' THEN '120'
    END,
    updated_at = now()
FROM hourly_defaults
WHERE settings.system_account_id = hourly_defaults.system_account_id
  AND settings.key IN ('accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes');
