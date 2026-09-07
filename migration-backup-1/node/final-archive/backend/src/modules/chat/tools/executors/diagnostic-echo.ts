import type { ChatInternalToolDefinition } from '../contracts.js'

const diagnosticEchoInputSchema = {
  type: 'object',
  properties: {
    text: { type: 'string', minLength: 1, maxLength: 1_024 }
  },
  required: ['text'],
  additionalProperties: false
} as const

export function createDiagnosticEchoTool(): ChatInternalToolDefinition {
  return {
    id: 'diagnostic.echo',
    version: '1.0.0',
    modelName: 'diagnostic_echo',
    title: '诊断回显',
    description: '仅在开发和测试环境回显一段有界文本，用于验证内部工具调用链。',
    inputSchema: diagnosticEchoInputSchema,
    executionKind: 'inline',
    executionOwner: 'application',
    limits: {
      maxArgumentBytes: 4 * 1024,
      maxResultBytes: 4 * 1024,
      timeoutMs: 1_000
    },
    availability: {
      environments: ['development', 'test'],
      requiresInternalToolsEnabled: true
    },
    duplicatePolicy: 'reuse_exact',
    execute: async (input) => {
      const echoedText = String(input.text)
      const publicResult = { echoedText }
      return { modelOutput: JSON.stringify(publicResult), publicResult }
    },
    projectResult: (result) => result.publicResult
  }
}
