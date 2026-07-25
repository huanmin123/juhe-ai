import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')

assert.match(
  source,
  /<a-descriptions-item label="会话 ID">[\s\S]{0,500}detailConversation\.id[\s\S]{0,500}<\/a-descriptions-item>/,
  '会话详情必须显示完整会话 ID'
)
assert.match(source, /title="复制会话 ID"/, '会话 ID 必须提供中文复制提示')
assert.match(source, /aria-label="复制会话 ID"/, '会话 ID 复制按钮必须提供无障碍名称')
assert.match(source, /CopyOutlined/, '会话 ID 复制入口必须使用复制图标')
assert.match(
  source,
  /copyTextToClipboard\(detailConversation\.id,\s*'会话 ID 已复制'\)/,
  '复制会话 ID 必须复用公共剪贴板能力并提供成功反馈'
)
assert.match(source, /<a-descriptions-item label="工具能力">/, '会话详情必须展示工具能力')
assert.match(source, /tool\.available \? '可用' : '不可用'/, '工具能力必须明确展示可用或不可用')
assert.match(source, /!tool\.available && tool\.reason/, '工具不可用时必须展示后端返回的原因')
assert.match(
  source,
  /async function openDetails[\s\S]{0,500}chatApi\.getConversation\(item\.id\)/,
  '打开会话详情时必须重新读取当前工具能力，不能只使用列表快照'
)

console.log('AI 问答会话详情回归通过')
