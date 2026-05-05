import { randomUUID } from 'node:crypto'
import { Router, type Request, type Response } from 'express'

import { bindRequestContextFields, getTraceId } from '../../shared/request-context.js'
import {
  recordAccountStreamFailure,
  updateAccount,
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
  preloadStreamResponseFirstChunk,
  requestUpstream,
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  UpstreamRequestTimeoutError,
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
import {
  forgetOpenAIAccountForSession,
  orderOpenAIAccountsBySessionAffinity,
  rememberOpenAIAccountForSession,
  resolveOpenAIGatewaySessionAffinityKey
} from './openai-gateway-session-affinity.service.js'
import { listProviderModelPricing } from '../model-pricing/model-pricing.service.js'

export const openAIGatewayRouter = Router()

openAIGatewayRouter.all('/*', async (req, res) => {
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

  const apiKeyRecord = resolveGatewayApiKey(req, res)
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
    return
  }
  auditCapture.bindContext({
    systemAccountId: apiKeyRecord.system_account_id,
    apiKeyId: apiKeyRecord.id,
    groupId: apiKeyRecord.group_id,
    providerCode: 'openai'
  })
  bindRequestContextFields({
    systemAccountId: apiKeyRecord.system_account_id,
    apiKeyId: apiKeyRecord.id,
    groupId: apiKeyRecord.group_id
  })

  const groupAccess = resolveCachedGroupUsageAccessMetadata(apiKeyRecord.group_id, apiKeyRecord.system_account_id)
  if (!groupAccess) {
    const statusCode = 403
    const responsePayload = gatewayErrorPayload('API key group authorization is unavailable', 'forbidden')
    recordGatewayFailure(req, {
      traceId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
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
  if (isOpenAIModelsRequest(req)) {
    const responsePayload = buildOpenAIModelsResponse()
    res.status(200).json(responsePayload)
    enqueueUsageRecord({
      traceId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
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
    systemAccountId: apiKeyRecord.system_account_id,
    apiKeyId: apiKeyRecord.id,
    groupId: apiKeyRecord.group_id
  })
  const accounts = orderOpenAIAccountsBySessionAffinity(
    listCachedOpenAIAccountsForGroup(apiKeyRecord.group_id, apiKeyRecord.system_account_id),
    sessionAffinityKey
  )
  if (accounts.length === 0) {
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('No available upstream account', 'service_unavailable')
    recordGatewayFailure(req, {
      traceId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
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
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    }, auditCapture, sessionAffinityKey)
    const { account, response: upstreamResponse, upstreamUrl, auditAttemptId } = upstreamResult

    const contentType = upstreamResponse.headers.get('content-type') ?? ''
    res.status(upstreamResponse.status)
    copyResponseHeaders(upstreamResponse, res)
    persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, 'gateway')

    let usage = emptyUsage()
    let firstTokenMs: number | undefined
    let responseBodyText: string | undefined
    let errorPayload: Record<string, unknown> = {}
    if (isOpenAIStreamContentType(contentType)) {
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
          systemAccountId: apiKeyRecord.system_account_id,
          apiKeyId: apiKeyRecord.id,
          groupId: apiKeyRecord.group_id,
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
      applyAccountErrorHandlingWithCacheInvalidation(account, { success: true, settings: gatewaySettings })
    }

    enqueueUsageRecord({
      traceId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
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
  } catch (error) {
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : 'No available upstream account'
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('No available upstream account', 'service_unavailable')
    if (!lastAttempt) {
      recordGatewayFailure(req, {
        traceId,
        clientIp,
        systemAccountId: apiKeyRecord.system_account_id,
        apiKeyId: apiKeyRecord.id,
        groupId: apiKeyRecord.group_id,
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
})

type UpstreamAccount = OpenAIAccountSecret

class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

function buildUpstreamUrl(baseUrl: string, pathAndQuery: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/+$/, '')
  const requestPath = pathAndQuery.startsWith('/') ? pathAndQuery : `/${pathAndQuery}`
  const normalizedPath = normalizedBase.endsWith('/v1') ? requestPath.replace(/^\/v1/, '') || '/' : requestPath
  return `${normalizedBase}${normalizedPath}`
}

function buildUpstreamUrls(baseUrl: string, pathAndQuery: string): string[] {
  const primary = buildUpstreamUrl(baseUrl, pathAndQuery)
  const fallbackBase = baseUrl.trim().replace(/\/+$/, '')
  const fallback = fallbackBase.endsWith('/v1')
    ? `${fallbackBase}${(pathAndQuery.startsWith('/') ? pathAndQuery : `/${pathAndQuery}`).replace(/^\/v1/, '') || '/'}`
    : `${fallbackBase}${pathAndQuery.startsWith('/v1') ? pathAndQuery.replace(/^\/v1/, '') || '/' : pathAndQuery}`
  return [...new Set([primary, fallback])]
}

function buildUpstreamUrlsForAccount(account: UpstreamAccount, req: Request): string[] {
  if (account.type === 'oauth') {
    return buildOpenAICodexUpstreamUrls(req)
  }
  return buildUpstreamUrls(account.baseUrl, req.originalUrl)
}

function buildOpenAICodexUpstreamUrls(req: Request): string[] {
  if (req.method.toUpperCase() !== 'POST') {
    return []
  }
  const { path, query } = splitPathAndQuery(req.originalUrl)
  const normalizedPath = path.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (!openAICodexSupportedPaths.has(normalizedPath)) {
    return []
  }
  return [`${openAICodexBaseUrl}${normalizedPath}${query}`]
}

function splitPathAndQuery(pathAndQuery: string): { path: string; query: string } {
  const queryIndex = pathAndQuery.indexOf('?')
  if (queryIndex < 0) {
    return { path: pathAndQuery, query: '' }
  }
  return {
    path: pathAndQuery.slice(0, queryIndex),
    query: pathAndQuery.slice(queryIndex)
  }
}

function isOpenAIModelsRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'GET') {
    return false
  }
  const { path } = splitPathAndQuery(req.originalUrl)
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/models'
}

function buildOpenAIModelsResponse(): { object: 'list'; data: Array<{ id: string; object: 'model'; created: number; owned_by: string }> } {
  return {
    object: 'list',
    data: listProviderModelPricing('openai').map((item) => ({
      id: item.model,
      object: 'model',
      created: item.releaseDate ? Math.trunc(Date.parse(`${item.releaseDate}T00:00:00.000Z`) / 1000) : 0,
      owned_by: 'openai'
    }))
  }
}

async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext,
  auditCapture: AuditCaptureContext,
  sessionAffinityKey?: string
): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string; auditAttemptId: string }> {
  const retryAttempts = Math.max(0, settings.temporaryUnschedulableRetryAttempts)
  const isStreamRequest = isOpenAIStreamRequest(req)
  let lastAttempt: UpstreamAttempt | undefined
  let auditAttemptIndex = 0

  for (const originalAccount of accounts) {
    const account = await prepareUpstreamAccount(originalAccount)
    const upstreamUrls = buildUpstreamUrlsForAccount(account, req)
    if (upstreamUrls.length === 0) {
      continue
    }
    const headers = buildUpstreamHeaders(req.headers, account)
    const body = buildUpstreamRequestBody(req, account.passthroughEnabled)
    let skipAccount = false
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
                return { account, response: preloadedResponse, upstreamUrl, auditAttemptId }
              } catch (error) {
                const message = error instanceof Error ? error.message : 'Upstream stream request interrupted before first chunk'
                lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status, message }
                auditCapture.completeAttempt(auditAttemptId, {
                  statusCode: response.status,
                  responseHeaders: response.headers,
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
                  errorMessage: message
                })
                handleStreamFailure(account, message, settings)
                skipAccount = true
                break
              }
            }
            rememberOpenAIAccountForSession(sessionAffinityKey, account.id)
            return { account, response, upstreamUrl, auditAttemptId }
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
          if (isStreamRequest && error instanceof UpstreamRequestTimeoutError) {
            handleStreamFailure(account, message, settings)
            forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
            skipAccount = true
            break
          }
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

const openAICodexBaseUrl = 'https://chatgpt.com/backend-api/codex'
const openAICodexSupportedPaths = new Set(['/responses', '/responses/compact'])
