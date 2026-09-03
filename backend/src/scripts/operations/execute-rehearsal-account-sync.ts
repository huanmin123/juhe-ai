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
import { decryptJson, hashSecret } from '../../storage/crypto.js'

type Row = Record<string, unknown>
type QueryClient = Pick<pg.Client, 'query'>

export interface ScopeManifest {
  schemaVersion: 1
  tables: Record<string, string[]>
  approvedCanaryAccountIds: string[]
  sourceAccountIds: string[]
  selectedAccountIds: string[]
  systemAccountIds: string[]
  systemTeamIds: string[]
  groupIds: string[]
  routeStrategyIds: string[]
  resourceAuthorizationIds: string[]
  resourceAuthorizationGrantIds: string[]
  apiKeyIds: string[]
  /**
   * The executor deliberately keeps API-key row IDs stable.  Secrets are
   * regenerated in generated-values.json; changing the row ID would require
   * rewriting quota bindings and every external reference, which is not
   * implemented by this command.
   */
  apiKeyRemap: Array<{ sourceId: string; targetId: string }>
  approvedCanaryAccountIdsHash: string
  approvedCanaryCount: number
  sourceAccountIdsHash: string
  selectedAccountIdsHash: string
  systemAccountIdsHash: string
  systemTeamIdsHash: string
  groupIdsHash: string
  routeStrategyIdsHash: string
  resourceAuthorizationIdsHash: string
  resourceAuthorizationGrantIdsHash: string
  apiKeyIdsHash: string
  apiKeyRemapHash: string
}

export interface AccountClosureManifest {
  schemaVersion: 1
  approvedCanaryAccountIds: string[]
  sourceAccountIds: string[]
  selectedAccountIds: string[]
  systemAccountIds: string[]
  systemTeamIds: string[]
  groupIds: string[]
  routeStrategyIds: string[]
  resourceAuthorizationIds: string[]
  resourceAuthorizationGrantIds: string[]
  apiKeyIds: string[]
  apiKeyRemap: Array<{ sourceId: string; targetId: string }>
  [key: string]: unknown
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
  scopeManifestDigest: string
  closureManifestDigest: string
  apiKeyRemapHash: string
  approvedCanaryCount: number
  tableReports: ExecutionTableReport[]
  referenceClosureVerified: true
  runtimeResetTables: Array<{ schema: string; name: string; beforeRows: number; afterRows: number; beforeChecksum: string; afterChecksum: string }>
  status: 'passed'
}

const businessSchema = 'juhe_business'
const confirmation = 'I_UNDERSTAND_TEST_TARGET_ONLY'
const sha256Pattern = /^[0-9a-f]{64}$/u
const minimumRehearsalSecretLength = 32
const scopeEntitySpecs = [
  { table: 'system_accounts', idsField: 'systemAccountIds', hashField: 'systemAccountIdsHash', label: '系统账户' },
  { table: 'system_teams', idsField: 'systemTeamIds', hashField: 'systemTeamIdsHash', label: '系统团队' },
  { table: 'groups', idsField: 'groupIds', hashField: 'groupIdsHash', label: '分组' },
  { table: 'route_strategies', idsField: 'routeStrategyIds', hashField: 'routeStrategyIdsHash', label: '路由策略' },
  { table: 'resource_authorizations', idsField: 'resourceAuthorizationIds', hashField: 'resourceAuthorizationIdsHash', label: '资源授权' },
  { table: 'resource_authorization_grants', idsField: 'resourceAuthorizationGrantIds', hashField: 'resourceAuthorizationGrantIdsHash', label: '资源授权 grant' },
  { table: 'api_keys', idsField: 'apiKeyIds', hashField: 'apiKeyIdsHash', label: 'API Key' }
] as const

export function stableKey(values: readonly unknown[]): string {
  return JSON.stringify(values.map((value) => normalizeValue(value)))
}

export function hashStringList(values: readonly string[]): string {
  const hash = createHash('sha256')
  for (const value of [...values].sort()) hash.update(value).update('\n')
  return hash.digest('hex')
}

function canonicalJson(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`
  return `{${Object.entries(value as Record<string, unknown>)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, item]) => `${JSON.stringify(key)}:${canonicalJson(item)}`)
    .join(',')}}`
}

export function manifestDigest(value: unknown): string {
  return createHash('sha256').update(canonicalJson(value)).digest('hex')
}

export function assertExecuteEnvironment(env: NodeJS.ProcessEnv): void {
  if (env.JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE !== 'execute') {
    throw new Error('执行账户同步必须显式设置 JUHE_AI_REHEARSAL_ACCOUNT_SYNC_MODE=execute')
  }
  if (env.JUHE_AI_REHEARSAL_EXECUTE_CONFIRM !== confirmation) {
    throw new Error(`执行账户同步必须显式设置 JUHE_AI_REHEARSAL_EXECUTE_CONFIRM=${confirmation}`)
  }
  const rehearsalSecret = env.JUHE_AI_SECRET?.trim()
  if (!rehearsalSecret || rehearsalSecret.length < minimumRehearsalSecretLength) {
    throw new Error(`执行账户同步必须显式设置至少 ${minimumRehearsalSecretLength} 位 JUHE_AI_SECRET；禁止使用隐式开发密钥`)
  }
  if (!env.JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL?.trim()) throw new Error('JUHE_AI_REHEARSAL_SOURCE_POSTGRES_URL 未配置')
  if (!env.JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL?.trim()) throw new Error('JUHE_AI_REHEARSAL_TARGET_POSTGRES_URL 未配置')
}

/**
 * API Key 的 hash/prefix/suffix 必须由同一个 test-only key secret 派生。
 * 只在内存中解密并比较，错误信息不包含 key 或密文。
 */
export function assertGeneratedApiKeyValues(values: Record<string, unknown>): void {
  const encrypted = values.key_secret_encrypted
  if (typeof encrypted !== 'string' || !encrypted.trim()) {
    throw new Error('api_keys.key_secret_encrypted 缺少 test 专用密文')
  }
  let decrypted: unknown
  try {
    decrypted = decryptJson<unknown>(encrypted)
  } catch {
    throw new Error('api_keys.key_secret_encrypted 无法使用当前 JUHE_AI_SECRET 解密')
  }
  const key = decrypted && typeof decrypted === 'object' && !Array.isArray(decrypted)
    ? (decrypted as Record<string, unknown>).key
    : undefined
  if (typeof key !== 'string' || !key.trim()) {
    throw new Error('api_keys.key_secret_encrypted 未包含有效 test key')
  }
  if (!/^sk-[0-9a-f]{64}$/u.test(key)) {
    throw new Error('api_keys.key_secret_encrypted 未包含符合应用格式的 test key')
  }
  if (values.key_hash !== hashSecret(key)) {
    throw new Error('api_keys.key_hash 与 test key 派生值不一致')
  }
  if (values.key_prefix !== key.slice(0, 8)) {
    throw new Error('api_keys.key_prefix 与 test key 派生值不一致')
  }
  if (values.key_suffix !== key.slice(-8)) {
    throw new Error('api_keys.key_suffix 与 test key 派生值不一致')
  }
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
  for (const spec of scopeEntitySpecs) validateApprovedScopeEntityList(scope, spec)
  validateApiKeyRemap(scope)
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
  const accountIdsFromKeys = accountKeys.map((key) => parseSingleColumnScopeKey(key))
  if (new Set(accountIdsFromKeys).size !== accountIdsFromKeys.length || accountIdsFromKeys.length !== scope.selectedAccountIds.length) {
    throw new Error('scope.accounts 必须逐一列出 selectedAccountIds，且不得重复')
  }
  if (new Set(accountIdsFromKeys).size !== selectedAccountSet.size || accountIdsFromKeys.some((id) => !selectedAccountSet.has(id))) {
    throw new Error('scope.accounts 行键与 selectedAccountIds 不一致')
  }
  for (const spec of scopeEntitySpecs) validateScopeEntityTableBinding(scope, spec)
}

function normalizeApiKeyRemap(value: unknown): Array<{ sourceId: string; targetId: string }> {
  if (!Array.isArray(value) || value.length === 0) throw new Error('scope.apiKeyRemap 必须是非空数组')
  const remap = value.map((item, index) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      throw new Error(`scope.apiKeyRemap[${index}] 必须是对象`)
    }
    const sourceId = (item as Record<string, unknown>).sourceId
    const targetId = (item as Record<string, unknown>).targetId
    if (typeof sourceId !== 'string' || !sourceId.trim() || typeof targetId !== 'string' || !targetId.trim()) {
      throw new Error(`scope.apiKeyRemap[${index}] 必须包含非空 sourceId/targetId`)
    }
    return { sourceId, targetId }
  }).sort((left, right) => left.sourceId.localeCompare(right.sourceId) || left.targetId.localeCompare(right.targetId))
  if (new Set(remap.map((item) => item.sourceId)).size !== remap.length) throw new Error('scope.apiKeyRemap 的 sourceId 不得重复')
  if (new Set(remap.map((item) => item.targetId)).size !== remap.length) throw new Error('scope.apiKeyRemap 的 targetId 不得重复')
  return remap
}

function validateApiKeyRemap(scope: ScopeManifest): void {
  const remap = normalizeApiKeyRemap(scope.apiKeyRemap)
  const expected = [...scope.apiKeyIds].sort()
  const sources = remap.map((item) => item.sourceId).sort()
  const targets = remap.map((item) => item.targetId).sort()
  if (JSON.stringify(sources) !== JSON.stringify(expected) || JSON.stringify(targets) !== JSON.stringify(expected)) {
    throw new Error('scope.apiKeyRemap 必须覆盖全部 apiKeyIds，且 source/target 集合必须一致')
  }
  if (remap.some((item) => item.sourceId !== item.targetId)) {
    throw new Error('当前执行器不支持 API Key ID remap；必须保留原 ID，仅重建 test 专用 key_secret')
  }
  if (scope.apiKeyRemapHash !== manifestDigest(remap)) throw new Error('scope.apiKeyRemapHash 与 apiKeyRemap 不一致')
}

export function assertClosureMatchesScope(scope: ScopeManifest, closure: AccountClosureManifest): void {
  if (!closure || closure.schemaVersion !== 1) throw new Error('closure manifest 必须是 schemaVersion=1')
  const idFields = [
    'approvedCanaryAccountIds', 'sourceAccountIds', 'selectedAccountIds', 'systemAccountIds',
    'systemTeamIds', 'groupIds', 'routeStrategyIds', 'resourceAuthorizationIds',
    'resourceAuthorizationGrantIds', 'apiKeyIds'
  ] as const
  for (const field of idFields) {
    const closureIds = closure[field]
    const scopeIds = scope[field]
    if (!Array.isArray(closureIds) || JSON.stringify([...closureIds].sort()) !== JSON.stringify([...scopeIds].sort())) {
      throw new Error(`closure.${field} 必须与执行 scope 完全一致`)
    }
  }
  const remap = normalizeApiKeyRemap(closure.apiKeyRemap)
  if (JSON.stringify(remap) !== JSON.stringify(normalizeApiKeyRemap(scope.apiKeyRemap))) {
    throw new Error('closure.apiKeyRemap 必须与执行 scope 完全一致')
  }
  const hashPairs: Array<[keyof ScopeManifest, 'approvedCanaryAccountIds' | 'sourceAccountIds' | 'selectedAccountIds' | 'systemAccountIds' | 'systemTeamIds' | 'groupIds' | 'routeStrategyIds' | 'resourceAuthorizationIds' | 'resourceAuthorizationGrantIds' | 'apiKeyIds']> = [
    ['approvedCanaryAccountIdsHash', 'approvedCanaryAccountIds'],
    ['sourceAccountIdsHash', 'sourceAccountIds'],
    ['selectedAccountIdsHash', 'selectedAccountIds'],
    ['systemAccountIdsHash', 'systemAccountIds'],
    ['systemTeamIdsHash', 'systemTeamIds'],
    ['groupIdsHash', 'groupIds'],
    ['routeStrategyIdsHash', 'routeStrategyIds'],
    ['resourceAuthorizationIdsHash', 'resourceAuthorizationIds'],
    ['resourceAuthorizationGrantIdsHash', 'resourceAuthorizationGrantIds'],
    ['apiKeyIdsHash', 'apiKeyIds']
  ]
  for (const [hashField, idField] of hashPairs) {
    const closureHash = closure[hashField]
    if (closureHash !== scope[hashField] || closureHash !== hashStringList(scope[idField])) {
      throw new Error(`closure.${idField} hash 必须与执行 scope 一致`)
    }
  }
  if (closure.apiKeyRemapHash !== scope.apiKeyRemapHash || closure.apiKeyRemapHash !== manifestDigest(remap)) {
    throw new Error('closure.apiKeyRemapHash 必须与执行 scope 一致')
  }
}

function validateApprovedScopeEntityList(
  scope: ScopeManifest,
  spec: (typeof scopeEntitySpecs)[number]
): void {
  const ids: unknown = scope[spec.idsField]
  const hash: unknown = scope[spec.hashField]
  if (!Array.isArray(ids) || ids.length === 0) throw new Error(`scope.${spec.idsField} 必须是非空数组`)
  if (ids.some((value) => typeof value !== 'string' || !value.trim())) throw new Error(`scope.${spec.idsField} 含无效 ${spec.label} ID`)
  if (new Set(ids).size !== ids.length) throw new Error(`scope.${spec.idsField} 不得重复`)
  if (typeof hash !== 'string' || !sha256Pattern.test(hash)) throw new Error(`scope.${spec.hashField} 无效`)
  if (hashStringList(ids) !== hash) throw new Error(`scope.${spec.hashField} 与 ID 列表不一致`)
}

function validateScopeEntityTableBinding(
  scope: ScopeManifest,
  spec: (typeof scopeEntitySpecs)[number]
): void {
  const tableKeys = scope.tables[spec.table]
  const approvedIds = scope[spec.idsField]
  if (!Array.isArray(tableKeys) || tableKeys.includes('*')) {
    throw new Error(`scope.${spec.table} 必须逐一列出获批 ${spec.label}，不能使用 *`)
  }
  const tableIds = tableKeys.map((key) => parseSingleColumnScopeKey(key, spec.table))
  const approvedIdSet = new Set(approvedIds)
  if (tableIds.length !== approvedIdSet.size || new Set(tableIds).size !== tableIds.length || tableIds.some((id) => !approvedIdSet.has(id))) {
    throw new Error(`scope.${spec.table} 行键必须与 scope.${spec.idsField} 精确一致`)
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
  closure: AccountClosureManifest,
  generatedValues: GeneratedValuesManifest
): Promise<RehearsalAccountSyncExecutionReport> {
  const validation = validateRehearsalAccountSyncPlanBinding(preflightText, preflight, plan)
  if (validation.status !== 'passed') throw new Error(`field-level plan 未通过校验：${validation.blockers.join('; ')}`)
  validateScopeManifest(scope, plan)
  assertClosureMatchesScope(scope, closure)
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
    if (policy.name === 'providers' && tablePlan.selfForeignKeyPolicy !== 'parent-before-child') {
      throw new Error('当前执行器只支持 providers.selfForeignKeyPolicy=parent-before-child')
    }
    reports.push(await copyTable(source, target, policy.name, tablePlan, scope.tables[policy.name], generatedValues.tables[policy.name] ?? {}))
  }

  // Several authorization/quota columns are intentionally polymorphic and do
  // not have database FKs. Verify their target-side closure before clearing
  // runtime state, otherwise a rehearsal could pass INSERT/readback while a
  // scheduler or gateway later follows an orphaned reference.
  await assertTargetReferenceClosure(target, scope)

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
    scopeManifestDigest: manifestDigest(scope),
    closureManifestDigest: manifestDigest(closure),
    apiKeyRemapHash: scope.apiKeyRemapHash,
    approvedCanaryCount: scope.approvedCanaryCount,
    tableReports: reports,
    referenceClosureVerified: true,
    runtimeResetTables: runtimeReports,
    status: 'passed'
  }
}

export async function assertTargetReferenceClosure(target: QueryClient, scope: ScopeManifest): Promise<void> {
  const checks: Array<{ label: string; sql: string; parameters: string[][] }> = [
    {
      label: 'accounts/api_keys scope',
      sql: `
        SELECT count(*)::text AS count
        FROM (
          SELECT accounts.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('accounts')} accounts
          LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} system_accounts
            ON system_accounts.id = accounts.system_account_id
           LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('resource_authorizations')} authorizations
             ON authorizations.id = accounts.authorization_instance_authorization_id
           LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('accounts')} source_accounts
             ON source_accounts.id = accounts.authorization_instance_source_account_id
          WHERE system_accounts.id IS NULL
             OR accounts.id <> ALL($1::text[])
             OR accounts.system_account_id <> ALL($2::text[])
             OR (accounts.authorization_instance_owner_system_account_id IS NOT NULL
                 AND accounts.authorization_instance_owner_system_account_id <> ALL($2::text[]))
              OR (accounts.authorization_instance_authorization_id IS NOT NULL
                  AND (
                    authorizations.id IS NULL
                    OR accounts.authorization_instance_authorization_id <> ALL($3::text[])
                    OR authorizations.resource_type <> 'account'
                    OR source_accounts.id IS NULL
                    OR authorizations.resource_id <> source_accounts.id
                    OR authorizations.resource_owner_system_account_id IS DISTINCT FROM source_accounts.system_account_id
                    OR authorizations.grantee_system_account_id IS DISTINCT FROM accounts.system_account_id
                    OR accounts.authorization_instance_owner_system_account_id IS DISTINCT FROM authorizations.resource_owner_system_account_id
                  ))
              OR (accounts.authorization_instance_source_account_id IS NOT NULL
                  AND source_accounts.id IS NULL)
          UNION ALL
          SELECT api_keys.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('api_keys')} api_keys
          LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} system_accounts
            ON system_accounts.id = api_keys.system_account_id
          LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('route_strategies')} route_strategies
            ON route_strategies.id = api_keys.route_strategy_id
          WHERE system_accounts.id IS NULL
             OR api_keys.id <> ALL($4::text[])
             OR api_keys.system_account_id <> ALL($2::text[])
              OR route_strategies.id IS NULL
              OR api_keys.route_strategy_id <> ALL($5::text[])
              OR route_strategies.system_account_id IS DISTINCT FROM api_keys.system_account_id
        ) invalid_references
      `,
      parameters: [scope.selectedAccountIds, scope.systemAccountIds, scope.resourceAuthorizationIds, scope.apiKeyIds, scope.routeStrategyIds]
    },
    {
      label: 'resource_authorizations scope',
      sql: `
        SELECT count(*)::text AS count
        FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('resource_authorizations')} authorizations
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('accounts')} account_rows
          ON authorizations.resource_type = 'account' AND account_rows.id = authorizations.resource_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('groups')} group_rows
          ON authorizations.resource_type = 'group' AND group_rows.id = authorizations.resource_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} owner_accounts
          ON owner_accounts.id = authorizations.resource_owner_system_account_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} grantee_accounts
          ON grantee_accounts.id = authorizations.grantee_system_account_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_teams')} source_teams
          ON authorizations.effective_source_team_id = source_teams.id
        WHERE authorizations.resource_type NOT IN ('account', 'group')
           OR authorizations.id <> ALL($5::text[])
           OR account_rows.id IS NULL AND authorizations.resource_type = 'account'
           OR group_rows.id IS NULL AND authorizations.resource_type = 'group'
           OR (authorizations.resource_type = 'account' AND authorizations.resource_id <> ALL($1::text[]))
           OR (authorizations.resource_type = 'account'
               AND account_rows.system_account_id IS DISTINCT FROM authorizations.resource_owner_system_account_id)
           OR (authorizations.resource_type = 'group' AND authorizations.resource_id <> ALL($4::text[]))
           OR (authorizations.resource_type = 'group'
               AND group_rows.system_account_id IS DISTINCT FROM authorizations.resource_owner_system_account_id)
           OR owner_accounts.id IS NULL OR authorizations.resource_owner_system_account_id <> ALL($2::text[])
           OR grantee_accounts.id IS NULL OR authorizations.grantee_system_account_id <> ALL($2::text[])
           OR (authorizations.effective_source_type = 'team'
               AND (source_teams.id IS NULL OR authorizations.effective_source_team_id <> ALL($3::text[])))
           OR authorizations.created_by <> ALL($2::text[])
           OR (authorizations.revoked_by IS NOT NULL AND authorizations.revoked_by <> ALL($2::text[]))
      `,
      parameters: [scope.selectedAccountIds, scope.systemAccountIds, scope.systemTeamIds, scope.groupIds, scope.resourceAuthorizationIds]
    },
    {
      label: 'resource_authorization_grants scope',
      sql: `
        SELECT count(*)::text AS count
        FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('resource_authorization_grants')} grant_rows
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('accounts')} account_rows
          ON grant_rows.resource_type = 'account' AND account_rows.id = grant_rows.resource_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('groups')} group_rows
          ON grant_rows.resource_type = 'group' AND group_rows.id = grant_rows.resource_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} owner_accounts
          ON owner_accounts.id = grant_rows.resource_owner_system_account_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} grantee_accounts
          ON grantee_accounts.id = grant_rows.grantee_system_account_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_teams')} grantee_teams
          ON grantee_teams.id = grant_rows.grantee_team_id
        WHERE grant_rows.resource_type NOT IN ('account', 'group')
           OR grant_rows.id <> ALL($5::text[])
           OR (grant_rows.resource_type = 'account' AND account_rows.id IS NULL)
           OR (grant_rows.resource_type = 'group' AND group_rows.id IS NULL)
           OR (grant_rows.resource_type = 'account' AND grant_rows.resource_id <> ALL($1::text[]))
           OR (grant_rows.resource_type = 'account'
               AND account_rows.system_account_id IS DISTINCT FROM grant_rows.resource_owner_system_account_id)
           OR (grant_rows.resource_type = 'group' AND grant_rows.resource_id <> ALL($4::text[]))
           OR (grant_rows.resource_type = 'group'
               AND group_rows.system_account_id IS DISTINCT FROM grant_rows.resource_owner_system_account_id)
           OR owner_accounts.id IS NULL OR grant_rows.resource_owner_system_account_id <> ALL($2::text[])
           OR (grant_rows.grantee_type = 'system_account'
               AND (grantee_accounts.id IS NULL OR grant_rows.grantee_system_account_id <> ALL($2::text[])))
           OR (grant_rows.grantee_type = 'team'
               AND (grantee_teams.id IS NULL OR grant_rows.grantee_team_id <> ALL($3::text[])))
           OR grant_rows.created_by <> ALL($2::text[])
           OR (grant_rows.revoked_by IS NOT NULL AND grant_rows.revoked_by <> ALL($2::text[]))
      `,
      parameters: [scope.selectedAccountIds, scope.systemAccountIds, scope.systemTeamIds, scope.groupIds, scope.resourceAuthorizationGrantIds]
    },
    {
      label: 'teams/groups/routes scope',
      sql: `
        SELECT count(*)::text AS count
        FROM (
          SELECT teams.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_teams')} teams
          WHERE teams.id <> ALL($1::text[]) OR teams.created_by <> ALL($2::text[])
          UNION ALL
          SELECT members.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_team_members')} members
          WHERE members.team_id <> ALL($1::text[])
             OR members.system_account_id <> ALL($2::text[])
             OR members.created_by <> ALL($2::text[])
          UNION ALL
          SELECT sources.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('resource_authorization_sources')} sources
          WHERE sources.authorization_id <> ALL($3::text[])
             OR (sources.source_team_id IS NOT NULL AND sources.source_team_id <> ALL($1::text[]))
             OR sources.created_by <> ALL($2::text[])
             OR (sources.revoked_by IS NOT NULL AND sources.revoked_by <> ALL($2::text[]))
          UNION ALL
          SELECT group_rows.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('groups')} group_rows
          WHERE group_rows.id <> ALL($4::text[]) OR group_rows.system_account_id <> ALL($2::text[])
          UNION ALL
          SELECT settings.authorization_id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('group_authorization_settings')} settings
          WHERE settings.authorization_id <> ALL($3::text[])
             OR settings.system_account_id <> ALL($2::text[])
             OR settings.group_id <> ALL($4::text[])
          UNION ALL
          SELECT group_accounts.group_id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('group_accounts')} group_accounts
          WHERE group_accounts.system_account_id <> ALL($2::text[])
             OR group_accounts.group_id <> ALL($4::text[])
             OR group_accounts.account_id <> ALL($5::text[])
             OR (group_accounts.account_authorization_id IS NOT NULL AND group_accounts.account_authorization_id <> ALL($3::text[]))
          UNION ALL
          SELECT routes.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('route_strategies')} routes
          WHERE routes.id <> ALL($6::text[]) OR routes.system_account_id <> ALL($2::text[])
          UNION ALL
          SELECT route_groups.id AS reference_id
          FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('route_strategy_groups')} route_groups
          WHERE route_groups.route_strategy_id <> ALL($6::text[])
             OR route_groups.system_account_id <> ALL($2::text[])
             OR route_groups.group_id <> ALL($4::text[])
        ) invalid_references
      `,
      parameters: [scope.systemTeamIds, scope.systemAccountIds, scope.resourceAuthorizationIds, scope.groupIds, scope.selectedAccountIds, scope.routeStrategyIds]
    },
    {
      label: 'request_quota_hourly_window_scope_bindings',
      sql: `
        SELECT count(*)::text AS count
        FROM ${quoteIdentifier(businessSchema)}.${quoteIdentifier('request_quota_hourly_window_scope_bindings')} bindings
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_accounts')} system_accounts
          ON system_accounts.id = bindings.system_account_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('api_keys')} source_api_keys
          ON bindings.source_type = 'api_key' AND source_api_keys.id = bindings.source_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('resource_authorization_grants')} source_grants
          ON bindings.source_type = 'resource_authorization_grant' AND source_grants.id = bindings.source_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('api_keys')} scope_api_keys
          ON bindings.scope_type = 'api_key' AND scope_api_keys.id = bindings.scope_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('resource_authorizations')} scope_authorizations
          ON bindings.scope_type IN ('account_authorization', 'group_authorization')
         AND scope_authorizations.id = bindings.scope_id
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('accounts')} account_team_scopes
          ON bindings.scope_type = 'account_authorization_team'
         AND account_team_scopes.id = split_part(bindings.scope_id, ':', 1)
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('groups')} group_team_scopes
          ON bindings.scope_type = 'group_authorization_team'
         AND group_team_scopes.id = split_part(bindings.scope_id, ':', 1)
        LEFT JOIN ${quoteIdentifier(businessSchema)}.${quoteIdentifier('system_teams')} scope_teams
          ON bindings.scope_type IN ('account_authorization_team', 'group_authorization_team')
         AND scope_teams.id = split_part(bindings.scope_id, ':', 2)
        WHERE bindings.system_account_id <> ALL($1::text[])
           OR system_accounts.id IS NULL
           OR bindings.source_type NOT IN ('api_key', 'resource_authorization_grant')
           OR (bindings.source_type = 'api_key' AND (source_api_keys.id IS NULL OR bindings.source_id <> ALL($2::text[])))
           OR (bindings.source_type = 'resource_authorization_grant' AND (source_grants.id IS NULL OR bindings.source_id <> ALL($3::text[])))
           OR (bindings.scope_type = 'api_key' AND (scope_api_keys.id IS NULL OR bindings.scope_id <> ALL($2::text[]) OR bindings.source_type <> 'api_key' OR bindings.source_id <> bindings.scope_id))
           OR (bindings.scope_type IN ('account_authorization', 'group_authorization')
               AND (scope_authorizations.id IS NULL OR bindings.scope_id <> ALL($4::text[]) OR bindings.source_type <> 'resource_authorization_grant'))
           OR (bindings.scope_type = 'account_authorization_team'
               AND (account_team_scopes.id IS NULL OR split_part(bindings.scope_id, ':', 1) <> ALL($5::text[]) OR scope_teams.id IS NULL OR split_part(bindings.scope_id, ':', 2) <> ALL($7::text[]) OR bindings.source_type <> 'resource_authorization_grant'))
           OR (bindings.scope_type = 'group_authorization_team'
               AND (group_team_scopes.id IS NULL OR split_part(bindings.scope_id, ':', 1) <> ALL($6::text[]) OR scope_teams.id IS NULL OR split_part(bindings.scope_id, ':', 2) <> ALL($7::text[]) OR bindings.source_type <> 'resource_authorization_grant'))
           OR (bindings.scope_type NOT IN ('api_key', 'account_authorization', 'group_authorization', 'account_authorization_team', 'group_authorization_team'))
      `,
      parameters: [scope.systemAccountIds, scope.apiKeyIds, scope.resourceAuthorizationGrantIds, scope.resourceAuthorizationIds, scope.selectedAccountIds, scope.groupIds, scope.systemTeamIds]
    }
  ]
  for (const check of checks) {
    const result = await target.query<{ count: string }>(check.sql, check.parameters)
    const count = Number(result.rows[0]?.count ?? 0)
    if (!Number.isFinite(count) || count !== 0) {
      throw new Error(`${check.label} 目标引用闭包校验失败：存在 ${Number.isFinite(count) ? count : '未知数量'} 条无效引用，事务将回滚`)
    }
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
  if (table === 'providers' && plan.selfForeignKeyPolicy === 'parent-before-child') selected = orderProviderRows(selected)
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
    if (table === 'api_keys') assertGeneratedApiKeyValues(generatedForRow)
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

function parseSingleColumnScopeKey(value: string, table = 'accounts'): string {
  let parsed: unknown
  try {
    parsed = JSON.parse(value)
  } catch {
    throw new Error(`${table} scope 行键不是合法 JSON：${value}`)
  }
  if (!Array.isArray(parsed) || parsed.length !== 1 || typeof parsed[0] !== 'string' || !parsed[0].trim()) {
    throw new Error(`${table} scope 行键必须是单列字符串主键：${value}`)
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

export function orderProviderRows(rows: Row[]): Row[] {
  const byCode = new Map<string, Row>()
  for (const row of rows) {
    const code = providerCode(row)
    if (byCode.has(code)) throw new Error(`providers 源库主键 code 重复：${code}`)
    byCode.set(code, row)
  }

  const visiting = new Set<string>()
  const visited = new Set<string>()
  const ordered: Row[] = []
  const visit = (row: Row): void => {
    const code = providerCode(row)
    if (visited.has(code)) return
    if (visiting.has(code)) throw new Error(`providers parent_code 拓扑存在环：${code}`)
    visiting.add(code)
    const parentValue = row.parent_code
    const parentCode = parentValue === null || parentValue === undefined ? '' : String(parentValue)
    if (parentCode) {
      const parent = byCode.get(parentCode)
      if (!parent) throw new Error(`providers ${code} 缺少 scope 内父节点 ${parentCode}`)
      visit(parent)
    }
    visiting.delete(code)
    visited.add(code)
    ordered.push(row)
  }

  for (const row of rows) visit(row)
  return ordered
}

function providerCode(row: Row): string {
  const value = row.code
  const code = typeof value === 'string' ? value : String(value ?? '')
  if (!code.trim()) throw new Error('providers 源库存在空 code，无法建立 parent_code 拓扑')
  return code
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
  const [preflightPath, planPath, scopePath, closurePath, generatedPath] = process.argv.slice(2)
  if (!preflightPath || !planPath || !scopePath || !closurePath || !generatedPath || process.argv.slice(2).length !== 5) {
    throw new Error('用法：execute-rehearsal-account-sync <preflight.json> <field-level-plan.json> <scope.json> <closure.json> <generated-values.json>')
  }
  const [preflightText, planText, scopeText, closureText, generatedText] = await Promise.all([
    readFile(preflightPath, 'utf8'), readFile(planPath, 'utf8'), readFile(scopePath, 'utf8'), readFile(closurePath, 'utf8'), readFile(generatedPath, 'utf8')
  ])
  const preflight = JSON.parse(preflightText) as AccountSyncPreflightReport
  const plan = JSON.parse(planText) as RehearsalAccountSyncPlan
  const scope = JSON.parse(scopeText) as ScopeManifest
  const closure = JSON.parse(closureText) as AccountClosureManifest
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
    const report = await executeRehearsalAccountSync(source, target, preflight, plan, preflightText, scope, closure, generatedValues)
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
