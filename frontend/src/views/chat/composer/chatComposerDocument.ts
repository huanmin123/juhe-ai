import type { JSONContent } from '@tiptap/core'

export type ChatInputBlock =
  | { type: 'input_text'; text: string }
  | { type: 'input_image'; assetId: string; previewUrl?: string }

const maxInputBytes = 192 * 1024

export function composerDocumentToBlocks(document: JSONContent): ChatInputBlock[] {
  const blocks: ChatInputBlock[] = []
  const text: string[] = []
  serialize(document, text, blocks)
  flushText(text, blocks)
  const textBytes = blocks.filter((item): item is Extract<ChatInputBlock, { type: 'input_text' }> => item.type === 'input_text')
    .reduce((total, item) => total + new TextEncoder().encode(item.text).byteLength, 0)
  if (textBytes > maxInputBytes) throw new Error('消息内容超过 192 KiB 上限')
  return blocks.filter((item) => item.type !== 'input_text' || item.text.length > 0)
}

function flushText(text: string[], blocks: ChatInputBlock[]): void {
  if (!text.length) return
  const value = text.join('').replace(/\n{3,}/g, '\n\n').trimEnd()
  if (value) blocks.push({ type: 'input_text', text: value })
  text.length = 0
}

function serialize(node: JSONContent, text: string[], blocks: ChatInputBlock[]): void {
  if (node.type === 'image' && typeof node.attrs?.assetId === 'string') {
    flushText(text, blocks)
    blocks.push({ type: 'input_image', assetId: node.attrs.assetId, previewUrl: typeof node.attrs.previewUrl === 'string' ? node.attrs.previewUrl : undefined })
    return
  }
  if (node.type === 'chatImageAttachment' && typeof node.attrs?.assetId === 'string') {
    flushText(text, blocks)
    blocks.push({ type: 'input_image', assetId: node.attrs.assetId, previewUrl: typeof node.attrs.previewUrl === 'string' ? node.attrs.previewUrl : undefined })
    return
  }
  if (node.type === 'text' && typeof node.text === 'string') {
    text.push(node.text)
    return
  }
  if (node.type === 'hardBreak') { text.push('\n'); return }
  if (node.type === 'codeBlock') {
    text.push('```' + (typeof node.attrs?.language === 'string' && node.attrs.language ? node.attrs.language : '') + '\n')
    for (const child of node.content ?? []) serialize(child, text, blocks)
    text.push('\n```\n\n')
    return
  }
  if (node.type === 'listItem') text.push('- ')
  for (const child of node.content ?? []) serialize(child, text, blocks)
  if (node.type === 'paragraph' || node.type === 'heading') flushText(text, blocks)
  else if (['listItem', 'blockquote', 'codeBlock'].includes(node.type ?? '')) text.push('\n')
}
