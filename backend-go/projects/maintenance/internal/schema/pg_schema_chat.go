// Code generated from the Node PostgreSQL storage sources. The statements
// in this file are a verbatim extract of one schema group from
// pg_schema.go's postgresSchemaStatements literal (see the header of
// pg_schema.go for the full provenance and execution model). The
// statements are data: do not hand-edit them and keep them byte-identical
// to the Node dump order.

package schema

// postgresSchemaChat holds the juhe_chat DDL statements in execution
// order.
var postgresSchemaChat = []PGStatement{
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_conversations (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      api_key_id text,
      api_key_name_snapshot text NOT NULL,
      bind_account_id text,
      bind_account_name_snapshot text,
      archived integer NOT NULL DEFAULT 0,
      search_account_id text,
      search_model_id text,
      image_account_id text,
      video_account_id text,
      default_video_model text,
      audio_account_id text,
      default_audio_model text,
      title text NOT NULL DEFAULT '新对话',
      title_source_message_id text,
      is_pinned integer NOT NULL DEFAULT 0,
      last_model text,
      default_image_model text NOT NULL DEFAULT 'gpt-image-2',
      next_sequence_no bigint NOT NULL DEFAULT 1,
      user_turn_count bigint NOT NULL DEFAULT 0,
      message_revision bigint NOT NULL DEFAULT 0,
      active_turn_id text,
      active_started_at text,
      context_revision bigint NOT NULL DEFAULT 0,
      active_checkpoint_id text,
      compacted_through_sequence bigint NOT NULL DEFAULT 0,
      context_state text NOT NULL DEFAULT 'ready',
      active_context_tokens bigint,
      effective_context_limit_tokens bigint,
      context_usage_estimated integer NOT NULL DEFAULT 1,
      context_claim_id text,
      context_claim_revision bigint,
      context_claim_through_sequence bigint,
      context_claimed_at text,
      context_retry_at text,
      context_attempt_count integer NOT NULL DEFAULT 0,
      context_error_code text,
      context_progress_sequence bigint NOT NULL DEFAULT 0,
      context_progress_earliest_expires_at text,
      last_message_at text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (next_sequence_no >= 1),
      CHECK (user_turn_count >= 0),
      CHECK (message_revision >= 0),
      CHECK (is_pinned IN (0, 1)),
      CHECK (archived IN (0, 1)),
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
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_messages (
      id text NOT NULL,
      conversation_id text NOT NULL,
      system_account_id text NOT NULL,
      turn_id text NOT NULL,
      sequence_no bigint NOT NULL,
      client_message_id text,
      role text NOT NULL,
      status text NOT NULL,
      content_text text NOT NULL DEFAULT '',
      content_blocks_json text NOT NULL DEFAULT '[]',
      content_bytes bigint NOT NULL DEFAULT 0,
      storage_reserved_bytes bigint NOT NULL DEFAULT 0,
      model text NOT NULL,
      trace_id text,
      finish_reason text,
      error_code text,
      error_message text,
      created_at text NOT NULL,
      completed_at text,
      expires_at text NOT NULL,
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
      ),
      PRIMARY KEY (created_at, id)
    ) PARTITION BY RANGE (created_at)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_message_idempotency (
      conversation_id text NOT NULL,
      client_message_id text NOT NULL,
      system_account_id text NOT NULL,
      turn_id text NOT NULL,
      user_message_id text NOT NULL,
      assistant_message_id text NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      PRIMARY KEY (conversation_id, client_message_id),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_user_storage_windows (
      system_account_id text NOT NULL,
      bucket_date text NOT NULL,
      content_bytes bigint NOT NULL DEFAULT 0,
      reserved_bytes bigint NOT NULL DEFAULT 0,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, bucket_date),
      CHECK (content_bytes >= 0),
      CHECK (reserved_bytes >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_user_asset_usage (
      system_account_id text PRIMARY KEY,
      asset_bytes bigint NOT NULL DEFAULT 0,
      asset_count integer NOT NULL DEFAULT 0,
      updated_at text NOT NULL,
      CHECK (asset_bytes >= 0),
      CHECK (asset_count >= 0)
    )`,
	},
	// 用户级默认工具绑定表（AI 问答工具体系与主子模型设计 §2.11/§7，2026-10-02）：
	// 每用户一行（system_account_id 主键），列均可空（空 = 未设默认）；
	// 无存量回填，建表经 --ensure-schema 幂等生效。M7 问答音视频工具（2026-10-04，
	// docs/functions/问答音视频工具设计.md §3）增 video/audio 四列。
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_user_tool_preferences (
      system_account_id text PRIMARY KEY,
      search_account_id text,
      search_model_id text,
      image_account_id text,
      default_image_model text,
      video_account_id text,
      default_video_model text,
      audio_account_id text,
      default_audio_model text,
      updated_at timestamptz NOT NULL DEFAULT now()
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_context_checkpoints (
      id text PRIMARY KEY,
      conversation_id text NOT NULL,
      system_account_id text NOT NULL,
      version integer NOT NULL,
      source_revision bigint NOT NULL,
      source_from_sequence bigint NOT NULL,
      source_through_sequence bigint NOT NULL,
      recent_tail_from_sequence bigint NOT NULL,
      entry_from_sequence bigint NOT NULL,
      entry_through_sequence bigint NOT NULL,
      payload_digest text NOT NULL,
      estimated_input_tokens bigint,
      upstream_input_tokens bigint,
      request_body_bytes bigint NOT NULL,
      model_id text NOT NULL,
      provider_code text,
      provider_profile_id text,
      endpoint_family text NOT NULL,
      compact_compatibility_hash text,
      prompt_version text NOT NULL,
      status text NOT NULL DEFAULT 'pending',
      quality_status text NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
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
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_context_entries (
      conversation_id text NOT NULL,
      checkpoint_id text NOT NULL,
      sequence bigint NOT NULL,
      source_message_id text,
      kind text NOT NULL,
      content_json text NOT NULL,
      content_bytes bigint NOT NULL,
      provenance text NOT NULL,
      trust_level text NOT NULL,
      token_count bigint,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      PRIMARY KEY (checkpoint_id, sequence),
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      FOREIGN KEY (checkpoint_id) REFERENCES chat_context_checkpoints(id) ON DELETE CASCADE,
      CHECK (sequence >= 1),
      CHECK (kind IN ('verbatim', 'durable_memory', 'task_state', 'tool_result', 'image_observation', 'provider_compaction')),
      CHECK (content_bytes >= 2),
      CHECK (provenance IN ('user', 'assistant', 'tool', 'asset', 'provider')),
      CHECK (trust_level IN ('untrusted', 'assistant_derived', 'provider_opaque')),
      CHECK (token_count IS NULL OR token_count >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_assets (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      conversation_id text NOT NULL,
      source_kind text NOT NULL DEFAULT 'user_upload',
      original_filename text NOT NULL,
      original_mime_type text NOT NULL,
      original_width integer,
      original_height integer,
      original_bytes bigint NOT NULL,
      original_sha256 text NOT NULL,
      processed_mime_type text,
      processed_width integer,
      processed_height integer,
      processed_bytes bigint,
      processed_sha256 text,
      storage_key text,
      preview_mime_type text,
      preview_width integer,
      preview_height integer,
      preview_bytes integer,
      preview_sha256 text,
      preview_storage_key text,
      processing_status text NOT NULL DEFAULT 'pending',
      processing_error_code text,
      observation_status text NOT NULL DEFAULT 'not_requested',
      observation_json text,
      observation_revision integer NOT NULL DEFAULT 0,
      observation_claim_id text,
      observation_claimed_at text,
      quota_bytes integer NOT NULL,
      turn_id text,
      message_id text,
      committed_at text,
      cleanup_status text NOT NULL DEFAULT 'active',
      cleanup_claim_id text,
      cleanup_attempt_count integer NOT NULL DEFAULT 0,
      cleanup_claimed_at text,
      cleanup_retry_at text,
      cleanup_error_code text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      expires_at text NOT NULL,
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
      CHECK (processed_mime_type IS NULL OR processed_mime_type IN ('image/jpeg', 'image/png', 'image/webp', 'audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')),
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
      CHECK (source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL
        OR processed_mime_type IN ('audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')),
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
          AND processed_bytes IS NOT NULL
          AND processed_sha256 IS NOT NULL
          AND storage_key IS NOT NULL
          AND (
            processed_mime_type IN ('audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')
            OR (processed_width IS NOT NULL AND processed_height IS NOT NULL)
          )
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
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_asset_references (
      asset_id text NOT NULL,
      conversation_id text NOT NULL,
      turn_id text NOT NULL,
      message_id text NOT NULL,
      reference_kind text NOT NULL,
      content_order integer NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      UNIQUE (message_id, content_order),
      CHECK (reference_kind IN ('user_input', 'assistant_output')),
      CHECK (content_order >= 0)
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE TABLE IF NOT EXISTS chat_image_generations (
      asset_id text PRIMARY KEY,
      conversation_id text NOT NULL,
      system_account_id text NOT NULL,
      operation text NOT NULL,
      model text NOT NULL,
      prompt text NOT NULL,
      source_asset_ids_json text NOT NULL DEFAULT '[]',
      root_asset_id text NOT NULL,
      size text NOT NULL,
      quality text NOT NULL,
      output_format text NOT NULL,
      created_at text NOT NULL,
      expires_at text NOT NULL,
      FOREIGN KEY (asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (root_asset_id, conversation_id) REFERENCES chat_assets(id, conversation_id) ON DELETE CASCADE,
      FOREIGN KEY (conversation_id) REFERENCES chat_conversations(id) ON DELETE CASCADE,
      CHECK (operation IN ('generate', 'edit')),
      CHECK (jsonb_typeof(source_asset_ids_json::jsonb) = 'array')
    )`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_recent
      ON chat_conversations(system_account_id, last_message_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_pinned_recent
      ON chat_conversations(system_account_id, is_pinned DESC, last_message_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_api_key
      ON chat_conversations(system_account_id, api_key_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_active_started
      ON chat_conversations(active_started_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_conversations_context_queue
      ON chat_conversations(context_state, context_retry_at, context_claimed_at, updated_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_sequence
      ON chat_messages(conversation_id, sequence_no DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation_turn
      ON chat_messages(conversation_id, turn_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_context
      ON chat_messages(system_account_id, conversation_id, status, expires_at, sequence_no DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_compaction_source
      ON chat_messages(conversation_id, system_account_id, status, sequence_no)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_messages_expiry
      ON chat_messages(expires_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_idempotency_expiry
      ON chat_message_idempotency(expires_at, conversation_id, client_message_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_conversation_version
      ON chat_context_checkpoints(conversation_id, version DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_context_checkpoints_one_active
      ON chat_context_checkpoints(conversation_id) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_cleanup
      ON chat_context_checkpoints(expires_at, status, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_entries_conversation_checkpoint
      ON chat_context_entries(conversation_id, checkpoint_id, sequence)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_context_entries_expiry
      ON chat_context_entries(expires_at, checkpoint_id, sequence)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_conversation
      ON chat_assets(system_account_id, conversation_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_lookup
      ON chat_assets(system_account_id, id, conversation_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_message
      ON chat_assets(conversation_id, turn_id, message_id, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_uncommitted
      ON chat_assets(system_account_id, conversation_id, expires_at, id)
      WHERE turn_id IS NULL AND message_id IS NULL
        AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active'`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_assets_cleanup
      ON chat_assets(cleanup_status, cleanup_retry_at, expires_at, id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_asset_references_message
      ON chat_asset_references(conversation_id, message_id, content_order)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_asset_references_asset_valid
      ON chat_asset_references(asset_id, expires_at)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_asset_references_cleanup
      ON chat_asset_references(expires_at, asset_id, message_id)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_image_generations_conversation_recent
      ON chat_image_generations(conversation_id, created_at DESC, asset_id DESC)`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat",
		SQL: `CREATE INDEX IF NOT EXISTS idx_chat_image_generations_expiry
      ON chat_image_generations(expires_at, asset_id)`,
	},
	// 会话账户唯一绑定列（AI 问答会话账户唯一绑定设计，2026-09-28）：新库由
	// 上方 CREATE TABLE 直接声明；既有库经下列幂等 ALTER 补齐（列存在即
	// no-op）。bind_mode/bind_group_id/bind_group_name_snapshot 三列已由一次性
	// 迁移命令 --migrate-chat-account-only-binding 删除（先备份到
	// chat_conversations_bind_legacy_backup 再 DROP），ensure 不再重建旧列。
	// archived 带 IN (0,1) 值域 CHECK，ADD COLUMN 时存量行取 DEFAULT 0 恒满足
	// 约束。
	{
		SchemaName: "juhe_chat",
		Source:     "chat-conversation-account-binding-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS bind_account_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-conversation-account-binding-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS bind_account_name_snapshot text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-conversation-account-binding-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS archived integer NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-conversation-account-binding-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS search_account_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-conversation-account-binding-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS search_model_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-conversation-account-binding-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS image_account_id text`,
	},
	// M7 问答音视频工具绑定列（问答音视频工具设计 §3，2026-10-04）：新库由上方
	// CREATE TABLE 直接声明；既有库经幂等 ALTER 补齐（列存在即 no-op）。
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS video_account_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS default_video_model text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS audio_account_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_conversations ADD COLUMN IF NOT EXISTS default_audio_model text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_user_tool_preferences ADD COLUMN IF NOT EXISTS video_account_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_user_tool_preferences ADD COLUMN IF NOT EXISTS default_video_model text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_user_tool_preferences ADD COLUMN IF NOT EXISTS audio_account_id text`,
	},
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-tool-pg-columns",
		SQL:        `ALTER TABLE chat_user_tool_preferences ADD COLUMN IF NOT EXISTS default_audio_model text`,
	},
	// M7 chat_assets 媒体 MIME 词表扩展（问答音视频工具设计 §3，2026-10-04）：
	// 新库由上方 CREATE TABLE 直接声明新约束；既有库的三条内联无名 CHECK
	//（processed_mime_type 词表、assistant_generated 预览必备、ready 形状含
	// 宽高必备）经本 DO 块按约束定义文本特征定位后 DROP（内联约束的自动命名
	// 不稳定，按列名组合匹配是确定性判据；chat_assets_media_* 为本块新建的
	// 带名约束，被排除），并以带名约束重建（媒体行无宽高/无预览合法）。幂等：
	// 目标约束已存在（按名判定）时整块 no-op。存量行不含媒体 MIME，恒满足
	// 新约束（NOT VALID 不需要）。
	{
		SchemaName: "juhe_chat",
		Source:     "chat-media-asset-pg-constraints",
		SQL: `DO $$
DECLARE
  candidate RECORD;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chat_assets_media_ready_check') THEN
    FOR candidate IN
      SELECT c.conname, pg_get_constraintdef(c.oid) AS def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
      JOIN pg_namespace n ON n.oid = t.relnamespace
      WHERE c.contype = 'c'
        AND n.nspname = 'juhe_chat'
        AND t.relname = 'chat_assets'
        AND c.conname NOT LIKE 'chat\_assets\_media\_%'
        AND (
          (pg_get_constraintdef(c.oid) LIKE '%processed_mime_type%' AND pg_get_constraintdef(c.oid) LIKE '%image/webp%')
          OR (pg_get_constraintdef(c.oid) LIKE '%processing_status%' AND pg_get_constraintdef(c.oid) LIKE '%storage_key%')
          OR (pg_get_constraintdef(c.oid) LIKE '%source_kind%' AND pg_get_constraintdef(c.oid) LIKE '%preview_storage_key%')
        )
    LOOP
      EXECUTE format('ALTER TABLE juhe_chat.chat_assets DROP CONSTRAINT %I', candidate.conname);
    END LOOP;
    ALTER TABLE juhe_chat.chat_assets ADD CONSTRAINT chat_assets_media_mime_check
      CHECK (processed_mime_type IS NULL OR processed_mime_type IN ('image/jpeg', 'image/png', 'image/webp', 'audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm'));
    ALTER TABLE juhe_chat.chat_assets ADD CONSTRAINT chat_assets_media_preview_check
      CHECK (source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL
        OR processed_mime_type IN ('audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm'));
    ALTER TABLE juhe_chat.chat_assets ADD CONSTRAINT chat_assets_media_ready_check
      CHECK (
        processing_status != 'ready'
        OR (
          processed_mime_type IS NOT NULL
          AND processed_bytes IS NOT NULL
          AND processed_sha256 IS NOT NULL
          AND storage_key IS NOT NULL
          AND (
            processed_mime_type IN ('audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')
            OR (processed_width IS NOT NULL AND processed_height IS NOT NULL)
          )
        )
      );
  END IF;
END $$`,
	},
}
