import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { sendAccountHealthCheckTriggerToWorker } from '../background/background-ipc.js'
import type { AccountHealthCheckTriggerReason } from '../accounts/account-health-check-trigger.js'
import {
  accountHealthCheckDispatchInternalPrefix,
  createAccountHealthCheckDispatchSignature
} from './account-health-check-dispatch.routes.js'

const accountHealthCheckDispatchPath = '/v1/account-health-check/dispatch'
const accountHealthCheckDispatchTimeoutMs = 2_000
const requestFailureDispatchCooldownMs = 10 * 60_000
const requestFailureDispatchInFlight = new Set<string>()
const requestFailureDispatchAcceptedAt = new Map<string, number>()
let missingGatewayDispatchTargetLogged = false

export function dispatchAccountHealthCheck(accountId: string, reason: AccountHealthCheckTriggerReason): boolean {
  const normalizedId = accountId.trim()
  if (!normalizedId) return false
  if (requestFailureDispatchIsCoolingDown(normalizedId, reason)) return false
  if (
    runtimeConfig.runtimeMode === 'performance'
    && runtimeConfig.performanceNodeRole === 'gateway'
    && runtimeConfig.processRole === 'server'
  ) {
    return dispatchAccountHealthCheckToControl(normalizedId, reason)
  }
  if (runtimeConfig.processRole !== 'db-service') {
    return rememberAcceptedRequestFailureDispatch(
      normalizedId,
      reason,
      sendAccountHealthCheckTriggerToWorker(normalizedId, reason)
    )
  }
  if (!process.send || process.connected === false) return false
  try {
    process.send({
      type: 'background_worker_account_health_check_trigger',
      accountId: normalizedId,
      reason
    }, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'account_health_check_db_service_dispatch_failed',
          accountId: normalizedId
        }), 'DB service 投递账户健康检查触发消息失败')
      }
    })
    return rememberAcceptedRequestFailureDispatch(normalizedId, reason, true)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_health_check_db_service_dispatch_failed',
      accountId: normalizedId
    }), 'DB service 投递账户健康检查触发消息失败')
    return false
  }
}

function dispatchAccountHealthCheckToControl(
  accountId: string,
  reason: AccountHealthCheckTriggerReason
): boolean {
  const dispatchUrl = accountHealthCheckDispatchTargetUrl()
  if (!dispatchUrl) {
    if (!missingGatewayDispatchTargetLogged) {
      missingGatewayDispatchTargetLogged = true
      logger.warn({
        event: 'account_health_check_control_dispatch_unconfigured'
      }, 'Gateway 未配置 control 健康检查投递地址，无法触发独立账户检查')
    }
    return false
  }
  if (reason === 'request_failure') requestFailureDispatchInFlight.add(accountId)
  void postAccountHealthCheckDispatch(dispatchUrl, accountId, reason)
    .then((accepted) => {
      rememberAcceptedRequestFailureDispatch(accountId, reason, accepted)
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'account_health_check_control_dispatch_failed',
        accountId
      }), 'Gateway 向 control 投递账户健康检查失败')
    })
    .finally(() => {
      if (reason === 'request_failure') requestFailureDispatchInFlight.delete(accountId)
      cleanupRequestFailureDispatchCooldowns(Date.now())
    })
  return true
}

async function postAccountHealthCheckDispatch(
  url: URL,
  accountId: string,
  reason: AccountHealthCheckTriggerReason
): Promise<boolean> {
  const body = Buffer.from(JSON.stringify({ accountId, reason }), 'utf8')
  const response = await fetch(url, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'content-length': String(body.byteLength),
      'x-juhe-ai-signature': createAccountHealthCheckDispatchSignature(runtimeConfig.secret, body)
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

function requestFailureDispatchIsCoolingDown(
  accountId: string,
  reason: AccountHealthCheckTriggerReason
): boolean {
  if (reason !== 'request_failure') return false
  if (requestFailureDispatchInFlight.has(accountId)) return true
  const now = Date.now()
  cleanupRequestFailureDispatchCooldowns(now)
  const acceptedAt = requestFailureDispatchAcceptedAt.get(accountId)
  return acceptedAt !== undefined && now - acceptedAt < requestFailureDispatchCooldownMs
}

function rememberAcceptedRequestFailureDispatch(
  accountId: string,
  reason: AccountHealthCheckTriggerReason,
  accepted: boolean
): boolean {
  if (accepted && reason === 'request_failure') {
    requestFailureDispatchAcceptedAt.set(accountId, Date.now())
  }
  return accepted
}

function cleanupRequestFailureDispatchCooldowns(now: number): void {
  const cutoff = now - requestFailureDispatchCooldownMs
  for (const [accountId, acceptedAt] of requestFailureDispatchAcceptedAt) {
    if (acceptedAt <= cutoff) requestFailureDispatchAcceptedAt.delete(accountId)
  }
}
