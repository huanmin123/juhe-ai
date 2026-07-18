-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS temporary_unavailable_continuous_probe_enabled integer;

ALTER TABLE juhe_business.accounts
  ALTER COLUMN temporary_unavailable_continuous_probe_enabled SET DEFAULT 1;

UPDATE juhe_business.accounts
SET temporary_unavailable_continuous_probe_enabled = 1
WHERE temporary_unavailable_continuous_probe_enabled IS NULL;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'accounts_temporary_unavailable_continuous_probe_enabled_check'
      AND conrelid = 'juhe_business.accounts'::regclass
  ) THEN
    ALTER TABLE juhe_business.accounts
      ADD CONSTRAINT accounts_temporary_unavailable_continuous_probe_enabled_check
      CHECK (temporary_unavailable_continuous_probe_enabled IN (0, 1)) NOT VALID;
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE juhe_business.accounts
  VALIDATE CONSTRAINT accounts_temporary_unavailable_continuous_probe_enabled_check;

ALTER TABLE juhe_business.accounts
  ALTER COLUMN temporary_unavailable_continuous_probe_enabled SET NOT NULL;

-- +goose Down
-- no-op: the account probe preference is current business state and remains readable by the previous release.
