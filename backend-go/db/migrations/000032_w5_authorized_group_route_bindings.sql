-- +goose Up
ALTER TABLE juhe_business.route_strategy_groups
  DROP CONSTRAINT IF EXISTS route_strategy_groups_group_id_system_account_id_fkey;

ALTER TABLE juhe_business.route_strategy_groups
  DROP CONSTRAINT IF EXISTS route_strategy_groups_group_id_fkey;

ALTER TABLE juhe_business.route_strategy_groups
  DROP CONSTRAINT IF EXISTS fk_route_strategy_groups_group;

ALTER TABLE juhe_business.route_strategy_groups
  ADD CONSTRAINT fk_route_strategy_groups_group
  FOREIGN KEY (group_id)
  REFERENCES juhe_business.groups(id)
  ON DELETE CASCADE;

-- +goose Down
-- no-op: authorized cross-owner group route bindings are part of the current routing contract.
