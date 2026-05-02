import type { IncomingHttpHeaders } from 'node:http'
import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { resolveProxyUrlForProfile, updateAccount } from '../../storage/repositories.js'
import { applyAccountErrorHandling } from '../gateway/account-error-policy.service.js'
import { persistOpenAICodexUsageHeaders } from '../gateway/openai-codex-usage.service.js'
import {
  buildOpenAIOAuthCredentials,
  createProxyAgent,
  refreshOpenAIOAuthToken,
  shouldRefreshOpenAIOAuthCredentials
} from '../openai-oauth/openai-oauth.service.js'

const defaultTestModel = 'gpt-5.5'
const defaultTestPrompt = 'hi'
const defaultOpenAITestInstructions = 'You are ChatGPT, a helpful assistant.'
const chatGPTCodexResponsesUrl = 'https://chatgpt.com/backend-api/codex/responses'

export async function testOpenAIAccount(account: AccountSummary, input: { model?: string; prompt?: string } = {}): Promise<AccountTestResult> {
  const prepared = await prepareAccountForTest(account)
  const modelsUrl = `${prepared.baseUrl.replace(/\/+$/, '')}/models`
  const model = stringValue(input.model) || defaultTestModel
  const prompt = stringValue(input.prompt) || defaultTestPrompt
  const isOAuth = account.type === 'oauth'
  const requestUrl = isOAuth ? chatGPTCodexResponsesUrl : `${prepared.baseUrl.replace(/\/+$/, '')}/responses`
  const requestBody = createOpenAITestPayload(model, prompt, isOAuth)
  const requestBodyText = JSON.stringify(requestBody)
  const headers = buildTestHeaders(prepared, isOAuth, requestBodyText)
  const startedAt = Date.now()

  try {
    const response = await requestOpenAITest(requestUrl, headers, requestBodyText, prepared.proxyUrl)
    const upstreamMessage = parseUpstreamMessage(response.bodyText)
    const streamFailureMessage = parseOpenAIStreamFailureMessage(response.bodyText)
    const outputText = extractOpenAIResponseOutputText(response.bodyText)
    const success = response.statusCode >= 200 && response.statusCode < 300 && !streamFailureMessage
    if (isOAuth) {
      persistOpenAICodexUsageHeaders(account.id, response.headers, 'account_test')
    }
    const policyResult = applyAccountErrorHandling(account, {
      success,
      statusCode: response.statusCode,
      headers: response.headers,
      bodyText: response.bodyText,
      errorMessage: streamFailureMessage || upstreamMessage
    })
    return {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success,
      statusCode: response.statusCode,
      message: success ? 'OpenAI Responses 测试通过' : streamFailureMessage || upstreamMessage || `API returned ${response.statusCode}`,
      model,
      requestUrl,
      requestBody,
      responseHeaders: response.headers,
      responseBody: parseJsonBody(response.bodyText),
      responseText: response.bodyText,
      outputText,
      modelsUrl,
      proxyUrl: prepared.proxyUrl,
      tokenRefreshed: prepared.tokenRefreshed,
      durationMs: Date.now() - startedAt,
      accountStatusChanged: policyResult.changed,
      accountStatus: policyResult.accountStatus,
      errorPolicyAction: policyResult.action,
      errorPolicyReason: policyResult.reason
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : 'OpenAI Responses 测试失败'
    const policyResult = applyAccountErrorHandling(account, {
      success: false,
      errorMessage: message
    })
    return {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success: false,
      message,
      model,
      requestUrl,
      requestBody,
      responseText: message,
      modelsUrl,
      proxyUrl: prepared.proxyUrl,
      tokenRefreshed: prepared.tokenRefreshed,
      durationMs: Date.now() - startedAt,
      accountStatusChanged: policyResult.changed,
      accountStatus: policyResult.accountStatus,
      errorPolicyAction: policyResult.action,
      errorPolicyReason: policyResult.reason
    }
  }
}

function buildTestHeaders(
  prepared: { apiKey: string; organizationId?: string; chatgptAccountId?: string },
  isOAuth: boolean,
  bodyText: string
): Record<string, string> {
  const headers = {
    authorization: `Bearer ${prepared.apiKey}`,
    accept: isOAuth ? 'text/event-stream' : 'application/json, text/event-stream',
    'content-type': 'application/json',
    'content-length': String(Buffer.byteLength(bodyText)),
    'user-agent': isOAuth ? 'codex_cli_rs/0.125.0' : 'juhe-ai-account-test/0.1'
  } as Record<string, string>
  if (prepared.organizationId) {
    headers['OpenAI-Organization'] = prepared.organizationId
  }
  if (isOAuth) {
    headers.originator = 'codex_cli_rs'
    headers.version = '0.125.0'
    headers['openai-beta'] = 'responses=experimental'
    if (prepared.chatgptAccountId) {
      headers['chatgpt-account-id'] = prepared.chatgptAccountId
    }
  }
  return headers
}

function createOpenAITestPayload(model: string, prompt: string, isOAuth: boolean): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    model,
    input: [
      {
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: prompt
          }
        ]
      }
    ],
    instructions: defaultOpenAITestInstructions,
    stream: true
  }
  if (isOAuth) {
    payload.store = false
  }
  return payload
}

function requestOpenAITest(
  requestUrl: string,
  headers: Record<string, string>,
  bodyText: string,
  proxyUrl?: string
): Promise<{ statusCode: number; headers: Record<string, string | string[]>; bodyText: string }> {
  return new Promise((resolve, reject) => {
    const url = new URL(requestUrl)
    const requestFn = url.protocol === 'http:' ? httpRequest : httpsRequest
    const request = requestFn(url, {
      method: 'POST',
      headers,
      agent: proxyUrl ? createProxyAgent(proxyUrl) : undefined,
      timeout: 120000
    }, (response) => {
      const chunks: Buffer[] = []
      response.on('data', (chunk: Buffer) => {
        chunks.push(chunk)
      })
      response.on('end', () => {
        resolve({
          statusCode: response.statusCode ?? 0,
          headers: normalizeHeaders(response.headers),
          bodyText: Buffer.concat(chunks).toString('utf8')
        })
      })
    })
    request.on('error', reject)
    request.on('timeout', () => request.destroy(new Error('OpenAI Responses test timed out')))
    request.end(bodyText)
  })
}

async function prepareAccountForTest(account: AccountSummary): Promise<{
  apiKey: string
  baseUrl: string
  organizationId?: string
  chatgptAccountId?: string
  proxyUrl?: string
  tokenRefreshed: boolean
}> {
  if (account.type === 'oauth') {
    const refreshToken = stringValue(account.credentials.refresh_token)
    const clientId = stringValue(account.credentials.client_id)
    const proxyUrl = resolveProxyUrlForProfile(account.proxyProfileId)
    if (shouldRefreshOpenAIOAuthCredentials(account.credentials)) {
      if (!refreshToken) {
        throw new Error('OAuth 账户缺少 refresh_token，无法刷新 access_token')
      }
      const tokenInfo = await refreshOpenAIOAuthToken({ refreshToken, clientId, proxyUrl })
      const credentials = {
        ...account.credentials,
        ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken })
      }
      updateAccount(account.id, { credentials, status: 'active' })
      return {
        apiKey: stringValue(credentials.access_token),
        baseUrl: stringValue(credentials.base_url) || 'https://api.openai.com/v1',
        organizationId: stringValue(credentials.organization_id),
        chatgptAccountId: stringValue(credentials.chatgpt_account_id) || stringValue(credentials.account_id),
        proxyUrl,
        tokenRefreshed: true
      }
    }
    const accessToken = stringValue(account.credentials.access_token)
    if (!accessToken) {
      throw new Error('OAuth 账户缺少 access_token')
    }
    return {
      apiKey: accessToken,
      baseUrl: stringValue(account.credentials.base_url) || 'https://api.openai.com/v1',
      organizationId: stringValue(account.credentials.organization_id),
      chatgptAccountId: stringValue(account.credentials.chatgpt_account_id) || stringValue(account.credentials.account_id),
      proxyUrl,
      tokenRefreshed: false
    }
  }

  const apiKey = stringValue(account.credentials.api_key)
  if (!apiKey) {
    throw new Error('API Key 账户缺少 api_key')
  }
  return {
    apiKey,
    baseUrl: stringValue(account.credentials.base_url) || 'https://api.openai.com/v1',
    organizationId: stringValue(account.credentials.organization_id),
    proxyUrl: resolveProxyUrlForProfile(account.proxyProfileId),
    tokenRefreshed: false
  }
}

function normalizeHeaders(headers: IncomingHttpHeaders): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  const hiddenHeaders = new Set(['authorization', 'cookie', 'set-cookie', 'proxy-authorization'])
  for (const [name, value] of Object.entries(headers)) {
    if (hiddenHeaders.has(name.toLowerCase())) {
      output[name] = '[redacted]'
      continue
    }
    if (typeof value === 'string' || Array.isArray(value)) {
      output[name] = value
    }
  }
  return output
}

function parseJsonBody(bodyText: string): unknown {
  if (!bodyText) return undefined
  try {
    return JSON.parse(bodyText) as unknown
  } catch {
    return undefined
  }
}

function extractOpenAIResponseOutputText(bodyText: string): string | undefined {
  if (!bodyText.trim()) return undefined
  const jsonOutput = extractTextFromOpenAIResponsePayload(parseJsonBody(bodyText))
  if (jsonOutput) return jsonOutput

  const outputParts: string[] = []
  for (const event of parseSseEvents(bodyText)) {
    if (event.type === 'response.output_text.delta' || event.type === 'response.refusal.delta') {
      const delta = stringValue(event.delta)
      if (delta) outputParts.push(delta)
      continue
    }
    if (event.type === 'response.output_text.done') {
      const text = stringValue(event.text)
      if (text && outputParts.join('') !== text) {
        return text
      }
      continue
    }
    if (event.type === 'response.completed' || event.type === 'response.done') {
      const responseText = extractTextFromOpenAIResponsePayload(event.response)
      if (responseText) return responseText
    }
  }

  const outputText = outputParts.join('').trim()
  return outputText || undefined
}

function extractTextFromOpenAIResponsePayload(payload: unknown): string | undefined {
  if (typeof payload !== 'object' || payload === null) return undefined
  const record = payload as Record<string, unknown>
  const outputText = stringValue(record.output_text)
  if (outputText) return outputText

  const outputParts: string[] = []
  const output = Array.isArray(record.output) ? record.output : []
  for (const item of output) {
    if (typeof item !== 'object' || item === null) continue
    const content = (item as Record<string, unknown>).content
    if (!Array.isArray(content)) continue
    for (const contentItem of content) {
      if (typeof contentItem !== 'object' || contentItem === null) continue
      const contentRecord = contentItem as Record<string, unknown>
      const text = stringValue(contentRecord.text)
      if (text) outputParts.push(text)
    }
  }

  const text = outputParts.join('').trim()
  return text || undefined
}

function parseOpenAIStreamFailureMessage(bodyText: string): string | undefined {
  if (!bodyText.includes('response.failed') && !bodyText.includes('response.incomplete') && !bodyText.includes('error')) {
    return undefined
  }
  for (const payload of parseSseEvents(bodyText)) {
    const type = stringValue(payload.type)
    if (type !== 'response.failed' && type !== 'response.incomplete' && type !== 'error') continue
    const error = payload.error ?? (payload.response as Record<string, unknown> | undefined)?.error
    const message = parseErrorMessage(error) || parseErrorMessage(payload)
    return message || type
  }
  return undefined
}

function parseSseEvents(bodyText: string): Array<Record<string, unknown>> {
  const events: Array<Record<string, unknown>> = []
  for (const line of bodyText.split(/\r?\n/)) {
    const trimmed = line.trim()
    if (!trimmed.startsWith('data:')) continue
    const jsonText = trimmed.replace(/^data:\s*/, '')
    if (!jsonText || jsonText === '[DONE]') continue
    try {
      const payload = JSON.parse(jsonText) as Record<string, unknown>
      events.push(payload)
    } catch {
      continue
    }
  }
  return events
}

function parseUpstreamMessage(bodyText: string): string | undefined {
  if (!bodyText) return undefined
  try {
    const payload = JSON.parse(bodyText) as Record<string, unknown>
    const error = payload.error
    if (typeof error === 'object' && error !== null) {
      const message = (error as Record<string, unknown>).message
      if (typeof message === 'string' && message.trim()) {
        return message.trim()
      }
    }
    const message = payload.message
    if (typeof message === 'string' && message.trim()) {
      return message.trim()
    }
  } catch {
    return bodyText.slice(0, 240)
  }
  return undefined
}

function parseErrorMessage(value: unknown): string | undefined {
  if (typeof value === 'string' && value.trim()) return value.trim()
  if (typeof value !== 'object' || value === null) return undefined
  const payload = value as Record<string, unknown>
  return stringValue(payload.message) || stringValue(payload.code) || stringValue(payload.type)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
