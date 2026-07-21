import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { postgresTransactionLocalTimeoutSetSql, type PostgresQueryClient, type PostgresPoolClient } from './postgres-client.js'

export type DatabaseClientDriver = 'sqlite' | 'postgres'

export interface SqlBindResult {
  sql: string
  params: readonly unknown[]
}

export interface SqlDialect {
  driver: DatabaseClientDriver
  placeholder(index: number): string
  placeholders(count: number, startIndex?: number): string
  bindPlaceholders(count: number): string
  bind(sql: string, params?: readonly unknown[]): SqlBindResult
  quoteIdentifier(identifier: string): string
  qualifyTable(schemaName: string, tableName: string): string
}

export interface ExecuteResult {
  changes: number
  lastInsertRowid?: number | bigint
}

export interface DatabaseClient {
  driver: DatabaseClientDriver
  dialect: SqlDialect
  query<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T[]>
  one<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T | undefined>
  execute(sql: string, params?: readonly unknown[]): Promise<ExecuteResult>
  transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T>
}

interface PostgresPoolLike extends PostgresQueryClient {
  connect?: () => Promise<PostgresPoolClient>
}

export const sqliteDialect: SqlDialect = {
  driver: 'sqlite',
  placeholder: () => '?',
  placeholders: (count: number) => Array.from({ length: normalizedPlaceholderCount(count) }, () => '?').join(', '),
  bindPlaceholders: (count: number) => Array.from({ length: normalizedPlaceholderCount(count) }, () => '?').join(', '),
  bind: (sql: string, params: readonly unknown[] = []) => ({ sql, params }),
  quoteIdentifier,
  qualifyTable: (_schemaName: string, tableName: string) => quoteIdentifier(tableName)
}

export const postgresDialect: SqlDialect = {
  driver: 'postgres',
  placeholder: (index: number) => `$${normalizePlaceholderIndex(index)}`,
  placeholders: (count: number, startIndex = 1) => {
    const size = normalizedPlaceholderCount(count)
    const start = normalizePlaceholderIndex(startIndex)
    return Array.from({ length: size }, (_item, index) => `$${start + index}`).join(', ')
  },
  bindPlaceholders: (count: number) => Array.from({ length: normalizedPlaceholderCount(count) }, () => '?').join(', '),
  bind: (sql: string, params: readonly unknown[] = []) => ({
    sql: convertQuestionPlaceholdersToPostgres(sql),
    params
  }),
  quoteIdentifier,
  qualifyTable: (schemaName: string, tableName: string) => `${quoteIdentifier(schemaName)}.${quoteIdentifier(tableName)}`
}

export function createSqliteDatabaseClient(database: DatabaseSync): DatabaseClient {
  return createSqliteDatabaseClientInternal(database, false)
}

export function createPostgresDatabaseClient(client: PostgresPoolLike): DatabaseClient {
  return createPostgresDatabaseClientInternal(client, false)
}

export function convertQuestionPlaceholdersToPostgres(sql: string, startIndex = 1): string {
  let placeholderIndex = normalizePlaceholderIndex(startIndex)
  let output = ''
  let inSingleQuote = false
  let inDoubleQuote = false
  let inLineComment = false
  let inBlockComment = false

  for (let index = 0; index < sql.length; index += 1) {
    const current = sql[index]
    const next = sql[index + 1]

    if (inLineComment) {
      output += current
      if (current === '\n') {
        inLineComment = false
      }
      continue
    }

    if (inBlockComment) {
      output += current
      if (current === '*' && next === '/') {
        output += next
        index += 1
        inBlockComment = false
      }
      continue
    }

    if (!inSingleQuote && !inDoubleQuote && current === '-' && next === '-') {
      output += current + next
      index += 1
      inLineComment = true
      continue
    }

    if (!inSingleQuote && !inDoubleQuote && current === '/' && next === '*') {
      output += current + next
      index += 1
      inBlockComment = true
      continue
    }

    if (current === "'" && !inDoubleQuote) {
      output += current
      if (inSingleQuote && next === "'") {
        output += next
        index += 1
        continue
      }
      inSingleQuote = !inSingleQuote
      continue
    }

    if (current === '"' && !inSingleQuote) {
      output += current
      if (inDoubleQuote && next === '"') {
        output += next
        index += 1
        continue
      }
      inDoubleQuote = !inDoubleQuote
      continue
    }

    if (current === '?' && !inSingleQuote && !inDoubleQuote) {
      output += `$${placeholderIndex}`
      placeholderIndex += 1
      continue
    }

    output += current
  }

  return output
}

function createSqliteDatabaseClientInternal(database: DatabaseSync, transactionActive: boolean): DatabaseClient {
  const client: DatabaseClient = {
    driver: 'sqlite',
    dialect: sqliteDialect,
    async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      const bound = sqliteDialect.bind(sql, params)
      return database.prepare(bound.sql).all(...toSqliteValues(bound.params)) as unknown as T[]
    },
    async one<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      const bound = sqliteDialect.bind(sql, params)
      return database.prepare(bound.sql).get(...toSqliteValues(bound.params)) as unknown as T | undefined
    },
    async execute(sql: string, params: readonly unknown[] = []): Promise<ExecuteResult> {
      const bound = sqliteDialect.bind(sql, params)
      const result = database.prepare(bound.sql).run(...toSqliteValues(bound.params))
      return {
        changes: Number(result.changes ?? 0),
        lastInsertRowid: result.lastInsertRowid
      }
    },
    async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
      if (transactionActive || database.isTransaction) {
        return operation(createSqliteDatabaseClientInternal(database, true))
      }
      database.exec('BEGIN IMMEDIATE')
      try {
        const result = await operation(createSqliteDatabaseClientInternal(database, true))
        database.exec('COMMIT')
        return result
      } catch (error) {
        database.exec('ROLLBACK')
        throw error
      }
    }
  }
  return client
}

function createPostgresDatabaseClientInternal(client: PostgresPoolLike, transactionActive: boolean): DatabaseClient {
  return {
    driver: 'postgres',
    dialect: postgresDialect,
    async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      const bound = postgresDialect.bind(sql, params)
      const result = await queryPostgres(client, bound.sql, bound.params)
      return normalizePostgresRows(result.rows) as T[]
    },
    async one<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      const rows = await this.query<T>(sql, params)
      return rows[0]
    },
    async execute(sql: string, params: readonly unknown[] = []): Promise<ExecuteResult> {
      const bound = postgresDialect.bind(sql, params)
      const result = await queryPostgres(client, bound.sql, bound.params)
      return {
        changes: Number(result.rowCount ?? result.rows.length ?? 0)
      }
    },
    async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
      if (transactionActive) {
        return operation(createPostgresDatabaseClientInternal(client, true))
      }
      if (!client.connect) {
        throw new Error('PostgreSQL transaction requires a pool client with connect()')
      }
      const connection = await client.connect()
      let released = false
      const releaseConnection = (): void => {
        if (released) return
        released = true
        connection.release()
      }
      try {
        await connection.query('BEGIN')
        await connection.query(postgresTransactionLocalTimeoutSetSql())
        const tx = createPostgresDatabaseClientInternal(connection, true)
        const result = await operation(tx)
        await connection.query('COMMIT')
        return result
      } catch (error) {
        try {
          await connection.query('ROLLBACK')
        } finally {
          releaseConnection()
        }
        throw error
      } finally {
        releaseConnection()
      }
    }
  }
}

function toSqliteValues(params: readonly unknown[]): SQLInputValue[] {
  return params.map((value) => {
    if (value === undefined) return null
    if (typeof value === 'boolean') return value ? 1 : 0
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'bigint' || value === null || value instanceof Uint8Array) {
      return value
    }
    throw new Error(`SQLite parameter type is not supported by DatabaseClient: ${typeof value}`)
  })
}

async function queryPostgres(client: PostgresQueryClient, sql: string, params: readonly unknown[]): Promise<{ rows: Array<Record<string, unknown>>; rowCount?: number | null }> {
  const result = params.length > 0
    ? client.query(sql, params)
    : client.query(sql)
  return normalizePostgresQueryResult(await result)
}

function normalizePostgresQueryResult(result: Awaited<ReturnType<PostgresQueryClient['query']>> | Array<Awaited<ReturnType<PostgresQueryClient['query']>>>): { rows: Array<Record<string, unknown>>; rowCount?: number | null } {
  if (Array.isArray(result)) {
    const lastResult = result[result.length - 1]
    return {
      rows: lastResult?.rows ?? [],
      rowCount: result.reduce((total, item) => total + Number(item.rowCount ?? 0), 0)
    }
  }
  return result
}

const postgresNumericResultFieldNames = new Set([
  'active',
  'blocked_targets',
  'canceled',
  'completed',
  'count',
  'failed',
  'failed_targets',
  'found',
  'index_count',
  'lag_seconds',
  'metric_value',
  'pending',
  'pending_targets',
  'ready',
  'running',
  'schema_version',
  'success',
  'total'
])

const postgresNumericResultFieldPatterns = [
  /(^|_)average$/,
  /(^|_)avg$/,
  /(^|_)backlog$/,
  /(^|_)bindings$/,
  /(^|_)bytes$/,
  /(^|_)cost$/,
  /(^|_)count$/,
  /(^|_)days$/,
  /(^|_)duration$/,
  /(^|_)hits$/,
  /(^|_)hours$/,
  /(^|_)lag$/,
  /(^|_)latency$/,
  /(^|_)length$/,
  /(^|_)max$/,
  /(^|_)members$/,
  /(^|_)min$/,
  /(^|_)ms$/,
  /(^|_)offset$/,
  /(^|_)pages$/,
  /(^|_)percent$/,
  /(^|_)rank$/,
  /(^|_)rate$/,
  /(^|_)rows$/,
  /(^|_)score$/,
  /(^|_)seconds$/,
  /(^|_)size$/,
  /(^|_)sum$/,
  /(^|_)targets$/,
  /(^|_)tokens$/,
  /(^|_)total$/,
  /(^|_)usd$/
]

function normalizePostgresRows(rows: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
  return rows.map((row) => {
    let normalized: Record<string, unknown> | undefined
    for (const [key, value] of Object.entries(row)) {
      if (value instanceof Date && Number.isFinite(value.getTime())) {
        normalized ??= { ...row }
        normalized[key] = value.toISOString()
        continue
      }
      const numericValue = postgresNumericResultValue(key, value)
      if (numericValue === value) continue
      normalized ??= { ...row }
      normalized[key] = numericValue
    }
    return normalized ?? row
  })
}

function postgresNumericResultValue(key: string, value: unknown): unknown {
  if (!isPostgresNumericResultField(key)) return value
  if (typeof value === 'number') return Number.isFinite(value) ? value : value
  if (typeof value === 'bigint') return Number(value)
  if (typeof value !== 'string') return value
  const trimmed = value.trim()
  if (!trimmed) return value
  const numberValue = Number(trimmed)
  return Number.isFinite(numberValue) ? numberValue : value
}

function isPostgresNumericResultField(key: string): boolean {
  const normalizedKey = camelToSnakeCase(key)
  if (postgresNumericResultFieldNames.has(normalizedKey)) return true
  return postgresNumericResultFieldPatterns.some((pattern) => pattern.test(normalizedKey))
}

function camelToSnakeCase(value: string): string {
  return value.replace(/([a-z0-9])([A-Z])/g, '$1_$2').replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2').toLowerCase()
}

function normalizedPlaceholderCount(count: number): number {
  if (!Number.isInteger(count) || count < 0) {
    throw new Error(`invalid placeholder count: ${count}`)
  }
  return count
}

function normalizePlaceholderIndex(index: number): number {
  if (!Number.isInteger(index) || index < 1) {
    throw new Error(`invalid placeholder index: ${index}`)
  }
  return index
}

function quoteIdentifier(identifier: string): string {
  const normalized = identifier.trim()
  if (!normalized) {
    throw new Error('identifier cannot be empty')
  }
  return `"${normalized.replace(/"/g, '""')}"`
}
