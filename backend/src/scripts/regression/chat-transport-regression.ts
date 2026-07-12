import assert from 'node:assert/strict'

import { buildChatTransportRequest, resolveChatSupportedProtocols, selectChatTransport } from '../../modules/chat/chat-transport.js'

assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions'], toolsEnabled: true }), 'chat_completions')
assert.equal(selectChatTransport({ supportedProtocols: ['responses'], toolsEnabled: true }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions', 'responses'], toolsEnabled: true }), 'responses')
assert.equal(selectChatTransport({ supportedProtocols: ['chat_completions', 'responses'], toolsEnabled: false }), 'chat_completions')
assert.equal(selectChatTransport({ supportedProtocols: [], toolsEnabled: true }), 'chat_completions')

const responses = buildChatTransportRequest({
  protocol: 'responses', model: 'model-a', history: [{ role: 'user', content: '此前问题' }], currentContent: '继续', toolsEnabled: true
})
assert.equal(responses.path, '/v1/responses')
assert.deepEqual(responses.body.input, [{ role: 'user', content: '此前问题' }, { role: 'user', content: '继续' }])
assert.equal(responses.body.stream, true)
assert.deepEqual(responses.body.tools, [{ type: 'web_search' }])

const chat = buildChatTransportRequest({
  protocol: 'chat_completions', model: 'model-a', history: [], currentContent: '你好', toolsEnabled: true
})
assert.equal(chat.path, '/v1/chat/completions')
assert.deepEqual(chat.body.messages, [{ role: 'user', content: '你好' }])

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
