import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'

import pg from 'pg'

import {
  ACCOUNT_SYNC_RUNTIME_RESET_TABLES,
  ACCOUNT_SYNC_TABLE_POLICIES,
  AUXILIARY_RUNTIME_RESET_TABLES,
  assertDistinctDatabaseIdentities,
  assertTargetDatabaseName,
  type AccountSyncDatabaseIdentity,
  type AccountSyncPreflightReport
} from './rehearsal-account-sync-preflight.js'
import {
  validateRehearsalAccountSyncPlanBinding,
  type RehearsalAccountSyncPlan,
  type RehearsalAccountSyncTablePlan
} from './validate-rehearsal-account-sync-plan.js'

type Row = Record<string, unknown>
type QueryClient = Pick<pg.Client, 'query'>

export interface ScopeManifest {
  schemaVersion: 1
  tables: Record<string, string[]>
  approvedCanaryAccountIds: string[]
  sourceAccountIds: string[]
  selectedAccountIds: string[]
  approvedCanaryAccountIdsHash: string
  approvedCanaryCount: number
  sourceAccountIdsHash: string
  selectedAccountIdsHash: string
}

export interface GeneratedValuesManifest {
  schemaVersion: 1
  tables: Record<string, Record<string, Record<string, unknown>>>
}

interface PrimaryKeyColumn {
  name: string
  ordinal: number
}

interface ExecutionTableReport {
  name: string
  sourceRows: number
  selectedRows: number
  insertedRows: number
  copiedDigest: string
  readbackDigest: string
}

interface ReadbackExpectation {
  rowKey: string
  values: Record<string, unknown>
  requiredNonNullColumns: string[]
}

export interface RehearsalAccountSyncExecutionReport {
  schemaVersion: 1
  mode: 'execute'
  source: AccountSyncDatabaseIdentity
  target: AccountSyncDatabaseIdentity
  targetNameAccepted: true
  approvedCanaryCount: number
  tableReports: ExecutionTableReport[]
  runtimeResetTables: Array<{ schema: string; name: string; beforeRows: number; afterRows: number; beforeChecksum: string; afterChecksum: string }>
  status: 'passed'
}

const businessSchema = 'juhe_business'
const confirmation = 'I_UNDERSTAND_TEST_TARGET_ONLY'
const sha256Pattern = /^[0-9a-f]{64}$/u

export function stableKey(values: readonly unknown[]): string {
  return JSON.stringify(values.map((value) => normalizeValue(value)))
}

export function hashStringList(values: readonly string[]): string {
  const hash = createHash('sha256')
  for (const value of [...values].sort()) hash.update(value).update('\n')
  return hash.digest('hex')
}

export function assertExecuteEnvironment(env: NodeJS.ProcessEnv): void {
  if (env.JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE !== 'execute') {
    throw new Error('执行账户同步必须显式设置 JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE=execute')
  }
  if (env.JUHE_AI_REHEARSAL_EXECUTE_CONFIRM !== confirmation) {
    throw new Error(`执行账户同步必须显式设置 JUHE_AI_REHEARSAL_EXECUTE_CONFIRM=${confirmation}`)
  }
  if (!env.JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL?.trim()) throw new Error('JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL 未配置')
  if (!env.JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL?.trim()) throw new Error('JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL 未配置')
}

export function validateScopeManifest(
  scope: ScopeManifest,
  plan: RehearsalAccountSyncPlan
): void {
  if (!scope || scope.schemaVersion !== 1 || !scope.tables || typeof scope.tables !== 'object') {
    throw new Error('scope manifest 必须是 schemaVersion=1 且包含 tables')
  }
  if (!Array.isArray(scope.approvedCanaryAccountIds) || scope.approvedCanaryAccountIds.length === 0) throw new Error('scope.approvedCanaryAccountIds 必须是非空数组')
  if (!Array.isArray(scope.sourceAccountIds)) throw new Error('scope.sourceAccountIds 必须是数组')
  if (!Array.isArray(scope.selectedAccountIds) || scope.selectedAccountIds.length === 0) throw new Error('scope.selectedAccountIds 必须是非空数组')
  if (scope.approvedCanaryAccountIds.some((value) => typeof value !== 'string' || !value.trim())) throw new Error('scope.approvedCanaryAccountIds 含无效账户 ID')
  if (scope.sourceAccountIds.some((value) => typeof value !== 'string' || !value.trim())) throw new Error('scope.sourceAccountIds 含无效账户 ID')
  if (scope.selectedAccountIds.some((value) => typeof value !== 'string' || !value.trim())) throw new Error('scope.selectedAccountIds 含无效账户 ID')
  if (new Set(scope.approvedCanaryAccountIds).size !== scope.approvedCanaryAccountIds.length) throw new Error('scope.approvedCanaryAccountIds 不得重复')
  if (new Set(scope.sourceAccountIds).size !== scope.sourceAccountIds.length) throw new Error('scope.sourceAccountIds 不得重复')
  if (new Set(scope.selectedAccountIds).size !== scope.selectedAccountIds.length) throw new Error('scope.selectedAccountIds 不得重复')
  if (!sha256Pattern.test(scope.approvedCanaryAccountIdsHash)) throw new Error('scope approvedCanaryAccountIdsHash 无效')
  if (!sha256Pattern.test(scope.sourceAccountIdsHash)) throw new Error('scope sourceAccountIdsHash 无效')
  if (!sha256Pattern.test(scope.selectedAccountIdsHash)) throw new Error('scope selectedAccountIdsHash 无效')
  if (!Number.isInteger(scope.approvedCanaryCount) || scope.approvedCanaryCount < 1) throw new Error('scope approvedCanaryCount 无效')
  if (scope.approvedCanaryCount !== scope.approvedCanaryAccountIds.length) throw new Error('scope approvedCanaryCount 与账户 ID 数量不一致')
  if (scope.approvedCanaryAccountIdsHash !== plan.approvedCanaryAccountIdsHash) {
    throw new Error('scope 与 field-level plan 的 approvedCanaryAccountIdsHash 不一致')
  }
  if (scope.approvedCanaryCount !== plan.approvedCanaryCount) {
    throw new Error('scope 与 field-level plan 的 approvedCanaryCount 不一致')
  }
  if (hashStringList(scope.approvedCanaryAccountIds) !== scope.approvedCanaryAccountIdsHash) throw new Error('scope approvedCanaryAccountIdsHash 与 ID 列表不一致')
  if (hashStringList(scope.sourceAccountIds) !== scope.sourceAccountIdsHash) throw new Error('scope sourceAccountIdsHash 与 ID 列表不一致')
  if (hashStringList(scope.selectedAccountIds) !== scope.selectedAccountIdsHash) throw new Error('scope selectedAccountIdsHash 与 ID 列表不一致')
  const selectedAccountSet = new Set(scope.selectedAccountIds)
  const expectedAccountSet = new Set([...scope.approvedCanaryAccountIds, ...scope.sourceAccountIds])
  if (expectedAccountSet.size !== selectedAccountSet.size || [...selectedAccountSet].some((id) => !expectedAccountSet.has(id))) {
    throw new Error('scope.selectedAccountIds 必须恰好是 canary 与 source 账户闭包的并集')
  }
  for (const accountId of scope.approvedCanaryAccountIds) {
    if (!selectedAccountSet.has(accountId)) throw new Error(`scope.selectedAccountIds 缺少 canary 账户 ${accountId}`)
  }
  for (const accountId of scope.sourceAccountIds) {
    if (!selectedAccountSet.has(accountId)) throw new Error(`scope.selectedAccountIds 缺少 source 账户 ${accountId}`)
  }

  const policyNames = new Set(ACCOUNT_SYNC_TABLE_POLICIES.filter((item) => item.purpose === 'configuration').map((item) => item.name))
  for (const name of Object.keys(scope.tables)) {
    if (!policyNames.has(name)) throw new Error(`scope 包含未批准的账户同步表：${name}`)
  }
  const wildcardAllowed = new Set([
    'providers', 'protocols', 'protocol_endpoint_families', 'provider_protocol_profiles',
    'provider_protocol_profile_families', 'provider_model_catalog', 'custom_provider_models',
    'provider_default_health_check_models', 'provider_system_default_health_check_models',
    'model_quality_policies', 'global_settings', 'system_settings'
  ])
  for (const name of policyNames) {
    const keys = scope.tables[name]
    if (!Array.isArray(keys) || keys.length === 0) throw new Error(`scope 缺少 ${name} 的行范围（使用 ["*"] 表示明确允许该表全部配置）`)
    if (keys.some((key) => key !== '*' && typeof key !== 'string')) throw new Error(`scope ${name} 含无效行键`)
    if (new Set(keys).size !== keys.length) throw new Error(`scope ${name} 行键重复`)
    if (keys.includes('*') && !wildcardAllowed.has(name)) throw new Error(`scope ${name} 不允许使用 *，必须列出闭包内的行键`)
  }
  const accountKeys = scope.tables.accounts
  if (accountKeys.includes('*')) {
    throw new Error('scope.accounts 必须列出 canary 与 source 闭包账户，不能使用 *')
  }
  const accountIdsFromKeys = accountKeys.map(parseSingleColumnScopeKey)
  if (new Set(accountIdsFromKeys).size !== accountIdsFromKeys.length || accountIdsFromKeys.length !== scope.selectedAccountIds.length) {
    throw new Error('scope.accounts 必须逐一列出 selectedAccountIds，且不得重复')
  }
  if (new Set(accountIdsFromKeys).size !== selectedAccountSet.size || accountIdsFromKeys.some((id) => !selectedAccountSet.has(id))) {
    throw new Error('scope.accounts 行键与 selectedAccountIds 不一致')
  }
}

export function validateGeneratedValuesManifest(manifest: GeneratedValuesManifest): void {
  if (!manifest || manifest.schemaVersion !== 1 || !manifest.tables || typeof manifest.tables !== 'object') {
    throw new Error('generated values manifest 必须是 schemaVersion=1 且包含 tables')
  }
  for (const [table, rows] of Object.entries(manifest.tables)) {
    if (!/^[a-z][a-z0-9_]*$/u.test(table) || !rows || typeof rows !== 'object') throw new Error(`generated values 表名无效：${table}`)
    for (const [rowKey, values] of Object.entries(rows)) {
      if (rowKey !== '*') {
        let parsed: unknown
        try {
          parsed = JSON.parse(rowKey)
        } catch {
          throw new Error(`generated values ${table} 行键必须是 JSON 主键数组或 *`)
        }
        if (!Array.isArray(parsed)) throw new Error(`generated values ${table} 行键必须是 JSON 主键数组或 *`)
      }
      if (!values || typeof values !== 'object' || Array.isArray(values)) throw new Error(`generated values ${table}.${rowKey} 必须是对象`)
    }
  }
}

export async function executeRehearsalAccountSync(
  source: pg.Client,
  target: pg.Client,
  preflight: AccountSyncPreflightReport,
  plan: RehearsalAccountSyncPlan,
  preflightText: string,
  scope: ScopeManifest,
  generatedValues: GeneratedValuesManifest
): Promise<RehearsalAccountSyncExecutionReport> {
  const validation = validateRehearsalAccountSyncPlanBinding(preflightText, preflight, plan)
  if (validation.status !== 'passed') throw new Error(`field-level plan 未通过校验：${validation.blockers.join('; ')}`)
  validateScopeManifest(scope, plan)
  validateGeneratedValuesManifest(generatedValues)
  assertTargetDatabaseName(preflight.target.databaseName)
  assertDistinctDatabaseIdentities(preflight.source, preflight.target)

  const sourceIdentity = await readIdentity(source)
  const targetIdentity = await readIdentity(target)
  assertDistinctDatabaseIdentities(sourceIdentity, targetIdentity)
  if (sourceIdentity.databaseOid !== preflight.source.databaseOid) throw new Error('源数据库身份已偏离 preflight，拒绝执行')
  if (targetIdentity.databaseOid !== preflight.target.databaseOid) throw new Error('目标数据库身份已偏离 preflight，拒绝执行')
  if (sourceIdentity.databaseName !== preflight.source.databaseName) throw new Error('源数据库名称已偏离 preflight，拒绝执行')
  if (targetIdentity.databaseName !== preflight.target.databaseName) throw new Error('目标数据库名称已偏离 preflight，拒绝执行')
  assertTargetDatabaseName(targetIdentity.databaseName)

  await assertTargetEmpty(target, preflight)
  const reports: ExecutionTableReport[] = []
  const plansByName = new Map(plan.tables.map((table) => [table.name, table]))
  assertGeneratedValuesMatchPlans(generatedValues, plansByName)
  const configurationTables = ACCOUNT_SYNC_TABLE_POLICIES
    .filter((policy) => policy.purpose === 'configuration')
    .sort((left, right) => (plansByName.get(left.name)?.importOrder ?? 0) - (plansByName.get(right.name)?.importOrder ?? 0))

  for (const policy of configurationTables) {
    const tablePlan = plansByName.get(policy.name)
    if (!tablePlan) throw new Error(`${policy.name} 缺少 plan`) // validation should already catch this
    if (policy.name === 'accounts' && tablePlan.selfForeignKeyPolicy !== 'source-before-authorization-instance') {
      throw new Error('当前执行器只支持 accounts.selfForeignKeyPolicy=source-before-authorization-instance；deferred 约束执行尚未实现')
    }
    reports.push(await copyTable(source, target, policy.name, tablePlan, scope.tables[policy.name], generatedValues.tables[policy.name] ?? {}))
  }

  const runtimeResetTables = [
    ...ACCOUNT_SYNC_RUNTIME_RESET_TABLES.map((name) => ({ schema: businessSchema, name })),
    ...AUXILIARY_RUNTIME_RESET_TABLES.map((item) => ({ schema: item.schema, name: item.name }))
  ]
  const runtimeReports = await clearRuntimeTables(target, runtimeResetTables)

  return {
    schemaVersion: 1,
    mode: 'execute',
    source: sourceIdentity,
    target: targetIdentity,
    targetNameAccepted: true,
    approvedCanaryCount: scope.approvedCanaryCount,
    tableReports: reports,
    runtimeResetTables: runtimeReports,
    status: 'passed'
  }
}

async function copyTable(
  source: QueryClient,
  target: QueryClient,
  table: string,
  plan: RehearsalAccountSyncTablePlan,
  scopeKeys: string[],
  generated: Record<string, Record<string, unknown>>
): Promise<ExecutionTableReport> {
  const primaryKey = await readPrimaryKey(source, table)
  const copiedColumns = [...new Set(plan.copiedColumns)]
  for (const key of primaryKey.map((item) => item.name)) {
    if (!copiedColumns.includes(key)) throw new Error(`${table}: 主键列 ${key} 必须在 copiedColumns 中，才能建立 scope 与回读摘要`)
  }
  const selectedColumns = copiedColumns
  const scopePredicate = buildScopePredicate(primaryKey, scopeKeys)
  const sqlColumns = selectedColumns.map(quoteIdentifier).join(', ')
  const result = await source.query<Row>(
    `SELECT ${sqlColumns} FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier(table)}${scopePredicate.sql}${primaryKey.length > 0 ? ` ORDER BY ${primaryKey.map((item) => quoteIdentifier(item.name)).join(', ')}` : ''}`,
    scopePredicate.parameters
  )
  let selected = result.rows
  if (table === 'accounts' && plan.selfForeignKeyPolicy === 'source-before-authorization-instance') selected = orderAccountRows(selected)
  if (!scopeKeys.includes('*') && result.rows.length !== scopeKeys.length) {
    throw new Error(`${table} scope 指定 ${scopeKeys.length} 行，但源库只找到 ${result.rows.length} 行，拒绝部分导入`)
  }
  const targetColumns = await readTargetColumns(target, table)
  const sensitive = new Set(ACCOUNT_SYNC_TABLE_POLICIES.find((item) => item.name === table)?.sensitiveColumns ?? [])
  const readbackColumns = [...new Set([...copiedColumns, ...plan.generatedColumns, ...plan.clearedColumns])]
  const readbackExpectations: ReadbackExpectation[] = []
  let insertedRows = 0
  for (const row of selected) {
    const rowKey = stableKey(primaryKey.map((item) => row[item.name]))
    const generatedForRow = generated[rowKey] ?? generated['*'] ?? {}
    const columns: string[] = []
    const values: unknown[] = []
    const expectedValues: Record<string, unknown> = {}
    const requiredNonNullColumns: string[] = []
    const sourceColumns = [...plan.copiedColumns, ...plan.clearedColumns, ...plan.generatedColumns]
    for (const column of sourceColumns) {
      if (plan.copiedColumns.includes(column)) {
        columns.push(column)
        values.push(row[column])
        expectedValues[column] = row[column]
        continue
      }
      if (plan.generatedColumns.includes(column)) {
        const hasRowSpecificValue = Object.prototype.hasOwnProperty.call(generated, rowKey)
        if (requiresPerRowGeneratedValue(table, column) && !hasRowSpecificValue) {
          throw new Error(`${table}.${column} 必须按主键逐行提供 test 专用生成值（禁止使用 * 复用凭据）`)
        }
        if (sensitive.has(column) && !(column in generatedForRow)) throw new Error(`${table}.${column} 缺少 test 专用生成值（拒绝复制生产密文）`)
        if (column in generatedForRow) {
          columns.push(column)
          values.push(generatedForRow[column])
          expectedValues[column] = generatedForRow[column]
        } else if (targetColumns.get(column)?.defaultExpression) {
          // Let PostgreSQL evaluate the candidate schema default.
          if (!targetColumns.get(column)?.isNullable) requiredNonNullColumns.push(column)
        } else {
          throw new Error(`${table}.${column} 既未提供生成值，也没有目标默认值`)
        }
        continue
      }
      if (plan.clearedColumns.includes(column)) {
        const metadata = targetColumns.get(column)
        if (metadata?.isNullable) {
          columns.push(column)
          values.push(null)
          expectedValues[column] = null
        } else if (!metadata?.defaultExpression) {
          throw new Error(`${table}.${column} 是 NOT NULL 且无默认值，不能清空`)
        } else {
          requiredNonNullColumns.push(column)
        }
      }
    }
    if (columns.length === 0) throw new Error(`${table} 产生空 INSERT，拒绝继续`)
    const insertSql = `INSERT INTO ${quoteIdentifier(businessSchema)}.${quoteIdentifier(table)} (${columns.map(quoteIdentifier).join(', ')}) VALUES (${columns.map((_, index) => `$${index + 1}`).join(', ')})`
    await target.query(insertSql, values)
    readbackExpectations.push({ rowKey, values: expectedValues, requiredNonNullColumns })
    insertedRows += 1
  }
  const readbackPredicate = buildScopePredicate(primaryKey, scopeKeys)
  const readback = await target.query<Row>(
    `SELECT ${readbackColumns.map(quoteIdentifier).join(', ')} FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier(table)}${readbackPredicate.sql}${primaryKey.length > 0 ? ` ORDER BY ${primaryKey.map((item) => quoteIdentifier(item.name)).join(', ')}` : ''}`,
    readbackPredicate.parameters
  )
  if (readback.rows.length !== selected.length) throw new Error(`${table} 目标 readback 行数 ${readback.rows.length} 与导入行数 ${selected.length} 不一致`)
  assertTransformedReadback(table, readback.rows, readbackExpectations, primaryKey)
  const readbackDigest = createHash('sha256')
  for (const value of projectedRowKeys(selected, copiedColumns)) readbackDigest.update(value).update('\n')
  const sourceDigest = readbackDigest.digest('hex')
  const targetDigest = createHash('sha256')
  for (const value of projectedRowKeys(readback.rows, copiedColumns)) targetDigest.update(value).update('\n')
  if (sourceDigest !== targetDigest.digest('hex')) throw new Error(`${table} copied 列 readback digest 不一致，事务将回滚`)
  const transformedReadbackDigest = createHash('sha256')
  for (const value of projectedRowKeys(readback.rows, readbackColumns)) transformedReadbackDigest.update(value).update('\n')
  await advanceIdSequence(target, table)
  return {
    name: table,
    sourceRows: result.rowCount ?? result.rows.length,
    selectedRows: selected.length,
    insertedRows,
    copiedDigest: sourceDigest,
    readbackDigest: transformedReadbackDigest.digest('hex')
  }
}

/**
 * Compare every explicitly supplied generated/cleared value after the INSERT.
 * Defaults are not compared byte-for-byte, but NOT NULL defaulted columns must
 * still be present. Sensitive values are compared in memory only and never
 * included in the returned digest or error text.
 */
export function assertTransformedReadback(
  table: string,
  actualRows: readonly Row[],
  expectations: readonly ReadbackExpectation[],
  primaryKey: readonly PrimaryKeyColumn[]
): void {
  const actualByKey = new Map<string, Row>()
  if (primaryKey.length > 0) {
    for (const row of actualRows) {
      const key = stableKey(primaryKey.map((column) => row[column.name]))
      if (actualByKey.has(key)) throw new Error(`${table} 目标 readback 主键重复，拒绝继续`)
      actualByKey.set(key, row)
    }
  }
  for (const [index, expectation] of expectations.entries()) {
    const actual = primaryKey.length > 0 ? actualByKey.get(expectation.rowKey) : actualRows[index]
    if (!actual) throw new Error(`${table} 目标 readback 缺少行 ${expectation.rowKey}`)
    for (const [column, expected] of Object.entries(expectation.values)) {
      if (!valuesEquivalent(expected, actual[column])) {
        throw new Error(`${table}.${column} 生成/清空值 readback 不一致，事务将回滚`)
      }
    }
    for (const column of expectation.requiredNonNullColumns) {
      if (actual[column] === null || actual[column] === undefined) {
        throw new Error(`${table}.${column} 默认值 readback 为空，事务将回滚`)
      }
    }
  }
}

function valuesEquivalent(expected: unknown, actual: unknown): boolean {
  if (expected instanceof Date || actual instanceof Date) {
    const expectedDate = toDate(expected)
    const actualDate = toDate(actual)
    if (expectedDate && actualDate) return expectedDate.toISOString() === actualDate.toISOString()
  }
  return stableKey([expected]) === stableKey([actual])
}

function toDate(value: unknown): Date | undefined {
  if (value instanceof Date && !Number.isNaN(value.getTime())) return value
  if (typeof value !== 'string' || !value.trim()) return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed
}

function requiresPerRowGeneratedValue(table: string, column: string): boolean {
  if (table === 'system_accounts' && column === 'password_hash') return true
  if (table === 'accounts' || table === 'proxy_profiles' || table === 'api_keys') return true
  if (table === 'model_quality_schedules' && ['enabled', 'next_run_at'].includes(column)) return true
  return false
}

async function advanceIdSequence(target: QueryClient, table: string): Promise<void> {
  const sequence = await target.query<{ sequence_name: string | null }>(
    'SELECT pg_get_serial_sequence($1, $2) AS sequence_name',
    [`${businessSchema}.${table}`, 'id']
  )
  const sequenceName = sequence.rows[0]?.sequence_name
  if (!sequenceName) return
  await target.query(
    `SELECT setval($1::regclass, COALESCE((SELECT max(${quoteIdentifier('id')}) FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier(table)}), 1), (SELECT count(*) > 0 FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier(table)}))`,
    [sequenceName]
  )
}

function buildScopePredicate(primaryKey: readonly PrimaryKeyColumn[], scopeKeys: readonly string[]): { sql: string; parameters: unknown[] } {
  if (scopeKeys.includes('*')) return { sql: '', parameters: [] }
  if (scopeKeys.length === 0) throw new Error('scope 行键不能为空')
  if (primaryKey.length === 0) throw new Error('无主键表不能使用显式 scope 行键；必须使用 *')
  const values = scopeKeys.map((key) => {
    let parsed: unknown
    try {
      parsed = JSON.parse(key)
    } catch {
      throw new Error(`scope 行键不是合法 JSON：${key}`)
    }
    if (!Array.isArray(parsed) || parsed.length !== primaryKey.length) throw new Error(`scope 行键主键列数不匹配：${key}`)
    return parsed
  })
  const parameters: unknown[] = []
  const clauses = values.map((keyValues) => {
    const clause = keyValues.map((value, columnIndex) => {
      parameters.push(value)
      return `${quoteIdentifier(primaryKey[columnIndex].name)} = $${parameters.length}`
    })
    return `(${clause.join(' AND ')})`
  })
  return { sql: ` WHERE ${clauses.join(' OR ')}`, parameters }
}

function projectedRowKeys(rows: readonly Row[], columns: readonly string[]): string[] {
  return rows.map((row) => {
    const projection: Row = {}
    for (const column of columns) projection[column] = row[column]
    return stableKey([projection])
  }).sort()
}

async function clearRuntimeTables(
  target: QueryClient,
  tables: readonly { schema: string; name: string }[]
): Promise<Array<{ schema: string; name: string; beforeRows: number; afterRows: number; beforeChecksum: string; afterChecksum: string }>> {
  const uniqueTables = [...new Map(tables.map((table) => [`${table.schema}.${table.name}`, table])).values()]
  const reports = [] as Array<{ schema: string; name: string; beforeRows: number; afterRows: number; beforeChecksum: string; afterChecksum: string }>
  for (const table of uniqueTables) {
    const beforeRows = await countRows(target, table.schema, table.name)
    const beforeChecksum = await tableChecksum(target, table.schema, table.name)
    reports.push({ schema: table.schema, name: table.name, beforeRows, afterRows: 0, beforeChecksum, afterChecksum: '' })
  }
  if (uniqueTables.length > 0) {
    // Truncate the complete runtime set in one statement so PostgreSQL can
    // validate any runtime-to-runtime foreign keys as a group. No CASCADE is
    // used: an unexpected reference outside this explicit set must abort.
    await target.query(`TRUNCATE TABLE ${uniqueTables.map((table) => `${quoteIdentifier(table.schema)}.${quoteIdentifier(table.name)}`).join(', ')}`)
  }
  for (const report of reports) {
    report.afterRows = await countRows(target, report.schema, report.name)
    report.afterChecksum = await tableChecksum(target, report.schema, report.name)
    if (report.afterRows !== 0 || report.afterChecksum !== emptyChecksum()) {
      throw new Error(`${report.schema}.${report.name} 运行态清空后 readback 非空，事务将回滚`)
    }
  }
  return reports
}

async function tableChecksum(client: QueryClient, schema: string, table: string): Promise<string> {
  const result = await client.query<{ row_json: string }>(`SELECT row_to_json(row_data)::text AS row_json FROM ${quoteIdentifier(schema)}.${quoteIdentifier(table)} AS row_data`)
  const hash = createHash('sha256')
  for (const row of result.rows.map((item) => item.row_json).sort()) hash.update(row).update('\n')
  return hash.digest('hex')
}

function emptyChecksum(): string {
  return createHash('sha256').digest('hex')
}

function parseSingleColumnScopeKey(value: string): string {
  let parsed: unknown
  try {
    parsed = JSON.parse(value)
  } catch {
    throw new Error(`accounts scope 行键不是合法 JSON：${value}`)
  }
  if (!Array.isArray(parsed) || parsed.length !== 1 || typeof parsed[0] !== 'string' || !parsed[0].trim()) {
    throw new Error(`accounts scope 行键必须是单列字符串主键：${value}`)
  }
  return parsed[0]
}

export function orderAccountRows(rows: Row[]): Row[] {
  const byId = new Map<string, Row>()
  for (const row of rows) {
    const id = String(row.id)
    if (byId.has(id)) throw new Error(`accounts 源库主键重复：${id}`)
    byId.set(id, row)
  }
  const pending = new Set(rows)
  const ordered: Row[] = []
  while (pending.size > 0) {
    let progressed = false
    for (const row of [...pending]) {
      const sourceId = row.authorization_instance_source_account_id
      if (sourceId !== null && sourceId !== undefined && !ordered.some((candidate) => String(candidate.id) === String(sourceId))) continue
      pending.delete(row)
      ordered.push(row)
      progressed = true
    }
    if (!progressed) {
      const unresolved = [...pending].map((row) => String(row.id)).join(', ')
      throw new Error(`accounts source-before-authorization-instance 顺序存在缺失 source 或环：${unresolved}`)
    }
  }
  return ordered
}

async function assertTargetEmpty(target: QueryClient, preflight: AccountSyncPreflightReport): Promise<void> {
  const tables = [
    ...ACCOUNT_SYNC_TABLE_POLICIES.map((item) => ({ schema: businessSchema, name: item.name })),
    ...ACCOUNT_SYNC_RUNTIME_RESET_TABLES.map((name) => ({ schema: businessSchema, name })),
    ...AUXILIARY_RUNTIME_RESET_TABLES.map((item) => ({ schema: item.schema, name: item.name }))
  ]
  const seen = new Set<string>()
  for (const table of tables) {
    const key = `${table.schema}.${table.name}`
    if (seen.has(key)) continue
    seen.add(key)
    const rows = await countRows(target, table.schema, table.name)
    if (rows !== 0) throw new Error(`${table.schema}.${table.name} 目标已有 ${rows} 行；请创建新的 rehearsal 数据库，不执行隐式清理`)
  }
  if (preflight.target.databaseName === 'juhe_ai_test') throw new Error('拒绝将 juhe_ai_test 作为 rehearsal 写入目标')
}

function assertGeneratedValuesMatchPlans(
  manifest: GeneratedValuesManifest,
  plansByName: ReadonlyMap<string, RehearsalAccountSyncTablePlan>
): void {
  for (const [table, rows] of Object.entries(manifest.tables)) {
    const plan = plansByName.get(table)
    if (!plan) throw new Error(`generated values 包含未在 field-level plan 中声明的表：${table}`)
    const allowedColumns = new Set(plan.generatedColumns)
    for (const [rowKey, values] of Object.entries(rows)) {
      for (const column of Object.keys(values)) {
        if (!allowedColumns.has(column)) throw new Error(`generated values ${table}.${rowKey}.${column} 不在 generatedColumns 中`)
      }
    }
  }
}

async function countRows(client: QueryClient, schema: string, table: string): Promise<number> {
  const result = await client.query<{ count: string }>(`SELECT count(*)::text AS count FROM ${quoteIdentifier(schema)}.${quoteIdentifier(table)}`)
  return Number(result.rows[0]?.count ?? 0)
}

async function readIdentity(client: QueryClient): Promise<AccountSyncDatabaseIdentity> {
  const result = await client.query<AccountSyncDatabaseIdentity>(`SELECT current_database() AS "databaseName", (SELECT oid::text FROM pg_database WHERE datname=current_database()) AS "databaseOid"`)
  const identity = result.rows[0]
  if (!identity) throw new Error('无法读取 PostgreSQL 数据库身份')
  return identity
}

async function readPrimaryKey(client: QueryClient, table: string): Promise<PrimaryKeyColumn[]> {
  const result = await client.query<{ column_name: string; ordinal_position: number }>(`
    SELECT attribute.attname AS column_name, key_columns.ordinal_position
    FROM pg_index index_catalog
    JOIN pg_class relation ON relation.oid = index_catalog.indrelid
    JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
    JOIN LATERAL unnest(index_catalog.indkey) WITH ORDINALITY AS key_columns(attnum, ordinal_position) ON TRUE
    JOIN pg_attribute attribute ON attribute.attrelid = relation.oid AND attribute.attnum = key_columns.attnum
    WHERE namespace.nspname = $1 AND relation.relname = $2 AND index_catalog.indisprimary
    ORDER BY key_columns.ordinal_position
  `, [businessSchema, table])
  return result.rows.map((row) => ({ name: row.column_name, ordinal: row.ordinal_position }))
}

async function readTargetColumns(client: QueryClient, table: string): Promise<Map<string, { isNullable: boolean; defaultExpression: string | null }>> {
  const result = await client.query<{ column_name: string; is_nullable: string; column_default: string | null }>(`
    SELECT column_name, is_nullable, column_default
    FROM information_schema.columns
    WHERE table_schema = $1 AND table_name = $2
  `, [businessSchema, table])
  return new Map(result.rows.map((row) => [row.column_name, { isNullable: row.is_nullable === 'YES', defaultExpression: row.column_default }]))
}

function normalizeValue(value: unknown): unknown {
  if (value instanceof Date) return { $date: value.toISOString() }
  if (typeof value === 'bigint') return value.toString()
  if (Buffer.isBuffer(value)) return { $buffer: value.toString('base64') }
  if (Array.isArray(value)) return value.map(normalizeValue)
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value as Record<string, unknown>).sort(([left], [right]) => left.localeCompare(right)).map(([key, item]) => [key, normalizeValue(item)]))
  return value
}

function quoteIdentifier(value: string): string {
  if (!/^[a-z][a-z0-9_]*$/u.test(value)) throw new Error(`非法 SQL 标识符：${value}`)
  return `"${value}"`
}

async function main(): Promise<void> {
  assertExecuteEnvironment(process.env)
  const [preflightPath, planPath, scopePath, generatedPath] = process.argv.slice(2)
  if (!preflightPath || !planPath || !scopePath || !generatedPath || process.argv.slice(2).length !== 4) {
    throw new Error('用法：execute-rehearsal-account-sync <preflight.json> <field-level-plan.json> <scope.json> <generated-values.json>')
  }
  const [preflightText, planText, scopeText, generatedText] = await Promise.all([
    readFile(preflightPath, 'utf8'), readFile(planPath, 'utf8'), readFile(scopePath, 'utf8'), readFile(generatedPath, 'utf8')
  ])
  const preflight = JSON.parse(preflightText) as AccountSyncPreflightReport
  const plan = JSON.parse(planText) as RehearsalAccountSyncPlan
  const scope = JSON.parse(scopeText) as ScopeManifest
  const generatedValues = JSON.parse(generatedText) as GeneratedValuesManifest
  const source = new pg.Client({ connectionString: process.env.JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL, application_name: 'juhe-ai-rehearsal-account-sync-source', connectionTimeoutMillis: 10_000 })
  const target = new pg.Client({ connectionString: process.env.JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL, application_name: 'juhe-ai-rehearsal-account-sync-target', connectionTimeoutMillis: 10_000 })
  let sourceConnected = false
  let targetConnected = false
  try {
    await Promise.all([source.connect().then(() => { sourceConnected = true }), target.connect().then(() => { targetConnected = true })])
    await source.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY')
    await target.query('BEGIN')
    await Promise.all([
      source.query("SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true)"),
      target.query("SELECT set_config('statement_timeout', '120s', true), set_config('lock_timeout', '10s', true), pg_advisory_xact_lock(hashtext(current_database()))")
    ])
    const report = await executeRehearsalAccountSync(source, target, preflight, plan, preflightText, scope, generatedValues)
    await target.query('COMMIT')
    await source.query('COMMIT')
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)
  } catch (error) {
    await Promise.allSettled([sourceConnected ? source.query('ROLLBACK') : Promise.resolve(), targetConnected ? target.query('ROLLBACK') : Promise.resolve()])
    throw error
  } finally {
    await Promise.allSettled([sourceConnected ? source.end() : Promise.resolve(), targetConnected ? target.end() : Promise.resolve()])
  }
}

const entryPath = process.argv[1] ? pathToFileURL(process.argv[1]).href : ''
if (entryPath === import.meta.url) {
  void main().catch((error: unknown) => {
    console.error(`账户同步执行失败：${error instanceof Error ? error.message : String(error)}`)
    process.exitCode = 1
  })
}
