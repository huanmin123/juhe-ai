import { runtimeConfig, type DatabaseDriver, type PostgresSchemaOwner } from '../config/runtime.js'
import {
  enforcePostgresGooseSchemaGate,
  type PostgresGooseSchemaGatePool,
  type PostgresGooseSchemaGatePoolConfig,
  type PostgresGooseSchemaGatePoolFactory
} from './postgres-goose-schema-gate.js'

/**
 * Node-owned schema contract for the current juhe-ai release.
 *
 * This is deliberately a read-only adoption/preflight contract. It does not
 * create tables, update goose_db_version, or infer a Goose history from an
 * already-populated Node database.
 */
export const NODE_POSTGRES_SCHEMA_CONTRACT_VERSION = 94

export const POSTGRES_NODE_SCHEMA_PREFLIGHT_QUERY = `
SELECT
  ARRAY_REMOVE(ARRAY[
    CASE WHEN to_regclass('juhe_business.accounts') IS NULL THEN 'juhe_business.accounts' END,
    CASE WHEN to_regclass('juhe_business.api_keys') IS NULL THEN 'juhe_business.api_keys' END,
    CASE WHEN to_regclass('juhe_business.account_circuit_incidents') IS NULL THEN 'juhe_business.account_circuit_incidents' END,
    CASE WHEN to_regclass('juhe_business.account_health_jobs_input_versions') IS NULL THEN 'juhe_business.account_health_jobs_input_versions' END,
    CASE WHEN to_regclass('juhe_business.account_balance_projection_cursors') IS NULL THEN 'juhe_business.account_balance_projection_cursors' END
  ], NULL) AS missing_relations,
  ARRAY_REMOVE(ARRAY[
    CASE WHEN NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_attribute a
      INNER JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
      INNER JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'juhe_business'
        AND c.relname = 'account_circuit_incidents'
        AND a.attname = 'confirmation_failures_required'
        AND a.attnum > 0
        AND NOT a.attisdropped
    ) THEN 'juhe_business.account_circuit_incidents.confirmation_failures_required' END,
    CASE WHEN NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_attribute a
      INNER JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
      INNER JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'juhe_business'
        AND c.relname = 'account_circuit_incidents'
        AND a.attname = 'confirmation_failure_evidence_keys_json'
        AND a.attnum > 0
        AND NOT a.attisdropped
    ) THEN 'juhe_business.account_circuit_incidents.confirmation_failure_evidence_keys_json' END,
    CASE WHEN NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_attribute a
      INNER JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
      INNER JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'juhe_business'
        AND c.relname = 'account_test_tasks'
        AND a.attname = 'queued_deadline_at'
        AND a.attnum > 0
        AND NOT a.attisdropped
    ) THEN 'juhe_business.account_test_tasks.queued_deadline_at' END
  ], NULL) AS missing_columns,
  ARRAY_REMOVE(ARRAY[
    CASE WHEN NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_indexes
      WHERE schemaname = 'juhe_business'
        AND indexname = 'idx_accounts_balance_auto_detect_due'
    ) THEN 'juhe_business.idx_accounts_balance_auto_detect_due' END
  ], NULL) AS missing_indexes,
  (to_regclass('public.goose_db_version') IS NOT NULL) AS goose_ledger_present
`

export interface PostgresNodeSchemaPreflightRow {
  missing_relations: string[]
  missing_columns: string[]
  missing_indexes: string[]
  goose_ledger_present: boolean
}

export interface PostgresNodeSchemaPreflightState {
  missingRelations: string[]
  missingColumns: string[]
  missingIndexes: string[]
  gooseLedgerPresent: boolean
}

export interface PostgresSchemaOwnerGateConfig {
  ownerLock: { enabled: boolean }
  schemaOwner?: PostgresSchemaOwner
  databaseDriver: DatabaseDriver
  postgres: {
    schemaOwner?: PostgresSchemaOwner
    url?: string
    connectionTimeoutMs: number
    statementTimeoutMs: number
    lockTimeoutMs: number
  }
}

export type PostgresSchemaOwnerGatePool = PostgresGooseSchemaGatePool

type PostgresSchemaOwnerGatePoolFactory = PostgresGooseSchemaGatePoolFactory

export function validatePostgresNodeSchemaPreflight(
  state: PostgresNodeSchemaPreflightState
): void {
  const missing = [
    ...state.missingRelations,
    ...state.missingColumns,
    ...state.missingIndexes
  ]
  if (missing.length > 0) {
    throw new Error(`Node PostgreSQL schema contract ${NODE_POSTGRES_SCHEMA_CONTRACT_VERSION} 不完整：${missing.join(', ')}`)
  }
  if (state.gooseLedgerPresent) {
    throw new Error('Node-owned PostgreSQL schema 不得同时存在 Goose ledger；请先完成 owner 切换契约')
  }
}

export async function checkPostgresNodeSchemaPreflight(
  pool: { query(text: string): Promise<{ rows: unknown[] }> }
): Promise<PostgresNodeSchemaPreflightState> {
  const result = await pool.query(POSTGRES_NODE_SCHEMA_PREFLIGHT_QUERY)
  const row = result.rows[0] as Partial<PostgresNodeSchemaPreflightRow> | undefined
  if (!row) {
    throw new Error('Node PostgreSQL schema preflight 未返回检查结果')
  }
  const state: PostgresNodeSchemaPreflightState = {
    missingRelations: Array.isArray(row.missing_relations) ? row.missing_relations : [],
    missingColumns: Array.isArray(row.missing_columns) ? row.missing_columns : [],
    missingIndexes: Array.isArray(row.missing_indexes) ? row.missing_indexes : [],
    gooseLedgerPresent: row.goose_ledger_present === true
  }
  validatePostgresNodeSchemaPreflight(state)
  return state
}

export async function enforcePostgresSchemaOwnerGate(
  config: PostgresSchemaOwnerGateConfig = runtimeConfig,
  createPool: PostgresSchemaOwnerGatePoolFactory = createPostgresSchemaOwnerGatePool
): Promise<void> {
  if (!config.ownerLock.enabled || config.databaseDriver !== 'postgres') {
    return
  }

  const schemaOwner = config.schemaOwner ?? config.postgres.schemaOwner
  if (!schemaOwner) {
    throw new Error('PostgreSQL schema owner 未显式配置；请设置 JUHE_AI_POSTGRES_SCHEMA_OWNER=node 或 goose')
  }
  if (schemaOwner === 'goose') {
    await enforcePostgresGooseSchemaGate(config, createPool)
    return
  }
  if (schemaOwner !== 'node') {
    throw new Error(`未知 PostgreSQL schema owner：${String(schemaOwner)}`)
  }

  const connectionString = config.postgres.url?.trim()
  if (!connectionString) {
    throw new Error('Node PostgreSQL schema preflight 缺少数据库连接配置')
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
    await checkPostgresNodeSchemaPreflight(pool)
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
      'Node PostgreSQL schema preflight 执行与连接池关闭均失败'
    )
  }
  if (operationFailed) throw operationError
  if (closeFailed) throw closeError
}

async function createPostgresSchemaOwnerGatePool(
  config: PostgresGooseSchemaGatePoolConfig
): Promise<PostgresSchemaOwnerGatePool> {
  const { Pool } = await import('pg')
  return new Pool(config) as unknown as PostgresSchemaOwnerGatePool
}
