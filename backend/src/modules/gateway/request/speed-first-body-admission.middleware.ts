import type { NextFunction, Response } from 'express'

import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, resolveGroupSchedulingPolicy } from '../../../domain/group-scheduling.js'
import { logRequestStage } from '../../../shared/request-context.js'
import { gatewayAccountConcurrencyLimitsByAccountId } from '../dispatch/account-concurrency-identity.js'
import { gatewayErrorPayload, sendGatewayJsonError } from '../response/responses.js'
import { acquireSpeedFirstBodyAdmission } from '../runtime/speed-first-body-admission.service.js'
import { recordGatewayBodyRejection } from './body-middleware.js'
import type { GatewayRuntimeRequest } from './pre-auth.js'

export async function admitSpeedFirstRequestBody(
  req: GatewayRuntimeRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  const stageStartedAt = performance.now()
  const runtime = req.gatewayRuntime
  const apiKey = runtime?.apiKey
  const groupAccess = runtime?.groupAccess
  if (
    !apiKey
    || apiKey.route_strategy_mode !== 'normal'
    || apiKey.normal_routing_config?.schedulingPreference !== 'speed_first'
    || groupAccess?.groupType !== 'high_concurrency'
    || runtime.accounts.length === 0
  ) {
    logRequestStage('body.speed_first_admission', {
      admissionMode: 'speed_first_high_concurrency',
      applicable: false
    }, 'skipped', stageStartedAt)
    next()
    return
  }

  const policy = resolveGroupSchedulingPolicy('high_concurrency', groupAccess.schedulingPolicy)
    ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const abortController = new AbortController()
  const abortWait = () => abortController.abort()
  req.once('aborted', abortWait)
  res.once('close', abortWait)
  try {
    const decision = await acquireSpeedFirstBodyAdmission({
      systemAccountId: apiKey.system_account_id,
      routeStrategyId: apiKey.route_strategy_id,
      groupId: apiKey.selected_group_id,
      apiKeyId: apiKey.id,
      capacity: bodyAdmissionCapacity(runtime.accounts),
      maxQueueWaitMs: policy.maxQueueWaitMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueWaitMs,
      maxQueueSize: policy.maxQueueSize ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueSize,
      perApiKeyQueueLimit: policy.perApiKeyQueueLimit ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.perApiKeyQueueLimit,
      signal: abortController.signal
    })
    req.off('aborted', abortWait)
    res.off('close', abortWait)
    if (!decision.acquired) {
      if (decision.reason === 'aborted' || req.aborted || res.destroyed) {
        logRequestStage('body.speed_first_admission', {
          admissionMode: 'speed_first_high_concurrency',
          reason: decision.reason
        }, 'aborted', stageStartedAt)
        return
      }
      const responsePayload = gatewayErrorPayload('当前分组繁忙，请稍后重试或增加可用账户。', 'rate_limit_error')
      res.setHeader('Connection', 'close')
      await recordGatewayBodyRejection(req, {
        statusCode: 429,
        responsePayload,
        rawBodyBytes: contentLengthBytes(req),
        reason: 'gateway_body_admission',
        errorCode: `speed_first_body_admission_${decision.reason}`,
        errorMessage: responsePayload.error.message
      })
      sendGatewayJsonError(res, 429, responsePayload)
      logRequestStage('body.speed_first_admission', {
        admissionMode: 'speed_first_high_concurrency',
        failureReason: `speed_first_body_admission_${decision.reason}`,
        decisionInputs: {
          reason: decision.reason,
          capacity: bodyAdmissionCapacity(runtime.accounts),
          maxQueueWaitMs: policy.maxQueueWaitMs,
          maxQueueSize: policy.maxQueueSize,
          perApiKeyQueueLimit: policy.perApiKeyQueueLimit
        }
      }, 'expected_failure', stageStartedAt)
      return
    }

    let released = false
    const release = () => {
      if (released) return
      released = true
      decision.release()
    }
    res.once('finish', release)
    res.once('close', release)
    if (req.aborted || res.destroyed) {
      release()
      logRequestStage('body.speed_first_admission', {
        admissionMode: 'speed_first_high_concurrency'
      }, 'aborted', stageStartedAt)
      return
    }
    logRequestStage('body.speed_first_admission', {
      admissionMode: 'speed_first_high_concurrency',
      acquired: true,
      capacity: bodyAdmissionCapacity(runtime.accounts)
    }, 'success', stageStartedAt)
    next()
  } catch (error) {
    req.off('aborted', abortWait)
    res.off('close', abortWait)
    logRequestStage('body.speed_first_admission', {
      admissionMode: 'speed_first_high_concurrency',
      error
    }, 'unexpected_failure', stageStartedAt)
    next(error)
  }
}

function bodyAdmissionCapacity(accounts: Array<{ id: string; credentialSourceAccountId?: string; concurrencyLimit: number }>): number {
  return Object.values(gatewayAccountConcurrencyLimitsByAccountId(accounts))
    .reduce((total, limit) => total + limit, 0)
}

function contentLengthBytes(req: GatewayRuntimeRequest): number {
  const value = req.header('content-length')
  if (!value) return 0
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0
}
