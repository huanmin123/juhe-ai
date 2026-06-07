import { Router, type NextFunction, type Request, type Response } from 'express'

import { createTraceId, getRequestLogger, getTraceId } from '../../shared/request-context.js'
import { errorLogFields } from '../../shared/logger.js'
import {
  buildUsageRequestSnapshot,
  extractClientIp,
  requestEndpoint
} from './openai-gateway-usage.js'
import {
  isEffectiveOpenAIStreamRequest
} from './openai-gateway-upstream.js'
import {
  gatewayErrorPayload,
  sendGatewayErrorResponse,
  shouldHandleOpenAIUpstreamResponseAsStream
} from './openai-gateway-responses.js'
import { createAuditCapture } from './audit-capture.service.js'
import {
  type UpstreamAccount
} from './openai-gateway-route-helpers.js'
import {
  buildDiagnosticUpstreamError
} from './openai-gateway-error-helpers.js'
import {
  persistOpenAICodexHeadersIfNeeded
} from './openai-gateway-account-effects.js'
import {
  finalizeHandledUpstreamResponse,
  handleNonStreamUpstreamResponse,
  handleStreamUpstreamResponse
} from './openai-gateway-response-finalization.js'
import { sendGatewayFailureResponse } from './openai-gateway-failure-response.js'
import { handleUpstreamRequestError } from './openai-gateway-failure-dispatch.js'
import { handleGatewayRequestKnownErrorResponse } from './openai-gateway-request-error-response.js'
import {
  prepareOpenAIGatewayDispatchContext,
  prepareApiKeyGroupFallbackDispatchContext,
  type OpenAIGatewayDispatchContext,
  type OpenAIGatewayRequestIdentity
} from './openai-gateway-request-preflight.js'
import {
  fetchFirstAvailableUpstream,
  UpstreamAttemptError
} from './openai-gateway-upstream-dispatch.js'
import type { StreamInterceptDecision } from './openai-gateway-stream-intercept.js'
import type { GatewaySettings } from './request-error-policy.service.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'
import { recordClientIpErrorCircuitSample } from './openai-gateway-client-ip-error-circuit.service.js'
import { transferClientIpAccountPendingFailures } from './openai-gateway-client-ip-account-avoidance.service.js'
import type { GatewayFailureUsageContext } from './openai-gateway-usage-records.js'
import { isGatewayForcedDownstreamClose } from './openai-gateway-body.js'
import {
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from './openai-gateway-traffic-source.js'
import { resolveOpenAIGatewayRequestLane } from './openai-gateway-request-lane.js'

export const openAIGatewayRouter = Router()

export type { OpenAIGatewayRequestIdentity } from './openai-gateway-request-preflight.js'

export interface OpenAIGatewayHandleOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  exposeUpstreamDiagnostics?: boolean
  trafficSource?: OpenAIGatewayTrafficSource
  settingsOverride?: Partial<GatewaySettings>
  disableAccountStateMutation?: boolean
}

export function handleGatewayDbServiceUnavailable(error: unknown, req: Request, res: Response, next: NextFunction): void {
  const message = dbServiceUnavailableMessage(error)
  if (!message || res.headersSent) {
    next(error)
    return
  }

  getRequestLogger().error(errorLogFields(error, {
    event: 'gateway_db_service_unavailable',
    endpoint: `${req.method.toUpperCase()} ${requestEndpoint(req)}`
  }), '网关 DB service 不可用')

  sendGatewayErrorResponse(res, 503, gatewayErrorPayload(message, 'service_unavailable'))
}

function dbServiceUnavailableMessage(error: unknown): string | undefined {
  if (!(error instanceof Error)) {
    return undefined
  }
  return /^本地数据库服务(暂时不可用|未就绪|请求超时|已退出)/.test(error.message)
    ? error.message
    : undefined
}

openAIGatewayRouter.all('*', async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res)
  } catch (error) {
    handleGatewayDbServiceUnavailable(error, req, res, next)
  }
})

export async function handleOpenAIGatewayRequest(
  req: Request,
  res: Response,
  options: OpenAIGatewayHandleOptions = {}
): Promise<void> {
  const startedAt = Date.now()
  const abortController = new AbortController()
  const traceId = getTraceId() ?? createTraceId()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestLane = resolveOpenAIGatewayRequestLane(req)
  const trafficSource = normalizeOpenAIGatewayTrafficSource(options.trafficSource)
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const auditCapture = createAuditCapture({
    req,
    traceId,
    clientIp,
    startedAtMs: startedAt,
    trafficSource,
    captureMode: trafficSource === 'cooldown_retest' ? 'metadata_only' : 'default'
  })
  req.once('aborted', () => {
    auditCapture.markClientAborted()
    abortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded && !isGatewayForcedDownstreamClose(res)) {
      auditCapture.markClientAborted()
      abortController.abort()
    }
  })

  const preflight = await prepareOpenAIGatewayDispatchContext({
    req,
    res,
    auditCapture,
    options: { ...options, trafficSource, requestLane },
    startedAt,
    traceId,
    clientIp,
    endpoint,
    requestSnapshot,
    signal: abortController.signal
  })
  if (!preflight) {
    return
  }
  let currentPreflight = preflight
  let releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
  let streamServerRetryExcludedAccountIds = new Set<string>()
  let streamServerRetryCount = 0
  let maxStreamServerRetryCount = streamServerRetryLimit(currentPreflight.accounts)
  const exhaustedAccountIds = new Set<string>()
  const nonStreamResponseStartedFailedAccountIds = new Set<string>()
  const switchToFallbackGroup = async (
    reason: string,
    streamInterceptPolicies?: OpenAIGatewayDispatchContext['streamInterceptPolicies'],
    input: { allowCandidateWrap?: boolean } = {}
  ): Promise<'none' | 'switched' | 'completed'> => {
    const gatewayUsageContext = currentPreflight.usageContext
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options: {
        ...options,
        trafficSource,
        requestLane: currentPreflight.requestLane,
        streamInterceptPolicies
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: abortController.signal,
      reason,
      apiKeyRecord: currentPreflight.apiKeyRecord,
      systemAccountId: gatewayUsageContext.systemAccountId,
      apiKeyId: gatewayUsageContext.apiKeyId,
      groupId: gatewayUsageContext.groupId,
      trafficSource: gatewayUsageContext.trafficSource,
      requestLane: currentPreflight.requestLane,
      streamInterceptPolicies,
      excludedAccountIds: exhaustedAccountIds,
      allowCandidateWrap: input.allowCandidateWrap
    })
    if (!fallback.attempted) {
      return 'none'
    }
    if (!fallback.context) {
      return 'completed'
    }
    transferClientIpAccountPendingFailures(
      currentPreflight.clientIpAccountAvoidanceTracker,
      fallback.context.clientIpAccountAvoidanceTracker
    )
    releaseClientIpSlot()
    currentPreflight = fallback.context
    releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
    streamServerRetryExcludedAccountIds = new Set<string>()
    streamServerRetryCount = 0
    maxStreamServerRetryCount = streamServerRetryLimit(currentPreflight.accounts)
    return 'switched'
  }

  try {
    while (true) {
      const {
        activeGatewaySettings,
        usageContext: gatewayUsageContext,
        accounts,
        sessionAffinityKey,
        clientStrategy,
        clientIpAccountAvoidanceTracker,
        streamInterceptPolicies,
        errorPolicies,
        errorPolicyContext
      } = currentPreflight
      const dispatchAccounts = streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds)
      if (dispatchAccounts.length === 0) {
        for (const accountId of streamServerRetryExcludedAccountIds) {
          exhaustedAccountIds.add(accountId)
        }
        const fallbackSwitch = await switchToFallbackGroup('upstream_accounts_exhausted', streamInterceptPolicies, { allowCandidateWrap: true })
        if (fallbackSwitch === 'completed') {
          return
        }
        if (fallbackSwitch === 'switched') {
          continue
        }
        throw new UpstreamAttemptError('没有可用的上游账户')
      }
      let upstreamResult: Awaited<ReturnType<typeof fetchFirstAvailableUpstream>>
      try {
        upstreamResult = await fetchFirstAvailableUpstream(
          req,
          dispatchAccounts,
          activeGatewaySettings,
          gatewayUsageContext,
          auditCapture,
          sessionAffinityKey,
          abortController.signal,
          clientIpAccountAvoidanceTracker,
          currentPreflight.requestLane,
          currentPreflight.groupSchedulingPolicy,
          options.disableAccountStateMutation !== true,
          errorPolicies,
          errorPolicyContext
        )
      } catch (error) {
        if (error instanceof UpstreamAttemptError) {
          for (const accountId of nonStreamResponseStartedFailedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          for (const accountId of error.failedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          const fallbackSwitch = await switchToFallbackGroup('upstream_accounts_exhausted', streamInterceptPolicies, { allowCandidateWrap: true })
          if (fallbackSwitch === 'completed') {
            return
          }
          if (fallbackSwitch === 'switched') {
            continue
          }
        }
        throw error
      }
      const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, releaseConcurrency, markFirstOutput } = upstreamResult

      try {
        const responseHandlingStartedAt = Date.now()
        const contentType = upstreamResponse.headers.get('content-type') ?? ''
        const shouldHandleAsStream = shouldHandleOpenAIUpstreamResponseAsStream({
          contentType,
          streamRequest: isEffectiveOpenAIStreamRequest(req, account)
        })
        persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, gatewayUsageContext.trafficSource)

        let handledResponse: Awaited<ReturnType<typeof handleStreamUpstreamResponse>>
        if (shouldHandleAsStream) {
          handledResponse = await handleStreamUpstreamResponse({
            req,
            res,
            account,
            upstreamResponse,
            upstreamUrl,
            auditAttemptId,
            auditCapture,
            settings: activeGatewaySettings,
            usageContext: gatewayUsageContext,
            startedAt,
            signal: abortController.signal,
            sessionAffinityKey,
            clientStrategy,
            streamInterceptPolicies,
            markFirstOutput,
            clientIpAccountAvoidanceTracker,
            accountStateMutationEnabled: options.disableAccountStateMutation !== true
          })
        } else {
          try {
            handledResponse = await handleNonStreamUpstreamResponse({
              req,
              res,
              account,
              upstreamResponse,
              upstreamUrl,
              auditAttemptId,
              auditCapture,
              settings: activeGatewaySettings,
              usageContext: gatewayUsageContext,
              startedAt,
              signal: abortController.signal,
              sessionAffinityKey,
              markFirstOutput,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: options.disableAccountStateMutation !== true
            })
          } catch (error) {
            if (res.headersSent || res.writableEnded || res.destroyed) {
              throw error
            }
            const requestErrorResult = await handleUpstreamRequestError({
              req,
              usageContext: gatewayUsageContext,
              auditCapture,
              auditAttemptId,
              account,
              upstreamUrl,
              attemptStartedAt: responseHandlingStartedAt,
              attemptIndex: 0,
              auditAttemptIndex: 0,
              settings: activeGatewaySettings,
              sessionAffinityKey,
              signal: abortController.signal,
              lastAttempt: { accountId: account.id, accountName: account.name, upstreamUrl, status: upstreamResponse.status },
              failedProxyDispatchKeys: new Map(),
              error,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: options.disableAccountStateMutation !== true
            })
            nonStreamResponseStartedFailedAccountIds.add(account.id)
            if (requestErrorResult.action === 'skip_account') {
              streamServerRetryExcludedAccountIds.add(account.id)
              continue
            }
            throw error
          }
        }
        if (handledResponse.alreadyFinalized) {
          return
        }
        if (handledResponse.retryUpstream) {
          streamServerRetryCount += 1
          if (shouldExcludeCurrentAccountForStreamRetry(handledResponse.streamIntercept)) {
            streamServerRetryExcludedAccountIds.add(account.id)
          }
          auditCapture.addGatewayMetadata({
            label: 'stream_intercept_server_retry_dispatch',
            metadata: {
              retryCount: streamServerRetryCount,
              maxRetryCount: maxStreamServerRetryCount,
              accountId: account.id,
              excludedAccountIds: [...streamServerRetryExcludedAccountIds],
              policyId: handledResponse.streamIntercept.policyId,
              policyName: handledResponse.streamIntercept.policyName,
              accountSwitch: handledResponse.streamIntercept.accountSwitch
            }
          })
          if (
            streamServerRetryCount > maxStreamServerRetryCount
            || streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds).length === 0
          ) {
            for (const accountId of streamServerRetryExcludedAccountIds) {
              exhaustedAccountIds.add(accountId)
            }
            const fallbackSwitch = await switchToFallbackGroup('stream_intercept_server_retry_exhausted', streamInterceptPolicies, { allowCandidateWrap: true })
            if (fallbackSwitch !== 'none') {
              if (fallbackSwitch === 'completed') {
                return
              }
              continue
            }
            sendStreamServerRetryExhaustedResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              decision: handledResponse.streamIntercept,
              message: handledResponse.message
            })
            return
          }
          continue
        }
        finalizeHandledUpstreamResponse({
          req,
          res,
          account,
          upstreamResponse,
          upstreamUrl,
          auditAttemptId,
          auditCapture,
          settings: activeGatewaySettings,
          usageContext: gatewayUsageContext,
          startedAt,
          signal: abortController.signal,
          result: handledResponse,
          clientIpAccountAvoidanceTracker,
          accountStateMutationEnabled: options.disableAccountStateMutation !== true
        })
        return
      } finally {
        releaseConcurrency()
      }
    }
  } catch (error) {
    if (error instanceof UpstreamAttemptError) {
      for (const accountId of nonStreamResponseStartedFailedAccountIds) {
        error.failedAccountIds.push(accountId)
      }
    }
    const gatewayUsageContext = currentPreflight.usageContext
    recordKnownClientIpRequestError(error, gatewayUsageContext, auditCapture)
    if (handleGatewayRequestKnownErrorResponse({
      res,
      auditCapture,
      error,
      signal: abortController.signal
    })) {
      return
    }
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : '没有可用的上游账户'
    const diagnosticError = options.exposeUpstreamDiagnostics
      ? buildDiagnosticUpstreamError(lastAttempt, message)
      : undefined
    const statusCode = diagnosticError?.statusCode ?? 503
    const responsePayload = diagnosticError?.payload ?? gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: gatewayUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'upstream_failed',
        errorPhase: 'dispatch',
        errorCode: 'service_unavailable',
        errorMessage: diagnosticError?.errorMessage ?? message
      },
      recordUsage: !lastAttempt,
      usageErrorMessage: message
    })
  } finally {
    releaseClientIpSlot()
  }
}

function once(callback: () => void): () => void {
  let called = false
  return () => {
    if (called) return
    called = true
    callback()
  }
}

function attachClientIpSlotRelease(res: Response, preflight: OpenAIGatewayDispatchContext): () => void {
  const releaseClientIpSlot = once(preflight.releaseClientIpConcurrency)
  res.once('finish', releaseClientIpSlot)
  res.once('close', releaseClientIpSlot)
  return releaseClientIpSlot
}

function streamServerRetryLimit(accounts: UpstreamAccount[]): number {
  return Math.max(1, Math.min(3, accounts.length))
}

function streamRetryDispatchAccounts(accounts: UpstreamAccount[], excludedAccountIds: Set<string>): UpstreamAccount[] {
  if (excludedAccountIds.size === 0) {
    return accounts
  }
  return accounts.filter((account) => !excludedAccountIds.has(account.id))
}

function shouldExcludeCurrentAccountForStreamRetry(decision: StreamInterceptDecision): boolean {
  return decision.accountSwitch === 'request_next_account'
    || decision.accountSwitch === 'avoid_account_ttl'
    || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    || decision.accountState === 'runtime_avoidance'
}

function sendStreamServerRetryExhaustedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  decision: StreamInterceptDecision
  message: string
}): void {
  const message = input.message || '流式响应命中拦截策略，服务端重试未找到可用账号'
  const responsePayload = gatewayErrorPayload(message, 'service_unavailable', 'stream_intercept_retry_exhausted')
  input.auditCapture.addGatewayMetadata({
    label: 'stream_intercept_server_retry_exhausted',
    metadata: {
      policyId: input.decision.policyId,
      policyName: input.decision.policyName,
      accountSwitch: input.decision.accountSwitch,
      retryEnabled: input.decision.retryEnabled,
      matchedField: input.decision.matchedField,
      matchedValue: input.decision.matchedValue
    }
  })
  sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode: 503,
    responsePayload,
    audit: {
      outcome: 'upstream_failed',
      errorPhase: 'dispatch',
      errorCode: 'stream_intercept_retry_exhausted',
      errorMessage: message
    },
    recordUsage: false,
    usageErrorMessage: message
  })
}

function recordKnownClientIpRequestError(
  error: unknown,
  usageContext: GatewayFailureUsageContext,
  auditCapture: ReturnType<typeof createAuditCapture>
): void {
  const sample = clientIpRequestErrorSample(error)
  if (!sample) {
    return
  }
  const result = recordClientIpErrorCircuitSample({
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp,
    endpoint: usageContext.endpoint,
    reason: sample.reason,
    signature: sample.signature
  })
  if (!result.blocked) {
    return
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_error_circuit_opened',
    reason: sample.reason,
    retryAfterSeconds: result.retryAfterSeconds,
    failureCount: result.failureCount,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp
  }, '客户端 IP 级错误熔断已打开')
  auditCapture.addGatewayMetadata({
    label: 'client_ip_error_circuit',
    metadata: {
      opened: true,
      reason: sample.reason,
      retryAfterSeconds: result.retryAfterSeconds,
      failureCount: result.failureCount
    }
  })
}

function clientIpRequestErrorSample(error: unknown): { reason: 'adapter_request_validation'; signature: string } | undefined {
  if (error instanceof OpenAIOAuthCodexAdapterError) {
    return {
      reason: 'adapter_request_validation',
      signature: [error.type, error.code].filter(Boolean).join('|') || error.message
    }
  }
  return undefined
}
