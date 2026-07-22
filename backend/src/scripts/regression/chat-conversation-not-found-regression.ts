import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')

assert.match(source, /class ChatConversationNotFoundError extends Error/, '会话不存在必须使用专用错误，不能抛普通 Error 后落到 500')
assert.match(source, /if \(error instanceof ChatConversationNotFoundError\)[\s\S]{0,240}status\(404\)[\s\S]{0,240}code: error\.code/, '会话不存在或无归属必须稳定映射为不泄露归属的 404')
assert.match(source, /if \(!conversation\) throw new ChatConversationNotFoundError\(\)/, 'requireOwnedConversation 必须抛出统一的不存在错误')

console.log('chat conversation not found regression passed')
