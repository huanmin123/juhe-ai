import type { Request } from 'express'

import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import { listProviderModelPricing } from '../model-pricing/model-pricing.service.js'

export type UpstreamAccount = OpenAIAccountSecret

export function buildUpstreamUrl(baseUrl: string, pathAndQuery: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/+$/, '')
  const requestPath = pathAndQuery.startsWith('/') ? pathAndQuery : `/${pathAndQuery}`
  const normalizedPath = normalizedBase.endsWith('/v1') ? requestPath.replace(/^\/v1/, '') || '/' : requestPath
  return `${normalizedBase}${normalizedPath}`
}

export function buildUpstreamUrls(baseUrl: string, pathAndQuery: string): string[] {
  const primary = buildUpstreamUrl(baseUrl, pathAndQuery)
  const fallbackBase = baseUrl.trim().replace(/\/+$/, '')
  const fallback = fallbackBase.endsWith('/v1')
    ? `${fallbackBase}${(pathAndQuery.startsWith('/') ? pathAndQuery : `/${pathAndQuery}`).replace(/^\/v1/, '') || '/'}`
    : `${fallbackBase}${pathAndQuery.startsWith('/v1') ? pathAndQuery.replace(/^\/v1/, '') || '/' : pathAndQuery}`
  return [...new Set([primary, fallback])]
}

export function buildUpstreamUrlsForAccount(account: UpstreamAccount, req: Request): string[] {
  if (account.type === 'oauth') {
    return buildOpenAICodexUpstreamUrls(req)
  }
  return buildUpstreamUrls(account.baseUrl, req.originalUrl)
}

export function buildOpenAICodexUpstreamUrls(req: Request): string[] {
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

export function splitPathAndQuery(pathAndQuery: string): { path: string; query: string } {
  const queryIndex = pathAndQuery.indexOf('?')
  if (queryIndex < 0) {
    return { path: pathAndQuery, query: '' }
  }
  return {
    path: pathAndQuery.slice(0, queryIndex),
    query: pathAndQuery.slice(queryIndex)
  }
}

export function isOpenAIModelsRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'GET') {
    return false
  }
  const { path } = splitPathAndQuery(req.originalUrl)
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/models'
}

export function buildOpenAIModelsResponse(): { object: 'list'; data: Array<{ id: string; object: 'model'; created: number; owned_by: string }> } {
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

const openAICodexBaseUrl = 'https://chatgpt.com/backend-api/codex'
const openAICodexSupportedPaths = new Set(['/responses', '/responses/compact'])
