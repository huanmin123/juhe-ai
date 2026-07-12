import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import {
  buildChatSystemInstructions,
  chatSystemInstructionsVersion
} from '../../modules/chat/chat-system-instructions.js'

const plain = buildChatSystemInstructions({ toolsEnabled: false })
const tools = buildChatSystemInstructions({ toolsEnabled: true })

assert.equal(chatSystemInstructionsVersion, 'chat-system-v1')
assert.equal(plain.version, chatSystemInstructionsVersion)
assert.match(plain.text, /用户明确要求的语言、格式、长度和交付形态优先/)
assert.match(plain.text, /无法判断时使用简体中文/)
assert.match(plain.text, /简单回答不强制使用标题、表格或代码块/)
assert.match(plain.text, /JSON、CSV、XML、YAML、纯文本、仅代码、完整文件或补丁/)
assert.match(plain.text, /不擅自添加 Markdown 围栏/)
assert.match(plain.text, /区分已知事实、合理推断和不确定信息/)
assert.match(plain.text, /不声称使用当前未提供的工具或能力/)
assert.doesNotMatch(plain.text, /重复调用名称相同/)

assert.match(tools.text, /避免重复调用名称相同且参数等价的工具/)
assert.match(tools.text, /前次调用失败、结果可能过期或用户明确要求刷新时允许再次调用/)
assert.equal(tools.text.match(/重复调用名称相同/g)?.length, 1)
assert.match(plain.hash, /^[a-f0-9]{64}$/)
assert.match(tools.hash, /^[a-f0-9]{64}$/)
assert.equal(plain.hash, createHash('sha256').update(plain.text).digest('hex'))
assert.equal(tools.hash, createHash('sha256').update(tools.text).digest('hex'))
assert.deepEqual(buildChatSystemInstructions({ toolsEnabled: false }), plain)
assert.deepEqual(buildChatSystemInstructions({ toolsEnabled: true }), tools)

console.log('chat system instructions regression passed')
