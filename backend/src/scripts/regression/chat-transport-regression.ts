import assert from 'node:assert/strict'

import { buildChatSystemInstructions } from '../../modules/chat/chat-system-instructions.js'
import { buildChatTransportRequest, resolveChatBudgetContent, resolveChatSupportedProtocols, selectChatTransport } from '../../modules/chat/chat-transport.js'

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

const instructions = buildChatSystemInstructions({ toolsEnabled: true }).text
const responses = buildChatTransportRequest({
  protocol: 'responses', instructions, model: 'model-a', history: [{ role: 'user', content: '此前问题' }], currentContent: '继续', currentBlocks: [{ type: 'input_text', text: '图片前' }, { type: 'input_image', dataUrl: 'data:image/png;base64,abc' }, { type: 'input_text', text: '图片后' }], toolsEnabled: true, reasoningEffort: 'high', serviceTier: 'default'
})
assert.equal(responses.path, '/v1/responses')
assert.equal(responses.body.instructions, instructions)
assert.deepEqual(responses.body.input, [{ role: 'user', content: '此前问题' }, { role: 'user', content: [{ type: 'input_text', text: '图片前' }, { type: 'input_image', image_url: 'data:image/png;base64,abc' }, { type: 'input_text', text: '图片后' }] }])
assert.equal((responses.body.input as Array<{ role: string }>).some((item) => item.role === 'system'), false)
assert.equal(responses.body.stream, true)
assert.deepEqual(responses.body.tools, [{ type: 'web_search' }])
assert.deepEqual(responses.body.reasoning, { effort: 'high' })
assert.equal(responses.body.service_tier, 'default')

const chat = buildChatTransportRequest({
  protocol: 'chat_completions', instructions, model: 'model-a', history: [{ role: 'assistant', content: '此前回答' }], currentContent: '你好', toolsEnabled: true, reasoningEffort: 'low', serviceTier: 'flex'
})
assert.equal(chat.path, '/v1/chat/completions')
assert.deepEqual(chat.body.messages, [{ role: 'system', content: instructions }, { role: 'assistant', content: '此前回答' }, { role: 'user', content: '你好' }])
assert.equal((chat.body.messages as Array<{ role: string }>).filter((item) => item.role === 'system').length, 1)
assert.equal('tools' in chat.body, false, 'Chat Completions 不应因 toolsEnabled 注入 Responses 工具字段')
assert.equal(chat.body.reasoning_effort, 'low')
assert.equal(chat.body.service_tier, 'flex')

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
    }]
  }]
})
assert.deepEqual(responsesBridgeProtocols, ['chat_completions', 'responses'], '显式 Responses 到 Chat 映射必须按上游 chat_sse 能力保留 Responses 协议')

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
