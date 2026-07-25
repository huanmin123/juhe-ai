import type { EditorState } from '@tiptap/pm/state'

export type ChatComposerCommand =
  | { key: 'clear-input' | 'code'; kind: 'editor'; insert: string; label: string; description: string }
  | { key: 'image'; kind: 'image'; label: string; description: string }
  | { key: 'parameters'; kind: 'generation'; label: string; description: string }
  | { key: 'image-model' | 'compact' | 'clear'; kind: 'conversation'; action: 'set-image-model' | 'compact-context' | 'clear-conversation'; label: string; description: string }

export const chatComposerCommands: ChatComposerCommand[] = [
  { key: 'clear-input', kind: 'editor', label: '清空输入', description: '清除当前编辑内容', insert: '' },
  { key: 'code', kind: 'editor', label: '代码块', description: '插入 Markdown 代码块', insert: '\n```\n\n```' },
  { key: 'image', kind: 'image', label: '添加图片', description: '粘贴或选择图片' },
  { key: 'parameters', kind: 'generation', label: '生成参数', description: '设置温度、Top P、重复惩罚和回复长度' },
  { key: 'image-model', kind: 'conversation', action: 'set-image-model', label: '默认图像模型', description: '设置当前会话的图片生成模型' },
  { key: 'compact', kind: 'conversation', action: 'compact-context', label: '压缩上下文', description: '整理当前会话的较早内容' },
  { key: 'clear', kind: 'conversation', action: 'clear-conversation', label: '清空会话', description: '清除消息但保留会话壳' }
]

export function filterChatComposerCommands(query: string): ChatComposerCommand[] {
  const normalized = query.trim().toLowerCase()
  return normalized ? chatComposerCommands.filter((item) => `${item.key} ${item.label} ${item.description}`.toLowerCase().includes(normalized)) : chatComposerCommands
}

export function moveChatComposerCommandIndex(index: number, direction: 1 | -1, count: number): number {
  return count > 0 ? (index + direction + count) % count : 0
}

export function findChatComposerCommandQuery(
  state: EditorState
): { query: string; range: { from: number; to: number } } | undefined {
  const { selection } = state
  const { $from } = selection
  if (!selection.empty || !$from.parent.isTextblock) return undefined

  let textBeforeCursor = ''
  const contentOffsets: number[] = []
  $from.parent.forEach((node, offset) => {
    const availableSize = Math.min(node.nodeSize, $from.parentOffset - offset)
    if (availableSize <= 0) return
    if (node.isText) {
      const text = node.text?.slice(0, availableSize) ?? ''
      for (let index = 0; index < text.length; index += 1) {
        textBeforeCursor += text[index]
        contentOffsets.push(offset + index)
      }
      return
    }
    if (node.type.name === 'hardBreak' || node.type.name === 'hard_break') {
      textBeforeCursor += '\n'
      contentOffsets.push(offset)
      return
    }
    if (node.isInline && (node.isLeaf || node.isAtom)) {
      textBeforeCursor += ' '
      contentOffsets.push(offset)
    }
  })
  const match = textBeforeCursor.match(/(?:^|\s)\/([^\s/]*)$/)
  if (!match) return undefined

  const query = match[1]
  const slashIndex = match.index! + match[0].length - query.length - 1
  const slashOffset = contentOffsets[slashIndex]
  if (slashOffset === undefined) return undefined
  return {
    query,
    range: { from: $from.start() + slashOffset, to: $from.pos }
  }
}
