import { randomUUID } from 'node:crypto'
import * as http from 'node:http'
import * as https from 'node:https'
import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import { Router, type Request, type Response } from 'express'

import {
  clearAccountFailureState,
  createUsageRecord,
  getSettings,
  listErrorPolicies,
  listOpenAIAccountsForGroup,
  markAccountDisabledByFailure,
  markAccountCooldown,
  recordAccountStreamFailure,
  updateAccount,
  validateGatewayApiKey,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { estimateProviderCostUsd } from '../model-pricing/model-pricing.service.js'
import { buildOpenAIOAuthCredentials, createProxyAgent, refreshOpenAIOAuthToken, shouldRefreshOpenAIOAuthCredentials } from '../openai-oauth/openai-oauth.service.js'

export const openAIGatewayRouter = Router()

openAIGatewayRouter.all('/*', async (req, res) => {
  const startedAt = Date.now()
  const requestId = randomUUID()
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  const gatewaySettings = readGatewaySettings()

  if (!gatewayApiKey) {
    res.status(401).json({ error: { message: 'Missing bearer token', type: 'invalid_request_error' } })
    return
  }

  const apiKeyRecord = validateGatewayApiKey(gatewayApiKey)
  if (!apiKeyRecord) {
    res.status(401).json({ error: { message: 'Invalid API key', type: 'invalid_request_error' } })
    return
  }

  const accounts = listOpenAIAccountsForGroup(apiKeyRecord.group_id)
  if (accounts.length === 0) {
    res.status(503).json({ error: { message: 'No available OpenAI account in bound group', type: 'service_unavailable' } })
    return
  }

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(req, accounts, gatewaySettings)
    const { account, response: upstreamResponse } = upstreamResult

    const contentType = upstreamResponse.headers.get('content-type') ?? ''
    res.status(upstreamResponse.status)
    copyResponseHeaders(upstreamResponse, res)

    let usage = emptyUsage()
    if (contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')) {
      if (!upstreamResponse.body) {
        res.end()
        return
      }
      const streamResult = await pipeUpstreamStream(upstreamResponse.body, res, account, gatewaySettings)
      if (!streamResult.completed) {
        createUsageRecord({
          requestId,
          apiKeyId: apiKeyRecord.id,
          groupId: apiKeyRecord.group_id,
          accountId: account.id,
          providerCode: 'openai',
          model: typeof req.body?.model === 'string' ? req.body.model : undefined,
          stream: req.body?.stream === true,
          statusCode: upstreamResponse.status,
          success: false,
          durationMs: Date.now() - startedAt,
          errorMessage: streamResult.message
        })
        return
      }
      usage = parseOpenAIUsageFromSseText(Buffer.concat(streamResult.chunks).toString('utf8'))
    } else {
      const responseBody = Buffer.from(await upstreamResponse.arrayBuffer())
      usage = parseOpenAIUsageFromJsonBuffer(responseBody)
      res.send(responseBody)
    }

    if (upstreamResponse.ok) {
      clearAccountFailureState(account.id)
    }

    createUsageRecord({
      requestId,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      accountId: account.id,
      providerCode: 'openai',
      model: typeof req.body?.model === 'string' ? req.body.model : undefined,
      stream: req.body?.stream === true,
      statusCode: upstreamResponse.status,
      success: upstreamResponse.ok,
      durationMs: Date.now() - startedAt,
      inputTokens: usage.inputTokens,
      outputTokens: usage.outputTokens,
      cacheReadTokens: usage.cacheReadTokens,
      costUsd: estimateProviderCostUsd({
        providerCode: 'openai',
        model: typeof req.body?.model === 'string' ? req.body.model : undefined,
        inputTokens: usage.inputTokens,
        outputTokens: usage.outputTokens,
        cacheReadTokens: usage.cacheReadTokens
      })
    })
  } catch (error) {
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : 'Upstream request failed'
    const statusCode = 502
    createUsageRecord({
      requestId,
      apiKeyId: apiKeyRecord.id,
      groupId: apiKeyRecord.group_id,
      accountId: lastAttempt?.accountId,
      providerCode: 'openai',
      model: typeof req.body?.model === 'string' ? req.body.model : undefined,
      stream: req.body?.stream === true,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      errorMessage: message
    })
    res.status(statusCode).json({ error: { message, type: 'upstream_error' } })
  }
})

type UpstreamAccount = OpenAIAccountSecret

interface GatewaySettings {
  defaultErrorPolicyId: string
  defaultTemporaryUnschedulableMinutes: number
  streamCircuitBreakerEnabled: boolean
  streamIdleTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
}

interface UpstreamAttempt {
  accountId: string
  accountName: string
  upstreamUrl: string
  status?: number
  message?: string
}

class UpstreamAttemptError extends Error {
  constructor(message: string, readonly lastAttempt?: UpstreamAttempt) {
    super(message)
  }
}

function readGatewaySettings(): GatewaySettings {
  const settings = getSettings()
  return {
    defaultErrorPolicyId: stringSetting(settings.defaultErrorPolicyId, 'ep_default_passthrough'),
    defaultTemporaryUnschedulableMinutes: numberSetting(settings.defaultTemporaryUnschedulableMinutes, 5, 1, 1440),
    streamCircuitBreakerEnabled: booleanSetting(settings.streamCircuitBreakerEnabled, true),
    streamIdleTimeoutSeconds: numberSetting(settings.streamIdleTimeoutSeconds, 180, 10, 3600),
    streamFailureThresholdCount: numberSetting(settings.streamFailureThresholdCount, 3, 1, 100),
    streamFailureThresholdWindowMinutes: numberSetting(settings.streamFailureThresholdWindowMinutes, 10, 1, 1440)
  }
}

function booleanSetting(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function stringSetting(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function numberSetting(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}


function extractBearerToken(authorization?: string): string | undefined {
  if (!authorization) return undefined
  const match = authorization.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim()
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

async function fetchFirstAvailableUpstream(req: Request, accounts: UpstreamAccount[], settings: GatewaySettings): Promise<{ account: UpstreamAccount; response: GatewayUpstreamResponse; upstreamUrl: string }> {
  const body = req.method === 'GET' || req.method === 'HEAD' ? undefined : JSON.stringify(req.body ?? {})
  let lastAttempt: UpstreamAttempt | undefined

  for (const originalAccount of accounts) {
    const account = await prepareUpstreamAccount(originalAccount)
    const headers = buildUpstreamHeaders(req.headers, account.apiKey)
    for (const upstreamUrl of buildUpstreamUrls(account.baseUrl, req.originalUrl)) {
      try {
        const response = await requestUpstream(upstreamUrl, {
          method: req.method,
          headers,
          body,
          proxyUrl: account.proxyUrl,
          timeoutMs: upstreamSocketTimeoutMs(req, settings)
        })
        lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl, status: response.status }
        if (response.ok) {
          return { account, response, upstreamUrl }
        }

        const responseBody = Buffer.from(await response.arrayBuffer())
        const bufferedResponse = new BufferedGatewayUpstreamResponse(response.status, response.headers, responseBody)
        const policyDecision = decideAccountErrorPolicy(account, response.status, response.headers, responseBody, settings)
        if (policyDecision?.action === 'passthrough') {
          return { account, response: bufferedResponse, upstreamUrl }
        }
        if (policyDecision?.action === 'custom_error') {
          return { account, response: buildCustomErrorResponse(policyDecision), upstreamUrl }
        }
        const shouldTryNextByPolicy = policyDecision?.action === 'retry_next' || policyDecision?.action === 'cooldown' || policyDecision?.action === 'disable'
        if (!shouldTryNextByPolicy && !shouldTryNextUpstream(response.status)) {
          return { account, response: bufferedResponse, upstreamUrl }
        }
        if (policyDecision) {
          applyAccountErrorPolicySideEffect(account, response.status, policyDecision, settings)
        } else {
          markDefaultTemporaryUnschedulable(account, settings, 'Unhandled upstream status ' + response.status)
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : 'request failed'
        lastAttempt = {
          accountId: account.id,
          accountName: account.name,
          upstreamUrl,
          message
        }
        markDefaultTemporaryUnschedulable(account, settings, 'Unhandled upstream exception: ' + message)
      }
    }
  }

  throw new UpstreamAttemptError(
    lastAttempt
      ? 'All upstream accounts failed; last attempt ' + lastAttempt.accountName + ' ' + lastAttempt.upstreamUrl + ' returned ' + (lastAttempt.status ?? lastAttempt.message)
      : 'All upstream accounts failed',
    lastAttempt
  )
}

interface GatewayUpstreamResponse {
  readonly status: number
  readonly ok: boolean
  readonly headers: Headers
  readonly body: AsyncIterable<Uint8Array> | null
  arrayBuffer(): Promise<ArrayBuffer>
}

interface UpstreamRequestOptions {
  method: string
  headers: Headers
  body?: string
  proxyUrl?: string
  timeoutMs?: number
}

class NodeGatewayUpstreamResponse implements GatewayUpstreamResponse {
  constructor(private readonly message: IncomingMessage) {}

  get status(): number {
    return this.message.statusCode ?? 0
  }

  get ok(): boolean {
    return this.status >= 200 && this.status < 300
  }

  get headers(): Headers {
    return headersFromIncoming(this.message.headers)
  }

  get body(): AsyncIterable<Uint8Array> | null {
    return this.message as AsyncIterable<Uint8Array>
  }

  async arrayBuffer(): Promise<ArrayBuffer> {
    const buffer = await collectIncomingBody(this.message)
    const arrayBuffer = new ArrayBuffer(buffer.byteLength)
    new Uint8Array(arrayBuffer).set(buffer)
    return arrayBuffer
  }
}

class BufferedGatewayUpstreamResponse implements GatewayUpstreamResponse {
  constructor(readonly status: number, readonly headers: Headers, private readonly buffer: Buffer) {}

  get ok(): boolean {
    return this.status >= 200 && this.status < 300
  }

  get body(): AsyncIterable<Uint8Array> | null {
    return iterateBuffer(this.buffer)
  }

  async arrayBuffer(): Promise<ArrayBuffer> {
    const arrayBuffer = new ArrayBuffer(this.buffer.byteLength)
    new Uint8Array(arrayBuffer).set(this.buffer)
    return arrayBuffer
  }
}

async function* iterateBuffer(buffer: Buffer): AsyncIterable<Uint8Array> {
  if (buffer.byteLength > 0) {
    yield buffer
  }
}

function requestUpstream(upstreamUrl: string, options: UpstreamRequestOptions): Promise<GatewayUpstreamResponse> {
  return new Promise((resolve, reject) => {
    const url = new URL(upstreamUrl)
    const transport = url.protocol === 'http:' ? http : https
    const requestOptions: http.RequestOptions = {
      method: options.method,
      headers: headersToNodeHeaders(options.headers),
      agent: options.proxyUrl ? createProxyAgent(options.proxyUrl) as http.Agent : undefined
    }
    const request = transport.request(url, requestOptions, (message) => {
      resolve(new NodeGatewayUpstreamResponse(message))
    })
    const abort = () => request.destroy(new Error('Upstream request timed out'))

    request.setTimeout(options.timeoutMs ?? 120000, abort)
    request.on('error', reject)
    if (options.body) {
      request.write(options.body)
    }
    request.end()
  })
}

function headersToNodeHeaders(headers: Headers): http.OutgoingHttpHeaders {
  const output: http.OutgoingHttpHeaders = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}

function headersFromIncoming(headers: IncomingHttpHeaders): Headers {
  const output = new Headers()
  for (const [name, value] of Object.entries(headers)) {
    if (value === undefined) continue
    if (Array.isArray(value)) {
      for (const item of value) output.append(name, item)
    } else {
      output.set(name, value)
    }
  }
  return output
}

async function collectIncomingBody(message: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = []
  for await (const chunk of message) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  return Buffer.concat(chunks)
}

interface StreamPipeResult {
  completed: boolean
  chunks: Buffer[]
  message: string
}

async function pipeUpstreamStream(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  account: UpstreamAccount,
  settings: GatewaySettings
): Promise<StreamPipeResult> {
  const chunks: Buffer[] = []
  const iterator = upstreamBody[Symbol.asyncIterator]()
  let completed = false

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
      chunks.push(buffer)
      res.write(buffer)
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Upstream stream interrupted'
    handleStreamFailure(account, message, settings)
    res.write(formatSseError(message))
    res.end()
    return { completed: false, chunks, message }
  }

  res.end()

  if (!completed) {
    const message = 'Upstream stream interrupted before completion'
    handleStreamFailure(account, message, settings)
    return { completed: false, chunks, message }
  }

  return { completed: true, chunks, message: 'completed' }
}

async function readStreamChunkWithIdleTimeout(
  iterator: AsyncIterator<Uint8Array>,
  timeoutSeconds: number
): Promise<IteratorResult<Uint8Array>> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      iterator.next(),
      new Promise<IteratorResult<Uint8Array>>((_, reject) => {
        timeout = setTimeout(() => reject(new Error(`Upstream stream idle timeout after ${timeoutSeconds}s`)), timeoutSeconds * 1000)
      })
    ])
  } finally {
    if (timeout) {
      clearTimeout(timeout)
    }
  }
}

interface AccountErrorPolicyDecision {
  action: 'passthrough' | 'custom_error' | 'retry_next' | 'cooldown' | 'disable'
  ruleName?: string
  statusCode?: number
  message?: string
  cooldownMinutes?: number
}

function decideAccountErrorPolicy(
  account: UpstreamAccount,
  statusCode: number,
  headers: Headers,
  body: Buffer,
  settings: GatewaySettings
): AccountErrorPolicyDecision | undefined {
  const policyId = account.errorPolicyId ?? settings.defaultErrorPolicyId
  const policy = listErrorPolicies().find((item) => item.enabled && item.id === policyId)
  if (!policy) {
    return undefined
  }

  const bodyText = body.toString('utf8')
  const errorPayload = parseErrorPayload(bodyText, headers)
  const rules = [...policy.rules]
    .filter((rule) => rule.enabled !== false)
    .sort((left, right) => numericRuleValue(left.priority, Number.MAX_SAFE_INTEGER) - numericRuleValue(right.priority, Number.MAX_SAFE_INTEGER))

  for (const rule of rules) {
    if (!matchesErrorPolicyRule(rule, statusCode, bodyText, errorPayload)) {
      continue
    }
    const action = normalizePolicyAction(rule.action)
    const ruleName = typeof rule.name === 'string' ? rule.name : policy.name
    if (action === 'custom_error') {
      return {
        action,
        ruleName,
        statusCode: boundedStatusCode(rule.statusCode ?? rule.status_code, 502),
        message: stringRuleValue(rule.message ?? rule.errorMessage ?? rule.error_message, 'Upstream request failed')
      }
    }
    if (action === 'cooldown') {
      return {
        action,
        ruleName,
        cooldownMinutes: numericRuleValue(rule.durationMinutes ?? rule.duration_minutes, settings.defaultTemporaryUnschedulableMinutes)
      }
    }
    return { action, ruleName }
  }

  return undefined
}

function matchesErrorPolicyRule(rule: Record<string, unknown>, statusCode: number, bodyText: string, errorPayload: Record<string, unknown>): boolean {
  const match = typeof rule.match === 'object' && rule.match !== null && !Array.isArray(rule.match)
    ? rule.match as Record<string, unknown>
    : {}
  const statusSpec = rule.statusCode ?? rule.status_code ?? rule.statusCodes ?? rule.status_codes ?? match.statusCode ?? match.status_code ?? match.statusCodes ?? match.status_codes
  const keywordSpec = rule.keywords ?? match.keywords
  const codeSpec = rule.errorCode ?? rule.error_code ?? rule.errorCodes ?? rule.error_codes ?? match.errorCode ?? match.error_code ?? match.errorCodes ?? match.error_codes
  const typeSpec = rule.errorType ?? rule.error_type ?? rule.errorTypes ?? rule.error_types ?? match.errorType ?? match.error_type ?? match.errorTypes ?? match.error_types

  if (statusSpec !== undefined && !matchesStatusCode(statusCode, statusSpec)) return false
  if (keywordSpec !== undefined && !matchesTextList(bodyText, keywordSpec)) return false
  if (codeSpec !== undefined && !matchesValueList(errorPayload.code, codeSpec)) return false
  if (typeSpec !== undefined && !matchesValueList(errorPayload.type, typeSpec)) return false
  return true
}

function matchesStatusCode(statusCode: number, spec: unknown): boolean {
  const items = listRuleValues(spec)
  if (!items.length) return true
  return items.some((item) => {
    const token = item.toLowerCase()
    if (token === '*' || token === 'all') return true
    const range = token.match(/^(\d{3})\s*-\s*(\d{3})$/)
    if (range) return statusCode >= Number(range[1]) && statusCode <= Number(range[2])
    const family = token.match(/^([1-5])xx$/)
    if (family) return Math.floor(statusCode / 100) === Number(family[1])
    return Number(token) === statusCode
  })
}

function matchesTextList(text: string, spec: unknown): boolean {
  const items = listRuleValues(spec)
  if (!items.length) return true
  const normalized = text.toLowerCase()
  return items.some((item) => normalized.includes(item.toLowerCase()))
}

function matchesValueList(value: unknown, spec: unknown): boolean {
  const items = listRuleValues(spec)
  if (!items.length) return true
  const normalized = String(value ?? '').toLowerCase()
  return Boolean(normalized) && items.some((item) => normalized === item.toLowerCase())
}

function listRuleValues(spec: unknown): string[] {
  if (Array.isArray(spec)) {
    return spec.flatMap((item) => listRuleValues(item))
  }
  if (typeof spec === 'number') {
    return [String(spec)]
  }
  if (typeof spec !== 'string') {
    return []
  }
  return spec
    .split(/[,;，；\n\/]+/)
    .map((item) => item.trim())
    .filter(Boolean)
}

function parseErrorPayload(text: string, headers: Headers): Record<string, unknown> {
  if (!headers.get('content-type')?.includes('json')) return {}
  try {
    const payload = JSON.parse(text) as Record<string, unknown>
    const error = typeof payload.error === 'object' && payload.error !== null ? payload.error as Record<string, unknown> : payload
    return {
      code: error.code,
      type: error.type,
      message: error.message
    }
  } catch {
    return {}
  }
}

function normalizePolicyAction(value: unknown): AccountErrorPolicyDecision['action'] {
  if (value === 'custom_error' || value === 'customError' || value === 'custom_response') return 'custom_error'
  if (value === 'retry_next' || value === 'switch_account' || value === 'next') return 'retry_next'
  if (value === 'temp_unschedulable' || value === 'overloaded' || value === 'rate_limited') return 'cooldown'
  if (value === 'error_disabled' || value === 'disable') return 'disable'
  return 'passthrough'
}

function applyAccountErrorPolicySideEffect(account: UpstreamAccount, statusCode: number, decision: AccountErrorPolicyDecision, settings: GatewaySettings): void {
  const reason = decision.ruleName
    ? 'Error policy matched: ' + decision.ruleName + ' (HTTP ' + statusCode + ')'
    : 'Error policy matched HTTP ' + statusCode
  if (decision.action === 'cooldown') {
    const minutes = Math.max(1, decision.cooldownMinutes ?? settings.defaultTemporaryUnschedulableMinutes)
    const until = new Date(Date.now() + minutes * 60_000).toISOString()
    markAccountCooldown(account.id, until, reason)
  }
  if (decision.action === 'disable') {
    markAccountDisabledByFailure(account.id, reason)
  }
}

function buildCustomErrorResponse(decision: AccountErrorPolicyDecision): GatewayUpstreamResponse {
  const status = boundedStatusCode(decision.statusCode, 502)
  const payload = Buffer.from(JSON.stringify({ error: { message: decision.message ?? 'Upstream request failed', type: 'upstream_error' } }))
  const headers = new Headers()
  headers.set('content-type', 'application/json; charset=utf-8')
  return new BufferedGatewayUpstreamResponse(status, headers, payload)
}

function boundedStatusCode(value: unknown, fallback: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isInteger(number) && number >= 400 && number <= 599 ? number : fallback
}

function numericRuleValue(value: unknown, fallback: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : fallback
}

function stringRuleValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function markDefaultTemporaryUnschedulable(account: UpstreamAccount, settings: GatewaySettings, reason: string): void {
  const minutes = Math.max(1, settings.defaultTemporaryUnschedulableMinutes)
  const until = new Date(Date.now() + minutes * 60_000).toISOString()
  markAccountCooldown(account.id, until, reason)
}

function handleStreamFailure(account: UpstreamAccount, reason: string, settings: GatewaySettings): void {
  if (!settings.streamCircuitBreakerEnabled) {
    return
  }

  recordAccountStreamFailure({
    accountId: account.id,
    thresholdCount: settings.streamFailureThresholdCount,
    thresholdWindowMinutes: settings.streamFailureThresholdWindowMinutes,
    action: 'cooldown',
    cooldownMinutes: settings.defaultTemporaryUnschedulableMinutes,
    reason
  })
}

function formatSseError(message: string): string {
  return `event: error\ndata: ${JSON.stringify({ error: { message, type: 'upstream_stream_error' } })}\n\n`
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

function shouldTryNextUpstream(status: number): boolean {
  return status === 402 || status === 403 || status === 404 || status === 429 || status === 500 || status === 502 || status === 503 || status === 504
}

function upstreamSocketTimeoutMs(req: Request, settings: GatewaySettings): number {
  const isStreamRequest = req.body?.stream === true
  if (!isStreamRequest || !settings.streamCircuitBreakerEnabled) {
    return 120000
  }
  return Math.max(settings.streamIdleTimeoutSeconds + 15, 30) * 1000
}

const skippedUpstreamRequestHeaders = new Set([
  'host',
  'authorization',
  'content-length',
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
  'expect',
  'content-encoding',
  'accept-encoding'
])

function buildUpstreamHeaders(inputHeaders: Record<string, string | string[] | undefined>, apiKey: string): Headers {
  const headers = new Headers()
  for (const [name, value] of Object.entries(inputHeaders)) {
    const lowerName = name.toLowerCase()
    if (skippedUpstreamRequestHeaders.has(lowerName)) {
      continue
    }
    if (Array.isArray(value)) {
      headers.set(name, value.join(', '))
    } else if (typeof value === 'string') {
      headers.set(name, value)
    }
  }
  headers.set('authorization', `Bearer ${apiKey}`)
  headers.set('content-type', headers.get('content-type') ?? 'application/json')
  return headers
}

function copyResponseHeaders(upstreamResponse: GatewayUpstreamResponse, res: { setHeader: (name: string, value: string) => void }): void {
  upstreamResponse.headers.forEach((value, name) => {
    const lowerName = name.toLowerCase()
    if (['content-length', 'content-encoding', 'connection', 'transfer-encoding'].includes(lowerName)) {
      return
    }
    res.setHeader(name, value)
  })
}

interface ParsedUsage {
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
}

function emptyUsage(): ParsedUsage {
  return {}
}

function parseOpenAIUsageFromJsonBuffer(responseBody: Buffer): ParsedUsage {
  if (responseBody.length === 0) return emptyUsage()
  try {
    const payload = JSON.parse(responseBody.toString('utf8')) as Record<string, unknown>
    return extractUsage(payload.usage)
  } catch {
    return emptyUsage()
  }
}

function parseOpenAIUsageFromSseText(text: string): ParsedUsage {
  let usage = emptyUsage()
  for (const line of text.split(/\r?\n/)) {
    if (!line.startsWith('data:')) continue
    const data = line.slice(5).trim()
    if (!data || data === '[DONE]') continue
    try {
      const event = JSON.parse(data) as Record<string, unknown>
      const eventType = typeof event.type === 'string' ? event.type : ''
      if (eventType !== 'response.completed' && eventType !== 'response.done' && eventType !== 'response.failed') {
        continue
      }
      const nextUsage = extractUsage(typeof event.response === 'object' && event.response !== null ? (event.response as Record<string, unknown>).usage : event.usage)
      if (nextUsage.inputTokens !== undefined || nextUsage.outputTokens !== undefined || nextUsage.cacheReadTokens !== undefined) {
        usage = nextUsage
      }
    } catch {
      continue
    }
  }
  return usage
}

function extractUsage(value: unknown): ParsedUsage {
  if (typeof value !== 'object' || value === null) return emptyUsage()
  const usage = value as Record<string, unknown>
  const details = typeof usage.input_tokens_details === 'object' && usage.input_tokens_details !== null
    ? usage.input_tokens_details as Record<string, unknown>
    : {}
  const inputTokens = numberValue(usage.input_tokens)
  const outputTokens = numberValue(usage.output_tokens)
  const cacheReadTokens = numberValue(details.cached_tokens)
  return { inputTokens, outputTokens, cacheReadTokens }
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}
