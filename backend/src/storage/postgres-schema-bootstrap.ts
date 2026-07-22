import type { DatabaseClient } from './database-client.js'

export interface InitializePostgresSchemaWithGooseOptions {
  client: Pick<DatabaseClient, 'one' | 'execute'>
  expectedVersion: number
  schemaOnly: boolean
  migrate: () => Promise<void>
  verify: () => Promise<void>
  applySchema: () => Promise<{ schemaCount: number; statementCount: number }>
  seed: () => Promise<{ statementCount: number }>
}

const bootstrapLockSQL = `SELECT pg_try_advisory_lock(hashtextextended('juhe-ai:postgres-schema-bootstrap', 0)) AS acquired`
const bootstrapUnlockSQL = `SELECT pg_advisory_unlock(hashtextextended('juhe-ai:postgres-schema-bootstrap', 0)) AS released`

const juheRelationCountSQL = `
SELECT COUNT(*)::text AS relation_count
FROM pg_catalog.pg_class c
INNER JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname LIKE 'juhe\_%' ESCAPE '\\'
  AND c.relkind IN ('r', 'p', 'v', 'm', 'i', 'S')
`

const currentGooseVersionSQL = `
WITH latest_versions AS (
  SELECT DISTINCT ON (version_id)
    id,
    version_id,
    is_applied
  FROM public.goose_db_version
  ORDER BY version_id, id DESC
)
SELECT version_id::text AS version_id, is_applied
FROM latest_versions
WHERE is_applied = TRUE
ORDER BY id DESC
LIMIT 1
`

const goosePresenceSQL = `
SELECT to_regclass('public.goose_db_version')::text AS goose_table
`

export async function validatePostgresSchemaBootstrapSource(
  client: Pick<DatabaseClient, 'one'>,
  expectedVersion: number
): Promise<void> {
  assertPositiveInteger(expectedVersion, 'expectedVersion')

  const presence = await client.one<{ goose_table: string | null }>(goosePresenceSQL)
  if (presence?.goose_table) {
    const current = await client.one<{ version_id: string; is_applied: boolean }>(currentGooseVersionSQL)
    if (!current) {
      throw new Error('PostgreSQL 已有 goose_db_version，但没有已应用版本记录')
    }
    const version = Number(current.version_id)
    if (!Number.isInteger(version) || version < 0 || current.is_applied !== true) {
      throw new Error(`PostgreSQL Goose schema 版本无效：${current.version_id}/${String(current.is_applied)}`)
    }
    if (version > expectedVersion) {
      throw new Error(`Goose 已应用高版本 ${version}，当前 catalog 为 ${expectedVersion}`)
    }
    return
  }

  const relationCountRow = await client.one<{ relation_count: string }>(juheRelationCountSQL)
  const relationCount = Number(relationCountRow?.relation_count ?? 0)
  if (!Number.isInteger(relationCount) || relationCount < 0) {
    throw new Error(`PostgreSQL juhe_ 业务对象计数无效：${String(relationCountRow?.relation_count)}`)
  }
  if (relationCount > 0) {
    throw new Error('没有 Goose 账本但已存在 juhe_ 业务对象；拒绝伪造 Goose 版本，请先备份并重建或先完成真实 migration')
  }
}

export async function initializePostgresSchemaWithGoose(
  options: InitializePostgresSchemaWithGooseOptions
): Promise<{ schemaCount: number; schemaStatementCount: number; seedStatementCount: number }> {
  assertPositiveInteger(options.expectedVersion, 'expectedVersion')
  const lock = await options.client.one<{ acquired: boolean }>(bootstrapLockSQL)
  if (lock?.acquired !== true) {
    throw new Error('已有 PostgreSQL schema 初始化正在执行')
  }

  let operationError: unknown
  let result: { schemaCount: number; schemaStatementCount: number; seedStatementCount: number } | undefined
  try {
    await validatePostgresSchemaBootstrapSource(options.client, options.expectedVersion)
    await options.migrate()
    await options.verify()
    const schemaResult = await options.applySchema()
    const seedResult = options.schemaOnly
      ? { statementCount: 0 }
      : await options.seed()
    result = {
      schemaCount: schemaResult.schemaCount,
      schemaStatementCount: schemaResult.statementCount,
      seedStatementCount: seedResult.statementCount
    }
  } catch (error) {
    operationError = error
  }

  let unlockError: unknown
  try {
    const unlock = await options.client.one<{ released: boolean }>(bootstrapUnlockSQL)
    if (unlock?.released !== true) {
      throw new Error('PostgreSQL schema 初始化 advisory lock 未释放')
    }
  } catch (error) {
    unlockError = error
  }

  if (operationError !== undefined && unlockError !== undefined) {
    throw new AggregateError([operationError, unlockError], 'PostgreSQL schema 初始化与 advisory lock 释放均失败')
  }
  if (operationError !== undefined) throw operationError
  if (unlockError !== undefined) throw unlockError
  if (!result) throw new Error('PostgreSQL schema 初始化未生成结果')
  return result
}

function assertPositiveInteger(value: number, label: string): void {
  if (!Number.isInteger(value) || value < 1) {
    throw new Error(`${label} must be a positive integer`)
  }
}
