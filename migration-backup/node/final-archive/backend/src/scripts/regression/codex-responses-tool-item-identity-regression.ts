import assert from 'node:assert/strict'
import type { Request } from 'express'

import {
  buildCodexResponsesChatBridgeBody,
  transformCodexResponsesChatBridgeUpstreamResponse
} from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

type JsonRecord = Record<string, unknown>

interface BridgeToolCaseResult {
  added: JsonRecord
  done: JsonRecord
  completedItem: JsonRecord
}

for (const toolKind of ['function', 'custom'] as const) {
  const expectedType = toolKind === 'custom' ? 'custom_tool_call' : 'function_call'
  const expectedPrefix = toolKind === 'custom' ? /^ctc_/ : /^fc_/
  const sseResult = await runBridgeToolCase(toolKind, true)
  const jsonResult = await runBridgeToolCase(toolKind, false)

  for (const [transport, result] of [['SSE', sseResult], ['JSON', jsonResult]] as const) {
    const addedItem = objectValue(result.added.item)
    const doneItem = objectValue(result.done.item)

    assert.equal(addedItem?.type, expectedType, `${transport} added 必须保留 ${toolKind} 工具类型`)
    assert.match(stringValue(addedItem?.id) ?? '', expectedPrefix, `${transport} added 必须按 ${toolKind} 类型生成 ID`)
    assert.equal(doneItem?.type, expectedType, `${transport} done 必须保留 ${toolKind} 工具类型`)
    assert.equal(doneItem?.id, addedItem?.id, `${transport} added/done 必须使用同一个 item ID`)
    assert.equal(doneItem?.call_id, addedItem?.call_id, `${transport} added/done 必须使用同一个 call_id`)
    assert.equal(result.done.output_index, result.added.output_index, `${transport} added/done 必须使用同一个 output_index`)
    assert.equal(result.completedItem.id, addedItem?.id, `${transport} completed response 必须复用流式 item ID`)
    assert.equal(result.completedItem.call_id, addedItem?.call_id, `${transport} completed response 必须复用 call_id`)
  }
}

const additionalToolsRequest = {
  method: 'POST',
  originalUrl: '/v1/responses',
  path: '/v1/responses',
  body: {
    model: 'gpt-5.6-sol',
    input: [
      {
        type: 'additional_tools',
        role: 'developer',
        tools: [
          { type: 'custom', name: 'exec', description: 'Run commands.' },
          { type: 'function', name: 'wait', parameters: { type: 'object', properties: {} } },
          {
            type: 'namespace',
            name: 'collaboration',
            tools: [{ type: 'function', name: 'send_message', parameters: { type: 'object', properties: {} } }]
          }
        ]
      },
      { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'run a command' }] }
    ],
    tool_choice: { type: 'custom', name: 'exec' }
  }
} as Request
const additionalToolsChatBody = JSON.parse((await buildCodexResponsesChatBridgeBody(additionalToolsRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const additionalToolsChatTools = Array.isArray(additionalToolsChatBody.tools) ? additionalToolsChatBody.tools : []
assert.equal(additionalToolsChatTools.length, 3, 'bridge 必须合并 additional_tools 中的 function/custom/namespace 工具')
assert.deepEqual(
  additionalToolsChatTools.map((tool) => objectValue(objectValue(tool)?.function)?.name),
  ['custom__exec', 'wait', 'collaboration__send_message'],
  'bridge 必须保持 additional_tools 工具的类型和 namespace 名称映射'
)
assert.deepEqual(additionalToolsChatBody.tool_choice, {
  type: 'function',
  function: { name: 'custom__exec' }
}, 'bridge 必须使用 additional_tools 工具解析 tool_choice')
const additionalToolsMessages = Array.isArray(additionalToolsChatBody.messages) ? additionalToolsChatBody.messages : []
assert.equal(
  additionalToolsMessages.filter((message) => objectValue(message)?.role === 'user').length,
  1,
  'additional_tools 不得变成 Chat message'
)
assert.equal(
  additionalToolsMessages.some((message) => objectValue(message)?.role === 'developer'),
  false,
  'additional_tools 的 developer role 不得被当成普通 Chat 消息'
)

const namespacedCustomHistoryRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [{
      type: 'namespace',
      name: 'editor',
      tools: [{ type: 'custom', name: 'exec' }]
    }],
    input: [
      { type: 'custom_tool_call', namespace: 'editor', name: 'exec', call_id: 'custom-1', input: 'run' },
      { type: 'custom_tool_call_output', call_id: 'custom-1', output: 'ok' }
    ],
    tool_choice: 'auto',
    stream: true
  }
} as Request
const namespacedCustomHistoryBody = JSON.parse((await buildCodexResponsesChatBridgeBody(namespacedCustomHistoryRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const namespacedCustomChatTools = Array.isArray(namespacedCustomHistoryBody.tools)
  ? namespacedCustomHistoryBody.tools
  : []
const namespacedCustomChatName = stringValue(objectValue(objectValue(namespacedCustomChatTools[0])?.function)?.name)
assert.equal(namespacedCustomChatName, 'editor__custom__exec', 'namespaced custom 工具必须使用稳定的 Chat 名称')
const namespacedCustomMessages = Array.isArray(namespacedCustomHistoryBody.messages)
  ? namespacedCustomHistoryBody.messages
  : []
const namespacedCustomCallMessage = objectValue(namespacedCustomMessages[0])
const namespacedCustomToolCalls = Array.isArray(namespacedCustomCallMessage?.tool_calls)
  ? namespacedCustomCallMessage.tool_calls
  : []
assert.equal(
  stringValue(objectValue(objectValue(namespacedCustomToolCalls[0])?.function)?.name),
  namespacedCustomChatName,
  'namespaced custom 历史调用必须匹配已声明的 Chat 工具名称'
)

const namespaceChildrenRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [{
      type: 'namespace',
      name: 'editor',
      children: [{ type: 'function', name: 'open', parameters: { type: 'object', properties: {} } }]
    }],
    input: 'open a file',
    tool_choice: 'auto'
  }
} as Request
const namespaceChildrenBody = JSON.parse((await buildCodexResponsesChatBridgeBody(namespaceChildrenRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const namespaceChildrenTools = Array.isArray(namespaceChildrenBody.tools) ? namespaceChildrenBody.tools : []
assert.equal(
  stringValue(objectValue(objectValue(namespaceChildrenTools[0])?.function)?.name),
  'editor__open',
  'namespace.children 必须与 namespace.tools 使用相同的展开规则'
)

const longFunctionName = 'long_function_' + 'x'.repeat(80)
const longFunctionRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [{ type: 'function', name: longFunctionName, parameters: { type: 'object', properties: {} } }],
    input: [
      { type: 'function_call', name: longFunctionName, call_id: 'long-1', arguments: '{}' },
      { type: 'function_call_output', call_id: 'long-1', output: 'ok' }
    ],
    tool_choice: 'auto'
  }
} as Request
const longFunctionBody = JSON.parse((await buildCodexResponsesChatBridgeBody(longFunctionRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const longFunctionChatTools = Array.isArray(longFunctionBody.tools) ? longFunctionBody.tools : []
const longFunctionChatName = stringValue(objectValue(objectValue(longFunctionChatTools[0])?.function)?.name)
const longFunctionMessages = Array.isArray(longFunctionBody.messages) ? longFunctionBody.messages : []
const longFunctionMessage = objectValue(longFunctionMessages[0])
const longFunctionToolCalls = Array.isArray(longFunctionMessage?.tool_calls) ? longFunctionMessage.tool_calls : []
const longFunctionHistoryName = stringValue(
  objectValue(objectValue(longFunctionToolCalls[0])?.function)?.name
)
assert.equal(longFunctionChatName?.length, 64, 'Chat 工具名必须遵守 64 字符上限')
assert.equal(longFunctionHistoryName, longFunctionChatName, '长工具名的历史调用必须复用截断后的 Chat 名称')

const conflictingFlattenedNameRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [{ type: 'function', name: 'foo__bar', parameters: { type: 'object', properties: {} } }],
    input: [{
      type: 'additional_tools',
      tools: [{
        type: 'namespace',
        name: 'foo',
        tools: [{ type: 'function', name: 'bar', parameters: { type: 'object', properties: {} } }]
      }]
    }],
    tool_choice: 'auto'
  }
} as Request
await assert.rejects(
  () => buildCodexResponsesChatBridgeBody(conflictingFlattenedNameRequest, { defaultModel: 'gpt-5.6-sol' }),
  (error: unknown) => {
    const candidate = error as { code?: string; statusCode?: number; message?: string }
    assert.equal(candidate.statusCode, 400, 'Chat 工具名冲突必须返回本地 400')
    assert.equal(candidate.code, 'conflicting_codex_bridge_chat_tool_name', 'Chat 工具名冲突必须使用稳定错误码')
    assert.match(candidate.message ?? '', /Chat 工具名 .*冲突/)
    return true
  }
)

const namespacedCustomResponseRequest = {
  ...namespacedCustomHistoryRequest,
  body: {
    ...namespacedCustomHistoryRequest.body,
    input: 'call the tool'
  }
} as Request
await buildCodexResponsesChatBridgeBody(namespacedCustomResponseRequest, { defaultModel: 'gpt-5.6-sol' })
const namespacedCustomResponse = transformCodexResponsesChatBridgeUpstreamResponse(
  namespacedCustomResponseRequest,
  chatSseResponse('editor__custom__exec', JSON.stringify({ input: 'run' })),
  {
    enabled: true,
    explicitMappingBridge: true,
    defaultModel: 'gpt-5.6-sol',
    idPrefix: 'namespaced_custom_test'
  }
)
assert.ok(namespacedCustomResponse.body, 'namespaced custom 响应必须包含 body')
const namespacedCustomResponseText = await collectBody(namespacedCustomResponse.body)
const namespacedCustomResponseEvents = parseSseJsonEvents(namespacedCustomResponseText)
const namespacedCustomAdded = namespacedCustomResponseEvents.find((event) => event.type === 'response.output_item.added')
const namespacedCustomDone = namespacedCustomResponseEvents.find((event) => event.type === 'response.output_item.done')
assert.equal(objectValue(namespacedCustomAdded?.item)?.namespace, 'editor', 'custom output added 必须保留 namespace')
assert.equal(objectValue(namespacedCustomDone?.item)?.namespace, 'editor', 'custom output done 必须保留 namespace')

const explicitEmptyToolsBridgeRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [],
    tool_choice: 'auto'
  }
} as Request
const explicitEmptyToolsBridgeBody = JSON.parse((await buildCodexResponsesChatBridgeBody(explicitEmptyToolsBridgeRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
assert.equal(Object.hasOwn(explicitEmptyToolsBridgeBody, 'tools'), false, '显式 tools=[] 的 bridge 请求不得重新打开 additional_tools')
assert.equal(Object.hasOwn(explicitEmptyToolsBridgeBody, 'tool_choice'), false, '没有 Chat 工具时不得转发工具选择')

const malformedTopLevelToolsBridgeRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: null,
    tool_choice: 'auto'
  }
} as Request
const malformedTopLevelToolsBridgeBody = JSON.parse((await buildCodexResponsesChatBridgeBody(malformedTopLevelToolsBridgeRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
assert.equal(
  Array.isArray(malformedTopLevelToolsBridgeBody.tools) && malformedTopLevelToolsBridgeBody.tools.length,
  3,
  '非法顶层 tools 不能冒充显式空数组并屏蔽 additional_tools'
)

const malformedAdditionalToolsRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    input: [{ type: 'additional_tools', tools: 'not-an-array' }]
  }
} as Request
await assert.rejects(
  () => buildCodexResponsesChatBridgeBody(malformedAdditionalToolsRequest, { defaultModel: 'gpt-5.6-sol' }),
  /additional_tools.*tools.*数组/
)

const shorthandAdditionalToolsRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    input: [
      {
        type: 'additional_tools',
        role: 'developer',
        tools: ['exec', 'wait']
      },
      { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'run a command' }] }
    ]
  }
} as Request
const shorthandAdditionalToolsChatBody = JSON.parse((await buildCodexResponsesChatBridgeBody(shorthandAdditionalToolsRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const shorthandAdditionalTools = Array.isArray(shorthandAdditionalToolsChatBody.tools)
  ? shorthandAdditionalToolsChatBody.tools
  : []
assert.deepEqual(
  shorthandAdditionalTools.map((tool) => objectValue(objectValue(tool)?.function)?.name),
  ['custom__exec', 'custom__wait'],
  'additional_tools 字符串简写必须按 custom 工具注册'
)

const mixedToolsRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [{ type: 'function', name: 'top_level', parameters: { type: 'object', properties: {} } }],
    tool_choice: 'auto'
  }
} as Request
const mixedToolsChatBody = JSON.parse((await buildCodexResponsesChatBridgeBody(mixedToolsRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const mixedChatTools = Array.isArray(mixedToolsChatBody.tools) ? mixedToolsChatBody.tools : []
assert.equal(mixedChatTools.length, 4, '顶层 tools 与 additional_tools 必须合并而不是二选一')

const duplicateDefinitionRequest = {
  ...additionalToolsRequest,
  body: {
    ...additionalToolsRequest.body,
    tools: [{
      type: 'function',
      name: 'same_tool',
      description: 'Same tool.',
      parameters: { type: 'object', properties: { value: { type: 'string' } } }
    }],
    input: [{
      type: 'additional_tools',
      tools: [{
        parameters: { properties: { value: { type: 'string' } }, type: 'object' },
        description: 'Same tool.',
        name: 'same_tool',
        type: 'function'
      }]
    }],
    tool_choice: 'auto'
  }
} as Request
const duplicateDefinitionChatBody = JSON.parse((await buildCodexResponsesChatBridgeBody(duplicateDefinitionRequest, {
  defaultModel: 'gpt-5.6-sol'
})).toString('utf8')) as JsonRecord
const duplicateDefinitionTools = Array.isArray(duplicateDefinitionChatBody.tools)
  ? duplicateDefinitionChatBody.tools
  : []
assert.equal(duplicateDefinitionTools.length, 1, '相同工具定义必须去重，不能生成重复 Chat 工具')

const conflictingDefinitionRequest = {
  ...duplicateDefinitionRequest,
  body: {
    ...duplicateDefinitionRequest.body,
    input: [{
      type: 'additional_tools',
      tools: [{
        type: 'function',
        name: 'same_tool',
        description: 'Conflicting tool.',
        parameters: { type: 'object', properties: { other: { type: 'number' } } }
      }]
    }]
  }
} as Request
await assert.rejects(
  () => buildCodexResponsesChatBridgeBody(conflictingDefinitionRequest, { defaultModel: 'gpt-5.6-sol' }),
  (error: unknown) => {
    const candidate = error as { code?: string; message?: string; statusCode?: number }
    assert.equal(candidate.statusCode, 400, '冲突工具定义必须返回本地 400')
    assert.equal(candidate.code, 'conflicting_codex_bridge_tool_definition', '冲突工具定义必须使用稳定错误码')
    assert.match(candidate.message ?? '', /重复声明且定义冲突/)
    return true
  }
)

console.log('Codex Responses 工具 item 身份回归通过：function/custom 在 JSON 与 SSE 中从源头生成正确前缀，并保持 added/done/completed 身份一致')

async function runBridgeToolCase(
  toolKind: 'function' | 'custom',
  stream: boolean
): Promise<BridgeToolCaseResult> {
  const responsesName = toolKind === 'custom' ? 'apply_patch' : 'run_task'
  const body: JsonRecord = {
    model: 'gpt-5.6-sol',
    input: 'call the tool',
    stream,
    tools: toolKind === 'custom'
      ? [{ type: 'custom', name: responsesName, description: 'Apply a patch.' }]
      : [{
          type: 'function',
          name: responsesName,
          description: 'Run a task.',
          parameters: { type: 'object', properties: { value: { type: 'string' } } }
        }]
  }
  const req = {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body
  } as Request
  const chatBody = JSON.parse((await buildCodexResponsesChatBridgeBody(req, {
    defaultModel: 'gpt-5.6-sol'
  })).toString('utf8')) as JsonRecord
  const chatTools = Array.isArray(chatBody.tools) ? chatBody.tools : []
  const chatFunction = objectValue(objectValue(chatTools[0])?.function)
  const chatName = stringValue(chatFunction?.name)
  assert.ok(chatName, 'bridge 必须生成 Chat function tool name')

  const argumentsText = toolKind === 'custom'
    ? JSON.stringify({ input: '*** Begin Patch\n*** End Patch\n' })
    : JSON.stringify({ value: 'ok' })
  const transformed = transformCodexResponsesChatBridgeUpstreamResponse(req, chatSseResponse(chatName, argumentsText), {
    enabled: true,
    explicitMappingBridge: true,
    defaultModel: 'gpt-5.6-sol',
    idPrefix: 'identity_test'
  })
  assert.ok(transformed.body, 'bridge 响应必须包含 body')
  const text = await collectBody(transformed.body)

  if (!stream) {
    const response = JSON.parse(text) as JsonRecord
    const output = Array.isArray(response.output) ? response.output : []
    assert.equal(output.length, 1, 'JSON 响应必须包含一个工具 item')
    const completedItem = objectValue(output[0]) ?? {}
    return {
      added: { output_index: 0, item: completedItem },
      done: { output_index: 0, item: completedItem },
      completedItem
    }
  }

  const events = parseSseJsonEvents(text)
  const added = events.find((event) => event.type === 'response.output_item.added')
  const done = events.find((event) => event.type === 'response.output_item.done')
  const completed = events.find((event) => event.type === 'response.completed')
  assert.ok(added, 'SSE 必须包含 response.output_item.added')
  assert.ok(done, 'SSE 必须包含 response.output_item.done')
  assert.ok(completed, 'SSE 必须包含 response.completed')
  const completedResponse = objectValue(completed.response)
  const completedOutput = Array.isArray(completedResponse?.output) ? completedResponse.output : []
  assert.equal(completedOutput.length, 1, 'completed response 必须包含一个工具 item')
  return {
    added,
    done,
    completedItem: objectValue(completedOutput[0]) ?? {}
  }
}

function chatSseResponse(chatName: string, argumentsText: string): GatewayUpstreamResponse {
  const chunks = [
    sseData({
      id: 'chatcmpl_identity',
      object: 'chat.completion.chunk',
      model: 'gpt-5.6-sol',
      choices: [{
        index: 0,
        delta: {
          tool_calls: [{
            index: 0,
            id: 'call_identity',
            type: 'function',
            function: { name: chatName, arguments: argumentsText }
          }]
        },
        finish_reason: null
      }]
    }),
    sseData({
      id: 'chatcmpl_identity',
      object: 'chat.completion.chunk',
      model: 'gpt-5.6-sol',
      choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }]
    }),
    'data: [DONE]\n\n'
  ]
  return {
    status: 200,
    ok: true,
    headers: new Headers({ 'content-type': 'text/event-stream' }),
    body: (async function * () {
      for (const chunk of chunks) yield Buffer.from(chunk, 'utf8')
    })()
  }
}

function sseData(value: unknown): string {
  return `data: ${JSON.stringify(value)}\n\n`
}

async function collectBody(body: AsyncIterable<Uint8Array>): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

function parseSseJsonEvents(text: string): JsonRecord[] {
  const output: JsonRecord[] = []
  for (const block of text.split(/\r?\n\r?\n/)) {
    const data = block
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trimStart())
      .join('\n')
    if (!data || data === '[DONE]') continue
    const parsed = JSON.parse(data) as unknown
    if (isPlainObject(parsed)) output.push(parsed)
  }
  return output
}

function objectValue(value: unknown): JsonRecord | undefined {
  return isPlainObject(value) ? value : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
