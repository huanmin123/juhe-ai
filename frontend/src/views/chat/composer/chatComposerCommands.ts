export interface ChatComposerCommand {
  key: string
  label: string
  description: string
  insert: string
}

export const chatComposerCommands: ChatComposerCommand[] = [
  { key: 'clear', label: '清空输入', description: '清除当前编辑内容', insert: '' },
  { key: 'code', label: '代码块', description: '插入 Markdown 代码块', insert: '\n```\n\n```' },
  { key: 'list', label: '无序列表', description: '插入 Markdown 列表', insert: '\n- ' },
  { key: 'image', label: '添加图片', description: '粘贴或选择图片', insert: '' }
]

export function filterChatComposerCommands(query: string): ChatComposerCommand[] {
  const normalized = query.trim().toLowerCase()
  return normalized ? chatComposerCommands.filter((item) => `${item.key} ${item.label} ${item.description}`.toLowerCase().includes(normalized)) : chatComposerCommands
}
