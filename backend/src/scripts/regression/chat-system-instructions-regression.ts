import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import {
  buildChatSystemInstructions,
  chatSystemInstructionsVersion
} from '../../modules/chat/chat-system-instructions.js'

const plain = buildChatSystemInstructions({ effectiveTools: [] })
const tools = buildChatSystemInstructions({ effectiveTools: ['web_search', 'image_generation'] })
const internalImage = buildChatSystemInstructions({ effectiveTools: [], internalToolNames: ['generate_image'] })
const expectedToolDiscipline =
  '避免重复调用名称相同且参数等价的工具；前次调用失败、结果可能过期或用户明确要求刷新时允许再次调用。'

assert.equal(chatSystemInstructionsVersion, 'chat-system-v4')
assert.equal(plain.version, chatSystemInstructionsVersion)
assert.equal(tools.version, chatSystemInstructionsVersion)
assert.match(plain.text, /用户明确要求的语言、格式、长度和交付形态优先/)
assert.match(plain.text, /无法判断时使用简体中文/)
assert.match(plain.text, /简单回答不强制使用标题、表格或代码块/)
assert.match(plain.text, /JSON、CSV、XML、YAML、纯文本、仅代码、完整文件或补丁/)
assert.match(plain.text, /不擅自添加 Markdown 围栏/)
assert.match(plain.text, /区分已知事实、合理推断和不确定信息/)
assert.match(plain.text, /不声称使用当前未提供的工具或能力/)
assert.match(plain.text, /严格区分事实、推断、假设和未知/)
assert.match(plain.text, /禁止猜测、伪造或脑补未知内容/)
assert.match(plain.text, /信息不足，无法完成需求|信息不足.*无法完成/)
assert.match(plain.text, /逐项列明缺失的具体信息/)
assert.match(plain.text, /引导用户补齐/)
assert.match(plain.text, /不私自添加用户未提及的场景、数据、规则或条件/)
assert.doesNotMatch(plain.text, /可撤销的最小假设/)
assert.match(plain.text, /Mermaid/)
assert.match(plain.text, /LaTeX/)
assert.match(plain.text, /fenced.*svg|fenced `svg`|svg/)
assert.match(plain.text, /真实生图|真实图像工具|位图/)
assert.doesNotMatch(plain.text, /重复调用名称相同/)

assert.match(tools.text, /避免重复调用名称相同且参数等价的工具/)
assert.match(tools.text, /前次调用失败、结果可能过期或用户明确要求刷新时允许再次调用/)
assert.equal(tools.text.match(/重复调用名称相同/g)?.length, 1)
assert.equal(tools.text, `${plain.text}\n\n${expectedToolDiscipline}`)
assert.match(internalImage.text, /没有明确指定宽高或分辨率.*常规尺寸/u)
assert.match(internalImage.text, /不得自行选择 2K、4K/u)
assert.match(internalImage.text, /高清.*不等于.*尺寸/u)
assert.match(internalImage.text, /reference_asset_ids|assetId/u)
assert.match(internalImage.text, /无法唯一判断.*询问|不明确.*询问/u)
assert.match(internalImage.text, /编辑.*既有图片|二次编辑/u)
assert.doesNotMatch(internalImage.text, /1536|1572864|1024x1024|16px|3:1/u, '服务端具体尺寸上限不得写入系统提示词')
assert.match(internalImage.text, /避免重复调用名称相同/u)
assert.match(plain.hash, /^[a-f0-9]{64}$/)
assert.match(tools.hash, /^[a-f0-9]{64}$/)
assert.equal(plain.hash, 'fb26ecacfc8d0fb0454daabcfa25c341949e96d286a98a221368ddf6858549e7')
assert.equal(tools.hash, '0b923cc4584aadd463907b993a498eb81ca7033f553e78a41b85b9db38fb517b')
assert.equal(plain.hash, createHash('sha256').update(plain.text).digest('hex'))
assert.equal(tools.hash, createHash('sha256').update(tools.text).digest('hex'))
assert.deepEqual(buildChatSystemInstructions({ effectiveTools: [] }), plain)
assert.deepEqual(buildChatSystemInstructions({ effectiveTools: ['web_search', 'image_generation'] }), tools)
assert.deepEqual(buildChatSystemInstructions({ effectiveTools: ['image_generation', 'image_generation', 'unknown' as never] }), buildChatSystemInstructions({ effectiveTools: ['image_generation'] }))

console.log('chat system instructions regression passed')
