import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import type { AccountHealthCheckSettings } from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  testOpenAIAccount,
  testOpenAIAccountDiagnosticAttempt,
  testOpenAIAccountWithDiagnosticRetries,
  type AccountDiagnosticAttemptResult,
  type AccountTestInput
} from '../accounts/account-test.service.js'
import {
  automaticAccountAvailabilityProbeFailed,
  automaticAccountProbeOutcome
} from '../accounts/automatic-account-probe-outcome.js'
import {
  accountApiKeyPoolEntriesForCandidate,
} from '../accounts/account-api-key-pool-runtime.js'
import {
  accountApiKeyPoolKeySetFingerprint,
  orderAccountApiKeyPoolEntries,
  runAccountApiKeyPoolDiagnostic
} from '../accounts/account-api-key-pool-diagnostic.js'
import { diagnosticAccountTestGatewaySettingsOverride, diagnosticAttemptSignals } from '../accounts/account-diagnostic-retry-policy.js'
import { effectiveAccountApiKeyCount } from '../accounts/account-balance-config.js'
import { isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { gatewayAccountRuntimeKey } from '../gateway/runtime/account-runtime-keys.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import type { AccountApiKeyPoolProbeCursor } from '../../storage/account-api-key-pool-probe-cursor.repository.js'
import { requestBackgroundWorkerDbService, sendAccountRuntimeClearToServer } from './background-ipc.js'
import {
  accountHealthCheckTriggerPriority,
  type AccountHealthCheckTriggerReason
} from '../accounts/account-health-check-trigger.js'
import { enqueueAccountBalanceAutoDetection } from './account-balance-auto-detect.service.js'
import {
  accountHealthCheckProbeDeadlineMs,
  backgroundProbeDbServiceTimeoutMs,
  globalSharedQueueConcurrency,
  runWithAccountHealthCheckDiagnosticSlot,
  runWithBackgroundAccountAvailabilityProbe,
} from './account-probe-limits.js'

interface AccountHealthCheckQueueItem extends AccountHealthCheckSettings {
  accountId: string
  accountName: string
  configRevision: number
  maxPauseMinutes: number
  reason: AccountHealthCheckTriggerReason
}

const accountHealthCheckRetryPolicy = sequenceRetryPolicy('account_health_check', [], 0)
const requestFailureHealthCheckCooldownMs = 5 * 60_000
const recentRequestFailureHealthChecks = new Map<string, number>()
let lastRequestFailureHealthCheckCleanupAt = 0

export interface AccountHealthCheckProbeResult {
  result: AccountTestResult
  upstreamAttempt?: UpstreamAttempt
  diagnosticCanceled?: boolean
  diagnosticTimeoutExhausted?: boolean
  diagnosticDeadlineExceeded?: boolean
}

const accountHealthCheckQueue = createRetryQueue<AccountHealthCheckQueueItem>({
  name: 'account-health-check',
  policy: accountHealthCheckRetryPolicy,
  concurrency: globalSharedQueueConcurrency,
  run: async (item, context) => await runAccountHealthCheckQueueItem(item, context),
  onExhausted: (event) => {
    logger.warn(errorLogFields(event.error, {
      event: 'background_account_health_check_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1
    }), '账号健康检测任务已用尽，本轮跳过')
  }
})

export function enqueueAccountHealthCheck(
  account: AccountSummary,
  settings: AccountHealthCheckSettings & { maxPauseMinutes: number },
  reason: AccountHealthCheckTriggerReason
): boolean {
  const effectiveReason = account.status === 'pending_test' ? 'activation' : reason
  const now = Date.now()
  if (effectiveReason === 'request_failure') {
    cleanupRecentRequestFailureHealthChecks(now)
    const lastTriggeredAt = recentRequestFailureHealthChecks.get(account.id)
    if (lastTriggeredAt !== undefined && now - lastTriggeredAt < requestFailureHealthCheckCooldownMs) {
      return false
    }
  }
  const enqueued = accountHealthCheckQueue.enqueue(account.id, {
    accountId: account.id,
    accountName: account.name,
    configRevision: account.configRevision ?? 1,
    intervalHours: settings.intervalHours,
    jitterMinutes: settings.jitterMinutes,
    failureThreshold: effectiveReason === 'request_failure' ? 1 : settings.failureThreshold,
    maxPauseMinutes: settings.maxPauseMinutes,
    reason: effectiveReason
  }, {
    priority: accountHealthCheckTriggerPriority(effectiveReason),
    replaceExisting: effectiveReason !== 'scheduled',
    replaceExistingOnlyIfHigherPriority: effectiveReason === 'request_failure'
  })
  if (effectiveReason === 'request_failure') {
    recentRequestFailureHealthChecks.set(account.id, now)
  }
  return enqueued
}

function cleanupRecentRequestFailureHealthChecks(now: number): void {
  if (now - lastRequestFailureHealthCheckCleanupAt < 60_000) return
  lastRequestFailureHealthCheckCleanupAt = now
  const cutoff = now - requestFailureHealthCheckCooldownMs
  for (const [accountId, triggeredAt] of recentRequestFailureHealthChecks) {
    if (triggeredAt <= cutoff) recentRequestFailureHealthChecks.delete(accountId)
  }
}

export async function enqueueAccountHealthCheckById(
  accountId: string,
  settings: AccountHealthCheckSettings & { maxPauseMinutes: number },
  reason: AccountHealthCheckTriggerReason
): Promise<boolean> {
  const normalizedId = accountId.trim()
  if (!normalizedId) return false
  const account = await requestBackgroundWorkerDbService({
    type: 'find_account_for_health_check',
    accountId: normalizedId,
    ignoreSchedule: reason !== 'scheduled'
  }, backgroundProbeDbServiceTimeoutMs)
  return account ? enqueueAccountHealthCheck(account, settings, reason) : false
}

export function getAccountHealthCheckQueueSnapshot() {
  return accountHealthCheckQueue.snapshot()
}

async function runAccountHealthCheckQueueItem(
  item: AccountHealthCheckQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await accountForHealthCheckQueueItem(item)
  if (!isAccountHealthCheckEligible(account, item.reason)) {
    logger.debug({
      event: 'background_account_health_check_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      accountStatus: account?.status,
      schedulable: account?.schedulable,
      boundGroupId: account?.boundGroupId,
      nextHealthCheckAt: account?.nextHealthCheckAt,
      effectiveAvailabilityStatus: account?.effectiveAvailability?.status
    }, '账号健康检测任务已失效，跳过')
    return true
  }
  if ((account.configRevision ?? 1) !== item.configRevision) {
    logger.debug({
      event: 'background_account_health_check_stale_config_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      queuedConfigRevision: item.configRevision,
      currentConfigRevision: account.configRevision ?? 1
    }, '账号配置已变化，丢弃旧健康检测任务')
    return true
  }

  const groupId = account.boundGroupId
  const observedAt = new Date().toISOString()
  const deadline = accountHealthCheckDeadline()
  try {
    return await runWithBackgroundAccountAvailabilityProbe(gatewayAccountRuntimeKey(account), async () => {
      if (deadline.signal.aborted) return accountHealthCheckDeadlineResult(account)
      const candidate = await healthCheckCandidateForAccount(account, groupId)
      if (deadline.signal.aborted) return accountHealthCheckDeadlineResult(account)
      return await runAccountHealthCheckProbe(account, groupId, candidate, deadline.signal)
    }, async ({ result, upstreamAttempt, diagnosticCanceled, diagnosticTimeoutExhausted, diagnosticDeadlineExceeded }, { joined }) => {
    if (joined) {
      logger.debug({
        event: 'background_account_health_check_singleflight_joined',
        accountId: item.accountId,
        accountName: item.accountName,
        reason: item.reason
      }, '同一账户已有可用性探针执行，本轮健康检查复用其结果')
    }
    const probeOutcome = automaticAccountProbeOutcome(result, {
      upstreamAttempt,
      canceled: diagnosticCanceled,
      timeout: diagnosticTimeoutExhausted,
      diagnosticTimeoutExhausted
    })
    const availabilityProbeFailed = automaticAccountAvailabilityProbeFailed(probeOutcome)
    const immediateTemporaryUnavailable = diagnosticTimeoutExhausted === true && probeOutcome === 'upstream_failure'

    if (probeOutcome === 'complete_success') {
      const scheduleBalanceAutoDetection = shouldScheduleAccountBalanceAutoDetection(account)
      const healthCheckResult = await requestBackgroundWorkerDbService({
        type: 'record_account_health_check_success',
        accountId: account.id,
        input: {
          intervalHours: item.intervalHours,
          jitterMinutes: item.jitterMinutes,
          failureThreshold: item.failureThreshold,
          statusCode: result.statusCode,
          expectedConfigRevision: item.configRevision,
          scheduleBalanceAutoDetection,
          traceId: result.traceId
        }
      }, backgroundProbeDbServiceTimeoutMs)
      const changed = healthCheckResult?.changed ?? false
      sendAccountRuntimeClearToServer({ accountId: account.id })
      logger.info({
        event: 'background_account_health_check_passed',
        accountId: account.id,
        accountName: account.name,
        statusCode: result.statusCode,
        durationMs: result.durationMs,
        changed,
        attemptIndex: context.attemptIndex,
        retryNumber: context.retryNumber
      }, '账号健康检测通过，已顺延下次检测')
      if (changed && scheduleBalanceAutoDetection) {
        enqueueAccountBalanceAutoDetection(account.id, item.configRevision)
      }
      return true
    }

    const failure = await requestBackgroundWorkerDbService({
      type: 'record_account_health_check_failure',
      accountId: account.id,
      input: {
        intervalHours: item.intervalHours,
        jitterMinutes: item.jitterMinutes,
        failureThreshold: item.failureThreshold,
        statusCode: result.statusCode,
        errorCode: result.errorCode,
        errorMessage: result.message,
        countTowardsThreshold: availabilityProbeFailed,
        expectedConfigRevision: item.configRevision,
        observedAt,
        traceId: result.traceId
      }
    }, backgroundProbeDbServiceTimeoutMs)

    let markedTemporaryUnavailable = false
    if (failure && account.status !== 'pending_test' && availabilityProbeFailed && (failure.reachedThreshold || immediateTemporaryUnavailable)) {
      const updated = await requestBackgroundWorkerDbService({
        type: 'mark_account_test_temporary_unavailable',
        accountId: account.id,
        reason: accountHealthCheckTemporaryUnavailableReason(failure.failureCount, result, {
          diagnosticTimeoutExhausted: immediateTemporaryUnavailable
        }),
        traceId: result.traceId,
        access: { systemAccountId: account.systemAccountId ?? '', role: 'user' },
        healthCheckGuard: {
          configRevision: item.configRevision,
          checkedAt: failure.checkedAt,
          failureCount: failure.failureCount,
          observedAt
        }
      }, backgroundProbeDbServiceTimeoutMs)
      markedTemporaryUnavailable = updated?.updated ?? false
    }

    const logFields = {
      event: 'background_account_health_check_failed',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      durationMs: result.durationMs,
      failureCount: failure?.failureCount ?? 0,
      reachedThreshold: failure?.reachedThreshold ?? false,
      failureStartedAt: failure?.failureStartedAt,
      transitionedToError: failure?.transitionedToError ?? false,
      accountFailureEligible: result.accountFailureEligible,
      diagnosticTimeoutExhausted,
      diagnosticDeadlineExceeded,
      nextHealthCheckAt: failure?.nextHealthCheckAt,
      markedTemporaryUnavailable,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      message: result.message,
      traceId: result.traceId
    }
    if (diagnosticDeadlineExceeded) {
      logger.warn(logFields, '账号健康检测达到账户级 deadline，已中止剩余 API Key 探测且不累计失败')
    } else if (failure?.transitionedToError) {
      logger.error(logFields, '账号激活检查从首次失败起已持续 24 小时，账户已转为异常')
    } else if (account.status !== 'pending_test' && availabilityProbeFailed && (failure?.reachedThreshold || immediateTemporaryUnavailable)) {
      logger.warn(logFields, '账号健康检测连续失败，已尝试标记为临时不可调用')
    } else {
      logger.warn(logFields, '账号健康检测失败，已记录失败并安排短间隔复检')
    }
      return true
    }, {
      signal: deadline.signal,
      abortedObservation: () => accountHealthCheckDeadlineResult(account)
    })
  } finally {
    deadline.cancel()
  }
}

function shouldScheduleAccountBalanceAutoDetection(account: AccountSummary): boolean {
  return account.status === 'pending_test'
    && account.type === 'api_key'
    && effectiveAccountApiKeyCount(account.credentials) === 1
    && account.balanceQueryEnabled !== true
    && (!account.balanceQueryConfig || Object.keys(account.balanceQueryConfig).length === 0)
}

export async function probeAccountHealthCheckApiKeyPool(
  candidate: OpenAIAccountSecret,
  probe: (fixedCandidate: OpenAIAccountSecret, input: { signal: AbortSignal; timeoutMs: number }) => Promise<AccountHealthCheckProbeResult>,
  options: {
    runAttempt?: <T>(task: () => Promise<T>) => Promise<T>
    signal?: AbortSignal
    startAfterFingerprint?: string
    onKeyAttempt?: (fingerprint: string) => void
    onPoolComplete?: (summary: { lastCompletedFingerprint?: string; completed: boolean; errors: unknown[] }) => void
    abortedResult?: () => AccountHealthCheckProbeResult
  } = {}
): Promise<AccountHealthCheckProbeResult | undefined> {
  const entries = orderAccountApiKeyPoolEntries(
    accountApiKeyPoolEntriesForCandidate(candidate),
    options.startAfterFingerprint
  )
  if (options.signal?.aborted) return options.abortedResult?.()
  const diagnostic = await runAccountApiKeyPoolDiagnostic(candidate, entries, async ({ candidate: fixedCandidate, timeoutMs, signal }) => {
    const value = await (options.runAttempt ?? (async <T>(task: () => Promise<T>) => await task()))(
      async () => await probe(fixedCandidate, { timeoutMs, signal })
    )
    return {
      value,
      success: value.result.success,
      timedOutAfterRealUpstreamAttempt: value.diagnosticTimeoutExhausted === true
        && Boolean(value.upstreamAttempt && isRealUpstreamAttempt(value.upstreamAttempt))
    }
  }, {
    signal: options.signal,
    allowSingleEntry: true,
    onKeyAttempt: (entry) => options.onKeyAttempt?.(entry.fingerprint)
  })
  if (!diagnostic) return undefined
  options.onPoolComplete?.({
    lastCompletedFingerprint: diagnostic.lastCompletedFingerprint,
    completed: diagnostic.completed,
    errors: diagnostic.errors.map((item) => item.error)
  })
  if (options.signal?.aborted) return options.abortedResult?.() ?? canceledPoolProbeResult(diagnostic.attempts[0]?.value, options.signal)
  if (diagnostic.winner) return diagnostic.winner.value
  let fallback: AccountHealthCheckProbeResult | undefined
  let upstreamFailure: AccountHealthCheckProbeResult | undefined
  for (const item of diagnostic.attempts) {
    fallback ??= item.value
    if (automaticAccountProbeOutcome(item.value.result, {
      upstreamAttempt: item.value.upstreamAttempt,
      canceled: item.value.diagnosticCanceled,
      timeout: item.value.diagnosticTimeoutExhausted,
      diagnosticTimeoutExhausted: item.value.diagnosticTimeoutExhausted
    }) === 'upstream_failure') upstreamFailure ??= item.value
  }
  return upstreamFailure ?? fallback
}

function isRoutineAccountHealthCheckReason(reason: AccountHealthCheckTriggerReason): boolean {
  return reason === 'scheduled' || reason === 'request_failure'
}

function accountHealthCheckDeadline(): { signal: AbortSignal; cancel: () => void } {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(new Error('account health check deadline')), accountHealthCheckProbeDeadlineMs)
  timer.unref()
  return {
    signal: controller.signal,
    cancel: () => clearTimeout(timer)
  }
}

function accountHealthCheckDeadlineResult(account: AccountSummary): AccountHealthCheckProbeResult {
  return {
    result: {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      type: account.type,
      success: false,
      errorCode: 'server_diagnostic_cancelled',
      message: '账户健康检查已达到总时限',
      accountFailureEligible: false
    },
    diagnosticCanceled: true,
    diagnosticTimeoutExhausted: false,
    diagnosticDeadlineExceeded: true
  }
}

async function healthCheckCandidateForAccount(
  account: AccountSummary,
  groupId: string
): Promise<OpenAIAccountSecret | undefined> {
  const systemAccountId = account.systemAccountId?.trim()
  const candidate = systemAccountId
    ? await loadOpenAIAccountForGroupViaDbService(groupId, account.id, systemAccountId, { ignoreAvailability: true })
    : undefined
  return candidate
    ? { ...candidate, concurrencyLimit: runtimeConfig.concurrency.globalMax }
    : undefined
}

async function runAccountHealthCheckProbe(
  account: AccountSummary,
  groupId: string,
  candidate: OpenAIAccountSecret | undefined,
  signal: AbortSignal
): Promise<AccountHealthCheckProbeResult> {
  if (signal.aborted) return accountHealthCheckDeadlineResult(account)
  let lastCompletedFingerprint: string | undefined
  let poolCompleted = false
  let poolDiagnosticErrors: unknown[] = []
  const poolEntries = candidate ? accountApiKeyPoolEntriesForCandidate(candidate) : []
  const keySetFingerprint = accountApiKeyPoolKeySetFingerprint(poolEntries)
  const storedCursor: AccountApiKeyPoolProbeCursor | undefined = candidate && poolEntries.length > 0
    ? await requestBackgroundWorkerDbService({
        type: 'account_api_key_pool_probe_cursor',
        action: 'read',
        accountId: account.id,
        purpose: 'health_check'
      }, backgroundProbeDbServiceTimeoutMs) as AccountApiKeyPoolProbeCursor | undefined
    : undefined
  const cursorMatches = storedCursor
    && storedCursor.keySetFingerprint === keySetFingerprint
    && storedCursor.configRevision === (account.configRevision ?? 1)
  const poolResult = candidate
    ? await probeAccountHealthCheckApiKeyPool(candidate, async (fixedCandidate, attempt) => (
        await runAccountHealthCheckDiagnostic(account, groupId, fixedCandidate, attempt.signal, attempt.timeoutMs)
      ), {
        runAttempt: async (task) => await runWithAccountHealthCheckDiagnosticSlot(task),
        signal,
        startAfterFingerprint: cursorMatches ? storedCursor.lastCompletedKeyFingerprint : undefined,
        onPoolComplete: (summary) => {
          lastCompletedFingerprint = summary.lastCompletedFingerprint
          poolCompleted = summary.completed
          poolDiagnosticErrors = summary.errors
        },
        abortedResult: () => accountHealthCheckDeadlineResult(account)
      })
    : undefined
  if (candidate && poolEntries.length > 0) {
    if (poolCompleted && !signal.aborted) {
      await requestBackgroundWorkerDbService({
        type: 'account_api_key_pool_probe_cursor',
        action: 'delete',
        accountId: account.id,
        purpose: 'health_check'
      }, backgroundProbeDbServiceTimeoutMs)
    } else if (lastCompletedFingerprint) {
      await requestBackgroundWorkerDbService({
        type: 'account_api_key_pool_probe_cursor',
        action: 'save',
        input: {
          accountId: account.id,
          purpose: 'health_check',
          lastCompletedKeyFingerprint: lastCompletedFingerprint,
          keySetFingerprint,
          configRevision: account.configRevision ?? 1
        }
      }, backgroundProbeDbServiceTimeoutMs)
    }
  }
  if (poolDiagnosticErrors.length > 0) {
    logger.warn(errorLogFields(poolDiagnosticErrors[0], {
      event: 'background_account_health_check_api_key_pool_attempt_failed',
      accountId: account.id,
      accountName: account.name,
      failedKeyCount: poolDiagnosticErrors.length
    }), '账户 Key 池探针存在调用异常，已保留连续完成游标')
    throw new AggregateError(poolDiagnosticErrors, `账户 ${account.id} 的 API Key 池探针存在调用异常`)
  }
  if (candidate && poolEntries.length > 0 && !poolResult) {
    throw new Error(`账户 ${account.id} 的固定 API Key 池探针没有返回结果`)
  }
  return poolResult ?? await runWithAccountHealthCheckDiagnosticSlot(async () => (
    await runAccountHealthCheckDiagnostic(account, groupId, undefined, signal)
  ))
}

async function runAccountHealthCheckDiagnostic(
  account: AccountSummary,
  groupId: string,
  candidateAccount?: OpenAIAccountSecret,
  signal?: AbortSignal,
  timeoutMs?: number
): Promise<AccountHealthCheckProbeResult> {
  let upstreamAttempt: UpstreamAttempt | undefined
  let diagnosticCanceled = false
  let diagnosticTimeoutExhausted = false
  const diagnosticSignal = timeoutMs === undefined
    ? undefined
    : diagnosticAttemptSignals(signal, timeoutMs)
  const input: AccountTestInput = {
    model: account.healthCheckModel,
    diagnostics: 'limited',
    groupId,
    trafficSource: 'account_health_check',
    testEndpointMode: account.healthCheckEndpointMode,
    forceProbeKind: account.healthCheckEndpointMode === 'images_json' ? 'models_catalog' : undefined,
    requireCatalogModelEvidence: account.healthCheckEndpointMode === 'images_json',
    disableAccountStateMutation: true,
    candidateAccount,
    signal: diagnosticSignal?.signal ?? signal,
    onUpstreamAttempt: (attempt) => {
      upstreamAttempt = attempt
    },
    findAccountForTest: loadAccountForTestViaDbService,
    findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  }
  let result: AccountTestResult
  if (timeoutMs === undefined) {
    result = await testOpenAIAccountWithDiagnosticRetries(account, {
        ...input,
        retryAllFailures: true,
        onDiagnosticAttemptProgress: () => {
          upstreamAttempt = undefined
        },
        onDiagnosticAttemptResult: (attempt: AccountDiagnosticAttemptResult) => {
          upstreamAttempt = attempt.upstreamAttempt
          diagnosticCanceled = attempt.canceled || signal?.aborted === true
          diagnosticTimeoutExhausted = attempt.diagnosticTimeoutExhausted
        }
      })
  } else {
    const singleAttempt = await testOpenAIAccountDiagnosticAttempt(account, {
        ...input,
        signal,
        onUpstreamAttempt: (attempt) => {
          upstreamAttempt = attempt
        }
      }, timeoutMs)
    result = singleAttempt.result
    diagnosticCanceled = singleAttempt.canceled
    diagnosticTimeoutExhausted = singleAttempt.diagnosticTimeoutExhausted
  }
  return {
    result,
    upstreamAttempt,
    diagnosticCanceled,
    diagnosticTimeoutExhausted,
    diagnosticDeadlineExceeded: signal?.aborted === true
  }
}

function canceledPoolProbeResult(
  result: AccountHealthCheckProbeResult | undefined,
  signal: AbortSignal | undefined
): AccountHealthCheckProbeResult | undefined {
  if (!signal?.aborted || !result) return undefined
  return {
    ...result,
    diagnosticCanceled: true,
    diagnosticDeadlineExceeded: true,
    diagnosticTimeoutExhausted: false
  }
}

async function accountForHealthCheckQueueItem(item: AccountHealthCheckQueueItem): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_health_check',
    accountId: item.accountId,
    ignoreSchedule: item.reason !== 'scheduled'
  }, backgroundProbeDbServiceTimeoutMs)
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_test',
    accountId,
    access
  }, backgroundProbeDbServiceTimeoutMs)
}

async function loadOpenAIAccountForGroupViaDbService(
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = { ignoreAvailability: true }
) {
  return await requestBackgroundWorkerDbService({
    type: 'find_openai_account_for_group',
    groupId,
    accountId,
    systemAccountId,
    includeUnavailable: options.includeUnavailable,
    ignoreAvailability: options.ignoreAvailability
  }, backgroundProbeDbServiceTimeoutMs)
}

function isAccountHealthCheckEligible(
  account: AccountSummary | undefined,
  reason: AccountHealthCheckTriggerReason
): account is AccountSummary & { boundGroupId: string } {
  if (!account) return false
  if (!['active', 'pending_test'].includes(account.status) || !account.boundGroupId) return false
  if (account.status === 'active' && !account.schedulable) return false
  if (account.accountExpiresAt) {
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) return false
  }
  if (reason === 'scheduled' && account.nextHealthCheckAt) {
    const nextMs = Date.parse(account.nextHealthCheckAt)
    if (Number.isFinite(nextMs) && nextMs > Date.now()) return false
  }
  if (account.status === 'active' && account.effectiveAvailability && !account.effectiveAvailability.available) return false
  return true
}

function accountHealthCheckTemporaryUnavailableReason(
  failureCount: number,
  result: AccountTestResult,
  options: { diagnosticTimeoutExhausted?: boolean } = {}
): string {
  const parts = [options.diagnosticTimeoutExhausted
    ? '后台健康检查完整诊断阶梯均在真实上游尝试后超时，已标记为临时不可调用'
    : `后台健康检测连续失败 ${failureCount} 次，已标记为临时不可调用`]
  if (typeof result.statusCode === 'number') {
    parts.push(`HTTP ${Math.trunc(result.statusCode)}`)
  }
  if (result.errorCode) {
    parts.push(result.errorCode)
  }
  if (result.message) {
    parts.push(result.message)
  }
  return parts.join('；').slice(0, 1000)
}
