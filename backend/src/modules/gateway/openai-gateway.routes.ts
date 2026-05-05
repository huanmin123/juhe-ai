import { randomUUID } from 'node:crypto'
import { Router, type Request, type Response } from 'express'

import {
  recordAccountStreamFailure,
  updateAccount,
  validateGatewayApiKey,
  type GroupUsageAccessMetadata,
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
  buildGatewayErrorResponseSnapshot,
  buildUsageRequestSnapshot,
  buildUsageResponseSnapshot,
  emptyUsage,
  extractBearerToken,
  extractClientIp,
  headersToObject,
  inspectOpenAIStreamText,
  parseOpenAIUsageFromJsonBuffer,
  parseOpenAIUsageFromSseText,
  requestEndpoint,
  requestModel,
  type UpstreamAttempt,
  type UsageRequestSnapshot
} from './openai-gateway-usage.js'
import {
  buildUpstreamHeaders,
  buildUpstreamRequestBody,
  copyResponseHeaders,
  isStreamResponse,
  preloadStreamResponseFirstChunk,
  readStreamChunkWithIdleTimeout,
  requestUpstream,
  upstreamRequestTimeoutMs,
  upstreamSocketTimeoutMs,
  UpstreamRequestTimeoutError,
  type GatewayUpstreamResponse
} from './openai-gateway-upstream.js'

export const openAIGatewayRouter = Router()

openAIGatewayRouter.all('/*', async (req, res) => {
  const startedAt = Date.now()
  const requestId = randomUUID()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestSnapshot = buildUsageRequestSnapshot(req, requestId, clientIp)
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  const gatewaySettings = readCachedGatewaySettings()

  if (!gatewayApiKey) {
    res.status(401).json({ error: { message: 'Missing bearer token', type: 'invalid_request_error' } })
    return
  }

  const apiKeyRecord = validateGatewayApiKey(gatewayApiKey)
  if (!apiKeyRecord) {
    res.status(401).json({ error: { message: 'Invalid API key', type: 'invalid_request_error' } })
    return
  }

  const groupAccess = resolveCachedGroupUsageAccessMetadata(apiKeyRecord.group_id, apiKeyRecord.system_account_id)
  if (!groupAccess) {
    const statusCode = 403
    const responsePayload = { error: { message: 'API key group authorization is unavailable', type: 'forbidden' } }
    enqueueUsageRecord({
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      endpoint,
      providerCode: 'openai',
      model: requestModel(req),
      stream: req.body?.stream === true,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      errorMessage: responsePayload.error.message,
      requestSnapshot,
      responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, responsePayload)
    })
    res.status(statusCode).json(responsePayload)
    return
  }

  const groupUsageFields = groupUsageMetadata(groupAccess)
  const accounts = listCachedOpenAIAccountsForGroup(apiKeyRecord.group_id, apiKeyRecord.system_account_id)
  if (accounts.length === 0) {
    const statusCode = 503
    const responsePayload = { error: { message: 'No available upstream account', type: 'service_unavailable' } }
    enqueueUsageRecord({
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      ...groupUsageFields,
      endpoint,
      providerCode: 'openai',
      model: requestModel(req),
      stream: req.body?.stream === true,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      errorMessage: responsePayload.error.message,
      requestSnapshot,
      responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, responsePayload)
    })
    res.status(statusCode).json(responsePayload)
    return
  }

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(req, accounts, gatewaySettings, {
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      ...groupUsageFields,
      endpoint,
      requestSnapshot
    })
    const { account, response: upstreamResponse, upstreamUrl } = upstreamResult

    const contentType = upstreamResponse.headers.get('content-type') ?? ''
    res.status(upstreamResponse.status)
    copyResponseHeaders(upstreamResponse, res)
    persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, 'gateway')

    let usage = emptyUsage()
    let firstTokenMs: number | undefined
    let responseBodyText: string | undefined
    let errorPayload: Record<string, unknown> = {}
    if (contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')) {
      if (!upstreamResponse.body) {
        res.end()
        return
      }
      const streamResult = await pipeUpstreamStream(upstreamResponse.body, res, account, gatewaySettings, startedAt)
      firstTokenMs = streamResult.firstTokenMs
      responseBodyText = Buffer.concat(streamResult.chunks).toString('utf8')
      if (!streamResult.completed) {
        enqueueUsageRecord({
          requestId,
          clientIp,
          systemAccountId: apiKeyRecord.system_account_id,
          apiKeyId: apiKeyRecord.id,
          groupId: apiKeyRecord.group_id,
          accountId: account.id,
          ...accountUsageMetadata(account),
          endpoint,
          providerCode: 'openai',
          model: requestModel(req),
          stream: req.body?.stream === true,
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
      res.send(responseBody)
    }

    if (upstreamResponse.ok) {
      applyAccountErrorHandlingWithCacheInvalidation(account, { success: true, settings: gatewaySettings })
    }

    enqueueUsageRecord({
      requestId,
      clientIp,
      systemAccountId: apiKeyRecord.system_account_id,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      accountId: account.id,
      ...accountUsageMetadata(account),
      endpoint,
      providerCode: 'openai',
      model: requestModel(req),
      stream: req.body?.stream === true,
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
  } catch (error) {
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : 'No available upstream account'
    const statusCode = 503
    const responsePayload = { error: { message: 'No available upstream account', type: 'service_unavailable' } }
    if (!lastAttempt) {
      enqueueUsageRecord({
        requestId,
        clientIp,
        systemAccountId: apiKeyRecord.system_account_id,
        apiKeyId: apiKeyRecord.id,
        groupId: apiKeyRecord.group_id,
        ...groupUsageFields,
        endpoint,
        providerCode: 'openai',
        model: requestModel(req),
        stream: req.body?.stream === true,
        statusCode,
        success: false,
        durationMs: Date.now() - startedAt,
        errorMessage: message,
        requestSnapshot,
        responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, responsePayload)
      })
    }
    res.status(statusCode).json(responsePayload)
  }
})

type UpstreamAccount = OpenAIAccountSecret

type UsageAccessFields = Pick<OpenAIAccountSecret,
  'accountOwnerSystemAccountId'
  | 'groupOwnerSystemAccountId'
  | 'accountAccessType'
  | 'groupAccessType'
  | 'accountAuthorizationId'
  | 'groupAuthorizationId'
>

function accountUsageMetadata(account: UpstreamAccount): UsageAccessFields {
  return {
    accountOwnerSystemAccountId: account.accountOwnerSystemAccountId,
    groupOwnerSystemAccountId: account.groupOwnerSystemAccountId,
    accountAccessType: account.accountAccessType,
    groupAccessType: account.groupAccessType,
    accountAuthorizationId: account.accountAuthorizationId,
    groupAuthorizationId: account.groupAuthorizationId
  }
}

function groupUsageMetadata(groupAccess: GroupUsageAccessMetadata): Pick<UsageAccessFields, 'groupOwnerSystemAccountId' | 'groupAccessType' | 'groupAuthorizationId'> {
  return {
    groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
    groupAccessType: groupAccess.groupAccessType,
    groupAuthorizationId: groupAccess.groupAuthorizationId
  }
}

class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

interface GatewayUsageContext {
  requestId: string
  clientIp?: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
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

function recordFailedUpstreamAttempt(
  req: Request,
  usageContext: GatewayUsageContext,
  account: UpstreamAccount,
  input: {
    upstreamUrl: string
    startedAt: number
    statusCode?: number
    headers?: Headers | Record<string, string>
    bodyText?: string
    errorMessage?: string
  }
): void {
  const errorPayload = input.bodyText && input.headers instanceof Headers
    ? parseErrorPayload(input.bodyText, input.headers)
    : {}
  const errorMessage = input.errorMessage
    ?? (typeof errorPayload.message === 'string' ? errorPayload.message : undefined)
    ?? (typeof input.statusCode === 'number' ? `Upstream returned HTTP ${input.statusCode}` : 'Upstream request failed')

  enqueueUsageRecord({
    requestId: usageContext.requestId,
    clientIp: usageContext.clientIp,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    accountId: account.id,
    ...accountUsageMetadata(account),
    endpoint: usageContext.endpoint,
    providerCode: 'openai',
    model: requestModel(req),
    stream: req.body?.stream === true,
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage,
    requestSnapshot: usageContext.requestSnapshot,
    responseSnapshot: buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.statusCode,
      headers: input.headers,
      bodyText: input.bodyText,
      errorMessage
    })
  })
}

async function fetchFirstAvailableUpstream(
  req: Request,
  accounts: UpstreamAccount[],
  settings: GatewaySettings,
  usageContext: GatewayUsageContext
): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string }> {
  const retryAttempts = Math.max(0, settings.temporaryUnschedulableRetryAttempts)
  const isStreamRequest = req.body?.stream === true
  let lastAttempt: UpstreamAttempt | undefined

  for (const originalAccount of accounts) {
    const account = await prepareUpstreamAccount(originalAccount)
    const headers = buildUpstreamHeaders(req.headers, account)
    const body = buildUpstreamRequestBody(req, account.passthroughEnabled)
    let skipAccount = false
    for (const upstreamUrl of buildUpstreamUrls(account.baseUrl, req.originalUrl)) {
      for (let attemptIndex = 0; attemptIndex <= retryAttempts; attemptIndex += 1) {
        const attemptStartedAt = Date.now()
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
                return { account, response: preloadedResponse, upstreamUrl }
              } catch (error) {
                const message = error instanceof Error ? error.message : 'Upstream stream request interrupted before first chunk'
                lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status, message }
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
            return { account, response, upstreamUrl }
          }

          const responseBody = Buffer.from(await response.arrayBuffer())
          const responseBodyText = responseBody.toString('utf8')
          lastAttempt = {
            ...lastAttempt,
            responseHeaders: headersToObject(response.headers),
            responseBodyText
          }
          recordFailedUpstreamAttempt(req, usageContext, account, {
            upstreamUrl,
            startedAt: attemptStartedAt,
            statusCode: response.status,
            headers: response.headers,
            bodyText: responseBodyText
          })
          persistOpenAICodexHeadersIfNeeded(account, response.headers, 'gateway_error')
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
          recordFailedUpstreamAttempt(req, usageContext, account, {
            upstreamUrl,
            startedAt: attemptStartedAt,
            errorMessage: message
          })
          if (isStreamRequest && error instanceof UpstreamRequestTimeoutError) {
            handleStreamFailure(account, message, settings)
            skipAccount = true
            break
          }
          if (attemptIndex < retryAttempts) {
            await waitBeforeTemporaryUnschedulableRetry(settings)
            continue
          }
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

interface StreamPipeResult {
  completed: boolean
  chunks: Buffer[]
  message: string
  firstTokenMs?: number
}

async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  account: UpstreamAccount,
  settings: GatewaySettings,
  startedAt: number
): Promise<StreamPipeResult> {
  const chunks: Buffer[] = []
  const iterator = upstreamBody[Symbol.asyncIterator]()
  let completed = false
  let firstTokenMs: number | undefined

  try {
    while (true) {
      const result = settings.streamCircuitBreakerEnabled
        ? await readStreamChunkWithIdleTimeout(iterator, settings.streamIdleTimeoutSeconds)
        : await iterator.next()

      if (result.done) {
        completed = true
        break
      }

      const buffer = Buffer.from(result.value)
      if (firstTokenMs === undefined && buffer.length > 0) {
        firstTokenMs = Date.now() - startedAt
      }
      chunks.push(buffer)
      res.write(buffer)
    }
  } catch (error) {
    const rawMessage = error instanceof Error ? error.message : 'Upstream stream interrupted'
    const inspection = inspectOpenAIStreamText(Buffer.concat(chunks).toString('utf8'))
    if (inspection.terminalReceived && !inspection.failedReceived) {
      res.end()
      return { completed: true, chunks, message: 'completed', firstTokenMs }
    }
    const message = inspection.errorMessage ?? rawMessage
    handleStreamFailure(account, message, settings)
    if (!inspection.failedReceived) {
      const failureEvent = writeGatewayStreamFailureEvent(res, message)
      if (failureEvent) {
        chunks.push(failureEvent)
      }
    }
    res.end()
    return { completed: false, chunks, message, firstTokenMs }
  }

  const inspection = inspectOpenAIStreamText(Buffer.concat(chunks).toString('utf8'))
  if (!inspection.terminalReceived) {
    const message = 'Upstream stream ended before OpenAI terminal event'
    handleStreamFailure(account, message, settings)
    const failureEvent = writeGatewayStreamFailureEvent(res, message)
    if (failureEvent) {
      chunks.push(failureEvent)
    }
    res.end()
    return { completed: false, chunks, message, firstTokenMs }
  }

  res.end()

  if (!completed || inspection.failedReceived) {
    const message = inspection.errorMessage ?? 'Upstream stream failed'
    handleStreamFailure(account, message, settings)
    return { completed: false, chunks, message, firstTokenMs }
  }

  return { completed: true, chunks, message: 'completed', firstTokenMs }
}

function writeGatewayStreamFailureEvent(res: Response, message: string): Buffer | undefined {
  if (res.writableEnded || res.destroyed) {
    return undefined
  }

  const payload = {
    type: 'response.failed',
    response: {
      status: 'failed',
      error: {
        code: gatewayStreamFailureCode(message),
        message
      }
    }
  }
  const buffer = Buffer.from(`event: response.failed\ndata: ${JSON.stringify(payload)}\n\n`, 'utf8')
  try {
    res.write(buffer)
    return buffer
  } catch {
    return undefined
  }
}

function gatewayStreamFailureCode(message: string): string {
  return message.toLowerCase().includes('idle timeout')
    ? 'upstream_stream_idle_timeout'
    : 'upstream_stream_interrupted'
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
