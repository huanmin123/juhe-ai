-- +goose Up
ALTER TABLE juhe_business.accounts
  ADD COLUMN IF NOT EXISTS health_check_failure_started_at timestamptz;

-- +goose Down
-- no-op: 账户健康检查首次失败时间属于当前状态机事实，回滚不删除字段。
