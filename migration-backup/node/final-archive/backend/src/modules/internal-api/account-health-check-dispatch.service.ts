import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { getTraceId } from '../../shared/request-context.js'
import { findAccountForAccountHealthJobsInputAsync, findAccountHealthJobsInputRevisionsAsync } from '../../storage/account-health-jobs-input.repository.js'
import { currentAccountHealthJobsInputVersionForRuntimeAsync } from '../../storage/account-health-jobs-input-version.repository.js'
import { publishAccountHealthJobsProbeRequest } from '../background/account-health-jobs-input.service.js'
import type { AccountHealthCheckTriggerReason, CodexSourceProbeFence } from '../accounts/account-health-check-trigger.js'
import { settleAccountHealthJobsSourceFence } from '../gateway/runtime/account-health-jobs-source-fence.consumer.js'
import { currentAccountHealthJobsProbeInput } from './account-health-jobs-dispatch-boundary.js'
import type { KeyModelFenceReference } from '../gateway/runtime/key-model-redis-store.js'

// Node publishes immutable request facts only.  Go jobs is the only J1 task
// owner; this path never calls a worker, Gateway, Redis Stream, or HTTP bridge.
export type AccountHealthCheckDispatchOutcome =
  | { outcome: 'queued'; decisionCode: 'queued'; targetRole: 'go-jobs' }
  | { outcome: 'rejected'; decisionCode: 'dispatch_rejected' | 'input_unavailable'; targetRole?: 'go-jobs' }

export function dispatchAccountHealthCheck(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId = getTraceId(),
  keyModelFence?: KeyModelFenceReference
): boolean {
  return dispatchAccountHealthCheckWithOutcome(accountId, reason, traceId, undefined, keyModelFence).outcome !== 'rejected'
}

export function dispatchAccountHealthCheckWithOutcome(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId = getTraceId(),
  sourceFence?: CodexSourceProbeFence,
  keyModelFence?: KeyModelFenceReference
): AccountHealthCheckDispatchOutcome {
  const normalizedId = accountId.trim()
  if (!normalizedId) return rejectedDispatchOutcome('dispatch_rejected')
  return publishGoJobsProbeRequest(normalizedId, reason, traceId, sourceFence, keyModelFence)
}

function publishGoJobsProbeRequest(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  traceId: string | undefined,
  sourceFence: CodexSourceProbeFence | undefined,
  keyModelFence: KeyModelFenceReference | undefined
): AccountHealthCheckDispatchOutcome {
  const root = runtimeConfig.accountHealthJobs.inputDirectory?.trim()
  const signingKey = runtimeConfig.accountHealthJobs.inputSigningKey?.trim()
  if (!root || !signingKey) return rejectedDispatchOutcome('input_unavailable')
  const requestId = `j1-${randomUUID()}`
  void (async () => {
    const account = await findAccountForAccountHealthJobsInputAsync(accountId)
    const inputVersion = account ? await currentAccountHealthJobsInputVersionForRuntimeAsync(accountId) : undefined
    const revisions = account ? await findAccountHealthJobsInputRevisionsAsync(accountId) : undefined
    const probeInput = currentAccountHealthJobsProbeInput(account, inputVersion, revisions)
    if (!probeInput) {
      // Gateway failure dispatch is shared by accounts outside the frozen J1
      // scope.  They have no J1 input epoch by design, so skip the request
      // without an error log while still settling a source fence, if present.
      if (sourceFence) await settleAccountHealthJobsSourceFence(sourceFence, 'unknown')
      return
    }
    publishAccountHealthJobsProbeRequest({
      account: probeInput.account,
      inputVersion: probeInput.inputVersion,
      root,
      signingKey,
      requestId,
      reason,
      deadline: new Date(Date.now() + runtimeConfig.background.accountHealthCheckProbeDeadlineMs),
      sourceFence,
      keyModelFence
    })
  })().catch((error) => {
    logger.warn({
      event: 'account_health_jobs_request_publish_failed',
      accountId,
      requestId,
      traceId,
      error: error instanceof Error ? error.name : 'unknown'
    }, 'J1 请求事实发布失败')
    if (sourceFence) {
      void settleAccountHealthJobsSourceFence(sourceFence, 'unknown').catch((settlementError) => {
        logger.warn(errorLogFields(settlementError, {
          event: 'account_health_jobs_source_fence_unknown_settlement_failed',
          accountId
        }), 'J1 请求事实发布失败后未能结算 source fence')
      })
    }
  })
  return { outcome: 'queued', decisionCode: 'queued', targetRole: 'go-jobs' }
}

function rejectedDispatchOutcome(
  decisionCode: Extract<AccountHealthCheckDispatchOutcome, { outcome: 'rejected' }>['decisionCode']
): AccountHealthCheckDispatchOutcome {
  return { outcome: 'rejected', decisionCode, targetRole: 'go-jobs' }
}
