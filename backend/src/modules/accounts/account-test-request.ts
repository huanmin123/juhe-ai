import type { AccountClientCompatibility } from '../../domain/types.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  defaultAnthropicEndpointModes,
  isAnthropicEndpointMode
} from '../../domain/anthropic-endpoint-modes.js'

export const accountTestDefaultPrompt = '只输出 OK'
const defaultOpenAITestInstructions = 'You are ChatGPT, a helpful assistant.'
const gatewayTestPath = '/v1/responses'
const gatewayChatCompletionsPath = '/v1/chat/completions'
const gatewayAnthropicMessagesPath = '/v1/messages'
const gatewayGeminiVersionPrefix = '/v1beta'
export const accountTestModelsPath = '/v1/models'

export type AccountTestRequestInput = {
  explicitModel?: string
  fallbackModel: string
  prompt: string
  isOAuth: boolean
  clientCompatibility: AccountClientCompatibility
  testEndpointMode: AccountSupportedEndpointMode
}

export type AccountTestRequest = {
  path: string
  body: Record<string, unknown>
  model: string
}

export function createOpenAITestRequest(input: AccountTestRequestInput): AccountTestRequest {
  const mode = input.testEndpointMode
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
  testEndpointMode?: AccountSupportedEndpointMode
}): AccountTestRequest {
  const supportedModes = input.supportedEndpointModes?.filter(isAnthropicEndpointMode)
  const modes = supportedModes?.length ? supportedModes : defaultAnthropicEndpointModes()
  const mode = input.testEndpointMode ?? preferredAnthropicTestEndpointMode(modes)
  const stream = mode === 'messages_sse'
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

export function createGeminiTestRequest(input: {
  explicitModel?: string
  fallbackModel: string
  prompt: string
  testEndpointMode: AccountSupportedEndpointMode
}): AccountTestRequest {
  const model = stringValue(input.explicitModel) || input.fallbackModel
  const stream = input.testEndpointMode === 'generate_content_sse'
  const method = stream ? 'streamGenerateContent' : 'generateContent'
  return {
    path: `${gatewayGeminiVersionPrefix}/${geminiModelPath(model)}:${method}${stream ? '?alt=sse' : ''}`,
    body: createGeminiGenerateContentTestPayload(input.prompt),
    model
  }
}

export function testPathFromEndpointMode(mode: AccountSupportedEndpointMode, model = 'test-model'): string {
  if (mode === 'chat_json' || mode === 'chat_sse') {
    return gatewayChatCompletionsPath
  }
  if (mode === 'messages_json' || mode === 'messages_sse') {
    return gatewayAnthropicMessagesPath
  }
  if (mode === 'generate_content_json') {
    return `${gatewayGeminiVersionPrefix}/${geminiModelPath(model)}:generateContent`
  }
  if (mode === 'generate_content_sse') {
    return `${gatewayGeminiVersionPrefix}/${geminiModelPath(model)}:streamGenerateContent?alt=sse`
  }
  return gatewayTestPath
}

function preferredAnthropicTestEndpointMode(modes: AccountSupportedEndpointMode[]): AccountSupportedEndpointMode {
  if (modes.includes('messages_sse')) return 'messages_sse'
  return 'messages_json'
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
  if (clientCompatibility === 'codex_responses' && stream) {
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

export function createGeminiGenerateContentTestPayload(prompt: string): Record<string, unknown> {
  return {
    contents: [
      {
        role: 'user',
        parts: [
          {
            text: prompt
          }
        ]
      }
    ],
    generationConfig: {
      maxOutputTokens: 1
    }
  }
}

function geminiModelPath(model: string): string {
  const normalized = stringValue(model).replace(/^models\//i, '') || 'gemini-pro'
  return `models/${encodeURIComponent(normalized)}`
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
