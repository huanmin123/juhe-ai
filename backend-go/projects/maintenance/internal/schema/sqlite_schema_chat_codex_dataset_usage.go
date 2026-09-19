// Code generated from the Node storage schema sources. The DDL constants
// in this file are a verbatim extract of one schema group from
// sqlite_schema.go (see the header of sqlite_schema.go for the full
// provenance, porting rules and execution order). Do not hand-edit the
// constants; regenerate or re-verify against the Node sources when they
// change.

package schema

const sqliteChatDDL = `    CREATE TABLE IF NOT EXISTS chat_conversations (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      title TEXT NOT NULL DEFAULT '新对话',
      title_source_message_id TEXT,
      is_pinned INTEGER NOT NULL DEFAULT 0,
      last_model TEXT,
      default_image_model TEXT NOT NULL DEFAULT 'gpt-image-2',
      next_sequence_no INTEGER NOT NULL DEFAULT 1,
      user_turn_count INTEGER NOT NULL DEFAULT 0,
      message_revision INTEGER NOT NULL DEFAULT 0,
      active_turn_id TEXT,
      active_started_at TEXT,
      context_revision INTEGER NOT NULL DEFAULT 0,
      active_checkpoint_id TEXT,
      compacted_through_sequence INTEGER NOT NULL DEFAULT 0,
      context_state TEXT NOT NULL DEFAULT 'ready',
      active_context_tokens INTEGER,
      effective_context_limit_tokens INTEGER,
      context_usage_estimated INTEGER NOT NULL DEFAULT 1,
      context_claim_id TEXT,
      context_claim_revision INTEGER,
      context_claim_through_sequence INTEGER,
      context_claimed_at TEXT,
      context_retry_at TEXT,
      context_attempt_count INTEGER NOT NULL DEFAULT 0,
      context_error_code TEXT,
      context_progress_sequence INTEGER NOT NULL DEFAULT 0,
      context_progress_earliest_expires_at TEXT,
      last_message_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (next_sequence_no >= 1),
      CHECK (user_turn_count >= 0),
      CHECK (message_revision >= 0),
      CHECK (is_pinned IN (0, 1)),
      CHECK (context_revision >= 0),
      CHECK (compacted_through_sequence >= 0 AND compacted_through_sequence < next_sequence_no),
      CHECK (context_state IN ('ready', 'compact_pending', 'compacting', 'compact_failed')),
      CHECK (active_context_tokens IS NULL OR active_context_tokens >= 0),
      CHECK (effective_context_limit_tokens IS NULL OR effective_context_limit_tokens > 0),
      CHECK (context_usage_estimated IN (0, 1)),
      CHECK (context_attempt_count >= 0),
      CHECK (context_progress_sequence >= 0),
      CHECK (
        (active_checkpoint_id IS NULL AND compacted_through_sequence = 0)
        OR active_checkpoint_id IS NOT NULL
      ),
      CHECK (
        (
          context_state = 'compacting'
          AND context_claim_id IS NOT NULL
          AND context_claim_revision = context_revision
          AND context_claim_through_sequence IS NOT NULL
          AND context_claim_through_sequence > compacted_through_sequence
          AND context_claim_through_sequence <= next_sequence_no - 3
          AND context_claimed_at IS NOT NULL
          AND context_progress_sequence >= compacted_through_sequence
          AND context_progress_sequence <= context_claim_through_sequence
        )
        OR (
          context_state != 'compacting'
          AND context_claim_id IS NULL
          AND context_claim_revision IS NULL
          AND context_claim_through_sequence IS NULL
          AND context_claimed_at IS NULL
          AND context_progress_sequence = 0
          AND context_progress_earliest_expires_at IS NULL
        )
      )
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
      storage_reserved_bytes INTEGER NOT NULL DEFAULT 0,
      model TEXT NOT NULL,
      trace_id TEXT,
      finish_reason TEXT,
      error_code TEXT,
      error_message TEXT,
      created_at TEXT NOT NULL,
      completed_at TEXT,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (sequence_no >= 1),
      CHECK (content_bytes >= 0),
      CHECK (storage_reserved_bytes >= 0),
      CHECK (role IN ('user', 'assistant')),
      CHECK (status IN ('completed', 'streaming', 'failed', 'canceled')),
      CHECK (
        (role = 'user' AND client_message_id IS NOT NULL AND status = 'completed')
        OR (role = 'assistant' AND client_message_id IS NULL)
      ),
      CHECK (
        (role = 'assistant' AND status = 'streaming' AND storage_reserved_bytes > 0)
        OR (status != 'streaming' AND storage_reserved_bytes = 0)
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
      reserved_bytes INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, bucket_date),
      CHECK (content_bytes >= 0),
      CHECK (reserved_bytes >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_user_asset_usage (
      system_account_id TEXT PRIMARY KEY,
      asset_bytes INTEGER NOT NULL DEFAULT 0,
      asset_count INTEGER NOT NULL DEFAULT 0,
      updated_at TEXT NOT NULL,
      CHECK (asset_bytes >= 0),
      CHECK (asset_count >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
      id TEXT PRIMARY KEY,
      conversation_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      version INTEGER NOT NULL,
      source_revision INTEGER NOT NULL,
      source_from_sequence INTEGER NOT NULL,
      source_through_sequence INTEGER NOT NULL,
      recent_tail_from_sequence INTEGER NOT NULL,
      entry_from_sequence INTEGER NOT NULL,
      entry_through_sequence INTEGER NOT NULL,
      payload_digest TEXT NOT NULL,
      estimated_input_tokens INTEGER,
      upstream_input_tokens INTEGER,
      request_body_bytes INTEGER NOT NULL,
      model_id TEXT NOT NULL,
      provider_code TEXT,
      provider_profile_id TEXT,
      endpoint_family TEXT NOT NULL,
      compact_compatibility_hash TEXT,
      prompt_version TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'pending',
      quality_status TEXT NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      UNIQUE (conversation_id, version),
      CHECK (version >= 1),
      CHECK (source_revision >= 0),
      CHECK (source_from_sequence >= 1),
      CHECK (source_through_sequence >= source_from_sequence),
      CHECK (recent_tail_from_sequence = source_through_sequence + 1),
      CHECK (entry_from_sequence >= 1),
      CHECK (entry_through_sequence >= entry_from_sequence),
      CHECK (length(payload_digest) = 64),
      CHECK (estimated_input_tokens IS NULL OR estimated_input_tokens >= 0),
      CHECK (upstream_input_tokens IS NULL OR upstream_input_tokens >= 0),
      CHECK (request_body_bytes >= 0),
      CHECK (status IN ('pending', 'active', 'superseded', 'rejected')),
      CHECK (quality_status IN ('passed', 'failed'))
    );

    CREATE TABLE IF NOT EXISTS chat_context_entries (
      conversation_id TEXT NOT NULL,
      checkpoint_id TEXT NOT NULL,
      sequence INTEGER NOT NULL,
      source_message_id TEXT,
      kind TEXT NOT NULL,
      content_json TEXT NOT NULL,
      content_bytes INTEGER NOT NULL,
      provenance TEXT NOT NULL,
      trust_level TEXT NOT NULL,
      token_count INTEGER,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      PRIMARY KEY (checkpoint_id, sequence),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      FOREIGN KEY (checkpoint_id) REFERENCES chat_context_checkpoints(id) ON DELETE CASCADE,
      CHECK (sequence >= 1),
      CHECK (kind IN ('verbatim', 'durable_memory', 'task_state', 'tool_result', 'image_observation', 'provider_compaction')),
      CHECK (content_bytes >= 2),
      CHECK (provenance IN ('user', 'assistant', 'tool', 'asset', 'provider')),
      CHECK (trust_level IN ('untrusted', 'assistant_derived', 'provider_opaque')),
      CHECK (token_count IS NULL OR token_count >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_assets (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      source_kind TEXT NOT NULL DEFAULT 'user_upload',
      original_filename TEXT NOT NULL,
      original_mime_type TEXT NOT NULL,
      original_width INTEGER,
      original_height INTEGER,
      original_bytes INTEGER NOT NULL,
      original_sha256 TEXT NOT NULL,
      processed_mime_type TEXT,
      processed_width INTEGER,
      processed_height INTEGER,
      processed_bytes INTEGER,
      processed_sha256 TEXT,
      storage_key TEXT,
      preview_mime_type TEXT,
      preview_width INTEGER,
      preview_height INTEGER,
      preview_bytes INTEGER,
      preview_sha256 TEXT,
      preview_storage_key TEXT,
      processing_status TEXT NOT NULL DEFAULT 'pending',
      processing_error_code TEXT,
      observation_status TEXT NOT NULL DEFAULT 'not_requested',
      observation_json TEXT,
      observation_revision INTEGER NOT NULL DEFAULT 0,
      observation_claim_id TEXT,
      observation_claimed_at TEXT,
      quota_bytes INTEGER NOT NULL,
      turn_id TEXT,
      message_id TEXT,
      committed_at TEXT,
      cleanup_status TEXT NOT NULL DEFAULT 'active',
      cleanup_claim_id TEXT,
      cleanup_attempt_count INTEGER NOT NULL DEFAULT 0,
      cleanup_claimed_at TEXT,
      cleanup_retry_at TEXT,
      cleanup_error_code TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      UNIQUE (id, conversation_id),
      CHECK (original_width IS NULL OR original_width > 0),
      CHECK (original_height IS NULL OR original_height > 0),
      CHECK ((original_width IS NULL AND original_height IS NULL) OR (original_width IS NOT NULL AND original_height IS NOT NULL)),
      CHECK (original_bytes > 0),
      CHECK (length(original_sha256) = 64),
      CHECK (processed_width IS NULL OR processed_width > 0),
      CHECK (processed_height IS NULL OR processed_height > 0),
      CHECK ((processed_width IS NULL AND processed_height IS NULL) OR (processed_width IS NOT NULL AND processed_height IS NOT NULL)),
      CHECK (processed_bytes IS NULL OR processed_bytes > 0),
      CHECK (source_kind IN ('user_upload', 'assistant_generated')),
      CHECK (processed_mime_type IS NULL OR processed_mime_type IN ('image/jpeg', 'image/png', 'image/webp')),
      CHECK (processed_sha256 IS NULL OR length(processed_sha256) = 64),
      CHECK (preview_mime_type IS NULL OR preview_mime_type = 'image/webp'),
      CHECK (preview_width IS NULL OR preview_width > 0),
      CHECK (preview_height IS NULL OR preview_height > 0),
      CHECK (preview_bytes IS NULL OR preview_bytes > 0),
      CHECK (preview_sha256 IS NULL OR length(preview_sha256) = 64),
      CHECK (
        (preview_mime_type IS NULL AND preview_width IS NULL AND preview_height IS NULL AND preview_bytes IS NULL AND preview_sha256 IS NULL AND preview_storage_key IS NULL)
        OR (preview_mime_type IS NOT NULL AND preview_width IS NOT NULL AND preview_height IS NOT NULL AND preview_bytes IS NOT NULL AND preview_sha256 IS NOT NULL AND preview_storage_key IS NOT NULL)
      ),
      CHECK (source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL),
      CHECK (processing_status IN ('pending', 'ready', 'failed')),
      CHECK (observation_status IN ('not_requested', 'pending', 'ready', 'failed')),
      CHECK (observation_revision >= 0),
      CHECK (quota_bytes > 0),
      CHECK (cleanup_status IN ('active', 'claimed', 'failed')),
      CHECK (cleanup_attempt_count >= 0),
      CHECK (
        processing_status != 'ready'
        OR (
          processed_mime_type IS NOT NULL
          AND processed_width IS NOT NULL
          AND processed_height IS NOT NULL
          AND processed_bytes IS NOT NULL
          AND processed_sha256 IS NOT NULL
          AND storage_key IS NOT NULL
        )
      ),
      CHECK (
        (observation_status = 'pending' AND observation_claim_id IS NOT NULL AND observation_claimed_at IS NOT NULL)
        OR (observation_status != 'pending' AND observation_claim_id IS NULL AND observation_claimed_at IS NULL)
      ),
      CHECK (
        (turn_id IS NULL AND message_id IS NULL AND committed_at IS NULL)
        OR (turn_id IS NOT NULL AND message_id IS NOT NULL AND committed_at IS NOT NULL)
      ),
      CHECK (
        (cleanup_status = 'claimed' AND cleanup_claim_id IS NOT NULL AND cleanup_claimed_at IS NOT NULL)
        OR (cleanup_status != 'claimed' AND cleanup_claim_id IS NULL AND cleanup_claimed_at IS NULL)
      )
    );

    CREATE TABLE IF NOT EXISTS chat_asset_references (
      asset_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      turn_id TEXT NOT NULL,
      message_id TEXT NOT NULL,
      reference_kind TEXT NOT NULL,
      content_order INTEGER NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      UNIQUE (message_id, content_order),
      CHECK (reference_kind IN ('user_input', 'assistant_output')),
      CHECK (content_order >= 0)
    );

    CREATE TABLE IF NOT EXISTS chat_image_generations (
      asset_id TEXT PRIMARY KEY,
      conversation_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      operation TEXT NOT NULL,
      model TEXT NOT NULL,
      prompt TEXT NOT NULL,
      source_asset_ids_json TEXT NOT NULL DEFAULT '[]',
      root_asset_id TEXT NOT NULL,
      size TEXT NOT NULL,
      quality TEXT NOT NULL,
      output_format TEXT NOT NULL,
      created_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (root_asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (operation IN ('generate', 'edit')),
      CHECK (json_valid(source_asset_ids_json) AND json_type(source_asset_ids_json) = 'array')
    );

    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_recent
      ON chat_conversations(system_account_id, last_message_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_pinned_recent
      ON chat_conversations(system_account_id, is_pinned DESC, last_message_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_api_key
      ON chat_conversations(system_account_id, api_key_id);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_active_started
      ON chat_conversations(active_started_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_conversations_context_queue
      ON chat_conversations(context_state, context_retry_at, context_claimed_at, updated_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_sequence
      ON chat_messages(conversation_id, sequence_no DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_turn
      ON chat_messages(conversation_id, turn_id);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_context
      ON chat_messages(system_account_id, conversation_id, status, expires_at, sequence_no DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_compaction_source
      ON chat_messages(conversation_id, system_account_id, status, sequence_no);
    CREATE INDEX IF NOT EXISTS idx_chat_messages_expiry
      ON chat_messages(expires_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_idempotency_expiry
      ON chat_message_idempotency(expires_at, conversation_id, client_message_id);
    CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_conversation_version
      ON chat_context_checkpoints(conversation_id, version DESC, id DESC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_context_checkpoints_one_active
      ON chat_context_checkpoints(conversation_id) WHERE status = 'active';
    CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_cleanup
      ON chat_context_checkpoints(expires_at, status, id);
    CREATE INDEX IF NOT EXISTS idx_chat_context_entries_conversation_checkpoint
      ON chat_context_entries(conversation_id, checkpoint_id, sequence);
    CREATE INDEX IF NOT EXISTS idx_chat_context_entries_expiry
      ON chat_context_entries(expires_at, checkpoint_id, sequence);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_conversation
      ON chat_assets(system_account_id, conversation_id, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_lookup
      ON chat_assets(system_account_id, id, conversation_id);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_message
      ON chat_assets(conversation_id, turn_id, message_id, id);
    CREATE INDEX IF NOT EXISTS idx_chat_assets_uncommitted
      ON chat_assets(system_account_id, conversation_id, expires_at, id)
      WHERE turn_id IS NULL AND message_id IS NULL
        AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active';
    CREATE INDEX IF NOT EXISTS idx_chat_assets_cleanup
      ON chat_assets(cleanup_status, cleanup_retry_at, expires_at, id);
    CREATE INDEX IF NOT EXISTS idx_chat_asset_references_message
      ON chat_asset_references(conversation_id, message_id, content_order);
    CREATE INDEX IF NOT EXISTS idx_chat_asset_references_asset_valid
      ON chat_asset_references(asset_id, expires_at);
    CREATE INDEX IF NOT EXISTS idx_chat_asset_references_cleanup
      ON chat_asset_references(expires_at, asset_id, message_id);
    CREATE INDEX IF NOT EXISTS idx_chat_image_generations_conversation_recent
      ON chat_image_generations(conversation_id, created_at DESC, asset_id DESC);
    CREATE INDEX IF NOT EXISTS idx_chat_image_generations_expiry
      ON chat_image_generations(expires_at, asset_id);
`

const sqliteCodexContextDDL = `    CREATE TABLE IF NOT EXISTS codex_context_sessions (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      source_response_id TEXT,
      latest_response_id TEXT,
      latest_compact_id TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_responses (
      response_id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      previous_response_id TEXT,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      upstream_account_id TEXT,
      model TEXT,
      upstream_model TEXT,
      storage_key TEXT NOT NULL,
      storage_offset_bytes INTEGER NOT NULL,
      sha256 TEXT NOT NULL,
      raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL,
      compression TEXT NOT NULL DEFAULT 'gzip',
      schema_version INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_compacts (
      compact_id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      source_response_id TEXT,
      summary_digest TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      group_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      upstream_account_id TEXT,
      model TEXT,
      upstream_model TEXT,
      storage_key TEXT NOT NULL,
      storage_offset_bytes INTEGER NOT NULL,
      sha256 TEXT NOT NULL,
      raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL,
      compression TEXT NOT NULL DEFAULT 'gzip',
      schema_version INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      last_used_at TEXT NOT NULL,
      expires_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (
      storage_key TEXT PRIMARY KEY,
      enqueued_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      next_attempt_at TEXT NOT NULL,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      last_error TEXT
    );

    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON codex_context_sessions(expires_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON codex_context_sessions(last_used_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_boundary ON codex_context_sessions(system_account_id, api_key_id, group_id, provider_code);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON codex_context_responses(session_id, created_at ASC, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON codex_context_responses(expires_at ASC, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_responses_boundary ON codex_context_responses(system_account_id, api_key_id, group_id, provider_code, response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON codex_context_compacts(session_id, created_at ASC, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON codex_context_compacts(source_response_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON codex_context_compacts(expires_at ASC, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_boundary ON codex_context_compacts(system_account_id, api_key_id, group_id, provider_code, compact_id);
    CREATE INDEX IF NOT EXISTS idx_codex_context_storage_cleanup_due ON codex_context_storage_cleanup_queue(next_attempt_at ASC, enqueued_at ASC, storage_key ASC);
`

const sqliteDatasetDDL = `    CREATE TABLE IF NOT EXISTS public_api_logs (
          id TEXT PRIMARY KEY,
          trace_id TEXT,
          source_ref_id TEXT,
          source_name TEXT,
          token_id TEXT,
          token_name TEXT,
          token_prefix TEXT,
          is_test_token INTEGER NOT NULL DEFAULT 0,
          method TEXT NOT NULL,
          path TEXT NOT NULL,
          query_string TEXT,
          client_ip TEXT,
          user_agent TEXT,
          status_code INTEGER,
          success INTEGER NOT NULL DEFAULT 0,
          duration_ms INTEGER,
          request_size_bytes INTEGER NOT NULL DEFAULT 0,
          response_size_bytes INTEGER NOT NULL DEFAULT 0,
          request_capture_status TEXT NOT NULL DEFAULT 'empty',
          response_capture_status TEXT NOT NULL DEFAULT 'empty',
          request_data_json TEXT NOT NULL DEFAULT '{}',
          response_data_json TEXT NOT NULL DEFAULT '{}',
          error_code TEXT,
          error_message TEXT,
          started_at TEXT NOT NULL,
          ended_at TEXT NOT NULL,
          created_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
          api_key_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (
          account_id TEXT PRIMARY KEY,
          system_account_id TEXT NOT NULL,
          related_account_ids_json TEXT NOT NULL DEFAULT '[]',
          authorization_ids_json TEXT NOT NULL DEFAULT '[]',
          team_scope_ids_json TEXT NOT NULL DEFAULT '[]',
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          attempt_count INTEGER NOT NULL DEFAULT 0,
          last_attempt_at TEXT,
          last_blocked_reason TEXT,
          last_error_message TEXT
        );

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_created ON public_api_logs(created_at, id);

    CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created ON public_api_logs(source_ref_id, created_at, id);

    CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id);

    CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id);
`

const sqliteUsageCatalogDDL = `    CREATE TABLE IF NOT EXISTS usage_record_shards (
          shard_key TEXT PRIMARY KEY,
          bucket_date TEXT NOT NULL,
          shard_id INTEGER NOT NULL,
          file_path TEXT NOT NULL,
          schema_version INTEGER NOT NULL DEFAULT 1,
          status TEXT NOT NULL DEFAULT 'active',
          first_seen_at TEXT NOT NULL,
          last_write_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );

    CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
          usage_id TEXT PRIMARY KEY,
          shard_key TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          trace_id TEXT NOT NULL,
          api_key_id TEXT,
          account_id TEXT,
          group_id TEXT,
          model TEXT,
          traffic_source TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          status_code INTEGER,
          client_ip TEXT,
          first_token_ms INTEGER,
          duration_ms INTEGER,
          cost_usd REAL,
          created_at TEXT NOT NULL,
          indexed_at TEXT NOT NULL,
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_account_shards (
          account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
          api_key_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key),
          FOREIGN KEY (shard_key) REFERENCES usage_record_shards(shard_key) ON DELETE CASCADE
        );

    CREATE INDEX IF NOT EXISTS idx_usage_record_shards_bucket ON usage_record_shards(bucket_date, shard_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_account_shards_account_created ON usage_record_account_shards(account_id, first_created_at, shard_key);
    CREATE INDEX IF NOT EXISTS idx_usage_record_api_key_shards_key_created ON usage_record_api_key_shards(api_key_id, system_account_id, first_created_at, shard_key);

    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_shard ON usage_record_shard_entries(shard_key, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_created_sort ON usage_record_shard_entries(created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_created_sort ON usage_record_shard_entries(system_account_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_trace_created_sort ON usage_record_shard_entries(system_account_id, trace_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_api_key_created_sort ON usage_record_shard_entries(system_account_id, api_key_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_group_created_sort ON usage_record_shard_entries(system_account_id, group_id, created_at, usage_id);
    CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_account_created_sort ON usage_record_shard_entries(system_account_id, account_id, created_at, usage_id);
`
