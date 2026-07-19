import { runtimeConfig, type DatabaseDriver } from '../config/runtime.js'

export const EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION = 59
export const POSTGRES_GOOSE_CURRENT_VERSION_QUERY = `
  WITH latest_versions AS (
    SELECT DISTINCT ON (version_id) id, version_id, is_applied
    FROM goose_db_version
    ORDER BY version_id, id DESC
  )
  SELECT version_id::text, is_applied
  FROM latest_versions
  WHERE is_applied = TRUE
  ORDER BY id DESC
  LIMIT 1
`

const POSTGRES_GOOSE_NEWER_APPLIED_VERSION_QUERY = `
  WITH latest_versions AS (
    SELECT DISTINCT ON (version_id) id, version_id, is_applied
    FROM goose_db_version
    ORDER BY version_id, id DESC
  )
  SELECT version_id::text, is_applied
  FROM latest_versions
  WHERE version_id > $1 AND is_applied = TRUE
  ORDER BY id DESC
  LIMIT 1
`

interface PostgresGooseSchemaRow {
  version_id: string
  is_applied: boolean
}

export interface PostgresGooseSchemaState {
  currentRows: PostgresGooseSchemaRow[]
  newerAppliedRows: PostgresGooseSchemaRow[]
}

export interface PostgresGooseSchemaGateConfig {
  ownerLock: {
    enabled: boolean
  }
  databaseDriver: DatabaseDriver
  postgres: {
    url?: string
    connectionTimeoutMs: number
    statementTimeoutMs: number
    lockTimeoutMs: number
  }
}

export interface PostgresGooseSchemaGatePoolConfig {
  connectionString: string
  connectionTimeoutMillis: number
  max: number
  statement_timeout: number
  lock_timeout: number
}

export interface PostgresGooseSchemaGatePool {
  query(
    text: string,
    values?: readonly unknown[]
  ): Promise<{ rows: PostgresGooseSchemaRow[] }>
  end(): Promise<void>
}

type PostgresGooseSchemaGatePoolFactory = (
  config: PostgresGooseSchemaGatePoolConfig
) => PostgresGooseSchemaGatePool | Promise<PostgresGooseSchemaGatePool>

export function validatePostgresGooseSchemaState(state: PostgresGooseSchemaState): void {
  const current = state.currentRows[0]
  if (!current) {
    throw new Error('PostgreSQL Goose schema 版本记录不存在')
  }

  if (current.version_id !== String(EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION) || current.is_applied !== true) {
    throw new Error(
      `PostgreSQL Goose schema 当前版本必须为 ${EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION} 且已应用，实际为 ${current.version_id}/${String(current.is_applied)}`
    )
  }

  const newerApplied = state.newerAppliedRows.find((row) => (
    row.is_applied === true && Number(row.version_id) > EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION
  ))
  if (newerApplied) {
    throw new Error(
      `PostgreSQL Goose schema 存在已应用的高版本 ${newerApplied.version_id}，期望版本为 ${EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION}`
    )
  }
}

export async function enforcePostgresGooseSchemaGate(
  config: PostgresGooseSchemaGateConfig = runtimeConfig,
  createPool: PostgresGooseSchemaGatePoolFactory = createPostgresGooseSchemaGatePool
): Promise<void> {
  if (!config.ownerLock.enabled || config.databaseDriver !== 'postgres') {
    return
  }

  const connectionString = config.postgres.url?.trim()
  if (!connectionString) {
    throw new Error('PostgreSQL Goose schema 启动门禁缺少数据库连接配置')
  }

  const pool = await createPool({
    connectionString,
    connectionTimeoutMillis: config.postgres.connectionTimeoutMs,
    max: 1,
    statement_timeout: config.postgres.statementTimeoutMs,
    lock_timeout: config.postgres.lockTimeoutMs
  })

  let operationFailed = false
  let operationError: unknown
  try {
    const currentResult = await pool.query(POSTGRES_GOOSE_CURRENT_VERSION_QUERY)
    const newerAppliedResult = await pool.query(
      POSTGRES_GOOSE_NEWER_APPLIED_VERSION_QUERY,
      [EXPECTED_POSTGRES_GOOSE_SCHEMA_VERSION]
    )
    validatePostgresGooseSchemaState({
      currentRows: currentResult.rows,
      newerAppliedRows: newerAppliedResult.rows
    })
  } catch (error) {
    operationFailed = true
    operationError = error
  }

  let closeFailed = false
  let closeError: unknown
  try {
    await pool.end()
  } catch (error) {
    closeFailed = true
    closeError = error
  }

  if (operationFailed && closeFailed) {
    throw new AggregateError(
      [operationError, closeError],
      'PostgreSQL Goose schema 启动门禁执行与连接池关闭均失败'
    )
  }
  if (operationFailed) {
    throw operationError
  }
  if (closeFailed) {
    throw closeError
  }
}

async function createPostgresGooseSchemaGatePool(
  config: PostgresGooseSchemaGatePoolConfig
): Promise<PostgresGooseSchemaGatePool> {
  const { Pool } = await import('pg')
  return new Pool(config) as unknown as PostgresGooseSchemaGatePool
}
