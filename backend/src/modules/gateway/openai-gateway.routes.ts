import { randomUUID } from 'node:crypto'
import { Router, type Request, type Response } from 'express'

import { tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import { bindRequestContextFields, getTraceId } from '../../shared/request-context.js'
import {
  recordAccountStreamFailure,
  updateAccount,
  type GatewayApiKeyRow,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { enqueueUsageRecord } from './usage-record-queue.service.js'
import {
  clearGatewayRuntimeCache,
  listCachedOpenAIAccountsForGroup,
  readCachedGatewaySettings,
  resolveCachedGroupUsageAccessMetadata
} from './gateway-runtime-cache.service.js'
import { estimateProviderCostUsd } from '../model-pricing/model-pricing.service.js'
import { buildOpenAIOAuthCredentials, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'
import {
  applyAccountErrorHandling,
  parseErrorPayload,
  type GatewaySettings
} from './account-error-policy.service.js'
import { persistOpenAICodexUsageHeaders } from './openai-codex-usage.service.js'
import {
  buildUsageRequestSnapshot,
  buildUsageResponseSnapshot,
  emptyUsage,
  extractClientIp,
  headersToObject,
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromSseText,
  requestEndpoint,
  requestModel,
  extractBearerToken,
  type UpstreamAttempt
} from './openai-gateway-usage.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  copyResponseHeaders,
  isStreamResponse,
  isRetryableUpstreamStreamPreloadError,
  preloadStreamResponseFirstChunk,
  requestUpstream,
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  type GatewayUpstreamResponse
} from './openai-gateway-upstream.js'
import {
  accountUsageMetadata,
  groupUsageMetadata,
  recordGatewayFailure,
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from './openai-gateway-usage-records.js'
import {
  gatewayErrorPayload,
  isOpenAIStreamContentType,
  sendGatewayJsonError
} from './openai-gateway-responses.js'
import { isOpenAIStreamRequest, resolveGatewayApiKey } from './openai-gateway-request.js'
import { pipeUpstreamStream } from './openai-gateway-stream.js'
import { createAuditCapture, responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import { API_KEY_QUOTA_EXCEEDED_MESSAGE, checkGatewayApiKeyQuota } from './api-key-quota.service.js'
import { AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE, checkGatewayAuthorizationQuota } from './authorization-quota.service.js'
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
}

openAIGatewayRouter.all('/*', async (req, res) => {
  await handleOpenAIGatewayRequest(req, res)
})

export async function handleOpenAIGatewayRequest(
  req: Request,
  res: Response,
  options: OpenAIGatewayHandleOptions = {}
): Promise<void> {
  const startedAt = Date.now()
  const traceId = getTraceId() ?? randomUUID()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const gatewaySettings = readCachedGatewaySettings()
  const auditCapture = createAuditCapture({ req, traceId, clientIp, startedAtMs: startedAt })
  req.once('aborted', () => auditCapture.markClientAborted())
  res.once('close', () => {
    if (!res.writableEnded) {
      auditCapture.markClientAborted()
    }
  })

  let apiKeyRecord: GatewayApiKeyRow | undefined
  const identity = options.identity
    ? options.identity
    : (() => {
      apiKeyRecord = resolveGatewayApiKey(req, res)
      if (!apiKeyRecord) {
        const authErrorMessage = extractBearerToken(req.header('authorization')) ? 'Invalid API key' : 'Missing bearer token'
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
      return {
        systemAccountId: apiKeyRecord.system_account_id,
        apiKeyId: apiKeyRecord.id,
        groupId: apiKeyRecord.group_id
      }
    })()
  if (!identity) {
    return
  }
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

  const groupAccess = resolveCachedGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    const statusCode = 403
    const responsePayload = gatewayErrorPayload('API key group authorization is unavailable', 'forbidden')
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
    sendGatewayJsonError(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'authorization',
      errorCode: 'forbidden',
      errorMessage: 'API key group authorization is unavailable'
    })
    return
  }

  const groupUsageFields = groupUsageMetadata(groupAccess)
  const quotaDecision = apiKeyRecord ? checkGatewayApiKeyQuota(apiKeyRecord) : { allowed: true }
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
    sendGatewayJsonError(res, statusCode, responsePayload)
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

  const groupAuthorizationQuotaDecision = checkGatewayAuthorizationQuota({ groupAccess })
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
  const candidateAccounts = orderOpenAIAccountsBySessionAffinity(
    options.candidateAccounts ?? listCachedOpenAIAccountsForGroup(groupId, systemAccountId),
    options.disableSessionAffinity ? undefined : sessionAffinityKey
  )
  let authorizationQuotaDeniedAccountCount = 0
  const accounts = candidateAccounts.filter((account) => {
    const decision = checkGatewayAuthorizationQuota({ groupAccess, account })
    if (!decision.allowed) {
      authorizationQuotaDeniedAccountCount += 1
      return false
    }
    return true
  })
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
    const responsePayload = gatewayErrorPayload('No available upstream account', 'service_unavailable')
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
    sendGatewayJsonError(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'gateway_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable',
      errorMessage: 'No available upstream account'
    })
    return
  }

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(req, accounts, gatewaySettings, {
      traceId,
      clientIp,
      systemAccountId,
      apiKeyId,
      groupId,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    }, auditCapture, options.disableSessionAffinity ? undefined : sessionAffinityKey)
    const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, releaseConcurrency } = upstreamResult

    try {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const shouldHandleAsStream = isOpenAIStreamContentType(contentType) || isImplicitOpenAIStreamResponse(req, account)
      res.status(upstreamResponse.status)
      copyResponseHeaders(upstreamResponse, res)
      if (shouldHandleAsStream && !res.hasHeader('content-type')) {
        res.setHeader('content-type', 'text/event-stream; charset=utf-8')
      }
      persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, 'gateway')

      let usage = emptyUsage()
      let firstTokenMs: number | undefined
      let responseBodyText: string | undefined
      let errorPayload: Record<string, unknown> = {}
      if (shouldHandleAsStream) {
        if (!upstreamResponse.body) {
          res.end()
          forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
          auditCapture.completeAttempt(auditAttemptId, {
            statusCode: upstreamResponse.status,
            responseHeaders: upstreamResponse.headers,
            success: false,
            errorPhase: 'upstream_response',
            errorMessage: 'Upstream response body is empty'
          })
          auditCapture.finalize({
            outcome: 'stream_failed',
            success: false,
            statusCode: upstreamResponse.status,
            responseHeaders: responseHeadersToObject(res),
            responsePartType: 'gateway_error',
            errorPhase: 'upstream_response',
            errorMessage: 'Upstream response body is empty',
            accountId: account.id
          })
          return
        }
        const streamResult = await pipeUpstreamStream(
          upstreamResponse.body,
          res,
          gatewaySettings,
          startedAt,
          (message) => handleStreamFailure(account, message, gatewaySettings)
        )
        firstTokenMs = streamResult.firstTokenMs
        responseBodyText = Buffer.concat(streamResult.chunks).toString('utf8')
        auditCapture.completeAttempt(auditAttemptId, {
          statusCode: upstreamResponse.status,
          responseHeaders: upstreamResponse.headers,
          responseBody: Buffer.concat(streamResult.upstreamChunks),
          success: streamResult.completed && upstreamResponse.ok,
          errorPhase: streamResult.completed ? undefined : 'stream',
          errorMessage: streamResult.completed ? undefined : streamResult.message
        })
        if (!streamResult.completed) {
          forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
          enqueueUsageRecord({
            traceId,
            clientIp,
            systemAccountId,
            apiKeyId,
            groupId,
            accountId: account.id,
            ...accountUsageMetadata(account),
            endpoint,
            providerCode: 'openai',
            model: requestModel(req),
            stream: isOpenAIStreamRequest(req),
            statusCode: upstreamResponse.status,
            success: false,
            firstTokenMs: streamResult.firstTokenMs,
            durationMs: Date.now() - startedAt,
            errorMessage: streamResult.message,
            requestSnapshot,
            responseSnapshot: buildUsageResponseSnapshot({
              upstreamUrl,
              statusCode: upstreamResponse.status,
              headers: upstreamResponse.headers,
              bodyText: responseBodyText,
              errorMessage: streamResult.message
            })
          })
          auditCapture.finalize({
            outcome: 'stream_failed',
            success: false,
            statusCode: upstreamResponse.status,
            responseHeaders: responseHeadersToObject(res),
            responseBody: Buffer.concat(streamResult.chunks),
            responsePartType: 'gateway_response',
            errorPhase: 'stream',
            errorMessage: streamResult.message,
            accountId: account.id,
            firstTokenMs: streamResult.firstTokenMs
          })
          return
        }
        usage = parseOpenAIUsageFromSseText(responseBodyText)
      } else {
        const responseBody = Buffer.from(await upstreamResponse.arrayBuffer())
        responseBodyText = responseBody.toString('utf8')
        firstTokenMs = Date.now() - startedAt
        usage = parseOpenAIUsageFromJsonBuffer(responseBody)
        if (!upstreamResponse.ok) {
          errorPayload = parseErrorPayload(responseBodyText, upstreamResponse.headers)
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
        res.send(responseBody)
      }

      if (upstreamResponse.ok) {
        applyAccountErrorHandlingWithCacheInvalidation(account, {
          success: true,
          settings: gatewaySettings,
          preserveManualTrafficMigration: true
        })
      }

      enqueueUsageRecord({
        traceId,
        clientIp,
        systemAccountId,
        apiKeyId,
        groupId,
        accountId: account.id,
        ...accountUsageMetadata(account),
        endpoint,
        providerCode: 'openai',
        model: requestModel(req),
        stream: isOpenAIStreamRequest(req),
        statusCode: upstreamResponse.status,
        success: upstreamResponse.ok,
        firstTokenMs,
        durationMs: Date.now() - startedAt,
        inputTokens: usage.inputTokens,
        outputTokens: usage.outputTokens,
        cacheReadTokens: usage.cacheReadTokens,
        costUsd: estimateProviderCostUsd({
          providerCode: 'openai',
          model: requestModel(req),
          inputTokens: usage.inputTokens,
          outputTokens: usage.outputTokens,
          cacheReadTokens: usage.cacheReadTokens
        }),
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
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : 'No available upstream account'
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('No available upstream account', 'service_unavailable')
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
    sendGatewayJsonError(res, statusCode, responsePayload)
    auditCapture.finalize({
      outcome: 'upstream_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(res),
      responseBody: JSON.stringify(responsePayload),
      responsePartType: 'gateway_error',
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable',
      errorMessage: message
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
  sendGatewayJsonError(res, statusCode, responsePayload)
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
  sessionAffinityKey?: string
): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string; auditAttemptId: string; releaseConcurrency: () => void }> {
  const retryAttempts = Math.max(0, settings.temporaryUnschedulableRetryAttempts)
  const isStreamRequest = isOpenAIStreamRequest(req)
  let lastAttempt: UpstreamAttempt | undefined
  let auditAttemptIndex = 0

  for (const originalAccount of accounts) {
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
      continue
    }
    const account = await prepareUpstreamAccount(originalAccount)
    const upstreamUrls = buildUpstreamUrlsForAccount(account, req)
    if (upstreamUrls.length === 0) {
      continue
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
    const headers = buildUpstreamHeaders(req.headers, account)
    const body = buildUpstreamRequestBody(req, account.passthroughEnabled)
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
            const response = await requestUpstream(upstreamUrl, {
              method: req.method,
              headers,
              body,
              proxyUrl: account.proxyUrl,
              timeoutMs: upstreamSocketTimeoutMs(req, settings),
              requestTimeoutMs: upstreamRequestTimeoutMs(req, settings)
            })
            lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status }
            if (response.ok) {
              if (isStreamRequest && settings.streamCircuitBreakerEnabled && isStreamResponse(response)) {
                try {
                  const preloadedResponse = await preloadStreamResponseFirstChunk(response, settings)
                  rememberOpenAIAccountForSession(sessionAffinityKey, account.id)
                  keepConcurrencySlot = true
                  return { account, response: preloadedResponse, upstreamUrl, auditAttemptId, releaseConcurrency: concurrencySlot.release }
                } catch (error) {
                  const message = error instanceof Error ? error.message : 'Upstream stream request interrupted before first chunk'
                  lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status, message }
                  const preloadFailureBody = streamPreloadErrorBody(error)
                  auditCapture.completeAttempt(auditAttemptId, {
                    statusCode: response.status,
                    responseHeaders: response.headers,
                    responseBody: preloadFailureBody,
                    success: false,
                    errorPhase: 'stream_preload',
                    errorMessage: message
                  })
                  forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
                  recordFailedUpstreamAttempt(req, usageContext, account, {
                    upstreamUrl,
                    startedAt: attemptStartedAt,
                    statusCode: response.status,
                    headers: response.headers,
                    bodyText: preloadFailureBody?.toString('utf8'),
                    errorMessage: message
                  })
                  if (isRetryableUpstreamStreamPreloadError(error) && attemptIndex < retryAttempts) {
                    await waitBeforeTemporaryUnschedulableRetry(settings)
                    continue
                  }
                  applyAccountErrorHandlingWithCacheInvalidation(account, { success: false, errorMessage: message, settings })
                  skipAccount = true
                  break
                }
              }
              rememberOpenAIAccountForSession(sessionAffinityKey, account.id)
              keepConcurrencySlot = true
              return { account, response, upstreamUrl, auditAttemptId, releaseConcurrency: concurrencySlot.release }
            }

            const responseBody = Buffer.from(await response.arrayBuffer())
            const responseBodyText = responseBody.toString('utf8')
            lastAttempt = {
              ...lastAttempt,
              responseHeaders: headersToObject(response.headers),
              responseBodyText
            }
            auditCapture.completeAttempt(auditAttemptId, {
              statusCode: response.status,
              responseHeaders: response.headers,
              responseBody,
              success: false,
              errorPhase: 'upstream_response',
              errorMessage: responseBodyText
            })
            recordFailedUpstreamAttempt(req, usageContext, account, {
              upstreamUrl,
              startedAt: attemptStartedAt,
              statusCode: response.status,
              headers: response.headers,
              bodyText: responseBodyText
            })
            persistOpenAICodexHeadersIfNeeded(account, response.headers, 'gateway_error')
            forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
            applyAccountErrorHandlingWithCacheInvalidation(account, {
              success: false,
              statusCode: response.status,
              headers: response.headers,
              bodyText: responseBodyText,
              settings
            })
            skipAccount = true
            break
          } catch (error) {
            const message = error instanceof Error ? error.message : 'request failed'
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

  throw new UpstreamAttemptError(
    lastAttempt
      ? 'All upstream accounts failed; last attempt ' + lastAttempt.accountName + ' ' + lastAttempt.upstreamUrl + ' returned ' + (lastAttempt.message ?? lastAttempt.status)
      : 'All upstream accounts failed',
    lastAttempt
  )
}

async function waitBeforeTemporaryUnschedulableRetry(settings: GatewaySettings): Promise<void> {
  const intervalMs = Math.max(0, settings.temporaryUnschedulableRetryIntervalSeconds) * 1000
  if (intervalMs <= 0) {
    return
  }
  await new Promise((resolve) => setTimeout(resolve, intervalMs))
}

function applyAccountErrorHandlingWithCacheInvalidation(
  account: UpstreamAccount,
  input: Parameters<typeof applyAccountErrorHandling>[1]
): ReturnType<typeof applyAccountErrorHandling> {
  const result = applyAccountErrorHandling(account, input)
  if (result.changed) {
    clearGatewayRuntimeCache()
  }
  return result
}

function handleStreamFailure(account: UpstreamAccount, reason: string, settings: GatewaySettings): void {
  if (!settings.streamCircuitBreakerEnabled) {
    return
  }

  const result = recordAccountStreamFailure({
    accountId: account.id,
    thresholdCount: settings.streamFailureThresholdCount,
    thresholdWindowMinutes: settings.streamFailureThresholdWindowMinutes,
    action: 'cooldown',
    cooldownMinutes: settings.defaultTemporaryUnschedulableMinutes,
    reason
  })

  if (result.triggered) {

    clearGatewayRuntimeCache()

  }
}

function streamPreloadErrorBody(error: unknown): Buffer | undefined {
  if (typeof error === 'object' && error !== null && 'chunks' in error) {
    const chunks = (error as { chunks?: unknown }).chunks
    if (Array.isArray(chunks) && chunks.every(Buffer.isBuffer)) {
      return Buffer.concat(chunks)
    }
  }
  return undefined
}

async function prepareUpstreamAccount(account: UpstreamAccount): Promise<UpstreamAccount> {
  if (account.type !== 'oauth' || !shouldRefreshOpenAIOAuthCredentials(account.credentials) || !account.refreshToken) {
    return account
  }

  const tokenInfo = await refreshOpenAIOAuthToken({
    refreshToken: account.refreshToken,
    clientId: account.clientId,
    proxyUrl: account.proxyUrl
  })
  const credentials = {
    ...account.credentials,
    ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken: account.refreshToken })
  }
  updateAccount(account.id, { credentials, status: 'active' })
  clearGatewayRuntimeCache()
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

function persistOpenAICodexHeadersIfNeeded(account: UpstreamAccount, headers: Headers, source: string): void {
  if (account.type !== 'oauth') return
  persistOpenAICodexUsageHeaders(account.id, headers, source)
}

function isImplicitOpenAIStreamResponse(req: Request, account: UpstreamAccount): boolean {
  return isOpenAIStreamRequest(req) && account.type === 'oauth'
}
