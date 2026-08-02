import type { Request } from 'express'
import { createHash, randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import {
  loadAccountCurrentConcurrencyByIdsAsync,
  tryAcquireAccountConcurrencyAsync,
  type AccountConcurrencyAcquireOptions,
  type AccountConcurrencySlot
} from '../../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../../domain/group-scheduling.js'
import type { ClientCompatibilityCapability, GroupSchedulingPolicy } from '../../../domain/types.js'
import { getRequestLogger, logRequestStage, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import {
  exponentialRetryPolicy,
  retryDelayMs,
  waitForRetryDelayMs,
} from '../../../shared/retry-policy.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  gatewayTimeoutProfileForLane,
  type GatewayTimeoutProfile
} from '../policy/timeout-profile.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  buildPreparedUpstreamRequestParts,
  handleUnavailableProxyProfile,
  prepareUpstreamAccount,
  selectAccountApiKeyForDispatch,
  skipAccountForFailedProxyDispatch
} from './account-preparation.js'
import {
  shouldRecordAbortedUpstreamAttempt,
  throwIfRequestAborted
} from './helpers.js'
import {
  filterGatewayAccountRuntimeSuppressionsAsync,
  orderGatewayAccountsByRuntimeDegradation,
  type GatewayAccountHalfOpenLease
} from '../runtime/account-side-effects.service.js'
import {
  notifyOneRecoverableUnavailableRuntimeWaiter,
  waitForRecoverableUnavailableState
} from '../runtime/recoverable-unavailable-wait.js'
import type { ClientIpAccountAvoidanceTracker } from '../runtime/client-ip-account-avoidance.service.js'
import {
  handleFailedUpstreamResponse,
  handleUpstreamRequestError,
  isOpaqueUpstreamFailoverAllowed,
  type PendingAccountApiKeyFailure
} from '../response/failure-dispatch.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { buildGatewayUpstreamUrlsForAccount } from '../../providers/drivers/registry.js'
import { forgetOpenAIAccountForSessionAsync, rememberOpenAIAccountForSessionAsync } from '../runtime/session-affinity.service.js'
import {
  isPrimaryStartedGatewayTransportError,
  performUpstreamRequestAttempt
} from './upstream-attempts.js'
import { type UpstreamAttempt } from '../upstream/attempt.js'
import { recordFailedUpstreamAttempt, type GatewayUsageContext } from '../usage/records.js'
import { isAccountProbeTrafficSource } from '../usage/traffic-source.js'
import { type GatewayUpstreamResponse } from '../upstream/request.js'
import { isProvenUpstreamBodyTransportError } from '../upstream/body.js'
import { isGatewayFirstByteTimeoutError } from '../upstream/first-byte-timeout.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { GatewayAgentGuidanceResponse, GatewayLocalProtocolResponse, GatewayRequestValidationError } from '../request/validation-error.js'
import {
  captureGatewayAccountApiKeyFailureObservation,
  recordGatewayAccountApiKeyFailure
} from '../runtime/account-api-key-effects.service.js'
import { waitForHighConcurrencyGroupCapacity } from '../runtime/high-concurrency-queue.service.js'
import {
  gatewayAccountDispatchPriorityTier,
  preserveGatewayAccountDispatchPriorityTiers
} from '../runtime/account-dispatch-priority-order.js'
import {
  gatewayAccountConcurrencyAccountId,
  gatewayAccountConcurrencyAccountIds,
  gatewayAccountConcurrencyLimitsByAccountId
} from './account-concurrency-identity.js'
import type { GatewayAccountModelPriority } from './model-filter.js'
import type { SpeedFirstCutoverReservation } from '../runtime/speed-first-cutover-reservation.service.js'
import type { UsageServiceTier } from '../usage/service-tier.js'
import type { ServerRetryBudget } from '../runtime/server-retry-budget.js'
import { requestModel } from '../request/metadata.js'
import { gatewayAccountRuntimeKey } from '../runtime/account-runtime-keys.js'
import {
  defaultGatewayFinalResponseReserveMs,
  gatewayAttemptProtocolModelKey,
  type GatewayRequestAttemptTracker,
  type GatewayRequestWallBudget,
  type RouteCoordinationBudget
} from '../routing/route-coordination.js'
import {
  getGatewayAccountCircuitService,
  type GatewayAccountCircuitAttempt,
  type GatewayAccountCircuitConfirmation,
  type GatewayAccountCircuitTransportFailure
} from '../runtime/account-circuit.service.js'
import {
  createGatewayHotQualityAttemptLifecycle,
  type GatewayHotQualityAttemptLifecycle
} from '../runtime/hot-quality-attempt-lifecycle.js'
import {
  normalRouteAttemptFirstByteDeadline,
  type NormalRouteAttemptFirstByteDeadline,
  type NormalRouteFirstByteRuntimeConfig
} from '../routing/normal-route-first-byte-deadline.js'
import { normalRouteFirstByteDeadlineAppliesToLane } from '../policy/speed-first-lane.js'
import type { FirstByteDeadlineDecisionInput, FirstByteDeadlineAction, FirstByteDeadlineHandler } from '../upstream/first-byte-deadline.js'
import { observeGatewayRouting } from '../observability/routing-observability.service.js'
import { codexCompactionExpectedForRequest } from '../response/codex-compaction-contract.js'
import { getGatewaySessionIdentity } from '../session-identity/index.js'

/**
 * Owns the optional speed-first reservation for exactly one physical upstream
 * attempt. A late deadline callback must never touch route-level state because
 * that state may already belong to the next attempt.
 */
export class NormalRouteFirstByteAttemptCoordinator {
  private state: 'active' | 'superseded' | 'transferred' = 'active'
  private reservation: SpeedFirstCutoverReservation | undefined

  attachReservation(reservation: SpeedFirstCutoverReservation): boolean {
    if (this.state !== 'active') {
      reservation.release()
      return false
    }
    const previous = this.reservation
    this.reservation = reservation
    previous?.release()
    return true
  }

  releaseReservation(): void {
    const reservation = this.reservation
    this.reservation = undefined
    reservation?.release()
  }

  supersede(): void {
    if (this.state !== 'active') return
    this.state = 'superseded'
    this.releaseReservation()
  }

  get canCutover(): boolean {
    return this.state === 'active' && this.reservation !== undefined
  }

  get reservedTargetAccountId(): string | undefined {
    return this.canCutover ? this.reservation?.targetAccountId : undefined
  }

  transferForCutover(): SpeedFirstCutoverReservation | undefined {
    if (this.state !== 'active') return undefined
    this.state = 'transferred'
    const reservation = this.reservation
    this.reservation = undefined
    return reservation
  }
}

export interface OpenAIUpstreamDispatchResult {
  account: UpstreamAccount
  response: GatewayUpstreamResponse
  upstreamUrl: string
  auditAttemptId: string
  attemptStartedAt: number
  effectiveServiceTier: UsageServiceTier
  timeoutProfile: GatewayTimeoutProfile
  releaseConcurrency: () => void
  markFirstOutput: () => void
  confirmSameAccountApiKeyFailures: () => Promise<void>
  confirmHalfOpenSuccess: () => Promise<boolean>
  releaseHalfOpenLease: () => Promise<boolean>
  accountCircuitAttempt?: GatewayAccountCircuitAttempt
  hotQualityAttempt: GatewayHotQualityAttemptLifecycle
  normalRouteFirstByteDeadline?: NormalRouteAttemptFirstByteDeadline
  responsePrecommitDeadlineAtMs?: number
  onFirstByteDeadline?: FirstByteDeadlineHandler
  firstByteDeadlineCoordinator?: NormalRouteFirstByteAttemptCoordinator
}

export interface GatewayUpstreamRequestCoordinationContext {
  scope: 'gateway_request' | 'internal_hybrid_auxiliary'
  reason?: string
  timeoutPolicy?: 'codex_compaction_unbounded'
  serverRetryBudget: ServerRetryBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
  routeCoordinationBudget: RouteCoordinationBudget
  requestAttemptTracker: GatewayRequestAttemptTracker
  semanticRetryId?: string
  normalRouteFirstByteConfig?: NormalRouteFirstByteRuntimeConfig
  onNormalRouteFirstByteDeadline?: (input: FirstByteDeadlineDecisionInput & {
    account: UpstreamAccount
    deadline: NormalRouteAttemptFirstByteDeadline
    coordinator: NormalRouteFirstByteAttemptCoordinator
  }) => FirstByteDeadlineAction | Promise<FirstByteDeadlineAction>
  /** Called once when an account attempt is about to invoke the upstream transport. */
  onUpstreamAttemptStarted?: (account: UpstreamAccount, upstreamUrl: string) => void | Promise<void>
}

export class UpstreamAttemptError extends Error {
  constructor(
    message: string,
    readonly lastAttempt?: UpstreamAttempt,
    readonly failedAccountIds: string[] = [],
    readonly agentGuidanceResponse?: GatewayAgentGuidanceResponse,
    readonly recoverableAccountIds: string[] = [],
    /** A concrete upstream failure that must be returned without candidate failover. */
    readonly terminalUpstreamFailure = false
  ) {
    super(message)
  }
}

export class NormalRouteFirstByteCutoverError extends Error {
  readonly code = 'normal_route_first_byte_timeout'

  constructor(
    readonly accountId: string,
    readonly accountName: string,
    readonly deadline: NormalRouteAttemptFirstByteDeadline,
    message: string,
    readonly cutoverReservation?: SpeedFirstCutoverReservation
  ) {
    super(message)
    this.name = 'NormalRouteFirstByteCutoverError'
  }
}

export class GatewayRequestWallBudgetExhaustedError extends Error {
  readonly code = 'gateway_request_wall_budget_exhausted'

  constructor(
    readonly wallRemainingMs: number,
    readonly minimumMeaningfulAttemptMs = 0
  ) {
    super('网关请求墙钟预算已进入最终响应预留区')
    this.name = 'GatewayRequestWallBudgetExhaustedError'
  }
}

interface AccountConcurrencyAcquireResult {
  slot: AccountConcurrencySlot
  retryCount: number
  waitedMs: number
  remainingWaitBudgetMs: number
}

interface AccountCapacityLimitFailure {
  account: UpstreamAccount
  message: string
}

const accountConcurrencyRetryBudgetMs = 1200
const accountConcurrencyRetryPolicy = exponentialRetryPolicy('gateway_account_concurrency_short_wait', 120, 480)
// A route may traverse multiple 50-key account pools. Bound total request fan-out
// while auditing that untried keys remain, rather than claiming pool exhaustion.
export const gatewayAccountApiKeyRequestAttemptSafetyLimit = 64

export async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext,
  auditCapture: AuditCaptureContext,
  sessionAffinityKey?: string,
  signal?: AbortSignal,
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker,
  requestLane: OpenAIGatewayRequestLane = 'text',
  groupSchedulingPolicy?: GroupSchedulingPolicy,
  accountStateMutationEnabled = true,
  requestClientCompatibility?: ClientCompatibilityCapability,
  modelPriority?: GatewayAccountModelPriority,
  preAcquiredConcurrency?: SpeedFirstCutoverReservation,
  allowPrecheckHalfOpen = false,
  requestCoordination?: GatewayUpstreamRequestCoordinationContext,
  interpretUpstreamResponseSemantics = false,
  waitForRecoverableFailures = true,
  accountCircuitConfirmation?: GatewayAccountCircuitConfirmation
): Promise<OpenAIUpstreamDispatchResult> {
  if (!requestCoordination) {
    throw new Error('fetchFirstAvailableUpstream requires shared request coordination context')
  }
  const {
    serverRetryBudget,
    gatewayRequestWallBudget,
    routeCoordinationBudget,
    requestAttemptTracker,
    semanticRetryId
  } = requestCoordination
  const compactionTimeoutsDisabled = requestCoordination.timeoutPolicy === 'codex_compaction_unbounded'
    || codexCompactionExpectedForRequest(req)
  const timeoutProfile = gatewayTimeoutProfileForLane(settings, requestLane, {
    disableTimeouts: compactionTimeoutsDisabled
  })
  const accountCircuitFailureEvidenceKey = gatewayForegroundAccountCircuitFailureEvidenceKey(
    req,
    usageContext
  )
  // Explicit user policies remain available for gateway clients. Automatic
  // suppression and health state transitions are reserved for background probes.
  const automaticAccountStateMutationAllowed = accountStateMutationEnabled
    && isAccountProbeTrafficSource(usageContext.trafficSource)
  let lastAttempt: UpstreamAttempt | undefined
  let agentGuidanceResponse: GatewayAgentGuidanceResponse | undefined
  let auditAttemptIndex = 0
  let concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
  let highConcurrencyDispatchQueueWaitCount = 0
  const failedProxyDispatchKeys = new Map<string, string>()
  const failedAccountIds = new Set<string>()
  const recoverableFailedAccountIds = new Set<string>()
  const bypassLocalSuppression = isAccountProbeTrafficSource(usageContext.trafficSource)
  const accountCircuitService = accountStateMutationEnabled && usageContext.trafficSource === 'gateway'
    ? getGatewayAccountCircuitService()
    : undefined
  const confirmationLeaseDurationMs = Math.max(
    timeoutProfile.firstResponseTimeoutMs,
    timeoutProfile.uncommittedAttemptMaxLifetimeMs
  ) + 5_000
  let dispatchAccounts = orderGatewayAccountsByRuntimeDegradation(
    await orderAccountsForRequestLaneAsync(accounts, requestLane, groupSchedulingPolicy, modelPriority),
    { modelRankByAccountId: modelPriority?.rankByAccountId }
  ).accounts
  if (accountCircuitConfirmation) {
    dispatchAccounts = dispatchAccounts.filter((account) => (
      gatewayAccountRuntimeKey(account) === accountCircuitConfirmation.accountRuntimeKey
    ))
  }
  dispatchAccounts = dispatchAccounts.filter((account) => requestAttemptTracker.canAttemptAccount({
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    physicalCredentialKey: accountPhysicalCredentialKey(account),
    matchingConfirmation: accountCircuitConfirmation?.accountRuntimeKey === gatewayAccountRuntimeKey(account),
    semanticRetryId
  }).allowed)
  const dispatchTierOptions = { modelRankByAccountId: modelPriority?.rankByAccountId }
  const primaryDispatchTier = dispatchAccounts[0]
    ? gatewayAccountDispatchPriorityTier(dispatchAccounts[0], dispatchTierOptions)
    : undefined
  const observedEscapedTiers = new Set<string>()
  let requestApiKeyAttemptCount = requestAttemptTracker.snapshot().attemptedKeyFingerprints.length

  while (dispatchAccounts.length > 0) {
    const cycleRecoverableAccountIds = new Set<string>()
    const capacityLimitFailures: AccountCapacityLimitFailure[] = []

    for (const originalAccount of dispatchAccounts) {
      throwIfRequestAborted(signal)
      let accountCircuitAttempt: GatewayAccountCircuitAttempt | undefined
      if (accountCircuitService) {
        const circuitPreparation = await accountCircuitService.prepareAttempt({
          account: originalAccount,
          requestLane,
          model: requestModel(req),
          confirmationLeaseDurationMs,
          confirmationEligible: !compactionTimeoutsDisabled,
          confirmationFailuresRequired: runtimeConfig.gateway.accountCircuitConfirmationFailuresRequired
            ?? settings.accountCircuitConfirmationFailuresRequired,
          confirmation: accountCircuitConfirmation,
          failureEvidenceKey: accountCircuitFailureEvidenceKey
        })
        if (circuitPreparation.outcome === 'blocked') {
          lastAttempt = accountCircuitBlockedAttempt(originalAccount, circuitPreparation.state.phase)
          getRequestLogger().debug({
            event: 'gateway_account_circuit_dispatch_skipped',
            accountId: originalAccount.id,
            accountRuntimeKey: gatewayAccountRuntimeKey(originalAccount),
            phase: circuitPreparation.state.phase,
            generation: circuitPreparation.state.generation,
            retryAtMs: circuitPreparation.state.retryAtMs,
            confirmationInFlight: circuitPreparation.state.lease?.kind === 'confirmation'
          }, '账户短电路阻止普通候选派发')
          auditCapture.addGatewayMetadata({
            label: 'account_circuit_dispatch_skip',
            metadata: {
              accountId: originalAccount.id,
              phase: circuitPreparation.state.phase,
              generation: circuitPreparation.state.generation,
              retryAtMs: circuitPreparation.state.retryAtMs,
              confirmationInFlight: circuitPreparation.state.lease?.kind === 'confirmation'
            }
          })
          continue
        }
        accountCircuitAttempt = circuitPreparation.attempt
      }
      let accountCircuitAttemptTransferred = false
      try {
      const localSuppression = bypassLocalSuppression
        ? {
            accounts: [originalAccount],
            suppressedCount: 0,
            allSuppressed: false,
            suppressedAccountIds: [],
            acquiredHalfOpenLeases: []
          }
        : await filterGatewayAccountRuntimeSuppressionsAsync([originalAccount], {
            acquireHalfOpenLease: true,
            acquirePrecheckHalfOpenLease: allowPrecheckHalfOpen,
            precheckHalfOpenGroupKey: `${usageContext.systemAccountId}:${usageContext.groupId}`
          })
      if (localSuppression.allSuppressed) {
        lastAttempt = locallySuppressedAttempt(originalAccount, localSuppression.nextRetryAfterMs)
        getRequestLogger().warn({
          event: 'gateway_local_account_suppression_dispatch_skip',
          accountId: originalAccount.id,
          accountName: originalAccount.name,
          nextRetryAfterMs: localSuppression.nextRetryAfterMs
        }, '账号在本次调度执行前已进入本地短期屏蔽，跳过当前账号')
        auditCapture.addGatewayMetadata({
          label: 'local_account_suppression_dispatch_skip',
          metadata: {
            accountId: originalAccount.id,
            nextRetryAfterMs: localSuppression.nextRetryAfterMs
          }
        })
        continue
      }
      const halfOpenLease = localSuppression.acquiredHalfOpenLeases[0]
      const skippedProxyAttempt = skipAccountForFailedProxyDispatch(failedProxyDispatchKeys, originalAccount)
      if (skippedProxyAttempt) {
        await releaseHalfOpenLease(halfOpenLease)
        lastAttempt = skippedProxyAttempt
        failedAccountIds.add(originalAccount.id)
        if (recoverableFailedAccountIds.has(originalAccount.id)) {
          cycleRecoverableAccountIds.add(originalAccount.id)
        }
        continue
      }
      const unavailableProxyAuditAttemptIndex = auditAttemptIndex + 1
      const unavailableProxyAttempt = await handleUnavailableProxyProfile(
        req,
        usageContext,
        originalAccount,
        settings,
        failedProxyDispatchKeys,
        automaticAccountStateMutationAllowed,
        undefined,
        auditCapture,
        unavailableProxyAuditAttemptIndex
      )
      if (unavailableProxyAttempt) {
        auditAttemptIndex = unavailableProxyAuditAttemptIndex
        await releaseHalfOpenLease(halfOpenLease)
        lastAttempt = unavailableProxyAttempt
        failedAccountIds.add(originalAccount.id)
        continue
      }
      const concurrencyAccountId = gatewayAccountConcurrencyAccountId(originalAccount)
      const concurrencyStageStartedAt = performance.now()
      let concurrencyAcquire: AccountConcurrencyAcquireResult
      const reservedSlot = preAcquiredConcurrency?.takeForAccount(originalAccount)
      if (reservedSlot) {
        concurrencyAcquire = {
          slot: reservedSlot,
          retryCount: 0,
          waitedMs: 0,
          remainingWaitBudgetMs: concurrencyRetryWaitBudgetMs
        }
      } else {
        try {
          concurrencyAcquire = await acquireAccountConcurrencyWithShortRetry(
            concurrencyAccountId,
            originalAccount.concurrencyLimit,
            concurrencyRetryWaitBudgetMs,
            signal,
            requestLane,
            groupSchedulingPolicy,
            serverRetryBudget
          )
        } catch (error) {
          await releaseHalfOpenLease(halfOpenLease)
          logRequestStage('account.concurrency_acquire', {
            traceId: usageContext.traceId,
            accountId: originalAccount.id,
            concurrencyAccountId,
            error
          }, signal?.aborted ? 'aborted' : 'unexpected_failure', concurrencyStageStartedAt)
          throw error
        }
      }
      concurrencyRetryWaitBudgetMs = concurrencyAcquire.remainingWaitBudgetMs
      const concurrencySlot = concurrencyAcquire.slot
      logRequestStage('account.concurrency_acquire', {
        traceId: usageContext.traceId,
        accountId: originalAccount.id,
        concurrencyAccountId,
        acquired: concurrencySlot.acquired,
        current: concurrencySlot.current,
        limit: concurrencySlot.limit,
        retryCount: concurrencyAcquire.retryCount,
        waitedMs: concurrencyAcquire.waitedMs,
        reserved: Boolean(reservedSlot),
        ...(!concurrencySlot.acquired ? {
          failureReason: 'account_concurrency_limit',
          decisionInputs: {
            current: concurrencySlot.current,
            limit: concurrencySlot.limit,
            waitedMs: concurrencyAcquire.waitedMs,
            retryCount: concurrencyAcquire.retryCount,
            remainingWaitBudgetMs: concurrencyAcquire.remainingWaitBudgetMs
          }
        } : {})
      }, concurrencySlot.acquired ? 'success' : 'expected_failure', concurrencyStageStartedAt)
      if (!concurrencySlot.acquired) {
        await releaseHalfOpenLease(halfOpenLease)
        const message = concurrencyAcquire.waitedMs > 0
          ? accountConcurrencyLimitMessage(concurrencySlot, concurrencyAcquire.waitedMs)
          : accountConcurrencyLimitMessage(concurrencySlot)
        lastAttempt = accountCapacityLimitAttempt(originalAccount, message)
        capacityLimitFailures.push({ account: originalAccount, message })
        continue
      }
      if (concurrencyAcquire.waitedMs > 0) {
        getRequestLogger().info({
          event: 'gateway_account_concurrency_acquired_after_wait',
          accountId: originalAccount.id,
          accountConcurrencyAccountId: concurrencyAccountId,
          accountName: originalAccount.name,
          retryCount: concurrencyAcquire.retryCount,
          waitedMs: concurrencyAcquire.waitedMs,
          current: concurrencySlot.current,
          limit: concurrencySlot.limit
        }, '账号并发槽短等后释放，继续使用当前账号')
      }
      let keepConcurrencySlot = false
      const excludedApiKeyFingerprints = new Set(requestAttemptTracker.snapshot().attemptedKeyFingerprints)
      const pendingApiKeyFailures: PendingAccountApiKeyFailure[] = []
      let accountApiKeyAttemptCount = 0
      let previousSelectedApiKeyFingerprint: string | undefined
      try {
        let retryAccountApiKey = false
        do {
          retryAccountApiKey = false
          let skipAccount = false
          let account = originalAccount
          let headers: Headers
          let body: Buffer | string | undefined
          let effectiveServiceTier = usageContext.effectiveServiceTier ?? usageContext.requestedServiceTier ?? 'default'
          let upstreamUrls: string[]
          const preparationStartedAt = Date.now()
          const preparationStageStartedAt = performance.now()
          try {
            account = await prepareUpstreamAccount(originalAccount, signal)
            upstreamUrls = buildGatewayUpstreamUrlsForAccount(account, req)
            if (upstreamUrls.length === 0) {
              logRequestStage('upstream.request_prepare', {
                traceId: usageContext.traceId,
                accountId: account.id,
                providerCode: account.providerCode,
                failureReason: 'upstream_url_unavailable',
                decisionInputs: { accountType: account.type }
              }, 'expected_failure', preparationStageStartedAt)
              break
            }
            if (
              (account.apiKeys?.length ?? 0) > 1
              && requestApiKeyAttemptCount >= gatewayAccountApiKeyRequestAttemptSafetyLimit
            ) {
              recordAccountApiKeyRequestRetryBudgetExhausted(account, {
                accountApiKeyAttemptCount,
                requestApiKeyAttemptCount,
                remainingConfiguredKeyCount: Math.max(0, (account.apiKeys?.length ?? 0) - accountApiKeyAttemptCount),
                reason: 'request_safety_limit'
              }, auditCapture)
              lastAttempt = accountApiKeyRetryBudgetExhaustedAttempt(
                account,
                '请求级 API Key 尝试安全上限已达到，未宣称 Key 池已穷尽'
              )
              failedAccountIds.add(account.id)
              break
            }
            const selectedAccount = await selectAccountApiKeyForDispatch(account, {
              excludeFingerprints: excludedApiKeyFingerprints,
              continueAfterFingerprint: previousSelectedApiKeyFingerprint
            })
            if (!selectedAccount) {
              logRequestStage('upstream.request_prepare', {
                traceId: usageContext.traceId,
                accountId: account.id,
                providerCode: account.providerCode,
                failureReason: 'account_api_key_pool_unavailable',
                decisionInputs: {
                  excludedApiKeyCount: excludedApiKeyFingerprints.size,
                  accountApiKeyAttemptCount
                }
              }, 'expected_failure', preparationStageStartedAt)
              lastAttempt = accountApiKeyPoolUnavailableAttempt(account)
              failedAccountIds.add(account.id)
              auditCapture.addGatewayMetadata({
                label: 'account_api_key_pool_unavailable_dispatch_skip',
                metadata: {
                  accountId: account.id,
                  accountName: account.name
                }
              })
              break
            }
            account = selectedAccount
            if (account.selectedApiKeyFingerprint) {
              accountApiKeyAttemptCount += 1
              excludedApiKeyFingerprints.add(account.selectedApiKeyFingerprint)
              previousSelectedApiKeyFingerprint = account.selectedApiKeyFingerprint
            }
            const requestParts = await buildPreparedUpstreamRequestParts(req, account, usageContext, signal, {
              requestClientCompatibility
            })
            headers = requestParts.headers
            body = requestParts.body
            effectiveServiceTier = requestParts.effectiveServiceTier
            usageContext.effectiveServiceTier = effectiveServiceTier
            usageContext.effectiveReasoningEffort = requestParts.effectiveReasoningEffort
            logRequestStage('upstream.request_prepare', {
              traceId: usageContext.traceId,
              accountId: account.id,
              providerCode: account.providerCode,
              protocolCode: account.protocolCode,
              upstreamEndpointCount: upstreamUrls.length,
              requestBodyBytes: typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : body?.byteLength,
              proxyEnabled: Boolean(account.proxyUrl)
            }, 'success', preparationStageStartedAt)
          } catch (error) {
            const expectedPreparationFailure = error instanceof GatewayAgentGuidanceResponse
              || error instanceof GatewayLocalProtocolResponse
              || error instanceof OpenAIOAuthCodexAdapterError
              || error instanceof GatewayRequestValidationError
            logRequestStage('upstream.request_prepare', {
              traceId: usageContext.traceId,
              accountId: account.id,
              providerCode: account.providerCode,
              error,
              ...(expectedPreparationFailure ? {
                failureReason: error instanceof Error ? error.name : 'upstream_request_prepare_failed',
                decisionInputs: {
                  accountScoped: 'accountScoped' in error ? error.accountScoped : undefined,
                  accountType: account.type,
                  protocolCode: account.protocolCode
                }
              } : {})
            }, signal?.aborted
              ? 'aborted'
              : expectedPreparationFailure
                ? 'expected_failure'
                : 'unexpected_failure', preparationStageStartedAt)
            if (error instanceof GatewayAgentGuidanceResponse && error.accountScoped) {
              lastAttempt = accountScopedGuidanceAttempt(account, error)
              agentGuidanceResponse = error
              failedAccountIds.add(account.id)
              getRequestLogger().info({
                event: 'gateway_account_scoped_agent_guidance_dispatch_skip',
                accountId: account.id,
                accountName: account.name,
                providerCode: account.providerCode,
                providerProtocolProfileId: account.providerProtocolProfileId,
                protocolCode: account.protocolCode,
                protocolVersion: account.protocolVersion,
                guidanceCode: error.code,
                guidanceProtocol: error.protocol,
                guidanceModel: error.model
              }, '当前账号目标协议无法承载请求能力，跳过当前账号并继续调度')
              auditCapture.addGatewayMetadata({
                label: 'account_scoped_agent_guidance_dispatch_skip',
                metadata: {
                  accountId: account.id,
                  accountName: account.name,
                  providerCode: account.providerCode,
                  providerProtocolProfileId: account.providerProtocolProfileId,
                  protocolCode: account.protocolCode,
                  protocolVersion: account.protocolVersion,
                  guidanceCode: error.code,
                  guidanceProtocol: error.protocol,
                  guidanceModel: error.model
                }
              })
              continue
            }
            if (
              signal?.aborted
              || error instanceof GatewayAgentGuidanceResponse
              || error instanceof GatewayLocalProtocolResponse
              || (error instanceof OpenAIOAuthCodexAdapterError && !error.accountScoped)
              || (error instanceof GatewayRequestValidationError && !error.accountScoped)
            ) {
              throw error
            }
            auditAttemptIndex += 1
            const requestErrorResult = await handleUpstreamRequestError({
              req,
              usageContext,
              auditCapture,
              auditAttemptId: '',
              account,
              upstreamUrl: 'account:preparation',
              attemptStartedAt: preparationStartedAt,
              attemptIndex: 0,
              auditAttemptIndex,
              settings,
              sessionAffinityKey,
              signal,
              lastAttempt,
              failedProxyDispatchKeys,
              error,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: automaticAccountStateMutationAllowed
            })
            lastAttempt = requestErrorResult.lastAttempt ?? lastAttempt
            if (requestErrorResult.action === 'skip_account') {
              failedAccountIds.add(account.id)
              continue
            }
            failedAccountIds.add(account.id)
            if (halfOpenLease?.generation === undefined && shouldRetryAnotherAccountApiKey(
              account,
              requestErrorResult.keyScopedFailure,
              accountApiKeyAttemptCount,
              requestApiKeyAttemptCount,
              auditCapture
            )) {
              retryAccountApiKey = true
            }
            continue
          }
          for (const upstreamUrl of upstreamUrls) {
            for (let attemptIndex = 0; ; attemptIndex += 1) {
              assertGatewayRequestWallBudgetAvailableForAttempt(
                gatewayRequestWallBudget,
                requestAttemptTracker,
                auditCapture,
                requestCoordination.scope === 'gateway_request' ? defaultGatewayFinalResponseReserveMs : 0
              )
              const accountRuntimeKey = gatewayAccountRuntimeKey(account)
              const dispatchAttemptIdentity = {
                accountRuntimeKey,
                physicalCredentialKey: accountPhysicalCredentialKey(account),
                protocolModelKey: gatewayAttemptProtocolModelKey({
                  accountRuntimeKey,
                  protocolCode: account.protocolCode,
                  protocolVersion: account.protocolVersion,
                  model: requestModel(req)
                }),
                keyFingerprint: account.selectedApiKeyFingerprint
              }
              const attemptRegistration = requestAttemptTracker.tryRecordDispatchAttempt(
                {
                  ...dispatchAttemptIdentity,
                  matchingConfirmation: accountCircuitAttempt?.isConfirmation === true,
                  allowKeyRotation: accountApiKeyAttemptCount > 1,
                  semanticRetryId
                }
              )
              if (!attemptRegistration.allowed) {
                lastAttempt = requestDeduplicatedAttempt(account, attemptRegistration.reason)
                failedAccountIds.add(account.id)
                auditCapture.addGatewayMetadata({
                  label: 'gateway_request_attempt_deduplicated',
                  metadata: {
                    accountId: account.id,
                    accountRuntimeKey,
                    physicalCredentialKey: accountPhysicalCredentialKey(account),
                    keyFingerprint: account.selectedApiKeyFingerprint,
                    reason: attemptRegistration.reason,
                    coordinationScope: requestCoordination.scope
                  }
                })
                skipAccount = true
                break
              }
              if (account.selectedApiKeyFingerprint) {
                requestApiKeyAttemptCount += 1
              }
              const attemptTier = gatewayAccountDispatchPriorityTier(account, dispatchTierOptions)
              if (primaryDispatchTier && attemptTier !== primaryDispatchTier && !observedEscapedTiers.has(attemptTier)) {
                observedEscapedTiers.add(attemptTier)
                observeGatewayRouting({ kind: 'tier_escape', outcome: 'applied' })
              }
              const attemptStartedAt = Date.now()
              // Long-running lanes (for example image generation) keep their
              // own timeout profile. Unknown failures still remain terminal
              // for the current request regardless of lane timing.
              const normalRouteFirstByteDeadline = !compactionTimeoutsDisabled
                && normalRouteFirstByteDeadlineAppliesToLane(requestLane)
                && requestCoordination.normalRouteFirstByteConfig
                ? normalRouteAttemptFirstByteDeadline({
                    config: requestCoordination.normalRouteFirstByteConfig,
                    gatewayRequestWallBudget,
                    attemptStartedAtMs: attemptStartedAt,
                    laneFirstByteTimeoutMs: timeoutProfile.firstByteTimeoutMs,
                    uncommittedAttemptMaxLifetimeMs: timeoutProfile.uncommittedAttemptMaxLifetimeMs
                  })
                : undefined
              const firstByteDeadlineCoordinator = normalRouteFirstByteDeadline
                ? new NormalRouteFirstByteAttemptCoordinator()
                : undefined
              let firstByteDeadlineDecision: Promise<FirstByteDeadlineAction> | undefined
              let firstByteDeadlineTriggered = false
              const onFirstByteDeadline: FirstByteDeadlineHandler | undefined = normalRouteFirstByteDeadline
                ? (deadlineInput) => {
                    firstByteDeadlineTriggered = true
                    firstByteDeadlineDecision ??= Promise.resolve(
                      requestCoordination.onNormalRouteFirstByteDeadline?.({
                        ...deadlineInput,
                        account,
                        deadline: normalRouteFirstByteDeadline,
                        coordinator: firstByteDeadlineCoordinator!
                      }) ?? 'abort'
                    )
                    return firstByteDeadlineDecision
                  }
                : undefined
              auditAttemptIndex += 1
              const auditAttemptId = auditCapture.startAttempt({
                account,
                attemptIndex: auditAttemptIndex,
                upstreamUrl,
                method: req.method,
                headers,
                body,
                requestForModelAccounting: req
              })
              let hotQualityAttempt: GatewayHotQualityAttemptLifecycle | undefined
              const getHotQualityAttempt = () => {
                if (hotQualityAttempt) return hotQualityAttempt
                const rawAttempt = createGatewayHotQualityAttemptLifecycle({
                  // Audit capture may be disabled and return an empty id. Keep the
                  // runtime attempt identity unique across every actual upstream
                  // attempt so terminal idempotency cannot collapse retries.
                  attemptId: `hotq:${usageContext.traceId}:${attemptIndex}:${auditAttemptIndex}:${randomUUID()}`,
                  account,
                  requestLane,
                  model: requestModel(req)
                })
                hotQualityAttempt = hotQualityAttemptForCircuitMode(rawAttempt, accountCircuitAttempt)
                return hotQualityAttempt
              }
              try {
                // Settlement/observability must not add latency to the upstream
                // request or turn a successful dispatch into a gateway failure.
                void Promise.resolve(requestCoordination.onUpstreamAttemptStarted?.(account, upstreamUrl)).catch((error) => {
                  getRequestLogger().warn({
                    event: 'gateway_upstream_attempt_started_callback_failed',
                    accountId: account.id,
                    error
                  }, '上游 attempt started 回调失败')
                })
              } catch (error) {
                getRequestLogger().warn({
                  event: 'gateway_upstream_attempt_started_callback_failed',
                  accountId: account.id,
                  error
                }, '上游 attempt started 回调失败')
              }
              try {
                const response = await performUpstreamRequestAttempt({
                  req,
                  account,
                  upstreamUrl,
                  attemptIndex,
                  auditAttemptIndex,
                  headers,
                  body,
                  timeoutProfile,
                  attemptStartedAt,
                  firstByteDeadlineMs: normalRouteFirstByteDeadline?.effectiveDeadlineMs,
                  onFirstByteDeadline,
                  signal,
                  requestClientCompatibility
                })
                lastAttempt = {
                  accountId: account.id,
                  accountName: account.name,
                  providerCode: account.providerCode,
                  providerProtocolProfileId: account.providerProtocolProfileId,
                  protocolCode: account.protocolCode,
                  protocolVersion: account.protocolVersion,
                  upstreamUrl,
                  status: response.status
                }
                if (response.ok) {
                  await rememberOpenAIAccountForSessionAsync(sessionAffinityKey, account.id, {
                    systemAccountId: usageContext.systemAccountId,
                    apiKeyId: usageContext.apiKeyId,
                    groupId: usageContext.groupId
                  })
                  const dispatchResult: OpenAIUpstreamDispatchResult = {
                    account,
                    response,
                    upstreamUrl,
                    auditAttemptId,
                    attemptStartedAt,
                    effectiveServiceTier,
                    timeoutProfile,
                    releaseConcurrency: releaseAccountDispatchSlot(concurrencySlot.release),
                    markFirstOutput: concurrencySlot.markFirstOutput,
                    confirmSameAccountApiKeyFailures: () => recordConfirmedSameAccountApiKeyFailures(pendingApiKeyFailures, account, usageContext),
                    confirmHalfOpenSuccess: () => automaticAccountStateMutationAllowed
                      ? completeHalfOpenLeaseSuccess(halfOpenLease)
                      : Promise.resolve(false),
                    releaseHalfOpenLease: () => releaseHalfOpenLease(halfOpenLease),
                    accountCircuitAttempt,
                    hotQualityAttempt: getHotQualityAttempt(),
                    normalRouteFirstByteDeadline,
                    responsePrecommitDeadlineAtMs: requestLane === 'image' || gatewayRequestWallBudget.unbounded
                      ? undefined
                      : gatewayRequestWallBudget.deadlineAtMs - defaultGatewayFinalResponseReserveMs,
                    onFirstByteDeadline,
                    firstByteDeadlineCoordinator
                  }
                  keepConcurrencySlot = true
                  accountCircuitAttemptTransferred = true
                  return dispatchResult
                }

                firstByteDeadlineCoordinator?.supersede()
                const failedResponseInput = {
                  req,
                  requestLane,
                  usageContext,
                  auditCapture,
                  auditAttemptId,
                  account,
                  upstreamUrl,
                  response,
                  settings,
                  attemptStartedAt,
                  attemptIndex,
                  auditAttemptIndex,
                  sessionAffinityKey,
                  signal,
                  lastAttempt,
                  clientIpAccountAvoidanceTracker,
                  accountStateMutationEnabled,
                  automaticAccountStateMutationEnabled: automaticAccountStateMutationAllowed
                }
                const failedResponseResult = await handleFailedUpstreamResponse(failedResponseInput)
                const explicitPolicyFailure = failedResponseResult.action !== 'return_response'
                  && failedResponseResult.failureKind === 'explicit_policy'
                if (failedResponseResult.action !== 'return_response') {
                  await getHotQualityAttempt().recordTerminal({
                    outcomeClass: explicitPolicyFailure ? 'explicit_policy_failure' : 'upstream_response_failure',
                    failureScope: explicitPolicyFailure ? 'account' : 'none',
                    source: explicitPolicyFailure ? 'explicit_policy' : 'upstream_response'
                  })
                }
                if (failedResponseResult.action === 'return_response') {
                  const dispatchResult: OpenAIUpstreamDispatchResult = {
                    account,
                    response: failedResponseResult.response,
                    upstreamUrl,
                    auditAttemptId,
                    attemptStartedAt,
                    effectiveServiceTier,
                    timeoutProfile,
                    releaseConcurrency: releaseAccountDispatchSlot(concurrencySlot.release),
                    markFirstOutput: concurrencySlot.markFirstOutput,
                    confirmSameAccountApiKeyFailures: () => Promise.resolve(),
                    confirmHalfOpenSuccess: () => Promise.resolve(false),
                    releaseHalfOpenLease: () => releaseHalfOpenLease(halfOpenLease),
                    accountCircuitAttempt,
                    hotQualityAttempt: getHotQualityAttempt(),
                    normalRouteFirstByteDeadline,
                    responsePrecommitDeadlineAtMs: requestLane === 'image' || gatewayRequestWallBudget.unbounded
                      ? undefined
                      : gatewayRequestWallBudget.deadlineAtMs - defaultGatewayFinalResponseReserveMs,
                    onFirstByteDeadline
                  }
                  keepConcurrencySlot = true
                  accountCircuitAttemptTransferred = true
                  return dispatchResult
                }
                // A complete HTTP frame is transport evidence even when an
                // explicit user policy independently applies a business action.
                await accountCircuitAttempt?.reportFramingComplete()
                lastAttempt = failedResponseResult.lastAttempt
                failedAccountIds.add(account.id)
                recoverableFailedAccountIds.delete(account.id)
                cycleRecoverableAccountIds.delete(account.id)
                if (
                  !firstByteDeadlineTriggered
                  && halfOpenLease?.generation === undefined
                  && (
                    failedResponseResult.tryNextApiKeyForRequest
                      ? shouldTryAnotherAccountApiKeyForRequest(
                          account,
                          accountApiKeyAttemptCount,
                          requestApiKeyAttemptCount,
                          auditCapture
                        )
                      : shouldRetryAnotherAccountApiKey(
                          account,
                          failedResponseResult.keyScopedFailure,
                          accountApiKeyAttemptCount,
                          requestApiKeyAttemptCount,
                          auditCapture
                        )
                  )
                ) {
                  if (failedResponseResult.pendingApiKeyFailure) {
                    pendingApiKeyFailures.push(failedResponseResult.pendingApiKeyFailure)
                  }
                  retryAccountApiKey = true
                  break
                }
                skipAccount = true
                break
              } catch (error) {
                const configuredFirstByteDeadline = isGatewayFirstByteTimeoutError(error)
                  && error.source === 'configured_deadline'
                const neutralFirstByteDeadline = configuredFirstByteDeadline
                  && (
                    normalRouteFirstByteDeadline?.limitingFactor === 'configured'
                    || normalRouteFirstByteDeadline?.limitingFactor === 'wall_precommit'
                  )
                const configuredDeadlineCutover = neutralFirstByteDeadline
                  && normalRouteFirstByteDeadline?.limitingFactor === 'configured'
                if (!configuredDeadlineCutover) {
                  firstByteDeadlineCoordinator?.supersede()
                }
                const localRequestFailure = error instanceof GatewayAgentGuidanceResponse
                  || error instanceof GatewayLocalProtocolResponse
                  || error instanceof GatewayRequestValidationError
                  || error instanceof OpenAIOAuthCodexAdapterError
                const primaryStartedTransportFailure = isPrimaryStartedGatewayTransportError(error)
                const provenBodyTransportFailure = isProvenUpstreamBodyTransportError(error)
                const provenStartedTransportFailure = primaryStartedTransportFailure
                  || provenBodyTransportFailure
                const pendingKeyTransportFailure = accountStateMutationEnabled
                  && usageContext.trafficSource === 'gateway'
                  && !localRequestFailure
                  && provenStartedTransportFailure
                  && !firstByteDeadlineTriggered
                  && halfOpenLease?.generation === undefined
                  && !isGatewayFirstByteTimeoutError(error)
                  && shouldRetainTransportFailureForRecovery(upstreamUrl, signal)
                  && account.selectedApiKeyFingerprint
                  && !account.apiKeyRuntimeStateDisabled
                  && (account.apiKeys?.length ?? 0) > accountApiKeyAttemptCount
                  && requestApiKeyAttemptCount < gatewayAccountApiKeyRequestAttemptSafetyLimit
                  ? {
                      account,
                      status: 'temporary_unavailable' as const,
                      observationEpoch: captureGatewayAccountApiKeyFailureObservation(account),
                      errorMessage: error instanceof Error ? error.message : undefined
                    }
                  : undefined
                const retryAnotherAccountApiKey = isOpaqueUpstreamFailoverAllowed(req) && !localRequestFailure
                  && provenStartedTransportFailure
                  && !neutralFirstByteDeadline
                  && !signal?.aborted
                  && !firstByteDeadlineTriggered
                  && halfOpenLease?.generation === undefined
                  && shouldTryAnotherAccountApiKeyForRequest(
                    account,
                    accountApiKeyAttemptCount,
                    requestApiKeyAttemptCount,
                    auditCapture
                  )
                const deferredConfirmationFailure = retryAnotherAccountApiKey
                  && accountCircuitAttempt?.deferConfirmationTransportFailureForKeyRotation() === true
                const confirmedTransportQuality = accountCircuitAttempt?.isConfirmation === true
                  && !localRequestFailure
                  && provenStartedTransportFailure
                  && !neutralFirstByteDeadline
                  && !deferredConfirmationFailure
                await getHotQualityAttempt().recordTerminal(confirmedTransportQuality
                  ? hotQualityTerminalForDispatchError(error, signal)
                  : { outcomeClass: 'unknown', failureScope: 'none', source: 'request_lifecycle' })
                if (signal?.aborted) {
                  // A client can disconnect after this account was selected but
                  // before response headers are returned to routes.ts. In that
                  // window the outer lifecycle does not yet know which account
                  // owns the active affinity, so release it here.
                  await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
                }
                if (error instanceof GatewayAgentGuidanceResponse && error.accountScoped) {
                  auditCapture.completeAttempt(auditAttemptId, {
                    success: false,
                    errorPhase: 'request_validation',
                    errorMessage: error.message
                  })
                  lastAttempt = accountScopedGuidanceAttempt(account, error)
                  agentGuidanceResponse = error
                  failedAccountIds.add(account.id)
                  skipAccount = true
                  break
                }
                if (
                  error instanceof GatewayAgentGuidanceResponse
                  || error instanceof GatewayLocalProtocolResponse
                  || error instanceof GatewayRequestValidationError
                  || error instanceof OpenAIOAuthCodexAdapterError
                ) {
                  auditCapture.completeAttempt(auditAttemptId, {
                    success: false,
                    errorPhase: 'request_validation',
                    errorMessage: error.message
                  })
                  throw error
                }
                if (neutralFirstByteDeadline && normalRouteFirstByteDeadline) {
                  const message = error instanceof Error ? error.message : String(error)
                  auditCapture.completeAttempt(auditAttemptId, {
                    success: false,
                    errorPhase: 'upstream_request',
                    errorCode: 'normal_route_first_byte_timeout',
                    errorMessage: message
                  })
                  await recordFailedUpstreamAttempt(req, usageContext, account, {
                    upstreamUrl,
                    startedAt: attemptStartedAt,
                    errorMessage: message,
                    failureAttribution: 'gateway_policy',
                    interpretUpstreamSemantics: false
                  })
                  await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
                  await accountCircuitAttempt?.reportUnknown()
                  if (normalRouteFirstByteDeadline.limitingFactor === 'wall_precommit') {
                    firstByteDeadlineCoordinator?.supersede()
                    throw new GatewayRequestWallBudgetExhaustedError(gatewayRequestWallBudget.remainingMs())
                  }
                  const cutoverReservation = firstByteDeadlineCoordinator?.transferForCutover()
                  throw new NormalRouteFirstByteCutoverError(
                    account.id,
                    account.name,
                    normalRouteFirstByteDeadline,
                    message,
                    cutoverReservation
                  )
                }
                if (signal?.aborted && shouldRecordAbortedUpstreamAttempt(error)) {
                  await handleUpstreamRequestError({
                    req,
                    usageContext,
                    auditCapture,
                    auditAttemptId,
                    account,
                    upstreamUrl,
                    attemptStartedAt,
                    attemptIndex,
                    auditAttemptIndex,
                    settings,
                    sessionAffinityKey,
                    signal,
                    lastAttempt,
                    failedProxyDispatchKeys,
                    error,
                    clientIpAccountAvoidanceTracker,
                    accountStateMutationEnabled: automaticAccountStateMutationAllowed
                  })
                  throw error
                }
                if (!provenStartedTransportFailure) {
                  const message = error instanceof Error ? error.message : String(error)
                  auditCapture.completeAttempt(auditAttemptId, {
                    success: false,
                    errorPhase: 'gateway_local_dispatch',
                    errorCode: 'unproven_upstream_transport_failure',
                    errorMessage: message
                  })
                  auditCapture.addGatewayMetadata({
                    label: 'gateway_unproven_upstream_transport_failure',
                    metadata: {
                      accountId: account.id,
                      keyFingerprint: account.selectedApiKeyFingerprint,
                      requestLane,
                      endpoint: usageContext.endpoint
                    }
                  })
                  await accountCircuitAttempt?.reportUnknown()
                  throw error
                }
                const requestErrorResult = await handleUpstreamRequestError({
                  req,
                  usageContext,
                  auditCapture,
                  auditAttemptId,
                  account,
                  upstreamUrl,
                  attemptStartedAt,
                  attemptIndex,
                  auditAttemptIndex,
                  settings,
                  sessionAffinityKey,
                  signal,
                  lastAttempt,
                  failedProxyDispatchKeys,
                  error,
                  clientIpAccountAvoidanceTracker,
                  accountStateMutationEnabled: automaticAccountStateMutationAllowed
                })
                lastAttempt = requestErrorResult.lastAttempt ?? lastAttempt
                if (requestErrorResult.action === 'skip_account') {
                  const requestTransportFailure = accountCircuitTransportFailure(error, lastAttempt?.message)
                  if (lastAttempt) {
                    lastAttempt.transportFailureKind = requestTransportFailure.kind === 'timeout'
                      ? 'timeout'
                      : 'connection'
                  }
                  if (accountCircuitAttempt && !signal?.aborted && !deferredConfirmationFailure) {
                    await accountCircuitAttempt.reportTransportFailure(requestTransportFailure)
                  }
                  failedAccountIds.add(account.id)
                  skipAccount = true
                  break
                }
                if (!retryAnotherAccountApiKey) {
                  failedAccountIds.add(account.id)
                }
                if (accountCircuitAttempt && !signal?.aborted && !deferredConfirmationFailure) {
                  await accountCircuitAttempt.reportTransportFailure(accountCircuitTransportFailure(error, lastAttempt?.message))
                }
                if (retryAnotherAccountApiKey) {
                  if (pendingKeyTransportFailure) {
                    pendingApiKeyFailures.push(pendingKeyTransportFailure)
                  }
                  retryAccountApiKey = true
                  break
                }
                skipAccount = true
                break
              }
            }
            if (skipAccount || retryAccountApiKey) {
              break
            }
          }
        } while (retryAccountApiKey)
      } finally {
        if (!keepConcurrencySlot) {
          concurrencySlot.release()
          await releaseHalfOpenLease(halfOpenLease)
        }
      }
      } finally {
        if (!accountCircuitAttemptTransferred) {
          await settleUndispatchedAccountCircuitAttempt(accountCircuitAttempt, originalAccount.id)
        }
      }
    }

    if (capacityLimitFailures.length > 0 && canUseHighConcurrencyDispatchQueue(groupSchedulingPolicy)) {
      const queueWaitStartedAtMs = Date.now()
      serverRetryBudget.beginNoAvailableWait(queueWaitStartedAtMs)
      let queueWait: Awaited<ReturnType<typeof waitForHighConcurrencyGroupCapacity>>
      try {
        queueWait = await waitForHighConcurrencyGroupCapacity({
          systemAccountId: usageContext.systemAccountId,
          groupId: usageContext.groupId,
          apiKeyId: usageContext.apiKeyId,
          accountIds: gatewayAccountConcurrencyAccountIds(dispatchAccounts),
          accountConcurrencyLimits: gatewayAccountConcurrencyLimitsByAccountId(dispatchAccounts),
          lane: requestLane,
          policy: groupSchedulingPolicy,
          maxWaitMs: serverRetryBudget.remainingMs(queueWaitStartedAtMs),
          signal
        })
      } finally {
        serverRetryBudget.pauseNoAvailableWait()
      }
      highConcurrencyDispatchQueueWaitCount += 1
      auditCapture.addGatewayMetadata({
        label: 'high_concurrency_dispatch_queue',
        metadata: {
          ...queueWait,
          lane: requestLane,
          waitCount: highConcurrencyDispatchQueueWaitCount,
          source: 'account_concurrency_acquire'
        }
      })
      throwIfRequestAborted(signal)
      if (queueWait.ready) {
        concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
        dispatchAccounts = orderGatewayAccountsByRuntimeDegradation(
          await orderAccountsForRequestLaneAsync(dispatchAccounts, requestLane, groupSchedulingPolicy, modelPriority),
          { modelRankByAccountId: modelPriority?.rankByAccountId }
        ).accounts
        continue
      }
      if (!serverRetryBudget.handoffRequired('recoverable_later')) {
        if (queueWait.reason !== 'timeout') {
          const retryDelayMs = Math.min(1000, serverRetryBudget.remainingMs())
          serverRetryBudget.beginNoAvailableWait()
          try {
            await waitForRetryDelayMs(retryDelayMs, { signal })
          } finally {
            serverRetryBudget.pauseNoAvailableWait()
          }
        }
        concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
        continue
      }
      const failure = capacityLimitFailures[capacityLimitFailures.length - 1]
      if (failure) {
        auditAttemptIndex += 1
        await recordAccountCapacityLimitFailure(req, usageContext, failure.account, failure.message, auditCapture, auditAttemptIndex)
      }
    } else if (capacityLimitFailures.length > 0) {
      if (!serverRetryBudget.handoffRequired('recoverable_later')) {
        const retryDelayMs = Math.min(500, serverRetryBudget.remainingMs())
        serverRetryBudget.beginNoAvailableWait()
        try {
          await waitForRetryDelayMs(retryDelayMs, { signal })
        } finally {
          serverRetryBudget.pauseNoAvailableWait()
        }
        concurrencyRetryWaitBudgetMs = accountConcurrencyRetryBudgetMs
        continue
      }
      const failure = capacityLimitFailures[capacityLimitFailures.length - 1]
      if (failure) {
        auditAttemptIndex += 1
        await recordAccountCapacityLimitFailure(req, usageContext, failure.account, failure.message, auditCapture, auditAttemptIndex)
      }
    }

    const postCycleSuppressionFilter = bypassLocalSuppression
      ? {
          accounts: dispatchAccounts,
          suppressedCount: 0,
          allSuppressed: false,
          suppressedAccountIds: [],
          acquiredHalfOpenLeases: []
        }
      : await filterGatewayAccountRuntimeSuppressionsAsync(dispatchAccounts)
    const recoverableAccountIds = new Set([
      ...postCycleSuppressionFilter.suppressedAccountIds,
      ...(postCycleSuppressionFilter.precheckSuppressedAccountIds ?? []),
      ...cycleRecoverableAccountIds
    ])
    const recoverableAccounts = dispatchAccounts.filter((account) => recoverableAccountIds.has(account.id))
    if (recoverableAccounts.length === 0 || !waitForRecoverableFailures) {
      break
    }
    const suppressionFilter = recoverableAccounts.length === dispatchAccounts.length
      ? postCycleSuppressionFilter
      : await filterGatewayAccountRuntimeSuppressionsAsync(recoverableAccounts)
    if (!suppressionFilter.allSuppressed) {
      if (serverRetryBudget.handoffRequired('recoverable_later')) {
        break
      }
      const retryDelayMs = Math.min(3000, serverRetryBudget.remainingMs())
      auditCapture.addGatewayMetadata({
        label: 'recoverable_upstream_failure_dispatch_wait',
        metadata: {
          accountIds: suppressionFilter.accounts.map((account) => account.id),
          retryDelayMs,
          remainingWaitBudgetMs: serverRetryBudget.remainingMs()
        }
      })
      serverRetryBudget.beginNoAvailableWait()
      try {
        await waitForRetryDelayMs(retryDelayMs, { signal })
      } finally {
        serverRetryBudget.pauseNoAvailableWait()
      }
      if (serverRetryBudget.handoffRequired('recoverable_later')) {
        break
      }
      failedProxyDispatchKeys.clear()
      dispatchAccounts = orderGatewayAccountsByRuntimeDegradation(suppressionFilter.accounts, {
        modelRankByAccountId: modelPriority?.rankByAccountId
      }).accounts
      continue
    }
    const precheckRuntimeScopes = suppressionFilter.precheckSuppressedRuntimeScopes ?? []
    const allBlockedByPrecheck = precheckRuntimeScopes.length > 0
      && suppressionFilter.precheckSuppressedAccountIds?.length === dispatchAccounts.length
    const waitStartedAtMs = Date.now()
    const deadlineAtMs = serverRetryBudget.deadlineAtMs(waitStartedAtMs)
    let wait: Awaited<ReturnType<typeof waitForRecoverableUnavailableState<typeof suppressionFilter>>>
    try {
      wait = await waitForRecoverableUnavailableState({
        scopeKey: recoverableDispatchSuppressionScopeKey(
          usageContext.systemAccountId,
          usageContext.apiKeyId,
          usageContext.groupId,
          requestModel(req),
          precheckRuntimeScopes
        ),
        reason: allBlockedByPrecheck ? 'precheck_half_open' : 'local_account_suppression_dispatch',
        initialState: suppressionFilter,
        refresh: () => filterGatewayAccountRuntimeSuppressionsAsync(recoverableAccounts),
        isReady: (state) => !state.allSuppressed,
        nextRetryAfterMs: (state) => state.nextRetryAfterMs,
        waitWithoutRetryAfter: true,
        maxWaitMs: serverRetryBudget.remainingMs(waitStartedAtMs),
        auditCapture,
        signal,
        requestStartedAtMs: waitStartedAtMs,
        deadlineAtMs,
        runtimeKeys: precheckRuntimeScopes.map((scope) => scope.runtimeKey),
        routeCoordinationBudget,
        gatewayRequestWallBudget
      })
    } finally {
      serverRetryBudget.pauseNoAvailableWait()
    }
    if (!wait.state.allSuppressed) {
      failedProxyDispatchKeys.clear()
      dispatchAccounts = orderGatewayAccountsByRuntimeDegradation(wait.state.accounts, {
        modelRankByAccountId: modelPriority?.rankByAccountId
      }).accounts
      continue
    }

    auditCapture.addGatewayMetadata({
      label: 'local_account_suppression_dispatch_exhausted',
      metadata: {
        suppressedCount: suppressionFilter.suppressedCount,
        accountCount: accounts.length
      }
    })
    throwIfRequestAborted(signal)
    lastAttempt = {
      accountId: accounts[0]?.id ?? 'local_suppression',
      accountName: accounts.length === 1 ? accounts[0]?.name ?? '上游账户' : '上游账户',
      providerCode: accounts[0]?.providerCode,
      providerProtocolProfileId: accounts[0]?.providerProtocolProfileId,
      protocolCode: accounts[0]?.protocolCode,
      protocolVersion: accounts[0]?.protocolVersion,
      upstreamUrl: 'account:locally_suppressed',
      message: '所有上游账户仍处于本地短期屏蔽'
    }
    break
  }

  throw new UpstreamAttemptError(
    buildUpstreamAttemptFailureMessage(accounts.length, lastAttempt),
    lastAttempt,
    [...failedAccountIds],
    agentGuidanceResponse,
    [...recoverableFailedAccountIds]
  )
}

function isAbortLike(error: unknown, signal?: AbortSignal): boolean {
  if (signal?.aborted) return true
  if (error instanceof Error && (error.name === 'AbortError' || /aborted|cancelled|canceled/i.test(error.message))) return true
  return false
}

function hotQualityTerminalForDispatchError(
  error: unknown,
  signal?: AbortSignal
): Parameters<GatewayHotQualityAttemptLifecycle['recordTerminal']>[0] {
  if (isAbortLike(error, signal)) {
    return { outcomeClass: 'client_cancellation', source: 'request_lifecycle' }
  }
  if (
    error instanceof GatewayAgentGuidanceResponse
    || error instanceof GatewayLocalProtocolResponse
    || error instanceof GatewayRequestValidationError
    || error instanceof OpenAIOAuthCodexAdapterError
  ) {
    return { outcomeClass: 'unknown', source: 'request_lifecycle' }
  }
  const failure = accountCircuitTransportFailure(error)
  return {
    outcomeClass: failure.kind === 'timeout' ? 'timeout' : 'transport_failure',
    failureScope: 'protocol_model',
    source: 'gateway_transport'
  }
}

function hotQualityAttemptForCircuitMode(
  attempt: GatewayHotQualityAttemptLifecycle,
  circuitAttempt: GatewayAccountCircuitAttempt | undefined
): GatewayHotQualityAttemptLifecycle {
  if (circuitAttempt?.isConfirmation === true) return attempt
  return {
    attemptId: attempt.attemptId,
    scope: attempt.scope,
    markFirstByte: (firstByteMs) => attempt.markFirstByte(firstByteMs),
    recordTerminal: (terminal) => attempt.recordTerminal(isTransportQualityOutcome(terminal.outcomeClass)
      ? { outcomeClass: 'unknown', failureScope: 'none', source: 'request_lifecycle' }
      : terminal)
  }
}

function isTransportQualityOutcome(
  outcomeClass: Parameters<GatewayHotQualityAttemptLifecycle['recordTerminal']>[0]['outcomeClass']
): boolean {
  return outcomeClass === 'transport_failure'
    || outcomeClass === 'timeout'
    || outcomeClass === 'read_interruption'
    || outcomeClass === 'incomplete_response'
}

function accountPhysicalCredentialKey(account: UpstreamAccount): string {
  return account.credentialSourceAccountId?.trim() || account.id
}

function requestDeduplicatedAttempt(account: UpstreamAccount, reason: string): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'account:request_deduplicated',
    message: `请求内候选已尝试：${reason}`
  }
}

function shouldRetainTransportFailureForRecovery(upstreamUrl: string, signal?: AbortSignal): boolean {
  return !signal?.aborted && /^https?:\/\//i.test(upstreamUrl)
}

function releaseAccountDispatchSlot(releaseConcurrency: () => void): () => void {
  let released = false
  return () => {
    if (released) {
      return
    }
    released = true
    releaseConcurrency()
  }
}

async function releaseHalfOpenLease(lease?: GatewayAccountHalfOpenLease): Promise<boolean> {
  if (!lease) return false
  const released = await lease.release()
  if (released) notifyOneRecoverableUnavailableRuntimeWaiter(lease.runtimeKey)
  return released
}

async function completeHalfOpenLeaseSuccess(lease?: GatewayAccountHalfOpenLease): Promise<boolean> {
  if (!lease?.completeSuccess) return false
  const completed = await lease.completeSuccess()
  if (completed) notifyOneRecoverableUnavailableRuntimeWaiter(lease.runtimeKey)
  return completed
}

function buildUpstreamAttemptFailureMessage(accountCount: number, lastAttempt?: UpstreamAttempt): string {
  const prefix = accountCount === 1 ? '上游账户请求失败' : '所有上游账户均失败'
  if (!lastAttempt) {
    return prefix
  }
  const result = stringValue(lastAttempt.message) || numberValue(lastAttempt.status) || '未知错误'
  const upstreamUrl = sanitizeUrlCredentialsForLog(lastAttempt.upstreamUrl) ?? lastAttempt.upstreamUrl
  return `${prefix}；最后一次尝试 ${lastAttempt.accountName} ${upstreamUrl} 返回 ${result}`
}

async function acquireAccountConcurrencyWithShortRetry(
  accountId: string,
  concurrencyLimit: number,
  waitBudgetMs: number,
  signal: AbortSignal | undefined,
  requestLane: OpenAIGatewayRequestLane,
  groupSchedulingPolicy?: GroupSchedulingPolicy,
  serverRetryBudget?: ServerRetryBudget
): Promise<AccountConcurrencyAcquireResult> {
  let remainingWaitBudgetMs = Math.max(0, Math.trunc(waitBudgetMs))
  const acquireOptions = accountConcurrencyLaneAcquireOptions(concurrencyLimit, requestLane, groupSchedulingPolicy)
  let slot = await tryAcquireAccountConcurrencyAsync(accountId, concurrencyLimit, acquireOptions)
  let waitedMs = 0
  let retryCount = 0
  if (!slot.acquired && remainingWaitBudgetMs > 0) {
    serverRetryBudget?.beginNoAvailableWait()
    try {
      remainingWaitBudgetMs = Math.min(remainingWaitBudgetMs, serverRetryBudget?.remainingMs() ?? remainingWaitBudgetMs)
      while (!slot.acquired && remainingWaitBudgetMs > 0) {
        const delayMs = Math.min(retryDelayMs(accountConcurrencyRetryPolicy, retryCount + 1), remainingWaitBudgetMs)
        const currentDelayMs = Math.min(delayMs, remainingWaitBudgetMs)
        await waitForAccountConcurrencyRetry(currentDelayMs, signal)
        waitedMs += currentDelayMs
        remainingWaitBudgetMs -= currentDelayMs
        retryCount += 1
        slot = await tryAcquireAccountConcurrencyAsync(accountId, concurrencyLimit, acquireOptions)
      }
    } finally {
      serverRetryBudget?.pauseNoAvailableWait()
    }
  }
  return {
    slot,
    retryCount,
    waitedMs,
    remainingWaitBudgetMs
  }
}

function locallySuppressedAttempt(account: UpstreamAccount, nextRetryAfterMs?: number): UpstreamAttempt {
  const suffix = nextRetryAfterMs === undefined
    ? ''
    : `，预计 ${Math.max(1, Math.ceil(nextRetryAfterMs / 1000))} 秒后释放`
  return {
    accountId: account.id,
    accountName: account.name,
    upstreamUrl: 'account:locally_suppressed',
    message: `账号处于本地短期屏蔽${suffix}`
  }
}

function accountApiKeyPoolUnavailableAttempt(account: UpstreamAccount): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'account:api_key_pool_unavailable',
    message: '账户 API Key 池暂无可用 Key'
  }
}

function accountApiKeyRetryBudgetExhaustedAttempt(account: UpstreamAccount, message: string): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'account:api_key_request_retry_budget_exhausted',
    message
  }
}

function accountCircuitBlockedAttempt(account: UpstreamAccount, phase: string): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'account:circuit_blocked',
    message: `账户短电路处于 ${phase}`
  }
}

function accountCapacityLimitAttempt(account: UpstreamAccount, message: string): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'concurrency:limit',
    message
  }
}

function canUseHighConcurrencyDispatchQueue(groupSchedulingPolicy?: GroupSchedulingPolicy): groupSchedulingPolicy is GroupSchedulingPolicy {
  return groupSchedulingPolicy !== undefined
}

async function recordAccountCapacityLimitFailure(
  req: Request,
  usageContext: GatewayUsageContext,
  account: UpstreamAccount,
  message: string,
  auditCapture: AuditCaptureContext,
  auditAttemptIndex: number
): Promise<void> {
  const attemptStartedAt = Date.now()
  await recordFailedUpstreamAttempt(req, usageContext, account, {
    upstreamUrl: 'concurrency:limit',
    startedAt: attemptStartedAt,
    errorMessage: message,
    failureAttribution: 'gateway_capacity'
  })
  auditCapture.recordFailedDispatchAttempt({
    account,
    attemptIndex: auditAttemptIndex,
    upstreamUrl: 'concurrency:limit',
    method: req.method,
    startedAtMs: attemptStartedAt,
    errorPhase: 'dispatch',
    errorCode: 'account_concurrency_limit',
    errorMessage: message,
    requestForModelAccounting: req
  })
}

function accountScopedGuidanceAttempt(account: UpstreamAccount, guidance: GatewayAgentGuidanceResponse): UpstreamAttempt {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'gateway:agent_guidance',
    message: guidance.message
  }
}

function assertGatewayRequestWallBudgetAvailableForAttempt(
  gatewayRequestWallBudget: GatewayRequestWallBudget,
  requestAttemptTracker: GatewayRequestAttemptTracker,
  auditCapture: AuditCaptureContext,
  finalResponseReserveMs: number
): void {
  if (!gatewayRequestWallBudget.handoffRequired({
    finalResponseReserveMs
  })) {
    return
  }
  const wallRemainingMs = gatewayRequestWallBudget.remainingMs()
  auditCapture.addGatewayMetadata({
    label: 'gateway_upstream_attempt_blocked_wall_budget',
    metadata: {
      reason: 'gateway_request_wall_budget_exhausted',
      wallRemainingMs,
      finalResponseReserveMs,
      attempts: requestAttemptTracker.snapshot()
    }
  })
  throw new GatewayRequestWallBudgetExhaustedError(wallRemainingMs)
}

function shouldRetryAnotherAccountApiKey(
  account: UpstreamAccount,
  keyScopedFailure: boolean | undefined,
  accountApiKeyAttemptCount: number,
  requestApiKeyAttemptCount: number,
  auditCapture: AuditCaptureContext
): boolean {
  if (!keyScopedFailure || !account.selectedApiKeyFingerprint) {
    return false
  }
  if (account.apiKeyRuntimeStateDisabled) {
    return false
  }
  if ((account.apiKeys?.length ?? 0) <= accountApiKeyAttemptCount) {
    recordAccountApiKeyRequestPoolExhausted(account, accountApiKeyAttemptCount, requestApiKeyAttemptCount, auditCapture)
    return false
  }
  if (!accountApiKeyRequestRetryBudgetAvailable(
    account,
    accountApiKeyAttemptCount,
    requestApiKeyAttemptCount,
    auditCapture
  )) {
    return false
  }
  getRequestLogger().warn({
    event: 'gateway_account_api_key_request_failover_scheduled',
    accountId: account.id,
    accountName: account.name,
    selectedApiKeyIndex: account.selectedApiKeyIndex,
    accountApiKeyAttemptCount,
    requestApiKeyAttemptCount,
    requestAttemptSafetyLimit: gatewayAccountApiKeyRequestAttemptSafetyLimit
  }, '账户内 API Key 请求失败，本次请求尝试同账户下一个 Key')
  auditCapture.addGatewayMetadata({
    label: 'account_api_key_request_failover_scheduled',
    metadata: {
      accountId: account.id,
      accountName: account.name,
      selectedApiKeyIndex: account.selectedApiKeyIndex,
      accountApiKeyAttemptCount,
      requestApiKeyAttemptCount,
      requestAttemptSafetyLimit: gatewayAccountApiKeyRequestAttemptSafetyLimit
    }
  })
  return true
}

function shouldTryAnotherAccountApiKeyForRequest(
  account: UpstreamAccount,
  accountApiKeyAttemptCount: number,
  requestApiKeyAttemptCount: number,
  auditCapture: AuditCaptureContext
): boolean {
  if (!account.selectedApiKeyFingerprint) {
    return false
  }
  if ((account.apiKeys?.length ?? 0) <= accountApiKeyAttemptCount) {
    recordAccountApiKeyRequestPoolExhausted(account, accountApiKeyAttemptCount, requestApiKeyAttemptCount, auditCapture)
    return false
  }
  if (!accountApiKeyRequestRetryBudgetAvailable(
    account,
    accountApiKeyAttemptCount,
    requestApiKeyAttemptCount,
    auditCapture
  )) {
    return false
  }
  getRequestLogger().warn({
    event: 'gateway_account_api_key_opaque_request_failover_scheduled',
    accountId: account.id,
    accountName: account.name,
    selectedApiKeyIndex: account.selectedApiKeyIndex,
    accountApiKeyAttemptCount,
    requestApiKeyAttemptCount,
    requestAttemptSafetyLimit: gatewayAccountApiKeyRequestAttemptSafetyLimit
  }, '通用上游失败，本次请求尝试同账户下一个 Key')
  auditCapture.addGatewayMetadata({
    label: 'account_api_key_opaque_request_failover_scheduled',
    metadata: {
      accountId: account.id,
      selectedApiKeyIndex: account.selectedApiKeyIndex,
      accountApiKeyAttemptCount,
      requestApiKeyAttemptCount,
      requestAttemptSafetyLimit: gatewayAccountApiKeyRequestAttemptSafetyLimit
    }
  })
  return true
}

function accountApiKeyRequestRetryBudgetAvailable(
  account: UpstreamAccount,
  accountApiKeyAttemptCount: number,
  requestApiKeyAttemptCount: number,
  auditCapture: AuditCaptureContext
): boolean {
  const remainingConfiguredKeyCount = Math.max(0, (account.apiKeys?.length ?? 0) - accountApiKeyAttemptCount)
  if (requestApiKeyAttemptCount >= gatewayAccountApiKeyRequestAttemptSafetyLimit) {
    recordAccountApiKeyRequestRetryBudgetExhausted(account, {
      accountApiKeyAttemptCount,
      requestApiKeyAttemptCount,
      remainingConfiguredKeyCount,
      reason: 'request_safety_limit'
    }, auditCapture)
    return false
  }
  return true
}

function recordAccountApiKeyRequestPoolExhausted(
  account: UpstreamAccount,
  accountApiKeyAttemptCount: number,
  requestApiKeyAttemptCount: number,
  auditCapture: AuditCaptureContext
): void {
  auditCapture.addGatewayMetadata({
    label: 'account_api_key_request_pool_exhausted',
    metadata: {
      accountId: account.id,
      accountName: account.name,
      accountApiKeyAttemptCount,
      requestApiKeyAttemptCount,
      configuredKeyCount: account.apiKeys?.length ?? 0,
      poolExhausted: true
    }
  })
}

function recordAccountApiKeyRequestRetryBudgetExhausted(
  account: UpstreamAccount,
  input: {
    accountApiKeyAttemptCount: number
    requestApiKeyAttemptCount: number
    remainingConfiguredKeyCount: number
    reason: 'request_safety_limit'
  },
  auditCapture: AuditCaptureContext
): void {
  getRequestLogger().warn({
    event: 'gateway_account_api_key_request_retry_budget_exhausted',
    accountId: account.id,
    accountName: account.name,
    ...input,
    requestAttemptSafetyLimit: gatewayAccountApiKeyRequestAttemptSafetyLimit,
    poolExhausted: false
  }, '账户内仍可能存在未尝试 Key，但本次请求的安全预算已耗尽')
  auditCapture.addGatewayMetadata({
    label: 'account_api_key_request_retry_budget_exhausted',
    metadata: {
      accountId: account.id,
      accountName: account.name,
      ...input,
      requestAttemptSafetyLimit: gatewayAccountApiKeyRequestAttemptSafetyLimit,
      poolExhausted: false
    }
  })
}

async function recordConfirmedSameAccountApiKeyFailures(
  failures: PendingAccountApiKeyFailure[],
  successAccount: UpstreamAccount,
  usageContext: GatewayUsageContext
): Promise<void> {
  if (!failures.length || !successAccount.selectedApiKeyFingerprint) {
    return
  }
  const successSourceAccountId = accountRuntimeSourceId(successAccount)
  for (const failure of failures) {
    if (accountRuntimeSourceId(failure.account) !== successSourceAccountId) {
      continue
    }
    if (!failure.account.selectedApiKeyFingerprint || failure.account.selectedApiKeyFingerprint === successAccount.selectedApiKeyFingerprint) {
      continue
    }
    await recordGatewayAccountApiKeyFailure(failure.account, {
      status: failure.status,
      statusCode: failure.statusCode,
      errorCode: failure.errorCode,
      errorMessage: failure.errorMessage,
      observationEpoch: failure.observationEpoch,
      traceId: usageContext.traceId,
      cooldownUntil: failure.cooldownUntil,
      trafficSource: usageContext.trafficSource,
      clientIp: usageContext.clientIp,
      apiKeyId: usageContext.apiKeyId,
      source: 'same_account_api_key_failover_confirmed'
    })
  }
  failures.length = 0
}

function accountRuntimeSourceId(account: UpstreamAccount): string {
  return account.credentialSourceAccountId || account.id
}

async function orderAccountsForRequestLaneAsync(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  groupSchedulingPolicy?: GroupSchedulingPolicy,
  modelPriority?: GatewayAccountModelPriority
): Promise<UpstreamAccount[]> {
  if (requestLane !== 'image' || accounts.length < 2) {
    return accounts
  }
  const accountIds = gatewayAccountConcurrencyAccountIds(accounts)
  const currentConcurrency = await loadAccountCurrentConcurrencyByIdsAsync(accountIds)
  const imageLaneConcurrency = await loadAccountCurrentConcurrencyByIdsAsync(accountIds, 'image')
  const orderedAccounts = [...accounts].sort((left, right) => {
    return imageLaneBusyRank(left, currentConcurrency, imageLaneConcurrency, groupSchedulingPolicy)
      - imageLaneBusyRank(right, currentConcurrency, imageLaneConcurrency, groupSchedulingPolicy)
  })
  return preserveGatewayAccountDispatchPriorityTiers(accounts, orderedAccounts, {
    modelRankByAccountId: modelPriority?.rankByAccountId
  })
}

function imageLaneBusyRank(
  account: UpstreamAccount,
  currentConcurrency: Map<string, number>,
  imageLaneConcurrency: Map<string, number>,
  groupSchedulingPolicy?: GroupSchedulingPolicy
): number {
  const hardLimit = Number.isFinite(account.concurrencyLimit) ? Math.max(1, Math.trunc(account.concurrencyLimit)) : 1
  const concurrencyAccountId = gatewayAccountConcurrencyAccountId(account)
  if ((currentConcurrency.get(concurrencyAccountId) ?? 0) >= hardLimit) {
    return 2
  }
  const laneLimit = effectiveImageLaneConcurrencyLimit({
    accountConcurrencyLimit: hardLimit,
    policy: groupSchedulingPolicy
  })
  return (imageLaneConcurrency.get(concurrencyAccountId) ?? 0) >= laneLimit ? 1 : 0
}

function accountConcurrencyLaneAcquireOptions(
  concurrencyLimit: number,
  requestLane: OpenAIGatewayRequestLane,
  groupSchedulingPolicy?: GroupSchedulingPolicy
): AccountConcurrencyAcquireOptions {
  if (requestLane !== 'image') {
    return { lane: 'text' }
  }
  return {
    lane: 'image',
    laneLimit: effectiveImageLaneConcurrencyLimit({
      accountConcurrencyLimit: concurrencyLimit,
      policy: groupSchedulingPolicy
    })
  }
}

function accountConcurrencyLimitMessage(slot: AccountConcurrencySlot, waitedMs?: number): string {
  const suffix = waitedMs && waitedMs > 0 ? `（短等 ${waitedMs}ms 后仍未释放）` : ''
  if (slot.lane === 'image' && slot.laneCurrent >= slot.laneLimit && slot.current < slot.limit) {
    return `账户图像通道并发已达到上限 ${slot.laneCurrent}/${slot.laneLimit}，已为文本通道保留并发槽${suffix}`
  }
  return `账户并发已达到上限 ${slot.current}/${slot.limit}${suffix}`
}

async function waitForAccountConcurrencyRetry(delayMs: number, signal?: AbortSignal): Promise<void> {
  throwIfRequestAborted(signal)
  await waitForRetryDelayMs(delayMs, { signal })
  throwIfRequestAborted(signal)
}

async function settleUndispatchedAccountCircuitAttempt(
  attempt: GatewayAccountCircuitAttempt | undefined,
  accountId: string
): Promise<void> {
  if (!attempt?.isConfirmation) return
  let lastError: unknown
  for (let retry = 0; retry < 2; retry += 1) {
    try {
      await attempt.reportUnknown()
      return
    } catch (error) {
      lastError = error
    }
  }
  getRequestLogger().warn({
    event: 'gateway_account_circuit_confirmation_settlement_failed',
    accountId,
    errorName: lastError instanceof Error ? lastError.name : typeof lastError
  }, '账户 confirmation 在上游派发前退出时结算失败，保留原结果意图并等待租约到期')
}

export function gatewayForegroundAccountCircuitFailureEvidenceKey(
  req: Request,
  usageContext: Pick<GatewayUsageContext, 'systemAccountId' | 'apiKeyId' | 'clientIp'>
): string {
  const session = explicitAccountCircuitSessionIdentity(req)
  if (session) {
    return accountCircuitEvidenceDigest({
      source: 'explicit_session',
      systemAccountId: usageContext.systemAccountId,
      session
    })
  }

  const clientIp = usageContext.clientIp?.trim().toLowerCase()
  if (clientIp) {
    // Circuit scope already isolates protocol, lane and model. Keeping endpoint,
    // group, body and trace out of this identity prevents one caller from
    // manufacturing independent confirmations by varying request details.
    return accountCircuitEvidenceDigest({
      source: 'gateway_caller',
      systemAccountId: usageContext.systemAccountId,
      clientIp
    })
  }

  // Without a client address there is no foreground evidence of independence.
  // Aggregate even across API keys so an unidentified caller cannot self-confirm
  // an account-wide transport death by rotating keys or request metadata.
  return accountCircuitEvidenceDigest({
    source: 'gateway_unknown_caller',
    systemAccountId: usageContext.systemAccountId
  })
}

function explicitAccountCircuitSessionIdentity(req: Request): { source: string; value: string } | undefined {
  const identity = getGatewaySessionIdentity(req)
  const value = stringValue(identity?.sessionId)
  return value
    ? { source: identity?.semanticNamespace ?? 'gateway_session', value }
    : undefined
}

function accountCircuitEvidenceDigest(value: object): string {
  return createHash('sha256').update(JSON.stringify(value)).digest('hex')
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function numberValue(value: unknown): string {
  return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
}

function accountCircuitTransportFailure(error: unknown, fallbackMessage?: string): GatewayAccountCircuitTransportFailure {
  const reason = fallbackMessage?.trim()
    || (error instanceof Error ? error.message.trim() : '')
    || '上游传输失败'
  const diagnostic = [
    error instanceof Error ? error.name : '',
    typeof error === 'object' && error !== null && 'code' in error ? String(error.code) : '',
    reason
  ].join(' ').toLowerCase()
  return {
    kind: /timeout|timedout|timed out|etimedout|超时/.test(diagnostic) ? 'timeout' : 'transport',
    reason
  }
}

function recoverableDispatchSuppressionScopeKey(
  systemAccountId: string,
  apiKeyId: string | undefined,
  groupId: string,
  model: string | undefined,
  runtimeScopes: Array<{ runtimeKey: string; generation: number }>
): string {
  const candidates = runtimeScopes
    .map((scope) => `${scope.runtimeKey}@${scope.generation}`)
    .sort()
    .join(',')
  return [systemAccountId, apiKeyId ?? '', groupId, model ?? '', candidates].join(':')
}
