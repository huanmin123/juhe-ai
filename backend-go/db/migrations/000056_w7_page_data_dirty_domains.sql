-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.page_data_dirty_domains (
    domain TEXT PRIMARY KEY,
    generation BIGINT NOT NULL,
    is_dirty BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
-- no-op: page-data invalidation state is retained across application rollback.
