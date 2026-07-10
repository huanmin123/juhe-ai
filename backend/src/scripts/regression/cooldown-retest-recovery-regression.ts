import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { installWorkerParentIpcHarness } from '../shared/worker-parent-ipc-harness.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-cooldown-retest-recovery-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'cooldown-retest-recovery-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const restoreWorkerParentIpc = installWorkerParentIpcHarness()

const [databaseModule, repositories, gatewayRuntimeCache, cooldownRetestService, { closeSqliteReadWorkerPool }] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/background/cooldown-account-retest.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

let mockOpenAIServer: http.Server | undefined
let mockOpenAIResponseHitCount = 0

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('冷却复测恢复 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  const group = repositories.createGroup({
    name: '冷却复测回归分组',
    providerCode: 'gpt'
  }, access)
  const workerGatewaySettings = await gatewayRuntimeCache.readCachedGatewaySettingsAsync()
  assert.equal(typeof workerGatewaySettings.defaultTemporaryUnschedulableMinutes, 'number', 'worker 角色应能本地读取网关设置，不能误走 DB service IPC')
  const workerGroupAccess = await gatewayRuntimeCache.resolveCachedGroupUsageAccessMetadataAsync(group.id, access.systemAccountId)
  assert.equal(workerGroupAccess?.groupOwnerSystemAccountId, access.systemAccountId, 'worker 角色应能本地读取分组访问元数据，不能误走 DB service IPC')
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id
  }, access)
  assert(repositories.setAccountGroup(account.id, group.id, access), '冷却复测观察窗口账号应能绑定分组')
  const cooled = repositories.markAccountTemporaryUnavailable(account.id, '模拟临时不可调用')
  assert.equal(cooled?.status, 'temporary_unavailable', '临时不可调用应进入恢复通道')
  assert.ok(cooled?.cooldownRetestObservationStartedAt, '进入临时不可调用时应记录自动恢复观察起点')
  assert.ok(Date.parse(cooled.cooldownUntil ?? '') - Date.now() <= 10_000, '临时不可调用首次暂停应走秒级快速恢复')

  const expiredObservationStartedAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(expiredObservationStartedAt, new Date(Date.now() - 1000).toISOString(), account.id)

  const longRecovering = repositories.recordCooldownAccountRetestFailure(account.id, {
    statusCode: 401,
    errorMessage: '仍然不可用',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440,
    longTermIntervalHours: 24
  })
  assert.equal(longRecovering.action, 'long_term_cooldown', '超过观察窗口后应进入长期不可用低频恢复')
  assert.equal(longRecovering.recoveryStage, 'long_term', '超过观察窗口后应标记长期恢复阶段')
  assert.equal(longRecovering.account?.status, 'temporary_unavailable', '超过观察窗口后账号仍应保持临时不可调用')
  assert.equal(longRecovering.account?.schedulable, true, '长期不可用账号应保留后台可恢复调度标记')
  assert.ok(longRecovering.account?.cooldownUntil, '长期不可用账号应写入下一次低频复测时间')
  assert.equal(longRecovering.account?.lastErrorCode, 'cooldown_retest_long_term_unavailable', '超过观察窗口后应写入长期不可用原因码')
  assert.match(longRecovering.errorMessage, /进入长期不可用低频复测/, '失败摘要应说明仍会低频自动复测')
  assert(!repositories.listAccountsDueForCooldownRetest(20).some((item) => item.id === account.id), '长期不可用账号在下次复测时间前不应进入候选')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), account.id)
  assert(repositories.listAccountsDueForCooldownRetest(20).some((item) => item.id === account.id), '长期不可用账号到达下次复测时间后仍应进入后台复测候选')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(), account.id)

  const freshAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测未超观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery-fresh',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id
  }, access)
  assert(repositories.setAccountGroup(freshAccount.id, group.id, access), '冷却复测未超观察窗口账号应能绑定分组')
  repositories.markAccountTemporaryUnavailable(freshAccount.id, '模拟临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(new Date().toISOString(), new Date(Date.now() - 1000).toISOString(), freshAccount.id)

  const stillRecovering = repositories.recordCooldownAccountRetestFailure(freshAccount.id, {
    traceId: 'trace-cooldown-retest-quota',
    statusCode: 403,
    errorCode: 'insufficient_quota',
    errorMessage: '余额和订阅额度均不足，请充值后再使用 (request id: upstream-request-id-should-display)',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(stillRecovering.recoveryStage, 'slow', '超过快速阈值后应进入慢速恢复')
  assert.notEqual(stillRecovering.action, 'long_term_cooldown', '未超过观察阈值时不应进入长期不可用')
  const freshAfterRetest = repositories.findAccountSummary(freshAccount.id, access)
  assert.equal(freshAfterRetest?.status, 'temporary_unavailable', '未超过观察窗口时账号应继续恢复')
  assert.equal(freshAfterRetest?.lastErrorCode, 'insufficient_quota', '后台复测应把上游真实错误码写入账户状态')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '后台复测状态原因应保留真实上游错误摘要')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /traceId trace-cooldown-retest-quota/, '后台复测状态原因应写入本地 traceId 作为追踪主键')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /request id: upstream-request-id-should-display/, '后台复测状态原因应保留上游 request id')

  const restored = repositories.clearAccountFailureState(freshAccount.id, access)
  assert.equal(restored?.cooldownRetestObservationStartedAt, undefined, '恢复正常时应清理自动恢复观察起点')

  const disabledCleanupAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '停用清理过期失败原因回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-disable-clear-expired-error',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id
  }, access)
  repositories.markAccountTemporaryUnavailable(disabledCleanupAccount.id, '过期冷却错误')
  const disabledCleanup = repositories.updateAccount(disabledCleanupAccount.id, { status: 'disabled' }, access)
  assert.equal(disabledCleanup?.status, 'disabled', '冷却账号应允许手动停用')
  assert.equal(disabledCleanup?.lastErrorCode, undefined, '手动停用应清理既有错误码')
  assert.equal(disabledCleanup?.lastErrorMessage, undefined, '手动停用应清理既有失败原因，避免停用状态展示过期冷却错误')
  assert.equal(disabledCleanup?.cooldownUntil, undefined, '手动停用应清理既有冷却结束时间')

  const rateLimitedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '限流后台复测回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-rate-limited',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id
  }, access)
  assert(repositories.setAccountGroup(rateLimitedAccount.id, group.id, access), '限流复测账号应能绑定分组')
  const limited = repositories.markAccountCooldown(rateLimitedAccount.id, new Date(Date.now() - 1000).toISOString(), '模拟限流', 'rate_limited')
  assert.equal(limited?.status, 'rate_limited', '限流状态应进入同一自动恢复通道')
  assert.ok(limited?.cooldownRetestObservationStartedAt, '进入限流时应记录自动恢复观察起点')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), rateLimitedAccount.id)
  const dueIds = repositories.listAccountsDueForCooldownRetest(20).map((item) => item.id)
  assert(dueIds.includes(rateLimitedAccount.id), '限流到期账号应进入后台复测候选')
  const limitedStillRecovering = repositories.recordCooldownAccountRetestFailure(rateLimitedAccount.id, {
    statusCode: 429,
    errorMessage: '仍然限流',
    maxRecoveryHours: 1,
    maxPauseMinutes: 10
  })
  assert.equal(limitedStillRecovering.action, 'retry_immediately', '限流首次复测失败应走快速恢复通道')
  assert.equal(repositories.findAccountSummary(rateLimitedAccount.id, access)?.status, 'rate_limited', '限流复测失败后应保持限流状态等待下次自动恢复')

  const probeAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '后台探针通过恢复回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-probe-success',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: group.id
  }, access)
  repositories.markAccountTemporaryUnavailable(probeAccount.id, '模拟后台探针恢复前失败态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), probeAccount.id)
  const dueProbeAccount = repositories.findAccountSummary(probeAccount.id, access)
  assert.equal(dueProbeAccount?.status, 'temporary_unavailable', '后台探针恢复前账号应为临时不可调用')
  assert(dueProbeAccount?.cooldownUntil, '后台探针恢复前应有冷却时间')
  assert(cooldownRetestService.enqueueCooldownAccountRetest(dueProbeAccount, {
    maxPauseMinutes: 10,
    maxRecoveryHours: 1,
    longTermIntervalHours: 24
  }), '后台探针恢复账号应能入队')
  const restoredByProbe = await waitForAccountStatus(probeAccount.id, 'active')
  assert(restoredByProbe, '后台探针测试通过后应能读取恢复后的账号')
  assert.equal(restoredByProbe.schedulable, true, '后台探针测试通过后应恢复调度')
  assert.equal(restoredByProbe.cooldownUntil, undefined, '后台探针测试通过后应清理冷却时间')
  assert.equal(restoredByProbe.lastErrorMessage, undefined, '后台探针测试通过后应清理错误原因')

  const owner = repositories.createSystemAccount({
    username: 'cooldown_auth_owner',
    displayName: '冷却复测授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'cooldown_auth_grantee',
    displayName: '冷却复测被授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: '冷却复测授权来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '冷却复测授权目标分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const sourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测授权来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-authorized',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: ownerGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '冷却复测授权实例恢复回归'
  }, ownerAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccount.id)
  assert(authorizedInstance, '授权后应创建被授权方本地账号实例')
  const authorizedTestAccount = repositories.findAccountForTest(authorizedInstance.id, granteeAccess)
  assert.equal(authorizedTestAccount?.accessType, 'authorized', '被授权方测试对象应保持授权视角')
  assert.equal(authorizedTestAccount?.schedulable, true, '授权实例初始应可调度')
  const authorizedCooled = repositories.markAccountTestTemporaryUnavailable(authorizedTestAccount, '模拟授权实例临时不可调用', granteeAccess)
  assert.equal(authorizedCooled?.status, 'temporary_unavailable', '授权实例应进入本地临时不可调用状态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), authorizedInstance.id)
  const authorizedRetestFailure = repositories.recordCooldownAccountRetestFailure(authorizedInstance.id, {
    statusCode: 503,
    errorMessage: '授权实例仍然不可用',
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  assert.equal(authorizedRetestFailure.failureCount, 1, '授权实例后台复测失败应按本地实例累计失败次数')
  assert.equal(authorizedRetestFailure.action, 'retry_immediately', '授权实例首次后台复测失败应继续快速恢复')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), authorizedInstance.id)
  const authorizedCandidate = repositories.listAccountsDueForCooldownRetest(20)
    .find((item) => item.id === authorizedInstance.id)
  assert(authorizedCandidate, '授权实例冷却到期后应进入后台复测候选')
  assert.equal(authorizedCandidate.accessType, 'authorized', '后台复测候选应保留授权实例视角，不能伪装成普通账户')
  assert.equal(authorizedCandidate.schedulable, true, '后台复测候选应读取本地实例原始可恢复调度状态')
  assert.equal(authorizedCandidate.cooldownRetestFailureCount, 1, '后台复测候选应读取授权实例本地失败次数')
  assert.equal(authorizedCandidate.bindingSystemAccountId, grantee.id, '后台复测候选应保留被授权方本地绑定系统账户')
  assert.equal(authorizedCandidate.boundGroupId, granteeGroup.id, '后台复测候选应保留被授权方本地分组绑定')
  assert(authorizedCandidate.accountAuthorizationId, '后台复测候选应保留账号授权 ID')
  const authorizedProbeHitBefore = mockOpenAIResponseHitCount
  assert(cooldownRetestService.enqueueCooldownAccountRetest(authorizedCandidate, {
    maxPauseMinutes: 10,
    maxRecoveryHours: 1,
    longTermIntervalHours: 24
  }), '授权实例冷却复测候选应能入队')
  const restoredAuthorized = await waitForAccountStatus(authorizedInstance.id, 'active', granteeAccess)
  assert.equal(restoredAuthorized.status, 'active', '后台探针测试通过后授权实例应恢复为正常')
  assert.equal(restoredAuthorized.schedulable, true, '后台探针测试通过后授权实例应恢复调度')
  assert.equal(restoredAuthorized.cooldownUntil, undefined, '后台探针测试通过后授权实例应清理冷却时间')
  assert.equal(mockOpenAIResponseHitCount, authorizedProbeHitBefore + 1, '授权实例后台复测应真实调用上游探针')
  const sourceAfterAuthorizedRetest = repositories.findAccountSummary(sourceAccount.id, ownerAccess)
  assert.equal(sourceAfterAuthorizedRetest?.status, 'active', '授权实例恢复不应修改授权方原账户状态')

  const quotaOwner = repositories.createSystemAccount({
    username: 'cooldown_quota_auth_owner',
    displayName: '冷却复测额度授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const quotaGrantee = repositories.createSystemAccount({
    username: 'cooldown_quota_auth_grantee',
    displayName: '冷却复测额度被授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const quotaOwnerAccess = { systemAccountId: quotaOwner.id, role: 'user' as const }
  const quotaGranteeAccess = { systemAccountId: quotaGrantee.id, role: 'user' as const }
  const quotaOwnerGroup = repositories.createGroup({
    name: '冷却复测额度来源分组',
    providerCode: 'gpt'
  }, quotaOwnerAccess)
  const quotaGranteeGroup = repositories.createGroup({
    name: '冷却复测额度目标分组',
    providerCode: 'gpt'
  }, quotaGranteeAccess)
  const quotaSourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测额度来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-quota-limited',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: quotaOwnerGroup.id
  }, quotaOwnerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: quotaSourceAccount.id,
    granteeType: 'system_account',
    granteeId: quotaGrantee.id,
    targetGroupId: quotaGranteeGroup.id,
    remark: '冷却复测额度耗尽授权实例回归',
    limits: { total: { enabled: true, limit: 1 } }
  }, quotaOwnerAccess)
  const quotaLimitedAuthorization = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(quotaSourceAccount.id, quotaGrantee.id) as { id?: string } | undefined
  assert(quotaLimitedAuthorization?.id, '额度授权应写入运行时授权记录')
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, total_cost_usd, updated_at)
      VALUES (?, 'account_authorization', ?, 1, 1, ?)
    `)
    .run(quotaGrantee.id, quotaLimitedAuthorization.id, new Date().toISOString())
  const quotaLimitedInstance = repositories.listAccounts(quotaGranteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === quotaSourceAccount.id)
  assert(quotaLimitedInstance, '额度授权实例应创建本地账号实例')
  const quotaLimitedTestAccount = repositories.findAccountForTest(quotaLimitedInstance.id, quotaGranteeAccess)
  assert(quotaLimitedTestAccount, '额度授权实例应能读取测试对象')
  repositories.markAccountTestTemporaryUnavailable(quotaLimitedTestAccount, '模拟额度耗尽授权实例临时不可调用', quotaGranteeAccess)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 10_000).toISOString(), quotaLimitedInstance.id)
  const scanWindowOwnerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测扫描窗口普通账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-scan-window',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: group.id
  }, access)
  repositories.markAccountTemporaryUnavailable(scanWindowOwnerAccount.id, '模拟扫描窗口普通账户临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), scanWindowOwnerAccount.id)
  const scanWindowCandidates = repositories.listAccountsDueForCooldownRetest(1)
  assert(!scanWindowCandidates.some((item) => item.id === quotaLimitedInstance.id), '授权额度耗尽的授权实例不应进入后台复测候选')
  assert(scanWindowCandidates.some((item) => item.id === scanWindowOwnerAccount.id), '无效授权实例不应占满扫描窗口导致后续普通候选被挡住')

  console.log('cooldown retest recovery regression passed')
} finally {
  await closeServer(mockOpenAIServer)
  restoreWorkerParentIpc()
  await closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url?.split('?', 1)[0]
    if (req.method !== 'POST' || (requestPath !== '/v1/responses' && requestPath !== '/v1/chat/completions')) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    req.on('end', () => {
      mockOpenAIResponseHitCount += 1
      if (requestPath === '/v1/chat/completions') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'chatcmpl_cooldown_retest_probe_success',
          object: 'chat.completion',
          choices: [{
            index: 0,
            message: { role: 'assistant', content: 'OK' },
            finish_reason: 'stop'
          }],
          usage: {
            prompt_tokens: 1,
            completion_tokens: 1,
            total_tokens: 2
          }
        }))
        return
      }
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_cooldown_retest_probe_success',
          object: 'response',
          status: 'completed',
          output: [
            {
              type: 'message',
              content: [{ type: 'output_text', text: 'OK' }]
            }
          ],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2
          }
        }
      }
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
    req.resume()
  })
}

async function waitForAccountStatus(
  accountId: string,
  status: string,
  accountAccess: AccessScope = access
): Promise<NonNullable<ReturnType<typeof repositories.findAccountSummary>>> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    const account = repositories.findAccountSummary(accountId, accountAccess)
    if (account?.status === status) {
      return account
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
  }
  throw new Error(`等待账号 ${accountId} 恢复为 ${status} 超时`)
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
    server.closeIdleConnections?.()
  })
}
