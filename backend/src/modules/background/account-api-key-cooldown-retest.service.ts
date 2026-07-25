import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import {
  type AccountApiKeyRuntimeProbeCandidate
} from '../../storage/account-api-key-runtime-state.repository.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'
import { automaticAccountProbeOutcome } from '../accounts/automatic-account-probe-outcome.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { backgroundProbeDbServiceTimeoutMs, runWithBackgroundFullDiagnosticSlot } from './account-probe-limits.js'

interface AccountApiKeyCooldownRetestQueueItem extends AccountApiKeyRuntimeProbeCandidate {
  maxRecoveryHours: number
}

const accountApiKeyCooldownRetestRetryPolicy = sequenceRetryPolicy('account_api_key_cooldown_retest_revival', [], 0)

const accountApiKeyCooldownRetestQueue = createRetryQueue<AccountApiKeyCooldownRetestQueueItem>({
  name: 'account-api-key-cooldown-retest',
  policy: accountApiKeyCooldownRetestRetryPolicy,
  concurrency: 1,
  run: (item, context) => runWithBackgroundFullDiagnosticSlot(() => runAccountApiKeyCooldownRetestQueueItem(item, context)),
  onExhausted: (event) => {
    logger.warn({
      event: 'background_account_api_key_cooldown_retest_retry_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      keyFingerprint: event.item.keyFingerprint,
      attemptCount: event.attemptIndex + 1
    }, '账户内 API Key 复测重试已用尽，本轮保留冷却状态等待下个周期')
  }
})

export function enqueueAccountApiKeyCooldownRetest(
  candidate: AccountApiKeyRuntimeProbeCandidate,
  strategy: { maxRecoveryHours: number }
): boolean {
  return accountApiKeyCooldownRetestQueue.enqueue(`${candidate.accountId}:${candidate.keyFingerprint}`, {
    ...candidate,
    maxRecoveryHours: strategy.maxRecoveryHours
  })
}

export function getAccountApiKeyCooldownRetestQueueSnapshot() {
  return accountApiKeyCooldownRetestQueue.snapshot()
}

export function setAccountApiKeyCooldownRetestQueueConcurrency(concurrency: number): void {
  accountApiKeyCooldownRetestQueue.setConcurrency(concurrency)
}

async function runAccountApiKeyCooldownRetestQueueItem(
  item: AccountApiKeyCooldownRetestQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await loadAccountForTestViaDbService(item.accountId)
  if (!account || account.type !== 'api_key' || account.status !== 'active' || !account.schedulable || !account.boundGroupId) {
    logger.debug({
      event: 'background_account_api_key_cooldown_retest_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex,
      accountStatus: account?.status,
      boundGroupId: account?.boundGroupId
    }, '账户内 API Key 复测任务已失效，跳过队列项')
    return true
  }

  const systemAccountId = account.ownerSystemAccountId ?? account.systemAccountId
  if (!systemAccountId) {
    return true
  }
  const candidateAccount = await loadOpenAIAccountForGroupViaDbService(account.boundGroupId, account.id, systemAccountId)
  if (!candidateAccount || candidateAccount.type !== 'api_key') {
    return true
  }
  const currentKey = accountApiKeyEntries(candidateAccount.credentials)
    .find((entry) => entry.fingerprint === item.keyFingerprint && entry.key === item.apiKey)
  if (!currentKey) {
    logger.debug({
      event: 'background_account_api_key_cooldown_retest_stale_credential_discarded',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex
    }, '账户内 API Key 复测凭据已轮换，已丢弃旧队列项')
    return true
  }
  const fixedKeyCandidate = {
    ...candidateAccount,
    apiKey: item.apiKey,
    selectedApiKeyFingerprint: item.keyFingerprint,
    selectedApiKeyIndex: item.keyIndex
  }
  const probeStartedAt = new Date().toISOString()
  let upstreamAttempt: UpstreamAttempt | undefined
  const result = await testOpenAIAccount(account, {
    diagnostics: 'limited',
    testEndpointMode: account.healthCheckEndpointMode,
    groupId: account.boundGroupId,
    systemAccountId,
    trafficSource: 'cooldown_retest',
    candidateAccount: fixedKeyCandidate,
    disableAccountStateMutation: true,
    onUpstreamAttempt: (attempt) => {
      upstreamAttempt = attempt
    },
    findAccountForTest: loadAccountForTestViaDbService,
    findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  })

  const probeOutcome = automaticAccountProbeOutcome(result, { upstreamAttempt })
  if (probeOutcome === 'complete_success') {
    const restored = await requestBackgroundWorkerDbService({
      type: 'record_account_api_key_success',
      account: fixedKeyCandidate,
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome: 'complete_success'
      },
      observedAt: probeStartedAt,
      expectedStatus: item.status,
      expectedNextProbeAt: item.nextProbeAt,
      expectedStateUpdatedAt: item.stateUpdatedAt,
      expectedProbeClaimToken: item.probeClaimToken,
      expectedAccountConfigRevision: item.accountConfigRevision
    }, backgroundProbeDbServiceTimeoutMs)
    logger.info({
      event: 'background_account_api_key_cooldown_retest_restored',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      restored: restored?.changed ?? false
    }, '账户内 API Key 复测通过，Key 已恢复可调度')
    return true
  }

  if (probeOutcome !== 'upstream_failure') {
    const deferred = await requestBackgroundWorkerDbService({
      type: 'defer_account_api_key_probe',
      account: fixedKeyCandidate,
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome
      },
      input: {
        expectedStatus: item.status,
        expectedNextProbeAt: item.nextProbeAt,
        expectedStateUpdatedAt: item.stateUpdatedAt,
        expectedProbeClaimToken: item.probeClaimToken,
        expectedAccountConfigRevision: item.accountConfigRevision,
        delaySeconds: 60,
        observedAt: probeStartedAt
      }
    }, backgroundProbeDbServiceTimeoutMs)
    logger.warn({
      event: 'background_account_api_key_cooldown_retest_task_failed',
      accountId: account.id,
      accountName: account.name,
      keyFingerprint: item.keyFingerprint,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      probeOutcome,
      durationMs: result.durationMs,
      deferred: deferred?.changed ?? false,
      message: result.message
    }, '账户内 API Key 复测未形成传输失败证据，已保留 Key 状态')
    return true
  }

  const failure = await requestBackgroundWorkerDbService({
    type: 'record_account_api_key_failure',
    account: fixedKeyCandidate,
    trafficSource: 'cooldown_retest',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'upstream_failure'
    },
    input: {
      status: 'temporary_unavailable',
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      errorMessage: result.message,
      traceId: result.traceId,
      observedAt: probeStartedAt,
      expectedStatus: item.status,
      expectedNextProbeAt: item.nextProbeAt,
      expectedStateUpdatedAt: item.stateUpdatedAt,
      expectedProbeClaimToken: item.probeClaimToken,
      expectedAccountConfigRevision: item.accountConfigRevision
    }
  }, backgroundProbeDbServiceTimeoutMs)
  logger.debug({
    event: 'background_account_api_key_cooldown_retest_failed',
    accountId: account.id,
    accountName: account.name,
    keyFingerprint: item.keyFingerprint,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    probeOutcome,
    durationMs: result.durationMs,
    changed: failure?.changed ?? false,
    message: result.message
  }, '账户内 API Key 复测未通过，已按 Key 运行态退避等待下次复测')
  return true
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope) {
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
