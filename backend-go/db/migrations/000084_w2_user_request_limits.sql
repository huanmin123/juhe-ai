-- +goose Up
ALTER TABLE juhe_business.system_accounts
  ADD COLUMN IF NOT EXISTS request_limits_json text;

ALTER TABLE juhe_business.system_accounts
  DROP CONSTRAINT IF EXISTS system_accounts_request_limits_json_object_check;

ALTER TABLE juhe_business.system_accounts
  ADD CONSTRAINT system_accounts_request_limits_json_object_check
  CHECK (
    request_limits_json IS NULL
    OR jsonb_typeof(request_limits_json::jsonb) = 'object'
  );

-- +goose Down
ALTER TABLE juhe_business.system_accounts
  DROP CONSTRAINT IF EXISTS system_accounts_request_limits_json_object_check;

ALTER TABLE juhe_business.system_accounts
  DROP COLUMN IF EXISTS request_limits_json;
