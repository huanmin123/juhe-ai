import { runtimeConfig } from '../config/runtime.js'

export interface PostgresHealthCheckResult {
  ok: boolean
  latencyMs: number
  serverVersion?: string
}

export interface PostgresQueryResult {
  rows: Array<Record<string, unknown>>
  rowCount?: number | null
}

export interface PostgresQueryClient {
  query(text: string, values?: readonly unknown[]): Promise<PostgresQueryResult>
}

export interface PostgresPoolClient extends PostgresQueryClient {
  release(error?: Error): void
  on?(event: 'error', listener: (error: Error) => void): unknown
  off?(event: 'error', listener: (error: Error) => void): unknown
  removeListener?(event: 'error', listener: (error: Error) => void): unknown
}

type PostgresPool = PostgresQueryClient & {
  connect(): Promise<PostgresPoolClient>
  end(): Promise<void>
  on(event: string, listener: (...args: unknown[]) => void): void
}

let postgresPool: Promise<PostgresPool> | undefined

export function isPostgresConfigured(): boolean {
  return runtimeConfig.databaseDriver === 'postgres'
    && typeof runtimeConfig.postgres.url === 'string'
    && runtimeConfig.postgres.url.trim().length > 0
}

export async function getPostgresPool(): Promise<PostgresPool> {
  if (!isPostgresConfigured()) {
    throw new Error('当前运行模式未配置 PostgreSQL')
  }
  if (!postgresPool) {
    postgresPool = createPostgresPool()
  }
  return postgresPool
}

export async function checkPostgresHealth(): Promise<PostgresHealthCheckResult> {
  const startedAt = Date.now()
  try {
    const pool = await getPostgresPool()
    const result = await pool.query('SELECT version() AS version')
    const serverVersion = result.rows[0]?.version
    return {
      ok: true,
      latencyMs: Date.now() - startedAt,
      serverVersion: typeof serverVersion === 'string' ? serverVersion : undefined
    }
  } catch {
    return {
      ok: false,
      latencyMs: Date.now() - startedAt
    }
  }
}

export async function closePostgresPool(): Promise<void> {
  if (!postgresPool) return
  const pool = await postgresPool
  postgresPool = undefined
  await pool.end()
}

export function postgresApplicationName(): string {
  if (runtimeConfig.processRole === 'worker') {
    return `juhe-ai:${runtimeConfig.processRole}:${runtimeConfig.workerRole}`
  }
  return `juhe-ai:${runtimeConfig.processRole}`
}

export function postgresPoolTimeoutConfig(): {
  statement_timeout: number
  lock_timeout: number
  idle_in_transaction_session_timeout: number
} {
  return {
    statement_timeout: runtimeConfig.postgres.statementTimeoutMs,
    lock_timeout: runtimeConfig.postgres.lockTimeoutMs,
    idle_in_transaction_session_timeout: runtimeConfig.postgres.idleInTransactionSessionTimeoutMs
  }
}

async function createPostgresPool(): Promise<PostgresPool> {
  const { Pool } = await import('pg')
  const pool = new Pool({
    connectionString: runtimeConfig.postgres.url,
    // node-postgres has no separate max-idle setting. Keep the effective pool
    // bounded at ten so idle clients cannot exceed the platform policy.
    max: Math.min(runtimeConfig.postgres.poolMax, 10),
    idleTimeoutMillis: runtimeConfig.postgres.idleTimeoutMs,
    connectionTimeoutMillis: runtimeConfig.postgres.connectionTimeoutMs,
    application_name: postgresApplicationName()
  }) as unknown as PostgresPool
  pool.on('error', () => {
    // Pool level errors are surfaced by individual query promises and health checks.
  })
  return pool
}

export function postgresTransactionLocalSettingsSql(): string {
  const config = postgresPoolTimeoutConfig()
  const statements = [
    `SET LOCAL statement_timeout = ${config.statement_timeout}`,
    `SET LOCAL lock_timeout = ${config.lock_timeout}`,
    `SET LOCAL idle_in_transaction_session_timeout = ${config.idle_in_transaction_session_timeout}`
  ]
  if (!runtimeConfig.postgres.jitEnabled) {
    // PgBouncer transaction pooling rejects the libpq "options" startup
    // parameter. SET LOCAL is scoped to this transaction and cannot leak to
    // another client when the backend connection is returned to the pool.
    statements.push('SET LOCAL jit = off')
  }
  return statements.join('; ')
}
