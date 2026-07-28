import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-projection-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-list-projection-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '列表严格投影分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '列表严格投影账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-list-projection',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    modelMappings: [],
    tags: ['严格投影'],
    status: 'active',
    groupId: group.id
  }, access)

  seedAccountUsage(account.id)
  const businessDatabase = databaseModule.getBusinessDatabase()
  const statsDatabase = databaseModule.getStatsDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const originalStatsPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  const listSql: string[] = []
  businessDatabase.prepare = ((sql: string) => {
    listSql.push(sql)
    return originalBusinessPrepare(sql)
  }) as typeof businessDatabase.prepare
  statsDatabase.prepare = ((sql: string) => {
    if (/\b(?:usage_stats_daily|usage_stats_totals|account_usage_snapshots)\b/i.test(sql)) {
      throw new Error(`基础列表不应读取用量或余额统计：${sql}`)
    }
    return originalStatsPrepare(sql)
  }) as typeof statsDatabase.prepare
  const result = await repositories.listAccountManagementItemsPageReadOnly(access, { page: 1, pageSize: 20 })
  businessDatabase.prepare = originalBusinessPrepare
  statsDatabase.prepare = originalStatsPrepare
  const item = result.items.find((candidate) => candidate.id === account.id)
  assert(item, '账户基础列表应返回新建账户')

  const forbiddenFields = [
    'credentials',
    'supportedModels',
    'modelMappings',
    'apiKeyRuntimeDetails',
    'apiKeyRuntime',
    'balanceQueryEnabled',
    'balanceQueryConfig',
    'balanceQueryNextRefreshAt',
    'balanceSnapshot',
    'usage',
    'todayUsage',
    'currentConcurrency',
    'lastUsedAt',
    'runtimeAvailability',
    'effectiveAvailability',
    'availabilityPresentation',
    'status',
    'schedulable',
    'accountExpiresAt',
    'cooldownUntil',
    'lastErrorCode',
    'lastErrorMessage',
    'lastErrorTraceId',
    'cooldownRetestFailureCount',
    'cooldownRetestObservationStartedAt',
    'cooldownRetestLastAt',
    'cooldownRetestLastStatusCode',
    'lastHealthCheckAt',
    'nextHealthCheckAt',
    'lastHealthSuccessAt',
    'healthCheckFailureCount',
    'healthCheckFailureStartedAt',
    'lastHealthCheckStatusCode',
    'lastHealthCheckErrorCode',
    'lastHealthCheckErrorMessage',
    'lastHealthCheckTraceId',
    'streamFailureCount',
    'streamFailureWindowStartedAt',
    'oauthUsage',
    'authorizationSources',
    'authorizationCount',
    'authorizationTeamCount'
  ] as const
  for (const field of forbiddenFields) {
    assert.equal(Object.prototype.hasOwnProperty.call(item, field), false, `账户基础列表不应返回重量字段 ${field}`)
  }
  const allowedFields = new Set([
    'id', 'configRevision', 'systemAccountId', 'systemAccountName', 'ownerSystemAccountId', 'ownerSystemAccountName',
    'providerCode', 'providerProtocolProfileId', 'protocolCode', 'protocolVersion', 'name', 'notes', 'type',
    'concurrencyLimit', 'priority', 'superPriorityEnabled', 'fallbackEnabled', 'clientCompatibility', 'tags',
    'healthCheckModel', 'healthCheckEndpointMode', 'proxyProfileId', 'proxyProfileName',
    'proxyProfileType', 'proxyProfileEnabled', 'proxyProfileUnavailable', 'proxyProfileErrorMessage',
    'availabilitySchedule', 'accessType', 'accountAuthorizationId', 'boundGroupId', 'boundGroupName', 'groupBindStatus',
    'bindingSystemAccountId', 'permissions'
  ])
  assert.deepEqual(
    Object.keys(item).filter((key) => !allowedFields.has(key)),
    [],
    '基础列表 DTO 只能包含显式白名单字段'
  )
  assert.deepEqual(Object.keys(item.permissions).sort(), [
    'canAuthorize', 'canDelete', 'canEdit', 'canReturnAuthorization', 'canUse', 'canViewCredentials'
  ])
  assert.deepEqual(Object.keys(item.tags[0] ?? {}).sort(), ['id', 'name'], '列表 tag 只能返回 id/name')
  assert.equal(item.name, '列表严格投影账户')
  assert.equal(item.providerCode, 'gpt')
  assert.equal(item.permissions?.canEdit, true, '基础列表必须保留行操作权限')

  const executedSql = listSql.join('\n')
  assert.doesNotMatch(executedSql, /\b(?:accounts|source_accounts)\.\*/i, '列表 SQL 禁止通配读取账户列')
  assert.doesNotMatch(executedSql, /\bcredentials_encrypted\b|\bcredential_mask\b/i, '列表 SQL 禁止读取凭据列')
  assert.doesNotMatch(executedSql, /\bsupported_models\b|\bmodel_mappings\b|\bapi_key_runtime\b/i, '列表 SQL 禁止读取模型或 API Key 运行态')
  assert.doesNotMatch(executedSql, /account_quality_scores|account_management_quality|\bATTACH\s+DATABASE\b/i, '列表 SQL 禁止读取未展示的质量统计')

  const repositorySource = readFileSync(resolve('src/storage/account-management-list.repository.ts'), 'utf8')
  assert.doesNotMatch(repositorySource, /AccountSummary|account-summary\.repository|account-read\.repository|usage-summary-loaders|account-api-key-runtime-state\.repository/)
  const routeSource = readFileSync(resolve('src/modules/accounts/account-list.routes.ts'), 'utf8')
  assert.match(routeSource, /listAccountManagementItemsPageAsync/)
  assert.doesNotMatch(routeSource, /listAccountItemsPageAsync|sanitizeAccountListResponse/)

  console.log('AI 账户列表投影回归通过：首包只返回轻量静态字段，动态字段由当前页快照补齐')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedAccountUsage(accountId: string): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id,
        request_count, input_tokens, output_tokens, total_cost_usd,
        last_used_at, updated_at
      ) VALUES (?, 'account', ?, 9, 120, 80, 0.01, ?, ?)
    `)
    .run('sys_admin', accountId, now, now)
}
