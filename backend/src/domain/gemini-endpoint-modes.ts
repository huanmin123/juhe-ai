import type { AccountSupportedEndpointMode } from './types.js'
import {
  GEMINI_COUNT_TOKENS_FAMILY,
  GEMINI_EMBED_CONTENT_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_MODELS_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY
} from './provider-protocol.js'

export const GEMINI_ENDPOINT_MODE_VALUES: readonly AccountSupportedEndpointMode[] = [
  'generate_content_json',
  'generate_content_sse',
  'count_tokens',
  'embed_content'
] as const

const geminiEndpointModeSet = new Set<string>(GEMINI_ENDPOINT_MODE_VALUES)

export interface GeminiEndpointModeDefaultContext {
  providerCode?: string
  accountType?: string
  protocolCode?: string
  protocolVersion?: string
  providerProtocolProfileId?: string
}

export type GeminiEndpointFamily =
  | typeof GEMINI_GENERATE_CONTENT_FAMILY
  | typeof GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  | typeof GEMINI_COUNT_TOKENS_FAMILY
  | typeof GEMINI_EMBED_CONTENT_FAMILY
  | typeof GEMINI_MODELS_FAMILY

export function defaultGeminiEndpointModes(_input: GeminiEndpointModeDefaultContext = {}): AccountSupportedEndpointMode[] {
  return ['generate_content_json', 'generate_content_sse', 'count_tokens']
}

export function normalizeGeminiEndpointModesForWrite(
  value: unknown,
  defaults: GeminiEndpointModeDefaultContext = {},
  label = '接口能力限制'
): AccountSupportedEndpointMode[] {
  if (value === undefined) {
    return defaultGeminiEndpointModes(defaults)
  }
  if (!Array.isArray(value)) {
    throw new Error(`${label}必须是数组`)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isGeminiEndpointMode(item)) {
      throw new Error(`${label}包含不支持的能力：${String(item)}`)
    }
    if (seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  if (!output.length) {
    throw new Error(`${label}至少选择一项`)
  }
  return output
}

export function normalizeGeminiEndpointModesForRuntime(
  value: unknown,
  defaults: GeminiEndpointModeDefaultContext = {}
): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) {
    return defaultGeminiEndpointModes(defaults)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isGeminiEndpointMode(item) || seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  return output.length ? output : defaultGeminiEndpointModes(defaults)
}

export function isGeminiEndpointMode(value: unknown): value is AccountSupportedEndpointMode {
  return typeof value === 'string' && geminiEndpointModeSet.has(value)
}

export function geminiEndpointModeForRequestShape(input: {
  endpoint?: string
  stream: boolean
}): AccountSupportedEndpointMode | undefined {
  const endpointFamily = geminiEndpointFamilyFromPath(input.endpoint)
  if (endpointFamily === GEMINI_GENERATE_CONTENT_FAMILY) {
    return input.stream ? 'generate_content_sse' : 'generate_content_json'
  }
  if (endpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return 'generate_content_sse'
  }
  if (endpointFamily === GEMINI_COUNT_TOKENS_FAMILY) {
    return 'count_tokens'
  }
  if (endpointFamily === GEMINI_EMBED_CONTENT_FAMILY) {
    return 'embed_content'
  }
  return undefined
}

export function geminiEndpointFamilyFromPath(value: unknown): GeminiEndpointFamily | undefined {
  if (typeof value !== 'string') return undefined
  const path = value.trim().toLowerCase()
  if (!path) return undefined
  const normalizedPath = normalizedGeminiPath(path)
  if (normalizedPath === '/models') return GEMINI_MODELS_FAMILY
  const match = /^\/models\/[^/]+:(generatecontent|streamgeneratecontent|counttokens|embedcontent)$/.exec(normalizedPath)
  if (!match) return undefined
  switch (match[1]) {
    case 'generatecontent':
      return GEMINI_GENERATE_CONTENT_FAMILY
    case 'streamgeneratecontent':
      return GEMINI_STREAM_GENERATE_CONTENT_FAMILY
    case 'counttokens':
      return GEMINI_COUNT_TOKENS_FAMILY
    case 'embedcontent':
      return GEMINI_EMBED_CONTENT_FAMILY
    default:
      return undefined
  }
}

export function accountSupportsGeminiEndpointMode(input: {
  supportedEndpointModes?: readonly AccountSupportedEndpointMode[]
  credentials?: Record<string, unknown>
  providerCode?: string
  accountType?: string
  protocolCode?: string
  protocolVersion?: string
  providerProtocolProfileId?: string
  mode: AccountSupportedEndpointMode
}): boolean {
  const supportedModes = input.supportedEndpointModes?.length
    ? [...input.supportedEndpointModes]
    : normalizeGeminiEndpointModesForRuntime(input.credentials?.supported_endpoint_modes, {
      providerCode: input.providerCode,
      accountType: input.accountType,
      protocolCode: input.protocolCode,
      protocolVersion: input.protocolVersion,
      providerProtocolProfileId: input.providerProtocolProfileId
    })
  return supportedModes.includes(input.mode)
}

export function assertGeminiEndpointModesCompatible(input: {
  modes: readonly AccountSupportedEndpointMode[]
  accountType?: string
}): void {
  if (input.accountType !== 'api_key') {
    throw new Error('Gemini 原生协议当前仅支持 API Key 账户')
  }
  const unsupported = input.modes.filter((mode) => !GEMINI_ENDPOINT_MODE_VALUES.includes(mode))
  if (unsupported.length) {
    throw new Error(`Gemini API Key 账户接口能力不支持：${unsupported.join(', ')}`)
  }
  if (!input.modes.includes('generate_content_json') && !input.modes.includes('generate_content_sse')) {
    throw new Error('Gemini API Key 账户必须至少支持 generateContent JSON 或 streamGenerateContent')
  }
}

function normalizedGeminiPath(pathAndQuery: string): string {
  const queryIndex = pathAndQuery.indexOf('?')
  const path = queryIndex < 0 ? pathAndQuery : pathAndQuery.slice(0, queryIndex)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  return requestPath.replace(/^\/v1beta(?=\/|$)/, '') || '/'
}
