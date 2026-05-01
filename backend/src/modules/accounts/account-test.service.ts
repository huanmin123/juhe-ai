import { request as httpsRequest } from 'node:https'

import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { resolveProxyUrlForProfile, updateAccount } from '../../storage/repositories.js'
import {
  buildOpenAIOAuthCredentials,
  createProxyAgent,
  refreshOpenAIOAuthToken,
  shouldRefreshOpenAIOAuthCredentials
} from '../openai-oauth/openai-oauth.service.js'

export async function testOpenAIAccount(account: AccountSummary): Promise<AccountTestResult> {
  const prepared = await prepareAccountForTest(account)
  const modelsUrl = `${prepared.baseUrl.replace(/\/+$/, '')}/models`
  const headers = {
    authorization: `Bearer ${prepared.apiKey}`,
    accept: 'application/json',
    'user-agent': 'sub2api-lite-account-test/0.1'
  } as Record<string, string>
  if (prepared.organizationId) {
    headers['OpenAI-Organization'] = prepared.organizationId
  }

  try {
    const response = await requestModels(modelsUrl, headers, prepared.proxyUrl)
    const upstreamMessage = parseUpstreamMessage(response.bodyText)
    return {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success: response.statusCode >= 200 && response.statusCode < 300,
      statusCode: response.statusCode,
      message: response.statusCode >= 200 && response.statusCode < 300 ? 'OpenAI /models 测试通过' : upstreamMessage || `OpenAI /models 测试失败：HTTP ${response.statusCode}`,
      modelsUrl,
      proxyUrl: prepared.proxyUrl,
      tokenRefreshed: prepared.tokenRefreshed
    }
  } catch (error) {
    return {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success: false,
      message: error instanceof Error ? error.message : 'OpenAI /models 测试失败',
      modelsUrl,
      proxyUrl: prepared.proxyUrl,
      tokenRefreshed: prepared.tokenRefreshed
    }
  }
}

function requestModels(modelsUrl: string, headers: Record<string, string>, proxyUrl?: string): Promise<{ statusCode: number; bodyText: string }> {
  return new Promise((resolve, reject) => {
    const request = httpsRequest(modelsUrl, {
      method: 'GET',
      headers,
      agent: proxyUrl ? createProxyAgent(proxyUrl) : undefined,
      timeout: 30000
    }, (response) => {
      const chunks: Buffer[] = []
      response.on('data', (chunk: Buffer) => chunks.push(chunk))
      response.on('end', () => {
        resolve({ statusCode: response.statusCode ?? 0, bodyText: Buffer.concat(chunks).toString('utf8') })
      })
    })
    request.on('error', reject)
    request.on('timeout', () => request.destroy(new Error('OpenAI /models test timed out')))
    request.end()
  })
}

async function prepareAccountForTest(account: AccountSummary): Promise<{
  apiKey: string
  baseUrl: string
  organizationId?: string
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

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
