import type { DistributionProbeDefinition } from './model-checks.probes.js'
import type { ModelCheckProbeProtocol } from './model-checks.profiles.js'

export interface ModelCheckProbeRequest {
  path: string
  body: Record<string, unknown>
  responseProtocol: ModelCheckProbeProtocol
  expectedModel: string
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

export function createModelCheckLongContextRequest(protocol: ModelCheckProbeProtocol, model: string): ModelCheckProbeRequest {
  if (protocol === 'openai_responses') {
    return {
      path: '/v1/responses',
      responseProtocol: protocol,
      expectedModel: model,
      body: createLongContextPayload(model)
    }
  }
  const marker = 'NEEDLE-7482-ORCHID'
  const prompt = longContextPrompt(marker)
  return createModelCheckProbeRequest(protocol, model, prompt, {
    maxOutputTokens: 40,
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
    stream: options.stream,
    temperature: options.temperature ?? 0
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

export function createLongContextPayload(model: string): Record<string, unknown> {
  const marker = 'NEEDLE-7482-ORCHID'
  return createResponsesPayload(model, longContextPrompt(marker), {
    maxOutputTokens: 40,
    stream: false
  })
}

function longContextPrompt(marker: string): string {
  const filler = Array.from({ length: 240 }, (_, index) => {
    const ordinal = String(index + 1).padStart(2, '0')
    return `段落 ${ordinal}: 这是一段用于模型检测的普通上下文，包含编号、中文文本和少量干扰词，但不包含最终答案。`
  })
  filler.splice(173, 0, `关键记录: 本次检测的隐藏标记是 ${marker}，只在回答中输出这个标记。`)
  return `阅读以下上下文，找到隐藏标记。只输出标记本身，不要解释。\n\n${filler.join('\n')}`
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
