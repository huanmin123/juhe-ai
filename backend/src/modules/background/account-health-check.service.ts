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
  fixedAccountApiKeyPoolCandidate,
} from '../accounts/account-api-key-pool-runtime.js'
import {
  accountApiKeyPoolKeySetFingerprint,
  orderAccountApiKeyPoolEntries,
  runAccountApiKeyPoolDiagnostic
} from '../accounts/account-api-key-pool-diagnostic.js'
import type { AccountApiKeyEntry } from '../../storage/account-api-key-rotation.js'
import { diagnosticAccountTestGatewaySettingsOverride, diagnosticAttemptSignals } from '../accounts/account-diagnostic-retry-policy.js'
import { effectiveAccountApiKeyCount } from '../accounts/account-balance-config.js'
import { isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { gatewayAccountRuntimeKey } from '../gateway/runtime/account-runtime-keys.js'
import {
  acquireAvailabilityProbe,
  availabilityProbeSourceFences,
  getAvailabilityProbeState,
  settleAvailabilityProbe,
  type AvailabilityProbeOutcome
} from '../gateway/runtime/availability-probe-coordinator.js'
import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import type { AccountApiKeyPoolProbeCursor } from '../../storage/account-api-key-pool-probe-cursor.repository.js'
import {
  requestBackgroundWorkerDbService,
  sendAccountRuntimeClearToServer,
  sendCodexSourceFenceSettledToServer
} from './background-ipc.js'
import {
  accountHealthCheckTriggerPriority,
  type AccountHealthCheckTriggerReason,
  type CodexSourceProbeFence
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
  coordinatorFailureRetryCount?: number
}

interface AccountHealthCheckExecution {
  ordinaryAccountHealthSemantics: boolean
  sourceFences: Map<string, CodexSourceProbeFence>
  settledSourceFenceOutcome?: AvailabilityProbeOutcome
  settledSourceFenceGeneration?: number
}

type AccountHealthCheckProbeRunner = (
  account: AccountSummary,
  groupId: string,
  candidate: OpenAIAccountSecret | undefined,
  signal: AbortSignal
) => Promise<AccountHealthCheckProbeResult>

const accountHealthCheckRetryPolicy = sequenceRetryPolicy('account_health_check', [], 0)
const maxSourceFencesPerHealthExecution = 64
const coordinatorJoinRetryMinimumDelayMs = 50
const coordinatorJoinRetryMaximumDelayMs = 90_000
const coordinatorFailureRetryDelaysMs = [1_000, 5_000] as const
const accountHealthCheckExecutions = new Map<string, AccountHealthCheckExecution>()
let accountHealthCheckProbeRunnerForTest: AccountHealthCheckProbeRunner | undefined

export interface AccountHealthCheckProbeResult {
  result: AccountTestResult
  upstreamAttempt?: UpstreamAttempt
  apiKeyPoolWinner?: Pick<AccountApiKeyEntry, 'fingerprint' | 'index'>
  diagnosticCanceled?: boolean
  diagnosticTimeoutExhausted?: boolean
  diagnosticDeadlineExceeded?: boolean
  diagnosticCompleted?: boolean
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
  reason: AccountHealthCheckTriggerReason,
  sourceFence?: CodexSourceProbeFence
): boolean {
  const effectiveReason = account.status === 'pending_test' ? 'activation' : reason
  const executionKey = accountHealthCheckExecutionKey(account.id)
  let existingExecution = accountHealthCheckExecutions.get(executionKey)
  if (sourceFence) {
    if (sourceFence.configRevision !== (account.configRevision ?? 1)) {
      sendCodexSourceFenceSettledToServer(sourceFence, 'stale')
      return true
    }
    if (existingExecution?.settledSourceFenceOutcome !== undefined && !accountHealthCheckQueue.hasFollowUp(account.id)) {
      if (existingExecution.settledSourceFenceGeneration === sourceFence.probeGeneration) {
        sendCodexSourceFenceSettledToServer(sourceFence, existingExecution.settledSourceFenceOutcome)
        return true
      }
      // This fence belongs to a coordinator generation created after the
      // previous local probe settled. It must create fresh source-only work,
      // rather than accidentally consuming the old outcome.
      accountHealthCheckExecutions.delete(executionKey)
      existingExecution = undefined
    }
    // A source handoff is metadata for an existing physical probe when one is
    // queued/running. It must not create retry-queue follow-up work.
    if (!registerSourceFence(executionKey, sourceFence)) {
      sendCodexSourceFenceSettledToServer(sourceFence, 'unknown')
      return true
    }
    if (existingExecution) return true
  } else if (existingExecution) {
    existingExecution.ordinaryAccountHealthSemantics = true
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
    replaceExistingOnlyIfHigherPriority: effectiveReason === 'request_failure',
    followUpWhenRunning: effectiveReason === 'request_failure'
  })
  if (enqueued && !existingExecution) {
    accountHealthCheckExecutions.set(executionKey, {
      ordinaryAccountHealthSemantics: sourceFence === undefined,
      sourceFences: sourceFence ? new Map([[sourceFenceKey(sourceFence), sourceFence]]) : new Map()
    })
  }
  if (!enqueued && sourceFence) {
    const unqueuedFences = takeAccountHealthCheckExecutionSourceFences(executionKey)
    accountHealthCheckExecutions.delete(executionKey)
    settleCompletedSourceFences(unqueuedFences, 'unknown')
  }
  return enqueued
}

export async function enqueueAccountHealthCheckById(
  accountId: string,
  settings: AccountHealthCheckSettings & { maxPauseMinutes: number },
  reason: AccountHealthCheckTriggerReason,
  sourceFence?: CodexSourceProbeFence
): Promise<boolean> {
  const normalizedId = accountId.trim()
  if (!normalizedId) return false
  const account = await requestBackgroundWorkerDbService({
    type: 'find_account_for_health_check',
    accountId: normalizedId,
    ignoreSchedule: reason !== 'scheduled'
  }, backgroundProbeDbServiceTimeoutMs)
  if (!account) {
    if (sourceFence) sendCodexSourceFenceSettledToServer(sourceFence, 'stale')
    return false
  }
  return enqueueAccountHealthCheck(account, settings, reason, sourceFence)
}

export function getAccountHealthCheckQueueSnapshot() {
  return accountHealthCheckQueue.snapshot()
}

export function setAccountHealthCheckProbeRunnerForTest(runner: AccountHealthCheckProbeRunner | undefined): void {
  accountHealthCheckProbeRunnerForTest = runner
}

async function runAccountHealthCheckQueueItem(
  item: AccountHealthCheckQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const executionKey = accountHealthCheckExecutionKey(item.accountId)
  const execution = accountHealthCheckExecutions.get(executionKey)
  if (execution) {
    execution.settledSourceFenceOutcome = undefined
    execution.settledSourceFenceGeneration = undefined
  }
  let deadline: ReturnType<typeof accountHealthCheckDeadline> | undefined
  let coordination: Awaited<ReturnType<typeof acquireAvailabilityProbe>> | undefined
  let completedExecution: AccountHealthCheckExecution | undefined
  let completedExecutionSourceFences: CodexSourceProbeFence[] = []
  let completedExecutionSourceFencesSettled = false
  let coordinatorOperationFailed = false
  let successfulApiKeyPoolCandidate: OpenAIAccountSecret | undefined
  const runCoordinatorOperation = async <T>(operation: () => Promise<T>): Promise<T> => {
    try {
      return await operation()
    } catch (error) {
      coordinatorOperationFailed = true
      throw error
    }
  }
  try {
    const account = await accountForHealthCheckQueueItem(item)
    if (!isAccountHealthCheckEligible(account, item.reason)) {
      settleSourceFences(executionKey, 'stale')
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
      settleSourceFences(executionKey, 'stale')
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
    const executionDeadline = accountHealthCheckDeadline()
    deadline = executionDeadline
    const acquiredCoordination = await runCoordinatorOperation(async () => await acquireAvailabilityProbe({
      accountRuntimeScope: gatewayAccountRuntimeKey(account),
      probeKind: 'account_health_check',
      configRevision: item.configRevision,
      executionRole: 'health_probe',
      forceNewGeneration: item.reason === 'request_failure'
    }))
    coordination = acquiredCoordination
    settleReplacedCoordinatorSourceFences(acquiredCoordination)
    if (acquiredCoordination.disposition === 'joined') {
      logger.debug({
        event: 'background_account_health_check_runtime_coordinator_joined',
        accountId: item.accountId,
        accountName: item.accountName,
        reason: item.reason,
        generation: acquiredCoordination.generation,
        retryAtMs: acquiredCoordination.retryAtMs
      }, '共享可用性探活已有 owner，本轮健康检查不追加实际诊断')
      // A local source-only queue item that lost the distributed lease has no
      // authority to consume the remote owner's outcome. Its fence is unknown,
      // but the execution record must survive a queued ordinary tail.
      settleCompletedSourceFences(
        takeAccountHealthCheckExecutionSourceFences(executionKey, execution),
        'unknown'
      )
      enqueueRequestFailureTailAfterCoordinatorWait(item, execution, acquiredCoordination.retryAtMs)
      executionDeadline.cancel()
      return true
    }
    return await runWithBackgroundAccountAvailabilityProbe(gatewayAccountRuntimeKey(account), async () => {
      if (executionDeadline.signal.aborted) return accountHealthCheckDeadlineResult(account)
      if (accountHealthCheckProbeRunnerForTest) {
        return await accountHealthCheckProbeRunnerForTest(account, groupId, undefined, executionDeadline.signal)
      }
      const candidate = await healthCheckCandidateForAccount(account, groupId)
      if (executionDeadline.signal.aborted) return accountHealthCheckDeadlineResult(account)
      const probeResult = await runAccountHealthCheckProbe(account, groupId, candidate, executionDeadline.signal)
      if (candidate && probeResult.apiKeyPoolWinner) {
        const entry = accountApiKeyPoolEntriesForCandidate(candidate).find((item) => (
          item.fingerprint === probeResult.apiKeyPoolWinner?.fingerprint
          && item.index === probeResult.apiKeyPoolWinner?.index
        ))
        if (entry) successfulApiKeyPoolCandidate = fixedAccountApiKeyPoolCandidate(candidate, entry)
      }
      return probeResult
    }, async ({ result, upstreamAttempt, diagnosticCanceled, diagnosticTimeoutExhausted, diagnosticDeadlineExceeded, diagnosticCompleted }, { joined }) => {
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
      const coordinatorOutcome = availabilityProbeCoordinatorOutcome(probeOutcome, diagnosticCanceled)
      const probeSettled = await runCoordinatorOperation(async () => await settleAvailabilityProbe({
        runtimeKey: acquiredCoordination.runtimeKey,
        generation: acquiredCoordination.generation,
        ownerToken: acquiredCoordination.ownerToken,
        outcome: coordinatorOutcome
      }))
      // Settle the current probe's source fences, but retain the execution
      // record while a request_failure follow-up is queued. A fence arriving
      // between the first probe and its tail must join that tail rather than
      // replacing it with source-only work.
      completedExecution = execution
      const executionSourceFences = takeAccountHealthCheckExecutionSourceFences(executionKey, execution)
      completedExecutionSourceFences = executionSourceFences
      const sourceFenceOutcome = probeSettled ? coordinatorOutcome : 'stale'
      if (completedExecution) {
        completedExecution.settledSourceFenceOutcome = sourceFenceOutcome
        completedExecution.settledSourceFenceGeneration = acquiredCoordination.generation
      }
      const coordinatorSourceFences = probeSettled
        ? await runCoordinatorOperation(async () => await availabilityProbeSourceFences(acquiredCoordination.runtimeKey, acquiredCoordination.generation))
        : []
      const sourceFences = mergeSourceFences(
        executionSourceFences,
        coordinatorSourceFences.map((fence) => ({
          ...fence,
          runtimeKey: acquiredCoordination.runtimeKey,
          probeGeneration: acquiredCoordination.generation,
          configRevision: item.configRevision
        }))
      )
      const settleCompletedExecutionSourceFences = (outcome: AvailabilityProbeOutcome) => {
        settleCompletedSourceFences(sourceFences, outcome)
        completedExecutionSourceFencesSettled = true
      }
      // A missing local execution record must preserve ordinary health behavior.
      // Source-only queue work always registers its record before enqueueing.
      const sourceOnlyProbe = completedExecution?.ordinaryAccountHealthSemantics === false
      if (!probeSettled) {
        // A lease-taken-over owner has no authority to write either account
        // health or a source success. The current owner will produce the next
        // generation's result.
        settleCompletedExecutionSourceFences('stale')
        if (item.reason === 'request_failure' && completedExecution?.ordinaryAccountHealthSemantics) {
          const latestState = await runCoordinatorOperation(async () => await getAvailabilityProbeState(acquiredCoordination.runtimeKey))
          enqueueRequestFailureTailAfterCoordinatorWait(
            item,
            completedExecution,
            latestState?.probeRunUntilMs ?? latestState?.nextProbeAtMs ?? Date.now() + coordinatorJoinRetryMaximumDelayMs
          )
        }
        return true
      }
      const availabilityProbeFailed = automaticAccountAvailabilityProbeFailed(probeOutcome)
      const diagnosticTimeoutTemporaryUnavailable = diagnosticTimeoutExhausted === true && probeOutcome === 'upstream_failure'
      const scheduledProbeFailureImmediate = item.reason === 'scheduled'
        && diagnosticCompleted === true
        && diagnosticDeadlineExceeded !== true
        && probeOutcome !== 'complete_success'
        && probeOutcome !== 'probe_task_failure'
      const immediateTemporaryUnavailable = diagnosticTimeoutTemporaryUnavailable || scheduledProbeFailureImmediate

      if (probeOutcome === 'complete_success') {
        settleCompletedExecutionSourceFences(probeSettled ? 'success' : 'stale')
        if (sourceOnlyProbe) {
          // A source-bound success proves only the registered source/turn fence.
          // It must not clear account runtime/circuit state or write health.
          return true
        }
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
        if (changed && successfulApiKeyPoolCandidate) {
          await requestBackgroundWorkerDbService({
            type: 'record_account_api_key_success',
            account: successfulApiKeyPoolCandidate,
            trafficSource: 'account_health_check',
            mutationContext: {
              authority: 'automatic_probe',
              trafficSource: 'account_health_check',
              probeOutcome: 'complete_success'
            },
            observedAt,
            expectedAccountConfigRevision: item.configRevision
          }, backgroundProbeDbServiceTimeoutMs)
        }
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

      if (sourceOnlyProbe && !sourceBoundProbePermitsAccountHealthMutation(coordinatorOutcome)) {
        // Unknown, canceled, task failure and neutral framing have no account
        // mutation authority when this worker was dispatched by a Codex source.
        settleCompletedExecutionSourceFences(sourceFenceOutcome)
        return true
      }

      settleCompletedExecutionSourceFences(sourceFenceOutcome)

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
      let temporaryUnavailableSkippedReason: string | undefined
      if (failure?.changed !== true) {
        temporaryUnavailableSkippedReason = 'failure_write_not_applied'
      } else if (account.status === 'pending_test') {
        temporaryUnavailableSkippedReason = 'status_ineligible'
      } else if (!availabilityProbeFailed) {
        temporaryUnavailableSkippedReason = 'availability_not_confirmed'
      } else if (!failure.reachedThreshold && !immediateTemporaryUnavailable) {
        temporaryUnavailableSkippedReason = 'threshold_not_reached'
      } else {
        const updated = await requestBackgroundWorkerDbService({
          type: 'mark_account_test_temporary_unavailable',
          accountId: account.id,
          reason: accountHealthCheckTemporaryUnavailableReason(failure.failureCount, result, {
            diagnosticTimeoutExhausted: diagnosticTimeoutTemporaryUnavailable,
            diagnosticConfirmedFailure: immediateTemporaryUnavailable
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
        temporaryUnavailableSkippedReason = markedTemporaryUnavailable
          ? undefined
          : updated?.skippedReason ?? 'mutation_cas_rejected'
      }

      const logFields = {
        event: 'background_account_health_check_failed',
        accountId: account.id,
        accountName: account.name,
        statusCode: result.statusCode,
        errorCode: result.errorCode,
        durationMs: result.durationMs,
        failureCount: failure?.failureCount ?? 0,
        healthFailureRecorded: failure?.changed === true,
        reachedThreshold: failure?.reachedThreshold ?? false,
        failureStartedAt: failure?.failureStartedAt,
        transitionedToError: failure?.transitionedToError ?? false,
        accountFailureEligible: result.accountFailureEligible,
        diagnosticTimeoutExhausted,
        diagnosticDeadlineExceeded,
        diagnosticCompleted,
        nextHealthCheckAt: failure?.nextHealthCheckAt,
        markedTemporaryUnavailable,
        temporaryUnavailableSkippedReason,
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
      abortedObservation: () => accountHealthCheckDeadlineResult(account),
      joinSettled: item.reason !== 'request_failure'
    })
  } catch (error) {
    const ownerCoordination = coordination?.disposition === 'owner' ? coordination : undefined
    if (ownerCoordination) {
      try {
        await runCoordinatorOperation(async () => await settleAvailabilityProbe({
          runtimeKey: ownerCoordination.runtimeKey,
          generation: ownerCoordination.generation,
          ownerToken: ownerCoordination.ownerToken,
          outcome: 'probe_task_failure'
        }))
      } catch (settlementError) {
        logger.warn(errorLogFields(settlementError, {
          event: 'background_account_health_check_coordinator_settlement_failed',
          accountId: item.accountId,
          accountName: item.accountName
        }), '账号健康检查未能结算共享探针，继续结算本地来源 fence')
      }
    }
    if (completedExecution) {
      const completedExecutionOutcome = completedExecution.settledSourceFenceOutcome ?? 'probe_task_failure'
      if (!completedExecutionSourceFencesSettled) {
        settleCompletedSourceFences(completedExecutionSourceFences, completedExecutionOutcome)
      }
      settleCompletedSourceFences(
        takeAccountHealthCheckExecutionSourceFences(executionKey, completedExecution),
        completedExecutionOutcome
      )
    } else {
      settleCompletedSourceFences(
        takeAccountHealthCheckExecutionSourceFences(executionKey, execution),
        'probe_task_failure'
      )
    }
    if (coordinatorOperationFailed && enqueueCoordinatorFailureRetry(item, execution)) {
      logger.warn(errorLogFields(error, {
        event: 'background_account_health_check_coordinator_retry_enqueued',
        accountId: item.accountId,
        accountName: item.accountName,
        retryCount: (item.coordinatorFailureRetryCount ?? 0) + 1
      }), '共享探活协调器异常，已保留有界 request_failure 尾随重试')
    }
    throw error
  } finally {
    deadline?.cancel()
    if (
      accountHealthCheckExecutions.get(executionKey) === execution
      && !accountHealthCheckQueue.hasFollowUp(item.accountId)
    ) {
      accountHealthCheckExecutions.delete(executionKey)
      settleCompletedSourceFences([...execution?.sourceFences.values() ?? []], 'unknown')
    }
  }
}

function accountHealthCheckExecutionKey(accountId: string): string {
  return accountId.trim()
}

function sourceFenceKey(fence: CodexSourceProbeFence): string {
  return `${fence.stateKey}:${fence.accountId}:${fence.sourceGeneration}`
}

function registerSourceFence(executionKey: string, sourceFence: CodexSourceProbeFence): boolean {
  const execution = accountHealthCheckExecutions.get(executionKey)
  if (execution) {
    const key = sourceFenceKey(sourceFence)
    if (execution.sourceFences.has(key)) return true
    if (execution.sourceFences.size >= maxSourceFencesPerHealthExecution) return false
    execution.sourceFences.set(key, sourceFence)
    return true
  }
  accountHealthCheckExecutions.set(executionKey, {
    ordinaryAccountHealthSemantics: false,
    sourceFences: new Map([[sourceFenceKey(sourceFence), sourceFence]])
  })
  return true
}

function takeAccountHealthCheckExecutionSourceFences(
  executionKey: string,
  expectedExecution?: AccountHealthCheckExecution
): CodexSourceProbeFence[] {
  const execution = accountHealthCheckExecutions.get(executionKey)
  if (!execution || (expectedExecution && execution !== expectedExecution)) return []
  const sourceFences = [...execution.sourceFences.values()]
  execution.sourceFences.clear()
  return sourceFences
}

function settleSourceFences(
  executionKey: string,
  outcome: AvailabilityProbeOutcome,
  sourceFences = takeAccountHealthCheckExecutionSourceFences(executionKey)
): void {
  if (!accountHealthCheckQueue.hasFollowUp(executionKey)) {
    accountHealthCheckExecutions.delete(executionKey)
  }
  settleCompletedSourceFences(sourceFences, outcome)
}

function settleCompletedSourceFences(sourceFences: readonly CodexSourceProbeFence[], outcome: AvailabilityProbeOutcome): void {
  for (const sourceFence of sourceFences) {
    sendCodexSourceFenceSettledToServer(sourceFence, outcome)
  }
}

function settleReplacedCoordinatorSourceFences(
  coordination: Awaited<ReturnType<typeof acquireAvailabilityProbe>>
): void {
  if (coordination.disposition !== 'owner' || !coordination.replacedFenceSettlement) return
  const settlement = coordination.replacedFenceSettlement
  settleCompletedSourceFences(
    settlement.sourceFences.map((fence) => ({
      ...fence,
      runtimeKey: coordination.runtimeKey,
      probeGeneration: settlement.generation,
      configRevision: settlement.configRevision
    })),
    settlement.outcome
  )
}

function enqueueCoordinatorFailureRetry(
  item: AccountHealthCheckQueueItem,
  execution: AccountHealthCheckExecution | undefined
): boolean {
  if (item.reason !== 'request_failure' || execution?.ordinaryAccountHealthSemantics !== true) return false
  const retryCount = item.coordinatorFailureRetryCount ?? 0
  const delayMs = coordinatorFailureRetryDelaysMs[retryCount]
  if (delayMs === undefined) return false
  return accountHealthCheckQueue.enqueue(item.accountId, {
    ...item,
    coordinatorFailureRetryCount: retryCount + 1
  }, {
    priority: accountHealthCheckTriggerPriority('request_failure'),
    delayMs,
    replaceExisting: true,
    followUpWhenRunning: true
  })
}

function coordinatorJoinRetryDelayMs(retryAtMs: number): number {
  const remainingMs = Number.isFinite(retryAtMs) ? retryAtMs - Date.now() : coordinatorJoinRetryMaximumDelayMs
  return Math.max(
    coordinatorJoinRetryMinimumDelayMs,
    Math.min(coordinatorJoinRetryMaximumDelayMs, Math.trunc(remainingMs))
  )
}

function enqueueRequestFailureTailAfterCoordinatorWait(
  item: AccountHealthCheckQueueItem,
  execution: AccountHealthCheckExecution | undefined,
  retryAtMs: number
): void {
  if (item.reason !== 'request_failure' || !execution?.ordinaryAccountHealthSemantics) return
  const deferred = accountHealthCheckQueue.enqueue(item.accountId, {
    ...item,
    failureThreshold: 1,
    reason: 'request_failure'
  }, {
    priority: accountHealthCheckTriggerPriority('request_failure'),
    delayMs: coordinatorJoinRetryDelayMs(retryAtMs),
    replaceExisting: true,
    replaceExistingOnlyIfHigherPriority: true,
    followUpWhenRunning: true
  })
  if (!deferred) {
    logger.debug({
      event: 'background_account_health_check_runtime_coordinator_join_tail_preserved',
      accountId: item.accountId,
      accountName: item.accountName,
      reason: item.reason,
      retryAtMs
    }, '共享探活占用期间保留了更高优先级的健康检查任务')
  }
}

function mergeSourceFences(...groups: readonly CodexSourceProbeFence[][]): CodexSourceProbeFence[] {
  const merged = new Map<string, CodexSourceProbeFence>()
  for (const group of groups) {
    for (const sourceFence of group) merged.set(sourceFenceKey(sourceFence), sourceFence)
  }
  return [...merged.values()]
}

function availabilityProbeCoordinatorOutcome(
  outcome: ReturnType<typeof automaticAccountProbeOutcome>,
  diagnosticCanceled: boolean | undefined
): AvailabilityProbeOutcome {
  if (outcome === 'complete_success') return 'success'
  if (outcome === 'upstream_failure') return 'health_failure'
  // A complete HTTP frame with neutral semantic classification remains owned
  // by the existing automatic-account classifier. It is not a confirmed
  // transport health failure for source-avoidance settlement.
  if (outcome === 'framing_complete_neutral') return 'unknown'
  return diagnosticCanceled ? 'canceled' : 'probe_task_failure'
}

export function sourceBoundProbePermitsAccountHealthMutation(outcome: AvailabilityProbeOutcome): boolean {
  return outcome === 'health_failure'
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
  if (diagnostic.winner) {
    return {
      ...diagnostic.winner.value,
      apiKeyPoolWinner: {
        fingerprint: diagnostic.winner.entry.fingerprint,
        index: diagnostic.winner.entry.index
      }
    }
  }
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
    diagnosticDeadlineExceeded: true,
    diagnosticCompleted: false
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
  if (poolResult) {
    return {
      ...poolResult,
      diagnosticCompleted: poolCompleted && !signal.aborted
    }
  }
  return await runWithAccountHealthCheckDiagnosticSlot(async () => (
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
    diagnosticDeadlineExceeded: signal?.aborted === true,
    diagnosticCompleted: timeoutMs === undefined && signal?.aborted !== true
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
    diagnosticTimeoutExhausted: false,
    diagnosticCompleted: false
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
  options: { diagnosticTimeoutExhausted?: boolean; diagnosticConfirmedFailure?: boolean } = {}
): string {
  const reason = options.diagnosticTimeoutExhausted
    ? '后台健康检查完整诊断阶梯均在真实上游尝试后超时，已标记为临时不可调用'
    : options.diagnosticConfirmedFailure
      ? '后台周期健康检查完成诊断但未通过，已标记为临时不可调用'
      : `后台健康检测连续失败 ${failureCount} 次，已标记为临时不可调用`
  const parts = [reason]
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
