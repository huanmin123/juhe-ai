import assert from 'node:assert/strict'

import { createDiagnosticEchoTool } from '../../modules/chat/tools/executors/diagnostic-echo.js'
import { ChatInternalToolOrchestrator } from '../../modules/chat/tools/orchestrator.js'
import {
  buildChatToolContinuation,
  compileChatInternalTools,
  type ChatToolProtocol
} from '../../modules/chat/tools/protocol.js'
import { ChatInternalToolRegistry } from '../../modules/chat/tools/registry.js'

function registry(): ChatInternalToolRegistry {
  const value = new ChatInternalToolRegistry({ environment: 'test', internalToolsEnabled: true })
  value.register(createDiagnosticEchoTool())
  return value
}

for (const protocol of ['responses', 'chat_completions'] as const satisfies readonly ChatToolProtocol[]) {
  const currentRegistry = registry()
  const declarations = compileChatInternalTools(protocol, currentRegistry.resolve({ functionCalling: true }))
  assert.equal(declarations.length, 1)
  assert.equal(declarations[0]?.type, 'function')
  if (protocol === 'responses') {
    assert.equal((declarations[0] as { name?: string }).name, 'diagnostic_echo')
    assert.equal(
      (declarations[0] as { strict?: boolean }).strict,
      false,
      '上游函数声明必须使用兼容模式；严格参数校验由站内 AJV 执行'
    )
  } else {
    assert.equal((declarations[0] as { function?: { name?: string } }).function?.name, 'diagnostic_echo')
    assert.equal(
      (declarations[0] as { function?: { strict?: boolean } }).function?.strict,
      false,
      'Chat Completions 上游函数声明也必须使用兼容模式'
    )
  }

  const events: string[] = []
  const modelRequests: Array<{ round: number; continuation: readonly unknown[] }> = []
  const orchestrator = new ChatInternalToolOrchestrator({
    registry: currentRegistry,
    tools: currentRegistry.resolve({ functionCalling: true }),
    context: {
      environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1',
      turnId: 'turn-1', assistantMessageId: 'assistant-1', signal: new AbortController().signal
    },
    limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 },
    publish: (event) => events.push(`${event.status}:${event.callId}:${event.toolName}`)
  })

  const final = await orchestrator.run({
    protocol,
    invokeModel: async (request) => {
      modelRequests.push({ round: request.round, continuation: request.continuation })
      if (request.round === 1) {
        return {
          content: '',
          finishReason: 'tool_calls',
          continuationItems: protocol === 'responses'
            ? [{ type: 'reasoning', id: 'reasoning-1', summary: [] }, { type: 'function_call', call_id: 'call-1', name: 'diagnostic_echo', arguments: '{"text":"demo"}' }]
            : [{ role: 'assistant', content: null, tool_calls: [{ id: 'call-1', type: 'function', function: { name: 'diagnostic_echo', arguments: '{"text":"demo"}' } }] }],
          toolCalls: [{ callId: 'call-1', toolName: 'diagnostic_echo', argumentsJson: '{"text":"demo"}', sourceOrder: 0 }]
        }
      }
      return { content: '工具返回 demo', finishReason: 'stop', continuationItems: [], toolCalls: [] }
    }
  })

  assert.equal(final.content, '工具返回 demo')
  assert.equal(final.modelRounds, 2)
  assert.equal(final.toolCalls, 1)
  assert.deepEqual(events, ['started:call-1:diagnostic_echo', 'completed:call-1:diagnostic_echo'])
  assert.equal(modelRequests.length, 2)
  assert.equal(modelRequests[1]?.continuation.length, protocol === 'responses' ? 3 : 2)

  const continuation = modelRequests[1]!.continuation
  if (protocol === 'responses') {
    assert.deepEqual(continuation[0], { type: 'reasoning', id: 'reasoning-1', summary: [] }, 'Responses reasoning item 必须随工具往返保留')
    assert.deepEqual(continuation[2], {
      type: 'function_call_output', call_id: 'call-1', output: '{"echoedText":"demo"}'
    })
  } else {
    assert.deepEqual(continuation[1], { role: 'tool', tool_call_id: 'call-1', content: '{"echoedText":"demo"}' })
  }
}

for (const protocol of ['responses', 'chat_completions'] as const satisfies readonly ChatToolProtocol[]) {
  const modelRequests: Array<{ round: number; continuation: readonly unknown[] }> = []
  const currentRegistry = registry()
  const result = await new ChatInternalToolOrchestrator({
    registry: currentRegistry,
    tools: currentRegistry.resolve({ functionCalling: true }),
    context: {
      environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-multi-tool',
      turnId: 'turn-multi-tool', assistantMessageId: 'assistant-multi-tool', signal: new AbortController().signal
    },
    limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 }
  }).run({
    protocol,
    invokeModel: async (request) => {
      modelRequests.push({ round: request.round, continuation: request.continuation })
      if (request.round <= 2) {
        const callId = `call-${request.round}`
        return {
          content: '',
          finishReason: 'tool_calls',
          continuationItems: protocol === 'responses'
            ? [{ type: 'function_call', call_id: callId, name: 'diagnostic_echo', arguments: `{"text":"round-${request.round}"}` }]
            : [{ role: 'assistant', content: null, tool_calls: [{ id: callId, type: 'function', function: { name: 'diagnostic_echo', arguments: `{"text":"round-${request.round}"}` } }] }],
          toolCalls: [{ callId, toolName: 'diagnostic_echo', argumentsJson: `{"text":"round-${request.round}"}`, sourceOrder: 0 }]
        }
      }
      return { content: '两轮工具完成', finishReason: 'stop', continuationItems: [], toolCalls: [] }
    }
  })

  assert.equal(result.content, '两轮工具完成')
  assert.equal(modelRequests.length, 3)
  assert.equal(modelRequests[1]?.continuation.length, 2, `${protocol} 第二轮必须包含第一次调用和结果`)
  assert.equal(modelRequests[2]?.continuation.length, 4, `${protocol} 第三轮必须同时包含前两次调用和结果`)
  const serialized = JSON.stringify(modelRequests[2]?.continuation)
  assert.match(serialized, /call-1/u, `${protocol} 第三轮不得丢失第一次工具调用`)
  assert.match(serialized, /round-1/u, `${protocol} 第三轮不得丢失第一次工具结果`)
  assert.match(serialized, /call-2/u, `${protocol} 第三轮必须保留第二次工具调用`)
  assert.match(serialized, /round-2/u, `${protocol} 第三轮必须保留第二次工具结果`)
}

const sequentialEvents: string[] = []
const duplicateRegistry = registry()
let modelRound = 0
const duplicateResult = await new ChatInternalToolOrchestrator({
  registry: duplicateRegistry,
  tools: duplicateRegistry.resolve({ functionCalling: true }),
  context: {
    environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1',
    turnId: 'turn-2', assistantMessageId: 'assistant-2', signal: new AbortController().signal
  },
  limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 },
  publish: (event) => sequentialEvents.push(`${event.status}:${event.callId}:${event.reused === true}`)
}).run({
  protocol: 'responses',
  invokeModel: async () => {
    modelRound += 1
    return modelRound === 1
      ? {
          content: '', finishReason: 'tool_calls', continuationItems: [],
          toolCalls: [
            { callId: 'call-a', toolName: 'diagnostic_echo', argumentsJson: '{"text":"same"}', sourceOrder: 2 },
            { callId: 'call-b', toolName: 'diagnostic_echo', argumentsJson: '{"text":"same"}', sourceOrder: 1 }
          ]
        }
      : { content: 'done', finishReason: 'stop', continuationItems: [], toolCalls: [] }
  }
})
assert.equal(duplicateResult.toolCalls, 2)
assert.deepEqual(sequentialEvents, [
  'started:call-b:false', 'completed:call-b:false',
  'started:call-a:false', 'completed:call-a:true'
], '调用必须按 sourceOrder 串行，规范化参数完全相同的第二次调用必须复用结果')

const correctionEvents: string[] = []
const correctionContinuations: string[] = []
const correctionRegistry = registry()
const correctionResult = await new ChatInternalToolOrchestrator({
  registry: correctionRegistry,
  tools: correctionRegistry.resolve({ functionCalling: true }),
  context: {
    environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-correction',
    turnId: 'turn-correction', assistantMessageId: 'assistant-correction', signal: new AbortController().signal
  },
  limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 },
  publish: (event) => correctionEvents.push(`${event.status}:${event.callId}:${event.errorCode ?? ''}`)
}).run({
  protocol: 'responses',
  invokeModel: async (request) => {
    correctionContinuations.push(JSON.stringify(request.continuation))
    return request.round === 1
      ? {
          content: '', finishReason: 'tool_calls',
          continuationItems: [{ type: 'function_call', call_id: 'bad-args', name: 'diagnostic_echo', arguments: '{}' }],
          toolCalls: [{ callId: 'bad-args', toolName: 'diagnostic_echo', argumentsJson: '{}', sourceOrder: 0 }]
        }
      : { content: '工具参数无效，已向用户说明。', finishReason: 'stop', continuationItems: [], toolCalls: [] }
  }
})
assert.equal(correctionResult.content, '工具参数无效，已向用户说明。')
assert.deepEqual(correctionEvents, ['started:bad-args:', 'failed:bad-args:tool_arguments_invalid'])
assert.match(correctionContinuations[1] ?? '', /tool_arguments_invalid/u, '首次可修正工具错误必须作为受控结果回灌模型')

const imageFailureRegistry = new ChatInternalToolRegistry({ environment: 'test', internalToolsEnabled: true })
imageFailureRegistry.register({
  id: 'image.failure.fixture', version: '1.0.0', modelName: 'generate_image', title: '图片失败 fixture', description: '测试结构化图片错误',
  inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  executionKind: 'network_adapter', executionOwner: 'application',
  limits: { maxArgumentBytes: 1024, maxResultBytes: 1024, timeoutMs: 1000 },
  availability: {}, duplicatePolicy: 'reuse_exact',
  execute: async () => { throw Object.assign(new Error('raw upstream body: image capability disabled; api_key=sk-tool-secret-value'), { code: 'image_generation_not_enabled' }) },
  projectResult: () => undefined
})
const imageFailureContinuations: string[] = []
const imageFailureEvents: Array<{ status: string; errorCode?: string; errorMessage?: string }> = []
const reportedImageFailures: Array<{ errorCode: string; errorMessage: string }> = []
const imageFailureResult = await new ChatInternalToolOrchestrator({
  registry: imageFailureRegistry,
  tools: imageFailureRegistry.resolve({ functionCalling: true }),
  context: {
    environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-image-failure',
    turnId: 'turn-image-failure', assistantMessageId: 'assistant-image-failure', signal: new AbortController().signal
  },
  limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 },
  publish: (event) => imageFailureEvents.push(event),
  reportError: (_error, failure) => reportedImageFailures.push(failure)
}).run({
  protocol: 'responses',
  invokeModel: async (request) => {
    imageFailureContinuations.push(JSON.stringify(request.continuation))
    return request.round === 1
      ? {
          content: '', finishReason: 'tool_calls',
          continuationItems: [{ type: 'function_call', call_id: 'image-failed', name: 'generate_image', arguments: '{}' }],
          toolCalls: [{ callId: 'image-failed', toolName: 'generate_image', argumentsJson: '{}', sourceOrder: 0 }]
        }
      : { content: '图片上游未开通，已向用户说明。', finishReason: 'stop', continuationItems: [], toolCalls: [] }
  }
})
assert.equal(imageFailureResult.content, '图片上游未开通，已向用户说明。')
assert.match(imageFailureContinuations[1] ?? '', /image_generation_not_enabled/u, '结构化图片错误码必须回灌模型')
assert.match(imageFailureContinuations[1] ?? '', /可用上游分组未开通图片生成功能/u, '模型必须收到可操作的安全图片失败原因')
assert.match(imageFailureContinuations[1] ?? '', /raw upstream body: image capability disabled/u, '工具回灌必须保留脱敏后的真实错误详情')
assert.doesNotMatch(imageFailureContinuations[1] ?? '', /sk-tool-secret-value/u, '工具回灌不得泄露上游凭据')
assert.match(imageFailureEvents.find((event) => event.status === 'failed')?.errorMessage ?? '', /raw upstream body: image capability disabled/u, '工具失败事件必须携带前端可见诊断详情')
assert.doesNotMatch(imageFailureEvents.find((event) => event.status === 'failed')?.errorMessage ?? '', /sk-tool-secret-value/u, '工具失败事件不得泄露凭据')
assert.equal(reportedImageFailures.length, 1, '每次内部工具失败必须进入服务端日志回调')

const repeatedFailureRegistry = registry()
await assert.rejects(new ChatInternalToolOrchestrator({
  registry: repeatedFailureRegistry,
  tools: repeatedFailureRegistry.resolve({ functionCalling: true }),
  context: {
    environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-repeat-failure',
    turnId: 'turn-repeat-failure', assistantMessageId: 'assistant-repeat-failure', signal: new AbortController().signal
  },
  limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 }
}).run({
  protocol: 'responses',
  invokeModel: async (request) => ({
    content: '', finishReason: 'tool_calls',
    continuationItems: [{ type: 'function_call', call_id: `bad-${request.round}`, name: 'diagnostic_echo', arguments: '{}' }],
    toolCalls: [{ callId: `bad-${request.round}`, toolName: 'diagnostic_echo', argumentsJson: '{}', sourceOrder: 0 }]
  })
}), /工具参数无效/u, '同一轮工具错误最多允许一次模型修正，第二次失败必须终止')

const limitedRegistry = registry()
const limited = new ChatInternalToolOrchestrator({
  registry: limitedRegistry,
  tools: limitedRegistry.resolve({ functionCalling: true }),
  context: {
    environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1',
    turnId: 'turn-limit', assistantMessageId: 'assistant-limit', signal: new AbortController().signal
  },
  limits: { maxModelRounds: 1, maxToolCalls: 8, maxImageCalls: 2 }
})
await assert.rejects(limited.run({
  protocol: 'responses',
  invokeModel: async () => ({
    content: '', finishReason: 'tool_calls', continuationItems: [],
    toolCalls: [{ callId: 'loop', toolName: 'diagnostic_echo', argumentsJson: '{"text":"again"}', sourceOrder: 0 }]
  })
}), /模型请求轮次超过 1|工具循环/, '工具循环必须有模型轮次上限')

const abortController = new AbortController()
abortController.abort()
const abortedRegistry = registry()
const aborted = new ChatInternalToolOrchestrator({
  registry: abortedRegistry,
  tools: abortedRegistry.resolve({ functionCalling: true }),
  context: {
    environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1',
    turnId: 'turn-abort', assistantMessageId: 'assistant-abort', signal: abortController.signal
  },
  limits: { maxModelRounds: 4, maxToolCalls: 8, maxImageCalls: 2 }
})
await assert.rejects(aborted.run({
  protocol: 'chat_completions',
  invokeModel: async () => ({ content: 'should-not-run', continuationItems: [], toolCalls: [] })
}), /aborted|取消/iu, '已取消请求不得进入模型调用')

assert.deepEqual(buildChatToolContinuation('responses', [{ type: 'function_call', call_id: 'x', name: 'diagnostic_echo', arguments: '{}' }], [{ callId: 'x', toolName: 'diagnostic_echo', modelOutput: '{}', reused: false }]), [
  { type: 'function_call', call_id: 'x', name: 'diagnostic_echo', arguments: '{}' },
  { type: 'function_call_output', call_id: 'x', output: '{}' }
])

console.log('AI 问答内部工具 Orchestrator 回归通过')
