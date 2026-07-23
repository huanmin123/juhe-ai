-- +goose Up
DROP TABLE IF EXISTS juhe_business.page_data_dirty_domains;

-- +goose Down
-- no-op: the retired page-data dirty-domain mechanism must not be recreated on rollback.
