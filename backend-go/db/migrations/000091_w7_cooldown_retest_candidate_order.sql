-- +goose Up
-- Keep the global cooldown due scan in keyset order. Historical Node-created
-- PostgreSQL schemas may already contain this index, so the Goose-owned
-- catalog must adopt it without an upgrade-time name collision.
ALTER TABLE juhe_business.accounts
  DROP CONSTRAINT IF EXISTS accounts_cooldown_retest_generation_check;

ALTER TABLE juhe_business.accounts
  ADD CONSTRAINT accounts_cooldown_retest_generation_check
  CHECK (
    cooldown_retest_generation IS NULL
    OR (
      btrim(cooldown_retest_generation, CHR(9) || CHR(10) || CHR(11) || CHR(12) || CHR(13) || CHR(32) || CHR(160) || CHR(5760) || CHR(8192) || CHR(8193) || CHR(8194) || CHR(8195) || CHR(8196) || CHR(8197) || CHR(8198) || CHR(8199) || CHR(8200) || CHR(8201) || CHR(8202) || CHR(8232) || CHR(8233) || CHR(8239) || CHR(8287) || CHR(12288) || CHR(65279)) <> ''
      AND cooldown_retest_generation = btrim(cooldown_retest_generation, CHR(9) || CHR(10) || CHR(11) || CHR(12) || CHR(13) || CHR(32) || CHR(160) || CHR(5760) || CHR(8192) || CHR(8193) || CHR(8194) || CHR(8195) || CHR(8196) || CHR(8197) || CHR(8198) || CHR(8199) || CHR(8200) || CHR(8201) || CHR(8202) || CHR(8232) || CHR(8233) || CHR(8239) || CHR(8287) || CHR(12288) || CHR(65279))
    )
  );

CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_candidate_order
  ON juhe_business.accounts (
    cooldown_until ASC,
    priority ASC,
    created_at ASC,
    id ASC,
    health_check_endpoint_mode
  )
  WHERE deleted_at IS NULL
    AND cooldown_until IS NOT NULL
    AND schedulable = true
    AND type IN ('api_key', 'oauth', 'google_oauth')
    AND status IN ('temporary_unavailable', 'rate_limited');

-- +goose Down
-- Forward-only shared-schema safety fence. A rolled-back binary may still run
-- beside a newer worker and benefits from retaining the bounded due scan.
SELECT 1;
