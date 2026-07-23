-- +goose Up
ALTER TABLE juhe_business.api_keys
  ADD COLUMN purpose TEXT NOT NULL DEFAULT 'general';

ALTER TABLE juhe_business.api_keys
  ADD CONSTRAINT api_keys_purpose_check CHECK (purpose IN ('general', 'chat'));

CREATE UNIQUE INDEX idx_api_keys_chat_purpose_unique
  ON juhe_business.api_keys(system_account_id)
  WHERE purpose = 'chat';

-- +goose Down
DROP INDEX IF EXISTS juhe_business.idx_api_keys_chat_purpose_unique;

ALTER TABLE juhe_business.api_keys
  DROP CONSTRAINT IF EXISTS api_keys_purpose_check;

ALTER TABLE juhe_business.api_keys
  DROP COLUMN IF EXISTS purpose;
