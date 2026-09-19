// Code generated from the Node PostgreSQL storage sources. The statements
// in this file are a verbatim extract of one schema group from
// pg_schema.go's postgresSchemaStatements literal (see the header of
// pg_schema.go for the full provenance and execution model). The
// statements are data: do not hand-edit them and keep them byte-identical
// to the Node dump order.

package schema

// postgresSchemaDataset holds the juhe_dataset DDL statements in
// execution order.
var postgresSchemaDataset = []PGStatement{
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL: `CREATE TABLE IF NOT EXISTS public_api_logs (
          id text PRIMARY KEY,
          trace_id text,
          source_ref_id text,
          source_name text,
          token_id text,
          token_name text,
          token_prefix text,
          is_test_token integer NOT NULL DEFAULT 0,
          method text NOT NULL,
          path text NOT NULL,
          query_string text,
          client_ip text,
          user_agent text,
          status_code integer,
          success integer NOT NULL DEFAULT 0,
          duration_ms integer,
          request_size_bytes bigint NOT NULL DEFAULT 0,
          response_size_bytes bigint NOT NULL DEFAULT 0,
          request_capture_status text NOT NULL DEFAULT 'empty',
          response_capture_status text NOT NULL DEFAULT 'empty',
          request_data_json text NOT NULL DEFAULT '{}',
          response_data_json text NOT NULL DEFAULT '{}',
          error_code text,
          error_message text,
          started_at text NOT NULL,
          ended_at text NOT NULL,
          created_at text NOT NULL
        )`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL: `CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
          api_key_id text PRIMARY KEY,
          system_account_id text NOT NULL,
          created_at text NOT NULL,
          updated_at text NOT NULL,
          attempt_count integer NOT NULL DEFAULT 0,
          last_attempt_at text,
          last_blocked_reason text,
          last_error_message text
        )`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL: `CREATE TABLE IF NOT EXISTS account_record_cleanup_targets (
          account_id text PRIMARY KEY,
          system_account_id text NOT NULL,
          related_account_ids_json text NOT NULL DEFAULT '[]',
          authorization_ids_json text NOT NULL DEFAULT '[]',
          team_scope_ids_json text NOT NULL DEFAULT '[]',
          created_at text NOT NULL,
          updated_at text NOT NULL,
          attempt_count integer NOT NULL DEFAULT 0,
          last_attempt_at text,
          last_blocked_reason text,
          last_error_message text
        )`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_public_api_logs_created ON public_api_logs(created_at, id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_public_api_logs_source_created ON public_api_logs(source_ref_id, created_at, id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id)`,
	},
	{
		SchemaName: "juhe_dataset",
		Source:     "dataset",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id)`,
	},
}
