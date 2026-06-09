import type { Request } from 'express'

import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import {
  buildOpenAIResponsesChatBridgeUpstreamPathAndQuery,
  isOpenAIResponsesChatBridgeAccount,
  isOpenAIResponsesCompactRequest,
  isOpenAIResponsesChatBridgeRequest
} from './openai-responses-chat-bridge.js'
import {
  buildOpenAIModelsResponseFromCatalog,
  type OpenAIModelsListResponse,
  type ProviderModelCatalogItem
} from '../model-pricing/model-catalog.service.js'

export type UpstreamAccount = OpenAIAccountSecret

export function buildUpstreamUrl(baseUrl: string, pathAndQuery: string): string {
  const normalizedBase = normalizeOpenAIBaseUrl(baseUrl)
  return `${normalizedBase}${openAIPathSuffix(pathAndQuery)}`
}

export function buildUpstreamUrls(baseUrl: string, pathAndQuery: string): string[] {
  return [buildUpstreamUrl(baseUrl, pathAndQuery)]
}

export function buildUpstreamUrlsForAccount(account: UpstreamAccount, req: Request): string[] {
  if (account.type === 'oauth') {
    return buildOpenAICodexUpstreamUrls(req)
  }
  if (isOpenAIResponsesChatBridgeAccount(account) && isOpenAIResponsesCompactRequest(req)) {
    return []
  }
  if (isOpenAIResponsesChatBridgeRequest(req, account)) {
    return buildUpstreamUrls(account.baseUrl, buildOpenAIResponsesChatBridgeUpstreamPathAndQuery(req))
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

function normalizeOpenAIBaseUrl(baseUrl: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/+$/, '')
  return normalizedBase.endsWith('/v1') ? normalizedBase : `${normalizedBase}/v1`
}

function openAIPathSuffix(pathAndQuery: string): string {
  const { path, query } = splitPathAndQuery(pathAndQuery)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const pathWithoutVersion = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  return `${pathWithoutVersion === '/' ? '' : pathWithoutVersion}${query}`
}

export function isOpenAIModelsRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'GET') {
    return false
  }
  const { path } = splitPathAndQuery(req.originalUrl)
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/models'
}

export type { OpenAIModelsListResponse }

export function buildOpenAIModelsResponse(catalog: ProviderModelCatalogItem[]): OpenAIModelsListResponse {
  return buildOpenAIModelsResponseFromCatalog(catalog)
}

const openAICodexBaseUrl = 'https://chatgpt.com/backend-api/codex'
const openAICodexSupportedPaths = new Set(['/responses', '/responses/compact'])
