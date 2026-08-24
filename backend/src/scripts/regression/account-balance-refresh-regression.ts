import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { passiveScheduleJitterWindowMs } from '../../shared/passive-schedule-jitter.js'
import { UpstreamRequestTimeoutError } from '../../modules/gateway/upstream/request.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-balance-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-balance-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, balanceRepository, balanceQueryService, balanceRefreshJob, autoDetectService, workerSchedulerModule] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-balance.repository.js'),
  import('../../modules/accounts/account-balance-query.service.js'),
  import('../../modules/background/account-balance-refresh.job.js'),
  import('../../modules/background/account-balance-auto-detect.service.js'),
  import('../../modules/background/worker-scheduler.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

const balanceServiceSource = readFileSync(resolve('src/modules/accounts/account-balance-query.service.ts'), 'utf8')
const balanceRoutesSource = readFileSync(resolve('src/modules/accounts/account-balance.routes.ts'), 'utf8')
const balanceRepositorySource = readFileSync(resolve('src/storage/account-balance.repository.ts'), 'utf8')
const accountRoutesSource = readFileSync(resolve('src/modules/accounts/accounts.routes.ts'), 'utf8')
const repositoriesSource = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')
const balanceRefreshJobSource = readFileSync(resolve('src/modules/background/account-balance-refresh.job.ts'), 'utf8')
const autoDetectServiceSource = readFileSync(resolve('src/modules/background/account-balance-auto-detect.service.ts'), 'utf8')
const backgroundJobsSource = readFileSync(resolve('src/modules/background/background-jobs.ts'), 'utf8')
const accountHealthProjectionSource = readFileSync(resolve('src/storage/account-health-projection.repository.ts'), 'utf8')
assert.doesNotMatch(
  balanceServiceSource,
  /response\.status\s*===\s*(?:401|403|408|429)|status\s*>=\s*500|HTTP \(\?:401\|403\)/,
  '余额查询不得按上游 HTTP 状态码推断鉴权、限流或账户能力语义'
)
assert.match(balanceServiceSource, /const balanceRefreshLeaseMs = 30_000/)
assert.match(
  balanceServiceSource,
  /const requestTimeoutMs = 15_000/,
  '余额查询的全部内置适配器应共享 15 秒总 deadline'
)
assert.match(balanceRoutesSource, /post\('\/balance\/test-draft'/, '新增和编辑表单必须使用独立草稿余额测试接口')
assert.match(balanceRoutesSource, /prepareAccountDraftTestSnapshotAsync/, '草稿余额测试必须使用当前表单账户快照')
assert.match(balanceRoutesSource, /testAccountBalanceCandidate/, '草稿余额测试必须调用无持久化查询入口')
  assert.match(balanceRoutesSource, /refreshAccountBalanceCandidateWithOutcome\(candidate, \{ mode: 'manual' \}\)/, '列表人工刷新必须使用可验证持久化结果的 manual 模式')
  assert.match(balanceRoutesSource, /if \(!result\.persisted\)[\s\S]*res\.status\(409\)/, '人工刷新未落盘时必须返回冲突，不能冒充刷新成功')
assert.match(balanceRoutesSource, /findAccountBalanceManualRefreshCandidateAsync/, '列表人工刷新必须使用不受账户运行状态限制的候选入口')
assert.ok(!balanceRoutesSource.includes('saveAccountBalanceConfigurationAsync'), '余额路由不能在查询时保存账户配置')
assert.ok(!balanceRoutesSource.includes('delete_account_balance_snapshot'), '余额测试不能删除或替换已保存快照')
assert.match(balanceRepositorySource, /balance_query_config_json::jsonb = \?::jsonb/, 'PostgreSQL 偏好条件更新必须按 JSON 语义比较配置')
assert.match(balanceRepositorySource, /persistAccountBalanceRefreshWithSnapshotAsync[\s\S]+client\.transaction/, 'PostgreSQL 余额配置与统计快照必须在同一事务原子提交')
assert.match(balanceServiceSource, /runtimeConfig\.databaseDriver === 'postgres'[\s\S]+persistAccountBalanceRefreshWithSnapshotAsync/, 'PostgreSQL 余额刷新必须使用跨 schema 原子提交入口')
assert.ok(!accountRoutesSource.includes('saveAccountBalanceConfigurationAsync'), '账户路由不应在账户保存后进行第二次余额配置写入')
assert.match(repositoriesSource, /balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at/)
  const accountManagementPatchSource = readFileSync(resolve('src/storage/account-management-patch.repository.ts'), 'utf8')
  assert.match(accountManagementPatchSource, /balanceDecision\.autoDisabledForMultipleApiKeys/, '账户更新必须接受集中写入层的多 Key 自动关闭决策')
  assert.match(accountManagementPatchSource, /balanceIdentityChanged = !isDeepStrictEqual/, '账户保存必须按余额查询身份变化决定是否清理旧快照')
  assert.match(accountRoutesSource, /if \(account\.balanceIdentityChanged\) \{[\s\S]*cleanupAccountBalanceSnapshotAfterSave/, '只有真实余额身份变化才允许清理旧快照')
assert.match(balanceRepositorySource, /listAccountsNeedingBalanceRefreshRecoveryAsync/, '余额 worker 必须能自愈活动账户缺快照且无刷新计划的状态')
assert.match(balanceRepositorySource, /postgresBalanceRecoveryAfterId/, 'PostgreSQL 自愈候选也必须使用轮转游标，不能固定扫描最小 ID 前缀')
assert.match(balanceRepositorySource, /listAccountsDueForBalanceAutoDetectionAsync/, '首次余额自动探测必须有持久化到期补偿扫描')
assert.match(balanceRepositorySource, /status = 'active'[\s\S]*schedulable = 1/, '自动余额任务只允许领取活动且可调度的账户')
assert.match(balanceRepositorySource, /function balanceBooleanLiteral\(value: boolean\): '1' \| '0'[\s\S]*return value \? '1' : '0'/, 'Node PostgreSQL 余额字段保留 INTEGER 0/1，谓词必须使用数值字面量')
assert.match(balanceRepositorySource, /listAccountsDueForBalanceRefreshAsync[\s\S]*schedulable = 1[\s\S]*balance_query_enabled = 1/, 'PostgreSQL 自动刷新候选必须匹配 Node INTEGER partial index')
assert.match(balanceRepositorySource, /saveAccountBalanceConfigurationAsync[\s\S]*?\[input\.enabled \? 1 : 0, JSON\.stringify\(config \?\? \{\}\)/, 'PostgreSQL 余额开关写入必须绑定 INTEGER 0/1 参数')
assert.match(balanceRepositorySource, /balanceDetectionCandidateWhere[\s\S]*balanceBooleanPredicate\('schedulable', true\)[\s\S]*balanceBooleanPredicate\('balance_query_enabled', false\)/, '首次探测候选及写回必须按当前方言核对可调度和关闭状态')
assert.match(accountHealthProjectionSource, /case 'activation_success':[\s\S]*set\('schedulable', 1\)[\s\S]*balance_query_next_refresh_at/, 'J1 activation projector 必须在同一业务事务写入首次余额探测意图')
assert.match(balanceRefreshJobSource, /const refreshBatchSize = runtimeConfig\.background\.accountBalanceRefreshBatchSize/, '余额刷新单轮候选批次必须来自环境配置')
assert.match(balanceRefreshJobSource, /const refreshConcurrency = runtimeConfig\.concurrency\.globalMax/, '余额刷新必须使用全局共享并发池')
assert.match(balanceRefreshJobSource, /runWithGlobalBackgroundConcurrencySlot/, '余额刷新候选必须获取全局共享槽')
assert.match(balanceRefreshJobSource, /const recoveryBatchSize = runtimeConfig\.background\.accountBalanceRefreshRecoveryBatchSize/, '余额刷新自愈批次必须来自环境配置')
assert.match(balanceRefreshJobSource, /const refreshRunBudgetMs = 45_000/, '余额刷新领取新候选必须受单轮运行预算约束')
assert.doesNotMatch(balanceRefreshJobSource, /Promise\.race/, '余额刷新不得用 detached Promise.race 伪造取消')
assert.match(balanceRefreshJobSource, /signal: candidateController\.signal/, '候选超时必须传递到真实余额查询')
assert.match(balanceRefreshJobSource, /deadlineAtMs: candidateDeadlineAtMs/, '候选必须传递绝对截止时间')
assert.match(balanceRefreshJobSource, /diagnosticCount/, '候选级上游诊断必须汇总到完成摘要')
assert.match(balanceRefreshJobSource, /unfinishedCount/, '未完成候选必须汇总到完成摘要')
assert.doesNotMatch(balanceRefreshJobSource, /\.catch\(\(\) => false\)/, '运行态延期的本地持久化异常不得被吞掉')
assert.match(backgroundJobsSource, /task: \([^)]*\) => runAccountBalanceRefresh\([^)]*\)/, '余额定时任务必须把结构化执行结果返回给 WorkerScheduler')
assert.match(backgroundJobsSource, /account-balance-auto-detect-recovery/, 'ops-worker 必须注册余额自动探测补偿任务')
assert.match(autoDetectServiceSource, /runAccountBalanceAutoDetectionRecovery/, '自动探测服务必须提供重启后的持久补偿入口')
assert.match(autoDetectServiceSource, /runWithAccountBalanceLease/, '自动探测必须复用余额查询的共享账户租约')
assert.match(balanceServiceSource, /export async function runWithAccountBalanceLease/, '余额查询服务必须提供共享账户租约入口')
assert.match(balanceServiceSource, /loadCurrentGenerationBalanceSnapshot\(candidate\)/, '租约冲突与瞬时失败只能复用当前刷新代次的余额快照')
assert.doesNotMatch(balanceServiceSource, /loadAccountBalanceSnapshotsByAccountIdsAsync/, '余额刷新 fallback 不能绕过刷新代次直接读取快照金额')
assert.doesNotMatch(balanceRepositorySource, /expectedUpdatedAt|stateUpdatedAt|AND updated_at = \?/, '余额刷新不得把普通账户活动时间作为 CAS 条件')

let untrustedStatusServer: Server | undefined
try {
  const mockState = {
    status: 401,
    requestCount: 0,
    invalidJson: false,
    recoverWithNewApi: false,
    hang: false,
    resetConnection: false,
    resetResponseBody: false,
    interruptedResponseBodyCount: 0
  }
  let markHungRequestStarted: (() => void) | undefined
  const hungRequestStarted = new Promise<void>((resolve) => { markHungRequestStarted = resolve })
  let markHungResponseClosed: (() => void) | undefined
  const hungResponseClosed = new Promise<void>((resolve) => { markHungResponseClosed = resolve })
  untrustedStatusServer = createServer((request, response) => {
    mockState.requestCount += 1
    if (mockState.hang) {
      markHungRequestStarted?.()
      response.once('close', () => markHungResponseClosed?.())
      return
    }
    if (mockState.resetConnection) {
      request.socket.destroy()
      return
    }
    if (mockState.resetResponseBody) {
      response.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      response.write('{"data":')
      mockState.interruptedResponseBodyCount += 1
      setTimeout(() => request.socket.destroy(), 5)
      return
    }
    const requestPath = request.url?.split('?', 1)[0]
    if (mockState.recoverWithNewApi && requestPath === '/api/usage/token/') {
      response.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      response.end(JSON.stringify({ data: { total_available: 3_655_000 } }))
      return
    }
    if (mockState.recoverWithNewApi && requestPath === '/api/status') {
      response.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      response.end(JSON.stringify({ data: { quota_per_unit: 500_000 } }))
      return
    }
    response.writeHead(mockState.status, { 'content-type': 'application/json; charset=utf-8' })
    response.end(mockState.invalidJson ? '{not-json' : JSON.stringify({ error: { code: 'provider_defined', message: 'opaque upstream response' } }))
  })
  await new Promise<void>((resolveListen, rejectListen) => {
    untrustedStatusServer?.once('error', rejectListen)
    untrustedStatusServer?.listen(0, '127.0.0.1', resolveListen)
  })
  const mockAddress = untrustedStatusServer.address() as AddressInfo
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}/v1`
  const group = repositories.createGroup({ name: '余额回归分组', providerCode: 'gpt', enabled: true }, access)
  const create = (name: string, type = 'api_key', credentials: Record<string, unknown> = { api_key: `sk-${name}`, base_url: 'https://relay.example/v1' }) =>
    repositories.createAccount({
      providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name, type, credentials, supportedModels: ['gpt-5.5'], groupId: group.id
    }, access)
  const dueA = create('due-a')
  const dueB = create('due-b')
  const disabled = create('disabled')
  const oauth = create('oauth', 'oauth', { access_token: 'oauth-token', refresh_token: 'refresh-token', account_id: 'oauth-balance-regression', base_url: 'https://relay.example/v1' })
  const multi = create('multi', 'api_key', { api_keys: ['sk-a', 'sk-b'], api_key: 'sk-a', base_url: 'https://relay.example/v1' })
  const future = create('future')
  const autoDetect = create('auto-detect')
  const directActiveAutoDetect = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'direct-active-auto-detect', type: 'api_key',
    credentials: { api_key: 'sk-direct-active-auto-detect', base_url: 'https://relay.example/v1' },
    supportedModels: ['gpt-5.5'], groupId: group.id,
    status: 'active', skipInitialHealthCheck: true
  }, access)
  const durableAutoDetect = create('durable-auto-detect')
  const retryAutoDetect = create('retry-auto-detect')
  const unsupportedAutoDetect = create('unsupported-auto-detect')
  const stateTransitionAutoDetect = create('state-transition-auto-detect')
  const leaseAutoDetect = create('lease-auto-detect')
  const transientFailure = create('transient-failure')
  const deterministicFailure = create('deterministic-failure')
  const untrustedStatusFailure = create('untrusted-status-failure', 'api_key', {
    api_key: 'sk-untrusted-status-failure',
    base_url: mockBaseUrl
  })
  const manualRefresh = create('manual-refresh')
  const gatewayActivity = create('gateway-activity')
  const automaticEligibilityRevoked = create('automatic-eligibility-revoked')
  const lifecycle = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'lifecycle', type: 'api_key', credentials: { api_key: 'sk-lifecycle', base_url: 'https://relay.example/v1' },
    supportedModels: ['gpt-5.5'], groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
  }, access)
  const recoverMissing = create('recover-missing')
  const recoverPaused = create('recover-paused')
  const recoverInactive = create('recover-inactive')
  const configured = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'configured', type: 'api_key', credentials: { api_key: 'sk-configured', base_url: 'https://relay.example/v1' },
    supportedModels: ['gpt-5.5'], groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  const persistenceFailure = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'persistence-failure', type: 'api_key', credentials: { api_key: 'sk-persistence-failure', base_url: 'https://relay.example/v1' },
    supportedModels: ['gpt-5.5'], groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
  }, access)
  const database = databaseModule.getBusinessDatabase()

  const activateFirstBalanceDetection = (account: { id: string; configRevision?: number }) => {
    const checkedAt = new Date(Date.now() - 1_000).toISOString()
    database.prepare(`
      UPDATE accounts
      SET status = 'pending_test', schedulable = 0, balance_query_enabled = 0,
          balance_query_config_json = '{}', balance_query_next_refresh_at = NULL
      WHERE id = ?
    `).run(account.id)
    assert.equal(repositories.projectAccountHealthFixtureSuccess(account.id, {
      intervalHours: 1,
      jitterMinutes: 0,
      failureThreshold: 3,
      checkedAt,
      expectedConfigRevision: account.configRevision,
      scheduleBalanceAutoDetection: true
    }), true, '首次健康检查成功必须原子写入余额自动探测意图')
    const row = database.prepare(`
      SELECT status, schedulable, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
      FROM accounts WHERE id = ?
    `).get(account.id) as Record<string, unknown>
    assert.equal(row.status, 'active')
    assert.equal(row.schedulable, 1)
    assert.equal(row.balance_query_enabled, 0)
    assert.equal(row.balance_query_config_json, '{}')
    assert.equal(row.balance_query_next_refresh_at, checkedAt)
    return checkedAt
  }

  const directActiveDetectionAt = new Date(Date.now() - 1_000).toISOString()
  database.prepare(`UPDATE accounts SET status = 'pending_test', schedulable = 0 WHERE id = ?`).run(directActiveAutoDetect.id)
  assert.equal(repositories.projectAccountHealthFixtureSuccess(directActiveAutoDetect.id, {
    intervalHours: 1,
    jitterMinutes: 0,
    failureThreshold: 3,
    checkedAt: directActiveDetectionAt,
    expectedConfigRevision: directActiveAutoDetect.configRevision,
    scheduleBalanceAutoDetection: true
  }), true, '直接启用的新单 Key 账户首次健康检查成功也必须写入余额自动探测意图')
  const directActiveDetectionRow = database.prepare(`
    SELECT status, schedulable, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
    FROM accounts WHERE id = ?
  `).get(directActiveAutoDetect.id) as Record<string, unknown>
  assert.equal(directActiveDetectionRow.status, 'active')
  assert.equal(directActiveDetectionRow.schedulable, 1)
  assert.equal(directActiveDetectionRow.balance_query_enabled, 0)
  assert.equal(directActiveDetectionRow.balance_query_config_json, '{}')
  assert.equal(directActiveDetectionRow.balance_query_next_refresh_at, directActiveDetectionAt)

  const durableDetectionAt = activateFirstBalanceDetection(durableAutoDetect)
  const durableCandidate = (await balanceRepository.listAccountsDueForBalanceAutoDetectionAsync({
    now: durableDetectionAt,
    limit: 10
  })).find((candidate: { id: string }) => candidate.id === durableAutoDetect.id)
  assert.ok(durableCandidate, '重启补偿扫描必须领取已经持久化的首次探测意图')
  const durableRecovery = await autoDetectService.runAccountBalanceAutoDetectionRecovery({
    listCandidates: async () => [durableCandidate],
    autoDetect: async (candidate) => await autoDetectService.autoDetectAccountBalanceCandidate(candidate, {
      queryBuiltin: async () => ({
        adapter: 'sub2api',
        snapshot: { status: 'fresh', remainingUsd: '15.750000', basis: 'wallet' }
      })
    })
  })
  assert.equal(durableRecovery.outcome, 'success')
  assert.equal(durableRecovery.enabledCount, 1, '恢复任务必须自动开启严格命中的余额接口')
  const durableAfter = database.prepare(`
    SELECT balance_query_enabled, balance_query_config_json FROM accounts WHERE id = ?
  `).get(durableAutoDetect.id) as Record<string, unknown>
  assert.equal(durableAfter.balance_query_enabled, 1)
  assert.deepEqual(JSON.parse(String(durableAfter.balance_query_config_json)), {
    adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api'
  })
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([durableAutoDetect.id]).get(durableAutoDetect.id)?.remainingUsd,
    '15.750000',
    '恢复探测写入后列表快照必须能回显新余额'
  )

  const retryDetectionAt = activateFirstBalanceDetection(retryAutoDetect)
  const retryCandidate = (await balanceRepository.listAccountsDueForBalanceAutoDetectionAsync({
    now: retryDetectionAt,
    limit: 10
  })).find((candidate: { id: string }) => candidate.id === retryAutoDetect.id)
  assert.ok(retryCandidate)
  assert.equal(await autoDetectService.autoDetectAccountBalanceCandidate(retryCandidate, {
    queryBuiltin: async () => { throw new Error('temporary upstream outage') }
  }), 'retry', '临时上游失败不能误判为不支持或丢弃探测意图')
  const retryAfter = database.prepare(`
    SELECT balance_query_enabled, balance_query_next_refresh_at FROM accounts WHERE id = ?
  `).get(retryAutoDetect.id) as Record<string, unknown>
  assert.equal(retryAfter.balance_query_enabled, 0)
  assert.ok(Date.parse(String(retryAfter.balance_query_next_refresh_at)) > Date.now(), '临时失败必须把持久探测意图延后重试')

  const unsupportedDetectionAt = activateFirstBalanceDetection(unsupportedAutoDetect)
  const unsupportedCandidate = (await balanceRepository.listAccountsDueForBalanceAutoDetectionAsync({
    now: unsupportedDetectionAt,
    limit: 10
  })).find((candidate: { id: string }) => candidate.id === unsupportedAutoDetect.id)
  assert.ok(unsupportedCandidate)
  assert.equal(await autoDetectService.autoDetectAccountBalanceCandidate(unsupportedCandidate, {
    queryBuiltin: async () => ({ adapter: 'sub2api', snapshot: { status: 'unsupported', errorMessage: 'not supported' } })
  }), 'unsupported', '确定不支持必须完成首次探测而不是无限重试')
  assert.equal(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(unsupportedAutoDetect.id)?.balance_query_next_refresh_at,
    null,
    '确定不支持必须清除首次探测意图'
  )

  const stateTransitionDetectionAt = activateFirstBalanceDetection(stateTransitionAutoDetect)
  const stateTransitionCandidate = await balanceRepository.findAccountBalanceDetectionCandidateAsync(
    stateTransitionAutoDetect.id,
    stateTransitionAutoDetect.configRevision ?? 1
  )
  assert.ok(stateTransitionCandidate)
  let markStateTransitionQueryStarted: (() => void) | undefined
  const stateTransitionQueryStarted = new Promise<void>((resolve) => {
    markStateTransitionQueryStarted = resolve
  })
  let releaseStateTransitionQuery: (() => void) | undefined
  const stateTransitionQueryGate = new Promise<void>((resolve) => {
    releaseStateTransitionQuery = resolve
  })
  const stateTransitionResult = autoDetectService.autoDetectAccountBalanceCandidate(stateTransitionCandidate, {
    queryBuiltin: async () => {
      markStateTransitionQueryStarted?.()
      await stateTransitionQueryGate
      return { adapter: 'sub2api', snapshot: { status: 'unsupported', errorMessage: 'not supported' } }
    }
  })
  await stateTransitionQueryStarted
  const stateTransitionRevisionBefore = database.prepare(`SELECT config_revision FROM accounts WHERE id = ?`)
    .get(stateTransitionAutoDetect.id)?.config_revision
  database.prepare(`UPDATE accounts SET status = 'disabled', schedulable = 0 WHERE id = ?`).run(stateTransitionAutoDetect.id)
  assert.equal(
    database.prepare(`SELECT config_revision FROM accounts WHERE id = ?`).get(stateTransitionAutoDetect.id)?.config_revision,
    stateTransitionRevisionBefore,
    '可用性状态切换可以不改变余额配置版本'
  )
  releaseStateTransitionQuery?.()
  assert.equal(
    await stateTransitionResult,
    'stale',
    '查询在途期间变为不可调度后，不支持结果不得清除首次探测意图'
  )
  const stateTransitionAfter = database.prepare(`
    SELECT status, schedulable, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
    FROM accounts WHERE id = ?
  `).get(stateTransitionAutoDetect.id) as Record<string, unknown>
  assert.equal(stateTransitionAfter.status, 'disabled')
  assert.equal(stateTransitionAfter.schedulable, 0)
  assert.equal(stateTransitionAfter.balance_query_enabled, 0)
  assert.equal(stateTransitionAfter.balance_query_config_json, '{}')
  assert.equal(
    stateTransitionAfter.balance_query_next_refresh_at,
    stateTransitionDetectionAt,
    '失效写回必须保留原始持久探测意图，等待账户重新可调度'
  )

  const leaseDetectionAt = activateFirstBalanceDetection(leaseAutoDetect)
  const leaseCandidate = await balanceRepository.findAccountBalanceDetectionCandidateAsync(
    leaseAutoDetect.id,
    leaseAutoDetect.configRevision ?? 1
  )
  assert.ok(leaseCandidate)
  let autoDetectUpstreamRequestCount = 0
  let markLeaseAutoDetectQueryStarted: (() => void) | undefined
  const leaseAutoDetectQueryStarted = new Promise<void>((resolve) => {
    markLeaseAutoDetectQueryStarted = resolve
  })
  let releaseLeaseAutoDetectQuery: (() => void) | undefined
  const leaseAutoDetectQueryGate = new Promise<void>((resolve) => {
    releaseLeaseAutoDetectQuery = resolve
  })
  const firstLeaseAutoDetect = autoDetectService.autoDetectAccountBalanceCandidate(leaseCandidate, {
    queryBuiltin: async () => {
      autoDetectUpstreamRequestCount += 1
      markLeaseAutoDetectQueryStarted?.()
      await leaseAutoDetectQueryGate
      return { adapter: 'sub2api', snapshot: { status: 'unsupported', errorMessage: 'not supported' } }
    }
  })
  await leaseAutoDetectQueryStarted
  const leaseBusyAutoDetect = await autoDetectService.autoDetectAccountBalanceCandidate(leaseCandidate, {
    queryBuiltin: async () => {
      autoDetectUpstreamRequestCount += 1
      return { adapter: 'sub2api', snapshot: { status: 'unsupported', errorMessage: 'must not execute' } }
    }
  })
  assert.equal(leaseBusyAutoDetect, 'lease_busy', '同一账户已有余额查询租约时，第二次自动探测不得写回或访问上游')
  assert.equal(autoDetectUpstreamRequestCount, 1, '并发自动探测只允许一个上游余额请求')
  assert.equal(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(leaseAutoDetect.id)?.balance_query_next_refresh_at,
    leaseDetectionAt,
    '租约占用不能清除或延后持久探测意图'
  )
  releaseLeaseAutoDetectQuery?.()
  assert.equal(await firstLeaseAutoDetect, 'unsupported')

  databaseModule.getBusinessDatabase().prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(persistenceFailure.id)
  const persistenceFailureCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(persistenceFailure.id)
  assert.ok(persistenceFailureCandidate)
  await assert.rejects(
    balanceQueryService.refreshAccountBalanceCandidate({
      ...persistenceFailureCandidate,
      proxyProfileId: 'proxy_db_failure'
    }, {
      resolveProxyUrl: async () => { throw new Error('代理配置 DB 读取失败') }
    }),
    /代理配置 DB 读取失败/,
    '代理配置 repository/DB 失败必须冒泡，不能归一成账户余额 transient 失败'
  )
  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase.exec(`
    CREATE TRIGGER reject_balance_snapshot_insert
    BEFORE INSERT ON account_usage_snapshots
    WHEN NEW.account_id = '${persistenceFailure.id}'
    BEGIN
      SELECT RAISE(ABORT, 'stats snapshot write failed');
    END;
  `)
  await assert.rejects(
    balanceQueryService.refreshAccountBalanceCandidate(persistenceFailureCandidate, {
      query: async () => ({ status: 'fresh', remainingUsd: '3.210000', rawRemaining: '3.21', rawUnit: 'usd', basis: 'wallet' })
    }),
    /stats snapshot write failed/,
    '余额配置或快照持久化失败必须冒泡，不能改写成账户级 transient 失败'
  )
  const persistenceRecoveryAt = database.prepare(`
    SELECT balance_query_next_refresh_at
    FROM accounts
    WHERE id = ?
  `).get(persistenceFailure.id)?.balance_query_next_refresh_at as string | undefined
  assert.ok(persistenceRecoveryAt && Date.parse(persistenceRecoveryAt) <= Date.now(), '快照写入失败后必须把余额刷新重新安排为立即可恢复')
  statsDatabase.exec('DROP TRIGGER reject_balance_snapshot_insert')

  mockState.hang = true
  const transportAbortController = new AbortController()
  const transportAbortRefresh = balanceQueryService.refreshAccountBalanceCandidateWithOutcome({
    id: untrustedStatusFailure.id,
    systemAccountId: 'sys_admin',
    configRevision: untrustedStatusFailure.configRevision ?? 1,
    credentials: { api_key: 'sk-untrusted-status-failure', base_url: mockBaseUrl },
    config: { adapter: 'builtin', intervalMinutes: 5 },
    nextRefreshAt: null,
  }, {
    signal: transportAbortController.signal,
    deadlineAtMs: Date.now() + 5_000
  })
  await hungRequestStarted
  transportAbortController.abort(new Error('候选超时'))
  const transportAbortResult = await transportAbortRefresh
  await hungResponseClosed
  assert.equal(transportAbortResult.outcome, 'stale', '真实上游请求取消后必须归类为 stale')
  assert.equal(transportAbortResult.persisted, false, '失效候选不得冒充已持久化')
  mockState.hang = false

  const timeoutRefresh = await balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [{
        id: dueA.id,
        systemAccountId: 'sys_admin',
        configRevision: dueA.configRevision ?? 1,
        credentials: { api_key: 'sk-due-a', base_url: 'https://relay.example/v1' },
        config: { adapter: 'builtin', intervalMinutes: 5 },
        nextRefreshAt: new Date().toISOString()
      }],
      refreshCandidate: async (_candidate, context) => await new Promise<never>((_resolve, reject) => {
        const abort = () => reject(context.signal.reason)
        if (context.signal.aborted) abort()
        else context.signal.addEventListener('abort', abort, { once: true })
      }),
      runBudgetMs: 20,
      candidateTimeoutMs: 10
    })
  assert.equal(timeoutRefresh.outcome, 'success', '单候选超时是账户级诊断，不得标记后台任务部分失败')
  assert.equal(timeoutRefresh.diagnosticCount, 1)
  assert.equal(timeoutRefresh.staleCount, 1)
  assert.equal(timeoutRefresh.processedCount, 1)

  const callableDueAt = new Date().toISOString()
  const runtimeCandidate = {
    id: dueA.id,
    systemAccountId: 'sys_admin',
    configRevision: dueA.configRevision ?? 1,
    credentials: { api_key: 'sk-due-a' },
    config: { adapter: 'builtin', intervalMinutes: 5 } as const,
    nextRefreshAt: callableDueAt
  }
  const executableRuntimeCases = [
    { name: 'runtime absent', available: true, values: {} },
    { name: 'normal', available: true, values: { [dueA.id]: { status: 'normal' as const } } },
    { name: 'degraded', available: true, values: { [dueA.id]: { status: 'degraded' as const } } },
    { name: 'runtime unavailable fail-open', available: false, values: {} }
  ]
  for (const runtimeCase of executableRuntimeCases) {
    const refreshedIds: string[] = []
    const summary = await balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [runtimeCandidate],
      loadRuntimeAvailability: async (runtimeKeys) => {
        assert.deepEqual(runtimeKeys, [dueA.id], `${runtimeCase.name} 必须按候选账户 ID 加载运行态`)
        return { available: runtimeCase.available, values: runtimeCase.values }
      },
      refreshCandidate: async (candidate) => {
        refreshedIds.push(candidate.id)
        return { outcome: 'refreshed', snapshot: { status: 'fresh' }, persisted: true }
      }
    })
    assert.deepEqual(refreshedIds, [dueA.id], `${runtimeCase.name} 账户必须执行自动余额查询`)
    assert.equal(summary.selectedCount, 1)
    assert.equal(summary.processedCount, 1)
    assert.equal(summary.deferredCount, 0)
  }
  for (const status of ['local_suppressed', 'precheck_pending', 'half_open', 'precheck_failed'] as const) {
    const refreshedIds: string[] = []
    const deferredIds: string[] = []
    const summary = await balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [runtimeCandidate],
      loadRuntimeAvailability: async () => ({ available: true, values: { [dueA.id]: { status } } }),
      refreshCandidate: async (candidate) => { refreshedIds.push(candidate.id) },
      deferCandidate: async (candidate) => {
        deferredIds.push(candidate.id)
        return true
      }
    })
    assert.deepEqual(refreshedIds, [], `${status} 账户必须延后且不得调用余额刷新`)
    assert.deepEqual(deferredIds, [dueA.id], `${status} 账户必须写入可恢复的下一次余额刷新计划`)
    assert.equal(summary.selectedCount, 1)
    assert.equal(summary.processedCount, 0)
    assert.equal(summary.deferredCount, 1)
    assert.equal(summary.outcome, 'success', `${status} 账户延后是可恢复调度，不得标记后台任务部分失败`)
  }

  await assert.rejects(
    balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [runtimeCandidate],
      refreshCandidate: async () => { throw new Error('DB service 写入失败') }
    }),
    /DB service 写入失败/,
    '候选刷新中的本地 DB 异常必须冒泡为后台任务失败'
  )
  let candidateTimeoutObserved = false
  await assert.rejects(
    balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [runtimeCandidate],
      refreshCandidate: async (_candidate, context) => await new Promise<never>((_resolve, reject) => {
        context.signal.addEventListener('abort', () => {
          candidateTimeoutObserved = true
          reject(new Error('候选截止后的 DB/IPC 异常'))
        }, { once: true })
      }),
      runBudgetMs: 20,
      candidateTimeoutMs: 5
    }),
    /候选截止后的 DB\/IPC 异常/,
    '候选截止后发生的 DB/IPC 异常不得伪装为 stale 诊断'
  )
  assert.equal(candidateTimeoutObserved, true, '候选超时信号必须已实际触发')
  await assert.rejects(
    balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [runtimeCandidate],
      loadRuntimeAvailability: async () => ({ available: true, values: { [dueA.id]: { status: 'local_suppressed' as const } } }),
      deferCandidate: async () => { throw new Error('IPC 延后持久化失败') }
    }),
    /IPC 延后持久化失败/,
    '运行态延期的本地 IPC 或持久化异常必须冒泡为后台任务失败'
  )

  const classifiedCandidates = ['refreshed', 'lease_busy', 'stale', 'failed', 'unsupported'] as const
  const classifiedSummary = await balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => classifiedCandidates.map((outcome, index) => ({
      ...runtimeCandidate,
      id: `classified-${index}-${outcome}`
    })),
    refreshCandidate: async (candidate) => ({
      outcome: candidate.id.split('-').slice(2).join('-'),
      snapshot: { status: 'fresh' },
      persisted: true
    })
  })
  assert.equal(classifiedSummary.outcome, 'success', '所有已返回的账户级结果都必须完成后台任务')
  assert.equal(classifiedSummary.refreshedCount, 1)
  assert.equal(classifiedSummary.leaseBusyCount, 1)
  assert.equal(classifiedSummary.staleCount, 1)
  assert.equal(classifiedSummary.failedCount, 1)
  assert.equal(classifiedSummary.unsupportedCount, 1)
  assert.equal(classifiedSummary.diagnosticCount, 3)
  assert.equal(classifiedSummary.unfinishedCount, 1)
  assert.equal(classifiedSummary.processedCount, 5)

  const leaseBusyOnlySummary = await balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    refreshCandidate: async () => ({
      outcome: 'lease_busy',
      snapshot: { status: 'pending' },
      persisted: false
    })
  })
  assert.equal(leaseBusyOnlySummary.outcome, 'success', '租约占用是可恢复调度，不得标记后台任务部分失败')
  assert.equal(leaseBusyOnlySummary.leaseBusyCount, 1)
  assert.equal(leaseBusyOnlySummary.diagnosticCount, 0, '租约占用不是账户级查询诊断')
  assert.equal(leaseBusyOnlySummary.unfinishedCount, 1)

  await assertBalanceRefreshSchedulerSuccess('candidate-timeout', () => balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    refreshCandidate: async (_candidate, context) => await new Promise<never>((_resolve, reject) => {
      context.signal.addEventListener('abort', () => reject(context.signal.reason), { once: true })
    }),
    runBudgetMs: 20,
    candidateTimeoutMs: 5
  }))
  await assert.rejects(
    balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [runtimeCandidate],
      refreshCandidate: async () => undefined
    }),
    /候选返回结果无效/,
    '畸形候选结果不得默认记为 refreshed'
  )
  await assertBalanceRefreshSchedulerFailure('malformed-candidate-outcome', () => balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    refreshCandidate: async () => ({ outcome: 'unknown' })
  }))
  await assertBalanceRefreshSchedulerSuccess('unsupported', () => balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    refreshCandidate: async () => ({ outcome: 'unsupported', snapshot: { status: 'unsupported' }, persisted: true })
  }))
  await assertBalanceRefreshSchedulerSuccess('lease-busy', () => balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    refreshCandidate: async () => ({ outcome: 'lease_busy', snapshot: { status: 'pending' }, persisted: false })
  }))
  await assertBalanceRefreshSchedulerSuccess('runtime-deferred', () => balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    loadRuntimeAvailability: async () => ({ available: true, values: { [dueA.id]: { status: 'local_suppressed' as const } } }),
    deferCandidate: async () => true
  }))

  const cancelledController = new AbortController()
  cancelledController.abort(new Error('上层调度停止'))
  const cancelledSummary = await balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [runtimeCandidate],
    refreshCandidate: async () => { throw new Error('已取消后不应执行') }
  }, cancelledController.signal)
  assert.equal(cancelledSummary.processedCount, 0, '外层取消后不得再启动候选')
  assert.equal(cancelledSummary.deferredCount, 1, '外层取消未执行候选应保留为延后')

  await assert.rejects(
    balanceRefreshJob.runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => { throw new Error('数据库候选扫描失败') }
    }),
    /数据库候选扫描失败/,
    '候选扫描或数据库边界失败仍必须让后台任务进入真正失败'
  )
  assert.equal(configured.balanceQueryEnabled, true)
  assert.deepEqual(configured.balanceQueryConfig, { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' })
  const recoveryConfig = JSON.stringify({ adapter: 'builtin', intervalMinutes: 5 })
  database.prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run('active', 1, recoveryConfig, recoverMissing.id)
  database.prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run('active', 1, recoveryConfig, recoverPaused.id)
  database.prepare(`
    UPDATE accounts
    SET status = ?, schedulable = ?, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run('disabled', 0, recoveryConfig, recoverInactive.id)
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: recoverPaused.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'unsupported', errorMessage: '当前配置未找到可用余额接口' }
  })
  assert.deepEqual(
    (await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 100 })).map((item: { id: string }) => item.id),
    [recoverMissing.id, recoverPaused.id].sort(),
    '历史 unsupported 的活动账户必须进入自愈，余额失败不能永久移出调度'
  )
  assert.equal(
    (await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 100 })).some((item: { id: string }) => item.id === recoverInactive.id),
    false,
    '用户已开启余额查询的停用账户不得进入自动自愈候选'
  )
  const nullGenerationCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(recoverMissing.id)
  assert.ok(nullGenerationCandidate)
  const nullGenerationCommit = {
    accountId: recoverMissing.id,
    expectedConfigRevision: nullGenerationCandidate.configRevision,
    expectedConfig: nullGenerationCandidate.config,
    expectedNextRefreshAt: null,
    nextConfig: nullGenerationCandidate.config,
    nextRefreshAt: new Date(Date.now() + 60_000).toISOString()
  }
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync(nullGenerationCommit), true)
  assert.equal(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(recoverMissing.id)?.balance_query_next_refresh_at,
    nullGenerationCommit.nextRefreshAt,
    '自动自愈的空计划提交必须写入非空的下一次刷新计划'
  )
  assert.equal(
    await balanceRepository.commitAccountBalanceRefreshAsync(nullGenerationCommit),
    false,
    '自动自愈的旧空计划尝试不得覆盖已经推进的非空刷新计划'
  )
  const pausedPrefix = Array.from({ length: 45 }, (_, index) => create(`recover-paused-prefix-${index}`))
  const recoverAfterPausedPrefix = create('recover-after-paused-prefix')
  const configureRecovery = database.prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `)
  for (const account of pausedPrefix) {
    configureRecovery.run(recoveryConfig, account.id)
    balanceRepository.replaceAccountBalanceSnapshot({
      accountId: account.id,
      systemAccountId: 'sys_admin',
      snapshot: { status: 'unsupported', errorMessage: '暂停回归夹具' }
    })
  }
  configureRecovery.run(recoveryConfig, recoverAfterPausedPrefix.id)
  const firstPausedRecoveryPage = await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 10 })
  assert.ok(
    firstPausedRecoveryPage.some((item: { id: string }) => pausedPrefix.some((account) => account.id === item.id)),
    '历史 unsupported 暂停账户必须进入恢复调度'
  )
  let reachedRecoveryAfterPausedPrefix = firstPausedRecoveryPage.some((item: { id: string }) => item.id === recoverAfterPausedPrefix.id)
  for (let round = 1; round < 5 && !reachedRecoveryAfterPausedPrefix; round += 1) {
    reachedRecoveryAfterPausedPrefix = (await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 10 }))
      .some((item: { id: string }) => item.id === recoverAfterPausedPrefix.id)
  }
  assert.ok(reachedRecoveryAfterPausedPrefix, 'SQLite 自愈游标必须按实际消费位置分轮越过前缀，不能固定扫描最前一页')
  database.prepare(`
    UPDATE accounts
    SET balance_query_enabled = 0
    WHERE balance_query_enabled = 1 AND balance_query_next_refresh_at IS NULL
  `).run()
  const recoveryCursorCandidates = Array.from({ length: 6 }, (_, index) => create(`recovery-cursor-${index}`))
  for (const account of recoveryCursorCandidates) configureRecovery.run(recoveryConfig, account.id)
  const recoveredAcrossSmallPages = new Set<string>()
  for (let round = 0; round < 3; round += 1) {
    for (const candidate of await balanceRepository.listAccountsNeedingBalanceRefreshRecoveryAsync({ limit: 2 })) {
      recoveredAcrossSmallPages.add(candidate.id)
    }
  }
  assert.deepEqual(
    recoveredAcrossSmallPages,
    new Set(recoveryCursorCandidates.map((account) => account.id)),
    'recovery 游标必须停在实际消费位置，连续小批领取不得饿死同一扫描页后续候选'
  )
  for (const account of recoveryCursorCandidates) {
    database.prepare(`UPDATE accounts SET balance_query_enabled = 0 WHERE id = ?`).run(account.id)
  }
  const unconfiguredMultiRow = database.prepare(`SELECT balance_query_enabled, balance_query_config_json FROM accounts WHERE id = ?`).get(multi.id) as Record<string, unknown>
  assert.equal(unconfiguredMultiRow.balance_query_enabled, 0)
  assert.deepEqual(JSON.parse(String(unconfiguredMultiRow.balance_query_config_json)), { adapter: 'builtin', intervalMinutes: 5 }, '多 Key 新建即使未提交余额配置，也必须写入已配置关闭标记')
  const configuredRow = database.prepare(`SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(configured.id) as Record<string, unknown>
  assert.equal(configuredRow.balance_query_enabled, 1)
  assert.deepEqual(JSON.parse(String(configuredRow.balance_query_config_json)), { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' })
  assert.equal(typeof configuredRow.balance_query_next_refresh_at, 'string')
  const lifecycleGeneration = '2026-07-11T06:00:00.000Z'
  databaseModule.getBusinessDatabase().prepare(`UPDATE accounts SET balance_query_next_refresh_at = ? WHERE id = ?`).run(lifecycleGeneration, lifecycle.id)
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: lifecycle.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '12.340000', lastAttemptAt: lifecycleGeneration, lastSuccessAt: lifecycleGeneration },
    nextRefreshAfter: lifecycleGeneration
  })
  const lifecycleSameValue = repositories.updateAccount(lifecycle.id, {
    name: 'lifecycle-renamed',
    balanceQueryEnabled: true,
    balanceQueryConfig: { intervalMinutes: 5, adapter: 'builtin' }
  }, access)
  assert.ok(lifecycleSameValue)
  assert.equal(lifecycleSameValue.balanceQueryNextRefreshAt, lifecycleGeneration, '同值全量表单和无关字段修改必须保留刷新代次')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([lifecycle.id]).get(lifecycle.id)?.remainingUsd,
    '12.340000',
    '同值全量表单和无关字段修改必须保留旧快照'
  )
  const lifecycleConnectionChanged = repositories.updateAccount(lifecycle.id, {
    credentials: { api_key: 'sk-lifecycle-next', base_url: 'https://relay.example/v1' },
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5 }
  }, access)
  assert.ok(lifecycleConnectionChanged)
  assert.notEqual(lifecycleConnectionChanged.balanceQueryNextRefreshAt, lifecycleGeneration, 'Key 变化必须开启新的刷新代次')
  const configuredDisabled = repositories.updateAccount(configured.id, {
    balanceQueryEnabled: false,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.equal(configuredDisabled?.balanceQueryEnabled, false)
  assert.equal(database.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(configured.id)?.balance_query_enabled, 0)
  const multiKeyRequestedBalance = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'multi-key-requested-balance', type: 'api_key',
    credentials: { api_keys: ['sk-multi-a', 'sk-multi-b'], base_url: 'https://relay.example/v1' },
    supportedModels: ['gpt-5.5'], groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 7, preferredBuiltinAdapter: 'user_balance' }
  }, access)
  assert.equal(multiKeyRequestedBalance.balanceQueryEnabled, false, '新建多 Key 即使请求开启余额也必须保存成功并自动关闭')
  const multiKeyRequestedBalanceRow = database.prepare(`
    SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at
    FROM accounts WHERE id = ?
  `).get(multiKeyRequestedBalance.id) as Record<string, unknown>
  assert.equal(multiKeyRequestedBalanceRow.balance_query_enabled, 0)
  assert.equal(multiKeyRequestedBalanceRow.balance_query_next_refresh_at, null)
  assert.deepEqual(JSON.parse(String(multiKeyRequestedBalanceRow.balance_query_config_json)), {
    adapter: 'builtin', intervalMinutes: 7, preferredBuiltinAdapter: 'user_balance'
  }, '多 Key 自动关闭不能丢失余额查询配置')

  const singleToMulti = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'single-to-multi', type: 'api_key', credentials: { api_key: 'sk-single', base_url: 'https://relay.example/v1' },
    supportedModels: ['gpt-5.5'], groupId: group.id, balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } }
  }, access)
  const singleToMultiOldGeneration = '2000-01-01T00:00:00.000Z'
  database.prepare(`UPDATE accounts SET balance_query_next_refresh_at = ? WHERE id = ?`).run(singleToMultiOldGeneration, singleToMulti.id)
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: singleToMulti.id,
    systemAccountId: 'sys_admin',
    snapshot: {
      status: 'fresh',
      remainingUsd: '41.000000',
      lastAttemptAt: singleToMultiOldGeneration,
      lastSuccessAt: singleToMultiOldGeneration
    },
    nextRefreshAfter: singleToMultiOldGeneration
  })
  const singleToMultiRevision = singleToMulti.configRevision ?? 1
  const singleToMultiUpdated = repositories.updateAccount(singleToMulti.id, {
    credentials: { api_keys: ['sk-single', 'sk-second'], base_url: 'https://relay.example/v1' }
  }, access)
  assert.ok(singleToMultiUpdated)
  assert.equal(singleToMultiUpdated.balanceQueryEnabled, false, '只更新凭据的直接 repository 入口也必须中央关闭余额')
  const singleToMultiRow = database.prepare(`
    SELECT balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, config_revision
    FROM accounts WHERE id = ?
  `).get(singleToMulti.id) as Record<string, unknown>
  assert.equal(singleToMultiRow.balance_query_enabled, 0)
  assert.equal(singleToMultiRow.balance_query_next_refresh_at, null)
  assert.deepEqual(JSON.parse(String(singleToMultiRow.balance_query_config_json)), {
    adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' }
  }, '凭据更新触发自动关闭时必须原样保留既有余额配置')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: singleToMulti.id,
    expectedConfigRevision: singleToMultiRevision,
    expectedConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } },
    nextConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } },
    nextRefreshAt: new Date().toISOString()
  }), false, '多 Key 保存递增配置版本后，进行中的旧余额结果不得提交')
  const returnedToSingle = repositories.updateAccount(singleToMulti.id, {
    credentials: { api_key: 'sk-single', base_url: 'https://relay.example/v1' }
  }, access)
  assert.ok(returnedToSingle)
  assert.equal(database.prepare(`SELECT balance_query_enabled FROM accounts WHERE id = ?`).get(singleToMulti.id)?.balance_query_enabled, 0, '多 Key 改回单 Key 后不能自动恢复余额查询')
  database.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(singleToMulti.id)
  assert.equal(
    await balanceRepository.findAccountBalanceDetectionCandidateAsync(singleToMulti.id, returnedToSingle.configRevision ?? Number(singleToMultiRow.config_revision) + 1),
    undefined,
    '保留的余额配置必须阻止单 Key 恢复后被首次探测重新开启'
  )
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([singleToMulti.id]).get(singleToMulti.id)?.remainingUsd,
    '41.000000',
    '模拟 stats 清理失败和进程重启时，旧 Key 余额快照应仍留在持久化层'
  )
  const resumedSingle = repositories.updateAccount(singleToMulti.id, {
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'custom', intervalMinutes: 6, custom: { path: '/balance', remainingPointer: '/remaining' } }
  }, access)
  assert.ok(resumedSingle)
  const resumedCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(singleToMulti.id)
  assert.ok(resumedCandidate)
  assert.notEqual(resumedCandidate.nextRefreshAt, singleToMultiOldGeneration, '重新启用必须产生与旧快照不同的刷新代次')
  let markResumedQueryStarted: (() => void) | undefined
  const resumedQueryStarted = new Promise<void>((resolve) => {
    markResumedQueryStarted = resolve
  })
  let releaseResumedQuery: (() => void) | undefined
  const resumedQueryGate = new Promise<void>((resolve) => {
    releaseResumedQuery = resolve
  })
  const resumedRefresh = balanceQueryService.refreshAccountBalanceCandidate(resumedCandidate, {
    query: async () => {
      markResumedQueryStarted?.()
      await resumedQueryGate
      throw new UpstreamRequestTimeoutError('恢复单 Key 后首次余额查询超时')
    }
  })
  await resumedQueryStarted
  let unexpectedLeaseFallbackQueryCount = 0
  const leaseFallback = await balanceQueryService.refreshAccountBalanceCandidate(resumedCandidate, {
    query: async () => {
      unexpectedLeaseFallbackQueryCount += 1
      return { status: 'fresh', remainingUsd: '100.000000' }
    }
  })
  assert.equal(unexpectedLeaseFallbackQueryCount, 0, '未获得余额租约时不能再次请求上游')
  assert.equal(leaseFallback.status, 'refreshing', '未获得租约且持久化快照代次失配时只能返回刷新中')
  assert.equal(leaseFallback.remainingUsd, undefined, '未获得租约时不能回传旧 Key 的余额')
  releaseResumedQuery?.()
  const resumedFailure = await resumedRefresh
  assert.equal(resumedFailure.status, 'pending', '恢复单 Key后的首次瞬时失败不能复制旧 Key 的成功状态')
  assert.equal(resumedFailure.remainingUsd, undefined, '恢复单 Key后的首次瞬时失败不能把旧余额洗成当前代次')
  const resumedFailureRecord = balanceRepository.loadAccountBalanceSnapshotRecordsByAccountIds([singleToMulti.id]).get(singleToMulti.id)
  assert.equal(resumedFailureRecord?.snapshot.status, 'pending')
  assert.equal(resumedFailureRecord?.snapshot.remainingUsd, undefined)
  assert.notEqual(resumedFailureRecord?.nextRefreshAfter, singleToMultiOldGeneration, '新失败快照必须写入当前刷新链的新代次')
  const dueAt = '2026-07-11T00:00:00.000Z'
  const futureAt = '2026-07-12T00:00:00.000Z'
  const configure = database.prepare(`UPDATE accounts SET status = ?, schedulable = 1, balance_query_enabled = 1, balance_query_config_json = ?, balance_query_next_refresh_at = ? WHERE id = ?`)
  const builtinConfig = JSON.stringify({ adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' })
  configure.run('active', builtinConfig, dueAt, dueA.id)
  configure.run('active', builtinConfig, dueAt, dueB.id)
  configure.run('disabled', builtinConfig, dueAt, disabled.id)
  configure.run('active', builtinConfig, dueAt, oauth.id)
  configure.run('active', builtinConfig, dueAt, multi.id)
  configure.run('active', builtinConfig, futureAt, future.id)
  configure.run('active', builtinConfig, futureAt, transientFailure.id)
  configure.run('active', builtinConfig, futureAt, deterministicFailure.id)
  configure.run('active', builtinConfig, futureAt, untrustedStatusFailure.id)
  configure.run('active', builtinConfig, futureAt, manualRefresh.id)
  configure.run('active', builtinConfig, futureAt, gatewayActivity.id)
  configure.run('active', builtinConfig, futureAt, automaticEligibilityRevoked.id)
  database.prepare(`UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?`).run(autoDetect.id)

  const invalidDuePrefix = Array.from({ length: 48 }, (_, index) => create(`invalid-due-prefix-${index}`))
  const validAfterInvalidDuePrefix = create('valid-after-invalid-due-prefix')
  const starvationDueAt = '2026-07-10T00:00:00.000Z'
  const configureInvalidDue = database.prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, balance_query_enabled = 1,
        balance_query_config_json = 'not-json', balance_query_next_refresh_at = ?
    WHERE id = ?
  `)
  for (const account of invalidDuePrefix) configureInvalidDue.run(starvationDueAt, account.id)
  configure.run('active', builtinConfig, starvationDueAt, validAfterInvalidDuePrefix.id)
  assert.deepEqual(
    balanceRepository.listAccountsDueForBalanceRefresh({ now: '2026-07-11T12:00:00.000Z', limit: 1 }).map((item: { id: string }) => item.id),
    [validAfterInvalidDuePrefix.id],
    '普通 due 游标必须越过无效配置前缀，不能永久饿死后续合法账户'
  )
  database.prepare(`UPDATE accounts SET balance_query_enabled = 0, balance_query_next_refresh_at = NULL WHERE id = ?`)
    .run(validAfterInvalidDuePrefix.id)
  for (const account of invalidDuePrefix) {
    database.prepare(`UPDATE accounts SET balance_query_enabled = 0, balance_query_next_refresh_at = NULL WHERE id = ?`).run(account.id)
  }

  const dueCursorCandidates = Array.from({ length: 6 }, (_, index) => create(`due-cursor-${index}`))
  const cursorStarvationDueAt = '2026-07-09T00:00:00.000Z'
  for (const account of dueCursorCandidates) configure.run('active', builtinConfig, cursorStarvationDueAt, account.id)
  const dueAcrossSmallPages = new Set<string>()
  for (let round = 0; round < 12; round += 1) {
    for (const candidate of balanceRepository.listAccountsDueForBalanceRefresh({ now: '2026-07-11T12:00:00.000Z', limit: 2 })) {
      dueAcrossSmallPages.add(candidate.id)
    }
  }
  assert.ok(
    dueCursorCandidates.every((account) => dueAcrossSmallPages.has(account.id)),
    'due 游标必须停在实际消费位置，连续小批领取不得饿死同一扫描页后续候选'
  )
  for (const account of dueCursorCandidates) {
    database.prepare(`UPDATE accounts SET balance_query_enabled = 0, balance_query_next_refresh_at = NULL WHERE id = ?`).run(account.id)
  }

  assert.equal(
    [...balanceRepositorySource.matchAll(/appendUniqueBalanceCandidates\(/g)].length,
    5,
    'SQLite/PostgreSQL 的 due/recovery 四条路径必须统一复用逐行消费 helper'
  )
  assert.match(balanceRepositorySource, /lastExamined/, '候选追加 helper 必须返回最后实际检查行')
  assert.match(balanceRepositorySource, /consumedAll/, '候选追加 helper 必须说明扫描页是否全部消费')
  assert.doesNotMatch(
    balanceRepositorySource,
    /(?:sqlite|postgres)Balance(?:DueCursor|RecoveryAfterId)\s*=\s*(?:balanceDueCursorFromRow\()?rows\.at\(-1\)/,
    'SQLite/PostgreSQL 游标不得直接推进到扫描页末'
  )

  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    expectedNextRefreshAt: '2026-07-10T23:59:59.000Z',
    nextConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: dueAt
  }), false, '旧余额尝试的刷新代次不匹配时不得覆盖新结果')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    nextConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: dueAt
  }), true, '成功回退后应记录新的内置适配偏好')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    nextConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'newapi' },
    nextRefreshAt: dueAt
  }), false, '旧配置快照不得覆盖已经更新的适配偏好')
  assert.equal((await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueA.id))?.config.preferredBuiltinAdapter, 'user_balance')
  assert.equal(await balanceRepository.commitAccountBalanceRefreshAsync({
    accountId: dueA.id,
    expectedConfigRevision: dueA.configRevision ?? 1,
    expectedConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextConfig: { adapter: 'builtin', intervalMinutes: 5 },
    nextRefreshAt: dueAt
  }), true, '全部内置适配失败后应清除旧偏好')
  assert.equal((await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueA.id))?.config.preferredBuiltinAdapter, undefined)

  const autoDetectCandidate = await balanceRepository.findAccountBalanceDetectionCandidateAsync(autoDetect.id, autoDetect.configRevision ?? 1)
  assert.equal(autoDetectCandidate?.id, autoDetect.id)
  assert.equal(await balanceRepository.enableDetectedAccountBalanceQueryAsync({
    accountId: autoDetect.id,
    expectedConfigRevision: (autoDetect.configRevision ?? 1) + 1,
    config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: futureAt
  }), false, '旧配置版本不得自动开启余额查询')
  assert.equal(await balanceRepository.enableDetectedAccountBalanceQueryAsync({
    accountId: autoDetect.id,
    expectedConfigRevision: autoDetect.configRevision ?? 1,
    config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance' },
    nextRefreshAt: futureAt
  }), true)
  assert.equal(await balanceRepository.enableDetectedAccountBalanceQueryAsync({
    accountId: autoDetect.id,
    expectedConfigRevision: autoDetect.configRevision ?? 1,
    config: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' },
    nextRefreshAt: futureAt
  }), false, '已经开启后不得被自动探测覆盖')
  assert.deepEqual((await balanceRepository.findAccountBalanceRefreshCandidateAsync(autoDetect.id))?.config, {
    adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'user_balance'
  })

  const due = balanceRepository.listAccountsDueForBalanceRefresh({ now: '2026-07-11T12:00:00.000Z', limit: 100 })
  assert.deepEqual(due.map((item: { id: string }) => item.id), [dueA.id, dueB.id].sort(), '自动余额刷新只能领取活动且可调度的物理单 API Key 账户')

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: dueA.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '7.310000', lastAttemptAt: dueAt, lastSuccessAt: dueAt }
  })
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: dueA.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'failed', errorMessage: '上游鉴权失败（HTTP 401）', lastAttemptAt: dueAt }
  })
  const snapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([dueA.id]).get(dueA.id)
  assert.equal(snapshot?.status, 'failed')
  assert.equal(snapshot?.remainingUsd, undefined, '失败快照必须清除旧金额')
  assert.equal(snapshot?.errorMessage, '上游鉴权失败（HTTP 401）')

  const candidate = balanceRepository.listAccountsDueForBalanceRefresh({ now: '2026-07-11T12:00:00.000Z', limit: 1 })[0]
  const fallbackAttempts: string[] = []
  const fallbackResult = await balanceQueryService.queryBuiltinAccountBalance(candidate, {
    queryAdapter: async (_candidate, adapter) => {
      fallbackAttempts.push(adapter)
      return adapter === 'user_balance'
        ? { status: 'fresh', remainingUsd: '4.790000', rawRemaining: '4.79', rawUnit: 'usd', basis: 'wallet' }
        : { status: 'unsupported', basis: 'api_key_quota' }
    }
  })
  assert.equal(fallbackResult.adapter, 'user_balance', '内置适配器返回 unsupported 后必须继续回退')
  assert.equal(fallbackResult.snapshot.remainingUsd, '4.790000')
  assert.deepEqual(fallbackAttempts, ['sub2api', 'newapi', 'openai_billing', 'litellm', 'user_balance'])
  const unsupportedResult = await balanceQueryService.queryBuiltinAccountBalance(candidate, {
    queryAdapter: async () => ({ status: 'unsupported', basis: 'api_key_quota' })
  })
  assert.equal(unsupportedResult.snapshot.status, 'unsupported', '全部内置适配器不支持时应返回可暂停的能力状态')
  let unknownAdapterAttempts = 0
  await assert.rejects(
    balanceQueryService.queryBuiltinAccountBalance(candidate, {
      queryAdapter: async () => {
        unknownAdapterAttempts += 1
        throw new Error('余额适配器依赖异常')
      }
    }),
    /余额适配器依赖异常/,
    '未知适配器依赖异常不得伪装为外部余额诊断'
  )
  assert.equal(unknownAdapterAttempts, 1, '未知适配器依赖异常必须立即冒泡，不能继续伪造上游回退')

  const untrustedStatuses = [
    300, 301, 302, 307, 308,
    400, 401, 403, 404, 408, 409, 418, 422, 429, 451,
    500, 501, 502, 503, 504, 599
  ] as const
  for (const upstreamStatus of untrustedStatuses) {
    const statusCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(untrustedStatusFailure.id)
    assert.ok(statusCandidate)
    const previousSuccessAt = new Date(Date.now() - 60_000).toISOString()
    balanceRepository.replaceAccountBalanceSnapshot({
      accountId: untrustedStatusFailure.id,
      systemAccountId: 'sys_admin',
      snapshot: {
        status: 'fresh',
        remainingUsd: '6.250000',
        lastAttemptAt: previousSuccessAt,
        lastSuccessAt: previousSuccessAt
      },
      nextRefreshAfter: statusCandidate.nextRefreshAt ?? undefined
    })
    mockState.status = upstreamStatus
    mockState.invalidJson = false
    mockState.recoverWithNewApi = false
    const requestCountBefore = mockState.requestCount
    const statusResult = await balanceQueryService.refreshAccountBalanceCandidate(statusCandidate)
    assert.equal(
      mockState.requestCount - requestCountBefore,
      5,
      `HTTP ${upstreamStatus} 不得被解读为鉴权或能力结论，应试完五个内置适配器`
    )
    assert.equal(statusResult.status, 'unsupported', `HTTP ${upstreamStatus} 必须作为完整响应未命中保存，而非伪造 transport 失败`)
    assert.equal(statusResult.remainingUsd, undefined)
    assert.equal(statusResult.consecutiveTransientFailures, undefined)
    assert.match(statusResult.errorMessage ?? '', new RegExp(`HTTP ${upstreamStatus}`))
    const storedCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(untrustedStatusFailure.id)
    assert.deepEqual(
      storedCandidate?.config,
      { adapter: 'builtin', intervalMinutes: 5 },
      `HTTP ${upstreamStatus} 必须保留用户配置但清除不再命中的内置适配偏好`
    )
    assertAccountDispatchState(database, untrustedStatusFailure.id, 'active', 1, `HTTP ${upstreamStatus} 余额查询失败后`)
  }

  const invalidJsonCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(untrustedStatusFailure.id)
  assert.ok(invalidJsonCandidate)
  mockState.status = 200
  mockState.invalidJson = true
  mockState.recoverWithNewApi = false
  const invalidJsonRequestCountBefore = mockState.requestCount
  const invalidJsonResult = await balanceQueryService.queryBuiltinAccountBalance(invalidJsonCandidate)
  assert.equal(invalidJsonResult.snapshot.status, 'unsupported', '完整 2xx 中的非法 JSON 属于本地可验证结构约束')
  assert.equal(mockState.requestCount - invalidJsonRequestCountBefore, 5, '结构不匹配仍应尝试全部内置适配器')

  mockState.status = 401
  mockState.invalidJson = false
  mockState.recoverWithNewApi = true
  const recoveredAdapterRequestCountBefore = mockState.requestCount
  const recoveredAdapterResult = await balanceQueryService.queryBuiltinAccountBalance(invalidJsonCandidate)
  assert.equal(recoveredAdapterResult.adapter, 'newapi', '前一适配器返回不可信 401 后，后续适配器仍可成功接管')
  assert.equal(recoveredAdapterResult.snapshot.status, 'fresh')
  assert.equal(recoveredAdapterResult.snapshot.remainingUsd, '7.310000')
  assert.equal(mockState.requestCount - recoveredAdapterRequestCountBefore, 3, '应尝试 sub2api 后完成 newapi 的两步查询')
  mockState.resetConnection = true
  mockState.recoverWithNewApi = false
  const genericNetworkRequestCountBefore = mockState.requestCount
  const genericNetworkResult = await balanceQueryService.refreshAccountBalanceCandidate(invalidJsonCandidate)
  assert.equal(mockState.requestCount - genericNetworkRequestCountBefore, 5, '通用网络请求异常必须作为每个内置适配器的可恢复失败')
  assert.equal(genericNetworkResult.status, 'pending', '通用网络请求异常必须归为临时余额诊断')
  assert.equal(genericNetworkResult.consecutiveTransientFailures, 1)
  assert.equal(genericNetworkResult.lastTransientErrorMessage, '上游余额接口网络请求失败')
  mockState.resetConnection = false

  const interruptedBodyCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(untrustedStatusFailure.id)
  assert.ok(interruptedBodyCandidate)
  const interruptedBodyPreviousSuccessAt = new Date(Date.now() - 60_000).toISOString()
  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: interruptedBodyCandidate.id,
    systemAccountId: interruptedBodyCandidate.systemAccountId,
    snapshot: {
      status: 'fresh',
      remainingUsd: '6.250000',
      lastAttemptAt: interruptedBodyPreviousSuccessAt,
      lastSuccessAt: interruptedBodyPreviousSuccessAt
    },
    nextRefreshAfter: interruptedBodyCandidate.nextRefreshAt ?? undefined
  })
  mockState.resetResponseBody = true
  const interruptedBodyRequestCountBefore = mockState.requestCount
  const interruptedResponseBodyCountBefore = mockState.interruptedResponseBodyCount
  const interruptedBodyRun = await balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [interruptedBodyCandidate]
  })
  assert.equal(interruptedBodyRun.outcome, 'success', '响应体传输中断必须完成自动余额刷新任务')
  assert.equal(interruptedBodyRun.staleCount, 1, '响应体传输中断必须形成账户级 stale 诊断')
  assert.equal(interruptedBodyRun.failedCount, 0)
  assert.equal(interruptedBodyRun.diagnosticCount, 1)
  assert.equal(mockState.requestCount - interruptedBodyRequestCountBefore, 5, '每个内置适配器都必须实际读取响应体后识别传输中断')
  assert.equal(mockState.interruptedResponseBodyCount - interruptedResponseBodyCountBefore, 5, 'mock 必须先发送 200 响应头和部分正文再断开')
  await assertBalanceRefreshSchedulerSuccess('response-body-interrupted', async () => await balanceRefreshJob.runAccountBalanceRefresh({
    listRecoveryCandidates: async () => [],
    listDueCandidates: async () => [interruptedBodyCandidate]
  }))
  mockState.resetResponseBody = false

  const tested = await balanceQueryService.testAccountBalanceCandidate(candidate, {
    query: async () => ({ status: 'fresh', remainingUsd: '8.880000', rawRemaining: '8.88', rawUnit: 'usd', basis: 'wallet' })
  })
  assert.equal(tested.remainingUsd, '8.880000')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([candidate.id]).get(candidate.id)?.remainingUsd,
    undefined,
    '草稿余额测试不得写入正式余额快照'
  )
  let queryCount = 0
  const query = async () => {
    queryCount += 1
    await new Promise((resolve) => setTimeout(resolve, 30))
    return { status: 'fresh', remainingUsd: '9.990000', rawRemaining: '9.99', rawUnit: 'usd', basis: 'wallet' } as const
  }
  assertAccountDispatchState(database, candidate.id, 'active', 1, '成功刷新前')
  await Promise.all([
    balanceQueryService.refreshAccountBalanceCandidate(candidate, { query }),
    balanceQueryService.refreshAccountBalanceCandidate(candidate, { query })
  ])
  assert.equal(queryCount, 1, '同一账户并发刷新必须通过租约只查询一次上游')
  assertAccountDispatchState(database, candidate.id, 'active', 1, '成功刷新后')

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: transientFailure.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '7.310000', lastAttemptAt: dueAt, lastSuccessAt: dueAt },
    nextRefreshAfter: futureAt
  })
  let transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  const timeoutQuery = async () => {
    throw new UpstreamRequestTimeoutError('上游余额查询超时')
  }
  assertAccountDispatchState(database, transientFailure.id, 'active', 1, 'transient 失败前')
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  assertAccountDispatchState(database, transientFailure.id, 'active', 1, 'transient 失败后')
  let transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'fresh', '第一次临时失败必须保留上次成功状态')
  assert.equal(transientSnapshot.remainingUsd, '7.310000', '第一次临时失败必须保留上次成功金额')
  assert.equal(transientSnapshot.consecutiveTransientFailures, 1)
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'fresh', '第二次临时失败仍应保留上次成功状态')
  assert.equal(transientSnapshot.consecutiveTransientFailures, 2)
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'failed', '连续第三次临时失败才应标记查询失败')
  assert.equal(transientSnapshot.remainingUsd, undefined, '连续第三次临时失败必须清除旧金额')
  assert.equal(transientSnapshot.consecutiveTransientFailures, 3)
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  transientCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(transientCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(transientCandidate, { query: timeoutQuery })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.consecutiveTransientFailures, 3, '连续失败次数应在 3 封顶')
  assertBalanceRetryDelay(database, transientFailure.id, transientSnapshot.lastAttemptAt, 5)
  const recoveryCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(transientFailure.id)
  assert.ok(recoveryCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(recoveryCandidate, {
    query: async () => ({ status: 'fresh', remainingUsd: '8.880000', rawRemaining: '8.88', rawUnit: 'usd', basis: 'wallet' })
  })
  transientSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([transientFailure.id]).get(transientFailure.id)
  assert.ok(transientSnapshot)
  assert.equal(transientSnapshot.status, 'fresh')
  assert.equal(transientSnapshot.remainingUsd, '8.880000')
  assert.equal(transientSnapshot.consecutiveTransientFailures, undefined, '成功后必须清零连续临时失败次数')

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: deterministicFailure.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '5.550000', lastAttemptAt: dueAt, lastSuccessAt: dueAt }
  })
  const deterministicCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(deterministicFailure.id)
  assert.ok(deterministicCandidate)
  assertAccountDispatchState(database, deterministicFailure.id, 'active', 1, '显式超时失败前')
  await balanceQueryService.refreshAccountBalanceCandidate(deterministicCandidate, {
    query: async () => { throw new DOMException('上游余额查询超时', 'TimeoutError') }
  })
  assertAccountDispatchState(database, deterministicFailure.id, 'active', 1, '显式超时失败后')
  const deterministicSnapshot = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([deterministicFailure.id]).get(deterministicFailure.id)
  assert.ok(deterministicSnapshot)
  assert.equal(deterministicSnapshot.status, 'pending', '显式上游超时必须作为可重试诊断，不得落为 unsupported')
  assert.equal(deterministicSnapshot.remainingUsd, undefined, '不属于当前配置代次的旧余额不得复用')
  assert.equal(deterministicSnapshot.consecutiveTransientFailures, 1)
  const deterministicRow = database.prepare(`SELECT balance_query_enabled, balance_query_next_refresh_at, balance_query_config_json FROM accounts WHERE id = ?`).get(deterministicFailure.id) as Record<string, unknown>
  assert.equal(deterministicRow.balance_query_enabled, 1, '能力暂停不能替用户关闭余额开关')
  assert.deepEqual(JSON.parse(String(deterministicRow.balance_query_config_json)), {
    adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api'
  }, '临时上游超时不得清除首选余额适配器')
  assertBalanceRetryDelay(database, deterministicFailure.id, deterministicSnapshot.lastAttemptAt, 5)
  const invalidBaseUrlResult = await balanceQueryService.refreshAccountBalanceCandidate({
    ...deterministicCandidate,
    credentials: { api_key: 'sk-invalid-base-url', base_url: 'not-a-valid-url' }
  })
  assert.equal(invalidBaseUrlResult.status, 'unsupported')
  assert.equal(invalidBaseUrlResult.errorMessage, '账户 Base URL 无效', '确定性本地配置错误应保留可读原因')
  const reactivated = repositories.updateAccount(deterministicFailure.id, {
    balanceQueryEnabled: true,
    balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 5, preferredBuiltinAdapter: 'sub2api' }
  }, access)
  assert.ok(reactivated)
  const reactivatedRow = database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(deterministicFailure.id) as { balance_query_next_refresh_at?: string | null }
  assert.ok(reactivatedRow.balance_query_next_refresh_at, '保存账户配置必须保留余额重试调度')
  const reactivatedCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(deterministicFailure.id)
  assert.ok(reactivatedCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(reactivatedCandidate, { query: timeoutQuery })
  const snapshotAfterReactivationTimeout = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([deterministicFailure.id]).get(deterministicFailure.id)
  assert.equal(snapshotAfterReactivationTimeout?.status, 'pending', '不可信 HTTP 状态后的临时失败仍必须显示待重试')
  assert.equal(snapshotAfterReactivationTimeout?.consecutiveTransientFailures, 2)
  assertBalanceRetryDelay(database, deterministicFailure.id, snapshotAfterReactivationTimeout?.lastAttemptAt, 5)

  balanceRepository.replaceAccountBalanceSnapshot({
    accountId: manualRefresh.id,
    systemAccountId: 'sys_admin',
    snapshot: { status: 'fresh', remainingUsd: '6.660000', lastAttemptAt: dueAt, lastSuccessAt: dueAt }
  })
  const manualCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(manualRefresh.id)
  assert.ok(manualCandidate)
  assertAccountDispatchState(database, manualRefresh.id, 'active', 1, 'manual 失败前')
  const manualResult = await balanceQueryService.refreshAccountBalanceCandidate(manualCandidate, {
    mode: 'manual',
    query: timeoutQuery
  })
  assertAccountDispatchState(database, manualRefresh.id, 'active', 1, 'manual 失败后')
  assert.equal(manualResult.status, 'failed', '人工刷新失败必须立即返回本次错误')
  const storedAfterManualFailure = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([manualRefresh.id]).get(manualRefresh.id)
  assert.ok(storedAfterManualFailure)
  assert.equal(storedAfterManualFailure.status, 'failed', '人工刷新失败必须立即替换当前行状态')
  assert.equal(storedAfterManualFailure.remainingUsd, undefined, '人工刷新失败不能继续展示旧金额')
  const manualUnsupported = await balanceQueryService.refreshAccountBalanceCandidate(manualCandidate, {
    mode: 'manual',
    query: async () => ({ status: 'unsupported', basis: 'api_key_quota' })
  })
  assert.equal(manualUnsupported.status, 'unsupported', '人工刷新必须即时返回能力不支持状态')
  const storedAfterManualUnsupported = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([manualRefresh.id]).get(manualRefresh.id)
  assert.equal(storedAfterManualUnsupported?.status, 'unsupported', '人工能力探测失败必须保存失败语义')
  assert.equal(storedAfterManualUnsupported?.remainingUsd, undefined, '人工能力探测失败不能继续展示旧金额')
  assertBalanceRetryDelay(database, manualRefresh.id, storedAfterManualUnsupported?.lastAttemptAt, 5)

  database.prepare(`
    UPDATE accounts
    SET status = 'disabled', schedulable = 0, balance_query_enabled = 1,
        balance_query_config_json = ?, balance_query_next_refresh_at = NULL
    WHERE id = ?
  `).run(recoveryConfig, disabled.id)
  assert.equal(await balanceRepository.findAccountBalanceRefreshCandidateAsync(disabled.id), undefined, '停用账户不得进入自动余额查询')
  const disabledManualCandidate = await balanceRepository.findAccountBalanceManualRefreshCandidateAsync(disabled.id)
  assert.ok(disabledManualCandidate, '停用的自有账户仍必须允许人工余额查询')
  const disabledManualResult = await balanceQueryService.refreshAccountBalanceCandidateWithOutcome(disabledManualCandidate, {
    mode: 'manual',
    query: async () => ({ status: 'fresh', remainingUsd: '4.440000', rawRemaining: '4.44', rawUnit: 'usd', basis: 'wallet' })
  })
  assert.equal(disabledManualResult.outcome, 'refreshed', '停用账户的人工余额查询必须成功落库')
  assert.equal(disabledManualResult.persisted, true, '停用账户的人工余额查询不得附加自动资格条件')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([disabled.id]).get(disabled.id)?.remainingUsd,
    '4.440000',
    '停用账户的人工余额查询必须保存最新快照'
  )
  assert.ok(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(disabled.id)?.balance_query_next_refresh_at,
    '停用账户的人工余额查询必须推进下一次刷新计划'
  )
  const manualSuccessCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(manualRefresh.id)
  assert.ok(manualSuccessCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(manualSuccessCandidate, {
    mode: 'manual',
    query: async () => ({ status: 'fresh', remainingUsd: '9.010000', rawRemaining: '9.01', rawUnit: 'usd', basis: 'wallet' })
  })
  const storedAfterManualSuccess = balanceRepository.loadAccountBalanceSnapshotsByAccountIds([manualRefresh.id]).get(manualRefresh.id)
  assert.ok(storedAfterManualSuccess)
  assert.equal(storedAfterManualSuccess.remainingUsd, '9.010000', '人工刷新成功必须保存新金额')
  assert.notEqual(
    database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(manualRefresh.id)?.balance_query_next_refresh_at,
    futureAt,
    '人工刷新成功必须推进自动刷新时间'
  )

  configure.run('active', builtinConfig, dueAt, gatewayActivity.id)
  const gatewayActivityCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(gatewayActivity.id)
  assert.ok(gatewayActivityCandidate)
  const gatewayActivityDueAt = gatewayActivityCandidate.nextRefreshAt
  const gatewayActivityAt = new Date().toISOString()
  const gatewayActivityResult = await balanceQueryService.refreshAccountBalanceCandidateWithOutcome(gatewayActivityCandidate, {
    query: async () => {
      database.prepare(`UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?`)
        .run(gatewayActivityAt, gatewayActivityAt, gatewayActivity.id)
      return { status: 'fresh', remainingUsd: '13.370000', rawRemaining: '13.37', rawUnit: 'usd', basis: 'wallet' }
    }
  })
  assert.equal(gatewayActivityResult.outcome, 'refreshed', '仅网关活动时间变化不能让自动余额结果误判 stale')
  assert.equal(gatewayActivityResult.persisted, true, '仅网关活动时间变化后的自动余额结果必须落库')
  const gatewayActivityRow = database.prepare(`
    SELECT last_used_at, balance_query_next_refresh_at
    FROM accounts WHERE id = ?
  `).get(gatewayActivity.id) as { last_used_at?: string | null; balance_query_next_refresh_at?: string | null }
  assert.equal(gatewayActivityRow.last_used_at, gatewayActivityAt, '网关活动时间更新必须保留')
  assert.ok(gatewayActivityRow.balance_query_next_refresh_at, '自动余额刷新必须写入非空下一次计划')
  assert.notEqual(gatewayActivityRow.balance_query_next_refresh_at, gatewayActivityDueAt, '自动余额刷新必须推进旧计划')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([gatewayActivity.id]).get(gatewayActivity.id)?.remainingUsd,
    '13.370000',
    '仅网关活动时间变化后的自动余额结果必须保存新快照'
  )

  configure.run('active', builtinConfig, dueAt, automaticEligibilityRevoked.id)
  const automaticEligibilityCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(automaticEligibilityRevoked.id)
  assert.ok(automaticEligibilityCandidate)
  const automaticEligibilityDueAt = automaticEligibilityCandidate.nextRefreshAt
  const automaticEligibilityRevision = automaticEligibilityCandidate.configRevision
  const automaticEligibilityResult = await balanceQueryService.refreshAccountBalanceCandidateWithOutcome(automaticEligibilityCandidate, {
    query: async () => {
      database.prepare(`
        UPDATE accounts
        SET status = 'disabled', schedulable = 0
        WHERE id = ?
      `).run(automaticEligibilityRevoked.id)
      return { status: 'fresh', remainingUsd: '15.150000', rawRemaining: '15.15', rawUnit: 'usd', basis: 'wallet' }
    }
  })
  assert.equal(automaticEligibilityResult.outcome, 'stale', '自动刷新在途失去资格时必须拒绝旧结果')
  assert.equal(automaticEligibilityResult.persisted, false, '自动刷新在途失去资格时不得持久化旧结果')
  const automaticEligibilityRow = database.prepare(`
    SELECT status, schedulable, config_revision, balance_query_next_refresh_at
    FROM accounts WHERE id = ?
  `).get(automaticEligibilityRevoked.id) as Record<string, unknown>
  assert.equal(automaticEligibilityRow.status, 'disabled')
  assert.equal(automaticEligibilityRow.schedulable, 0)
  assert.equal(automaticEligibilityRow.config_revision, automaticEligibilityRevision, '资格撤销回归不得依赖配置版本变化')
  assert.equal(automaticEligibilityRow.balance_query_next_refresh_at, automaticEligibilityDueAt, '资格撤销后必须保留原定刷新计划')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([automaticEligibilityRevoked.id]).has(automaticEligibilityRevoked.id),
    false,
    '资格撤销后不得写入在途查询的旧快照'
  )

  const staleCandidate = await balanceRepository.findAccountBalanceRefreshCandidateAsync(dueB.id)
  assert.ok(staleCandidate)
  await balanceQueryService.refreshAccountBalanceCandidate(staleCandidate, {
    query: async () => {
      const updated = repositories.updateAccount(dueB.id, {
        credentials: { api_keys: ['sk-due-b', 'sk-due-b-second'], base_url: 'https://relay.example/v1' }
      }, access)
      assert.ok(updated)
      assert.equal(updated.balanceQueryEnabled, false)
      return { status: 'fresh', remainingUsd: '12.340000', rawRemaining: '12.34', rawUnit: 'usd', basis: 'wallet' }
    }
  })
  const staleRow = database.prepare(`SELECT balance_query_enabled, balance_query_next_refresh_at, config_revision FROM accounts WHERE id = ?`).get(dueB.id) as Record<string, unknown>
  assert.equal(staleRow.balance_query_enabled, 0)
  assert.equal(staleRow.balance_query_next_refresh_at, null)
  assert.ok(Number(staleRow.config_revision) > staleCandidate.configRevision, '多 Key 保存必须递增配置版本，使在途查询结果失效')
  assert.equal(
    balanceRepository.loadAccountBalanceSnapshotsByAccountIds([dueB.id]).has(dueB.id),
    false,
    '查询期间关闭余额功能后，旧结果不得重新写入快照'
  )
} finally {
  if (untrustedStatusServer) {
    await new Promise<void>((resolveClose, rejectClose) => {
      untrustedStatusServer?.close((error) => error ? rejectClose(error) : resolveClose())
    })
  }
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('account balance refresh regression passed')

function assertBalanceRetryDelay(
  database: ReturnType<typeof databaseModule.getBusinessDatabase>,
  accountId: string,
  lastAttemptAt: string | undefined,
  expectedMinutes: number
): void {
  assert.ok(lastAttemptAt)
  const row = database.prepare(`SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`).get(accountId) as { balance_query_next_refresh_at?: string | null }
  assert.ok(row.balance_query_next_refresh_at)
  const delayMs = Date.parse(row.balance_query_next_refresh_at) - Date.parse(lastAttemptAt)
  const baselineMs = expectedMinutes * 60_000
  const windowMs = passiveScheduleJitterWindowMs(baselineMs)
  assert.ok(
    delayMs >= baselineMs - windowMs && delayMs <= baselineMs + windowMs && delayMs !== baselineMs,
    `余额临时失败应在全局偏移窗口内重试，实际 ${delayMs}ms，基准 ${baselineMs}ms，窗口 ±${windowMs}ms`
  )
}

function assertAccountDispatchState(
  database: ReturnType<typeof databaseModule.getBusinessDatabase>,
  accountId: string,
  expectedStatus: string,
  expectedSchedulable: number,
  phase: string
): void {
  const row = database.prepare(`SELECT status, schedulable FROM accounts WHERE id = ?`).get(accountId) as {
    status?: string
    schedulable?: number
  } | undefined
  assert.equal(row?.status, expectedStatus, `${phase}不得改变账户 status`)
  assert.equal(row?.schedulable, expectedSchedulable, `${phase}不得改变账户 schedulable`)
}

async function assertBalanceRefreshSchedulerSuccess(
  label: string,
  task: () => Promise<{ outcome: 'success' }>
): Promise<void> {
  const scheduler = new workerSchedulerModule.WorkerScheduler()
  scheduler.schedule({
    name: `account-balance-refresh-${label}`,
    intervalMs: 60_000,
    task
  })
  try {
    const deadline = Date.now() + 1_000
    let snapshot = scheduler.snapshots()[0]
    while (!snapshot || snapshot.successCount < 1) {
      if (Date.now() >= deadline) throw new Error(`${label} 未在预期时间内完成后台余额刷新`)
      await new Promise((resolve) => setTimeout(resolve, 5))
      snapshot = scheduler.snapshots()[0]
    }
    assert.equal(snapshot.failureCount, 0, `${label} 不得增加 scheduler failureCount`)
    assert.equal(snapshot.partialCount, 0, `${label} 不得增加 scheduler partialCount`)
    assert.equal(snapshot.lastOutcome, 'success', `${label} 必须在 scheduler 中完成为 success`)
  } finally {
    scheduler.stop()
  }
}

async function assertBalanceRefreshSchedulerFailure(
  label: string,
  task: () => Promise<{ outcome: 'success' }>
): Promise<void> {
  const scheduler = new workerSchedulerModule.WorkerScheduler()
  scheduler.schedule({
    name: `account-balance-refresh-${label}`,
    intervalMs: 60_000,
    task
  })
  try {
    const deadline = Date.now() + 1_000
    let snapshot = scheduler.snapshots()[0]
    while (!snapshot || snapshot.failureCount < 1) {
      if (Date.now() >= deadline) throw new Error(`${label} 未在预期时间内进入后台任务失败`)
      await new Promise((resolve) => setTimeout(resolve, 5))
      snapshot = scheduler.snapshots()[0]
    }
    assert.equal(snapshot.failureCount, 1, `${label} 必须增加 scheduler failureCount`)
    assert.equal(snapshot.partialCount, 0, `${label} 不得增加 scheduler partialCount`)
    assert.equal(snapshot.lastOutcome, 'failure', `${label} 必须在 scheduler 中完成为 failure`)
  } finally {
    scheduler.stop()
  }
}
