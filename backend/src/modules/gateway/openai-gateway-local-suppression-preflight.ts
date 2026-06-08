import type { Request, Response } from 'express'

import { logger } from '../../shared/logger.js'
import {
  filterLocallySuppressedGatewayAccounts,
  type LocalAccountSuppressionFilterResult
} from './gateway-account-side-effects.service.js'
import type { AuditCaptureContext } from './audit-capture.service.js'
import { sendGatewayFailureResponse } from './openai-gateway-failure-response.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'
import type { GatewayFailureUsageContext } from './openai-gateway-usage-records.js'

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
  signal?: AbortSignal
}): Promise<LocalAccountSuppressionFilterResult<UpstreamAccount> | undefined> {
  const filter = filterLocallySuppressedGatewayAccounts(input.accounts)
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
      ? '候选上游账号均处于本地短期屏蔽，立即返回'
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

  if (!filter.allSuppressed) {
    return filter
  }

  if (input.signal?.aborted || input.res.writableEnded) {
    return undefined
  }

  const statusCode = 503
  const responsePayload = gatewayErrorPayload('所有上游账户正在临时隔离，请稍后重试', 'service_unavailable')
  if (!input.res.headersSent && filter.nextRetryAfterMs !== undefined) {
    input.res.setHeader('Retry-After', String(Math.max(1, Math.ceil(filter.nextRetryAfterMs / 1000))))
  }
  sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable',
      errorMessage: responsePayload.error.message
    }
  })
  return undefined
}
