import assert from 'node:assert/strict'

import { buildChatSystemInstructions } from '../../modules/chat/chat-system-instructions.js'
import { mapChatHostedToolsToResponses, normalizeChatHostedTools } from '../../modules/chat/chat-tools.js'
import { buildChatTransportRequest, resolveChatBudgetContent, resolveChatSupportedProtocols, selectChatTransport } from '../../modules/chat/chat-transport.js'
import { createDiagnosticEchoTool } from '../../modules/chat/tools/executors/diagnostic-echo.js'

assert.deepEqual(
  normalizeChatHostedTools(['image_generation', 'web_search', 'image_generation', 'unsupported', '', null]),
  ['web_search', 'image_generation'],
  'hosted tools 必须过滤无效值、去重并保持固定声明顺序'
)
assert.deepEqual(normalizeChatHostedTools(undefined), [], '未提供 hosted tools 时必须返回空集合')
assert.deepEqual(
  mapChatHostedToolsToResponses(['image_generation', 'web_search', 'web_search']),
  [{ type: 'web_search' }, { type: 'image_generation' }],
  'Responses tool mapping 必须只投影规范化后的有效 hosted tools'
)

const promptCacheModule = await import('../../modules/chat/chat-prompt-cache.js').catch(() => undefined)
assert.equal(typeof promptCacheModule?.buildChatPromptCacheKey, 'function', 'AI 问答必须提供稳定且不透明的 prompt cache key 生成器')
const buildChatPromptCacheKey = promptCacheModule!.buildChatPromptCacheKey
const firstConversationKey = buildChatPromptCacheKey({ systemAccountId: 'sys-user-1', apiKeyId: 'key-1', conversationId: 'conversation-1' })
const repeatedConversationKey = buildChatPromptCacheKey({ systemAccountId: 'sys-user-1', apiKeyId: 'key-1', conversationId: 'conversation-1' })
const otherConversationKey = buildChatPromptCacheKey({ systemAccountId: 'sys-user-1', apiKeyId: 'key-1', conversationId: 'conversation-2' })
assert.equal(firstConversationKey, repeatedConversationKey, '相同用户、API Key 和会话必须复用相同 prompt cache key')
assert.notEqual(firstConversationKey, otherConversationKey, '不同会话必须隔离 prompt cache key')
assert(firstConversationKey.length <= 64, 'prompt cache key 不得超过上游 64 字符限制')
assert.doesNotMatch(firstConversationKey, /sys-user-1|key-1|conversation-1/, 'prompt cache key 不得泄露内部明文 ID')

assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions'], preferResponses: true }), 'chat_completions')
assert.equal(selectChatTransport({ supportedProtocols: ['responses'], preferResponses: true }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['responses'], preferResponses: false }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions', 'responses'], preferResponses: true }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions', 'responses'], preferResponses: false }), 'chat_completions')
assert.equal(selectChatTransport({ supportedProtocols: [], preferResponses: true }), 'chat_completions')
assert.equal(resolveChatBudgetContent({
  protocol: 'responses',
  currentContent: '短摘要',
  currentBlocks: [{ type: 'input_text', text: '第一段' }, { type: 'input_image', dataUrl: 'data:image/png;base64,abc' }, { type: 'input_text', text: '第二段' }]
}), '第一段\n第二段', 'Responses 预算必须使用实际发送的全部 input_text block')
assert.equal(resolveChatBudgetContent({ protocol: 'chat_completions', currentContent: 'Chat 正文', currentBlocks: [{ type: 'input_text', text: '忽略块' }] }), 'Chat 正文')

const instructions = buildChatSystemInstructions({ effectiveTools: ['web_search', 'image_generation'] }).text
const responses = buildChatTransportRequest({
  protocol: 'responses', instructions, model: 'model-a', history: [{ role: 'user', content: [{ type: 'input_text', text: '此前问题' }] }], currentContent: '继续', currentBlocks: [{ type: 'input_text', text: '图片前' }, { type: 'input_image', dataUrl: 'data:image/png;base64,abc' }, { type: 'input_text', text: '图片后' }], effectiveTools: ['image_generation', 'web_search', 'web_search'], reasoningEffort: 'high', serviceTier: 'default', promptCacheKey: firstConversationKey
})
assert.equal(responses.path, '/v1/responses')
assert.equal(responses.body.instructions, instructions)
assert.deepEqual(responses.body.input, [{ role: 'user', content: [{ type: 'input_text', text: '此前问题' }] }, { role: 'user', content: [{ type: 'input_text', text: '图片前' }, { type: 'input_image', image_url: 'data:image/png;base64,abc', detail: 'high' }, { type: 'input_text', text: '图片后' }] }])
assert.equal((responses.body.input as Array<{ role: string }>).some((item) => item.role === 'system'), false)
assert.equal(responses.body.stream, true)
assert.deepEqual(responses.body.tools, [{ type: 'web_search' }, { type: 'image_generation' }])
assert.deepEqual(
  responses.body.reasoning,
  { effort: 'high', summary: 'auto' },
  'Responses 思考请求必须显式申请摘要，否则真实模型不会返回可展示的思考过程'
)
assert.equal(responses.body.service_tier, 'default')
assert.equal(responses.body.prompt_cache_key, firstConversationKey)

const responsesWithGenerationParameters = buildChatTransportRequest({
  protocol: 'responses', instructions, model: 'model-a', history: [], currentContent: '参数', effectiveTools: [],
  generationParameters: { temperature: 0.4, maxOutputTokens: 321, frequencyPenalty: 1 }
})
assert.equal(responsesWithGenerationParameters.body.temperature, 0.4)
assert.equal(responsesWithGenerationParameters.body.max_output_tokens, 321)
assert.equal(Object.hasOwn(responsesWithGenerationParameters.body, 'frequency_penalty'), false, 'Responses 不得透传不支持的 penalty 字段')

const responsesPlainText = buildChatTransportRequest({
  protocol: 'responses', instructions, model: 'model-a', history: [], currentContent: '纯文本首轮', effectiveTools: []
})
assert.deepEqual(responsesPlainText.body.input, [{ role: 'user', content: [{ type: 'input_text', text: '纯文本首轮' }] }], 'Responses 首轮纯文本必须与后续历史保持相同 input_text block 表示')
assert.equal(Object.hasOwn(responsesPlainText.body, 'prompt_cache_key'), false, '未传入 cache key 时 Responses 不得伪造缓存键')
assert.equal(Object.hasOwn(responsesPlainText.body, 'tools'), false, '空 effectiveTools 不得声明 Responses tools')
assert.equal(Object.hasOwn(responsesPlainText.body, 'tool_choice'), false, '空 effectiveTools 不得声明 tool_choice')

const chat = buildChatTransportRequest({
  protocol: 'chat_completions', instructions, model: 'model-a', history: [{ role: 'assistant', content: '此前回答' }], currentContent: '你好', effectiveTools: ['web_search', 'image_generation'], reasoningEffort: 'low', serviceTier: 'flex', promptCacheKey: firstConversationKey
})
assert.equal(chat.path, '/v1/chat/completions')
assert.deepEqual(chat.body.messages, [{ role: 'system', content: instructions }, { role: 'assistant', content: '此前回答' }, { role: 'user', content: '你好' }])
assert.equal((chat.body.messages as Array<{ role: string }>).filter((item) => item.role === 'system').length, 1)
assert.equal('tools' in chat.body, false, 'Chat Completions 不应因 effectiveTools 注入 Responses 工具字段')
assert.equal(chat.body.reasoning_effort, 'low')
assert.equal(chat.body.service_tier, 'flex')
assert.equal(chat.body.prompt_cache_key, firstConversationKey)
assert.deepEqual(chat.body.stream_options, { include_usage: true })

const chatWithGenerationParameters = buildChatTransportRequest({
  protocol: 'chat_completions', instructions, model: 'model-a', history: [], currentContent: '参数', effectiveTools: [],
  generationParameters: { topP: 0.8, frequencyPenalty: 0.5, presencePenalty: -0.2, maxOutputTokens: 123, seed: 42 }
})
assert.deepEqual({
  top_p: chatWithGenerationParameters.body.top_p,
  frequency_penalty: chatWithGenerationParameters.body.frequency_penalty,
  presence_penalty: chatWithGenerationParameters.body.presence_penalty,
  max_completion_tokens: chatWithGenerationParameters.body.max_completion_tokens,
  seed: chatWithGenerationParameters.body.seed
}, { top_p: 0.8, frequency_penalty: 0.5, presence_penalty: -0.2, max_completion_tokens: 123, seed: 42 })

const diagnosticTool = createDiagnosticEchoTool()
const responsesWithInternalTool = buildChatTransportRequest({
  protocol: 'responses', instructions, model: 'model-a', history: [], currentContent: '必须调用诊断工具',
  effectiveTools: ['web_search'], internalTools: [diagnosticTool],
  toolContinuation: [
    { type: 'function_call', call_id: 'call-r1', name: 'diagnostic_echo', arguments: '{"text":"R"}' },
    { type: 'function_call_output', call_id: 'call-r1', output: '{"echoedText":"R"}' }
  ]
})
assert.deepEqual((responsesWithInternalTool.body.tools as Array<Record<string, unknown>>)[0], { type: 'web_search' })
assert.equal((responsesWithInternalTool.body.tools as Array<{ name?: string }>)[1]?.name, 'diagnostic_echo')
assert.equal(responsesWithInternalTool.body.parallel_tool_calls, false)
assert.equal((responsesWithInternalTool.body.input as unknown[]).length, 3, 'Responses 工具续答必须把 function call 与 output 追加到原始输入后')

const chatWithInternalTool = buildChatTransportRequest({
  protocol: 'chat_completions', instructions, model: 'model-a', history: [], currentContent: '必须调用诊断工具',
  effectiveTools: [], internalTools: [diagnosticTool],
  toolContinuation: [
    { role: 'assistant', content: null, tool_calls: [{ id: 'call-c1', type: 'function', function: { name: 'diagnostic_echo', arguments: '{"text":"C"}' } }] },
    { role: 'tool', tool_call_id: 'call-c1', content: '{"echoedText":"C"}' }
  ]
})
assert.equal((chatWithInternalTool.body.tools as Array<{ function?: { name?: string } }>)[0]?.function?.name, 'diagnostic_echo')
assert.equal(chatWithInternalTool.body.tool_choice, 'auto')
assert.equal(chatWithInternalTool.body.parallel_tool_calls, false)
assert.equal((chatWithInternalTool.body.messages as unknown[]).length, 4, 'Chat 工具续答必须保留 assistant tool_calls 与 role=tool 结果')

const checked: string[] = []
const protocols = await resolveChatSupportedProtocols({
  groupIds: ['group-a', 'group-a', 'group-b'],
  model: 'model-a',
  loadAccounts: async (groupId, model, endpointFamily) => {
    checked.push(`${groupId}:${model}:${endpointFamily}`)
    if (groupId === 'group-a' && endpointFamily === 'chat_completions') {
      return [{ supportedEndpointModes: ['chat_sse'] }]
    }
    if (groupId === 'group-b' && endpointFamily === 'responses') {
      return [{ supportedEndpointModes: ['responses_sse'] }]
    }
    return []
  }
})
assert.deepEqual(protocols, ['chat_completions', 'responses'])
assert.deepEqual(checked, [
  'group-a:model-a:chat_completions',
  'group-a:model-a:responses',
  'group-b:model-a:responses'
], '两种协议都必须用真实账户能力探测；已命中的协议无需对后续分组重复查询')

const chatOnlyProtocols = await resolveChatSupportedProtocols({
  groupIds: ['group-chat-only'],
  model: 'chat-only-model',
  loadAccounts: async () => [{
    supportedEndpointModes: ['chat_json', 'chat_sse']
  }]
})
assert.deepEqual(chatOnlyProtocols, ['chat_completions'], '候选查询非空不代表支持 Responses，协议选择必须校验账户 endpoint modes')

const unsupportedModelProtocols = await resolveChatSupportedProtocols({
  groupIds: ['group-limited-model'],
  model: 'model-not-supported',
  loadAccounts: async () => [{
    supportedEndpointModes: ['chat_sse', 'responses_sse'],
    supportedModels: ['model-supported']
  }]
})
assert.deepEqual(unsupportedModelProtocols, [], '候选查询可能包含普通窗口账号，聊天模型列表仍必须校验账户 supportedModels')

const responsesBridgeProtocols = await resolveChatSupportedProtocols({
  groupIds: ['group-responses-bridge'],
  model: 'responses-alias',
  loadAccounts: async () => [{
    supportedEndpointModes: ['chat_sse'],
    modelMappings: [{
      sourceModel: 'responses-alias',
      sourceEndpointFamily: 'responses',
      upstreamModel: 'chat-upstream',
      upstreamEndpointFamily: 'chat_completions'
    }],
    supportedModels: ['chat-upstream']
  }]
})
assert.deepEqual(responsesBridgeProtocols, ['responses'], '显式 Responses 到 Chat 映射必须按上游 chat_sse 能力保留 Responses 协议，且不得误报未映射的 Chat Completions')

for (const [upstreamEndpointFamily, supportedEndpointMode] of [
  ['messages', 'messages_sse'],
  ['generate_content', 'generate_content_sse']
] as const) {
  const hybridProtocols = await resolveChatSupportedProtocols({
    groupIds: [`group-${upstreamEndpointFamily}`],
    model: `${upstreamEndpointFamily}-alias`,
    loadAccounts: async (_groupId, _model, endpointFamily) => endpointFamily === 'responses' ? [{
      supportedEndpointModes: [supportedEndpointMode],
      modelMappings: [{
        sourceModel: `${upstreamEndpointFamily}-alias`,
        sourceEndpointFamily: 'responses',
        upstreamModel: 'hybrid-upstream',
        upstreamEndpointFamily
      }]
    }] : []
  })
  assert.deepEqual(hybridProtocols, ['responses'], `Responses 到 ${upstreamEndpointFamily} 的混合映射必须按真实 SSE 端点保留 Responses 协议`)
}

console.log('AI 问答 transport 协议选择回归通过')
