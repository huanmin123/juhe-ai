import type { DistributionProbeDefinition, LongContextProbeDefinition } from './model-checks.probes.js'
import type { ModelCheckProbeProtocol } from './model-checks.profiles.js'
import type { AccountModelMappingSourceEndpointFamily, AccountModelMappingUpstreamEndpointFamily } from '../../domain/types.js'
import { estimateTokenCountFromText } from '../gateway/protocols/openai-v1/stream-events.js'

export interface ModelCheckProbeRequest {
  path: string
  body: Record<string, unknown>
  responseProtocol: ModelCheckProbeProtocol
  requestModel?: string
  expectedModel: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily?: AccountModelMappingUpstreamEndpointFamily
}

export function createModelCheckProbeRequest(
  protocol: ModelCheckProbeProtocol,
  model: string,
  prompt: string,
  options: { maxOutputTokens: number; stream: boolean; temperature?: number }
): ModelCheckProbeRequest {
  const temperature = options.temperature ?? 0
  if (protocol === 'openai_responses') {
    return {
      path: '/v1/responses',
      responseProtocol: protocol,
      expectedModel: model,
      body: createResponsesPayload(model, prompt, { ...options, temperature })
    }
  }
  if (protocol === 'openai_chat') {
    return {
      path: '/v1/chat/completions',
      responseProtocol: protocol,
      expectedModel: model,
      body: createChatCompletionsPayload(model, prompt, { ...options, temperature })
    }
  }
  if (protocol === 'anthropic_messages') {
    return {
      path: '/v1/messages',
      responseProtocol: protocol,
      expectedModel: model,
      body: createAnthropicMessagesPayload(model, prompt, { ...options, temperature })
    }
  }
  return {
    path: geminiGenerateContentPath(model, options.stream),
    responseProtocol: protocol,
    expectedModel: model,
    body: createGeminiGenerateContentPayload(prompt, { ...options, temperature })
  }
}

export function createModelCheckDistributionProbeRequest(
  protocol: ModelCheckProbeProtocol,
  model: string,
  definition: DistributionProbeDefinition
): ModelCheckProbeRequest {
  return createModelCheckProbeRequest(protocol, model, definition.prompt, {
    maxOutputTokens: definition.maxOutputTokens,
    stream: false,
    temperature: 0.2
  })
}

export function createModelCheckStructuredOutputRequest(protocol: ModelCheckProbeProtocol, model: string): ModelCheckProbeRequest {
  if (protocol === 'openai_responses') {
    return {
      path: '/v1/responses',
      responseProtocol: protocol,
      expectedModel: model,
      body: createStructuredOutputPayload(model)
    }
  }
  if (protocol === 'openai_chat') {
    return {
      path: '/v1/chat/completions',
      responseProtocol: protocol,
      expectedModel: model,
      body: {
        ...createChatCompletionsPayload(model, 'Return {"status":"ok","value":7} as JSON.', { maxOutputTokens: 64, stream: false, temperature: 0 }),
        response_format: {
          type: 'json_schema',
          json_schema: {
            name: 'model_check_structured_output',
            strict: true,
            schema: structuredOutputJsonSchema()
          }
        }
      }
    }
  }
  if (protocol === 'anthropic_messages') {
    return {
      path: '/v1/messages',
      responseProtocol: protocol,
      expectedModel: model,
      body: createAnthropicMessagesPayload(model, 'Return only this JSON object: {"status":"ok","value":7}', {
        maxOutputTokens: 64,
        stream: false,
        temperature: 0
      })
    }
  }
  return {
    path: geminiGenerateContentPath(model, false),
    responseProtocol: protocol,
    expectedModel: model,
    body: {
      ...createGeminiGenerateContentPayload('Return {"status":"ok","value":7} as JSON.', {
        maxOutputTokens: 64,
        stream: false,
        temperature: 0
      }),
      generationConfig: {
        temperature: 0,
        maxOutputTokens: 128,
        responseMimeType: 'application/json',
        responseSchema: {
          type: 'OBJECT',
          properties: {
            status: { type: 'STRING', enum: ['ok'] },
            value: { type: 'INTEGER' }
          },
          required: ['status', 'value']
        }
      }
    }
  }
}

export function createModelCheckToolCallingRequest(protocol: ModelCheckProbeProtocol, model: string): ModelCheckProbeRequest {
  if (protocol === 'openai_responses') {
    return {
      path: '/v1/responses',
      responseProtocol: protocol,
      expectedModel: model,
      body: createToolCallingPayload(model)
    }
  }
  if (protocol === 'openai_chat') {
    return {
      path: '/v1/chat/completions',
      responseProtocol: protocol,
      expectedModel: model,
      body: {
        ...createChatCompletionsPayload(model, 'Call the provided function with code "ok" and count 1.', {
          maxOutputTokens: 64,
          stream: false,
          temperature: 0
        }),
        tools: [
          {
            type: 'function',
            function: {
              name: 'record_model_check',
              description: 'Record a model check marker.',
              parameters: toolParametersJsonSchema()
            }
          }
        ],
        tool_choice: {
          type: 'function',
          function: { name: 'record_model_check' }
        }
      }
    }
  }
  if (protocol === 'anthropic_messages') {
    return {
      path: '/v1/messages',
      responseProtocol: protocol,
      expectedModel: model,
      body: {
        ...createAnthropicMessagesPayload(model, 'Call the provided tool with code "ok" and count 1.', {
          maxOutputTokens: 64,
          stream: false,
          temperature: 0
        }),
        tools: [
          {
            name: 'record_model_check',
            description: 'Record a model check marker.',
            input_schema: toolParametersJsonSchema()
          }
        ],
        tool_choice: {
          type: 'tool',
          name: 'record_model_check'
        }
      }
    }
  }
  return {
    path: geminiGenerateContentPath(model, false),
    responseProtocol: protocol,
    expectedModel: model,
    body: {
      ...createGeminiGenerateContentPayload('Call the provided function with code "ok" and count 1.', {
        maxOutputTokens: 64,
        stream: false,
        temperature: 0
      }),
      tools: [
        {
          functionDeclarations: [
            {
              name: 'record_model_check',
              description: 'Record a model check marker.',
              parameters: {
                type: 'OBJECT',
                properties: {
                  code: { type: 'STRING' },
                  count: { type: 'INTEGER' }
                },
                required: ['code', 'count']
              }
            }
          ]
        }
      ],
      toolConfig: {
        functionCallingConfig: {
          mode: 'ANY',
          allowedFunctionNames: ['record_model_check']
        }
      }
    }
  }
}

export function createModelCheckLongContextRequest(
  protocol: ModelCheckProbeProtocol,
  model: string,
  definition: LongContextProbeDefinition
): ModelCheckProbeRequest {
  if (protocol === 'openai_responses') {
    return {
      path: '/v1/responses',
      responseProtocol: protocol,
      expectedModel: model,
      body: createLongContextPayload(model, definition)
    }
  }
  const prompt = buildLongContextPrompt(definition)
  return createModelCheckProbeRequest(protocol, model, prompt, {
    maxOutputTokens: definition.maxOutputTokens,
    stream: false,
    temperature: 0
  })
}

export function createResponsesPayload(
  model: string,
  prompt: string,
  options: { maxOutputTokens: number; stream: boolean; temperature?: number }
): Record<string, unknown> {
  return {
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
    instructions: 'You are a model capability checker. Follow the requested output exactly.',
    max_output_tokens: options.maxOutputTokens,
    stream: options.stream,
    store: false,
    temperature: options.temperature ?? 0
  }
}

export function createDistributionProbePayload(model: string, definition: DistributionProbeDefinition): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, definition.prompt, { maxOutputTokens: definition.maxOutputTokens, stream: false }),
    temperature: 0.2
  }
}

export function createChatCompletionsPayload(
  model: string,
  prompt: string,
  options: { maxOutputTokens: number; stream: boolean; temperature?: number }
): Record<string, unknown> {
  const maxTokens = Math.max(options.maxOutputTokens, 64)
  return {
    model,
    messages: [
      {
        role: 'system',
        content: 'You are a model capability checker. Follow the requested output exactly.'
      },
      {
        role: 'user',
        content: prompt
      }
    ],
    max_tokens: maxTokens,
    stream: options.stream,
    temperature: options.temperature ?? 0
  }
}

export function createAnthropicMessagesPayload(
  model: string,
  prompt: string,
  options: { maxOutputTokens: number; stream: boolean; temperature?: number }
): Record<string, unknown> {
  void options.temperature
  return {
    model,
    system: 'You are a model capability checker. Follow the requested output exactly.',
    messages: [
      {
        role: 'user',
        content: prompt
      }
    ],
    max_tokens: options.maxOutputTokens,
    stream: options.stream
  }
}

export function createGeminiGenerateContentPayload(
  prompt: string,
  options: { maxOutputTokens: number; stream: boolean; temperature?: number }
): Record<string, unknown> {
  void options.stream
  const maxOutputTokens = Math.max(options.maxOutputTokens, 128)
  return {
    systemInstruction: {
      parts: [
        {
          text: 'You are a model capability checker. Follow the requested output exactly.'
        }
      ]
    },
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
      temperature: options.temperature ?? 0,
      maxOutputTokens
    }
  }
}

export function createStructuredOutputPayload(model: string): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, 'Return {"status":"ok","value":7} as JSON.', { maxOutputTokens: 64, stream: false }),
    text: {
      format: {
        type: 'json_schema',
        name: 'model_check_structured_output',
        schema: {
          type: 'object',
          additionalProperties: false,
          properties: {
            status: { type: 'string', enum: ['ok'] },
            value: { type: 'integer' }
          },
          required: ['status', 'value']
        },
        strict: true
      }
    }
  }
}

export function createToolCallingPayload(model: string): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, 'Call the provided function with code "ok" and count 1.', { maxOutputTokens: 64, stream: false }),
    tools: [
      {
        type: 'function',
        name: 'record_model_check',
        description: 'Record a model check marker.',
        parameters: {
          type: 'object',
          additionalProperties: false,
          properties: {
            code: { type: 'string' },
            count: { type: 'integer' }
          },
          required: ['code', 'count']
        }
      }
    ],
    tool_choice: {
      type: 'function',
      name: 'record_model_check'
    }
  }
}

export function createLongContextPayload(model: string, definition: LongContextProbeDefinition): Record<string, unknown> {
  return createResponsesPayload(model, buildLongContextPrompt(definition), {
    maxOutputTokens: definition.maxOutputTokens,
    stream: false
  })
}

export function buildLongContextPrompt(definition: LongContextProbeDefinition): string {
  const targetInputTokens = definition.targetInputTokens
  const markerPlacementTokens = Math.max(512, Math.floor(targetInputTokens * definition.markerTokenRatio))
  const answerRule = `回答规则：只输出 key 为 ${definition.key} 的隐藏标记本身，不要解释，不要输出其他文字。`
  const markerLine = `关键记录 ${definition.key}: 本次检测的隐藏标记是 ${definition.marker}，只在回答中输出这个标记。`
  const lines = [
    `阅读以下上下文，找到 key 为 ${definition.key} 的隐藏标记。本窗口目标输入长度为 ${targetInputTokens} tokens。`,
    '每一段都是干扰上下文，只有关键记录行包含最终答案。'
  ]
  while (estimatedPromptTokens([...lines, markerLine, answerRule]) < markerPlacementTokens - 128) {
    lines.push(longContextFillerLine(definition, lines.length, '前置'))
  }
  lines.push(markerLine)
  while (estimatedPromptTokens([...lines, answerRule]) < targetInputTokens - 128) {
    lines.push(longContextFillerLine(definition, lines.length, '后置'))
  }
  lines.push(answerRule)
  let prompt = lines.join('\n')
  const deficit = targetInputTokens - estimateTokenCountFromText(prompt)
  if (deficit > 0) {
    prompt += '测'.repeat(deficit)
  }
  return prompt
}

function longContextFillerLine(definition: LongContextProbeDefinition, index: number, phase: '前置' | '后置'): string {
  const ordinal = String(index + 1).padStart(5, '0')
  const decoy = index % 89 === 17 ? ' 旁注：这一行是干扰记录，不是最终答案。' : ''
  return `段落 ${definition.key}-${phase}-${ordinal}: 这是一段用于模型检测的普通上下文，包含编号、中文文本、重复业务术语、干扰词和无关约束，但不包含最终答案。${decoy}`
}

function estimatedPromptTokens(lines: string[]): number {
  return estimateTokenCountFromText(lines.join('\n'))
}

function structuredOutputJsonSchema(): Record<string, unknown> {
  return {
    type: 'object',
    additionalProperties: false,
    properties: {
      status: { type: 'string', enum: ['ok'] },
      value: { type: 'integer' }
    },
    required: ['status', 'value']
  }
}

function toolParametersJsonSchema(): Record<string, unknown> {
  return {
    type: 'object',
    additionalProperties: false,
    properties: {
      code: { type: 'string' },
      count: { type: 'integer' }
    },
    required: ['code', 'count']
  }
}

function geminiGenerateContentPath(model: string, stream: boolean): string {
  const action = stream ? 'streamGenerateContent' : 'generateContent'
  return `/v1beta/models/${encodeURIComponent(model)}:${action}${stream ? '?alt=sse' : ''}`
}
