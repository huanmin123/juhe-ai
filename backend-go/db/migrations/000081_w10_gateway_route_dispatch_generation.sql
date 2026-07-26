-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.gateway_route_dispatch_generations (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  generation bigint NOT NULL CHECK (generation > 0)
);

INSERT INTO juhe_business.gateway_route_dispatch_generations (singleton, generation)
VALUES (true, 1)
ON CONFLICT (singleton) DO NOTHING;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION juhe_business.bump_gateway_route_dispatch_generation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE juhe_business.gateway_route_dispatch_generations
  SET generation = generation + 1
  WHERE singleton = true;

  RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS gateway_route_dispatch_generation_route_strategies
  ON juhe_business.route_strategies;
CREATE TRIGGER gateway_route_dispatch_generation_route_strategies
AFTER INSERT OR UPDATE OR DELETE ON juhe_business.route_strategies
FOR EACH STATEMENT EXECUTE FUNCTION juhe_business.bump_gateway_route_dispatch_generation();

DROP TRIGGER IF EXISTS gateway_route_dispatch_generation_route_strategy_groups
  ON juhe_business.route_strategy_groups;
CREATE TRIGGER gateway_route_dispatch_generation_route_strategy_groups
AFTER INSERT OR UPDATE OR DELETE ON juhe_business.route_strategy_groups
FOR EACH STATEMENT EXECUTE FUNCTION juhe_business.bump_gateway_route_dispatch_generation();

DROP TRIGGER IF EXISTS gateway_route_dispatch_generation_groups
  ON juhe_business.groups;
CREATE TRIGGER gateway_route_dispatch_generation_groups
AFTER INSERT OR UPDATE OR DELETE ON juhe_business.groups
FOR EACH STATEMENT EXECUTE FUNCTION juhe_business.bump_gateway_route_dispatch_generation();

DROP TRIGGER IF EXISTS gateway_route_dispatch_generation_resource_authorizations
  ON juhe_business.resource_authorizations;
CREATE TRIGGER gateway_route_dispatch_generation_resource_authorizations
AFTER INSERT OR UPDATE OR DELETE ON juhe_business.resource_authorizations
FOR EACH STATEMENT EXECUTE FUNCTION juhe_business.bump_gateway_route_dispatch_generation();

DROP TRIGGER IF EXISTS gateway_route_dispatch_generation_group_authorization_settings
  ON juhe_business.group_authorization_settings;
CREATE TRIGGER gateway_route_dispatch_generation_group_authorization_settings
AFTER INSERT OR UPDATE OR DELETE ON juhe_business.group_authorization_settings
FOR EACH STATEMENT EXECUTE FUNCTION juhe_business.bump_gateway_route_dispatch_generation();

-- +goose Down
-- no-op: the epoch preserves configuration causality for shared route state.
