-- +goose Up
ALTER TABLE juhe_dataset.runtime_log_file_cursors
  ADD COLUMN IF NOT EXISTS truncation_generation integer NOT NULL DEFAULT 0;

-- +goose Down
-- no-op: runtime log cursor generation is retained with the shared Node writer schema.
