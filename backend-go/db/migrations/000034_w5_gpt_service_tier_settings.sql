-- +goose Up
INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES
  ('sys_admin', 'gptPriorityPriceMultiplier', '2', now()),
  ('sys_admin', 'gptFlexPriceMultiplier', '0.5', now())
ON CONFLICT (system_account_id, key) DO NOTHING;

-- +goose Down
-- no-op: GPT service tier settings are part of the current management settings contract.
