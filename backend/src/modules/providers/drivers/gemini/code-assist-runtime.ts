import type { GatewayUpstreamResponse } from '../../../gateway/upstream/request.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'

export const GEMINI_CODE_ASSIST_BASE_URL = 'https://cloudcode-pa.googleapis.com'
export const GEMINI_CODE_ASSIST_STREAM_URL = `${GEMINI_CODE_ASSIST_BASE_URL}/v1internal:streamGenerateContent?alt=sse`
export const GEMINI_CLI_USER_AGENT = 'GeminiCLI/0.1.5 (Windows; AMD64)'

export type GeminiOAuthRuntimeMode = 'ai_studio' | 'code_assist' | 'google_one'

type GeminiOAuthRuntimeAccount = {
  type?: string
  credentials?: Record<string, unknown>
}

export function geminiOAuthRuntimeMode(account: GeminiOAuthRuntimeAccount): GeminiOAuthRuntimeMode {
  if (account.type !== 'google_oauth') return 'ai_studio'
  const oauthType = textCredential(account.credentials?.oauth_type)?.toLowerCase()
  if (oauthType === 'code_assist' || oauthType === 'google_one') return oauthType
  // sub2api treats legacy Google OAuth credentials with a project as Code Assist.
  return !oauthType && textCredential(account.credentials?.project_id) ? 'code_assist' : 'ai_studio'
}

export function usesGeminiCodeAssistRuntime(account: GeminiOAuthRuntimeAccount): boolean {
  const mode = geminiOAuthRuntimeMode(account)
  return mode === 'code_assist' || mode === 'google_one'
}

export function geminiCodeAssistProjectId(account: Pick<DispatchAccountSecret, 'credentials'>): string {
  const projectId = textCredential(account.credentials?.project_id)
  if (!projectId) throw new Error('Gemini Code Assist / Google One OAuth 缺少 project_id')
  return projectId
}

export function buildGeminiCodeAssistRequestParts(input: {
  accessToken: string
  projectId: string
  model: string
  body: Buffer | string | undefined
}): { headers: Headers; body: Buffer } {
  const request = parseGeminiRequestObject(input.body)
  const headers = new Headers({
    authorization: `Bearer ${input.accessToken}`,
    'content-type': 'application/json',
    'user-agent': GEMINI_CLI_USER_AGENT
  })
  return {
    headers,
    body: Buffer.from(JSON.stringify({
      model: requiredText(input.model, 'Gemini Code Assist model'),
      project: requiredText(input.projectId, 'Gemini Code Assist project_id'),
      request
    }), 'utf8')
  }
}

export function transformGeminiCodeAssistUpstreamResponse(
  response: GatewayUpstreamResponse,
  options: { downstreamStream: boolean }
): GatewayUpstreamResponse {
  if (!response.ok || !response.body) return response
  const headers = new Headers(response.headers)
  headers.delete('content-length')
  if (options.downstreamStream) {
    headers.set('content-type', 'text/event-stream; charset=utf-8')
    return {
      status: response.status,
      ok: response.ok,
      headers,
      body: unwrapGeminiCodeAssistSse(response.body)
    }
  }
  headers.set('content-type', 'application/json; charset=utf-8')
  return {
    status: response.status,
    ok: response.ok,
    headers,
    body: collectGeminiCodeAssistSse(response.body)
  }
}

function parseGeminiRequestObject(body: Buffer | string | undefined): Record<string, unknown> {
  if (body === undefined) throw new Error('Gemini Code Assist 请求体不能为空')
  let parsed: unknown
  try {
    parsed = JSON.parse(typeof body === 'string' ? body : body.toString('utf8'))
  } catch {
    throw new Error('Gemini Code Assist 请求体必须是有效的 JSON 对象')
  }
  if (!isJsonRecord(parsed)) throw new Error('Gemini Code Assist 请求体必须是 JSON 对象')
  return parsed
}

async function * unwrapGeminiCodeAssistSse(body: AsyncIterable<Uint8Array>): AsyncIterable<Uint8Array> {
  for await (const event of iterateSseEvents(body)) {
    const payload = sseDataPayload(event)
    if (payload === undefined || payload === '' || payload === '[DONE]') {
      yield Buffer.from(`${event}\n\n`, 'utf8')
      continue
    }
    yield Buffer.from(`data: ${unwrapGeminiCodeAssistPayload(payload)}\n\n`, 'utf8')
  }
}

async function * collectGeminiCodeAssistSse(body: AsyncIterable<Uint8Array>): AsyncIterable<Uint8Array> {
  let last: Record<string, unknown> | undefined
  let lastWithParts: Record<string, unknown> | undefined
  const collectedTextParts: string[] = []

  for await (const event of iterateSseEvents(body)) {
    const payload = sseDataPayload(event)
    if (!payload || payload === '[DONE]') continue
    const unwrappedPayload = unwrapGeminiCodeAssistPayload(payload)
    let unwrapped: unknown
    try {
      unwrapped = JSON.parse(unwrappedPayload)
    } catch {
      continue
    }
    if (!isJsonRecord(unwrapped)) continue
    last = unwrapped
    const parts = extractGeminiParts(unwrapped)
    if (!parts.length) continue
    lastWithParts = unwrapped
    for (const part of parts) {
      if (typeof part.text === 'string' && part.text) collectedTextParts.push(part.text)
    }
  }

  yield Buffer.from(JSON.stringify(mergeCollectedTextParts(lastWithParts ?? last ?? {}, collectedTextParts)), 'utf8')
}

async function * iterateSseEvents(body: AsyncIterable<Uint8Array>): AsyncIterable<string> {
  const decoder = new TextDecoder()
  let pending = ''
  for await (const chunk of body) {
    pending += decoder.decode(chunk, { stream: true })
    while (true) {
      const boundary = sseEventBoundary(pending)
      if (!boundary) break
      const event = pending.slice(0, boundary.index).replace(/\r\n/g, '\n')
      pending = pending.slice(boundary.index + boundary.length)
      yield event
    }
  }
  pending += decoder.decode()
  const tail = pending.trim()
  if (tail) yield tail.replace(/\r\n/g, '\n')
}

function sseEventBoundary(value: string): { index: number; length: number } | undefined {
  const match = /\r?\n\r?\n/u.exec(value)
  return match?.index === undefined ? undefined : { index: match.index, length: match[0].length }
}

function sseDataPayload(event: string): string | undefined {
  const dataLines = event
    .split('\n')
    .filter((line) => line === 'data' || line.startsWith('data:'))
    .map((line) => line === 'data' ? '' : line.slice(5).replace(/^ /, ''))
  return dataLines.length ? dataLines.join('\n').trim() : undefined
}

function unwrapGeminiCodeAssistPayload(payload: string): string {
  let parsed: unknown
  try {
    parsed = JSON.parse(payload)
  } catch {
    return payload
  }
  if (!isJsonRecord(parsed)) return payload
  const response = parsed.response
  return isJsonRecord(response) || Array.isArray(response) ? JSON.stringify(response) : payload
}

function extractGeminiParts(response: Record<string, unknown>): Array<Record<string, unknown>> {
  const candidates = Array.isArray(response.candidates) ? response.candidates : []
  const firstCandidate = isJsonRecord(candidates[0]) ? candidates[0] : undefined
  const content = isJsonRecord(firstCandidate?.content) ? firstCandidate.content : undefined
  const parts = Array.isArray(content?.parts) ? content.parts : []
  return parts.filter(isJsonRecord)
}

function mergeCollectedTextParts(
  response: Record<string, unknown>,
  textParts: string[]
): Record<string, unknown> {
  if (!textParts.length) return response
  const mergedText = textParts.join('')
  const result = structuredClone(response)
  const candidates = Array.isArray(result.candidates) && result.candidates.length
    ? result.candidates
    : [{}]
  const candidate = isJsonRecord(candidates[0]) ? candidates[0] : {}
  candidates[0] = candidate
  const content = isJsonRecord(candidate.content) ? candidate.content : { role: 'model' }
  candidate.content = content
  const existingParts = Array.isArray(content.parts) ? content.parts : []
  const newParts: unknown[] = []
  let textUpdated = false
  for (const part of existingParts) {
    if (isJsonRecord(part) && 'text' in part && !textUpdated) {
      newParts.push({ ...part, text: mergedText })
      textUpdated = true
    } else {
      newParts.push(part)
    }
  }
  if (!textUpdated) newParts.unshift({ text: mergedText })
  content.parts = newParts
  result.candidates = candidates
  return result
}

function requiredText(value: string, label: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`${label} 不能为空`)
  return normalized
}

function textCredential(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isJsonRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
