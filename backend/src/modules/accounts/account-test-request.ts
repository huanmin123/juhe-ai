import { randomBytes, randomUUID } from 'node:crypto'

import type { AccountClientCompatibility } from '../../domain/types.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import { normalizeOpenAICodexResponsesLiteBody } from '../gateway/adapters/gpt-codex/client-headers.js'
import {
  defaultAnthropicEndpointModes,
  isAnthropicEndpointMode
} from '../../domain/anthropic-endpoint-modes.js'

const accountTestOutputTokenLimit = 32
const defaultOpenAITestInstructions = 'You are ChatGPT, a helpful assistant.'
const gatewayTestPath = '/v1/responses'
const gatewayChatCompletionsPath = '/v1/chat/completions'
const gatewayImagesGenerationsPath = '/v1/images/generations'
const gatewayAnthropicMessagesPath = '/v1/messages'
const gatewayGeminiVersionPrefix = '/v1beta'
const gatewayGeminiInteractionsPath = '/v1beta/interactions'
const gatewayClientProfileHeader = 'x-juhe-client-profile'
const claudeCodeVersion = '2.1.201'
const claudeCodeBuildId = 'eb7'
const claudeCodeDeviceId = '7cfe24060ed291eb6ea9b7a6edf6947d14da82a0068470a6fc9cf8c147b252dc'
export const accountTestModelsPath = '/v1/models'
export const accountTestGeminiModelsPath = '/v1beta/models'
export const accountImageTestDefaultPrompt = 'Solid black.'

export type AccountTestOutputChallenge = {
  expectedOutput: string
  prompt: string
}

export function createAccountTestOutputChallenge(): AccountTestOutputChallenge {
  const expectedOutput = `OK:${randomBytes(16).toString('hex').toUpperCase()}`
  return {
    expectedOutput,
    prompt: [
      `你的回复必须且只能是：${expectedOutput}`,
      `除 ${expectedOutput} 外，不得输出任何字符；不得添加反引号、引号、空格、换行、解释或 Markdown。`
    ].join('\n')
  }
}

export function accountTestModelsPathForProtocol(protocolCode: string | undefined): string {
  return protocolCode === 'gemini' ? accountTestGeminiModelsPath : accountTestModelsPath
}

export function isAccountTestModelsPath(path: string | undefined): boolean {
  return path === accountTestModelsPath || path === accountTestGeminiModelsPath
}

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
  headers?: Record<string, string>
}

export function createOpenAITestRequest(input: AccountTestRequestInput): AccountTestRequest {
  const mode = input.testEndpointMode
  const path = testPathFromEndpointMode(mode)
  const stream = mode === 'chat_sse' || mode === 'responses_sse'
  const model = stringValue(input.explicitModel) || input.fallbackModel
  const body = path === gatewayChatCompletionsPath
    ? createOpenAIChatCompletionsTestPayload(model, input.prompt, stream)
    : createOpenAIResponsesTestPayload(model, input.prompt, input.isOAuth, input.clientCompatibility, stream)
  const codexHeaders = input.clientCompatibility === 'codex_responses' && stream && path === gatewayTestPath
    ? new Headers()
    : undefined
  if (codexHeaders) {
    normalizeOpenAICodexResponsesLiteBody(body, model, codexHeaders)
  }
  return {
    path,
    body,
    model,
    headers: codexHeaders ? Object.fromEntries(codexHeaders.entries()) : undefined
  }
}

export function createOpenAIImageGenerationTestRequest(input: {
  explicitModel?: string
  fallbackModel: string
}): AccountTestRequest {
  const model = stringValue(input.explicitModel) || input.fallbackModel
  return {
    path: gatewayImagesGenerationsPath,
    body: {
      model,
      prompt: accountImageTestDefaultPrompt,
      n: 1,
      size: '1024x1024',
      quality: 'low',
      output_format: 'webp',
      output_compression: 100
    },
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
  const sessionId = randomUUID()
  return {
    path: gatewayAnthropicMessagesPath,
    body: createAnthropicClaudeCodeAccountTestPayload(model, input.prompt, stream, sessionId),
    headers: {
      [gatewayClientProfileHeader]: 'claude_code',
      'x-claude-code-session-id': sessionId
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
  const interactions = input.testEndpointMode === 'interactions_json' || input.testEndpointMode === 'interactions_sse'
  const stream = input.testEndpointMode === 'generate_content_sse' || input.testEndpointMode === 'interactions_sse'
  if (interactions) {
    return {
      path: gatewayGeminiInteractionsPath,
      body: {
        model,
        input: input.prompt,
        stream
      },
      headers: stream ? { accept: 'text/event-stream' } : undefined,
      model
    }
  }
  const method = stream ? 'streamGenerateContent' : 'generateContent'
  return {
    path: `${gatewayGeminiVersionPrefix}/${geminiModelPath(model)}:${method}${stream ? '?alt=sse' : ''}`,
    body: createGeminiGenerateContentTestPayload(input.prompt),
    model
  }
}

export function testPathFromEndpointMode(mode: AccountSupportedEndpointMode, model = 'test-model'): string {
  if (mode === 'images_json') return gatewayImagesGenerationsPath
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
  if (mode === 'interactions_json') return gatewayGeminiInteractionsPath
  if (mode === 'interactions_sse') return gatewayGeminiInteractionsPath
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
    stream,
    max_output_tokens: accountTestOutputTokenLimit
  }
  if (isOAuth) {
    payload.store = false
  }
  if (clientCompatibility === 'codex_responses' && stream) {
    payload.stream = true
    payload.store = false
    payload.include = ['reasoning.encrypted_content']
    normalizeOpenAICodexResponsesLiteBody(payload, model)
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
    max_tokens: accountTestOutputTokenLimit,
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
      maxOutputTokens: accountTestOutputTokenLimit
    }
  }
}

function createAnthropicClaudeCodeAccountTestPayload(
  model: string,
  prompt: string,
  stream: boolean,
  sessionId: string
): Record<string, unknown> {
  return {
    model,
    messages: [
      {
        role: 'user',
        content: [
          {
            type: 'text',
            text: accountTestSystemReminder()
          },
          {
            type: 'text',
            text: `${prompt}\n`,
            cache_control: { type: 'ephemeral' }
          }
        ]
      }
    ],
    system: [
      {
        type: 'text',
        text: `x-anthropic-billing-header: cc_version=${claudeCodeVersion}.${claudeCodeBuildId}; cc_entrypoint=sdk-cli;`
      },
      {
        type: 'text',
        text: "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
        cache_control: { type: 'ephemeral' }
      },
      {
        type: 'text',
        text: `CWD: ${process.cwd()}\nDate: ${new Date().toISOString().slice(0, 10)}`
      }
    ],
    tools: [],
    max_tokens: 32000,
    thinking: { type: 'adaptive' },
    output_config: { effort: 'high' },
    metadata: {
      user_id: JSON.stringify({
        device_id: claudeCodeDeviceId,
        account_uuid: '',
        session_id: sessionId
      })
    },
    stream
  }
}

function accountTestSystemReminder(): string {
  return [
    '<system-reminder>',
    "As you answer the user's questions, you can use the following context:",
    '# currentDate',
    `Today's date is ${new Date().toISOString().slice(0, 10)}.`,
    '',
    '      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.',
    '</system-reminder>',
    '',
    ''
  ].join('\n')
}

function geminiModelPath(model: string): string {
  const normalized = stringValue(model).replace(/^models\//i, '') || 'gemini-pro'
  return `models/${encodeURIComponent(normalized)}`
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
