-- +goose Up
DELETE FROM juhe_business.system_settings
WHERE key = 'cooldownAccountRetestLongTermIntervalHours';

-- +goose Down
INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES ('sys_admin', 'cooldownAccountRetestLongTermIntervalHours', '1', now())
ON CONFLICT (system_account_id, key) DO NOTHING;
