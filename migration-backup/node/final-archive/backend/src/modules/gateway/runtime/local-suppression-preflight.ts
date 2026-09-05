import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import {
  filterGatewayAccountRuntimeSuppressionsAsync,
  type LocalAccountSuppressionFilterResult
} from './account-side-effects.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import { waitForRecoverableUnavailableState } from './recoverable-unavailable-wait.js'
import type { ServerRetryBudget } from './server-retry-budget.js'
import type {
  GatewayRequestWallBudget,
  RouteCoordinationBudget,
  GatewayRouteCoordinatorOwner
} from '../routing/route-coordination.js'
import type { OpenAIGatewayDispatchContext } from '../request/preflight.js'

export async function resolveLocalSuppressionFilter(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  accounts: UpstreamAccount[]
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  serverRetryBudget: ServerRetryBudget
  routeCoordinationBudget: RouteCoordinationBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
  routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>
  signal?: AbortSignal
}): Promise<LocalAccountSuppressionFilterResult<UpstreamAccount> | undefined> {
  let filter = await filterGatewayAccountRuntimeSuppressionsAsync(input.accounts)
  let waitSkippedReason: string | undefined
  if (filter.suppressedCount > 0) {
    logger.warn({
      event: filter.allSuppressed
        ? 'gateway_local_account_suppression_exhausted'
        : 'gateway_local_account_suppression_applied',
      suppressedCount: filter.suppressedCount,
      suppressedAccountIds: filter.suppressedAccountIds,
      allSuppressed: filter.allSuppressed,
      nextRetryAfterMs: filter.nextRetryAfterMs,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId
    }, filter.allSuppressed
      ? '候选上游账号均处于本地短期屏蔽，准备进入本地恢复等待窗口'
      : '网关本地短期屏蔽账号已应用到候选列表')
    input.auditCapture.addGatewayMetadata({
      label: 'local_account_suppression',
      metadata: {
        suppressedCount: filter.suppressedCount,
        suppressedAccountIds: filter.suppressedAccountIds,
        allSuppressed: filter.allSuppressed,
        nextRetryAfterMs: filter.nextRetryAfterMs
      }
    })
  }

  if (filter.allSuppressed) {
    const waitStartedAtMs = Date.now()
    const deadlineAtMs = input.serverRetryBudget.deadlineAtMs(waitStartedAtMs)
    try {
      const wait = await waitForRecoverableUnavailableState({
        scopeKey: recoverableSuppressionScopeKey(input.systemAccountId, input.apiKeyId, input.groupId),
        reason: 'local_account_suppression',
        initialState: filter,
        refresh: () => filterGatewayAccountRuntimeSuppressionsAsync(input.accounts),
        isReady: (state) => !state.allSuppressed,
        nextRetryAfterMs: (state) => state.nextRetryAfterMs,
        waitWithoutRetryAfter: true,
        auditCapture: input.auditCapture,
        maxWaitMs: input.serverRetryBudget.remainingMs(waitStartedAtMs),
        requestStartedAtMs: waitStartedAtMs,
        deadlineAtMs,
        routeCoordinationBudget: input.routeCoordinationBudget,
        gatewayRequestWallBudget: input.gatewayRequestWallBudget,
        signal: input.signal
      })
      filter = wait.state
      waitSkippedReason = wait.skippedReason
      if (waitSkippedReason) {
        input.auditCapture.addGatewayMetadata({
          label: 'local_account_suppression_temporarily_blocked',
          metadata: {
            reason: waitSkippedReason,
            coordinationRemainingMs: input.routeCoordinationBudget.remainingMs(),
            serverRetryRemainingMs: input.serverRetryBudget.remainingMs(),
            wallRemainingMs: input.gatewayRequestWallBudget.remainingMs()
          }
        })
      }
    } finally {
      input.serverRetryBudget.pauseNoAvailableWait()
    }
  }

  if (!filter.allSuppressed) {
    return filter
  }

  if (input.signal?.aborted || input.res.writableEnded) {
    return undefined
  }

  await input.routeCoordinator.completeFailure({
    statusCode: 503,
    message: '所有上游账户正在临时隔离，请稍后重试',
    errorType: 'service_unavailable',
    // The wait reason is internal coordination state and remains available in
    // audit metadata above. Clients receive one stable retry contract instead
    // of coupling themselves to gateway scheduling internals.
    errorCode: 'upstream_retryable_error',
    errorPhase: 'dispatch',
    retryAfterMs: filter.nextRetryAfterMs
  })
  return undefined
}

function recoverableSuppressionScopeKey(systemAccountId: string, apiKeyId: string | undefined, groupId: string): string {
  return [systemAccountId, apiKeyId ?? '', groupId].join(':')
}
