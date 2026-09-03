import { pathToFileURL } from 'node:url'

import pg from 'pg'

type Queryable = Pick<pg.Client, 'query'>

export interface AccountSyncTablePolicy {
  name: string
  purpose: 'configuration' | 'runtime-reset'
  sensitiveColumns: string[]
  specialHandling: string
}

export interface AccountSyncDatabaseIdentity {
  databaseName: string
  databaseOid: string
}

export interface AccountSyncTableReport {
  name: string
  purpose: AccountSyncTablePolicy['purpose']
  sourceExists: boolean
  targetExists: boolean
  sourceRows: number | null
  targetRows: number | null
  sourceColumns: AccountSyncColumnMetadata[]
  targetColumns: AccountSyncColumnMetadata[]
  sourceNotNullColumns: string[]
  targetNotNullColumns: string[]
  sourceForeignKeys: AccountSyncForeignKey[]
  targetForeignKeys: AccountSyncForeignKey[]
  foreignKeyDifferences: string[]
  foreignKeysOutsidePolicy: string[]
  missingTargetColumns: string[]
  unexpectedTargetColumns: string[]
  targetRequiredColumnsWithoutDefault: string[]
  columnDifferences: AccountSyncColumnDifference[]
  sensitiveColumns: string[]
  specialHandling: string
}

export interface AccountSyncForeignKey {
  constraintName: string
  parentSchema: string
  parentTable: string
  definition: string
}

export interface AccountSyncColumnMetadata {
  name: string
  dataType: string
  udtName: string
  isNullable: boolean
  defaultExpression: string | null
}

export interface AccountSyncColumnDifference {
  name: string
  source: AccountSyncColumnMetadata
  target: AccountSyncColumnMetadata
}

export interface AccountSyncPreflightReport {
  schemaVersion: 1
  mode: 'plan'
  source: AccountSyncDatabaseIdentity
  target: AccountSyncDatabaseIdentity
  targetNameAccepted: boolean
  targetNamePattern: string
  tableReports: AccountSyncTableReport[]
  runtimeResetReports: RuntimeResetTableReport[]
  auxiliaryRuntimeResetReports: AuxiliaryRuntimeResetReport[]
  blockers: string[]
  status: 'passed' | 'blocked'
}

export interface RuntimeResetTableReport {
  name: string
  sourceExists: boolean
  targetExists: boolean
  sourceRows: number | null
  targetRows: number | null
}

export interface AuxiliaryRuntimeResetReport {
  schema: string
  name: string
  sourceExists: boolean
  targetExists: boolean
  sourceRows: number | null
  targetRows: number | null
}

const businessSchema = 'juhe_business'
const targetDatabasePattern = /^juhe_ai_test_rehearsal_[a-z0-9_]{3,48}$/u

// This is intentionally a policy manifest, not an implicit "copy every table"
// list. Runtime and secret-bearing tables must be handled explicitly before an
// execution phase is added.
export const ACCOUNT_SYNC_TABLE_POLICIES: readonly AccountSyncTablePolicy[] = [
  policy('system_accounts', 'configuration', ['password_hash'], '保留 ID/username/角色/显示名；password_hash 必须使用 test 密码重新生成'),
  policy('providers', 'configuration', [], 'provider.parent_code 自引用外键必须按 parent-before-child 拓扑导入，缺失父节点或环引用必须阻断'),
  policy('protocols', 'configuration'),
  policy('protocol_endpoint_families', 'configuration'),
  policy('provider_protocol_profiles', 'configuration'),
  policy('provider_protocol_profile_families', 'configuration'),
  policy('provider_model_catalog', 'configuration'),
  policy('custom_provider_models', 'configuration'),
  policy('provider_default_health_check_models', 'configuration'),
  policy('provider_system_default_health_check_models', 'configuration'),
  policy('proxy_profiles', 'configuration', ['password_encrypted'], 'proxy 密码必须使用 test canary 或隔离重加密结果'),
  policy('model_quality_policies', 'configuration'),
  policy('model_quality_schedules', 'configuration', [], '只允许 canary；导入时 enabled=false，next_run_at 设为受控时间'),
  policy('account_quality_enforcements', 'runtime-reset', [], '只建结构并清空；由 owner 在 smoke 后重建'),
  policy('account_name_search_terms', 'runtime-reset', [], '只建结构并清空；由 owner 在 smoke 后重建'),
  policy('account_name_search_documents', 'runtime-reset', [], '只建结构并清空；由 owner 在 smoke 后重建'),
  policy('account_supported_models', 'configuration'),
  policy('account_model_mappings', 'configuration'),
  policy('account_tags', 'configuration'),
  policy('account_tag_bindings', 'configuration'),
  policy('system_teams', 'configuration'),
  policy('system_team_members', 'configuration'),
  policy('resource_authorizations', 'configuration'),
  policy('resource_authorization_sources', 'configuration'),
  policy('resource_authorization_grants', 'configuration'),
  policy(
    'accounts',
    'configuration',
    [
      'credentials_encrypted',
      'credential_fingerprint',
      'credential_mask',
      'oauth_access_token_expires_at',
      'oauth_refresh_token_present'
    ],
    '先导入普通 source accounts，再导入 authorization-instance accounts；凭据密文、指纹、掩码和 OAuth 元数据必须按 test canary/隔离重加密生成，不能复制生产派生值'
  ),
  policy('groups', 'configuration'),
  policy('group_authorization_settings', 'configuration'),
  policy('group_accounts', 'configuration'),
  policy('group_account_stats_dirty', 'runtime-reset', [], '只建结构并清空；由 owner 重建'),
  policy('route_strategies', 'configuration'),
  policy('route_strategy_groups', 'configuration'),
  policy('response_inspection_policies', 'configuration'),
  policy('global_settings', 'configuration'),
  policy('system_settings', 'configuration'),
  policy('request_quota_hourly_window_configs', 'configuration'),
  policy('request_quota_hourly_window_scope_bindings', 'configuration'),
  policy('api_keys', 'configuration', ['key_secret_encrypted'], '不得复制生产 key；test 中重新生成并只保存摘要'),
  policy('account_schedule_status_events', 'runtime-reset', [], '只建结构并清空，readback 必须为 0'),
  policy('api_key_schedule_status_events', 'runtime-reset', [], '只建结构并清空，readback 必须为 0')
]

const ACCOUNT_SYNC_POLICY_TABLE_NAMES = new Set(ACCOUNT_SYNC_TABLE_POLICIES.map((table) => table.name))

export const ACCOUNT_SYNC_RUNTIME_RESET_TABLES = [
  'account_lock_states',
  'account_circuit_incidents',
  'account_circuit_outbox',
  'account_api_key_runtime_states',
  'account_api_key_pool_probe_cursors',
  'account_quality_enforcements',
  'account_name_search_terms',
  'account_name_search_documents',
  'account_health_jobs_input_versions',
  'account_health_jobs_input_outbox',
  'account_health_projection_receipts',
  'account_health_projection_cursors',
  'account_balance_projection_cursors',
  'account_list_availability_projections',
  'account_list_availability_projection_index',
  'account_list_availability_projection_tags',
  'account_list_availability_projection_search_terms',
  'account_list_availability_projection_viewer_health',
  'account_list_availability_runtime_overlays',
  'account_list_availability_projection_dependency_health',
  'account_list_availability_dirty',
  'account_test_tasks',
  'account_test_sessions',
  'account_test_session_tasks',
  'proxy_latency_projection_receipts',
  'proxy_latency_projection_cursors',
  'account_schedule_status_events',
  'api_key_schedule_status_events'
] as const

// These stores live outside juhe_business but are consumed by the Node
// projection readers/schedulers. They must be structure-only and empty in a
// production-shaped rehearsal; copying their historical rows can make a
// rehearsal appear healthy while suppressing fresh upstream work.
export const AUXILIARY_RUNTIME_RESET_TABLES = [
  { schema: 'juhe_jobs', name: 'account_health_outcomes' },
  { schema: 'juhe_jobs', name: 'account_balance_outcomes' },
  { schema: 'juhe_stats', name: 'background_job_leases' }
] as const

export function assertTargetDatabaseName(databaseName: string): void {
  if (!targetDatabasePattern.test(databaseName)) {
    throw new Error(`目标数据库名必须匹配 ${targetDatabasePattern.source}，拒绝操作非隔离数据库`)
  }
}

export function assertDistinctDatabaseIdentities(source: AccountSyncDatabaseIdentity, target: AccountSyncDatabaseIdentity): void {
  if (source.databaseName === target.databaseName && source.databaseOid === target.databaseOid) {
    throw new Error('源库和目标库解析为同一 PostgreSQL 数据库，拒绝继续')
  }
}

export function validatePolicyManifest(policies: readonly AccountSyncTablePolicy[] = ACCOUNT_SYNC_TABLE_POLICIES): void {
  const names = policies.map((item) => item.name)
  if (new Set(names).size !== names.length) throw new Error('账户同步 manifest 存在重复表名')
  for (const item of policies) {
    if (!/^[a-z][a-z0-9_]*$/u.test(item.name)) throw new Error(`账户同步 manifest 表名无效：${item.name}`)
    if (item.sensitiveColumns.some((column) => !/^[a-z][a-z0-9_]*$/u.test(column))) {
      throw new Error(`账户同步 manifest 敏感列名无效：${item.name}`)
    }
    if (!item.specialHandling.trim()) throw new Error(`账户同步 manifest 缺少处理策略：${item.name}`)
  }
}

export async function collectAccountSyncPreflight(
  source: Queryable,
  target: Queryable,
  sourceIdentity: AccountSyncDatabaseIdentity,
  targetIdentity: AccountSyncDatabaseIdentity
): Promise<AccountSyncPreflightReport> {
  validatePolicyManifest()
  assertTargetDatabaseName(targetIdentity.databaseName)
  assertDistinctDatabaseIdentities(sourceIdentity, targetIdentity)

  const tableReports: AccountSyncTableReport[] = []
  const blockers: string[] = []
  for (const table of ACCOUNT_SYNC_TABLE_POLICIES) {
    const [[sourceMeta, targetMeta], [sourceForeignKeys, targetForeignKeys]] = await Promise.all([
      Promise.all([
        readTableMetadata(source, table.name),
        readTableMetadata(target, table.name)
      ]),
      Promise.all([
        readForeignKeys(source, table.name),
        readForeignKeys(target, table.name)
      ])
    ])
    const missingTargetColumns = sourceMeta.columns
      .map((column) => column.name)
      .filter((column) => !targetMeta.columns.some((targetColumn) => targetColumn.name === column))
    const unexpectedTargetColumns = targetMeta.columns
      .map((column) => column.name)
      .filter((columnName) => !sourceMeta.columns.some((sourceColumn) => sourceColumn.name === columnName))
    const targetRequiredColumnsWithoutDefault = targetMeta.columns
      .filter((column) => !sourceMeta.columns.some((sourceColumn) => sourceColumn.name === column.name))
      .filter((column) => !column.isNullable && column.defaultExpression === null)
      .map((column) => column.name)
    const columnDifferences = sourceMeta.columns
      .map((column) => {
        const targetColumn = targetMeta.columns.find((candidate) => candidate.name === column.name)
        if (!targetColumn || accountSyncColumnsEquivalent(column, targetColumn)) return null
        return { name: column.name, source: column, target: targetColumn }
      })
      .filter((difference): difference is AccountSyncColumnDifference => difference !== null)
    const foreignKeyDifferences = compareForeignKeySets(sourceForeignKeys, targetForeignKeys)
    const foreignKeysOutsidePolicy = findForeignKeysOutsidePolicyInEitherDatabase(
      sourceForeignKeys,
      targetForeignKeys,
      ACCOUNT_SYNC_POLICY_TABLE_NAMES
    )
    const report: AccountSyncTableReport = {
      name: table.name,
      purpose: table.purpose,
      sourceExists: sourceMeta.exists,
      targetExists: targetMeta.exists,
      sourceRows: sourceMeta.exists ? sourceMeta.rows : null,
      targetRows: targetMeta.exists ? targetMeta.rows : null,
      sourceColumns: sourceMeta.columns,
      targetColumns: targetMeta.columns,
      sourceNotNullColumns: sourceMeta.notNullColumns,
      targetNotNullColumns: targetMeta.notNullColumns,
      sourceForeignKeys,
      targetForeignKeys,
      foreignKeyDifferences,
      foreignKeysOutsidePolicy,
      missingTargetColumns,
      unexpectedTargetColumns,
      targetRequiredColumnsWithoutDefault,
      columnDifferences,
      sensitiveColumns: table.sensitiveColumns,
      specialHandling: table.specialHandling
    }
    tableReports.push(report)
    if (!report.sourceExists) blockers.push(`${table.name}: source table missing`)
    if (!report.targetExists) blockers.push(`${table.name}: target table missing`)
    if (missingTargetColumns.length > 0) blockers.push(`${table.name}: target missing columns ${missingTargetColumns.join(', ')}`)
    if (targetRequiredColumnsWithoutDefault.length > 0) {
      blockers.push(`${table.name}: target-only required columns without defaults ${targetRequiredColumnsWithoutDefault.join(', ')}`)
    }
    if (columnDifferences.length > 0) {
      blockers.push(`${table.name}: shared column definitions differ ${columnDifferences.map((difference) => difference.name).join(', ')}`)
    }
    if (foreignKeyDifferences.length > 0) {
      blockers.push(`${table.name}: foreign-key definitions differ ${foreignKeyDifferences.join(', ')}`)
    }
    if (foreignKeysOutsidePolicy.length > 0) {
      blockers.push(`${table.name}: foreign-key parents outside account sync policy ${foreignKeysOutsidePolicy.join(', ')}`)
    }
  }

  const runtimeResetReports: RuntimeResetTableReport[] = []
  for (const table of ACCOUNT_SYNC_RUNTIME_RESET_TABLES) {
    const [sourceMeta, targetMeta] = await Promise.all([
      readTableMetadata(source, table),
      readTableMetadata(target, table)
    ])
    runtimeResetReports.push({
      name: table,
      sourceExists: sourceMeta.exists,
      targetExists: targetMeta.exists,
      sourceRows: sourceMeta.exists ? sourceMeta.rows : null,
      targetRows: targetMeta.exists ? targetMeta.rows : null
    })
    // Runtime tables are structure-only in the rehearsal. A production source
    // may legitimately predate a newly introduced runtime table; in that case
    // there are no historical rows to copy. The schema three-way gate remains
    // responsible for proving that the candidate/test structure is complete.
    if (!targetMeta.exists) blockers.push(`${table}: target runtime table missing`)
  }

  const auxiliaryRuntimeResetReports: AuxiliaryRuntimeResetReport[] = []
  for (const table of AUXILIARY_RUNTIME_RESET_TABLES) {
    const [sourceMeta, targetMeta] = await Promise.all([
      readTableMetadata(source, table.name, table.schema),
      readTableMetadata(target, table.name, table.schema)
    ])
    auxiliaryRuntimeResetReports.push({
      schema: table.schema,
      name: table.name,
      sourceExists: sourceMeta.exists,
      targetExists: targetMeta.exists,
      sourceRows: sourceMeta.exists ? sourceMeta.rows : null,
      targetRows: targetMeta.exists ? targetMeta.rows : null
    })
    // As with juhe_business runtime tables, a missing source auxiliary store
    // means there is no runtime history to copy. The target structure and
    // candidate schema contract must still be verified separately.
    if (!targetMeta.exists) blockers.push(`${table.schema}.${table.name}: target auxiliary runtime table missing`)
  }

  return {
    schemaVersion: 1,
    mode: 'plan',
    source: sourceIdentity,
    target: targetIdentity,
    targetNameAccepted: true,
    targetNamePattern: targetDatabasePattern.source,
    tableReports,
    runtimeResetReports,
    auxiliaryRuntimeResetReports,
    blockers,
    status: blockers.length === 0 ? 'passed' : 'blocked'
  }
}

async function readTableMetadata(client: Queryable, table: string, schema = businessSchema): Promise<{
  exists: boolean
  rows: number
  columns: AccountSyncColumnMetadata[]
  notNullColumns: string[]
}> {
  const result = await client.query<{
    column_name: string
    data_type: string
    udt_name: string
    is_nullable: string
    column_default: string | null
  }>(`
    SELECT column_name, data_type, udt_name, is_nullable, column_default
    FROM information_schema.columns
    WHERE table_schema = $1 AND table_name = $2
    ORDER BY ordinal_position
  `, [schema, table])
  if (result.rows.length === 0) return { exists: false, rows: 0, columns: [], notNullColumns: [] }
  const count = await client.query<{ count: string }>(`
    SELECT count(*)::text AS count
    FROM ${quoteIdentifier(schema)}.${quoteIdentifier(table)}
  `)
  return {
    exists: true,
    rows: Number(count.rows[0]?.count ?? 0),
    columns: result.rows.map((row) => ({
      name: row.column_name,
      dataType: row.data_type,
      udtName: row.udt_name,
      isNullable: row.is_nullable === 'YES',
      defaultExpression: row.column_default
    })),
    notNullColumns: result.rows.filter((row) => row.is_nullable === 'NO').map((row) => row.column_name)
  }
}

async function readForeignKeys(client: Queryable, table: string, schema = businessSchema): Promise<AccountSyncForeignKey[]> {
  const result = await client.query<{
    constraint_name: string
    parent_schema: string
    parent_table: string
    definition: string
  }>(`
    SELECT con.conname AS constraint_name,
           parent_ns.nspname AS parent_schema,
           parent.relname AS parent_table,
           pg_get_constraintdef(con.oid, true) AS definition
    FROM pg_constraint con
    JOIN pg_class child ON child.oid = con.conrelid
    JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
    JOIN pg_class parent ON parent.oid = con.confrelid
    JOIN pg_namespace parent_ns ON parent_ns.oid = parent.relnamespace
    WHERE con.contype = 'f'
      AND child_ns.nspname = $1
      AND child.relname = $2
    ORDER BY con.conname
  `, [schema, table])
  return result.rows.map((row) => ({
    constraintName: row.constraint_name,
    parentSchema: row.parent_schema,
    parentTable: row.parent_table,
    definition: row.definition
  }))
}

export function foreignKeySignature(value: AccountSyncForeignKey): string {
  return [value.constraintName, value.parentSchema, value.parentTable, value.definition].join('|')
}

export function compareForeignKeySets(source: readonly AccountSyncForeignKey[], target: readonly AccountSyncForeignKey[]): string[] {
  const sourceSignatures = new Set(source.map(foreignKeySignature))
  const targetSignatures = new Set(target.map(foreignKeySignature))
  return [
    ...[...sourceSignatures].filter((value) => !targetSignatures.has(value)).map((value) => `missing-target:${value}`),
    ...[...targetSignatures].filter((value) => !sourceSignatures.has(value)).map((value) => `unexpected-target:${value}`)
  ].sort()
}

export function findForeignKeysOutsidePolicy(
  foreignKeys: readonly AccountSyncForeignKey[],
  policyTableNames: ReadonlySet<string>
): string[] {
  return foreignKeys
    .filter((foreignKey) => foreignKey.parentSchema !== businessSchema || !policyTableNames.has(foreignKey.parentTable))
    .map((foreignKey) => `${foreignKey.constraintName}|${foreignKey.parentSchema}.${foreignKey.parentTable}`)
    .sort()
}

/**
 * Check both sides of the rehearsal comparison. A shared out-of-policy FK is
 * still unsafe: the eventual field-level importer would need to populate its
 * parent table even when source and target happen to have identical DDL.
 */
export function findForeignKeysOutsidePolicyInEitherDatabase(
  sourceForeignKeys: readonly AccountSyncForeignKey[],
  targetForeignKeys: readonly AccountSyncForeignKey[],
  policyTableNames: ReadonlySet<string>
): string[] {
  return [...new Set([
    ...findForeignKeysOutsidePolicy(sourceForeignKeys, policyTableNames).map((value) => `source:${value}`),
    ...findForeignKeysOutsidePolicy(targetForeignKeys, policyTableNames).map((value) => `target:${value}`)
  ])].sort()
}

function accountSyncColumnsEquivalent(source: AccountSyncColumnMetadata, target: AccountSyncColumnMetadata): boolean {
  return source.dataType === target.dataType
    && source.udtName === target.udtName
    && source.isNullable === target.isNullable
    && source.defaultExpression === target.defaultExpression
}

function policy(
  name: string,
  purpose: AccountSyncTablePolicy['purpose'],
  sensitiveColumns: string[] = [],
  specialHandling = '按字段 allowlist 复制，导入后校验 FK/唯一键并记录行数摘要'
): AccountSyncTablePolicy {
  return { name, purpose, sensitiveColumns, specialHandling }
}

function quoteIdentifier(value: string): string {
  return `"${value.replaceAll('"', '""')}"`
}

async function main(): Promise<void> {
  if ((process.env.JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE ?? 'plan') !== 'plan') {
    throw new Error('账户同步 preflight 当前只支持 JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE=plan；执行阶段尚未实现')
  }
  const sourceUrl = requiredEnv('JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL')
  const targetUrl = requiredEnv('JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL')
  const source = new pg.Client({ connectionString: sourceUrl, application_name: 'juhe-ai-rehearsal-account-sync-source', connectionTimeoutMillis: 10_000 })
  const target = new pg.Client({ connectionString: targetUrl, application_name: 'juhe-ai-rehearsal-account-sync-target', connectionTimeoutMillis: 10_000 })
  let sourceConnected = false
  let targetConnected = false
  try {
    await Promise.all([
      source.connect().then(() => { sourceConnected = true }),
      target.connect().then(() => { targetConnected = true })
    ])
    await Promise.all([
      source.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY'),
      target.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY')
    ])
    await Promise.all([
      source.query("SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true)"),
      target.query("SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true)")
    ])
    const identitySql = `SELECT current_database() AS "databaseName", (SELECT oid::text FROM pg_database WHERE datname=current_database()) AS "databaseOid"`
    const [sourceResult, targetResult] = await Promise.all([source.query<AccountSyncDatabaseIdentity>(identitySql), target.query<AccountSyncDatabaseIdentity>(identitySql)])
    const sourceIdentity = sourceResult.rows[0]
    const targetIdentity = targetResult.rows[0]
    if (!sourceIdentity || !targetIdentity) throw new Error('无法读取源/目标数据库身份')
    const report = await collectAccountSyncPreflight(source, target, sourceIdentity, targetIdentity)
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)
    process.exitCode = report.status === 'passed' ? 0 : 3
    await Promise.all([source.query('COMMIT'), target.query('COMMIT')])
  } catch (error) {
    await Promise.allSettled([
      sourceConnected ? source.query('ROLLBACK') : Promise.resolve(),
      targetConnected ? target.query('ROLLBACK') : Promise.resolve()
    ])
    throw error
  } finally {
    await Promise.allSettled([
      sourceConnected ? source.end() : Promise.resolve(),
      targetConnected ? target.end() : Promise.resolve()
    ])
  }
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} 未配置`)
  return value
}

const entryPath = process.argv[1] ? pathToFileURL(process.argv[1]).href : ''
if (entryPath === import.meta.url) {
  void main().catch((error: unknown) => {
    console.error(`账户同步 preflight 失败：${error instanceof Error ? error.message : String(error)}`)
    process.exitCode = 1
  })
}
