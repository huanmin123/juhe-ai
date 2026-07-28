-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  IF to_regclass('juhe_chat.chat_messages') IS NOT NULL THEN
    ALTER TABLE juhe_chat.chat_messages
      ADD COLUMN IF NOT EXISTS error_message text;
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF to_regclass('juhe_chat.chat_messages') IS NOT NULL THEN
    ALTER TABLE juhe_chat.chat_messages
      DROP COLUMN IF EXISTS error_message;
  END IF;
END
$$;
-- +goose StatementEnd
