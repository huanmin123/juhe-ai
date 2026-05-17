import { Router, type NextFunction, type Request, type Response } from 'express'

import { tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import { bindRequestContextFields, createTraceId, getRequestLogger, getTraceId } from '../../shared/request-context.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  type GatewayApiKeyRow,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { enqueueUsageRecord } from './usage-record-queue.service.js'
import {
  listCachedOpenAIAccountsForGroupAsync,
  readCachedGatewaySettingsAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from './gateway-runtime-cache.service.js'
import { shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'
import { refreshOpenAIOAuthAccountAccessToken } from '../openai-oauth/openai-oauth-access-token-refresh.service.js'
import {
  parseErrorPayload,
  type GatewaySettings
} from './account-error-policy.service.js'
import {
  buildUsageRequestSnapshot,
  buildUsageResponseSnapshot,
  emptyUsage,
  extractClientIp,
  applyOpenAIStreamUsageFallback,
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromJsonTextFragment,
  requestEndpoint,
  requestModel,
  extractBearerToken,
  type UpstreamAttempt
} from './openai-gateway-usage.js'
import {
  buildUpstreamRequestParts,
  copyResponseHeaders,
  isEffectiveOpenAIStreamRequest,
  isUpstreamRequestAbortedError,
  requestUpstream,
  UpstreamRequestAbortedError,
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  type GatewayUpstreamResponse
} from './openai-gateway-upstream.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'
import {
  groupUsageMetadata,
  recordClientAbortedUpstreamAttempt,
  recordCompletedUpstreamAttempt,
  recordGatewayFailure,
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from './openai-gateway-usage-records.js'
import {
  gatewayErrorPayload,
  isOpenAIStreamContentType,
  sendGatewayJsonError,
  writeGatewayStreamFailureEvent
} from './openai-gateway-responses.js'
import { resolveGatewayRuntimeAsync } from './openai-gateway-request.js'
import { pipeUpstreamStream } from './openai-gateway-stream.js'
import { pipeNonStreamUpstreamResponse, readUpstreamBodyLimited } from './openai-gateway-body.js'
import { createAuditCapture, responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import { API_KEY_QUOTA_EXCEEDED_MESSAGE, checkGatewayApiKeyQuotaAsync } from './api-key-quota.service.js'
import { AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE, checkGatewayAuthorizationQuotaAsync, checkGatewayAuthorizationQuotaBatchAsync } from './authorization-quota.service.js'
import {
  forgetOpenAIAccountForSession,
  orderOpenAIAccountsBySessionAffinity,
  rememberOpenAIAccountForSession,
  resolveOpenAIGatewaySessionAffinityKey
} from './openai-gateway-session-affinity.service.js'
import {
  buildOpenAIModelsResponse,
  buildUpstreamUrlsForAccount,
  isOpenAIModelsRequest,
  type UpstreamAccount
} from './openai-gateway-route-helpers.js'
import {
  filterLocallySuppressedGatewayAccounts
} from './gateway-account-side-effects.service.js'
import {
  buildDiagnosticUpstreamError,
  parseClientVisibleUpstreamErrorForAudit,
  sendRawUpstreamErrorResponse
} from './openai-gateway-error-helpers.js'
import {
  failedProxyDispatchReason,
  rememberFailedProxyForDispatch,
  shouldRecordAbortedUpstreamAttempt,
  throwIfRequestAborted,
  waitBeforeTemporaryUnschedulableRetry
} from './openai-gateway-dispatch-helpers.js'
import {
  streamInterceptAuditMetadata
} from './openai-gateway-audit-metadata.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  clearAccountStreamFailureStateWithCacheInvalidation,
  handleStreamFailure,
  persistOpenAICodexHeadersIfNeeded
} from './openai-gateway-account-effects.js'
import {
  flushDeferredAccountFailures,
  handleFailedUpstreamResponse,
  UpstreamRejectedRequestError,
  type DeferredAccountFailure
} from './openai-gateway-failure-dispatch.js'

export const openAIGatewayRouter = Router()

export interface OpenAIGatewayRequestIdentity {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
}

export interface OpenAIGatewayHandleOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  exposeUpstreamDiagnostics?: boolean
}

function handleGatewayDbServiceUnavailable(error: unknown, req: Request, res: Response, next: NextFunction): void {
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
  return /^DB service (暂时不可用|未就绪|请求超时|已退出)/.test(error.message)
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
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  let gatewaySettings: GatewaySettings | undefined
  const auditCapture = createAuditCapture({ req, traceId, clientIp, startedAtMs: startedAt })
  req.once('aborted', () => {
    auditCapture.markClientAborted()
    abortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded) {
      auditCapture.markClientAborted()
      abortController.abort()
    }
  })

  let apiKeyRecord: GatewayApiKeyRow | undefined
  let runtimeGroupAccess: GroupUsageAccessMetadata | undefined
  let runtimeAccounts: UpstreamAccount[] | undefined
  const identity = options.identity
    ? (() => {
      return options.identity
    })()
    : await (async () => {
      const runtime = await resolveGatewayRuntimeAsync(req, res)
      if (!runtime?.apiKey) {
        const authErrorMessage = extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少 Bearer Token'
        const authErrorPayload = gatewayErrorPayload(authErrorMessage, 'invalid_request_error')
        auditCapture.finalize({
          outcome: 'gateway_failed',
          success: false,
          statusCode: res.statusCode,
          responseHeaders: responseHeadersToObject(res),
          responseBody: JSON.stringify(authErrorPayload),
          responsePartType: 'gateway_error',
          errorPhase: 'auth',
          errorCode: 'invalid_request_error',
          errorMessage: authErrorMessage
        })
        return undefined
      }
      gatewaySettings = runtime.settings
      apiKeyRecord = runtime.apiKey
      runtimeGroupAccess = runtime.groupAccess
      runtimeAccounts = runtime.accounts
      return {
        systemAccountId: runtime.apiKey.system_account_id,
        apiKeyId: runtime.apiKey.id,
        groupId: runtime.apiKey.group_id
      }
    })()
  if (!identity) {
    return
  }
  const activeGatewaySettings = gatewaySettings ?? await readCachedGatewaySettingsAsync()
  const { systemAccountId, apiKeyId, groupId } = identity

  auditCapture.bindContext({
    systemAccountId,
    apiKeyId,
    groupId,
    providerCode: 'openai'
  })
  bindRequestContextFields({
    systemAccountId,
    apiKeyId,
    groupId
  })

  const groupAccess = runtimeGroupAccess ?? await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  if (!groupAccess) {
    const statusCode = 403
    const responsePayload = gatewayErrorPayload('API Key 绑定的分组授权不可用', 'forbidden')
    recordGatewayFailure(req, {
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      endpoint,
      requestSnapshot
    }, {
      statusCode,
      startedAt,
      responsePayload
    })
    sendGatewayErrorResponse(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'authorization',
      errorCode: 'forbidden',
      errorMessage: 'API Key 绑定的分组授权不可用'
    })
    return
  }

  const groupUsageFields = groupUsageMetadata(groupAccess)
  const quotaDecision = apiKeyRecord ? await checkGatewayApiKeyQuotaAsync(apiKeyRecord) : { allowed: true }
  if (!quotaDecision.allowed) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload(quotaDecision.message ?? API_KEY_QUOTA_EXCEEDED_MESSAGE, 'rate_limit_exceeded')
    recordGatewayFailure(req, {
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    }, {
      statusCode,
      startedAt,
      responsePayload
    })
    sendGatewayErrorResponse(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'quota',
      errorCode: 'rate_limit_exceeded',
      errorMessage: responsePayload.error.message
    })
    return
  }

  const groupAuthorizationQuotaDecision = await checkGatewayAuthorizationQuotaAsync({ groupAccess })
  if (!groupAuthorizationQuotaDecision.allowed) {
    sendQuotaExceededResponse(req, res, auditCapture, {
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    }, startedAt, groupAuthorizationQuotaDecision.message ?? AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE)
    return
  }

  if (isOpenAIModelsRequest(req)) {
    const responsePayload = buildOpenAIModelsResponse()
    res.status(200).json(responsePayload)
    enqueueUsageRecord({
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      ...groupUsageFields,
      endpoint,
      providerCode: 'openai',
      stream: false,
      statusCode: 200,
      success: true,
      firstTokenMs: Date.now() - startedAt,
      durationMs: Date.now() - startedAt
    })
    auditCapture.finalize({
      outcome: 'success',
      success: true,
      statusCode: 200,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_response',
      firstTokenMs: Date.now() - startedAt
    })
    return
  }

  const sessionAffinityKey = resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId,
    apiKeyId,
    groupId
  })
  const orderedCandidateAccounts = orderOpenAIAccountsBySessionAffinity(
    options.candidateAccounts ?? runtimeAccounts ?? await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId),
    options.disableSessionAffinity ? undefined : sessionAffinityKey
  )
  const localSuppressionFilter = filterLocallySuppressedGatewayAccounts(orderedCandidateAccounts)
  if (localSuppressionFilter.suppressedCount > 0) {
    logger.warn({
      event: 'gateway_local_account_suppression_applied',
      suppressedCount: localSuppressionFilter.suppressedCount,
      bypassedAllSuppressed: localSuppressionFilter.bypassedAllSuppressed,
      groupId,
      systemAccountId
    }, '网关本地短期屏蔽账号已应用到候选列表')
  }
  const candidateAccounts = localSuppressionFilter.accounts
  let authorizationQuotaDeniedAccountCount = 0
  const accounts: UpstreamAccount[] = []
  const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess, accounts: candidateAccounts })
  for (const account of candidateAccounts) {
    const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
    if (!decision.allowed) {
      authorizationQuotaDeniedAccountCount += 1
      continue
    }
    accounts.push(account)
  }
  if (accounts.length === 0) {
    if (authorizationQuotaDeniedAccountCount > 0) {
      sendQuotaExceededResponse(req, res, auditCapture, {
        traceId,
        clientIp,
        systemAccountId,
        apiKeyId,
        groupId,
        ...groupUsageFields,
        endpoint,
        requestSnapshot
      }, startedAt, AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE)
      return
    }
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    recordGatewayFailure(req, {
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    }, {
      statusCode,
      startedAt,
      responsePayload
    })
    sendGatewayErrorResponse(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable',
      errorMessage: '没有可用的上游账户'
    })
    return
  }

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(req, accounts, activeGatewaySettings, {
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    }, auditCapture, options.disableSessionAffinity ? undefined : sessionAffinityKey, abortController.signal)
    const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, releaseConcurrency } = upstreamResult

    try {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const shouldHandleAsStream = isOpenAIStreamContentType(contentType) || isImplicitOpenAIStreamResponse(req, account)
      res.status(upstreamResponse.status)
      copyResponseHeaders(upstreamResponse, res)
      if (shouldHandleAsStream && !res.hasHeader('content-type')) {
        res.setHeader('content-type', 'text/event-stream; charset=utf-8')
      }
      if (shouldHandleAsStream) {
        setGatewayStreamResponseHeaders(res)
        flushResponseHeadersIfSupported(res)
      }
      persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, 'gateway')

      let usage = emptyUsage()
      let firstTokenMs: number | undefined
      let responseBodyText: string | undefined
      let responseUsageText: string | undefined
      let errorPayload: Record<string, unknown> = {}
      if (shouldHandleAsStream) {
        if (!upstreamResponse.body) {
          const responsePayload = gatewayErrorPayload('上游响应体为空', 'upstream_response_error')
          sendGatewayErrorResponse(res, upstreamResponse.status, responsePayload)
          forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
          auditCapture.completeAttempt(auditAttemptId, {
            statusCode: upstreamResponse.status,
            responseHeaders: upstreamResponse.headers,
            success: false,
            errorPhase: 'upstream_response',
            errorMessage: '上游响应体为空'
          })
          recordCompletedUpstreamAttempt(req, {
            traceId,
            clientIp,
            systemAccountId,
            apiKeyId,
            groupId,
            account,
            endpoint,
            statusCode: upstreamResponse.status,
            success: false,
            stream: isEffectiveOpenAIStreamRequest(req, account),
            firstTokenMs,
            startedAt,
            usage: emptyUsage(),
            requestSnapshot,
            responseSnapshot: buildUsageResponseSnapshot({
              upstreamUrl,
              statusCode: upstreamResponse.status,
              headers: upstreamResponse.headers,
              errorMessage: '上游响应体为空'
            }),
            errorMessage: '上游响应体为空'
          })
          auditCapture.finalize({
            outcome: 'stream_failed',
            success: false,
            statusCode: upstreamResponse.status,
            responseHeaders: responseHeadersToObject(res),
            responseBody: JSON.stringify(responsePayload),
            responsePartType: 'gateway_response',
            errorPhase: 'upstream_response',
            errorMessage: '上游响应体为空',
            accountId: account.id
          })
          return
        }
        let streamResult: Awaited<ReturnType<typeof pipeUpstreamStream>>
        try {
          streamResult = await pipeUpstreamStream(
            upstreamResponse.body,
            res,
            activeGatewaySettings,
            startedAt,
            (message, errorCode, context) => handleStreamFailure(account, message, activeGatewaySettings, errorCode, context),
            abortController.signal
          )
        } catch (error) {
          if (isUpstreamRequestAbortedError(error) || abortController.signal.aborted) {
            recordClientAbortedUpstreamAttempt(req, {
              traceId,
              clientIp,
              systemAccountId,
              apiKeyId,
              groupId,
              account,
              endpoint,
              statusCode: upstreamResponse.status,
              stream: isEffectiveOpenAIStreamRequest(req, account),
              firstTokenMs,
              startedAt,
              requestSnapshot,
              responseSnapshot: buildUsageResponseSnapshot({
                upstreamUrl,
                statusCode: upstreamResponse.status,
                headers: upstreamResponse.headers,
                bodyText: responseBodyText,
                errorMessage: '请求已取消'
              })
            })
            auditCapture.completeAttempt(auditAttemptId, {
              statusCode: upstreamResponse.status,
              responseHeaders: upstreamResponse.headers,
              success: false,
              errorPhase: 'client',
              errorMessage: '请求已取消'
            })
          }
          throw error
        }
        firstTokenMs = streamResult.firstTokenMs
        responseBodyText = streamResult.responseBodyText
        const streamUsageFallback = applyOpenAIStreamUsageFallback(req, streamResult.usage, {
          outputReceived: streamResult.outputReceived,
          estimatedOutputTokens: streamResult.estimatedOutputTokens
        })
        if (streamUsageFallback.estimated) {
          logger.warn({
            event: 'gateway_stream_usage_estimated',
            accountId: account.id,
            endpoint,
            model: requestModel(req),
            completed: streamResult.completed,
            outputReceived: streamResult.outputReceived,
            estimatedInputTokens: streamUsageFallback.estimatedInputTokens,
            estimatedOutputTokens: streamUsageFallback.estimatedOutputTokens
          }, '上游流式响应缺少 usage，网关已按可见输出估算 token 成本')
        }
        if (streamResult.streamIntercept) {
          auditCapture.addGatewayMetadata({
            label: 'stream_intercept',
            metadata: streamInterceptAuditMetadata(streamResult.streamIntercept)
          })
        }
        auditCapture.completeAttempt(auditAttemptId, {
          statusCode: upstreamResponse.status,
          responseHeaders: upstreamResponse.headers,
          responseBody: streamResult.auditUpstreamBody,
          success: streamResult.completed && upstreamResponse.ok,
          errorPhase: streamResult.completed ? undefined : 'stream',
          errorCode: streamResult.completed ? undefined : streamResult.errorCode,
          errorMessage: streamResult.completed ? undefined : streamResult.message
        })
        if (!streamResult.completed) {
          forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
          recordCompletedUpstreamAttempt(req, {
            traceId,
            clientIp,
            systemAccountId,
            apiKeyId,
            groupId,
            account,
            endpoint,
            statusCode: upstreamResponse.status,
            success: false,
            stream: isEffectiveOpenAIStreamRequest(req, account),
            firstTokenMs: streamResult.firstTokenMs,
            startedAt,
            usage: streamUsageFallback.usage,
            errorCode: streamResult.streamIntercept?.upstreamErrorCode ?? streamResult.errorCode,
            requestSnapshot,
            responseSnapshot: buildUsageResponseSnapshot({
              upstreamUrl,
              statusCode: upstreamResponse.status,
              headers: upstreamResponse.headers,
              bodyText: responseBodyText,
              errorMessage: streamResult.message
            }),
            errorMessage: streamResult.message
          })
          auditCapture.finalize({
            outcome: 'stream_failed',
            success: false,
            statusCode: upstreamResponse.status,
            responseHeaders: responseHeadersToObject(res),
            responseBody: streamResult.auditResponseBody,
            responsePartType: 'gateway_response',
            errorPhase: 'stream',
            errorCode: streamResult.streamIntercept?.upstreamErrorCode ?? streamResult.errorCode,
            errorMessage: streamResult.message,
            accountId: account.id,
            firstTokenMs: streamResult.firstTokenMs
          })
          return
        }
        usage = streamUsageFallback.usage
      } else {
        if (abortController.signal.aborted) {
          throw new UpstreamRequestAbortedError('请求已取消', true)
        }
        let responseBody: Buffer | undefined
        try {
          if (!upstreamResponse.body) {
            responseBody = Buffer.alloc(0)
            responseBodyText = ''
            firstTokenMs = Date.now() - startedAt
            res.end()
          } else if (upstreamResponse.ok) {
            const pipeResult = await pipeNonStreamUpstreamResponse(upstreamResponse.body, res, {
              startedAt,
              signal: abortController.signal
            })
            responseBody = pipeResult.capturedBody
            responseBodyText = pipeResult.captureTruncated ? undefined : pipeResult.capturedBodyText
            responseUsageText = pipeResult.usageTailText
            firstTokenMs = pipeResult.firstByteMs
            if (pipeResult.captureTruncated) {
              logger.warn({
                event: 'gateway_non_stream_response_capture_truncated',
                accountId: account.id,
                statusCode: upstreamResponse.status,
                transferredBytes: pipeResult.transferredBytes,
                endpoint
              }, '网关非流式响应过大，已边转发并跳过完整响应捕获')
            }
          } else {
            const readResult = await readUpstreamBodyLimited(upstreamResponse.body, {
              startedAt,
              signal: abortController.signal
            })
            responseBody = readResult.body
            responseBodyText = readResult.diagnosticBodyText
            responseUsageText = responseBodyText
            firstTokenMs = readResult.firstByteMs ?? Date.now() - startedAt
            if (readResult.truncated) {
              logger.warn({
                event: 'gateway_upstream_error_body_truncated',
                accountId: account.id,
                statusCode: upstreamResponse.status,
                readBytes: readResult.readBytes,
                endpoint
              }, '上游错误响应体超过网关捕获上限，已截断用于诊断')
            }
            res.send(readResult.body)
          }
        } catch (error) {
          if (isUpstreamRequestAbortedError(error) || abortController.signal.aborted) {
            recordClientAbortedUpstreamAttempt(req, {
              traceId,
              clientIp,
              systemAccountId,
              apiKeyId,
              groupId,
              account,
              endpoint,
              statusCode: upstreamResponse.status,
              stream: isEffectiveOpenAIStreamRequest(req, account),
              firstTokenMs,
              startedAt,
              requestSnapshot,
              responseSnapshot: buildUsageResponseSnapshot({
                upstreamUrl,
                statusCode: upstreamResponse.status,
                headers: upstreamResponse.headers,
                bodyText: responseBodyText,
                errorMessage: '请求已取消'
              })
            })
            auditCapture.completeAttempt(auditAttemptId, {
              statusCode: upstreamResponse.status,
              responseHeaders: upstreamResponse.headers,
              success: false,
              errorPhase: 'client',
              errorMessage: '请求已取消'
            })
          }
          throw error
        }
        if (responseBody) {
          usage = parseOpenAIUsageFromJsonBuffer(responseBody)
        } else if (upstreamResponse.ok) {
          usage = parseOpenAIUsageFromJsonTextFragment(responseUsageText)
        }
        if (!upstreamResponse.ok) {
          errorPayload = parseErrorPayload(responseBodyText ?? '', upstreamResponse.headers)
        }
        auditCapture.completeAttempt(auditAttemptId, {
          statusCode: upstreamResponse.status,
          responseHeaders: upstreamResponse.headers,
          responseBody,
          success: upstreamResponse.ok,
          errorPhase: upstreamResponse.ok ? undefined : 'upstream_response',
          errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
          errorMessage: typeof errorPayload.message === 'string' ? errorPayload.message : undefined
        })
      }

      if (upstreamResponse.ok) {
        applyAccountErrorHandlingWithCacheInvalidation(account, {
          success: true,
          settings: activeGatewaySettings
        })
        if (account.streamFailureCount > 0 || account.streamFailureWindowStartedAt || account.lastErrorMessage) {
          clearAccountStreamFailureStateWithCacheInvalidation(account.id)
        }
      }

      recordCompletedUpstreamAttempt(req, {
        traceId,
        clientIp,
        systemAccountId,
        apiKeyId,
        groupId,
        account,
        endpoint,
        stream: isEffectiveOpenAIStreamRequest(req, account),
        statusCode: upstreamResponse.status,
        success: upstreamResponse.ok,
        firstTokenMs,
        startedAt,
        usage,
        errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
        errorMessage: typeof errorPayload.message === 'string' ? errorPayload.message : undefined,
        requestSnapshot: upstreamResponse.ok ? undefined : requestSnapshot,
        responseSnapshot: upstreamResponse.ok
          ? undefined
          : buildUsageResponseSnapshot({
            upstreamUrl,
            statusCode: upstreamResponse.status,
            headers: upstreamResponse.headers,
            bodyText: responseBodyText
          })
      })
      auditCapture.finalize({
        outcome: upstreamResponse.ok ? 'success' : 'upstream_failed',
        success: upstreamResponse.ok,
        statusCode: upstreamResponse.status,
        responseHeaders: responseHeadersToObject(res),
        responseBody: responseBodyText,
        responsePartType: upstreamResponse.ok ? 'gateway_response' : 'gateway_error',
        errorPhase: upstreamResponse.ok ? undefined : 'upstream_response',
        errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
        errorMessage: typeof errorPayload.message === 'string' ? errorPayload.message : undefined,
        accountId: account.id,
        firstTokenMs
      })
    } finally {
      releaseConcurrency()
    }
  } catch (error) {
    if (isUpstreamRequestAbortedError(error) || abortController.signal.aborted) {
      auditCapture.finalize({
        outcome: 'client_aborted',
        success: false,
        statusCode: res.statusCode,
        responseHeaders: responseHeadersToObject(res),
        errorPhase: 'client',
        errorMessage: '请求已取消'
      })
      if (!res.writableEnded && !res.destroyed) {
        res.end()
      }
      return
    }
    if (error instanceof UpstreamRejectedRequestError) {
      const auditError = parseClientVisibleUpstreamErrorForAudit(error.response, error.message)
      sendRawUpstreamErrorResponse(res, error.response)
      auditCapture.finalize({
        outcome: 'upstream_failed',
        success: false,
        statusCode: error.response.statusCode,
        responseHeaders: responseHeadersToObject(res),
        responseBody: error.response.bodyText,
        responsePartType: 'gateway_error',
        errorPhase: 'upstream_response',
        errorCode: auditError.errorCode,
        errorMessage: auditError.errorMessage
      })
      return
    }
    if (error instanceof OpenAIOAuthCodexAdapterError) {
      const statusCode = error.statusCode
      const responsePayload = gatewayErrorPayload(error.message, error.type)
      sendGatewayErrorResponse(res, statusCode, responsePayload)
      auditCapture.finalize({
        outcome: 'gateway_failed',
        success: false,
        statusCode,
        responseHeaders: responseHeadersToObject(res),
        responseBody: JSON.stringify(responsePayload),
        responsePartType: 'gateway_error',
        errorPhase: 'request_validation',
        errorCode: error.code,
        errorMessage: error.message
      })
      return
    }
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : '没有可用的上游账户'
    const diagnosticError = options.exposeUpstreamDiagnostics
      ? buildDiagnosticUpstreamError(lastAttempt, message)
      : undefined
    const statusCode = diagnosticError?.statusCode ?? 503
    const responsePayload = diagnosticError?.payload ?? gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    if (!lastAttempt) {
      recordGatewayFailure(req, {
        traceId,
        clientIp,
        systemAccountId,
        apiKeyId,
        groupId,
        ...groupUsageFields,
        endpoint,
        requestSnapshot
      }, {
        statusCode,
        startedAt,
        responsePayload,
        errorMessage: message
      })
    }
    sendGatewayErrorResponse(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'upstream_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable',
      errorMessage: diagnosticError?.errorMessage ?? message
    })
  }
}

function sendQuotaExceededResponse(
  req: Request,
  res: Response,
  auditCapture: AuditCaptureContext,
  usageContext: GatewayUsageContext & Partial<ReturnType<typeof groupUsageMetadata>>,
  startedAt: number,
  message: string
): void {
  const statusCode = 429
  const responsePayload = gatewayErrorPayload(message, 'rate_limit_exceeded')
  recordGatewayFailure(req, usageContext, {
    statusCode,
    startedAt,
    responsePayload
  })
  sendGatewayErrorResponse(res, statusCode, responsePayload)
  auditCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_error',
    errorPhase: 'quota',
    errorCode: 'rate_limit_exceeded',
    errorMessage: responsePayload.error.message
  })
}

function sendGatewayErrorResponse(res: Response, statusCode: number, payload: ReturnType<typeof gatewayErrorPayload>): void {
  if (res.writableEnded || res.destroyed) {
    return
  }
  if (!res.headersSent) {
    sendGatewayJsonError(res, statusCode, payload)
    return
  }
  const contentType = String(res.getHeader('content-type') ?? '')
  if (isOpenAIStreamContentType(contentType)) {
    const failureEvent = writeGatewayStreamFailureEvent(res, payload.error.message)
    if (failureEvent) {
      res.write(failureEvent)
    }
  }
  res.end()
}

function flushResponseHeadersIfSupported(res: Response): void {
  const flushHeaders = (res as { flushHeaders?: unknown }).flushHeaders
  if (typeof flushHeaders === 'function') {
    flushHeaders.call(res)
  }
}

function setGatewayStreamResponseHeaders(res: Response): void {
  if (!res.hasHeader('cache-control')) {
    res.setHeader('cache-control', 'no-cache, no-transform')
  }
  res.setHeader('x-accel-buffering', 'no')
}

class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext,
  auditCapture: AuditCaptureContext,
  sessionAffinityKey?: string,
  signal?: AbortSignal
): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string; auditAttemptId: string; releaseConcurrency: () => void }> {
  const retryAttempts = Math.max(0, settings.temporaryUnschedulableRetryAttempts)
  let lastAttempt: UpstreamAttempt | undefined
  let auditAttemptIndex = 0
  const failedProxyDispatchKeys = new Map<string, string>()
  const deferredAccountFailures: DeferredAccountFailure[] = []

  for (const originalAccount of accounts) {
    throwIfRequestAborted(signal)
    const skippedProxyReason = failedProxyDispatchReason(failedProxyDispatchKeys, originalAccount)
    if (skippedProxyReason) {
      const message = `账户绑定的代理已在本次调度中失败，跳过重复尝试：${skippedProxyReason}`
      lastAttempt = { accountId: originalAccount.id, accountName: originalAccount.name, upstreamUrl: 'proxy:skipped', message }
      getRequestLogger().warn({
        event: 'gateway_proxy_duplicate_skipped',
        accountId: originalAccount.id,
        accountType: originalAccount.type,
        proxyProfileId: originalAccount.proxyProfileId,
        proxyConfigured: Boolean(originalAccount.proxyProfileId || originalAccount.proxyUrl)
      }, '跳过已失败代理绑定账号')
      continue
    }
    if (originalAccount.proxyProfileUnavailable) {
      const attemptStartedAt = Date.now()
      const message = originalAccount.proxyProfileErrorMessage ?? '账户绑定的代理不可用'
      lastAttempt = { accountId: originalAccount.id, accountName: originalAccount.name, upstreamUrl: 'proxy:configured', message }
      recordFailedUpstreamAttempt(req, usageContext, originalAccount, {
        upstreamUrl: 'proxy:configured',
        startedAt: attemptStartedAt,
        errorMessage: message
      })
      applyAccountErrorHandlingWithCacheInvalidation(originalAccount, { success: false, errorMessage: message, settings })
      rememberFailedProxyForDispatch(failedProxyDispatchKeys, originalAccount, message)
      continue
    }
    const account = await prepareUpstreamAccount(originalAccount, signal)
    const upstreamUrls = buildUpstreamUrlsForAccount(account, req)
    if (upstreamUrls.length === 0) {
      continue
    }
    let requestParts: Awaited<ReturnType<typeof buildUpstreamRequestParts>>
    try {
      requestParts = await buildUpstreamRequestParts(req, account, {
        systemAccountId: usageContext.systemAccountId,
        apiKeyId: usageContext.apiKeyId,
        groupId: usageContext.groupId
      }, signal)
    } catch (error) {
      if (error instanceof OpenAIOAuthCodexAdapterError) {
        lastAttempt = {
          accountId: account.id,
          accountName: account.name,
          upstreamUrl: 'openai-oauth-codex:local-validation',
          status: error.statusCode,
          message: error.message,
          responseBodyText: JSON.stringify({
            error: {
              message: error.message,
              type: error.type,
              code: error.code
            }
          })
        }
        recordFailedUpstreamAttempt(req, usageContext, account, {
          upstreamUrl: 'openai-oauth-codex:local-validation',
          startedAt: Date.now(),
          statusCode: error.statusCode,
          bodyText: lastAttempt.responseBodyText,
          errorMessage: error.message
        })
        throw error
      }
      throw error
    }
    const concurrencySlot = tryAcquireAccountConcurrency(account.id, account.concurrencyLimit)
    if (!concurrencySlot.acquired) {
      lastAttempt = {
        accountId: account.id,
        accountName: account.name,
        upstreamUrl: 'concurrency:limit',
        message: `账户并发已达到上限 ${concurrencySlot.current}/${concurrencySlot.limit}`
      }
      continue
    }
    const { headers, body } = requestParts
    let skipAccount = false
    let keepConcurrencySlot = false
    try {
      for (const upstreamUrl of upstreamUrls) {
        for (let attemptIndex = 0; attemptIndex <= retryAttempts; attemptIndex += 1) {
          const attemptStartedAt = Date.now()
          auditAttemptIndex += 1
          const auditAttemptId = auditCapture.startAttempt({
            account,
            attemptIndex: auditAttemptIndex,
            upstreamUrl,
            method: req.method,
            headers,
            body
          })
          try {
            const socketTimeoutMs = upstreamSocketTimeoutMs(req, settings, account)
            const requestTimeoutMs = upstreamRequestTimeoutMs(req, settings, account)
            getRequestLogger().info({
              event: 'gateway_upstream_request_started',
              accountId: account.id,
              accountType: account.type,
              accountStatus: account.status,
              upstreamUrl,
              attemptIndex,
              auditAttemptIndex,
              method: req.method,
              stream: isEffectiveOpenAIStreamRequest(req, account),
              requestBodyBytes: typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : body?.byteLength,
              socketTimeoutMs,
              requestTimeoutMs,
              proxyEnabled: Boolean(account.proxyUrl)
            }, '网关开始请求上游')
            const response = await requestUpstream(upstreamUrl, {
              method: req.method,
              headers,
              body,
              proxyUrl: account.proxyUrl,
              timeoutMs: socketTimeoutMs,
              requestTimeoutMs,
              signal
            })
            getRequestLogger().info({
              event: 'gateway_upstream_response_received',
              accountId: account.id,
              accountType: account.type,
              upstreamUrl,
              attemptIndex,
              auditAttemptIndex,
              statusCode: response.status,
              ok: response.ok,
              contentType: response.headers.get('content-type'),
              elapsedMs: Date.now() - attemptStartedAt,
              stream: isEffectiveOpenAIStreamRequest(req, account)
            }, '网关收到上游响应头')
            lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status }
            if (response.ok) {
              flushDeferredAccountFailures(deferredAccountFailures, sessionAffinityKey)
              rememberOpenAIAccountForSession(sessionAffinityKey, account.id, { systemAccountId: usageContext.systemAccountId, apiKeyId: usageContext.apiKeyId, groupId: usageContext.groupId })
              keepConcurrencySlot = true
              return { account, response, upstreamUrl, auditAttemptId, releaseConcurrency: concurrencySlot.release }
            }

            const failedResponseResult = await handleFailedUpstreamResponse({
              req,
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
              retryAttempts,
              sessionAffinityKey,
              signal,
              lastAttempt,
              deferredAccountFailures
            })
            lastAttempt = failedResponseResult.lastAttempt
            if (failedResponseResult.action === 'retry') {
              continue
            }
            skipAccount = true
            break
          } catch (error) {
            if (error instanceof UpstreamRejectedRequestError) {
              throw error
            }
            if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
              if (shouldRecordAbortedUpstreamAttempt(error)) {
                const statusCode = lastAttempt?.accountId === account.id && lastAttempt.upstreamUrl === upstreamUrl ? lastAttempt.status : undefined
                recordFailedUpstreamAttempt(req, usageContext, account, {
                  upstreamUrl,
                  startedAt: attemptStartedAt,
                  statusCode,
                  errorMessage: '请求已取消'
                })
                lastAttempt = {
                  accountId: account.id,
                  accountName: account.name,
                  upstreamUrl,
                  status: statusCode,
                  message: '请求已取消'
                }
              }
              auditCapture.completeAttempt(auditAttemptId, {
                success: false,
                errorPhase: 'client',
                errorMessage: '请求已取消'
              })
              throw error
            }
            const message = error instanceof Error ? error.message : '请求失败'
            getRequestLogger().warn(errorLogFields(error, {
              event: 'gateway_upstream_request_failed',
              accountId: account.id,
              accountType: account.type,
              upstreamUrl,
              attemptIndex,
              auditAttemptIndex,
              elapsedMs: Date.now() - attemptStartedAt,
              stream: isEffectiveOpenAIStreamRequest(req, account)
            }), '网关请求上游失败')
            lastAttempt = {
              accountId: account.id,
              accountName: account.name,
              upstreamUrl,
              message
            }
            auditCapture.completeAttempt(auditAttemptId, {
              success: false,
              errorPhase: 'upstream_request',
              errorMessage: message
            })
            recordFailedUpstreamAttempt(req, usageContext, account, {
              upstreamUrl,
              startedAt: attemptStartedAt,
              errorMessage: message
            })
            if (attemptIndex < retryAttempts) {
              await waitBeforeTemporaryUnschedulableRetry(settings)
              continue
            }
            forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
            applyAccountErrorHandlingWithCacheInvalidation(account, { success: false, errorMessage: message, settings })
            rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
            skipAccount = true
            break
          }
        }
        if (skipAccount) {
          break
        }
      }
    } finally {
      if (!keepConcurrencySlot) {
        concurrencySlot.release()
      }
    }
  }

  flushDeferredAccountFailures(deferredAccountFailures, sessionAffinityKey)
  throw new UpstreamAttemptError(
    lastAttempt
      ? '所有上游账户均失败；最后一次尝试 ' + lastAttempt.accountName + ' ' + lastAttempt.upstreamUrl + ' 返回 ' + (lastAttempt.message ?? lastAttempt.status)
      : '所有上游账户均失败',
    lastAttempt
  )
}

async function prepareUpstreamAccount(account: UpstreamAccount, signal?: AbortSignal): Promise<UpstreamAccount> {
  if (account.type !== 'oauth' || !shouldRefreshOpenAIOAuthCredentials(account.credentials) || !account.refreshToken) {
    return account
  }
  throwIfRequestAborted(signal)

  const updated = await refreshOpenAIOAuthAccountAccessToken(account, { signal, force: false, persistMode: 'db-service' })
  const credentials = updated.credentials
  const accessToken = typeof credentials.access_token === 'string' ? credentials.access_token : account.apiKey
  return {
    ...account,
    apiKey: accessToken,
    baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : account.baseUrl,
    refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : account.refreshToken,
    clientId: typeof credentials.client_id === 'string' ? credentials.client_id : account.clientId,
    expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : account.expiresAt,
    credentials
  }
}

function isImplicitOpenAIStreamResponse(req: Request, account: UpstreamAccount): boolean {
  return isEffectiveOpenAIStreamRequest(req, account)
}
