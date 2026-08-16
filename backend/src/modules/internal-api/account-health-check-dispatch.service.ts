import { randomUUID } from 'node:crypto'
import { runtimeConfig } from '../../config/runtime.js'
import { getTraceId } from '../../shared/request-context.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  accountHealthCheckWorkerIpcQueueLimits,
  sendAccountHealthCheckTriggerToWorkerWithOutcome
} from '../background/background-ipc.js'
import type { AccountHealthCheckTriggerReason, CodexSourceProbeFence } from '../accounts/account-health-check-trigger.js'
import { settleAccountHealthJobsSourceFence } from '../gateway/runtime/account-health-jobs-source-fence.consumer.js'
import { findAccountForAccountHealthJobsInputAsync } from '../../storage/account-health-check.repository.js'
import { currentAccountHealthJobsInputVersionForRuntimeAsync } from '../../storage/account-health-jobs-input-version.repository.js'
import { publishAccountHealthJobsProbeRequest } from '../background/account-health-jobs-input.service.js'
import {
  accountHealthCheckDispatchInternalPrefix,
  createAccountHealthCheckDispatchSignature,
  type AccountHealthCheckDispatchOutcome
} from './account-health-check-dispatch.routes.js'

const accountHealthCheckDispatchPath = '/v1/account-health-check/dispatch'
const accountHealthCheckDispatchTimeoutMs = 2_000
interface RequestFailureControlDispatchState {
  running: boolean
  trailing: boolean
  latestTraceId?: string
}

const requestFailureControlDispatches = new Map<string, RequestFailureControlDispatchState>()
let missingGatewayDispatchTargetLogged = false

export function dispatchAccountHealthCheck(accountId: string, reason: AccountHealthCheckTriggerReason, traceId = getTraceId()): boolean {
  return dispatchAccountHealthCheckWithOutcome(accountId, reason, traceId).outcome !== 'rejected'
}

export function dispatchAccountHealthCheckWithOutcome(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId = getTraceId(),
  sourceFence?: CodexSourceProbeFence
): AccountHealthCheckDispatchOutcome {
  const normalizedId = accountId.trim()
  if (!normalizedId) return rejectedDispatchOutcome('ops_ipc_unavailable')
  if (runtimeConfig.accountHealthJobs.owner === 'go') {
    return dispatchAccountHealthJobsRequestToFile(normalizedId, reason, traceId, sourceFence)
  }
  if (
    runtimeConfig.runtimeMode === 'performance'
    && (runtimeConfig.performanceNodeRole === 'gateway' || runtimeConfig.performanceNodeRole === 'control-replica')
    && runtimeConfig.processRole === 'server'
  ) {
    return dispatchAccountHealthCheckToControl(normalizedId, reason, traceId, sourceFence)
  }
  if (runtimeConfig.processRole !== 'db-service') {
    return workerDispatchOutcome(sendAccountHealthCheckTriggerToWorkerWithOutcome(normalizedId, reason, traceId, sourceFence))
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
    return queuedDispatchOutcome()
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_health_check_db_service_dispatch_failed',
      accountId: normalizedId
    }), 'DB service 投递账户健康检查触发消息失败')
    return rejectedDispatchOutcome('ops_ipc_unavailable')
  }
}

function dispatchAccountHealthJobsRequestToFile(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId: string | undefined,
  sourceFence: CodexSourceProbeFence | undefined
): AccountHealthCheckDispatchOutcome {
  const root = runtimeConfig.accountHealthJobs.inputDirectory?.trim()
  const signingKey = runtimeConfig.accountHealthJobs.inputSigningKey?.trim()
  if (!root || !signingKey) return rejectedDispatchOutcome('dispatch_rejected')
  const requestId = `j1-${randomUUID()}`
  void (async () => {
    const account = await findAccountForAccountHealthJobsInputAsync(accountId)
    const inputVersion = account ? await currentAccountHealthJobsInputVersionForRuntimeAsync(accountId) : undefined
    if (!account || inputVersion === undefined) throw new Error('J1 request 缺少当前账户或 input epoch')
    publishAccountHealthJobsProbeRequest({
      account,
      inputVersion,
      root,
      signingKey,
      requestId,
      reason,
      deadline: new Date(Date.now() + runtimeConfig.background.accountHealthCheckProbeDeadlineMs),
      sourceFence
    })
  })().catch((error) => {
    logger.warn({
      event: 'account_health_jobs_request_publish_failed',
      accountId,
      requestId,
      traceId,
      error: error instanceof Error ? error.name : 'unknown'
    }, 'J1 请求文件发布失败')
    if (sourceFence) {
      void settleAccountHealthJobsSourceFence(sourceFence, 'unknown').catch(() => undefined)
    }
  })
  return queuedDispatchOutcome()
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
  if (reason === 'request_failure' && !sourceFence) {
    const existing = requestFailureControlDispatches.get(accountId)
    if (existing) {
      existing.trailing = true
      existing.latestTraceId = traceId
      return coalescedInFlightDispatchOutcome()
    }
    const state: RequestFailureControlDispatchState = {
      running: true,
      trailing: false,
      latestTraceId: traceId
    }
    requestFailureControlDispatches.set(accountId, state)
    void dispatchRequestFailureAccountHealthCheckToControl(dispatchUrl, accountId, state)
    return queuedDispatchOutcome()
  }
  void postAccountHealthCheckDispatch(dispatchUrl, accountId, reason, traceId, sourceFence)
    .then((accepted) => {
      if (!accepted) settleClientSourceFenceAfterControlDispatchFailure(sourceFence, accountId, 'rejected')
    })
    .catch((error) => {
      settleClientSourceFenceAfterControlDispatchFailure(sourceFence, accountId, 'failed')
      logger.warn(errorLogFields(error, {
        event: 'account_health_check_control_dispatch_failed',
        accountId
      }), 'Gateway 向 control 投递账户健康检查失败')
    })
  return queuedDispatchOutcome()
}

async function dispatchRequestFailureAccountHealthCheckToControl(
  dispatchUrl: URL,
  accountId: string,
  state: RequestFailureControlDispatchState
): Promise<void> {
  while (state.running) {
    const traceId = state.latestTraceId
    try {
      await postAccountHealthCheckDispatch(dispatchUrl, accountId, 'request_failure', traceId)
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'account_health_check_control_dispatch_failed',
        accountId
      }), 'Gateway 向 control 投递账户健康检查失败')
    }
    if (state.trailing) {
      state.trailing = false
      continue
    }
    state.running = false
    if (requestFailureControlDispatches.get(accountId) === state) {
      requestFailureControlDispatches.delete(accountId)
    }
  }
}

function settleClientSourceFenceAfterControlDispatchFailure(
  sourceFence: CodexSourceProbeFence | undefined,
  accountId: string,
  reason: 'rejected' | 'failed'
): void {
  if (!sourceFence) return
  void settleAccountHealthJobsSourceFence(sourceFence, 'unknown').catch((error) => {
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

function queuedDispatchOutcome(): AccountHealthCheckDispatchOutcome {
  return {
    outcome: 'queued',
    decisionCode: 'queued',
    targetRole: 'ops-worker'
  }
}

function coalescedInFlightDispatchOutcome(): AccountHealthCheckDispatchOutcome {
  return {
    outcome: 'coalesced',
    decisionCode: 'request_failure_in_flight',
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
