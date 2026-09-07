import type {
  ChatInternalToolDefinition,
  ChatToolExecutionOutput
} from './contracts.js'

export type ChatToolProtocol = 'responses' | 'chat_completions'

// 兼容 OpenAI 风格上游的可选参数 Schema；站内仍由 AJV 做严格校验。
const upstreamStrictFunctionSchema = false

export function compileChatInternalTools(
  protocol: ChatToolProtocol,
  tools: readonly ChatInternalToolDefinition[]
): Array<Record<string, unknown>> {
  return tools.map((tool) => protocol === 'responses'
    ? {
        type: 'function',
        name: tool.modelName,
        description: tool.description,
        parameters: tool.inputSchema,
        strict: upstreamStrictFunctionSchema
      }
    : {
        type: 'function',
        function: {
          name: tool.modelName,
          description: tool.description,
          parameters: tool.inputSchema,
          strict: upstreamStrictFunctionSchema
        }
      })
}

export function buildChatToolContinuation(
  protocol: ChatToolProtocol,
  continuationItems: readonly unknown[],
  outputs: readonly ChatToolExecutionOutput[]
): unknown[] {
  if (protocol === 'responses') {
    return [
      ...continuationItems,
      ...outputs.map((output) => ({
        type: 'function_call_output',
        call_id: output.callId,
        output: output.modelOutput
      }))
    ]
  }
  return [
    ...continuationItems,
    ...outputs.map((output) => ({
      role: 'tool',
      tool_call_id: output.callId,
      content: output.modelOutput
    }))
  ]
}
