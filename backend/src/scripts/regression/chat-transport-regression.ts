import assert from 'node:assert/strict'

import { buildChatSystemInstructions } from '../../modules/chat/chat-system-instructions.js'
import { buildChatTransportRequest, resolveChatBudgetContent, resolveChatSupportedProtocols, selectChatTransport } from '../../modules/chat/chat-transport.js'

assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions'], toolsEnabled: true }), 'chat_completions')
assert.equal(selectChatTransport({ supportedProtocols: ['responses'], toolsEnabled: true }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions', 'responses'], toolsEnabled: true }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions', 'responses'], toolsEnabled: false }), 'chat_completions')
assert.equal(selectChatTransport({ supportedProtocols: [], toolsEnabled: true }), 'chat_completions')
assert.equal(resolveChatBudgetContent({
  protocol: 'responses',
  currentContent: '短摘要',
  currentBlocks: [{ type: 'input_text', text: '第一段' }, { type: 'input_image', dataUrl: 'data:image/png;base64,abc' }, { type: 'input_text', text: '第二段' }]
}), '第一段\n第二段', 'Responses 预算必须使用实际发送的全部 input_text block')
assert.equal(resolveChatBudgetContent({ protocol: 'chat_completions', currentContent: 'Chat 正文', currentBlocks: [{ type: 'input_text', text: '忽略块' }] }), 'Chat 正文')

const instructions = buildChatSystemInstructions({ toolsEnabled: true }).text
const responses = buildChatTransportRequest({
  protocol: 'responses', instructions, model: 'model-a', history: [{ role: 'user', content: '此前问题' }], currentContent: '继续', currentBlocks: [{ type: 'input_text', text: '图片前' }, { type: 'input_image', dataUrl: 'data:image/png;base64,abc' }, { type: 'input_text', text: '图片后' }], toolsEnabled: true, reasoningEffort: 'high', serviceTier: 'priority'
})
assert.equal(responses.path, '/v1/responses')
assert.equal(responses.body.instructions, instructions)
assert.deepEqual(responses.body.input, [{ role: 'user', content: '此前问题' }, { role: 'user', content: [{ type: 'input_text', text: '图片前' }, { type: 'input_image', image_url: 'data:image/png;base64,abc' }, { type: 'input_text', text: '图片后' }] }])
assert.equal((responses.body.input as Array<{ role: string }>).some((item) => item.role === 'system'), false)
assert.equal(responses.body.stream, true)
assert.deepEqual(responses.body.tools, [{ type: 'web_search' }])
assert.deepEqual(responses.body.reasoning, { effort: 'high' })
assert.equal(responses.body.service_tier, 'priority')

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
    return groupId === 'group-b' ? [{}] : []
  }
})
assert.deepEqual(protocols, ['chat_completions', 'responses'])
assert.deepEqual(checked, ['group-a:model-a:responses', 'group-b:model-a:responses'])

console.log('AI 问答 transport 协议选择回归通过')
