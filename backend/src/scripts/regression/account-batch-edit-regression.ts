import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-batch-edit-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-batch-edit-regression-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  balanceRepository,
  { batchEditAccountsAsync, loadAccountBatchEditContextAsync },
  {
    AccountBatchUpdateAccessError,
    AccountBatchUpdateVersionConflictError
  },
  { accountBatchEditSchema },
  { sanitizeAccountBatchEditDetailResponse },
  { invalidateAccountLookupCache },
  { buildPostgresSchemaSql }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-balance.repository.js'),
  import('../../modules/accounts/account-batch-edit.service.js'),
  import('../../storage/account-batch-update.repository.js'),
  import('../../modules/accounts/account-request.schemas.js'),
  import('../../modules/accounts/account-response-sanitizer.js'),
  import('../../storage/repository-lookups.js'),
  import('../../storage/postgres-schema.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  assertRouteAndSchemaBoundary()
  const group = repositories.createGroup({
    providerCode: 'gpt',
    name: '批量编辑回归分组'
  }, access)
  const accountA = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '批量编辑账户 A',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-batch-edit-a',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const accountB = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '批量编辑账户 B',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-batch-edit-b',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  setAccountsActive([accountA.id, accountB.id])

  const initialA = requiredAccount(accountA.id)
  const initialB = requiredAccount(accountB.id)
  assert.equal(initialA.configRevision, 1, '新账户批量版本应从 1 开始')
  assert.equal(initialB.configRevision, 1, '新账户批量版本应从 1 开始')

  const otherOwner = repositories.createSystemAccount({
    username: 'account_batch_edit_other_owner',
    displayName: '批量编辑其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const crossOwnerProxy = repositories.createProxy({
    name: '批量编辑跨用户代理',
    type: 'http',
    host: '127.0.0.1',
    port: 7890,
    enabled: true
  }, { systemAccountId: otherOwner.id, role: 'user' })
  await assert.rejects(
    batchEditAccountsAsync({
      targets: targets(initialA, initialB),
      updates: {
        proxyProfileId: { enabled: true, value: crossOwnerProxy.id }
      }
    }, access),
    /代理不存在或已停用/,
    '批量账户不能绑定其他用户拥有的代理'
  )
  const afterCrossOwnerProxyA = requiredAccount(accountA.id)
  const afterCrossOwnerProxyB = requiredAccount(accountB.id)
  assert.equal(afterCrossOwnerProxyA.configRevision, 1, '跨用户代理被拒绝后账户 A 版本必须不变')
  assert.equal(afterCrossOwnerProxyB.configRevision, 1, '跨用户代理被拒绝后账户 B 版本必须不变')
  assert.equal(afterCrossOwnerProxyA.proxyProfileId, undefined, '跨用户代理被拒绝后账户 A 代理必须不变')
  assert.equal(afterCrossOwnerProxyB.proxyProfileId, undefined, '跨用户代理被拒绝后账户 B 代理必须不变')
  assert.equal(afterCrossOwnerProxyA.notes, undefined, '跨用户代理被拒绝后账户 A 其他配置必须不变')
  assert.equal(afterCrossOwnerProxyB.notes, undefined, '跨用户代理被拒绝后账户 B 其他配置必须不变')

  const firstResult = await batchEditAccountsAsync({
    targets: targets(initialA, initialB),
    updates: {
      tags: { enabled: true, value: ['生产', '主力'] },
      concurrencyLimit: { enabled: true, value: 7 },
      priority: { enabled: true, value: 9 },
      notes: { enabled: true, value: '批量覆盖备注' },
      errorHandlingRules: {
        enabled: true,
        value: [{
          enabled: true,
          name: '429 切换',
          priority: 1,
          action: 'retry_next',
          status_codes: [429]
        }]
      }
    }
  }, access)
  assert.equal(firstResult.accounts.length, 2, '正常批量编辑应返回全部账户')
  for (const account of firstResult.accounts) {
    assert.equal(account.configRevision, 2, '成功批量编辑应把 config_revision 加 1')
    assert.equal(account.concurrencyLimit, 7, '并发限制应直接覆盖')
    assert.equal(account.priority, 9, '优先级应直接覆盖')
    assert.equal(account.notes, '批量覆盖备注', '备注应直接覆盖')
    assert.deepEqual(account.tags?.map((tag) => tag.name).sort(), ['主力', '生产'], '标签应直接覆盖')
    assert.equal(account.status, 'active', '非连接配置不应改变账户状态')
    assert.equal(account.schedulable, true, '非连接配置不应改变调度状态')
    const revision = databaseModule.getBusinessDatabase()
      .prepare('SELECT dispatch_revision FROM accounts WHERE id = ?')
      .get(account.id) as unknown as { dispatch_revision: number }
    assert.equal(revision.dispatch_revision, 2, '优先级、并发和用户错误策略不得推进传输电路 revision')
  }
  assertCredentialPoliciesMerged(accountA.id, 'sk-account-batch-edit-a')
  assertCredentialPoliciesMerged(accountB.id, 'sk-account-batch-edit-b')
  const batchContext = await loadAccountBatchEditContextAsync([accountA.id, accountB.id], access)
  assert.equal(batchContext.length, 2, '批量编辑上下文应一次返回全部目标账户')
  const sanitizedContext = batchContext.map(sanitizeAccountBatchEditDetailResponse)
  assert.equal(sanitizedContext[0]?.credentials.api_key, undefined, '批量编辑上下文不得返回 API Key')
  assert.equal(sanitizedContext[0]?.credentials.base_url, undefined, '批量编辑上下文不得返回 Base URL')
  assert.ok(sanitizedContext[0]?.credentials.error_handling_rules, '批量编辑上下文应返回允许覆盖的错误策略')

  await assert.rejects(
    batchEditAccountsAsync({
      targets: [
        { accountId: accountA.id, configRevision: 2 },
        { accountId: accountB.id, configRevision: 1 }
      ],
      updates: {
        concurrencyLimit: { enabled: true, value: 11 }
      }
    }, access),
    AccountBatchUpdateVersionConflictError,
    '任一版本冲突应拒绝整批写入'
  )
  assert.equal(requiredAccount(accountA.id).concurrencyLimit, 7, '版本冲突后账户 A 不应被部分写入')
  assert.equal(requiredAccount(accountB.id).concurrencyLimit, 7, '版本冲突后账户 B 不应被部分写入')

  await assert.rejects(
    batchEditAccountsAsync({
      targets: targets(requiredAccount(accountA.id), requiredAccount(accountB.id)),
      updates: {
        supportedModels: { enabled: true, value: ['gpt-5.5'] }
      }
    }, access),
    /检查模型必须属于最终支持模型/,
    '覆盖支持模型时必须按每个账户最终检查模型校验'
  )
  assert.equal(requiredAccount(accountA.id).configRevision, 2, '模型校验失败后不应增加账户 A 版本')
  assert.equal(requiredAccount(accountB.id).configRevision, 2, '模型校验失败后不应增加账户 B 版本')

  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET next_health_check_at = ?,
        health_check_failure_count = 2,
        last_health_check_error_code = 'legacy_health_failure',
        last_health_check_error_message = '既有健康检查失败'
    WHERE id IN (?, ?)
  `).run(new Date(Date.now() + 60 * 60_000).toISOString(), accountA.id, accountB.id)
  invalidateAccountLookupCache(accountA.id)
  invalidateAccountLookupCache(accountB.id)

  const connectionResult = await batchEditAccountsAsync({
    targets: targets(requiredAccount(accountA.id), requiredAccount(accountB.id)),
    updates: {
      supportedModels: { enabled: true, value: ['gpt-5.6-sol', 'gpt-5.5'] },
      healthCheckModel: { enabled: true, value: 'gpt-5.6-sol' }
    }
  }, access)
  for (const account of connectionResult.accounts) {
    assert.equal(account.configRevision, 3, '模型配置成功覆盖应增加版本')
    assert.equal(account.status, 'active', '支持模型和检查模型变化不应改变账户状态')
    assert.equal(account.schedulable, true, '支持模型和检查模型变化不应停止正常调度')
    assert.deepEqual(account.supportedModels?.sort(), ['gpt-5.5', 'gpt-5.6-sol'], '支持模型应直接覆盖')
    assert.equal(account.healthCheckModel, 'gpt-5.6-sol', '检查模型应直接覆盖')
  }
  const healthRows = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, next_health_check_at, health_check_failure_count, last_health_check_error_code
      FROM accounts
      WHERE id IN (?, ?)
      ORDER BY id ASC
    `)
    .all(accountA.id, accountB.id) as unknown as Array<{
      id: string
      next_health_check_at: string | null
      health_check_failure_count: number
      last_health_check_error_code: string | null
    }>
  assert.equal(healthRows.length, 2, '应读取两个连接配置变更账户')
  assert(healthRows.every((row) => row.next_health_check_at === null), '模型配置变更后应交给后台立即扫描')
  assert(healthRows.every((row) => row.health_check_failure_count === 2), '模型配置变更不应篡改既有健康失败事实')
  assert(healthRows.every((row) => row.last_health_check_error_code === 'legacy_health_failure'), '模型配置变更不应清理既有健康诊断')

  const overrideResult = await batchEditAccountsAsync({
    targets: targets(requiredAccount(accountA.id), requiredAccount(accountB.id)),
    updates: {
      supportedModels: { enabled: true, value: ['gpt-5.6-sol'] },
      serviceTierOverride: { enabled: true, value: 'priority' },
      reasoningEffortOverride: { enabled: true, value: 'high' }
    }
  }, access)
  for (const account of overrideResult.accounts) {
    assert.equal(account.configRevision, 4, 'GPT 覆盖成功后应增加配置版本')
    assert.equal(account.status, 'active', 'GPT 请求覆盖变化不应改变账户状态')
    assert.equal(account.schedulable, true, 'GPT 请求覆盖变化不应停止账户调度')
    const stored = repositories.findAccountForTest(account.id, access)
    assert.equal(stored?.credentials.service_tier_override, 'priority', '批量服务等级应写入 snake_case credentials')
    assert.equal(stored?.credentials.reasoning_effort_override, 'high', '批量思考级别应写入 snake_case credentials')
  }

  const expandedModelsResult = await batchEditAccountsAsync({
    targets: targets(requiredAccount(accountA.id), requiredAccount(accountB.id)),
    updates: {
      supportedModels: { enabled: true, value: ['gpt-5.6-sol', 'gpt-5.5'] }
    }
  }, access)
  for (const account of expandedModelsResult.accounts) {
    assert.equal(account.configRevision, 5, '增加低能力模型时应保留由其他模型支持的期望覆盖')
  }

  const clearedOverrideResult = await batchEditAccountsAsync({
    targets: targets(requiredAccount(accountA.id), requiredAccount(accountB.id)),
    updates: {
      supportedModels: { enabled: true, value: ['gpt-5.6-sol', 'gpt-5.5'] },
      serviceTierOverride: { enabled: true, value: null },
      reasoningEffortOverride: { enabled: true, value: '' }
    }
  }, access)
  for (const account of clearedOverrideResult.accounts) {
    assert.equal(account.configRevision, 6, '清除 GPT 覆盖后应增加配置版本')
    const stored = repositories.findAccountForTest(account.id, access)
    assert.equal(stored?.credentials.service_tier_override, undefined, 'null 应清除服务等级覆盖')
    assert.equal(stored?.credentials.reasoning_effort_override, undefined, '空字符串应清除思考级别覆盖')
  }

  const oauthAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '批量编辑 OAuth 异构账户',
    type: 'oauth',
    credentials: {
      refresh_token: 'refresh-account-batch-edit',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  await assert.rejects(
    batchEditAccountsAsync({
      targets: targets(requiredAccount(accountA.id), requiredAccount(oauthAccount.id)),
      updates: {
        supportedModels: { enabled: true, value: ['gpt-5.6-sol'] },
        healthCheckModel: { enabled: true, value: 'gpt-5.6-sol' }
      }
    }, access),
    /相同供应商、协议档案和账户类型/,
    '模型配置批次必须拒绝不同账户类型'
  )
  assert.equal(requiredAccount(accountA.id).configRevision, 6, '异构模型批次失败不应修改既有账户')

  await assert.rejects(
    batchEditAccountsAsync({
      targets: targets(requiredAccount(accountA.id), requiredAccount(accountB.id)),
      updates: {
        notes: { enabled: true, value: '不应越权写入' }
      }
    }, {
      ...access,
      systemAccountFilterId: 'sys_missing_owner'
    }),
    AccountBatchUpdateAccessError,
    '管理端筛选作用域不匹配时应整批拒绝'
  )
  assert.equal(requiredAccount(accountA.id).notes, '批量覆盖备注', '越权批次不应修改账户')

  const staleA = requiredAccount(accountA.id)
  const staleB = requiredAccount(accountB.id)
  const singleUpdatedA = repositories.updateAccount(accountA.id, { notes: '单账户编辑后的备注' }, access)
  assert.equal(singleUpdatedA?.configRevision, (staleA.configRevision ?? 0) + 1, '普通单账户编辑必须递增配置版本')
  await assert.rejects(
    batchEditAccountsAsync({
      targets: targets(staleA, staleB),
      updates: {
        concurrencyLimit: { enabled: true, value: 19 }
      }
    }, access),
    AccountBatchUpdateVersionConflictError,
    '普通编辑后使用旧版本发起批量覆盖必须整批拒绝'
  )
  assert.equal(requiredAccount(accountB.id).concurrencyLimit, 7, '旧版本批量覆盖被拒绝后其他账户不得被部分修改')

  const beforeTagRevision = requiredAccount(accountA.id).configRevision ?? 0
  repositories.updateAccountTags(accountA.id, ['配置版本标签'], access)
  assert.equal(requiredAccount(accountA.id).configRevision, beforeTagRevision + 1, '单独修改标签必须递增配置版本')

  const beforeHealthModel = requiredAccount(accountA.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET next_health_check_at = ?
    WHERE id = ?
  `).run(new Date(Date.now() + 60 * 60_000).toISOString(), accountA.id)
  invalidateAccountLookupCache(accountA.id)
  const healthModelUpdated = repositories.updateAccountHealthCheckModel(accountA.id, 'gpt-5.5', access)
  assert.equal(healthModelUpdated?.configRevision, (beforeHealthModel.configRevision ?? 0) + 1, '单独修改检查模型必须递增配置版本')
  assert.equal(healthModelUpdated?.status, 'active', '单独修改检查模型不得改变账户状态')
  const healthScheduleRow = databaseModule.getBusinessDatabase()
    .prepare('SELECT next_health_check_at FROM accounts WHERE id = ?')
    .get(accountA.id) as unknown as { next_health_check_at: string | null }
  assert.equal(healthScheduleRow.next_health_check_at, null, '单独修改检查模型应安排后台立即检查')

  const multiA = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '批量编辑多 Key A', type: 'api_key',
    credentials: { api_keys: ['sk-batch-multi-a1', 'sk-batch-multi-a2'], base_url: 'https://api.openai.com/v1' },
    groupId: group.id
  }, access)
  const multiB = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '批量编辑多 Key B', type: 'api_key',
    credentials: { api_keys: ['sk-batch-multi-b1', 'sk-batch-multi-b2'], base_url: 'https://api.openai.com/v1' },
    groupId: group.id
  }, access)
  setAccountsActive([multiA.id, multiB.id])
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET balance_query_enabled = 1,
        balance_query_config_json = ?,
        balance_query_next_refresh_at = ?
    WHERE id IN (?, ?)
  `).run('{}', new Date().toISOString(), multiA.id, multiB.id)
  for (const account of [multiA, multiB]) {
    balanceRepository.replaceAccountBalanceSnapshot({
      accountId: account.id,
      systemAccountId: 'sys_admin',
      snapshot: { status: 'fresh', remainingUsd: '3.210000' }
    })
  }
  await batchEditAccountsAsync({
    targets: targets(requiredAccount(multiA.id), requiredAccount(multiB.id)),
    updates: { notes: { enabled: true, value: '多 Key 批量保存' } }
  }, access)
  const multiRows = databaseModule.getBusinessDatabase().prepare(`
    SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
    FROM accounts WHERE id IN (?, ?)
  `).all(multiA.id, multiB.id) as Array<Record<string, unknown>>
  assert.equal(multiRows.length, 2)
  for (const row of multiRows) {
    assert.equal(row.balance_query_enabled, 0, '批量编辑入口必须中央关闭多 Key 账户余额')
    assert.equal(row.balance_query_next_refresh_at, null, '批量编辑关闭余额必须在同一事务清空调度')
    assert.deepEqual(JSON.parse(String(row.balance_query_config_json)), { adapter: 'builtin', intervalMinutes: 5 }, '批量编辑必须为旧空配置写入已配置关闭标记')
  }
  await waitFor(() => balanceRepository.loadAccountBalanceSnapshotsByAccountIds([multiA.id, multiB.id]).size === 0)
  assert.equal(balanceRepository.loadAccountBalanceSnapshotsByAccountIds([multiA.id, multiB.id]).size, 0, '批量事务提交后登记的有界后台任务必须最终幂等清理旧余额快照')

  await assertBatchModelMappingTargetCapabilities(group.id)

  console.log('account-batch-edit-regression passed')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertRouteAndSchemaBoundary(): void {
  const accountsRouteSource = readFileSync(resolve('src', 'modules', 'accounts', 'accounts.routes.ts'), 'utf8')
  const batchRouteSource = readFileSync(resolve('src', 'modules', 'accounts', 'account-batch-edit.routes.ts'), 'utf8')
  const repositorySource = readFileSync(resolve('src', 'storage', 'account-batch-update.repository.ts'), 'utf8')
  const systemApiSource = readFileSync(resolve('src', 'modules', 'system-api', 'system-api-app.ts'), 'utf8')
  assert(accountsRouteSource.includes('registerAccountBatchEditRoutes(accountsRouter)'), '账户主路由必须注册批量编辑子路由')
  assert(batchRouteSource.includes("router.post('/batch-edit-context'"), '批量编辑子路由必须提供去敏上下文入口')
  assert(batchRouteSource.includes("router.post('/batch-update'"), '批量编辑子路由必须提供统一 POST 入口')
  assert(systemApiSource.includes("app.use(`${systemApiPrefix}/my-accounts`, forceSelfAccessScope, accountsRouter)"), '批量编辑路由必须服务个人账户作用域')
  assert(systemApiSource.includes("app.use(`${systemApiPrefix}/accounts`, requireAdmin, accountsRouter)"), '批量编辑路由必须服务管理账户作用域')
  assert(repositorySource.includes("client.driver === 'postgres' ? ' FOR UPDATE' : ''"), 'PostgreSQL 批量编辑必须锁定目标账户')
  assert(repositorySource.includes('config_revision = config_revision + 1'), '批量编辑成功后必须递增配置版本')
  assert(repositorySource.includes('balance_query_enabled = CASE WHEN ? = 1 THEN 0'), 'SQLite/PostgreSQL 批量编辑 SQL 必须统一关闭多 Key 余额')
  assert(!repositorySource.includes('updateAccountAsync('), '批量编辑 repository 不能循环调用单账户更新')
  assert.match(
    buildPostgresSchemaSql(),
    /CREATE TABLE IF NOT EXISTS accounts[\s\S]+config_revision integer NOT NULL DEFAULT 1/,
    'PostgreSQL 新建 schema 必须包含账户配置版本'
  )
  assert.equal(accountBatchEditSchema.safeParse({
    targets: [
      { accountId: 'account-a', configRevision: 1 },
      { accountId: 'account-b', configRevision: 1 }
    ],
    updates: {
      status: { enabled: true, value: 'active' }
    }
  }).success, false, '批量接口必须拒绝 status 等非白名单字段')
  assert.equal(accountBatchEditSchema.safeParse({
    targets: [
      { accountId: 'account-a', configRevision: 1 },
      { accountId: 'account-b', configRevision: 1 }
    ],
    updates: {
      notes: { enabled: false }
    }
  }).success, false, '批量接口必须至少启用一个覆盖字段')
  assert.equal(accountBatchEditSchema.safeParse({
    targets: [
      { accountId: 'account-a', configRevision: 1 },
      { accountId: 'account-b', configRevision: 1 }
    ],
    updates: {
      serviceTierOverride: { enabled: true, value: null },
      reasoningEffortOverride: { enabled: true, value: 'ultra' }
    }
  }).success, false, '批量接口必须拒绝账户级 Ultra，并允许用 null 清除服务等级')
}

function setAccountsActive(accountIds: string[]): void {
  const database = databaseModule.getBusinessDatabase()
  const update = database.prepare(`
    UPDATE accounts
    SET status = 'active',
        schedulable = 1,
        cooldown_until = NULL,
        last_error_code = NULL,
        last_error_message = NULL
    WHERE id = ?
  `)
  for (const accountId of accountIds) {
    update.run(accountId)
    invalidateAccountLookupCache(accountId)
  }
}

function requiredAccount(accountId: string) {
  const account = repositories.findAccountSummary(accountId, access)
  assert(account, `账户不存在：${accountId}`)
  assert.equal(typeof account.configRevision, 'number', `账户缺少 configRevision：${accountId}`)
  return account
}

function targets(...accounts: Array<{ id: string; configRevision?: number }>) {
  return accounts.map((account) => ({
    accountId: account.id,
    configRevision: account.configRevision as number
  }))
}

async function waitFor(predicate: () => boolean, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待批量余额快照后台清理超时')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

function assertCredentialPoliciesMerged(accountId: string, expectedApiKey: string): void {
  const account = repositories.findAccountForTest(accountId, access)
  assert(account, `测试账户不存在：${accountId}`)
  assert.equal(account.credentials.api_key, expectedApiKey, '批量规则覆盖不能修改 API Key')
  assert.equal(account.credentials.base_url, 'https://api.openai.com/v1', '批量规则覆盖不能修改 Base URL')
  assert.deepEqual(account.credentials.error_handling_rules, [{
    enabled: true,
    name: '429 切换',
    priority: 1,
    action: 'retry_next',
    status_codes: [429]
  }], '批量规则覆盖应只 merge 指定凭据键')
}

async function assertBatchModelMappingTargetCapabilities(groupId: string): Promise<void> {
  const create = (suffix: string) => repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `批量映射目标能力账户 ${suffix}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-batch-mapping-${suffix}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'responses_sse']
    },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    groupId
  }, access)
  const accountC = create('C')
  const accountD = create('D')
  const enabledMapping = {
    sourceModel: 'gpt-5.5',
    sourceEndpointFamily: 'responses' as const,
    upstreamModel: 'gpt-5.5',
    upstreamEndpointFamily: 'chat_completions' as const,
    enabled: true
  }
  await assert.rejects(batchEditAccountsAsync({
    targets: targets(requiredAccount(accountC.id), requiredAccount(accountD.id)),
    updates: {
      supportedEndpointModes: { enabled: true, value: ['responses_json', 'responses_sse'] },
      healthCheckEndpointMode: { enabled: true, value: 'responses_sse' },
      modelMappings: { enabled: true, value: [enabledMapping] }
    }
  }, access), /Chat Completions.*上游接口能力/, '批量后端必须拒绝目标族能力缺失的启用映射')
  assert.equal(requiredAccount(accountC.id).configRevision, 1, '批量映射能力冲突被拒绝后账户 C 版本必须不变')
  assert.equal(requiredAccount(accountD.id).configRevision, 1, '批量映射能力冲突被拒绝后账户 D 版本必须不变')

  const disabledMapping = { ...enabledMapping, enabled: false }
  const accepted = await batchEditAccountsAsync({
    targets: targets(requiredAccount(accountC.id), requiredAccount(accountD.id)),
    updates: {
      supportedEndpointModes: { enabled: true, value: ['responses_json', 'responses_sse'] },
      healthCheckEndpointMode: { enabled: true, value: 'responses_sse' },
      modelMappings: { enabled: true, value: [disabledMapping] }
    }
  }, access)
  assert.equal(accepted.accounts.length, 2, '批量后端应允许目标族能力缺失的停用映射')
  for (const account of accepted.accounts) {
    assert.deepEqual(account.modelMappings, [disabledMapping], '批量后端应原样保留停用映射')
  }
}
