import { randomUUID } from 'node:crypto'

export const openAICodexOriginator = 'Codex Desktop'
export const openAICodexVersion = '0.145.0'
export const openAICodexUserAgent = `Codex Desktop/${openAICodexVersion} (Windows 10.0.22621; x86_64) unknown (codex_exec; ${openAICodexVersion})`
export const openAICodexResponsesLiteHeader = 'x-openai-internal-codex-responses-lite'

type HeaderSource = Headers | Record<string, string | string[] | undefined>

export function isOpenAICodexClientHeaders(headers: HeaderSource): boolean {
  return isCodexIdentity(headerValue(headers, 'originator'))
    || isCodexIdentity(headerValue(headers, 'user-agent'))
}

export function normalizeOpenAICodexClientHeaders(headers: Headers, model?: string): void {
  if (isOpenAICodexClientHeaders(headers)) {
    return
  }

  const metadata = syntheticCodexTurnMetadata(headers)
  headers.set('originator', openAICodexOriginator)
  headers.set('user-agent', openAICodexUserAgent)
  setHeaderIfMissing(headers, 'session-id', metadata.session_id)
  setHeaderIfMissing(headers, 'thread-id', metadata.thread_id)
  setHeaderIfMissing(headers, 'x-client-request-id', metadata.session_id)
  setHeaderIfMissing(headers, 'x-codex-beta-features', 'remote_compaction_v2')
  headers.set('x-codex-turn-metadata', JSON.stringify(metadata))
  setHeaderIfMissing(headers, 'x-codex-window-id', metadata.window_id)
  if (usesOpenAICodexResponsesLite(model)) {
    headers.set(openAICodexResponsesLiteHeader, 'true')
  } else {
    headers.delete(openAICodexResponsesLiteHeader)
  }
}

export function usesOpenAICodexResponsesLite(model: string | undefined): boolean {
  return model !== undefined && openAICodexResponsesLiteModels.has(model.trim().toLowerCase())
}

export function normalizeOpenAICodexResponsesLiteBody(
  body: Record<string, unknown>,
  model: string | undefined,
  headers?: Headers
): void {
  if (headers && !isOpenAICodexClientHeaders(headers)) {
    normalizeOpenAICodexClientHeaders(headers, model)
    const metadata = parsedCodexTurnMetadata(headers.get('x-codex-turn-metadata'))
    if (metadata) {
      body.client_metadata = {
        ...plainObject(body.client_metadata),
        'x-codex-window-id': metadata.window_id,
        turn_id: metadata.turn_id,
        session_id: metadata.session_id,
        'x-codex-turn-metadata': JSON.stringify(metadata),
        'x-codex-installation-id': metadata.installation_id,
        thread_id: metadata.thread_id
      }
      if (!body.prompt_cache_key) {
        body.prompt_cache_key = metadata.session_id
      }
    }
  }
  if (!usesOpenAICodexResponsesLite(model)) {
    return
  }
  const reasoning = isPlainObject(body.reasoning) ? body.reasoning : {}
  body.reasoning = {
    ...reasoning,
    context: 'all_turns'
  }
  body.parallel_tool_calls = false
}

function syntheticCodexTurnMetadata(headers: Headers): CodexTurnMetadata {
  const current = parsedCodexTurnMetadata(headers.get('x-codex-turn-metadata'))
  const sessionId = text(current?.session_id) ?? headers.get('session-id')?.trim() ?? randomUUID()
  const threadId = text(current?.thread_id) ?? headers.get('thread-id')?.trim() ?? sessionId
  const turnId = text(current?.turn_id) ?? headers.get('x-client-request-id')?.trim() ?? randomUUID()
  const installationId = text(current?.installation_id) ?? headers.get('x-codex-installation-id')?.trim() ?? randomUUID()
  const windowId = text(current?.window_id) ?? headers.get('x-codex-window-id')?.trim() ?? `${threadId}:0`
  return {
    ...current,
    installation_id: installationId,
    session_id: sessionId,
    thread_id: threadId,
    turn_id: turnId,
    window_id: windowId,
    request_kind: text(current?.request_kind) ?? 'turn',
    thread_source: text(current?.thread_source) ?? 'user',
    sandbox: text(current?.sandbox) ?? 'none',
    turn_started_at_unix_ms: typeof current?.turn_started_at_unix_ms === 'number'
      ? current.turn_started_at_unix_ms
      : Date.now()
  }
}

function setHeaderIfMissing(headers: Headers, name: string, value: string): void {
  if (!headers.has(name)) headers.set(name, value)
}

function headerValue(headers: HeaderSource, name: string): string | undefined {
  if (headers instanceof Headers) {
    return headers.get(name)?.trim() || undefined
  }
  const value = headers[name] ?? headers[Object.keys(headers).find((key) => key.toLowerCase() === name) ?? '']
  return Array.isArray(value) ? value.join(', ').trim() || undefined : value?.trim() || undefined
}

function isCodexIdentity(value: string | undefined): boolean {
  return typeof value === 'string' && /^codex(?:[\s_/-]|$)/i.test(value.trim())
}

function parsedCodexTurnMetadata(value: string | null): Partial<CodexTurnMetadata> | undefined {
  if (!value) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    return isPlainObject(parsed) ? parsed : undefined
  } catch {
    return undefined
  }
}

function plainObject(value: unknown): Record<string, unknown> {
  return isPlainObject(value) ? value : {}
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

interface CodexTurnMetadata extends Record<string, unknown> {
  installation_id: string
  session_id: string
  thread_id: string
  turn_id: string
  window_id: string
  request_kind: string
  thread_source: string
  sandbox: string
  turn_started_at_unix_ms: number
}

const openAICodexResponsesLiteModels = new Set([
  'gpt-5.6-sol',
  'gpt-5.6-terra',
  'gpt-5.6-luna'
])
