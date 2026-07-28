import { AsyncLocalStorage } from 'node:async_hooks'
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

interface PostgresTransactionQueryState {
  tail: Promise<unknown>
}

interface SqliteTransactionContext {
  database: DatabaseSync
  active: boolean
}

const sqliteTransactionContext = new AsyncLocalStorage<SqliteTransactionContext>()
const sqliteTransactionTails = new WeakMap<DatabaseSync, Promise<void>>()

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
  return createSqliteDatabaseClientInternal(database)
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

function createSqliteDatabaseClientInternal(database: DatabaseSync): DatabaseClient {
  const client: DatabaseClient = {
    driver: 'sqlite',
    dialect: sqliteDialect,
    async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      const transactionBoundary = sqliteTransactionBoundary(database)
      if (transactionBoundary) await transactionBoundary
      const bound = sqliteDialect.bind(sql, params)
      return database.prepare(bound.sql).all(...toSqliteValues(bound.params)) as unknown as T[]
    },
    async one<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      const transactionBoundary = sqliteTransactionBoundary(database)
      if (transactionBoundary) await transactionBoundary
      const bound = sqliteDialect.bind(sql, params)
      return database.prepare(bound.sql).get(...toSqliteValues(bound.params)) as unknown as T | undefined
    },
    async execute(sql: string, params: readonly unknown[] = []): Promise<ExecuteResult> {
      const transactionBoundary = sqliteTransactionBoundary(database)
      if (transactionBoundary) await transactionBoundary
      const bound = sqliteDialect.bind(sql, params)
      const result = database.prepare(bound.sql).run(...toSqliteValues(bound.params))
      return {
        changes: Number(result.changes ?? 0),
        lastInsertRowid: result.lastInsertRowid
      }
    },
    async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
      const activeContext = sqliteTransactionContext.getStore()
      if (activeContext?.active && activeContext.database === database) {
        return operation(createSqliteDatabaseClientInternal(database))
      }
      return enqueueSqliteTransaction(database, operation)
    }
  }
  return client
}

function enqueueSqliteTransaction<T>(
  database: DatabaseSync,
  operation: (tx: DatabaseClient) => Promise<T>
): Promise<T> {
  const previous = sqliteTransactionTails.get(database) ?? Promise.resolve()
  const run = previous.catch(() => undefined).then(async () => {
    const context: SqliteTransactionContext = { database, active: true }
    return sqliteTransactionContext.run(context, async () => {
      let transactionStarted = false
      try {
        database.exec('BEGIN IMMEDIATE')
        transactionStarted = true
        const result = await operation(createSqliteDatabaseClientInternal(database))
        database.exec('COMMIT')
        transactionStarted = false
        return result
      } catch (error) {
        if (transactionStarted && database.isTransaction) {
          try {
            database.exec('ROLLBACK')
          } catch {
            // Preserve the operation failure that caused the rollback.
          }
        }
        throw error
      } finally {
        context.active = false
      }
    })
  })
  const tail = run.then(() => undefined, () => undefined)
  sqliteTransactionTails.set(database, tail)
  void tail.then(() => {
    if (sqliteTransactionTails.get(database) === tail) {
      sqliteTransactionTails.delete(database)
    }
  })
  return run
}

function sqliteTransactionBoundary(database: DatabaseSync): Promise<void> | undefined {
  const activeContext = sqliteTransactionContext.getStore()
  if (activeContext?.active && activeContext.database === database) return undefined
  return sqliteTransactionTails.get(database)
}

function createPostgresDatabaseClientInternal(
  client: PostgresPoolLike,
  transactionActive: boolean,
  transactionQueryState?: PostgresTransactionQueryState
): DatabaseClient {
  return {
    driver: 'postgres',
    dialect: postgresDialect,
    async query<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T[]> {
      const bound = postgresDialect.bind(sql, params)
      const result = await queryPostgresForClient(client, bound.sql, bound.params, transactionQueryState)
      return normalizePostgresRows(result.rows) as T[]
    },
    async one<T extends object = Record<string, unknown>>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      const rows = await this.query<T>(sql, params)
      return rows[0]
    },
    async execute(sql: string, params: readonly unknown[] = []): Promise<ExecuteResult> {
      const bound = postgresDialect.bind(sql, params)
      const result = await queryPostgresForClient(client, bound.sql, bound.params, transactionQueryState)
      return {
        changes: Number(result.rowCount ?? result.rows.length ?? 0)
      }
    },
    async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
      if (transactionActive) {
        return operation(createPostgresDatabaseClientInternal(client, true, transactionQueryState))
      }
      if (!client.connect) {
        throw new Error('PostgreSQL transaction requires a pool client with connect()')
      }
      const connection = await client.connect()
      let released = false
      let connectionFailure: Error | undefined
      let rejectConnectionError: ((error: Error) => void) | undefined
      const connectionError = new Promise<never>((_resolve, reject) => {
        rejectConnectionError = reject
      })
      // The connection can emit while BEGIN / SET LOCAL is still in flight,
      // before operation() is raced below. Mark the deferred rejection handled
      // immediately while retaining the original promise for the later race.
      void connectionError.catch(() => undefined)
      const onConnectionError = (error: Error): void => {
        connectionFailure = error
        rejectConnectionError?.(error)
      }
      connection.on?.('error', onConnectionError)
      const releaseConnection = (): void => {
        if (released) return
        released = true
        connection.release(connectionFailure)
        if (connection.off) {
          connection.off('error', onConnectionError)
        } else {
          connection.removeListener?.('error', onConnectionError)
        }
      }
      try {
        await connection.query('BEGIN')
        await connection.query(postgresTransactionLocalTimeoutSetSql())
        const queryState: PostgresTransactionQueryState = { tail: Promise.resolve() }
        const tx = createPostgresDatabaseClientInternal(connection, true, queryState)
        const result = await Promise.race([operation(tx), connectionError])
        await queryState.tail
        await connection.query('COMMIT')
        return result
      } catch (error) {
        try {
          await connection.query('ROLLBACK')
        } catch (rollbackError) {
          // A server-terminated or already closed transaction connection cannot
          // accept ROLLBACK. Preserve the first failure that explains why the
          // transaction was abandoned; release() will discard the bad client.
          connectionFailure = rollbackError instanceof Error
            ? rollbackError
            : new Error(String(rollbackError))
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

function queryPostgresForClient(
  client: PostgresQueryClient,
  sql: string,
  params: readonly unknown[],
  transactionQueryState?: PostgresTransactionQueryState
): Promise<{ rows: Array<Record<string, unknown>>; rowCount?: number | null }> {
  if (!transactionQueryState) {
    return queryPostgres(client, sql, params)
  }

  // node-postgres only supports one active query per checked-out connection.
  // Repository helpers may intentionally use Promise.all when backed by a pool;
  // the same helper can also receive a transaction client. Serialize only the
  // transaction-bound connection so those callers cannot overlap protocol
  // messages, leave the transaction idle, or race COMMIT with an unawaited tail.
  const result = transactionQueryState.tail.then(() => queryPostgres(client, sql, params))
  transactionQueryState.tail = result
  return result
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
