-- +goose Up
INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES ('sys_admin', 'chatImageGenerationTotalTimeoutSeconds', '900', now())
ON CONFLICT (system_account_id, key) DO NOTHING;

-- +goose Down
DELETE FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key = 'chatImageGenerationTotalTimeoutSeconds';
