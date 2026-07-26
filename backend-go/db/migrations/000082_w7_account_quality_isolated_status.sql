-- +goose Up
ALTER TABLE juhe_business.accounts
  DROP CONSTRAINT IF EXISTS accounts_status_check;

ALTER TABLE juhe_business.accounts
  ADD CONSTRAINT accounts_status_check CHECK (
    status IN (
      'active',
      'pending_test',
      'disabled',
      'error',
      'rate_limited',
      'temporary_unavailable',
      'quality_isolated'
    )
  );

-- +goose Down
-- An older runtime cannot represent quality isolation. Preserve its
-- hard-unavailable meaning as disabled before restoring the former constraint.
UPDATE juhe_business.accounts
SET status = 'disabled',
    updated_at = now()
WHERE status = 'quality_isolated';

ALTER TABLE juhe_business.accounts
  DROP CONSTRAINT IF EXISTS accounts_status_check;

ALTER TABLE juhe_business.accounts
  ADD CONSTRAINT accounts_status_check CHECK (
    status IN (
      'active',
      'pending_test',
      'disabled',
      'error',
      'rate_limited',
      'temporary_unavailable'
    )
  );
