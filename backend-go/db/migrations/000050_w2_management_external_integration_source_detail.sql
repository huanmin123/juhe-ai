-- +goose Up
CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source_created
  ON juhe_business.external_integration_source_tokens (source_ref_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS juhe_business.idx_external_integration_source_tokens_source_created;
