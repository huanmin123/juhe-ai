-- +goose Up
-- A cooldown generation is created only when a real cooldown episode starts.
-- Existing rows stay NULL so the owning runtime can repair legacy state using
-- its authoritative generation allocator.
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS cooldown_retest_generation text;

ALTER TABLE juhe_business.accounts
  DROP CONSTRAINT IF EXISTS accounts_cooldown_retest_generation_check;

ALTER TABLE juhe_business.accounts
  ADD CONSTRAINT accounts_cooldown_retest_generation_check
  CHECK (
    cooldown_retest_generation IS NULL
    OR btrim(cooldown_retest_generation) <> ''
  );

-- +goose Down
-- no-op: cooldown generation is current shared account state. Removing the
-- column or constraint during binary rollback could admit ambiguous episodes.
SELECT 1;
