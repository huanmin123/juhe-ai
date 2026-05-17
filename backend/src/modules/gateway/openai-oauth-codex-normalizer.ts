import { createHash } from 'node:crypto'

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

export interface OpenAIOAuthCodexNormalizeInput {
  inputHeaders: Record<string, string | string[] | undefined>
  account: OpenAIOAuthCodexAccount
  identity: OpenAIOAuthCodexIdentity
  compact: boolean
}

export interface NormalizedCodexBody {
  body?: string
  stream: boolean
  session: OpenAIOAuthCodexSessionResolution
}

export interface OpenAIOAuthCodexSessionResolution {
  sessionId?: string
  conversationId?: string
  promptCacheKey?: string
}

export class OpenAIOAuthCodexAdapterError extends Error {
  readonly statusCode: number
  readonly type: string
  readonly code: string

  constructor(
    message: string,
    code = 'invalid_openai_oauth_codex_request',
    options: { statusCode?: number; type?: string } = {}
  ) {
    super(message)
    this.code = code
    this.statusCode = options.statusCode ?? 400
    this.type = options.type ?? 'invalid_request_error'
  }
}

export function normalizeOpenAIOAuthCodexParsedBody(
  parsedBody: unknown,
  input: OpenAIOAuthCodexNormalizeInput
): NormalizedCodexBody {
  const body = ensurePlainJsonObject(parsedBody)
  validateOpenAIOAuthCodexBody(body, input.compact)
  const session = resolveOpenAIOAuthCodexSession(input.inputHeaders, body, input.account, input.identity)
  applyOpenAIOAuthCodexSessionToBody(body, session, input.compact)
  normalizeOpenAIOAuthCodexInstructions(body)
  normalizeOpenAIOAuthCodexInput(body)
  normalizeOpenAIOAuthCodexTools(body)
  normalizeOpenAIOAuthCodexServiceTier(body)

  if (input.compact) {
    deleteFields(body, openAIOAuthCodexCompactDroppedFields)
    return { body: JSON.stringify(body), stream: false, session }
  }

  deleteFields(body, openAIOAuthCodexDroppedFields)
  body.store = false
  body.stream = true

  return { body: JSON.stringify(body), stream: true, session }
}

export function normalizeOpenAIOAuthCodexRawBody(
  rawBody: Buffer,
  input: OpenAIOAuthCodexNormalizeInput
): NormalizedCodexBody {
  let parsed: unknown
  try {
    parsed = JSON.parse(rawBody.toString('utf8')) as unknown
  } catch {
    throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体必须是有效的 JSON 对象')
  }
  return normalizeOpenAIOAuthCodexParsedBody(parsed, input)
}

export function ensureOpenAIOAuthCodexPlainJsonObject(value: unknown): Record<string, unknown> {
  return ensurePlainJsonObject(value)
}

function ensurePlainJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value === 'object' && value !== null && !Array.isArray(value)) {
    return { ...value as Record<string, unknown> }
  }
  throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体必须是 JSON 对象')
}

function validateOpenAIOAuthCodexBody(body: Record<string, unknown>, compact: boolean): void {
  if (typeof body.model !== 'string' || !body.model.trim()) {
    throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体中的 model 必须是非空字符串')
  }

  if (compact) {
    return
  }

  if (!Object.prototype.hasOwnProperty.call(body, 'input')) {
    throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体必须包含 input 字段')
  }
  if (typeof body.input !== 'string' && !Array.isArray(body.input)) {
    throw new OpenAIOAuthCodexAdapterError('OpenAI OAuth Codex 请求体中的 input 必须是字符串或数组')
  }
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

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

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
