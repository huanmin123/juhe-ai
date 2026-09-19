// Code generated from the Node PostgreSQL storage sources. The statements
// in this file are a verbatim extract of one schema group from
// pg_schema.go's postgresSchemaStatements literal (see the header of
// pg_schema.go for the full provenance and execution model). The
// statements are data: do not hand-edit them and keep them byte-identical
// to the Node dump order.

package schema

// postgresSchemaCodexContext holds the juhe_codex_context DDL statements
// in execution order.
var postgresSchemaCodexContext = []PGStatement{
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      api_key_id text,
      group_id text NOT NULL,
      provider_code text NOT NULL,
      source_response_id text,
      latest_response_id text,
      latest_compact_id text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      last_used_at text NOT NULL,
      expires_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_responses (
      response_id text PRIMARY KEY,
      session_id text NOT NULL,
      previous_response_id text,
      system_account_id text NOT NULL,
      api_key_id text,
      group_id text NOT NULL,
      provider_code text NOT NULL,
      upstream_account_id text,
      model text,
      upstream_model text,
      storage_key text NOT NULL,
      storage_offset_bytes bigint NOT NULL,
      sha256 text NOT NULL,
      raw_size_bytes bigint NOT NULL,
      compressed_size_bytes bigint NOT NULL,
      compression text NOT NULL DEFAULT 'gzip',
      schema_version integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      last_used_at text NOT NULL,
      expires_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_compacts (
      compact_id text PRIMARY KEY,
      session_id text NOT NULL,
      source_response_id text,
      summary_digest text NOT NULL,
      system_account_id text NOT NULL,
      api_key_id text,
      group_id text NOT NULL,
      provider_code text NOT NULL,
      upstream_account_id text,
      model text,
      upstream_model text,
      storage_key text NOT NULL,
      storage_offset_bytes bigint NOT NULL,
      sha256 text NOT NULL,
      raw_size_bytes bigint NOT NULL,
      compressed_size_bytes bigint NOT NULL,
      compression text NOT NULL DEFAULT 'gzip',
      schema_version integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      last_used_at text NOT NULL,
      expires_at text NOT NULL
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL: `CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue (
      storage_key text PRIMARY KEY,
      enqueued_at text NOT NULL,
      updated_at text NOT NULL,
      next_attempt_at text NOT NULL,
      attempt_count integer NOT NULL DEFAULT 0,
      last_error text
    )`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON codex_context_sessions(expires_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON codex_context_sessions(last_used_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_boundary ON codex_context_sessions(system_account_id, api_key_id, group_id, provider_code)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON codex_context_responses(session_id, created_at ASC, response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON codex_context_responses(expires_at ASC, response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_responses_boundary ON codex_context_responses(system_account_id, api_key_id, group_id, provider_code, response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON codex_context_compacts(session_id, created_at ASC, compact_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON codex_context_compacts(source_response_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON codex_context_compacts(expires_at ASC, compact_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_boundary ON codex_context_compacts(system_account_id, api_key_id, group_id, provider_code, compact_id)`,
	},
	{
		SchemaName: "juhe_codex_context",
		Source:     "codex-context",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_codex_context_storage_cleanup_due ON codex_context_storage_cleanup_queue(next_attempt_at ASC, enqueued_at ASC, storage_key ASC)`,
	},
}
