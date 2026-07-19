import type { Request } from 'express'

import type { DispatchAccountSecret } from '../../../../storage/repositories.js'
import type { ProviderModelCatalogItem } from '../../../model-pricing/model-catalog.service.js'
import { GEMINI_INTERACTIONS_FAMILY, geminiEndpointFamilyFromPath } from '../../../../domain/gemini-endpoint-modes.js'
import {
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_MODELS_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY
} from '../../../../domain/provider-protocol.js'

export type GeminiUpstreamAccount = DispatchAccountSecret

export interface GeminiModelListItem {
  name: string
  version: string
  displayName: string
  description?: string
  inputTokenLimit?: number
  outputTokenLimit?: number
  supportedGenerationMethods: string[]
}

export interface GeminiModelsListResponse {
  models: GeminiModelListItem[]
}

export function buildGeminiUpstreamUrlsForAccount(account: GeminiUpstreamAccount, req: Request): string[] {
  if (account.type !== 'api_key' && account.type !== 'google_oauth') {
    return []
  }
  if (!isGeminiNativeRequest(req)) {
    return []
  }
  if (isGeminiModelsRequest(req)) {
    return []
  }
  const family = geminiEndpointFamilyFromPath(req.originalUrl || req.path)
  const stream = family === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
    || (family === GEMINI_INTERACTIONS_FAMILY && requestIndicatesSse(req))
  return [buildGeminiUpstreamUrl(account.baseUrl, req.originalUrl, { stream })]
}

export function buildGeminiUpstreamUrl(baseUrl: string, pathAndQuery: string, options: { stream?: boolean } = {}): string {
  const { path, query } = normalizedGeminiPathAndQuery(pathAndQuery)
  const base = normalizedGeminiBaseUrl(baseUrl)
  const basePath = base.pathname.replace(/\/+$/, '')
  const suffixPath = basePath.endsWith('/v1beta') ? path.replace(/^\/v1beta(?=\/|$)/, '') : path
  base.pathname = `${basePath}${suffixPath === '/' ? '' : suffixPath}`.replace(/\/{2,}/g, '/')
  const endpointFamily = geminiEndpointFamilyFromPath(path)
  if (endpointFamily === GEMINI_INTERACTIONS_FAMILY) {
    query.delete('alt')
  }
  mergeGeminiQuery(
    base.searchParams,
    query,
    endpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
      || (options.stream === true && endpointFamily !== GEMINI_INTERACTIONS_FAMILY)
  )
  return base.toString()
}

export function isGeminiModelsRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'GET') {
    return false
  }
  return geminiEndpointFamilyFromPath(req.originalUrl || req.path) === GEMINI_MODELS_FAMILY
}

export function isGeminiNativeRequest(req: Request): boolean {
  const method = req.method.toUpperCase()
  const family = geminiEndpointFamilyFromPath(req.originalUrl || req.path)
  if (method === 'GET' && family === GEMINI_MODELS_FAMILY) return true
  if ((method === 'GET' || method === 'DELETE') && family === GEMINI_INTERACTIONS_FAMILY) return true
  if (method !== 'POST') return false
  return family === GEMINI_GENERATE_CONTENT_FAMILY
    || family === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
    || family === GEMINI_COUNT_TOKENS_FAMILY
    || family === GEMINI_EMBED_CONTENT_FAMILY
    || family === GEMINI_INTERACTIONS_FAMILY
}

export function isGeminiInteractionsRequest(req: Request): boolean {
  return geminiEndpointFamilyFromPath(req.originalUrl || req.path) === GEMINI_INTERACTIONS_FAMILY
}

export function buildGeminiModelsResponse(catalog: ProviderModelCatalogItem[]): GeminiModelsListResponse {
  return {
    models: catalog.map((item) => ({
      name: item.model.startsWith('models/') ? item.model : `models/${item.model}`,
      version: item.model,
      displayName: item.model,
      description: item.capabilityNotes || item.notes || undefined,
      inputTokenLimit: positiveInteger(item.maxInputTokens ?? item.contextWindowTokens),
      outputTokenLimit: positiveInteger(item.maxOutputTokens),
      supportedGenerationMethods: supportedGenerationMethods(item)
    }))
  }
}

function normalizedGeminiBaseUrl(baseUrl: string): URL {
  const normalized = (baseUrl || 'https://generativelanguage.googleapis.com').trim().replace(/\/+$/, '')
  return new URL(normalized || 'https://generativelanguage.googleapis.com')
}

function normalizedGeminiPathAndQuery(pathAndQuery: string): { path: string; query: URLSearchParams } {
  const queryIndex = pathAndQuery.indexOf('?')
  const rawPath = queryIndex < 0 ? pathAndQuery : pathAndQuery.slice(0, queryIndex)
  const rawQuery = queryIndex < 0 ? '' : pathAndQuery.slice(queryIndex + 1)
  const requestPath = rawPath.startsWith('/') ? rawPath : `/${rawPath}`
  const pathWithoutVersion = requestPath.replace(/^\/v1beta(?=\/|$)/i, '') || '/'
  return {
    path: `/v1beta${pathWithoutVersion === '/' ? '' : pathWithoutVersion}`,
    query: new URLSearchParams(rawQuery)
  }
}

function mergeGeminiQuery(target: URLSearchParams, source: URLSearchParams, stream: boolean): void {
  source.delete('key')
  source.forEach((value, key) => {
    target.set(key, value)
  })
  if (stream && !target.has('alt')) {
    target.set('alt', 'sse')
  }
}

function requestIndicatesSse(req: Request): boolean {
  const query = new URLSearchParams((req.originalUrl || req.path || '').split('?', 2)[1] ?? '')
  if (query.get('stream')?.toLowerCase() === 'true') return true
  if (req.body && typeof req.body === 'object' && (req.body as Record<string, unknown>).stream === true) return true
  const accept = req.headers.accept
  return typeof accept === 'string' && accept.toLowerCase().includes('text/event-stream')
}

function supportedGenerationMethods(item: ProviderModelCatalogItem): string[] {
  const protocols = new Set(item.supportedApiProtocols)
  const methods: string[] = []
  if (protocols.has('generate_content') || protocols.has('stream_generate_content')) {
    methods.push('generateContent')
  }
  if (protocols.has('count_tokens')) {
    methods.push('countTokens')
  }
  if (protocols.has('embed_content')) {
    methods.push('embedContent')
  }
  return methods.length ? methods : ['generateContent']
}

function positiveInteger(value: number | undefined): number | undefined {
  return Number.isFinite(value) && value && value > 0 ? Math.trunc(value) : undefined
}
