import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'

import {
  ACCOUNT_SYNC_RUNTIME_RESET_TABLES,
  ACCOUNT_SYNC_TABLE_POLICIES,
  AUXILIARY_RUNTIME_RESET_TABLES,
  type AccountSyncPreflightReport,
  type AccountSyncTableReport
} from './rehearsal-account-sync-preflight.js'

const sha256Pattern = /^[0-9a-f]{64}$/u
const canaryCredentialPolicies = new Set(['test-only-equivalent', 'isolated-reencrypt'])
const accountSelfForeignKeyPolicies = new Set([
  'source-before-authorization-instance',
  'deferred-constraints-verified'
])
const providerSelfForeignKeyPolicy = 'parent-before-child'

export interface RehearsalAccountSyncTablePlan {
  name: string
  importOrder: number
  copiedColumns: string[]
  generatedColumns: string[]
  clearedColumns: string[]
  targetExtraColumns: string[]
  conflictStrategy: string
  selfForeignKeyPolicy?: string
  canaryOnly?: boolean
  disabledUntilSmoke?: boolean
  nextRunAtControlled?: boolean
  availabilityScheduleNextCheckAtControlled?: boolean
}

export interface RehearsalRuntimeResetPlan {
  schema: string
  name: string
  action: 'structure-only-clear'
  readbackRequired: true
  checksumRequired: true
}

export interface RehearsalAccountSyncPlan {
  schemaVersion: 1
  mode: 'field-level-plan'
  preflightSha256: string
  credentialsPolicy: 'test-only-equivalent' | 'isolated-reencrypt'
  credentialsEvidenceRef: string
  approvedCanaryAccountIdsHash: string
  approvedCanaryCount: number
  tables: RehearsalAccountSyncTablePlan[]
  runtimeResetTables: RehearsalRuntimeResetPlan[]
  auxiliaryRuntimeResetTables: RehearsalRuntimeResetPlan[]
}

export interface RehearsalAccountSyncPlanValidation {
  status: 'passed' | 'blocked'
  blockers: string[]
}

export function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

/**
 * Validates an execution plan without touching a database. The plan is bound
 * to a read-only preflight report, so a field list cannot silently drift after
 * someone has approved it.
 */
export function validateRehearsalAccountSyncPlan(
  preflight: AccountSyncPreflightReport,
  plan: RehearsalAccountSyncPlan
): RehearsalAccountSyncPlanValidation {
  const blockers: string[] = []
  if (!preflight || typeof preflight !== 'object') {
    return { status: 'blocked', blockers: ['preflight 必须是对象'] }
  }
  if (!plan || typeof plan !== 'object') {
    return { status: 'blocked', blockers: ['plan 必须是对象'] }
  }
  if (preflight.status !== 'passed') blockers.push('preflight.status 必须为 passed')
  if (plan.schemaVersion !== 1) blockers.push('plan.schemaVersion 必须为 1')
  if (plan.mode !== 'field-level-plan') blockers.push('plan.mode 必须为 field-level-plan')
  if (!canaryCredentialPolicies.has(plan.credentialsPolicy)) {
    blockers.push('plan.credentialsPolicy 必须为 test-only-equivalent 或 isolated-reencrypt')
  }
  if (!isEvidenceReference(plan.credentialsEvidenceRef)) {
    blockers.push('plan.credentialsEvidenceRef 必须是受控凭据策略证据引用')
  }
  if (!sha256Pattern.test(plan.approvedCanaryAccountIdsHash)) {
    blockers.push('plan.approvedCanaryAccountIdsHash 必须是 canary 账户 ID 排序摘要')
  }
  if (!Number.isInteger(plan.approvedCanaryCount) || plan.approvedCanaryCount < 1) {
    blockers.push('plan.approvedCanaryCount 必须大于 0')
  }

  const reportsByName = new Map(
    (Array.isArray(preflight.tableReports) ? preflight.tableReports : [])
      .filter((report): report is AccountSyncTableReport => Boolean(report && typeof report === 'object' && typeof report.name === 'string'))
      .map((report) => [report.name, report])
  )
  if (!Array.isArray(preflight.tableReports)) blockers.push('preflight.tableReports 必须是数组')
  const policiesByName = new Map(ACCOUNT_SYNC_TABLE_POLICIES.map((policy) => [policy.name, policy]))
  const plansByName = new Map<string, RehearsalAccountSyncTablePlan>()
  if (!Array.isArray(plan.tables)) {
    blockers.push('plan.tables 必须是数组')
  }
  for (const tablePlan of Array.isArray(plan.tables) ? plan.tables : []) {
    if (!tablePlan || typeof tablePlan !== 'object' || typeof tablePlan.name !== 'string') {
      blockers.push('plan.tables 包含无效项')
      continue
    }
    if (plansByName.has(tablePlan.name)) {
      blockers.push(`${tablePlan.name}: plan.tables 表名重复`)
      continue
    }
    plansByName.set(tablePlan.name, tablePlan)
  }

  for (const name of policiesByName.keys()) {
    if (!reportsByName.has(name)) blockers.push(`${name}: preflight 缺少表报告`)
    if (!plansByName.has(name)) blockers.push(`${name}: 字段级 plan 缺少表处理`)
  }
  for (const name of reportsByName.keys()) {
    if (!policiesByName.has(name)) blockers.push(`${name}: preflight 包含白名单外表`)
  }
  for (const name of plansByName.keys()) {
    if (!policiesByName.has(name)) blockers.push(`${name}: field-level plan 包含白名单外表`)
  }

  const importOrders = new Set<number>()
  for (const [name, tablePlan] of plansByName) {
    if (!Number.isInteger(tablePlan.importOrder) || tablePlan.importOrder < 1) {
      blockers.push(`${name}: importOrder 必须是正整数`)
    } else if (importOrders.has(tablePlan.importOrder)) {
      blockers.push(`${name}: importOrder 不得重复`)
    } else {
      importOrders.add(tablePlan.importOrder)
    }
  }

  for (const [name, policy] of policiesByName) {
    const report = reportsByName.get(name)
    const tablePlan = plansByName.get(name)
    if (!report || !tablePlan) continue
    validateTablePlan(report, policy.sensitiveColumns, policy.purpose, tablePlan, blockers)
  }

  validateForeignKeyImportOrder(reportsByName, policiesByName, plansByName, blockers)
  validateRuntimeResetPlans(preflight, plan, blockers)
  return { status: blockers.length === 0 ? 'passed' : 'blocked', blockers }
}

export function validateRehearsalAccountSyncPlanBinding(
  preflightText: string,
  preflight: AccountSyncPreflightReport,
  plan: RehearsalAccountSyncPlan
): RehearsalAccountSyncPlanValidation {
  const validation = validateRehearsalAccountSyncPlan(preflight, plan)
  if (plan.preflightSha256 !== sha256(preflightText)) {
    validation.blockers.push('plan.preflightSha256 与输入 preflight 报告不匹配')
    validation.status = 'blocked'
  }
  return validation
}

function validateRuntimeResetPlans(
  preflight: AccountSyncPreflightReport,
  plan: RehearsalAccountSyncPlan,
  blockers: string[]
): void {
  const runtimePlans = Array.isArray(plan.runtimeResetTables) ? plan.runtimeResetTables : []
  const auxiliaryPlans = Array.isArray(plan.auxiliaryRuntimeResetTables) ? plan.auxiliaryRuntimeResetTables : []
  if (!Array.isArray(plan.runtimeResetTables)) blockers.push('plan.runtimeResetTables 必须完整覆盖 Node 运行态表')
  if (!Array.isArray(plan.auxiliaryRuntimeResetTables)) blockers.push('plan.auxiliaryRuntimeResetTables 必须完整覆盖辅助运行态表')
  if (!Array.isArray(preflight.runtimeResetReports)) blockers.push('preflight.runtimeResetReports 必须是数组')
  if (!Array.isArray(preflight.auxiliaryRuntimeResetReports)) blockers.push('preflight.auxiliaryRuntimeResetReports 必须是数组')

  const validate = (
    expected: readonly { schema?: string; name: string }[],
    actual: readonly RehearsalRuntimeResetPlan[],
    reports: readonly { schema?: string; name: string; targetExists: boolean }[],
    label: string
  ): void => {
    const expectedKeys = new Set(expected.map((item) => `${item.schema ?? 'juhe_business'}.${item.name}`))
    const actualKeys = new Set<string>()
    for (const item of actual) {
      if (!item || typeof item !== 'object' || typeof item.name !== 'string' || typeof item.schema !== 'string') {
        blockers.push(`${label}: 包含无效项`)
        continue
      }
      const key = `${item.schema}.${item.name}`
      if (actualKeys.has(key)) blockers.push(`${label}: ${key} 重复`)
      actualKeys.add(key)
      if (!expectedKeys.has(key)) blockers.push(`${label}: ${key} 不在运行态白名单`)
      if (item.action !== 'structure-only-clear') blockers.push(`${label}: ${key} 必须 structure-only-clear`)
      if (item.readbackRequired !== true || item.checksumRequired !== true) blockers.push(`${label}: ${key} 必须 readback/checksum`)
      const report = reports.find((candidate) => `${candidate.schema ?? 'juhe_business'}.${candidate.name}` === key)
      if (!report) blockers.push(`${label}: ${key} 缺少 preflight 报告`)
      else if (!report.targetExists) blockers.push(`${label}: ${key} 目标表不存在`)
    }
    for (const key of expectedKeys) {
      if (!actualKeys.has(key)) blockers.push(`${label}: 缺少 ${key}`)
    }
    if (actualKeys.size !== expectedKeys.size) blockers.push(`${label}: 必须完整覆盖 ${expectedKeys.size} 张运行态表`)
  }

  validate(
    ACCOUNT_SYNC_RUNTIME_RESET_TABLES.map((name) => ({ name })),
    runtimePlans,
    Array.isArray(preflight.runtimeResetReports) ? preflight.runtimeResetReports : [],
    'plan.runtimeResetTables'
  )
  validate(
    AUXILIARY_RUNTIME_RESET_TABLES,
    auxiliaryPlans,
    Array.isArray(preflight.auxiliaryRuntimeResetReports) ? preflight.auxiliaryRuntimeResetReports : [],
    'plan.auxiliaryRuntimeResetTables'
  )
}

function validateTablePlan(
  report: AccountSyncTableReport,
  sensitiveColumns: readonly string[],
  purpose: 'configuration' | 'runtime-reset',
  tablePlan: RehearsalAccountSyncTablePlan,
  blockers: string[]
): void {
  const prefix = report.name
  if (!report.sourceExists || !report.targetExists) blockers.push(`${prefix}: preflight 表必须同时存在于源/目标`)
  if (!Array.isArray(report.missingTargetColumns)) blockers.push(`${prefix}: preflight.missingTargetColumns 必须是数组`)
  if (!Array.isArray(report.columnDifferences)) blockers.push(`${prefix}: preflight.columnDifferences 必须是数组`)
  if (!Array.isArray(report.foreignKeyDifferences)) blockers.push(`${prefix}: preflight.foreignKeyDifferences 必须是数组`)
  if (!Array.isArray(report.foreignKeysOutsidePolicy)) blockers.push(`${prefix}: preflight.foreignKeysOutsidePolicy 必须是数组`)
  if (!Array.isArray(report.targetRequiredColumnsWithoutDefault)) blockers.push(`${prefix}: preflight.targetRequiredColumnsWithoutDefault 必须是数组`)
  if (Array.isArray(report.missingTargetColumns) && report.missingTargetColumns.length > 0) blockers.push(`${prefix}: preflight 仍有目标缺列`)
  if (Array.isArray(report.columnDifferences) && report.columnDifferences.length > 0) blockers.push(`${prefix}: preflight 仍有共享列定义差异`)
  if (Array.isArray(report.foreignKeyDifferences) && report.foreignKeyDifferences.length > 0) blockers.push(`${prefix}: preflight 仍有外键定义差异`)
  if (Array.isArray(report.foreignKeysOutsidePolicy) && report.foreignKeysOutsidePolicy.length > 0) blockers.push(`${prefix}: preflight 存在白名单外 FK 父表`)
  if (Array.isArray(report.targetRequiredColumnsWithoutDefault) && report.targetRequiredColumnsWithoutDefault.length > 0) {
    blockers.push(`${prefix}: 目标存在 source 未覆盖的必填列 ${report.targetRequiredColumnsWithoutDefault.join(', ')}`)
  }

  const sourceColumns = new Set((Array.isArray(report.sourceColumns) ? report.sourceColumns : []).map((column) => column.name))
  if (!Array.isArray(report.sourceColumns)) blockers.push(`${prefix}: preflight.sourceColumns 必须是数组`)
  const reportedTargetExtraColumns = new Set(
    (Array.isArray(report.unexpectedTargetColumns) ? report.unexpectedTargetColumns : []).map((column) => column)
  )
  if (!Array.isArray(report.unexpectedTargetColumns)) blockers.push(`${prefix}: preflight.unexpectedTargetColumns 必须是数组`)
  if (!Array.isArray(tablePlan.targetExtraColumns)) {
    blockers.push(`${prefix}.targetExtraColumns: 必须显式列出目标额外列（可为空数组）`)
  } else {
    const declaredTargetExtraColumns = new Set(tablePlan.targetExtraColumns)
    if (declaredTargetExtraColumns.size !== tablePlan.targetExtraColumns.length) {
      blockers.push(`${prefix}.targetExtraColumns: 不得重复`)
    }
    for (const column of tablePlan.targetExtraColumns) {
      if (typeof column !== 'string' || !column.trim()) blockers.push(`${prefix}.targetExtraColumns: 包含无效列名`)
    }
    for (const column of reportedTargetExtraColumns) {
      if (!declaredTargetExtraColumns.has(column)) blockers.push(`${prefix}: target extra column ${column} 未列入 field-level plan`)
    }
    for (const column of declaredTargetExtraColumns) {
      if (!reportedTargetExtraColumns.has(column)) blockers.push(`${prefix}: field-level plan 声明了 preflight 未发现的 target extra column ${column}`)
    }
  }
  const actionColumns = [
    ['copiedColumns', tablePlan.copiedColumns],
    ['generatedColumns', tablePlan.generatedColumns],
    ['clearedColumns', tablePlan.clearedColumns]
  ] as const
  const seen = new Set<string>()
  for (const [action, columns] of actionColumns) {
    if (!Array.isArray(columns) || columns.some((column) => typeof column !== 'string' || !column.trim())) {
      blockers.push(`${prefix}.${action}: 必须是非空列名数组`)
      continue
    }
    for (const column of columns) {
      if (!sourceColumns.has(column)) blockers.push(`${prefix}.${action}: 包含源表不存在的列 ${column}`)
      if (seen.has(column)) blockers.push(`${prefix}: 列 ${column} 只能归入 copied/generated/cleared 之一`)
      seen.add(column)
    }
  }
  for (const column of sourceColumns) {
    if (!seen.has(column)) blockers.push(`${prefix}: 源列 ${column} 未分类，禁止静默省略`)
  }

  if (typeof tablePlan.conflictStrategy !== 'string' || !tablePlan.conflictStrategy.trim()) {
    blockers.push(`${prefix}.conflictStrategy: 必须说明幂等冲突策略`)
  }

  const copied = new Set(Array.isArray(tablePlan.copiedColumns) ? tablePlan.copiedColumns : [])
  const generated = new Set(Array.isArray(tablePlan.generatedColumns) ? tablePlan.generatedColumns : [])
  const cleared = new Set(Array.isArray(tablePlan.clearedColumns) ? tablePlan.clearedColumns : [])
  if (purpose === 'runtime-reset') {
    if (copied.size > 0 || generated.size > 0 || cleared.size !== sourceColumns.size) {
      blockers.push(`${prefix}: runtime-reset 表只能逐列清空，不得复制或生成生产运行态`)
    }
    return
  }

  for (const sensitiveColumn of sensitiveColumns) {
    if (!sourceColumns.has(sensitiveColumn)) continue
    if (copied.has(sensitiveColumn) || !generated.has(sensitiveColumn) || cleared.has(sensitiveColumn)) {
      blockers.push(`${prefix}: 敏感列 ${sensitiveColumn} 必须 generated/replaced，不能复制或仅清空`)
    }
  }
  validateSpecialTableColumns(report, tablePlan, copied, generated, cleared, blockers)
}

function validateSpecialTableColumns(
  report: AccountSyncTableReport,
  tablePlan: RehearsalAccountSyncTablePlan,
  copied: ReadonlySet<string>,
  generated: ReadonlySet<string>,
  cleared: ReadonlySet<string>,
  blockers: string[]
): void {
  const columns = new Set((Array.isArray(report.sourceColumns) ? report.sourceColumns : []).map((column) => column.name))
  const requireGenerated = (column: string): void => {
    if (columns.has(column) && !generated.has(column)) blockers.push(`${report.name}: ${column} 必须生成/替换`)
  }
  const forbidCopied = (column: string): void => {
    if (columns.has(column) && copied.has(column)) blockers.push(`${report.name}: ${column} 不得复制生产运行态`)
  }

  if (report.name === 'system_accounts') {
    requireGenerated('password_hash')
  }
  if (report.name === 'accounts') {
    requireGenerated('credentials_encrypted')
    requireGenerated('credential_fingerprint')
    requireGenerated('credential_mask')
    requireGenerated('oauth_access_token_expires_at')
    requireGenerated('oauth_refresh_token_present')
    if (columns.has('availability_schedule_next_check_at')) {
      forbidCopied('availability_schedule_next_check_at')
      if (!generated.has('availability_schedule_next_check_at') || cleared.has('availability_schedule_next_check_at')) {
        blockers.push('accounts: availability_schedule_next_check_at 必须按 test 预演窗口逐行生成/替换，禁止复制或仅清空生产时间戳')
      }
      if (tablePlan.availabilityScheduleNextCheckAtControlled !== true) {
        blockers.push('accounts.availabilityScheduleNextCheckAtControlled 必须为 true，并绑定受控预演窗口')
      }
    }
    for (const runtimeColumn of [
      'last_used_at', 'cooldown_until', 'last_error_code', 'last_error_message', 'last_error_trace_id',
      'cooldown_retest_observation_started_at', 'cooldown_retest_generation', 'cooldown_retest_last_at',
      'cooldown_retest_last_status_code', 'last_health_check_at', 'next_health_check_at', 'last_health_success_at',
      'health_check_failure_started_at', 'last_health_check_status_code', 'last_health_check_error_code',
      'last_health_check_error_message', 'last_health_check_trace_id', 'stream_failure_window_started_at',
      'balance_query_next_refresh_at', 'deleted_at', 'deleted_by'
    ]) {
      forbidCopied(runtimeColumn)
      if (columns.has(runtimeColumn) && !cleared.has(runtimeColumn) && !generated.has(runtimeColumn)) {
        blockers.push(`accounts: ${runtimeColumn} 必须清空或按 test 规则生成`)
      }
    }
    if (!accountSelfForeignKeyPolicies.has(tablePlan.selfForeignKeyPolicy ?? '')) {
      blockers.push('accounts.selfForeignKeyPolicy 必须为 source-before-authorization-instance 或 deferred-constraints-verified')
    }
    if (cleared.has('authorization_instance_source_account_id')) {
      blockers.push('accounts: 不得统一清空 authorization_instance_source_account_id')
    }
  }
  if (report.name === 'providers') {
    const hasProviderSelfForeignKey = (Array.isArray(report.sourceForeignKeys) ? report.sourceForeignKeys : [])
      .some((foreignKey) => foreignKey.parentSchema === 'juhe_business' && foreignKey.parentTable === 'providers')
    if (hasProviderSelfForeignKey) {
      if (tablePlan.selfForeignKeyPolicy !== providerSelfForeignKeyPolicy) {
        blockers.push(`providers.selfForeignKeyPolicy 必须为 ${providerSelfForeignKeyPolicy}`)
      }
      for (const relationshipColumn of ['code', 'parent_code']) {
        if (!columns.has(relationshipColumn)) {
          blockers.push(`providers: 自引用外键存在但缺少 ${relationshipColumn} 源列`)
        } else if (!copied.has(relationshipColumn)) {
          blockers.push(`providers: 自引用拓扑列 ${relationshipColumn} 必须原值复制，不能生成或清空`)
        }
      }
    }
  }
  if (report.name === 'api_keys') {
    requireGenerated('key_secret_encrypted')
  }
  if (report.name === 'model_quality_schedules') {
    for (const runtimeColumn of ['enabled', 'next_run_at', 'last_run_id', 'last_run_at', 'last_run_status', 'lease_owner', 'lease_until']) {
      forbidCopied(runtimeColumn)
      if (columns.has(runtimeColumn) && !cleared.has(runtimeColumn) && !generated.has(runtimeColumn)) {
        blockers.push(`model_quality_schedules: ${runtimeColumn} 必须清空或按 test 规则生成`)
      }
    }
    if (tablePlan.canaryOnly !== true || tablePlan.disabledUntilSmoke !== true || tablePlan.nextRunAtControlled !== true) {
      blockers.push('model_quality_schedules: 必须声明 canaryOnly/disabledUntilSmoke/nextRunAtControlled 均为 true')
    }
  }
}

function validateForeignKeyImportOrder(
  reportsByName: ReadonlyMap<string, AccountSyncTableReport>,
  policiesByName: ReadonlyMap<string, unknown>,
  plansByName: ReadonlyMap<string, RehearsalAccountSyncTablePlan>,
  blockers: string[]
): void {
  for (const [childTable, report] of reportsByName) {
    const childPlan = plansByName.get(childTable)
    if (!childPlan) continue
    if (!Array.isArray(report.sourceForeignKeys)) {
      blockers.push(`${childTable}: preflight.sourceForeignKeys 必须是数组`)
      continue
    }
    for (const foreignKey of report.sourceForeignKeys) {
      const parentTable = foreignKey.parentTable
      if (foreignKey.parentSchema !== 'juhe_business' || !policiesByName.has(parentTable) || parentTable === childTable) continue
      const parentPlan = plansByName.get(parentTable)
      if (!parentPlan) continue
      if (parentPlan.importOrder >= childPlan.importOrder) {
        blockers.push(`${childTable}: FK ${foreignKey.constraintName} 的父表 ${parentTable} 必须先于子表导入`)
      }
    }
  }
}

function isEvidenceReference(value: unknown): value is string {
  return typeof value === 'string'
    && /^(?!\/)(?!.*(?:^|\/)\.\.(?:\/|$))[A-Za-z0-9._\/-]{1,256}$/u.test(value)
}

async function main(): Promise<void> {
  const [preflightPath, planPath] = process.argv.slice(2)
  if (!preflightPath || !planPath || process.argv.slice(2).length !== 2) {
    throw new Error('用法：validate-rehearsal-account-sync-plan <preflight-report.json> <field-level-plan.json>')
  }
  const [preflightText, planText] = await Promise.all([readFile(preflightPath, 'utf8'), readFile(planPath, 'utf8')])
  const preflight = JSON.parse(preflightText) as AccountSyncPreflightReport
  const plan = JSON.parse(planText) as RehearsalAccountSyncPlan
  const validation = validateRehearsalAccountSyncPlanBinding(preflightText, preflight, plan)
  process.stdout.write(`${JSON.stringify(validation, null, 2)}\n`)
  process.exitCode = validation.status === 'passed' ? 0 : 3
}

const entryPath = process.argv[1] ? pathToFileURL(process.argv[1]).href : ''
if (entryPath === import.meta.url) {
  void main().catch((error: unknown) => {
    console.error(`字段级账户同步计划校验失败：${error instanceof Error ? error.message : String(error)}`)
    process.exitCode = 2
  })
}
