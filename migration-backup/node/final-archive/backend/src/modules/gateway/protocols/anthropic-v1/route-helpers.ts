import type { Request } from 'express'

import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../../../shared/rfc3339.js'
import type { OpenAIAccountSecret } from '../../../../storage/repositories.js'
import type { ProviderModelCatalogItem } from '../../../model-pricing/model-catalog.service.js'
import { anthropicClaudeCodePathAndQueryForRequest } from './client-compatibility.js'

export type AnthropicUpstreamAccount = OpenAIAccountSecret

export interface AnthropicModelListItem {
  type: 'model'
  id: string
  display_name: string
  created_at?: string
}

export interface AnthropicModelsListResponse {
  data: AnthropicModelListItem[]
  has_more: boolean
  first_id: string | null
  last_id: string | null
}

export function buildAnthropicUpstreamUrlsForAccount(account: AnthropicUpstreamAccount, req: Request): string[] {
  if (account.type !== 'api_key' && account.type !== 'oauth') {
    return []
  }
  if (!isSupportedAnthropicRequest(req)) {
    return []
  }
  return [buildAnthropicUpstreamUrl(account.baseUrl, anthropicClaudeCodePathAndQueryForRequest(req))]
}

export function buildAnthropicUpstreamUrl(baseUrl: string, pathAndQuery: string): string {
  const normalizedBase = normalizeAnthropicBaseUrl(baseUrl)
  return `${normalizedBase}${anthropicPathSuffix(pathAndQuery)}`
}

export function isAnthropicModelsRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'GET') {
    return false
  }
  return normalizedAnthropicPath(req.originalUrl) === '/models'
}

export function isAnthropicNativeRequest(req: Request): boolean {
  return isSupportedAnthropicRequest(req)
}

export function buildAnthropicModelsResponse(catalog: ProviderModelCatalogItem[]): AnthropicModelsListResponse {
  const data = catalog.map((item) => ({
    type: 'model' as const,
    id: item.model,
    display_name: item.model,
    created_at: modelCreatedAt(item)
  }))
  return {
    data,
    has_more: false,
    first_id: data[0]?.id ?? null,
    last_id: data[data.length - 1]?.id ?? null
  }
}

function isSupportedAnthropicRequest(req: Request): boolean {
  const method = req.method.toUpperCase()
  const path = normalizedAnthropicPath(req.originalUrl)
  if (method === 'POST' && path === '/messages') return true
  if (method === 'POST' && path === '/messages/count_tokens') return true
  if (method === 'GET' && path === '/models') return true
  return false
}

function normalizeAnthropicBaseUrl(baseUrl: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/+$/, '')
  return normalizedBase.endsWith('/v1') ? normalizedBase : `${normalizedBase}/v1`
}

function anthropicPathSuffix(pathAndQuery: string): string {
  const { path, query } = splitPathAndQuery(pathAndQuery)
  const normalizedPath = normalizedAnthropicPath(path)
  return `${normalizedPath === '/' ? '' : normalizedPath}${query}`
}

function normalizedAnthropicPath(pathAndQuery: string): string {
  const { path } = splitPathAndQuery(pathAndQuery)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  return requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
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

function modelCreatedAt(item: ProviderModelCatalogItem): string | undefined {
  if (item.releaseDate !== undefined) {
    const releaseDate = item.releaseDate.trim()
    if (!/^\d{4}-\d{2}-\d{2}$/.test(releaseDate)) {
      throw new Error('Anthropic 模型 releaseDate 必须是 YYYY-MM-DD 日期')
    }
    const timestamp = rfc3339InstantMilliseconds(`${releaseDate}T00:00:00.000Z`)
    if (timestamp === undefined) {
      throw new Error('Anthropic 模型 releaseDate 必须是有效日期')
    }
    return new Date(timestamp).toISOString()
  }
  if (item.createdAt === undefined) return undefined
  return requiredRfc3339Instant(item.createdAt, 'Anthropic 模型 createdAt')
}
