import type { AccountClientCompatibility } from '../../domain/types.js'
import type { RecentOpenAIRequestShape } from '../../storage/repositories.js'

export const accountTestDefaultPrompt = '只输出 OK'
const defaultOpenAITestInstructions = 'You are ChatGPT, a helpful assistant.'
const gatewayTestPath = '/v1/responses'
const gatewayChatCompletionsPath = '/v1/chat/completions'
export const accountTestModelsPath = '/v1/models'

export type AccountTestRequestInput = {
  explicitModel?: string
  fallbackModel: string
  prompt: string
  isOAuth: boolean
  clientCompatibility: AccountClientCompatibility
  requestShape?: RecentOpenAIRequestShape
}

export type AccountTestRequest = {
  path: string
  body: Record<string, unknown>
  model: string
}

export function createOpenAITestRequest(input: AccountTestRequestInput): AccountTestRequest {
  const path = testPathFromRecentShape(input.requestShape, input.isOAuth, input.clientCompatibility)
  const model = stringValue(input.explicitModel) || input.fallbackModel
  return {
    path,
    body: path === gatewayChatCompletionsPath
      ? createOpenAIChatCompletionsTestPayload(model, input.prompt, input.requestShape?.stream ?? true)
      : createOpenAIResponsesTestPayload(model, input.prompt, input.isOAuth, input.clientCompatibility, input.requestShape?.stream ?? true),
    model
  }
}

export function testPathFromRecentShape(shape: RecentOpenAIRequestShape | undefined, isOAuth: boolean, clientCompatibility: AccountClientCompatibility): string {
  if (isOAuth) {
    return gatewayTestPath
  }
  if (clientCompatibility === 'codex_responses') {
    return gatewayTestPath
  }
  const endpoint = stringValue(shape?.endpoint).toLowerCase()
  if (endpoint.includes('/v1/chat/completions')) {
    return gatewayChatCompletionsPath
  }
  return gatewayTestPath
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
