import type { Request } from 'express'

import type { DispatchAccountSecret } from '../../../../storage/repositories.js'
import {
  buildCodexModelsResponseFromCatalog,
  buildOpenAIModelsResponseFromCatalog,
  type CodexModelsListResponse,
  type OpenAIModelsListResponse,
  type ProviderModelCatalogItem
} from '../../../model-pricing/model-catalog.service.js'

export type UpstreamAccount = DispatchAccountSecret

export function buildUpstreamUrl(baseUrl: string, pathAndQuery: string): string {
  const normalizedBase = normalizeOpenAIBaseUrl(baseUrl)
  return `${normalizedBase}${openAIPathSuffix(pathAndQuery)}`
}

export function buildUpstreamUrls(baseUrl: string, pathAndQuery: string): string[] {
  return [buildUpstreamUrl(baseUrl, pathAndQuery)]
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

export function isCodexModelsRequest(req: Request): boolean {
  if (!isOpenAIModelsRequest(req)) {
    return false
  }
  if (hasNonEmptyQueryParam(req, 'client_version')) {
    return true
  }
  return headerContainsCodex(requestHeader(req, 'originator'))
    || headerContainsCodex(requestHeader(req, 'user-agent'))
    || headerContainsCodex(requestHeader(req, 'x-codex-client'))
}

export type { CodexModelsListResponse, OpenAIModelsListResponse }

export function buildOpenAIModelsResponse(catalog: ProviderModelCatalogItem[], req?: Request): OpenAIModelsListResponse | CodexModelsListResponse {
  if (req && isCodexModelsRequest(req)) {
    return buildCodexModelsResponseFromCatalog(catalog)
  }
  return buildOpenAIModelsResponseFromCatalog(catalog)
}

function hasNonEmptyQueryParam(req: Request, name: string): boolean {
  const { query } = splitPathAndQuery(req.originalUrl)
  if (!query) {
    return false
  }
  const params = new URLSearchParams(query.startsWith('?') ? query.slice(1) : query)
  const value = params.get(name)
  return typeof value === 'string' && value.trim().length > 0
}

function headerContainsCodex(value: string | undefined): boolean {
  return typeof value === 'string' && value.toLowerCase().includes('codex')
}

function requestHeader(req: Request, name: string): string | undefined {
  if (typeof req.header === 'function') {
    return req.header(name)
  }
  const headers = req.headers as Record<string, string | string[] | undefined>
  const value = headers[name.toLowerCase()] ?? headers[name]
  return Array.isArray(value) ? value[0] : value
}

const openAICodexBaseUrl = 'https://chatgpt.com/backend-api/codex'
const openAICodexSupportedPaths = new Set(['/responses', '/responses/compact'])
