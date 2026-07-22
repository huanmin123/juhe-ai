import assert from 'node:assert/strict'

import { createDiagnosticEchoTool } from '../../modules/chat/tools/executors/diagnostic-echo.js'
import { ChatInternalToolRegistry } from '../../modules/chat/tools/registry.js'
import type { ChatInternalToolDefinition } from '../../modules/chat/tools/contracts.js'

const enabledRegistry = new ChatInternalToolRegistry({
  environment: 'test',
  internalToolsEnabled: true
})
enabledRegistry.register(createDiagnosticEchoTool())

const visible = enabledRegistry.resolve({ functionCalling: true })
assert.deepEqual(visible.map((tool) => tool.modelName), ['diagnostic_echo'])
assert.equal(visible[0]?.executionKind, 'inline')
assert.equal(visible[0]?.executionOwner, 'application')

const parsed = enabledRegistry.validateArguments('diagnostic_echo', '{"text":"协议回显"}')
assert.deepEqual(parsed, { text: '协议回显' })
assert.throws(
  () => enabledRegistry.validateArguments('diagnostic_echo', '{"text":"ok","unexpected":true}'),
  /工具参数无效/,
  'Schema 必须拒绝未知字段'
)
assert.throws(
  () => enabledRegistry.validateArguments('diagnostic_echo', '{"text":'),
  /工具参数不是有效 JSON/,
  '截断 JSON 必须返回稳定参数错误'
)
assert.throws(
  () => enabledRegistry.validateArguments('not_registered', '{}'),
  /tool_not_available/,
  '模型不得调用未注册工具'
)
assert.throws(
  () => enabledRegistry.register(createDiagnosticEchoTool()),
  /重复注册/,
  '同一工具名称不得重复注册'
)

const disabledRegistry = new ChatInternalToolRegistry({
  environment: 'development',
  internalToolsEnabled: false
})
disabledRegistry.register(createDiagnosticEchoTool())
assert.deepEqual(disabledRegistry.resolve({ functionCalling: true }), [], '未显式启用时 Demo 不得暴露')
assert.deepEqual(enabledRegistry.resolve({ functionCalling: false }), [], '模型不支持 function calling 时不得暴露应用工具')

const productionRegistry = new ChatInternalToolRegistry({
  environment: 'production',
  internalToolsEnabled: true
})
productionRegistry.register(createDiagnosticEchoTool())
assert.deepEqual(productionRegistry.resolve({ functionCalling: true }), [], '生产环境必须忽略 Demo 开关')

await assert.rejects(
  enabledRegistry.execute({
    toolName: 'diagnostic_echo',
    argumentsJson: '{"text":"hello"}',
    context: {
      environment: 'test',
      ownerId: 'owner-1',
      conversationId: 'conversation-1',
      turnId: 'turn-1',
      assistantMessageId: 'assistant-1',
      signal: AbortSignal.abort()
    }
  }),
  /aborted|取消/iu,
  '已取消 signal 不得执行工具'
)

const result = await enabledRegistry.execute({
  toolName: 'diagnostic_echo',
  argumentsJson: '{"text":"hello"}',
  context: {
    environment: 'test',
    ownerId: 'owner-1',
    conversationId: 'conversation-1',
    turnId: 'turn-1',
    assistantMessageId: 'assistant-1',
    signal: new AbortController().signal
  }
})
assert.deepEqual(result.publicResult, { echoedText: 'hello' })
assert.equal(JSON.parse(result.modelOutput).echoedText, 'hello')
assert.equal(Buffer.byteLength(result.modelOutput, 'utf8') <= visible[0]!.limits.maxResultBytes, true)

let executorSignal: AbortSignal | undefined
const timeoutTool: ChatInternalToolDefinition = {
  id: 'diagnostic.timeout_signal',
  version: '1',
  modelName: 'diagnostic_timeout_signal',
  title: '超时信号测试',
  description: '验证 Registry 会把有界超时 signal 传给执行器。',
  inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  executionKind: 'inline',
  executionOwner: 'application',
  limits: { maxArgumentBytes: 128, maxResultBytes: 128, timeoutMs: 20 },
  availability: { environments: ['test'] },
  duplicatePolicy: 'allow_repeat',
  execute: async (_input, context) => {
    executorSignal = context.signal
    return await new Promise((_resolve, reject) => {
      context.signal.addEventListener('abort', () => reject(context.signal.reason), { once: true })
    })
  },
  projectResult: () => undefined
}
enabledRegistry.register(timeoutTool)
const parentController = new AbortController()
await assert.rejects(
  enabledRegistry.execute({
    toolName: timeoutTool.modelName,
    argumentsJson: '{}',
    context: {
      environment: 'test',
      ownerId: 'owner-1',
      conversationId: 'conversation-1',
      turnId: 'turn-1',
      assistantMessageId: 'assistant-1',
      signal: parentController.signal
    }
  }),
  /timeout|超时/iu,
  '工具超过 timeoutMs 必须终止并返回超时'
)
assert.notEqual(executorSignal, parentController.signal, '执行器必须收到合并父取消与超时的有界 signal')
assert.equal(executorSignal?.aborted, true, '工具超时后执行器 signal 必须处于已取消状态')

console.log('AI 问答内部工具 Registry 回归通过')
