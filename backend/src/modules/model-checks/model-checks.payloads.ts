import type { DistributionProbeDefinition } from './model-checks.probes.js'

export function createResponsesPayload(
  model: string,
  prompt: string,
  options: { maxOutputTokens: number; stream: boolean }
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
    temperature: 0
  }
}

export function createDistributionProbePayload(model: string, definition: DistributionProbeDefinition): Record<string, unknown> {
  return {
    ...createResponsesPayload(model, definition.prompt, { maxOutputTokens: definition.maxOutputTokens, stream: false }),
    temperature: 0.2
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
  const filler = Array.from({ length: 240 }, (_, index) => {
    const ordinal = String(index + 1).padStart(2, '0')
    return `段落 ${ordinal}: 这是一段用于模型检测的普通上下文，包含编号、中文文本和少量干扰词，但不包含最终答案。`
  })
  filler.splice(173, 0, `关键记录: 本次检测的隐藏标记是 ${marker}，只在回答中输出这个标记。`)
  return createResponsesPayload(model, `阅读以下上下文，找到隐藏标记。只输出标记本身，不要解释。\n\n${filler.join('\n')}`, {
    maxOutputTokens: 40,
    stream: false
  })
}
