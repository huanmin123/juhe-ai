-- +goose Up
INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES ('sys_admin', 'operationLogMaxChangesPerRecord', '100', now())
ON CONFLICT (system_account_id, key) DO NOTHING;

-- +goose Down
-- no-op: operation log settings are part of the current system settings contract.
