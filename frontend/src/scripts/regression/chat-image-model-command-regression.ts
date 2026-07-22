import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const view = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
const composer = readFileSync('../frontend/src/views/chat/composer/AIComposer.vue', 'utf8')
const generatedImage = readFileSync('../frontend/src/views/chat/ChatGeneratedImage.vue', 'utf8')
const api = readFileSync('../frontend/src/api/domains/chat.ts', 'utf8')
const types = readFileSync('../frontend/src/types/domain/chat.ts', 'utf8')

assert.match(types, /defaultImageModel:\s*ChatImageModel/)
assert.match(types, /type ChatImageModel\s*=\s*'gpt-image-2'/)
assert.match(api, /defaultImageModel\?:\s*ChatImageModel/)
assert.match(composer, /set-image-model/)
assert.match(view, /默认图像模型/)
assert.match(view, /GPT Image 2/)
assert.match(view, /defaultImageModel/)
assert.match(view, /chatApi\.updateConversation[\s\S]{0,300}defaultImageModel/, '保存必须持久化当前会话默认图像模型')
assert.match(view, /replaceConversation[\s\S]{0,420}defaultImageModel/, '保存前必须乐观更新会话摘要')
assert.doesNotMatch(generatedImage, /基于此图继续编辑|emit\('reuse'|EditOutlined/, '连续编辑必须由普通对话自动选择图像谱系，不提供生成图专用按钮')
assert.doesNotMatch(generatedImage, /base64|data:image/)

console.log('AI 问答默认图像模型命令回归通过')
