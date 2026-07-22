import type { DatabaseSync } from 'node:sqlite'

import { applyBusinessSchema, applyChatSchema, applyCodexContextStateSchema, applyDatasetSchema, applyStatsSchema, applyUsageCatalogSchema } from './schema.js'
import type { DatabaseClient } from './database-client.js'
import { applyUsageRecordShardBaseSchema } from './usage-record-shards.js'

export type PostgresSchemaName = 'juhe_business' | 'juhe_chat' | 'juhe_dataset' | 'juhe_usage' | 'juhe_stats' | 'juhe_codex_context'

export interface PostgresSchemaStatement {
  schemaName: PostgresSchemaName
  source: string
  sql: string
}

interface SchemaSourceDefinition {
  source: string
  schemaName: PostgresSchemaName
  apply: (database: DatabaseSync) => void
}

const schemaSourceDefinitions: SchemaSourceDefinition[] = [
  { source: 'business', schemaName: 'juhe_business', apply: applyBusinessSchema },
  { source: 'chat', schemaName: 'juhe_chat', apply: applyChatSchema },
  { source: 'dataset', schemaName: 'juhe_dataset', apply: applyDatasetSchema },
  { source: 'usage-catalog', schemaName: 'juhe_usage', apply: applyUsageCatalogSchema },
  { source: 'usage-records', schemaName: 'juhe_usage', apply: applyUsageRecordShardBaseSchema },
  { source: 'stats', schemaName: 'juhe_stats', apply: applyStatsSchema },
  { source: 'codex-context', schemaName: 'juhe_codex_context', apply: applyCodexContextStateSchema }
]

const supplementalSchemaStatements: PostgresSchemaStatement[] = [
  {
    schemaName: 'juhe_business',
    source: 'api-keys-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_api_keys_name_c_lookup ON api_keys((name COLLATE "C"), id)'
  },
  {
    schemaName: 'juhe_business',
    source: 'api-keys-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_c_lookup ON api_keys(system_account_id, (name COLLATE "C"), id)'
  },
  {
    schemaName: 'juhe_business',
    source: 'api-keys-pg-drop-obsolete-indexes',
    sql: 'DROP INDEX IF EXISTS idx_api_keys_name_lookup; DROP INDEX IF EXISTS idx_api_keys_system_account_name_lookup'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_name_c_lookup ON accounts((name COLLATE "C"), id) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'system-teams-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_system_teams_name_c_lookup ON system_teams((name COLLATE "C"), id)'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-pg-indexes',
    sql: "CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_account_shape ON usage_records(account_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''"
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-pg-indexes',
    sql: "CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_group_shape ON usage_records(group_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''"
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_usage_records_system_trace_c_created_sort ON usage_records(system_account_id, (trace_id COLLATE "C"), created_at DESC, id DESC)'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-catalog-pg-drop-obsolete-indexes',
    sql: 'DROP INDEX IF EXISTS idx_usage_record_shard_entries_trace_c_created_sort; DROP INDEX IF EXISTS idx_usage_record_shard_entries_system_trace_c_created_sort; DROP INDEX IF EXISTS idx_usage_record_shard_entries_client_ip_c_created_sort; DROP INDEX IF EXISTS idx_usage_record_shard_entries_system_client_ip_c_created_sort; DROP INDEX IF EXISTS idx_usage_record_shard_entries_system_traffic_created_sort'
  },
  {
    schemaName: 'juhe_dataset',
    source: 'dataset-log-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_audit_logs_system_trace_c_created_sort ON audit_logs(system_account_id, (trace_id COLLATE "C"), created_at DESC, id DESC)'
  },
  {
    schemaName: 'juhe_dataset',
    source: 'dataset-log-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_audit_logs_system_client_ip_c_created_sort ON audit_logs(system_account_id, (client_ip COLLATE "C"), created_at DESC, id DESC)'
  },
  {
    schemaName: 'juhe_dataset',
    source: 'dataset-log-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_c_time ON runtime_logs((trace_id COLLATE "C"), time DESC, id DESC)'
  },
  {
    schemaName: 'juhe_dataset',
    source: 'dataset-log-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_operation_logs_trace_c_created ON operation_logs((trace_id COLLATE "C"), created_at DESC, id DESC)'
  },
  {
    schemaName: 'juhe_dataset',
    source: 'dataset-log-pg-drop-obsolete-indexes',
    sql: 'DROP INDEX IF EXISTS idx_audit_logs_trace_c_created_sort; DROP INDEX IF EXISTS idx_audit_logs_client_ip_c_created_sort; DROP INDEX IF EXISTS idx_public_api_logs_trace_c_created_sort; DROP INDEX IF EXISTS idx_public_api_logs_client_ip_c_created_sort; DROP INDEX IF EXISTS idx_audit_logs_trace_id; DROP INDEX IF EXISTS idx_audit_logs_outcome_created; DROP INDEX IF EXISTS idx_audit_logs_status_created; DROP INDEX IF EXISTS idx_audit_logs_path_created; DROP INDEX IF EXISTS idx_audit_logs_model_created; DROP INDEX IF EXISTS idx_audit_logs_upstream_model_created; DROP INDEX IF EXISTS idx_audit_logs_client_ip_created; DROP INDEX IF EXISTS idx_audit_logs_api_key_created; DROP INDEX IF EXISTS idx_audit_logs_group_created; DROP INDEX IF EXISTS idx_audit_logs_account_created; DROP INDEX IF EXISTS idx_audit_logs_traffic_source_created; DROP INDEX IF EXISTS idx_public_api_logs_trace_id; DROP INDEX IF EXISTS idx_public_api_logs_path_created; DROP INDEX IF EXISTS idx_public_api_logs_status_created; DROP INDEX IF EXISTS idx_public_api_logs_success_created; DROP INDEX IF EXISTS idx_public_api_logs_client_ip_created'
  }
]

const postgresBigintColumnNames = new Set([
  'bytes',
  'asset_bytes',
  'content_bytes',
  'storage_reserved_bytes',
  'reserved_bytes',
  'original_bytes',
  'processed_bytes',
  'request_body_bytes',
  'usage_bytes',
  'storage_offset_bytes',
  'raw_size_bytes',
  'compressed_size_bytes',
  'raw_payload_bytes',
  'compressed_payload_bytes',
  'compression_saved_bytes',
  'request_size_bytes',
  'response_size_bytes',
  'log_offset',
  'cursor_offset',
  'file_size',
  'file_mtime_ms',
  'request_count',
  'success_count',
  'error_count',
  'input_tokens',
  'output_tokens',
  'cache_read_tokens',
  'cache_write_tokens',
  'cache_write_1h_tokens',
  'thinking_tokens',
  'input_image_tokens',
  'output_image_tokens',
  'duration_ms_sum',
  'duration_ms_count',
  'duration_ms_max',
  'first_token_ms_sum',
  'first_token_ms_count',
  'first_token_ms_max',
  'sample_count',
  'event_loop_lag_ms_count',
  'network_rx_bytes_per_sec_count',
  'network_tx_bytes_per_sec_count',
  'hit_count',
  'row_count',
  'page_count',
  'freelist_count',
  'table_count',
  'index_count',
  'growth_bytes_1h',
  'growth_rows_1h',
  'growth_bytes_24h',
  'growth_rows_24h',
  'next_sequence_no',
  'user_turn_count',
  'message_revision',
  'sequence_no',
  'context_revision',
  'compacted_through_sequence',
  'context_claim_revision',
  'context_claim_through_sequence',
  'context_progress_sequence',
  'source_revision',
  'source_from_sequence',
  'source_through_sequence',
  'recent_tail_from_sequence',
  'entry_from_sequence',
  'entry_through_sequence',
  'sequence',
  'active_context_tokens',
  'effective_context_limit_tokens',
  'estimated_input_tokens',
  'upstream_input_tokens',
  'token_count'
])

export function collectPostgresSchemaStatements(): PostgresSchemaStatement[] {
  const statements: PostgresSchemaStatement[] = []
  for (const definition of schemaSourceDefinitions) {
    const rawStatements = collectSqlStatements(definition.apply)
    for (const rawStatement of rawStatements) {
      const normalized = transformSqliteStatementToPostgres(rawStatement, definition.schemaName)
      if (!normalized) continue
      statements.push({
        schemaName: definition.schemaName,
        source: definition.source,
        sql: normalized
      })
    }
  }
  statements.push(...supplementalSchemaStatements)
  return orderSchemaStatements(statements)
}

export function buildPostgresSchemaSql(): string {
  const statements = collectPostgresSchemaStatements()
  const chunks: string[] = [
    '-- Generated from the SQLite schema definitions in backend/src/storage/schema.',
    '-- Each schema is initialized with a dedicated search_path for PostgreSQL.'
  ]
  const seenSchemas = new Set<PostgresSchemaName>()
  for (const statement of statements) {
    if (!seenSchemas.has(statement.schemaName)) {
      seenSchemas.add(statement.schemaName)
      chunks.push('')
      chunks.push(`-- schema: ${statement.schemaName}`)
      chunks.push(`CREATE SCHEMA IF NOT EXISTS ${quoteIdentifier(statement.schemaName)};`)
      chunks.push(`SET search_path TO ${quoteIdentifier(statement.schemaName)}, public;`)
    }
    chunks.push(`${statement.sql};`)
  }
  return chunks.join('\n')
}

export async function applyPostgresSchema(client: Pick<DatabaseClient, 'execute'>): Promise<{ schemaCount: number; statementCount: number }> {
  const statements = collectPostgresSchemaStatements()
  const createdSchemas = new Set<PostgresSchemaName>()
  for (const statement of statements) {
    if (!createdSchemas.has(statement.schemaName)) {
      createdSchemas.add(statement.schemaName)
      await client.execute(`CREATE SCHEMA IF NOT EXISTS ${quoteIdentifier(statement.schemaName)}`)
    }
    await client.execute(`SET search_path TO ${quoteIdentifier(statement.schemaName)}, public;\n${statement.sql}`)
  }
  return {
    schemaCount: createdSchemas.size,
    statementCount: statements.length
  }
}

function collectSqlStatements(applySchema: (database: DatabaseSync) => void): string[] {
  const statements: string[] = []
  const recorder = {
    exec(sql: string): void {
      statements.push(sql)
    },
    prepare() {
      return {
        all: () => [],
        get: () => undefined,
        run: () => ({ changes: 0, lastInsertRowid: 0 })
      }
    }
  } as unknown as DatabaseSync
  applySchema(recorder)
  return statements.flatMap(splitSqlStatements)
}

function splitSqlStatements(sql: string): string[] {
  const statements: string[] = []
  let buffer = ''
  let inSingleQuote = false
  let inDoubleQuote = false
  let inLineComment = false
  let inBlockComment = false

  for (let index = 0; index < sql.length; index += 1) {
    const current = sql[index]
    const next = sql[index + 1]

    if (inLineComment) {
      if (current === '\n') {
        inLineComment = false
        buffer += current
      }
      continue
    }

    if (inBlockComment) {
      if (current === '*' && next === '/') {
        inBlockComment = false
        index += 1
      }
      continue
    }

    if (!inSingleQuote && !inDoubleQuote && current === '-' && next === '-') {
      inLineComment = true
      index += 1
      continue
    }

    if (!inSingleQuote && !inDoubleQuote && current === '/' && next === '*') {
      inBlockComment = true
      index += 1
      continue
    }

    if (current === "'" && !inDoubleQuote) {
      buffer += current
      if (inSingleQuote && next === "'") {
        buffer += next
        index += 1
        continue
      }
      inSingleQuote = !inSingleQuote
      continue
    }

    if (current === '"' && !inSingleQuote) {
      buffer += current
      if (inDoubleQuote && next === '"') {
        buffer += next
        index += 1
        continue
      }
      inDoubleQuote = !inDoubleQuote
      continue
    }

    if (current === ';' && !inSingleQuote && !inDoubleQuote) {
      const statement = buffer.trim()
      if (statement.length > 0) {
        statements.push(statement)
      }
      buffer = ''
      continue
    }

    buffer += current
  }

  const finalStatement = buffer.trim()
  if (finalStatement.length > 0) {
    statements.push(finalStatement)
  }

  return statements
}

function transformSqliteStatementToPostgres(sql: string, schemaName: PostgresSchemaName): string | undefined {
  const trimmed = sql.trim()
  if (trimmed.length === 0) {
    return undefined
  }
  if (/^PRAGMA\b/i.test(trimmed)) {
    return undefined
  }

  let transformed = trimmed
  transformed = transformed.replace(/CHECK\s*\(\s*json_valid\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s+AND\s+json_type\(\s*\1\s*\)\s*=\s*'(object|array)'\s*\)/gi, (_match, columnName: string, jsonType: string) => {
    return `CHECK (jsonb_typeof(${columnName}::jsonb) = '${jsonType.toLowerCase()}')`
  })
  transformed = transformed.replace(/\b([A-Za-z_][A-Za-z0-9_]*)\s+COLLATE\s+NOCASE\b/gi, (_match, columnName: string) => {
    return `lower(${columnName})`
  })
  transformed = transformed.replace(/\bAUTOINCREMENT\b/gi, '')
  transformed = transformed.replace(/\bBLOB\b/gi, 'bytea')
  transformed = transformed.replace(/\bREAL\b/gi, 'double precision')
  transformed = transformed.replace(/\bINTEGER\b/gi, 'integer')
  transformed = transformed.replace(/^(\s*)([A-Za-z_][A-Za-z0-9_]*)(\s+)integer\b/gim, (match, indent: string, columnName: string, spacing: string) => {
    return postgresBigintColumnNames.has(columnName.toLowerCase())
      ? `${indent}${columnName}${spacing}bigint`
      : match
  })
  transformed = transformed.replace(/\bTEXT\b/gi, 'text')
  transformed = transformProviderModelCatalogTableForPostgres(transformed, schemaName)
  transformed = transformCustomProviderModelsTableForPostgres(transformed, schemaName)
  transformed = transformPageDataDirtyDomainsTableForPostgres(transformed, schemaName)
  transformed = transformGatewayModelCatalogSnapshotsTableForPostgres(transformed, schemaName)
  transformed = transformUsageRecordsTableForPostgres(transformed, schemaName)
  transformed = transformChatMessagesTableForPostgres(transformed, schemaName)
  transformed = transformAccountRuntimeTablesForPostgres(transformed, schemaName)
  transformed = transformed.replace(/[ \t]+\n/g, '\n')
  transformed = transformed.replace(/\n{3,}/g, '\n\n')
  return transformed
}

function transformAccountRuntimeTablesForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  const normalized = sql.trim()
  if (schemaName === 'juhe_business' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_test_tasks\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(
      sql.replace(/\bcancel_requested\s+integer\s+NOT\s+NULL\s+DEFAULT\s+0\b/i, 'cancel_requested boolean NOT NULL DEFAULT false'),
      ['queued_at', 'started_at', 'finished_at', 'created_at', 'updated_at']
    )
  }
  if (schemaName === 'juhe_business' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_test_sessions\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(sql, ['last_heartbeat_at', 'cancel_requested_at', 'finished_at', 'created_at', 'updated_at'])
  }
  if (schemaName === 'juhe_business' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_test_session_tasks\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(sql, ['created_at'])
  }
  if (schemaName === 'juhe_stats' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_usage_snapshots\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(sql, ['last_attempt_at', 'last_success_at', 'next_refresh_after', 'updated_at', 'created_at'])
  }
  return sql
}

function postgresTimestampColumns(sql: string, columnNames: string[]): string {
  let transformed = sql
  for (const columnName of columnNames) {
    transformed = transformed.replace(
      new RegExp(`\\b${columnName}\\s+text\\b`, 'i'),
      `${columnName} timestamptz`
    )
  }
  return transformed
}

function transformProviderModelCatalogTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+provider_model_catalog\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\blong_context_input_token_threshold_inclusive\s+integer\s+NOT\s+NULL\s+DEFAULT\s+0\b/i, 'long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false')
    .replace(/\bsupports_prompt_caching\s+integer\s+NOT\s+NULL\s+DEFAULT\s+0\b/i, 'supports_prompt_caching boolean NOT NULL DEFAULT false')
    .replace(/\bcatalog_visible\s+integer\s+NOT\s+NULL\s+DEFAULT\s+1\b/i, 'catalog_visible boolean NOT NULL DEFAULT true')
}

function transformCustomProviderModelsTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+custom_provider_models\s*\(/i.test(sql.trim())) return sql
  return sql.replace(/\bcatalog_visible\s+integer\s+NOT\s+NULL\s+DEFAULT\s+1\b/i, 'catalog_visible boolean NOT NULL DEFAULT true')
}

function transformPageDataDirtyDomainsTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+page_data_dirty_domains\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\bgeneration\s+integer\b/i, 'generation bigint')
    .replace(
      /\bis_dirty\s+integer\s+NOT\s+NULL\s+DEFAULT\s+1\s+CHECK\s*\(\s*is_dirty\s+IN\s*\(\s*0\s*,\s*1\s*\)\s*\)/i,
      'is_dirty boolean NOT NULL DEFAULT TRUE'
    )
    .replace(/\bupdated_at\s+text\s+NOT\s+NULL\b/i, 'updated_at timestamptz NOT NULL')
}

function transformGatewayModelCatalogSnapshotsTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+gateway_model_catalog_snapshots\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\bcreated_at\s+text\s+NOT\s+NULL\b/i, 'created_at timestamptz NOT NULL')
    .replace(/\bupdated_at\s+text\s+NOT\s+NULL\b/i, 'updated_at timestamptz NOT NULL')
}
function transformChatMessagesTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_chat') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+chat_messages\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\bid\s+text\s+PRIMARY\s+KEY\b/i, 'id text NOT NULL')
    .replace(/\n\s*\)\s*$/i, ',\n      PRIMARY KEY (created_at, id)\n    ) PARTITION BY RANGE (created_at)')
}

function transformUsageRecordsTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_usage') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+usage_records\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\bid\s+text\s+PRIMARY\s+KEY\b/i, 'id text NOT NULL')
    .replace(/\n\s*\)\s*$/i, ',\n      PRIMARY KEY (created_at, id)\n    ) PARTITION BY RANGE (created_at)')
}

function orderSchemaStatements(statements: PostgresSchemaStatement[]): PostgresSchemaStatement[] {
  const ordered: PostgresSchemaStatement[] = []
  const groups = new Map<PostgresSchemaName, PostgresSchemaStatement[]>()
  for (const statement of statements) {
    const group = groups.get(statement.schemaName) ?? []
    group.push(statement)
    groups.set(statement.schemaName, group)
  }

  for (const group of groups.values()) {
    const tableStatements = group
      .map((statement) => ({ statement, tableName: extractCreatedTableName(statement.sql) }))
      .filter((entry): entry is { statement: PostgresSchemaStatement; tableName: string } => Boolean(entry.tableName))
    const nonTableStatements = group.filter((statement) => !extractCreatedTableName(statement.sql))
    const tableNames = new Set(tableStatements.map((entry) => entry.tableName))
    const resolvedTables = new Set<string>()
    const remaining = [...tableStatements]

    while (remaining.length > 0) {
      const nextIndex = remaining.findIndex((entry) => {
        const dependencies = extractReferencedTableNames(entry.statement.sql)
          .filter((tableName) => tableName !== entry.tableName && tableNames.has(tableName))
        return dependencies.every((tableName) => resolvedTables.has(tableName))
      })
      const selectedIndex = nextIndex >= 0 ? nextIndex : 0
      const [selected] = remaining.splice(selectedIndex, 1)
      resolvedTables.add(selected.tableName)
      ordered.push(selected.statement)
    }

    ordered.push(...nonTableStatements)
  }

  return ordered
}

function extractCreatedTableName(sql: string): string | undefined {
  const match = /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/i.exec(sql.trim())
  return match?.[1]?.toLowerCase()
}

function extractReferencedTableNames(sql: string): string[] {
  const tableNames: string[] = []
  for (const match of sql.matchAll(/\bREFERENCES\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/gi)) {
    tableNames.push(match[1].toLowerCase())
  }
  return tableNames
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
