import { runtimeConfig } from '../../config/runtime.js'
import { getTraceId } from '../../shared/request-context.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  accountHealthCheckWorkerIpcQueueLimits,
  sendAccountHealthCheckTriggerToWorkerWithOutcome,
  settleClientSourceFenceFromWorker
} from '../background/background-ipc.js'
import type { AccountHealthCheckTriggerReason, CodexSourceProbeFence } from '../accounts/account-health-check-trigger.js'
import {
  accountHealthCheckDispatchInternalPrefix,
  createAccountHealthCheckDispatchSignature,
  type AccountHealthCheckDispatchOutcome
} from './account-health-check-dispatch.routes.js'

const accountHealthCheckDispatchPath = '/v1/account-health-check/dispatch'
const accountHealthCheckDispatchTimeoutMs = 2_000
const requestFailureDispatchCooldownMs = 5 * 60_000
const requestFailureDispatchInFlight = new Set<string>()
const requestFailureDispatchInFlightAt = new Map<string, number>()
const requestFailureDispatchAcceptedAt = new Map<string, number>()
let missingGatewayDispatchTargetLogged = false

export function dispatchAccountHealthCheck(accountId: string, reason: AccountHealthCheckTriggerReason, traceId = getTraceId()): boolean {
  return dispatchAccountHealthCheckWithOutcome(accountId, reason, traceId).outcome === 'queued'
}

export function dispatchAccountHealthCheckWithOutcome(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId = getTraceId(),
  sourceFence?: CodexSourceProbeFence
): AccountHealthCheckDispatchOutcome {
  const normalizedId = accountId.trim()
  if (!normalizedId) return rejectedDispatchOutcome('ops_ipc_unavailable')
  const cooldownRemainingMs = sourceFence ? undefined : requestFailureDispatchCooldownRemainingMs(normalizedId, reason)
  if (cooldownRemainingMs !== undefined) return {
    outcome: 'coalesced',
    decisionCode: 'request_failure_cooldown',
    targetRole: 'ops-worker',
    cooldownRemainingMs
  }
  if (
    runtimeConfig.runtimeMode === 'performance'
    && runtimeConfig.performanceNodeRole === 'gateway'
    && runtimeConfig.processRole === 'server'
  ) {
    return dispatchAccountHealthCheckToControl(normalizedId, reason, traceId, sourceFence)
  }
  if (runtimeConfig.processRole !== 'db-service') {
    return rememberAccountHealthCheckDispatchOutcome(
      normalizedId,
      reason,
      workerDispatchOutcome(sendAccountHealthCheckTriggerToWorkerWithOutcome(normalizedId, reason, traceId, sourceFence))
    )
  }
  if (!process.send || process.connected === false) return rejectedDispatchOutcome('ops_ipc_unavailable')
  try {
    process.send({
      type: 'background_worker_account_health_check_trigger',
      accountId: normalizedId,
      reason,
      ...(traceId ? { traceId } : {}),
      ...(sourceFence ? { sourceFence } : {})
    }, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'account_health_check_db_service_dispatch_failed',
          accountId: normalizedId
        }), 'DB service 投递账户健康检查触发消息失败')
      }
    })
    return rememberAccountHealthCheckDispatchOutcome(normalizedId, reason, queuedDispatchOutcome())
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_health_check_db_service_dispatch_failed',
      accountId: normalizedId
    }), 'DB service 投递账户健康检查触发消息失败')
    return rejectedDispatchOutcome('ops_ipc_unavailable')
  }
}

function dispatchAccountHealthCheckToControl(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId?: string,
  sourceFence?: CodexSourceProbeFence
): AccountHealthCheckDispatchOutcome {
  const dispatchUrl = accountHealthCheckDispatchTargetUrl()
  if (!dispatchUrl) {
    if (!missingGatewayDispatchTargetLogged) {
      missingGatewayDispatchTargetLogged = true
      logger.warn({
        event: 'account_health_check_control_dispatch_unconfigured'
      }, 'Gateway 未配置 control 健康检查投递地址，无法触发独立账户检查')
    }
    return rejectedDispatchOutcome('ops_ipc_unavailable')
  }
  if (reason === 'request_failure') {
    requestFailureDispatchInFlight.add(accountId)
    requestFailureDispatchInFlightAt.set(accountId, Date.now())
  }
  void postAccountHealthCheckDispatch(dispatchUrl, accountId, reason, traceId, sourceFence)
    .then((accepted) => {
      if (!accepted) settleClientSourceFenceAfterControlDispatchFailure(sourceFence, accountId, 'rejected')
      rememberAccountHealthCheckDispatchOutcome(
        accountId,
        reason,
        accepted ? queuedDispatchOutcome() : rejectedDispatchOutcome('ops_ipc_unavailable')
      )
    })
    .catch((error) => {
      settleClientSourceFenceAfterControlDispatchFailure(sourceFence, accountId, 'failed')
      logger.warn(errorLogFields(error, {
        event: 'account_health_check_control_dispatch_failed',
        accountId
      }), 'Gateway 向 control 投递账户健康检查失败')
    })
    .finally(() => {
      if (reason === 'request_failure') {
        requestFailureDispatchInFlight.delete(accountId)
        requestFailureDispatchInFlightAt.delete(accountId)
      }
      cleanupRequestFailureDispatchCooldowns(Date.now())
    })
  return queuedDispatchOutcome()
}

function settleClientSourceFenceAfterControlDispatchFailure(
  sourceFence: CodexSourceProbeFence | undefined,
  accountId: string,
  reason: 'rejected' | 'failed'
): void {
  if (!sourceFence) return
  void settleClientSourceFenceFromWorker(sourceFence, 'unknown').catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'account_health_check_control_source_fence_settlement_failed',
      accountId,
      sourceGeneration: sourceFence.sourceGeneration,
      probeGeneration: sourceFence.probeGeneration,
      reason
    }), 'control 健康检查投递失败后未能结算来源 fence，保留短期避让等待后续接管')
  })
}

async function postAccountHealthCheckDispatch(
  url: URL,
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId?: string,
  sourceFence?: CodexSourceProbeFence
): Promise<boolean> {
  const body = Buffer.from(JSON.stringify({ accountId, reason, ...(sourceFence ? { sourceFence } : {}) }), 'utf8')
  const response = await fetch(url, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'content-length': String(body.byteLength),
      'x-juhe-ai-signature': createAccountHealthCheckDispatchSignature(runtimeConfig.secret, body),
      ...(traceId ? { 'x-trace-id': traceId } : {})
    },
    body,
    signal: AbortSignal.timeout(accountHealthCheckDispatchTimeoutMs)
  })
  const statusCode = response.status
  try {
    await response.body?.cancel()
  } catch {
    // The dispatch result is carried entirely by the status code.
  }
  if (statusCode === 202) return true
  logger.warn({
    event: 'account_health_check_control_dispatch_rejected',
    accountId,
    statusCode
  }, 'Gateway 向 control 投递账户健康检查被拒绝')
  return false
}

function accountHealthCheckDispatchTargetUrl(): URL | undefined {
  const configured = runtimeConfig.accountHealthCheckDispatchUrl?.trim()
  if (!configured) return undefined
  try {
    const baseUrl = new URL(configured)
    if (
      baseUrl.protocol !== 'http:'
      || (baseUrl.hostname !== '127.0.0.1' && baseUrl.hostname !== '[::1]' && baseUrl.hostname !== '::1')
      || baseUrl.username
      || baseUrl.password
      || baseUrl.search
      || baseUrl.hash
    ) {
      return undefined
    }
    return new URL(`${accountHealthCheckDispatchInternalPrefix}${accountHealthCheckDispatchPath}`, baseUrl)
  } catch {
    return undefined
  }
}

function requestFailureDispatchCooldownRemainingMs(
  accountId: string,
  reason: AccountHealthCheckTriggerReason
): number | undefined {
  if (reason !== 'request_failure') return undefined
  const now = Date.now()
  const inFlightAt = requestFailureDispatchInFlightAt.get(accountId)
  if (requestFailureDispatchInFlight.has(accountId)) {
    return Math.max(1, requestFailureDispatchCooldownMs - Math.max(0, now - (inFlightAt ?? now)))
  }
  cleanupRequestFailureDispatchCooldowns(now)
  const acceptedAt = requestFailureDispatchAcceptedAt.get(accountId)
  if (acceptedAt === undefined) return undefined
  const remainingMs = requestFailureDispatchCooldownMs - (now - acceptedAt)
  return remainingMs > 0 ? remainingMs : undefined
}

function rememberAccountHealthCheckDispatchOutcome(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  outcome: AccountHealthCheckDispatchOutcome
): AccountHealthCheckDispatchOutcome {
  if (outcome.outcome === 'queued' && reason === 'request_failure') {
    requestFailureDispatchAcceptedAt.set(accountId, Date.now())
  }
  return outcome
}

function queuedDispatchOutcome(): AccountHealthCheckDispatchOutcome {
  return {
    outcome: 'queued',
    decisionCode: 'queued',
    targetRole: 'ops-worker'
  }
}

function rejectedDispatchOutcome(
  decisionCode: Extract<AccountHealthCheckDispatchOutcome, { outcome: 'rejected' }>['decisionCode']
): AccountHealthCheckDispatchOutcome {
  return {
    outcome: 'rejected',
    decisionCode,
    targetRole: 'ops-worker',
    maxQueueMessages: accountHealthCheckWorkerIpcQueueLimits.maxQueueMessages,
    maxQueueBytes: accountHealthCheckWorkerIpcQueueLimits.maxQueueBytes
  }
}

function workerDispatchOutcome(
  outcome: ReturnType<typeof sendAccountHealthCheckTriggerToWorkerWithOutcome>
): AccountHealthCheckDispatchOutcome {
  if (outcome.accepted) {
    return {
      outcome: 'queued',
      decisionCode: 'queued',
      targetRole: 'ops-worker',
      queueLength: outcome.queueLength,
      queueBytes: outcome.queueBytes,
      messageBytes: outcome.messageBytes,
      maxQueueMessages: accountHealthCheckWorkerIpcQueueLimits.maxQueueMessages,
      maxQueueBytes: accountHealthCheckWorkerIpcQueueLimits.maxQueueBytes
    }
  }
  return {
    outcome: 'rejected',
    decisionCode: outcome.decisionCode,
    targetRole: 'ops-worker',
    queueLength: outcome.queueLength,
    queueBytes: outcome.queueBytes,
    messageBytes: outcome.messageBytes,
    maxQueueMessages: accountHealthCheckWorkerIpcQueueLimits.maxQueueMessages,
    maxQueueBytes: accountHealthCheckWorkerIpcQueueLimits.maxQueueBytes
  }
}

function cleanupRequestFailureDispatchCooldowns(now: number): void {
  const cutoff = now - requestFailureDispatchCooldownMs
  for (const [accountId, acceptedAt] of requestFailureDispatchAcceptedAt) {
    if (acceptedAt <= cutoff) requestFailureDispatchAcceptedAt.delete(accountId)
  }
}
