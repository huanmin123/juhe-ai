import type { AccountClientCompatibility } from '../../domain/types.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  defaultAnthropicEndpointModes,
  isAnthropicEndpointMode
} from '../../domain/anthropic-endpoint-modes.js'
import {
  defaultOpenAIEndpointModes,
  openAIEndpointModeForRequestShape,
  supportsCodexResponsesChatBridge
} from '../../domain/openai-endpoint-modes.js'
import type { RecentOpenAIRequestShape } from '../../storage/repositories.js'

export const accountTestDefaultPrompt = '只输出 OK'
const defaultOpenAITestInstructions = 'You are ChatGPT, a helpful assistant.'
const gatewayTestPath = '/v1/responses'
const gatewayChatCompletionsPath = '/v1/chat/completions'
const gatewayAnthropicMessagesPath = '/v1/messages'
export const accountTestModelsPath = '/v1/models'

export type AccountTestRequestInput = {
  explicitModel?: string
  fallbackModel: string
  prompt: string
  isOAuth: boolean
  clientCompatibility: AccountClientCompatibility
  providerProtocolProfileId?: string
  supportedEndpointModes?: AccountSupportedEndpointMode[]
  requestShape?: RecentOpenAIRequestShape
}

export type AccountTestRequest = {
  path: string
  body: Record<string, unknown>
  model: string
}

export function createOpenAITestRequest(input: AccountTestRequestInput): AccountTestRequest {
  const mode = testEndpointModeFromRecentShape(
    input.requestShape,
    input.isOAuth,
    input.clientCompatibility,
    input.supportedEndpointModes,
    input.providerProtocolProfileId
  )
  const path = testPathFromEndpointMode(mode)
  const stream = mode === 'chat_sse' || mode === 'responses_sse'
  const model = stringValue(input.explicitModel) || input.fallbackModel
  return {
    path,
    body: path === gatewayChatCompletionsPath
      ? createOpenAIChatCompletionsTestPayload(model, input.prompt, stream)
      : createOpenAIResponsesTestPayload(model, input.prompt, input.isOAuth, input.clientCompatibility, stream),
    model
  }
}

export function createAnthropicTestRequest(input: {
  explicitModel?: string
  fallbackModel: string
  prompt: string
  supportedEndpointModes?: AccountSupportedEndpointMode[]
}): AccountTestRequest {
  const supportedModes = input.supportedEndpointModes?.filter(isAnthropicEndpointMode)
  const modes = supportedModes?.length ? supportedModes : defaultAnthropicEndpointModes()
  const stream = !modes.includes('messages_json') && modes.includes('messages_sse')
  const model = stringValue(input.explicitModel) || input.fallbackModel
  return {
    path: gatewayAnthropicMessagesPath,
    body: {
      model,
      messages: [
        {
          role: 'user',
          content: input.prompt
        }
      ],
      max_tokens: 1,
      stream
    },
    model
  }
}

export function testPathFromRecentShape(
  shape: RecentOpenAIRequestShape | undefined,
  isOAuth: boolean,
  clientCompatibility: AccountClientCompatibility,
  supportedEndpointModes?: AccountSupportedEndpointMode[],
  providerProtocolProfileId?: string
): string {
  return testPathFromEndpointMode(testEndpointModeFromRecentShape(shape, isOAuth, clientCompatibility, supportedEndpointModes, providerProtocolProfileId))
}

export function testEndpointModeFromRecentShape(
  shape: RecentOpenAIRequestShape | undefined,
  isOAuth: boolean,
  clientCompatibility: AccountClientCompatibility,
  supportedEndpointModes?: AccountSupportedEndpointMode[],
  providerProtocolProfileId?: string
): AccountSupportedEndpointMode {
  const supportedModes = supportedEndpointModes?.length
    ? supportedEndpointModes
    : defaultOpenAIEndpointModes({
      accountType: isOAuth ? 'oauth' : 'api_key',
      providerProtocolProfileId,
      clientCompatibility
    })
  const preferredModes: AccountSupportedEndpointMode[] = []
  if (
    clientCompatibility === 'codex_responses'
    && supportsCodexResponsesChatBridge({ providerProtocolProfileId })
    && supportedModes.includes('chat_sse')
  ) {
    return 'responses_sse'
  }
  if (isOAuth || clientCompatibility === 'codex_responses') {
    preferredModes.push('responses_sse')
  }
  const recentMode = openAIEndpointModeForRequestShape({
    endpoint: shape?.endpoint,
    stream: shape?.stream ?? true
  })
  if (recentMode) {
    preferredModes.push(recentMode)
  }
  preferredModes.push('responses_sse', 'responses_json', 'chat_sse', 'chat_json')
  for (const mode of preferredModes) {
    if (supportedModes.includes(mode)) {
      return mode
    }
  }
  return supportedModes[0] ?? 'responses_sse'
}

function testPathFromEndpointMode(mode: AccountSupportedEndpointMode): string {
  return mode === 'chat_json' || mode === 'chat_sse'
    ? gatewayChatCompletionsPath
    : gatewayTestPath
}

export function createOpenAIResponsesTestPayload(model: string, prompt: string, isOAuth: boolean, clientCompatibility: AccountClientCompatibility, stream: boolean): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    model,
    input: [
      {
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: prompt
          }
        ]
      }
    ],
    instructions: defaultOpenAITestInstructions,
    stream
  }
  if (isOAuth) {
    payload.max_output_tokens = 1
    payload.store = false
  }
  if (clientCompatibility === 'codex_responses') {
    payload.stream = true
    payload.store = false
    payload.include = ['reasoning.encrypted_content']
  }
  return payload
}

export function createOpenAIChatCompletionsTestPayload(model: string, prompt: string, stream: boolean): Record<string, unknown> {
  return {
    model,
    messages: [
      {
        role: 'user',
        content: prompt
      }
    ],
    max_tokens: 1,
    stream
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
