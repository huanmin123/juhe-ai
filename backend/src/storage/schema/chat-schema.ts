import type { DatabaseSync } from 'node:sqlite'

export function applyChatSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;
    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS chat_conversations (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      title TEXT NOT NULL DEFAULT '新对话',
      title_source_message_id TEXT,
      last_model TEXT,
      next_sequence_no INTEGER NOT NULL DEFAULT 1,
      active_turn_id TEXT,
      active_started_at TEXT,
      last_message_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (next_sequence_no >= 1)
    );

    CREATE TABLE IF NOT EXISTS chat_messages (
      id TEXT PRIMARY KEY,
      conversation_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      turn_id TEXT NOT NULL,
      sequence_no INTEGER NOT NULL,
      client_message_id TEXT,
      role TEXT NOT NULL,
      status TEXT NOT NULL,
      content_text TEXT NOT NULL DEFAULT '',
      content_blocks_json TEXT NOT NULL DEFAULT '[]',
      content_bytes INTEGER NOT NULL DEFAULT 0,
      model TEXT NOT NULL,
      trace_id TEXT,
      finish_reason TEXT,
      error_code TEXT,
      created_at TEXT NOT NULL,
      completed_at TEXT,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (sequence_no >= 1),
      CHECK (content_bytes >= 0),
      CHECK (role IN ('user', 'assistant')),
      CHECK (status IN ('completed', 'streaming', 'failed', 'canceled')),
      CHECK (
        (role = 'user' AND client_message_id IS NOT NULL AND status = 'completed')
        OR (role = 'assistant' AND client_message_id IS NULL)
      )
    );

    CREATE TABLE IF NOT EXISTS chat_message_idempotency (
      conversation_id TEXT NOT NULL,
      client_message_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      turn_id TEXT NOT NULL,
      user_message_id TEXT NOT NULL,
      assistant_message_id TEXT NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      PRIMARY KEY (conversation_id, client_message_id),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS chat_user_storage_windows (
      system_account_id TEXT NOT NULL,
      bucket_date TEXT NOT NULL,
      content_bytes INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, bucket_date),
      CHECK (content_bytes >= 0)
    );

    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_recent
      ON chat_conversations(system_account_id, last_message_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_api_key
      ON chat_conversations(system_account_id, api_key_id);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_active_started
      ON chat_conversations(active_started_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_sequence
      ON chat_messages(conversation_id, sequence_no DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_turn
      ON chat_messages(conversation_id, turn_id);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_context
      ON chat_messages(system_account_id, conversation_id, status, expires_at, sequence_no DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_expiry
      ON chat_messages(expires_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_idempotency_expiry
      ON chat_message_idempotency(expires_at, conversation_id, client_message_id);
  `)
}
