import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync('../frontend/src/views/chat/ChatMessageList.vue', 'utf8')

assert.match(source, /message-row-user\s*\{\s*justify-content:\s*flex-end/, '用户消息必须位于右侧')
assert.match(source, /message-row-assistant\s*\{\s*justify-content:\s*flex-start/, 'AI 消息必须位于左侧')
assert.match(source, /message-bubble-user/, '用户消息需要独立气泡样式，不能和 AI 共用整行布局')

console.log('AI 问答消息方向回归通过')
