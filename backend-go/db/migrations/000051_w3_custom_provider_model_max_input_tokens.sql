-- +goose Up
ALTER TABLE juhe_business.custom_provider_models
  ADD COLUMN IF NOT EXISTS max_input_tokens integer CHECK (max_input_tokens IS NULL OR max_input_tokens >= 0);

-- +goose Down
-- no-op: custom provider model metadata is current business state.
