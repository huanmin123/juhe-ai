import type { EditorState } from '@tiptap/pm/state'

export type ChatComposerCommand =
  | { key: 'image'; kind: 'image'; label: string; description: string }
  | { key: 'parameters'; kind: 'generation'; label: string; description: string }
  | { key: 'image-model' | 'compact' | 'clear'; kind: 'conversation'; action: 'set-image-model' | 'compact-context' | 'clear-conversation'; label: string; description: string }

export const chatComposerCommands: ChatComposerCommand[] = [
  { key: 'image', kind: 'image', label: '添加图片', description: '从本机选择图片，或直接粘贴图片到当前消息。' },
  { key: 'parameters', kind: 'generation', label: '生成参数', description: '调整当前模型支持的温度、Top P、重复惩罚和回复长度等生成控制。' },
  { key: 'image-model', kind: 'conversation', action: 'set-image-model', label: '默认图像模型', description: '选择当前会话生成或编辑图片时使用的默认图像模型。' },
  { key: 'compact', kind: 'conversation', action: 'compact-context', label: '压缩上下文', description: '调用模型整理较早消息以释放上下文空间；会产生用量。' },
  { key: 'clear', kind: 'conversation', action: 'clear-conversation', label: '清空会话', description: '删除当前会话的全部消息并保留会话本身；此操作不可撤销。' }
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
