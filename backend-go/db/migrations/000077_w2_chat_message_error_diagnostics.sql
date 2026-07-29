-- +goose Up
ALTER TABLE juhe_chat.chat_messages
  ADD COLUMN IF NOT EXISTS error_message text;

-- +goose Down
ALTER TABLE juhe_chat.chat_messages
  DROP COLUMN IF EXISTS error_message;
