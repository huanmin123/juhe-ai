-- +goose Up
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE SCHEMA IF NOT EXISTS juhe_usage;
CREATE SCHEMA IF NOT EXISTS juhe_stats;

-- +goose Down
DROP SCHEMA IF EXISTS juhe_stats;
DROP SCHEMA IF EXISTS juhe_usage;
DROP SCHEMA IF EXISTS juhe_dataset;
DROP SCHEMA IF EXISTS juhe_business;

