import assert from 'node:assert/strict'
import { projectChatMessageProcess } from '../../views/chat/chatMessageProcess'
import type { ChatMessage } from '../../types/domain/chat'

const message = {
  contentBlocks: [
    { type: 'reasoning', text: '先分析' },
    { type: 'tool_call', id: 'search_1', toolType: 'web_search_call', status: 'completed', item: { query: '测试' } }
  ]
} as ChatMessage

assert.deepEqual(projectChatMessageProcess(message), {
  reasoningText: '先分析',
  toolEvents: [{ id: 'search_1', type: 'web_search_call', status: 'completed', item: { query: '测试' } }]
})
console.log('AI 问答历史过程投影回归通过')
