import { runtimeConfig } from '../../../config/runtime.js'

type JsonRecord = Record<string, unknown>

export interface CodexResponsesWebSearchToolConfig {
  searchContextSize?: string
  filters?: JsonRecord
  externalWebAccess?: boolean
}

export interface CodexResponsesWebSearchInput {
  query: string
  tool?: CodexResponsesWebSearchToolConfig
  signal?: AbortSignal
}

export interface CodexResponsesWebSearchResult {
  title: string
  url: string
  snippet: string
}

export interface CodexResponsesWebSearchExecuteResult {
  query: string
  results: CodexResponsesWebSearchResult[]
}

export interface CodexResponsesWebSearchExecutor {
  execute(input: CodexResponsesWebSearchInput): Promise<CodexResponsesWebSearchExecuteResult>
}

export function runtimeCodexResponsesWebSearchExecutor(): CodexResponsesWebSearchExecutor | undefined {
  const endpoint = runtimeConfig.codexWebSearch.endpoint?.trim()
  if (!endpoint) {
    return undefined
  }
  return {
    execute: async (input) => executeRuntimeCodexResponsesWebSearch(endpoint, input)
  }
}

async function executeRuntimeCodexResponsesWebSearch(
  endpoint: string,
  input: CodexResponsesWebSearchInput
): Promise<CodexResponsesWebSearchExecuteResult> {
  const query = input.query.trim()
  if (!query) {
    return { query, results: [] }
  }
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), runtimeConfig.codexWebSearch.timeoutMs)
  const signal = anySignal([controller.signal, input.signal])
  try {
    const response = await fetch(endpoint, {
      method: 'POST',
      headers: runtimeConfig.codexWebSearch.apiKey
        ? {
            authorization: `Bearer ${runtimeConfig.codexWebSearch.apiKey}`,
            'content-type': 'application/json'
          }
        : { 'content-type': 'application/json' },
      body: JSON.stringify({
        query,
        ...(input.tool?.searchContextSize ? { search_context_size: input.tool.searchContextSize } : {}),
        ...(input.tool?.filters ? { filters: input.tool.filters } : {}),
        ...(typeof input.tool?.externalWebAccess === 'boolean' ? { external_web_access: input.tool.externalWebAccess } : {}),
        max_results: runtimeConfig.codexWebSearch.maxResults
      }),
      signal
    })
    const text = await readResponseTextWithLimit(response, runtimeConfig.codexWebSearch.maxBodyBytes)
    if (!response.ok) {
      throw new Error(`web_search 执行器 HTTP ${response.status}: ${text.slice(0, 200)}`)
    }
    const parsed = JSON.parse(text) as unknown
    return {
      query,
      results: normalizeWebSearchResults(parsed).slice(0, runtimeConfig.codexWebSearch.maxResults)
    }
  } finally {
    clearTimeout(timeout)
  }
}

async function readResponseTextWithLimit(response: Response, maxBytes: number): Promise<string> {
  const reader = response.body?.getReader()
  if (!reader) {
    return await response.text()
  }
  const chunks: Uint8Array[] = []
  let total = 0
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    if (!value) continue
    total += value.byteLength
    if (total > maxBytes) {
      await reader.cancel()
      throw new Error(`web_search 执行器响应超过 ${maxBytes} 字节`)
    }
    chunks.push(value)
  }
  return Buffer.concat(chunks).toString('utf8')
}

function normalizeWebSearchResults(value: unknown): CodexResponsesWebSearchResult[] {
  const root = isPlainObject(value) ? value : {}
  const rows = Array.isArray(root.results) ? root.results : []
  const results: CodexResponsesWebSearchResult[] = []
  for (const row of rows) {
    if (!isPlainObject(row)) continue
    const title = stringValue(row.title)
    const url = stringValue(row.url)
    const snippet = stringValue(row.snippet) ?? stringValue(row.content) ?? ''
    if (!title || !url) continue
    results.push({ title, url, snippet })
  }
  return results
}

function anySignal(signals: Array<AbortSignal | undefined>): AbortSignal {
  const activeSignals = signals.filter((signal): signal is AbortSignal => Boolean(signal))
  if (activeSignals.length === 1) {
    return activeSignals[0]!
  }
  const controller = new AbortController()
  const abort = () => controller.abort()
  for (const signal of activeSignals) {
    if (signal.aborted) {
      controller.abort()
      break
    }
    signal.addEventListener('abort', abort, { once: true })
  }
  return controller.signal
}

function isPlainObject(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
