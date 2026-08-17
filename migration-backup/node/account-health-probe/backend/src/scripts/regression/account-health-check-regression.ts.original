import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { accountTestFailureEligibleForAccount } from '../../modules/accounts/account-test-failure-eligibility.js'
import { automaticAccountProbeOutcome } from '../../modules/accounts/automatic-account-probe-outcome.js'
import {
  accountHealthCheckProbeDeadlineMs,
  globalSharedQueueConcurrency
} from '../../modules/background/account-probe-limits.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-health-check-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-health-check.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-health-check-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, healthCheckRepository, healthCheckService, apiKeyRotation, poolCursorRepository, { handleDbServiceOperation }] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-health-check.repository.js'),
  import('../../modules/background/account-health-check.service.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../storage/account-api-key-pool-probe-cursor.repository.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const healthSettings = {
  intervalHours: 12,
  jitterMinutes: 0,
  failureThreshold: 3
}

try {
  const defaultHealthSettings = healthCheckRepository.normalizedHealthCheckSettings()
  assert.equal(defaultHealthSettings.intervalHours, 1, '默认健康检查基础间隔应为 1 小时')
  assert.equal(defaultHealthSettings.jitterMinutes, 10, '默认健康检查应按账户稳定错峰 0 到 10 分钟')
  const scheduleBaseAt = '2026-07-26T00:00:00.000Z'
  const staggeredSchedules = ['account-alpha', 'account-beta', 'account-gamma', 'account-delta'].map((accountId) => (
    healthCheckRepository.accountHealthSuccessSignalSchedule(accountId, scheduleBaseAt, defaultHealthSettings).nextHealthCheckAt
  ))
  const staggeredOffsets = staggeredSchedules.map((nextCheckAt) => Date.parse(nextCheckAt) - Date.parse(scheduleBaseAt))
  assert.ok(staggeredOffsets.every((offset) => offset >= 60 * 60_000 && offset < 70 * 60_000), '默认下次检查必须落在 1 小时后的 10 分钟错峰窗口内')
  assert.ok(new Set(staggeredOffsets).size > 1, '不同账户不能全部集中在同一个检查时间点')
  assert.equal(
    healthCheckRepository.accountHealthSuccessSignalSchedule('account-alpha', scheduleBaseAt, defaultHealthSettings).nextHealthCheckAt,
    staggeredSchedules[0],
    '同一账户的错峰偏移必须稳定，不能因重复计算改变'
  )

  const postgresFailureStartedAt = new Date('2026-07-16T20:13:55.032Z')
  assert.equal(
    healthCheckRepository.accountHealthCheckDatabaseDateTimeIso(postgresFailureStartedAt),
    postgresFailureStartedAt.toISOString(),
    'PostgreSQL timestamptz 返回 Date 时，健康检查首次失败时间必须归一化为 ISO 字符串'
  )
  assert.equal(
    healthCheckRepository.accountHealthCheckHasNewerSuccess(
      new Date('2026-07-16T20:13:56.000Z'),
      '2026-07-16T20:13:55.000Z'
    ),
    true,
    'PostgreSQL 健康成功时间返回 Date 时，旧探测保护必须按时间语义生效'
  )

  const repositorySource = readFileSync(resolve('src/storage/account-health-check.repository.ts'), 'utf8')
  const apiKeyRuntimeStateRepositorySource = readFileSync(resolve('src/storage/account-api-key-runtime-state.repository.ts'), 'utf8')
  const usageRepositorySource = readFileSync(resolve('src/storage/usage-records.repository.ts'), 'utf8')
  const serviceSource = readFileSync(resolve('src/modules/background/account-health-check.service.ts'), 'utf8')
  const postgresSuccessSource = sourceBetween(
    repositorySource,
    'export async function recordAccountHealthCheckSuccessAsync',
    'function healthCheckActivationStatus'
  )
  const postgresFailureSource = sourceBetween(
    repositorySource,
    'export async function recordAccountHealthCheckFailureAsync',
    'export function recordAccountHealthSuccessSignals'
  )
  assert.match(repositorySource, /SELECT status, config_revision, health_check_failure_count, health_check_failure_started_at, last_health_success_at[\s\S]+FOR UPDATE/, 'PostgreSQL 健康失败计数和首次失败窗口必须锁定账户行后更新')
  assert.match(repositorySource, /expectedConfigRevision[\s\S]+config_revision = \?/, '健康检查结果写入必须绑定账户配置版本')
  assert.doesNotMatch(postgresSuccessSource, /\(\? IS NULL OR/, 'PostgreSQL 健康成功写回不能使用无法推断参数类型的 NULL 守卫')
  assert.doesNotMatch(postgresFailureSource, /\(\? IS NULL OR/, 'PostgreSQL 健康失败写回不能使用无法推断参数类型的 NULL 守卫')
  assert.match(postgresSuccessSource, /schedulable = CASE WHEN status = 'pending_test' THEN 1[\s\S]+status IN \('pending_test', 'active'\)[\s\S]+AND \? = 1[\s\S]+balance_query_enabled = 0/, 'PostgreSQL 健康成功必须为首次创建的待检查或直接启用账户按 Node INTEGER 写入余额探测意图')
  assert.match(postgresSuccessSource, /const scheduleBalanceAutoDetection = input\.scheduleBalanceAutoDetection === true \? 1 : 0/, 'PostgreSQL 首次余额探测开关必须绑定 INTEGER 0/1 参数')
  assert.match(repositorySource, /group_accounts\.enabled = 1[\s\S]+accounts\.schedulable = 1/, 'PostgreSQL 健康检查候选必须匹配 Node INTEGER 分组和可调度字段')
  assert.match(repositorySource, /CASE WHEN accounts\.status = 'pending_test' THEN 0 ELSE 1 END/, '周期兜底应优先处理待检查账户')
  assert.equal(
    repositorySource.match(/accounts\.status = 'pending_test' AND accounts\.last_health_check_at IS NULL/g)?.length,
    2,
    'SQLite 和 PostgreSQL 候选查询都应让从未检查的待检查账户忽略遗留复检时间'
  )
  assert.match(serviceSource, /queuedConfigRevision[\s\S]+currentConfigRevision/, '健康检查队列执行前必须丢弃旧配置版本任务')
  assert.match(serviceSource, /healthCheckGuard/, '达到阈值后的保护状态写入必须携带健康失败快照')
  assert.match(serviceSource, /errorLogFields\(event\.error/, '健康检查队列耗尽日志必须保留真实异常')
  assert.match(serviceSource, /probeAccountHealthCheckApiKeyPool/, '多 Key 健康检查必须通过固定 Key 聚合探测')
  assert.match(serviceSource, /record_account_health_check_success[\s\S]{0,900}const changed[\s\S]{0,400}record_account_api_key_success[\s\S]{0,700}trafficSource: 'account_health_check'[\s\S]{0,400}observedAt[\s\S]{0,300}expectedAccountConfigRevision: item\.configRevision/, '账户健康成功 CAS 后必须以 winner 身份经 DB service 回写同代次 Key 成功')
  assert.match(apiKeyRuntimeStateRepositorySource, /status <> 'disabled'/, 'Key 成功写入必须拒绝 disabled Key，但允许其他当前运行态恢复')
  assert.equal(accountHealthCheckProbeDeadlineMs, 65_000, '单账户健康检查必须在 10/20/30 秒统一诊断阶梯外预留调度余量')
  assert.equal(globalSharedQueueConcurrency, runtimeConfig.concurrency.globalMax, '健康队列必须使用进程级共享并发上限')
  const accountProbeJobsSource = readFileSync(resolve('src/modules/background/account-probe-jobs.ts'), 'utf8')
  assert.match(accountProbeJobsSource, /accountHealthCheckScanLimit\(batchSize, queueConcurrency, queueBeforeScan\)/, '健康检查候选扫描必须扣除已有队列占用')
  assert.match(serviceSource, /account_api_key_pool_probe_cursor/, 'Key 池续扫必须使用持久化游标而不是进程内 TTL 状态')
  assert.match(serviceSource, /keySetFingerprint[\s\S]+configRevision/, 'Key 池游标必须绑定 Key 集合和账户配置版本')
  assert.match(serviceSource, /accountHealthCheckDeadline\(\)/, '健康检查必须创建账户级 deadline 信号')
  assert.match(serviceSource, /runAccountApiKeyPoolDiagnostic\(candidate, entries/, '多 Key 健康检查必须复用统一 API Key 池诊断器')
  assert.match(serviceSource, /attempt\.signal, attempt\.timeoutMs/, '每把 Key 的诊断必须继承账户级 deadline 和统一超时档位')
  assert.match(serviceSource, /lastCompletedFingerprint/, '健康检查游标只能记录实际完成的连续 Key')
  assert.match(serviceSource, /const diagnosticTimeoutTemporaryUnavailable = diagnosticTimeoutExhausted === true[\s\S]+const scheduledProbeFailureImmediate = item\.reason === 'scheduled'[\s\S]+diagnosticCompleted === true[\s\S]+probeOutcome !== 'complete_success'[\s\S]+probeOutcome !== 'probe_task_failure'/, 'scheduled 完整诊断未通过必须立即标记临时不可调用，未知任务结论除外')
  assert.match(serviceSource, /diagnosticCompleted: poolCompleted && !signal\.aborted/, 'Key 池健康检查必须以完整池诊断完成事实驱动立即标记')
  assert.match(serviceSource, /diagnosticCompleted: timeoutMs === undefined && signal\?\.aborted !== true/, '单 Key 健康检查必须在完整诊断阶梯未被 deadline 中止时确认完成')
  assert.doesNotMatch(serviceSource, /const immediateTemporaryUnavailable = diagnosticTimeoutExhausted === true/, '立即临时不可调用不得再仅由诊断超时事实触发')
  assert.match(serviceSource, /poolDiagnosticErrors\.length > 0[\s\S]+throw new AggregateError/, '池内任意调用异常必须阻止健康状态写入')
  assert.match(serviceSource, /const sourceOnlyProbe = completedExecution\?\.ordinaryAccountHealthSemantics === false/, '只有 source-only 任务可跳过普通账户健康结算；fence 数量本身不得降格普通任务')
  assert.match(serviceSource, /if \(sourceOnlyProbe\) \{[\s\S]{0,300}return true[\s\S]{0,600}record_account_health_check_success/, 'source-only success 必须在普通 success 状态写入前窄结算')
  assert.match(serviceSource, /settleCompletedExecutionSourceFences\(probeSettled \? 'success' : 'stale'\)[\s\S]{0,2400}sendAccountRuntimeClearToServer/, '普通 health success 附着 source fence 时仍须保留既有账户运行态结算')
  assert.match(serviceSource, /completedExecution = execution[\s\S]{0,1200}takeAccountHealthCheckExecutionSourceFences[\s\S]{0,1200}await availabilityProbeSourceFences/, 'worker completion 必须保留当前执行记录并仅取走本轮 source fence，避免晚到 fence 消费旧结果')
  assert.match(serviceSource, /!accountHealthCheckQueue\.hasFollowUp\(item\.accountId\)[\s\S]{0,300}accountHealthCheckExecutions\.delete/, 'worker 只有在没有尾随任务时才可删除执行记录')
  assert.match(serviceSource, /function settleSourceFences\([\s\S]{0,300}!accountHealthCheckQueue\.hasFollowUp\(executionKey\)[\s\S]{0,180}accountHealthCheckExecutions\.delete/, 'stale/config 早退必须保留带尾随任务的 execution，避免晚到 source fence 降格')
  assert.match(serviceSource, /sendCodexSourceFenceSettledToServer/, 'source fence outcome 必须显式回传 gateway 进程执行精确清理')
  assert.match(serviceSource, /if \(poolCompleted && !signal\.aborted\)/, '空 Key 池完成轮次也必须删除旧游标')
  assert.match(repositorySource, /pendingHealthCheckRetryIntervalMs = 60 \* 60_000/, '待检查账户失败后必须固定每 1 小时复检')
  assert.match(repositorySource, /pendingHealthCheckFailureTimeoutMs = 24 \* 60 \* 60_000/, '待检查账户必须从首次失败起 24 小时收敛为异常')
  assert.match(repositorySource, /account_activation_check_timeout/, '待检查超时必须写入明确异常码')
  assert.match(repositorySource, /function availabilityScheduleJsonValue/, '健康检查必须兼容 PostgreSQL JSONB 返回对象')
  assert.match(repositorySource, /JSON\.stringify\(value\)/, 'PostgreSQL JSONB 时间计划必须规范化为现有解析器使用的 JSON 文本')
  assert.match(usageRepositorySource, /accountHealthSuccessSignalSchedule\(accountId, successAt, healthCheckSettings \?\? \{\}\)/, 'PostgreSQL 真实成功请求必须复用健康检测间隔与 jitter 计划')
  assert.match(usageRepositorySource, /SET last_health_success_at = \?,[\s\S]+next_health_check_at = \?/, 'PostgreSQL 真实成功请求必须同时顺延下次健康复核')
  assert.match(usageRepositorySource, /next_health_check_at < \?[\s\S]+next_health_check_at > \?/, 'PostgreSQL 真实成功请求应与 SQLite 一致节流，避免每请求重写账户行')
  assert.equal((repositorySource.match(/AND status = 'active'/g) ?? []).length >= 2, true, 'SQLite 和通用 PostgreSQL 成功信号写回应只更新 active 账户')
  assert.match(usageRepositorySource, /WHERE id = \?[\s\S]+AND status = 'active'/, 'PostgreSQL usage 成功信号不得覆盖待检查状态')
  assert.equal(
    (repositorySource.match(/row\.status !== 'pending_test' && recentSuccessAt/g) ?? []).length,
    2,
    'SQLite 和 PostgreSQL 都必须让修改配置后的待检查账户忽略旧成功信号并立即进入检查候选'
  )

  const database = databaseModule.getBusinessDatabase()
  const healthyPoolKey = 'sk-health-pool-key-healthy'
  const disabledPoolKey = 'sk-health-pool-key-disabled'
  const recoveredPoolKey = 'sk-health-pool-key-recovered'
  const poolCandidate = {
    id: 'account-health-pool-probe',
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    type: 'api_key',
    apiKey: healthyPoolKey,
    apiKeys: [healthyPoolKey, disabledPoolKey, recoveredPoolKey],
    credentials: {
      api_key: healthyPoolKey,
      api_keys: [healthyPoolKey, disabledPoolKey, recoveredPoolKey],
      base_url: 'https://api.openai.com/v1'
    },
    apiKeyRuntimeStates: [{
      keyFingerprint: apiKeyRotation.fingerprintAccountApiKey(disabledPoolKey),
      status: 'disabled'
    }]
  } as unknown as import('../../storage/openai-account-selector.types.js').OpenAIAccountSecret
  const testedPoolKeys: string[] = []
  const poolProbeResult = await healthCheckService.probeAccountHealthCheckApiKeyPool(poolCandidate, async (fixedCandidate) => {
    testedPoolKeys.push(fixedCandidate.apiKey)
    const success = fixedCandidate.apiKey === recoveredPoolKey
    return {
      result: {
        accountId: poolCandidate.id,
        accountName: '多 Key 健康检查聚合',
        providerCode: 'gpt',
        type: 'api_key',
        success,
        statusCode: success ? 200 : 401,
        errorCode: success ? undefined : 'invalid_api_key',
        message: success ? 'ok' : 'invalid api key',
        accountFailureEligible: !success
      }
    }
  })
  assert(poolProbeResult, '多 Key 账户应生成至少一个固定 Key 探测结果')
  assert.equal(poolProbeResult.result.success, true, '任一固定 Key 探测成功即应判定账户健康检查成功')
  assert.deepEqual(
    poolProbeResult.apiKeyPoolWinner,
    {
      fingerprint: apiKeyRotation.fingerprintAccountApiKey(recoveredPoolKey),
      index: 2
    },
    '多 Key 健康检查成功必须保留 winner Key 身份，供同代次状态写回激活待验证 Key'
  )
  assert.deepEqual(
    testedPoolKeys,
    [healthyPoolKey, recoveredPoolKey],
    '健康检查必须按固定 Key 顺序探测，跳过已停用 Key，并在首个成功后停止'
  )
  const deadlineController = new AbortController()
  const deadlinePoolCandidate = {
    ...poolCandidate,
    apiKey: 'sk-health-deadline-0',
    apiKeys: Array.from({ length: 50 }, (_value, index) => `sk-health-deadline-${index}`),
    credentials: {
      ...poolCandidate.credentials,
      api_key: 'sk-health-deadline-0',
      api_keys: Array.from({ length: 50 }, (_value, index) => `sk-health-deadline-${index}`)
    },
    apiKeyRuntimeStates: []
  } as unknown as import('../../storage/openai-account-selector.types.js').OpenAIAccountSecret
  const deadlineProbedKeys: string[] = []
  const deadlinePoolResult = await healthCheckService.probeAccountHealthCheckApiKeyPool(deadlinePoolCandidate, async (fixedCandidate) => {
    deadlineProbedKeys.push(fixedCandidate.apiKey)
    deadlineController.abort(new Error('health probe deadline'))
    return {
      result: {
        accountId: deadlinePoolCandidate.id,
        accountName: '多 Key 健康检查 deadline',
        providerCode: 'gpt',
        type: 'api_key',
        success: false,
        errorCode: 'server_diagnostic_timeout',
        message: '账户测试超时',
        accountFailureEligible: true
      }
    }
  }, {
    signal: deadlineController.signal,
    abortedResult: () => ({
      result: {
        accountId: deadlinePoolCandidate.id,
        accountName: '多 Key 健康检查 deadline',
        providerCode: 'gpt',
        type: 'api_key',
        success: false,
        errorCode: 'server_diagnostic_cancelled',
        message: '账户健康检查已达到总时限',
        accountFailureEligible: false
      },
      diagnosticCanceled: true,
      diagnosticTimeoutExhausted: false,
      diagnosticDeadlineExceeded: true
    })
  })
  assert.equal(deadlineProbedKeys.length, 1, '账户 deadline 到达后不得继续扫描剩余 49 把 Key')
  assert.equal(deadlinePoolResult?.diagnosticDeadlineExceeded, true, '账户 deadline 必须以独立事实返回')
  assert.equal(automaticAccountProbeOutcome(deadlinePoolResult!.result, {
    canceled: deadlinePoolResult?.diagnosticCanceled,
    timeout: deadlinePoolResult?.diagnosticTimeoutExhausted,
    diagnosticTimeoutExhausted: deadlinePoolResult?.diagnosticTimeoutExhausted
  }), 'probe_task_failure', '账户 deadline 不得升级为账户可用性失败')
  const preAbortedController = new AbortController()
  preAbortedController.abort(new Error('health probe deadline'))
  let preAbortedProbeCalls = 0
  const preAbortedPoolResult = await healthCheckService.probeAccountHealthCheckApiKeyPool(deadlinePoolCandidate, async () => {
    preAbortedProbeCalls += 1
    return deadlinePoolResult!
  }, {
    signal: preAbortedController.signal,
    abortedResult: () => ({
      result: {
        accountId: deadlinePoolCandidate.id,
        accountName: '多 Key 健康检查已取消',
        providerCode: 'gpt',
        type: 'api_key',
        success: false,
        errorCode: 'server_diagnostic_cancelled',
        message: '账户健康检查已达到总时限',
        accountFailureEligible: false
      },
      diagnosticCanceled: true,
      diagnosticTimeoutExhausted: false,
      diagnosticDeadlineExceeded: true
    })
  })
  assert.equal(preAbortedProbeCalls, 0, '在获取诊断槽期间到达 deadline 后不得启动任何 API Key 探测')
  assert.equal(preAbortedPoolResult?.diagnosticDeadlineExceeded, true, '预先取消的 Key 池必须返回账户级 deadline 结果')
  const rotatingKeys = ['sk-health-rotate-0', 'sk-health-rotate-1', 'sk-health-rotate-2']
  const rotatingCandidate = {
    ...poolCandidate,
    apiKey: rotatingKeys[0],
    apiKeys: rotatingKeys,
    credentials: {
      ...poolCandidate.credentials,
      api_key: rotatingKeys[0],
      api_keys: rotatingKeys
    },
    apiKeyRuntimeStates: []
  } as unknown as import('../../storage/openai-account-selector.types.js').OpenAIAccountSecret
  const rotatingProbedKeys: string[] = []
  await healthCheckService.probeAccountHealthCheckApiKeyPool(rotatingCandidate, async (fixedCandidate) => {
    rotatingProbedKeys.push(fixedCandidate.apiKey)
    return {
      result: {
        accountId: rotatingCandidate.id,
        accountName: '多 Key 健康检查续扫',
        providerCode: 'gpt',
        type: 'api_key',
        success: false,
        message: '模拟失败',
        accountFailureEligible: true
      }
    }
  }, { startAfterFingerprint: apiKeyRotation.fingerprintAccountApiKey(rotatingKeys[1]) })
  assert.deepEqual(rotatingProbedKeys, [rotatingKeys[2], rotatingKeys[0], rotatingKeys[1]], '下一轮健康检查必须从上轮最后 Key 的后一个开始续扫')
  const accountColumns = database.prepare('PRAGMA table_info(accounts)').all() as unknown as Array<{ name: string }>
  for (const column of [
    'last_health_check_at',
    'next_health_check_at',
    'last_health_success_at',
    'health_check_failure_count',
    'health_check_failure_started_at',
    'last_health_check_status_code',
    'last_health_check_error_code',
    'last_health_check_error_message',
    'last_error_trace_id',
    'last_health_check_trace_id'
  ]) {
    assert.ok(accountColumns.some((row) => row.name === column), `accounts 应包含 ${column} 字段`)
  }
  for (const [statusCode, errorCode, message] of [
    [400, 'invalid_request_error', 'Unsupported model'],
    [401, 'invalid_api_key', 'Invalid API key'],
    [404, 'model_not_found', 'The model does not exist'],
    [429, 'rate_limit', 'Provider-defined throttling'],
    [500, 'server_error', 'Provider-defined failure'],
    [503, 'upstream_body_interrupted', 'Provider-controlled diagnostic']
  ] as const) {
    assert.equal(accountTestFailureEligibleForAccount({ statusCode, errorCode, message }), true, `HTTP ${statusCode} 的供应商语义不得改变诊断重试资格`)
  }

  const group = repositories.createGroup({
    name: '账号健康检测回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const anthropicGroup = repositories.createGroup({
    name: 'Anthropic 健康检测回归分组',
    providerCode: 'anthropic',
    enabled: true
  }, access)
  const geminiGroup = repositories.createGroup({
    name: 'Gemini 健康检测回归分组',
    providerCode: 'gemini',
    enabled: true
  }, access)
  const dueAccount = createActiveAccount(repositories, group.id, '健康检测到期账号', 'sk-health-due')
  poolCursorRepository.saveAccountApiKeyPoolProbeCursor({
    accountId: dueAccount.id,
    purpose: 'health_check',
    lastCompletedKeyFingerprint: 'key-a',
    keySetFingerprint: 'set-a',
    configRevision: dueAccount.configRevision ?? 1
  })
  poolCursorRepository.saveAccountApiKeyPoolProbeCursor({
    accountId: dueAccount.id,
    purpose: 'health_check',
    lastCompletedKeyFingerprint: 'key-b',
    keySetFingerprint: 'set-a',
    configRevision: dueAccount.configRevision ?? 1
  })
  assert.equal(
    poolCursorRepository.findAccountApiKeyPoolProbeCursor(dueAccount.id, 'health_check')?.lastCompletedKeyFingerprint,
    'key-b',
    'Key 池游标必须按账户和用途覆盖保存最后连续完成的 Key'
  )
  poolCursorRepository.deleteAccountApiKeyPoolProbeCursor(dueAccount.id, 'health_check')
  assert.equal(
    poolCursorRepository.findAccountApiKeyPoolProbeCursor(dueAccount.id, 'health_check'),
    undefined,
    '完整轮次删除游标后下一轮必须从 Key 池首部开始'
  )
  const recentAccount = createActiveAccount(repositories, group.id, '健康检测近期成功账号', 'sk-health-recent')
  const disabledAccount = createActiveAccount(repositories, group.id, '健康检测停用账号', 'sk-health-disabled')
  const runtimeFailureTraceId = 'trace-runtime-failure-regression'
  assert(repositories.markAccountTemporaryUnavailable(dueAccount.id, '上游运行态失败', undefined, runtimeFailureTraceId), '运行态失败应写入冷却状态')
  assert.equal(repositories.findAccountSummary(dueAccount.id, access)?.lastErrorTraceId, runtimeFailureTraceId, '运行态错误提示必须保留对应 traceId')
  repositories.clearAccountFailureStateResult(dueAccount.id, access, { allowErrorRestore: false })
  assert.equal(repositories.findAccountSummary(dueAccount.id, access)?.lastErrorTraceId, undefined, '恢复账户时必须同步清理旧错误 traceId')
  repositories.updateAccount(disabledAccount.id, { status: 'disabled' }, access)
  const futureScheduledAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '健康检测未来时间计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-future-schedule',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5'],
    availabilitySchedule: {
      enabled: true,
      timezone: 'UTC',
      mode: 'allow_windows',
      dateRange: { startDate: '2999-01-01' },
      windows: [{ daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' }]
    }
  }, access)
  assert.equal(repositories.recordAccountHealthCheckSuccess(futureScheduledAccount.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '未来时间计划账户的后台健康检查应能写入成功事实')
  assert.equal(
    repositories.findAccountSummary(futureScheduledAccount.id, access)?.status,
    'disabled',
    '后台健康检查成功不得绕过账户时间计划激活账户'
  )

  const oldCheckAt = new Date(Date.now() - 13 * 60 * 60_000).toISOString()
  const recentSuccessAt = new Date(Date.now() - 30 * 60_000).toISOString()
  database.prepare(`
    UPDATE accounts
    SET last_health_check_at = ?,
        next_health_check_at = ?,
        last_health_success_at = NULL,
        health_check_failure_count = 0
    WHERE id = ?
  `).run(oldCheckAt, oldCheckAt, dueAccount.id)
  database.prepare(`
    UPDATE accounts
    SET next_health_check_at = ?,
        last_health_success_at = ?,
        health_check_failure_count = 2,
        last_health_check_error_code = 'account_health_check_failed',
        last_health_check_error_message = '旧失败'
    WHERE id = ?
  `).run(oldCheckAt, recentSuccessAt, recentAccount.id)

  const pendingFirstCheckAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '待检查账户不应继承未来复检时间',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-pending-first-check',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5']
  }, access)
  database.prepare(`
    UPDATE accounts
    SET next_health_check_at = ?,
        last_health_check_at = NULL,
        last_health_success_at = ?
    WHERE id = ?
  `).run(
    new Date(Date.now() + 12 * 60 * 60_000).toISOString(),
    new Date().toISOString(),
    pendingFirstCheckAccount.id
  )

  const anthropicPendingAccount = repositories.createAccount({
    providerCode: 'anthropic',
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'Anthropic 待检查候选账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-anthropic-pending',
      base_url: 'https://api.anthropic.com/v1'
    },
    groupId: anthropicGroup.id,
    supportedModels: ['claude-sonnet-4-5'],
    healthCheckModel: 'claude-sonnet-4-5',
    healthCheckEndpointMode: 'messages_sse'
  }, access)
  const geminiPendingAccount = repositories.createAccount({
    providerCode: 'gemini',
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    name: 'Gemini 待检查候选账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-gemini-pending',
      base_url: 'https://generativelanguage.googleapis.com/v1beta'
    },
    groupId: geminiGroup.id,
    supportedModels: ['gemini-2.5-flash'],
    healthCheckModel: 'gemini-2.5-flash',
    healthCheckEndpointMode: 'generate_content_sse'
  }, access)

  const due = repositories.listAccountsDueForHealthCheck({ limit: 10, ...healthSettings })
  const dueIds = due.map((account) => account.id)
  for (const pendingId of [pendingFirstCheckAccount.id, anthropicPendingAccount.id, geminiPendingAccount.id]) {
    assert.ok(dueIds.includes(pendingId), '八种生成协议的待检查账户都必须进入常规健康检查候选')
    assert.ok(dueIds.indexOf(pendingId) < dueIds.indexOf(dueAccount.id), '待检查账户必须优先于到期 active 账户')
  }
  assert.equal(repositories.findAccountForHealthCheck(anthropicPendingAccount.id)?.id, anthropicPendingAccount.id, 'Anthropic Messages 待检查账户不得被 OpenAI profile 限制')
  assert.equal(repositories.findAccountForHealthCheck(geminiPendingAccount.id)?.id, geminiPendingAccount.id, 'Gemini GenerateContent 待检查账户不得被 OpenAI profile 限制')

  const recentAfterSkip = repositories.findAccountSummary(recentAccount.id, access)
  assert.equal(recentAfterSkip?.healthCheckFailureCount, 0, '近期成功信号应清理健康检测失败计数')
  assert.equal(recentAfterSkip?.lastHealthCheckErrorCode, undefined, '近期成功信号应清理健康检测错误码')
  assert.ok(recentAfterSkip?.nextHealthCheckAt, '近期成功信号应顺延下次检测时间')
  assert.ok(Date.parse(recentAfterSkip?.nextHealthCheckAt ?? '') > Date.now() + 11 * 60 * 60_000, '顺延后的下次检测应接近 12 小时后')

  const successAt = new Date().toISOString()
  repositories.recordAccountHealthCheckSuccess(dueAccount.id, {
    ...healthSettings,
    checkedAt: successAt,
    statusCode: 200,
    traceId: 'trace-health-success-regression'
  })
  const dueAfterSuccess = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(dueAfterSuccess?.lastHealthCheckAt, successAt, '健康检测成功应写入检测时间')
  assert.equal(dueAfterSuccess?.lastHealthSuccessAt, successAt, '健康检测成功应写入成功时间')
  assert.equal(dueAfterSuccess?.healthCheckFailureCount, 0, '健康检测成功应清零失败计数')
  assert.equal(dueAfterSuccess?.lastHealthCheckStatusCode, 200, '健康检测成功应记录 HTTP 状态码')
  assert.equal(dueAfterSuccess?.lastHealthCheckTraceId, 'trace-health-success-regression', '健康检测成功应记录结构化 traceId')
  assert.equal(
    repositories.findAccountForHealthCheck(dueAccount.id),
    undefined,
    '下次检查时间未到时，周期健康检查仍应跳过账户'
  )
  assert.equal(
    repositories.findAccountForHealthCheck(dueAccount.id, { ignoreSchedule: true })?.id,
    dueAccount.id,
    '真实请求失败触发的独立检查必须绕过周期到期门槛'
  )

  const staleProbeAccount = createActiveAccount(repositories, group.id, '健康检测配置版本账号', 'sk-health-revision')
  const staleProbeBefore = repositories.findAccountSummary(staleProbeAccount.id, access)
  const staleProbeRevision = staleProbeBefore?.configRevision
  assert.ok(staleProbeRevision, '健康检测候选应包含配置版本')
  repositories.updateAccount(staleProbeAccount.id, { notes: '探测期间配置已变化' }, access)
  const staleSuccessChanged = repositories.recordAccountHealthCheckSuccess(staleProbeAccount.id, {
    ...healthSettings,
    statusCode: 200,
    expectedConfigRevision: staleProbeRevision
  })
  assert.equal(staleSuccessChanged, false, '旧配置版本的健康检查成功不得写入新配置')
  assert.equal(
    repositories.findAccountSummary(staleProbeAccount.id, access)?.lastHealthCheckAt,
    staleProbeBefore?.lastHealthCheckAt,
    '旧配置版本的健康检查不得留下成功诊断'
  )

  const newerTrafficSuccessAt = new Date().toISOString()
  database.prepare(`
    UPDATE accounts
    SET last_health_success_at = ?,
        health_check_failure_count = 0
    WHERE id = ?
  `).run(newerTrafficSuccessAt, staleProbeAccount.id)
  const staleFailure = repositories.recordAccountHealthCheckFailure(staleProbeAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '旧探测失败',
    expectedConfigRevision: repositories.findAccountSummary(staleProbeAccount.id, access)?.configRevision,
    observedAt: new Date(Date.parse(newerTrafficSuccessAt) - 1_000).toISOString()
  })
  assert.equal(staleFailure.changed, false, '较新的真实请求成功后，旧健康检查失败不得覆盖成功信号')
  assert.equal(staleFailure.failureCount, 0, '旧健康检查失败不得重新累加已被真实成功清零的计数')

  const configurationFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 404,
    errorCode: 'model_not_found',
    errorMessage: '检查模型不存在',
    countTowardsThreshold: false
  })
  assert.equal(configurationFailure.failureCount, 0, '检查模型或请求配置错误不应累计账户连续失败次数')
  assert.equal(configurationFailure.reachedThreshold, false, '配置错误不得触发账户临时不可用阈值')
  const afterConfigurationFailure = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(afterConfigurationFailure?.lastHealthCheckErrorCode, 'model_not_found', '配置错误仍应保留健康检查诊断')
  assert.equal(afterConfigurationFailure?.lastHealthCheckTraceId, undefined, '最新检查没有 traceId 时必须清理旧探针 trace，不能与新错误摘要错配')
  assert.ok(afterConfigurationFailure?.nextHealthCheckAt, '配置错误仍应安排后台复检')

  const messageOnlyAccount = createActiveAccount(repositories, group.id, '健康检测结构化错误账号', 'sk-health-structured-error')
  const structuredFailure = repositories.recordAccountHealthCheckFailure(messageOnlyAccount.id, {
    ...healthSettings,
    statusCode: 520,
    errorCode: 'upstream_retryable_error',
    errorMessage: '上游请求失败'
  })
  assert.equal(structuredFailure.changed, true, '结构化错误字段测试账号应写入健康检查失败')
  const structuredFailureAccount = repositories.findAccountSummary(messageOnlyAccount.id, access)
  assert.equal(structuredFailureAccount?.lastHealthCheckStatusCode, 520, '健康检查失败应独立保留 HTTP 状态码')
  assert.equal(structuredFailureAccount?.lastHealthCheckErrorCode, 'upstream_retryable_error', '健康检查失败应独立保留错误码')
  assert.equal(structuredFailureAccount?.lastHealthCheckErrorMessage, '上游请求失败', '健康检查错误摘要不应重复拼接状态码和错误码')
  assert.doesNotMatch(structuredFailureAccount?.lastHealthCheckErrorMessage ?? '', /HTTP 520|upstream_retryable_error/, '健康检查错误摘要不得重复结构化字段')

  const firstFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败',
    traceId: 'trace-health-failure-regression'
  })
  assert.equal(firstFailure.failureCount, 1, '第一次失败应记录连续失败 1 次')
  assert.equal(firstFailure.reachedThreshold, false, '第一次失败不应达到阈值')
  const secondFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败'
  })
  assert.equal(repositories.findAccountSummary(dueAccount.id, access)?.lastHealthCheckTraceId, undefined, '后续无 trace 探针应覆盖清理前一次失败 trace')
  assert.equal(secondFailure.failureCount, 2, '第二次失败应递增连续失败次数')
  const thirdFailure = repositories.recordAccountHealthCheckFailure(dueAccount.id, {
    ...healthSettings,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟失败',
    traceId: 'trace-health-failure-latest'
  })
  assert.equal(thirdFailure.failureCount, 3, '第三次失败应递增到阈值')
  assert.equal(thirdFailure.reachedThreshold, true, '第三次失败应达到阈值')
  const dueAfterFailure = repositories.findAccountSummary(dueAccount.id, access)
  assert.equal(dueAfterFailure?.lastHealthCheckStatusCode, 401, '失败应记录最近 HTTP 状态码')
  assert.equal(dueAfterFailure?.lastHealthCheckErrorCode, 'invalid_api_key', '失败应记录错误码')
  assert.match(dueAfterFailure?.lastHealthCheckErrorMessage ?? '', /模拟失败/, '失败应记录错误摘要')
  assert.equal(dueAfterFailure?.lastHealthCheckTraceId, 'trace-health-failure-latest', '健康检测失败应记录最新探针的结构化 traceId')
  assert.ok(dueAfterFailure?.nextHealthCheckAt, '失败应写入短退避复检时间')
  const guardedAccount = repositories.findAccountSummary(dueAccount.id, access)
  assert.ok(guardedAccount?.configRevision, '达到阈值的账户应包含配置版本')
  const staleGuardUpdate = repositories.markAccountTestTemporaryUnavailable(
    guardedAccount,
    '错误版本不应改变状态',
    access,
    {
      configRevision: (guardedAccount.configRevision ?? 1) + 1,
      checkedAt: thirdFailure.checkedAt,
      failureCount: thirdFailure.failureCount,
      observedAt: thirdFailure.checkedAt
    }
  )
  assert.equal(staleGuardUpdate, undefined, '健康检查临时不可用写入必须校验配置版本和失败快照')
  const guardedUpdate = repositories.markAccountTestTemporaryUnavailable(
    guardedAccount,
    '达到阈值后标记临时不可调用',
    access,
    {
      configRevision: guardedAccount.configRevision ?? 1,
      checkedAt: thirdFailure.checkedAt,
      failureCount: thirdFailure.failureCount,
      observedAt: thirdFailure.checkedAt
    }
  )
  assert.equal(guardedUpdate?.status, 'temporary_unavailable', '匹配健康检查快照时才允许标记临时不可调用')

  const trafficAccount = createActiveAccount(repositories, group.id, '健康检测真实流量账号', 'sk-health-traffic')
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-success',
    trafficSource: 'gateway',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10
  }])
  const trafficAfterSuccess = repositories.findAccountSummary(trafficAccount.id, access)
  assert.ok(trafficAfterSuccess?.lastHealthSuccessAt, '真实成功请求应刷新健康成功信号')
  assert.ok(trafficAfterSuccess?.nextHealthCheckAt, '真实成功请求应顺延下次健康检测')
  const trafficFirstHealthSuccessAt = trafficAfterSuccess?.lastHealthSuccessAt
  const trafficFirstNextHealthCheckAt = trafficAfterSuccess?.nextHealthCheckAt
  assert.ok(trafficFirstHealthSuccessAt, '真实成功请求应写入健康成功时间')
  assert.ok(trafficFirstNextHealthCheckAt, '真实成功请求应写入下次健康检测时间')

  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-success-throttled',
    trafficSource: 'gateway',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10,
    createdAt: new Date(Date.parse(trafficFirstHealthSuccessAt) + 60_000).toISOString()
  }])
  const trafficAfterEarlySuccess = repositories.findAccountSummary(trafficAccount.id, access)
  assert.equal(trafficAfterEarlySuccess?.lastHealthSuccessAt, trafficFirstHealthSuccessAt, '检测窗口未过半时不应为每次成功请求重写健康成功信号')
  assert.equal(trafficAfterEarlySuccess?.nextHealthCheckAt, trafficFirstNextHealthCheckAt, '检测窗口未过半时不应为每次成功请求重写下次健康检测')

  const credentialChangedAccount = createActiveAccount(repositories, group.id, '健康检测修改 Key 竞态账号', 'sk-health-key-before')
  repositories.updateAccount(credentialChangedAccount.id, {
    credentials: { api_key: 'sk-health-key-after', base_url: 'https://api.openai.com/v1' }
  }, access)
  const pendingAfterCredentialChange = repositories.findAccountSummary(credentialChangedAccount.id, access)
  assert.equal(pendingAfterCredentialChange?.status, 'pending_test', '修改 Key 后账户必须进入待检查')
  assert.equal(pendingAfterCredentialChange?.nextHealthCheckAt, undefined, '修改 Key 后应立即等待后台检查')
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin', traceId: 'trace-old-inflight-success-after-key-change', trafficSource: 'gateway',
    accountId: credentialChangedAccount.id, groupId: group.id, providerCode: 'gpt', endpoint: '/v1/responses', model: 'gpt-test',
    stream: false, success: true, statusCode: 200, durationMs: 10,
    createdAt: new Date(Date.now() + 60_000).toISOString()
  }])
  const pendingAfterInflightSuccess = repositories.findAccountSummary(credentialChangedAccount.id, access)
  assert.equal(pendingAfterInflightSuccess?.status, 'pending_test', '旧在途成功请求不得激活新配置')
  assert.equal(pendingAfterInflightSuccess?.nextHealthCheckAt, undefined, '旧在途成功请求不得把待检查推迟到正常周期')
  assert(repositories.listAccountsDueForHealthCheck({ limit: 100, ...healthSettings }).some((item) => item.id === credentialChangedAccount.id), '修改 Key 的待检查账户应立即进入健康检查候选')

  const legacyMultiKeyAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '健康检测旧入口多 Key 增量账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-health-legacy-multi-a',
      api_keys: ['sk-health-legacy-multi-a', 'sk-health-legacy-multi-b'],
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-5.5'],
    status: 'active'
  }, access)
  assert.equal(repositories.recordAccountHealthCheckSuccess(legacyMultiKeyAccount.id, {
    ...healthSettings,
    statusCode: 200
  }), true)
  const legacyMultiKeyBeforeUpdate = repositories.findAccountSummary(legacyMultiKeyAccount.id, access)
  assert.equal(legacyMultiKeyBeforeUpdate?.status, 'active')
  database.prepare(`
    UPDATE accounts
    SET last_health_check_at = ?, last_health_success_at = ?
    WHERE id = ?
  `).run('2026-07-29T09:00:00.000Z', '2026-07-29T09:00:00.000Z', legacyMultiKeyAccount.id)
  const legacyMultiKeyUpdated = repositories.updateAccount(legacyMultiKeyAccount.id, {
    credentials: {
      api_key: 'sk-health-legacy-multi-a',
      api_keys: [
        'sk-health-legacy-multi-a',
        'sk-health-legacy-multi-b',
        'sk-health-legacy-multi-c'
      ],
      base_url: 'https://api.openai.com/v1'
    }
  }, access)
  assert.equal(legacyMultiKeyUpdated?.status, 'active', '旧更新入口保留正常 Key 时不得把账户改为待检查')
  const legacyMultiKeyRow = database.prepare(`
    SELECT status, schedulable, last_health_check_at, last_health_success_at
    FROM accounts
    WHERE id = ?
  `).get(legacyMultiKeyAccount.id) as {
    status: string
    schedulable: number
    last_health_check_at: string | null
    last_health_success_at: string | null
  }
  assert.equal(legacyMultiKeyRow.status, 'active')
  assert.equal(legacyMultiKeyRow.schedulable, 1)
  assert.equal(legacyMultiKeyRow.last_health_check_at, '2026-07-29T09:00:00.000Z')
  assert.equal(legacyMultiKeyRow.last_health_success_at, '2026-07-29T09:00:00.000Z')
  const legacyUnverifiedKey = database.prepare(`
    SELECT status, next_probe_at
    FROM account_api_key_runtime_states
    WHERE account_id = ?
      AND key_index = 2
  `).get(legacyMultiKeyAccount.id) as { status?: string; next_probe_at?: string | null } | undefined
  assert.equal(legacyUnverifiedKey?.status, 'unverified', '旧更新入口新增 Key 必须隔离为未验证')
  assert(legacyUnverifiedKey?.next_probe_at, '旧更新入口新增 Key 必须进入 Key 级探测队列')
  const legacyMultiKeyWinner = repositories.findOpenAIAccountForGroup(
    group.id,
    legacyMultiKeyAccount.id,
    access.systemAccountId,
    { ignoreAvailability: true }
  )
  assert(legacyMultiKeyWinner, '健康检查 Key 成功写回必须取得当前账户候选')
  const winnerKey = 'sk-health-legacy-multi-c'
  const winnerFingerprint = apiKeyRotation.fingerprintAccountApiKey(winnerKey)
  const selectedWinner = {
    ...legacyMultiKeyWinner,
    apiKey: winnerKey,
    selectedApiKeyFingerprint: winnerFingerprint,
    selectedApiKeyIndex: 2,
    apiKeyRuntimeStateDisabled: false,
    credentials: {
      ...legacyMultiKeyWinner.credentials,
      api_key: winnerKey,
      api_keys: ['sk-health-legacy-multi-a', 'sk-health-legacy-multi-b', winnerKey]
    }
  }
  const writeHealthWinnerSuccess = async () => await handleDbServiceOperation({
    type: 'record_account_api_key_success',
    account: selectedWinner,
    trafficSource: 'account_health_check',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'account_health_check',
      probeOutcome: 'complete_success'
    },
    observedAt: new Date().toISOString(),
    expectedAccountConfigRevision: legacyMultiKeyWinner.configRevision
  })
  assert.equal((await writeHealthWinnerSuccess()).changed, true, '账户健康成功后的 DB service 必须把 winner unverified Key 激活')
  assert.equal(
    database.prepare('SELECT status FROM account_api_key_runtime_states WHERE account_id = ? AND key_fingerprint = ?').get(legacyMultiKeyAccount.id, winnerFingerprint)?.status,
    'active',
    '健康检查 winner 的 unverified 状态必须实际写为 active'
  )
  database.prepare(`
    UPDATE account_api_key_runtime_states
    SET status = 'rate_limited', last_attempt_at = ?
    WHERE account_id = ? AND key_fingerprint = ?
  `).run(new Date(Date.now() - 1_000).toISOString(), legacyMultiKeyAccount.id, winnerFingerprint)
  assert.equal((await writeHealthWinnerSuccess()).changed, true, '账户健康成功必须同样恢复 rate_limited winner Key')
  assert.equal(
    database.prepare('SELECT status FROM account_api_key_runtime_states WHERE account_id = ? AND key_fingerprint = ?').get(legacyMultiKeyAccount.id, winnerFingerprint)?.status,
    'active',
    '健康检查 winner 不得因之前是限流态而无法恢复 active'
  )
  repositories.recordAccountHealthCheckFailure(credentialChangedAccount.id, {
    ...healthSettings,
    errorCode: 'probe_task_failure',
    errorMessage: '未收到上游响应头',
    countTowardsThreshold: false
  })
  const pendingAfterInconclusiveProbe = repositories.findAccountSummary(credentialChangedAccount.id, access)
  assert.equal(pendingAfterInconclusiveProbe?.status, 'pending_test', '无结论探针不得改变待检查账户状态')
  assert.equal(pendingAfterInconclusiveProbe?.healthCheckFailureStartedAt, undefined, '无结论探针不得启动 24 小时失败窗口')
  const oldPendingFailureStartedAt = new Date(Date.now() - 25 * 60 * 60_000).toISOString()
  database.prepare('UPDATE accounts SET health_check_failure_started_at = ? WHERE id = ?').run(oldPendingFailureStartedAt, credentialChangedAccount.id)
  repositories.recordAccountHealthCheckFailure(credentialChangedAccount.id, {
    ...healthSettings,
    errorCode: 'probe_task_failure',
    errorMessage: '仍未收到上游响应头',
    countTowardsThreshold: false
  })
  assert.equal(repositories.findAccountSummary(credentialChangedAccount.id, access)?.status, 'pending_test', '既有失败窗口超过 24 小时时，无结论探针也不得把账户转为异常')

  const cooldownProbeAt = trafficAfterSuccess?.lastHealthSuccessAt
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-cooldown',
    trafficSource: 'cooldown_retest',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10
  }])
  const trafficAfterCooldownProbe = repositories.findAccountSummary(trafficAccount.id, access)
  assert.equal(trafficAfterCooldownProbe?.lastHealthSuccessAt, cooldownProbeAt, '后台探活流量不应刷新真实健康成功信号')

  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-account-health-check',
    trafficSource: 'account_health_check',
    accountId: trafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: true,
    statusCode: 200,
    durationMs: 10
  }])
  const trafficAfterHealthCheck = repositories.findAccountSummary(trafficAccount.id, access)
  assert.equal(trafficAfterHealthCheck?.lastHealthSuccessAt, cooldownProbeAt, '定时健康检查使用记录不得重复刷新真实流量成功信号')

  const failedTrafficAccount = createActiveAccount(repositories, group.id, '健康检测真实失败账号', 'sk-health-failed-traffic')
  database.prepare(`
    UPDATE accounts
    SET last_health_success_at = ?,
        next_health_check_at = ?
    WHERE id = ?
  `).run(oldCheckAt, oldCheckAt, failedTrafficAccount.id)
  const failedTrafficBeforeUsage = repositories.findAccountSummary(failedTrafficAccount.id, access)
  repositories.createUsageRecordsBatch([{
    systemAccountId: 'sys_admin',
    traceId: 'trace-health-check-failed-traffic',
    trafficSource: 'gateway',
    accountId: failedTrafficAccount.id,
    groupId: group.id,
    providerCode: 'gpt',
    endpoint: '/v1/responses',
    model: 'gpt-test',
    stream: false,
    success: false,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '模拟真实失败请求',
    durationMs: 10
  }])
  const failedTrafficAfterUsage = repositories.findAccountSummary(failedTrafficAccount.id, access)
  assert.equal(
    failedTrafficAfterUsage?.lastHealthSuccessAt,
    failedTrafficBeforeUsage?.lastHealthSuccessAt,
    '真实失败请求不应刷新健康成功信号'
  )
  const dueAfterFailedTraffic = repositories.listAccountsDueForHealthCheck({ limit: 10, ...healthSettings })
  assert.ok(dueAfterFailedTraffic.some((account) => account.id === failedTrafficAccount.id), '最近只有失败请求的账号仍应进入健康检测候选')

  console.log('account-health-check-regression passed')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}

function createActiveAccount(
  repositories: typeof import('../../storage/repositories.js'),
  groupId: string,
  name: string,
  apiKey: string
) {
  const created = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://api.openai.com/v1'
    },
    groupId,
    supportedModels: ['gpt-5.5'],
    status: 'active'
  }, access)
  assert.equal(created.status, 'pending_test', '新账户应先进入待检查状态')
  assert.equal(repositories.recordAccountHealthCheckSuccess(created.id, {
    ...healthSettings,
    statusCode: 200
  }), true, '后台激活检查成功应更新账户')
  const activated = repositories.findAccountSummary(created.id, access)
  assert.equal(activated?.status, 'active', '后台激活检查成功应把待检查账户恢复为正常')
  return activated!
}
