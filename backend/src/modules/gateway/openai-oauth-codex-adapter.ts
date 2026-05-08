import { createHash } from 'node:crypto'
import type { Request } from 'express'

export interface OpenAIOAuthCodexAccount {
  id?: string
  apiKey: string
  credentials?: Record<string, unknown>
}

export interface OpenAIOAuthCodexIdentity {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface OpenAIOAuthCodexRequestParts {
  headers: Headers
  body?: string
}

export class OpenAIOAuthCodexAdapterError extends Error {
  readonly statusCode = 400
  readonly type = 'invalid_request_error'

  constructor(message: string, readonly code = 'invalid_openai_oauth_codex_request') {
    super(message)
  }
}

type RawBodyRequest = Request & { rawBody?: Buffer }

interface NormalizedCodexBody {
  body?: string
  stream: boolean
  session: OpenAIOAuthCodexSessionResolution
}

interface OpenAIOAuthCodexSessionResolution {
  sessionId?: string
  conversationId?: string
  promptCacheKey?: string
}

export function buildOpenAIOAuthCodexRequestParts(
  req: Request,
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity
): OpenAIOAuthCodexRequestParts {
  const compact = isOpenAIOAuthCodexCompactRequest(req)
  const normalizedBody = normalizeOpenAIOAuthCodexBody(req, inputHeaders, account, identity, compact)
  return {
    headers: buildOpenAIOAuthCodexHeaders(inputHeaders, account, {
      compact,
      stream: normalizedBody.stream,
      session: normalizedBody.session
    }),
    body: normalizedBody.body
  }
}

export function isOpenAIOAuthCodexCompactRequest(req: Request): boolean {
  const path = (req.originalUrl || req.path || '').split('?', 1)[0] || ''
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses/compact'
}

function normalizeOpenAIOAuthCodexBody(
  req: Request,
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity,
  compact: boolean
): NormalizedCodexBody {
  if (req.method === 'GET' || req.method === 'HEAD') {
    return { stream: false, session: {} }
  }

  const body = parseOpenAIOAuthCodexJsonObjectBody(req)
  const session = resolveOpenAIOAuthCodexSession(inputHeaders, body, account, identity)
  applyOpenAIOAuthCodexSessionToBody(body, session, compact)
  normalizeOpenAIOAuthCodexInstructions(body)
  normalizeOpenAIOAuthCodexInput(body)
  normalizeOpenAIOAuthCodexTools(body)
  normalizeOpenAIOAuthCodexServiceTier(body)

  if (compact) {
    deleteFields(body, openAIOAuthCodexCompactDroppedFields)
    return { body: JSON.stringify(body), stream: false, session }
  }

  deleteFields(body, openAIOAuthCodexDroppedFields)
  body.store = false
  body.stream = true

  return { body: JSON.stringify(body), stream: true, session }
}

function parseOpenAIOAuthCodexJsonObjectBody(req: Request): Record<string, unknown> {
  const rawBody = (req as RawBodyRequest).rawBody
  if (rawBody && rawBody.length > 0) {
    const text = rawBody.toString('utf8')
    try {
      return ensurePlainJsonObject(JSON.parse(text) as unknown)
    } catch (error) {
      if (error instanceof OpenAIOAuthCodexAdapterError) {
        throw error
      }
      throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体必须是有效的 JSON 对象')
    }
  }

  if (req.body === undefined || isEmptyPlainObject(req.body)) {
    return {}
  }
  return ensurePlainJsonObject(req.body)
}

function ensurePlainJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    return { ...value as Record<string, unknown> }
  }
  throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体必须是 JSON 对象')
}

function normalizeOpenAIOAuthCodexInstructions(body: Record<string, unknown>): void {
  if (!Object.prototype.hasOwnProperty.call(body, 'instructions')) {
    body.instructions = ''
    return
  }
  if (typeof body.instructions !== 'string') {
    throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体中的 instructions 必须是字符串')
  }
}

function normalizeOpenAIOAuthCodexInput(body: Record<string, unknown>): void {
  if (typeof body.input === 'string') {
    body.input = [
      {
        type: 'message',
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: body.input
          }
        ]
      }
    ]
    return
  }

  if (!Array.isArray(body.input)) {
    return
  }

  body.input = body.input.map((item) => {
    if (!isPlainObject(item)) {
      return item
    }
    if (item.role !== 'system') {
      return item
    }
    return {
      ...item,
      role: 'developer'
    }
  })
}

function normalizeOpenAIOAuthCodexTools(body: Record<string, unknown>): void {
  normalizeCodexBuiltinToolAtPath(body, ['tool_choice', 'type'])
  const tools = Array.isArray(body.tools) ? body.tools : undefined
  if (tools) {
    for (const tool of tools) {
      if (isPlainObject(tool)) {
        normalizeCodexBuiltinToolAtPath(tool, ['type'])
      }
    }
  }
  const toolChoice = isPlainObject(body.tool_choice) ? body.tool_choice : undefined
  const toolChoiceTools = Array.isArray(toolChoice?.tools) ? toolChoice.tools : undefined
  if (toolChoiceTools) {
    for (const tool of toolChoiceTools) {
      if (isPlainObject(tool)) {
        normalizeCodexBuiltinToolAtPath(tool, ['type'])
      }
    }
  }
}

function normalizeOpenAIOAuthCodexServiceTier(body: Record<string, unknown>): void {
  if (!Object.prototype.hasOwnProperty.call(body, 'service_tier')) {
    return
  }
  if (body.service_tier !== 'priority') {
    delete body.service_tier
  }
}

function normalizeCodexBuiltinToolAtPath(source: Record<string, unknown>, path: string[]): void {
  const owner = objectAtPath(source, path.slice(0, -1))
  if (!owner) return
  const key = path[path.length - 1]
  const current = typeof owner[key] === 'string' ? owner[key] : ''
  if (current === 'web_search_preview' || current === 'web_search_preview_2025_03_11') {
    owner[key] = 'web_search'
  }
}

function objectAtPath(source: Record<string, unknown>, path: string[]): Record<string, unknown> | undefined {
  let current: unknown = source
  for (const key of path) {
    if (!isPlainObject(current)) {
      return undefined
    }
    current = current[key]
  }
  return isPlainObject(current) ? current : undefined
}

function resolveOpenAIOAuthCodexSession(
  inputHeaders: Record<string, string | string[] | undefined>,
  body: Record<string, unknown>,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity
): OpenAIOAuthCodexSessionResolution {
  const metadata = isPlainObject(body.metadata) ? body.metadata : undefined
  const rawPromptCacheKey = firstNonEmptyString(
    body.prompt_cache_key,
    headerValue(inputHeaders, 'prompt_cache_key'),
    headerValue(inputHeaders, 'x-prompt-cache-key')
  )
  const rawSessionId = firstNonEmptyString(
    headerValue(inputHeaders, 'session_id'),
    headerValue(inputHeaders, 'session-id'),
    headerValue(inputHeaders, 'x-session-id'),
    body.session_id,
    metadata?.session_id,
    rawPromptCacheKey
  )
  const rawConversationId = firstNonEmptyString(
    headerValue(inputHeaders, 'conversation_id'),
    headerValue(inputHeaders, 'conversation-id'),
    headerValue(inputHeaders, 'x-conversation-id'),
    body.conversation_id,
    metadata?.conversation_id,
    rawPromptCacheKey
  )
  const rawPrimary = firstNonEmptyString(rawSessionId, rawConversationId, rawPromptCacheKey, metadata?.user_id)

  return {
    sessionId: rawSessionId ? isolateOpenAIOAuthCodexSessionId(rawSessionId, account, identity) : rawPromptCacheKey ? isolateOpenAIOAuthCodexSessionId(rawPromptCacheKey, account, identity) : undefined,
    conversationId: rawConversationId ? isolateOpenAIOAuthCodexSessionId(rawConversationId, account, identity) : rawPromptCacheKey ? isolateOpenAIOAuthCodexSessionId(rawPromptCacheKey, account, identity) : undefined,
    promptCacheKey: rawPromptCacheKey ? isolateOpenAIOAuthCodexSessionId(rawPromptCacheKey, account, identity) : rawPrimary ? isolateOpenAIOAuthCodexSessionId(rawPrimary, account, identity) : undefined
  }
}

function applyOpenAIOAuthCodexSessionToBody(
  body: Record<string, unknown>,
  session: OpenAIOAuthCodexSessionResolution,
  compact: boolean
): void {
  delete body.session_id
  delete body.conversation_id
  if (compact) {
    delete body.prompt_cache_key
    return
  }
  if (session.promptCacheKey) {
    body.prompt_cache_key = session.promptCacheKey
  }
}

export function isolateOpenAIOAuthCodexSessionId(
  raw: string,
  account: OpenAIOAuthCodexAccount,
  identity: OpenAIOAuthCodexIdentity
): string {
  const normalized = raw.trim()
  if (!normalized) {
    return ''
  }
  return createHash('sha256')
    .update(JSON.stringify({
      systemAccountId: identity.systemAccountId,
      apiKeyId: identity.apiKeyId ?? 'internal',
      groupId: identity.groupId,
      accountId: account.id ?? 'unknown',
      raw: normalized
    }))
    .digest('hex')
    .slice(0, 32)
}

function buildOpenAIOAuthCodexHeaders(
  inputHeaders: Record<string, string | string[] | undefined>,
  account: OpenAIOAuthCodexAccount,
  input: {
    compact: boolean
    stream: boolean
    session: OpenAIOAuthCodexSessionResolution
  }
): Headers {
  const headers = new Headers()

  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'accept-language')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-client-request-id')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-beta-features')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-turn-state')
  copyAllowedOpenAIOAuthCodexHeader(headers, inputHeaders, 'x-codex-turn-metadata')

  const incomingOriginator = headerValue(inputHeaders, 'originator')
  const originator = isCodexOriginator(incomingOriginator) ? incomingOriginator : 'codex_cli_rs'
  headers.set('originator', originator)

  const incomingUserAgent = headerValue(inputHeaders, 'user-agent')
  const keepIncomingUserAgent = isCodexUserAgent(incomingUserAgent)
    || (isCodexOriginator(incomingOriginator) && Boolean(incomingUserAgent))
  headers.set('user-agent', keepIncomingUserAgent ? incomingUserAgent ?? openAICodexUserAgent : openAICodexUserAgent)

  const incomingVersion = headerValue(inputHeaders, 'version')
  headers.set('version', isVersionLike(incomingVersion) && isCodexOriginator(incomingOriginator) ? incomingVersion : openAICodexVersion)

  const openAIBeta = headerValue(inputHeaders, 'openai-beta')
  headers.set('openai-beta', openAIBeta && openAIBeta.toLowerCase().includes('responses') ? openAIBeta : 'responses=experimental')
  headers.set('authorization', `Bearer ${account.apiKey}`)
  headers.set('content-type', 'application/json')
  headers.set('accept', input.compact || !input.stream ? 'application/json' : 'text/event-stream')

  const chatGPTAccountId = stringCredential(account.credentials, 'chatgpt_account_id') ?? stringCredential(account.credentials, 'account_id')
  if (chatGPTAccountId) {
    headers.set('chatgpt-account-id', chatGPTAccountId)
  }
  if (input.session.sessionId) {
    headers.set('session_id', input.session.sessionId)
  }
  if (input.session.conversationId) {
    headers.set('conversation_id', input.session.conversationId)
  }

  return headers
}

function copyAllowedOpenAIOAuthCodexHeader(
  output: Headers,
  inputHeaders: Record<string, string | string[] | undefined>,
  name: string
): void {
  const value = headerValue(inputHeaders, name)
  if (!value) {
    return
  }
  output.set(name, value)
}

function headerValue(inputHeaders: Record<string, string | string[] | undefined>, name: string): string | undefined {
  const lowerName = name.toLowerCase()
  for (const [inputName, value] of Object.entries(inputHeaders)) {
    if (inputName.toLowerCase() !== lowerName) {
      continue
    }
    const first = Array.isArray(value) ? value.find((item) => item.trim()) : value
    return typeof first === 'string' && first.trim() ? first.trim() : undefined
  }
  return undefined
}

function firstNonEmptyString(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) {
      return value.trim()
    }
  }
  return undefined
}

function deleteFields(body: Record<string, unknown>, fields: readonly string[]): void {
  for (const field of fields) {
    delete body[field]
  }
}

function isEmptyPlainObject(value: unknown): boolean {
  return isPlainObject(value) && Object.keys(value).length === 0
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringCredential(credentials: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = credentials?.[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isCodexOriginator(value: string | undefined): value is string {
  return typeof value === 'string' && /^codex(?:_|$)/i.test(value.trim())
}

function isCodexUserAgent(value: string | undefined): value is string {
  return typeof value === 'string' && value.toLowerCase().includes('codex')
}

function isVersionLike(value: string | undefined): value is string {
  return typeof value === 'string' && /^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][0-9a-z.-]+)?$/i.test(value.trim())
}

const openAICodexVersion = '0.125.0'
const openAICodexUserAgent = `codex_cli_rs/${openAICodexVersion}`

const openAIOAuthCodexDroppedFields = [
  'background',
  'conversation',
  'context_management',
  'frequency_penalty',
  'max_completion_tokens',
  'max_output_tokens',
  'metadata',
  'presence_penalty',
  'prompt_cache_retention',
  'safety_identifier',
  'stream_options',
  'temperature',
  'top_p',
  'truncation',
  'user'
]

const openAIOAuthCodexCompactDroppedFields = [
  ...openAIOAuthCodexDroppedFields,
  'include',
  'parallel_tool_calls',
  'prompt_cache_key',
  'reasoning',
  'store',
  'stream',
  'text',
  'tool_choice',
  'tools',
  'top_logprobs'
]
