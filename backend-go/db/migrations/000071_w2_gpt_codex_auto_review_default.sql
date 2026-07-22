-- +goose Up
UPDATE juhe_business.providers
SET default_supported_models_json = (
  default_supported_models_json::jsonb || '["codex-auto-review"]'::jsonb
)::text,
updated_at = now()
WHERE code = 'gpt'
  AND jsonb_typeof(default_supported_models_json::jsonb) = 'array'
  AND NOT (default_supported_models_json::jsonb @> '["codex-auto-review"]'::jsonb);

-- +goose Down
-- Keep the appended model on rollback: removing it could overwrite an administrator's
-- intentional default selection, and schema 70 already accepts this catalog model.
SELECT 1;
