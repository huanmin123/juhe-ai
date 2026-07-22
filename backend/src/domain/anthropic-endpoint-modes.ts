import type { AccountSupportedEndpointMode } from './types.js'
import {
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID
} from './provider-protocol.js'

export const ANTHROPIC_ENDPOINT_MODE_VALUES: readonly AccountSupportedEndpointMode[] = [
  'messages_json',
  'messages_sse',
  'message_token_counting'
] as const

const anthropicEndpointModeSet = new Set<string>(ANTHROPIC_ENDPOINT_MODE_VALUES)

export interface AnthropicEndpointModeDefaultContext {
  providerCode?: string
  accountType?: string
  protocolCode?: string
  protocolVersion?: string
  providerProtocolProfileId?: string
}

export function defaultAnthropicEndpointModes(input: AnthropicEndpointModeDefaultContext = {}): AccountSupportedEndpointMode[] {
  if (
    input.providerProtocolProfileId === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID
    || input.providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID
  ) {
    return ['messages_json', 'messages_sse']
  }
  return [...ANTHROPIC_ENDPOINT_MODE_VALUES]
}

export function normalizeAnthropicEndpointModesForWrite(
  value: unknown,
  defaults: AnthropicEndpointModeDefaultContext = {},
  label = '上游接口能力'
): AccountSupportedEndpointMode[] {
  if (value === undefined) {
    return defaultAnthropicEndpointModes(defaults)
  }
  if (!Array.isArray(value)) {
    throw new Error(`${label}必须是数组`)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isAnthropicEndpointMode(item)) {
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

export function normalizeAnthropicEndpointModesForRuntime(
  value: unknown,
  defaults: AnthropicEndpointModeDefaultContext = {}
): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) {
    return defaultAnthropicEndpointModes(defaults)
  }
  const output: AccountSupportedEndpointMode[] = []
  const seen = new Set<AccountSupportedEndpointMode>()
  for (const item of value) {
    if (!isAnthropicEndpointMode(item) || seen.has(item)) continue
    seen.add(item)
    output.push(item)
  }
  return output.length ? output : defaultAnthropicEndpointModes(defaults)
}

export function isAnthropicEndpointMode(value: unknown): value is AccountSupportedEndpointMode {
  return typeof value === 'string' && anthropicEndpointModeSet.has(value)
}

export function anthropicEndpointModeForRequestShape(input: {
  endpoint?: string
  stream: boolean
}): AccountSupportedEndpointMode | undefined {
  const endpointFamily = anthropicEndpointFamilyFromPath(input.endpoint)
  if (endpointFamily === 'messages') {
    return input.stream ? 'messages_sse' : 'messages_json'
  }
  if (endpointFamily === 'message_token_counting') {
    return 'message_token_counting'
  }
  return undefined
}

export function anthropicEndpointFamilyFromPath(value: unknown): 'messages' | 'message_token_counting' | 'models' | undefined {
  if (typeof value !== 'string') return undefined
  const path = value.trim().toLowerCase()
  if (!path) return undefined
  const normalizedPath = path.replace(/^\/v1(?=\/|$)/, '') || '/'
  if (normalizedPath === '/messages') return 'messages'
  if (normalizedPath === '/messages/count_tokens') return 'message_token_counting'
  if (normalizedPath === '/models') return 'models'
  return undefined
}

export function accountSupportsAnthropicEndpointMode(input: {
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
    : normalizeAnthropicEndpointModesForRuntime(input.credentials?.supported_endpoint_modes, {
      providerCode: input.providerCode,
      accountType: input.accountType,
      protocolCode: input.protocolCode,
      protocolVersion: input.protocolVersion,
      providerProtocolProfileId: input.providerProtocolProfileId
    })
  return supportedModes.includes(input.mode)
}

export function assertAnthropicEndpointModesCompatible(input: {
  modes: readonly AccountSupportedEndpointMode[]
  accountType?: string
}): void {
  if (input.accountType !== 'api_key' && input.accountType !== 'oauth') {
    throw new Error('Anthropic 当前仅支持 API Key 或 OAuth Access Token 账户')
  }
  const unsupported = input.modes.filter((mode) => !ANTHROPIC_ENDPOINT_MODE_VALUES.includes(mode))
  if (unsupported.length) {
    throw new Error(`Anthropic 账户上游接口能力不支持：${unsupported.join(', ')}`)
  }
  if (!input.modes.includes('messages_json') && !input.modes.includes('messages_sse')) {
    throw new Error('Anthropic 账户上游接口能力必须至少启用 Messages API (JSON) 或 Messages API (Streaming)')
  }
}
