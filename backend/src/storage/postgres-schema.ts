import type { DatabaseSync } from 'node:sqlite'

import { applyBusinessSchema, applyCodexContextStateSchema, applyDatasetSchema, applyStatsSchema, applyUsageCatalogSchema } from './schema.js'
import type { DatabaseClient } from './database-client.js'
import { applyUsageRecordShardBaseSchema } from './usage-record-shards.js'

export type PostgresSchemaName = 'juhe_business' | 'juhe_dataset' | 'juhe_usage' | 'juhe_stats' | 'juhe_codex_context'

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
  { source: 'dataset', schemaName: 'juhe_dataset', apply: applyDatasetSchema },
  { source: 'usage-catalog', schemaName: 'juhe_usage', apply: applyUsageCatalogSchema },
  { source: 'usage-records', schemaName: 'juhe_usage', apply: applyUsageRecordShardBaseSchema },
  { source: 'stats', schemaName: 'juhe_stats', apply: applyStatsSchema },
  { source: 'codex-context', schemaName: 'juhe_codex_context', apply: applyCodexContextStateSchema }
]

const supplementalSchemaStatements: PostgresSchemaStatement[] = [
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS usage_semantic text'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS cache_write_tokens integer'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS cache_write_1h_tokens integer'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS cache_write_cost_usd double precision'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS thinking_tokens integer'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS provider_protocol_profile_id text'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'ALTER TABLE IF EXISTS usage_records ADD COLUMN IF NOT EXISTS failure_attribution text'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-supplemental',
    sql: 'CREATE INDEX IF NOT EXISTS idx_usage_records_provider_protocol_profile_created_at ON usage_records(provider_protocol_profile_id, created_at)'
  }
]

export function collectPostgresSchemaStatements(): PostgresSchemaStatement[] {
  const statements: PostgresSchemaStatement[] = []
  for (const definition of schemaSourceDefinitions) {
    const rawStatements = collectSqlStatements(definition.apply)
    for (const rawStatement of rawStatements) {
      const normalized = transformSqliteStatementToPostgres(rawStatement)
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
  const tableColumns = new Map<string, Set<string>>()
  const recorder = {
    exec(sql: string): void {
      statements.push(sql)
      rememberSchemaColumns(sql, tableColumns)
    },
    prepare(sql: string) {
      const tableName = extractPragmaTableInfoTableName(sql)
      return {
        all: () => tableName
          ? [...(tableColumns.get(tableName) ?? new Set<string>())].map((name) => ({ name }))
          : [],
        get: () => undefined,
        run: () => ({ changes: 0, lastInsertRowid: 0 })
      }
    }
  } as unknown as DatabaseSync
  applySchema(recorder)
  return statements.flatMap(splitSqlStatements)
}

function rememberSchemaColumns(sql: string, tableColumns: Map<string, Set<string>>): void {
  for (const statement of splitSqlStatements(sql)) {
    const createdTable = extractCreatedTableColumns(statement)
    if (createdTable) {
      tableColumns.set(createdTable.tableName, new Set(createdTable.columns))
      continue
    }
    const addedColumn = extractAlterTableAddedColumn(statement)
    if (!addedColumn) continue
    const columns = tableColumns.get(addedColumn.tableName) ?? new Set<string>()
    columns.add(addedColumn.columnName)
    tableColumns.set(addedColumn.tableName, columns)
  }
}

function extractPragmaTableInfoTableName(sql: string): string | undefined {
  const match = /^\s*PRAGMA\s+(?:main\.)?table_info\s*\(\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\s*\)/i.exec(sql)
  return normalizeSqlIdentifier(match?.[1] ?? match?.[2] ?? match?.[3])
}

function extractCreatedTableColumns(sql: string): { tableName: string; columns: string[] } | undefined {
  const tableName = extractCreatedTableName(sql)
  if (!tableName) return undefined
  const body = extractCreateTableBody(sql)
  if (!body) return undefined
  const columns = splitTopLevelCommaList(body)
    .map(extractColumnName)
    .filter((columnName): columnName is string => Boolean(columnName))
  return { tableName, columns }
}

function extractCreateTableBody(sql: string): string | undefined {
  const start = sql.indexOf('(')
  if (start < 0) return undefined
  let depth = 0
  let inSingleQuote = false
  let inDoubleQuote = false
  for (let index = start; index < sql.length; index += 1) {
    const current = sql[index]
    const next = sql[index + 1]
    if (current === "'" && !inDoubleQuote) {
      if (inSingleQuote && next === "'") {
        index += 1
        continue
      }
      inSingleQuote = !inSingleQuote
      continue
    }
    if (current === '"' && !inSingleQuote) {
      if (inDoubleQuote && next === '"') {
        index += 1
        continue
      }
      inDoubleQuote = !inDoubleQuote
      continue
    }
    if (inSingleQuote || inDoubleQuote) continue
    if (current === '(') {
      depth += 1
      continue
    }
    if (current !== ')') continue
    depth -= 1
    if (depth === 0) {
      return sql.slice(start + 1, index)
    }
  }
  return undefined
}

function splitTopLevelCommaList(input: string): string[] {
  const items: string[] = []
  let buffer = ''
  let depth = 0
  let inSingleQuote = false
  let inDoubleQuote = false
  for (let index = 0; index < input.length; index += 1) {
    const current = input[index]
    const next = input[index + 1]
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
    if (!inSingleQuote && !inDoubleQuote) {
      if (current === '(') depth += 1
      else if (current === ')') depth = Math.max(0, depth - 1)
      else if (current === ',' && depth === 0) {
        const item = buffer.trim()
        if (item) items.push(item)
        buffer = ''
        continue
      }
    }
    buffer += current
  }
  const item = buffer.trim()
  if (item) items.push(item)
  return items
}

function extractColumnName(definition: string): string | undefined {
  if (/^(?:PRIMARY|FOREIGN|UNIQUE|CHECK|CONSTRAINT)\b/i.test(definition.trim())) {
    return undefined
  }
  const match = /^\s*(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\b/.exec(definition)
  return normalizeSqlIdentifier(match?.[1] ?? match?.[2])
}

function extractAlterTableAddedColumn(sql: string): { tableName: string; columnName: string } | undefined {
  const match = /^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))\b/i.exec(sql.trim())
  const tableName = normalizeSqlIdentifier(match?.[1] ?? match?.[2])
  const columnName = normalizeSqlIdentifier(match?.[3] ?? match?.[4])
  return tableName && columnName ? { tableName, columnName } : undefined
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

function transformSqliteStatementToPostgres(sql: string): string | undefined {
  const trimmed = sql.trim()
  if (trimmed.length === 0) {
    return undefined
  }
  if (/^PRAGMA\b/i.test(trimmed)) {
    return undefined
  }

  let transformed = trimmed
  transformed = transformed.replace(/CHECK\s*\(\s*json_valid\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s+AND\s+json_type\(\s*\1\s*\)\s*=\s*'object'\s*\)/gi, (_match, columnName: string) => {
    return `CHECK (jsonb_typeof(${columnName}::jsonb) = 'object')`
  })
  transformed = transformed.replace(/\b([A-Za-z_][A-Za-z0-9_]*)\s+COLLATE\s+NOCASE\b/gi, (_match, columnName: string) => {
    return `lower(${columnName})`
  })
  transformed = transformed.replace(/\bAUTOINCREMENT\b/gi, '')
  transformed = transformed.replace(/\bBLOB\b/gi, 'bytea')
  transformed = transformed.replace(/\bREAL\b/gi, 'double precision')
  transformed = transformed.replace(/\bINTEGER\b/gi, 'integer')
  transformed = transformed.replace(/\bTEXT\b/gi, 'text')
  transformed = transformed.replace(/[ \t]+\n/g, '\n')
  transformed = transformed.replace(/\n{3,}/g, '\n\n')
  return transformed
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

    const addColumnStatements = nonTableStatements.filter((statement) => extractAlterTableAddedColumn(statement.sql))
    const remainingNonTableStatements = nonTableStatements.filter((statement) => !extractAlterTableAddedColumn(statement.sql))
    ordered.push(...addColumnStatements, ...remainingNonTableStatements)
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

function normalizeSqlIdentifier(value: string | undefined): string | undefined {
  const normalized = value?.trim().replace(/^"|"$/g, '').toLowerCase()
  return normalized ? normalized : undefined
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
