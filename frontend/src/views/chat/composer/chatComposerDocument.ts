import type { JSONContent } from '@tiptap/core'
import { maxChatImageCount } from './chatImageSelection'

export type ChatInputBlock =
  | { type: 'input_text'; text: string }
  | { type: 'input_image'; assetId: string }

const maxInputBytes = 192 * 1024

export function composerTextToDocument(content: string): JSONContent {
  const normalized = content.replace(/\r\n?/g, '\n')
  const inline: JSONContent[] = []
  const lines = normalized.split('\n')
  lines.forEach((line, index) => {
    if (line) inline.push({ type: 'text', text: line })
    if (index < lines.length - 1) inline.push({ type: 'hardBreak' })
  })
  return { type: 'doc', content: [{ type: 'paragraph', ...(inline.length ? { content: inline } : {}) }] }
}

export function composerDocumentToBlocks(document: JSONContent): ChatInputBlock[] {
  const blocks: ChatInputBlock[] = []
  const text: string[] = []
  serialize(document, text, blocks)
  flushText(text, blocks)
  const normalizedBlocks = mergeAdjacentInputTextBlocks(blocks)
  const imageCount = normalizedBlocks.filter((item) => item.type === 'input_image').length
  if (imageCount > maxChatImageCount) throw new Error(`每条消息最多 ${maxChatImageCount} 张图片`)
  const textBytes = normalizedBlocks.filter((item): item is Extract<ChatInputBlock, { type: 'input_text' }> => item.type === 'input_text')
    .reduce((total, item) => total + new TextEncoder().encode(item.text).byteLength, 0)
  if (textBytes > maxInputBytes) throw new Error('消息内容超过 192 KiB 上限')
  return normalizedBlocks.filter((item) => item.type !== 'input_text' || item.text.length > 0)
}

function mergeAdjacentInputTextBlocks(blocks: ChatInputBlock[]): ChatInputBlock[] {
  const merged: ChatInputBlock[] = []
  for (const block of blocks) {
    const previous = merged.at(-1)
    if (block.type === 'input_text' && previous?.type === 'input_text') {
      previous.text = `${previous.text}\n\n${block.text}`
    } else {
      merged.push(block)
    }
  }
  return merged
}

function flushText(text: string[], blocks: ChatInputBlock[]): void {
  if (!text.length) return
  const value = text.join('').trimEnd()
  if (value) blocks.push({ type: 'input_text', text: value })
  text.length = 0
}

function serialize(node: JSONContent, text: string[], blocks: ChatInputBlock[]): void {
  if (node.type === 'chatImageAttachment') {
    const assetId = typeof node.attrs?.assetId === 'string' ? node.attrs.assetId.trim() : ''
    if (!assetId || node.attrs?.uploadStatus !== 'uploaded') throw new Error('图片尚未上传完成')
    flushText(text, blocks)
    blocks.push({ type: 'input_image', assetId })
    return
  }
  if (node.type === 'text' && typeof node.text === 'string') {
    text.push(serializeMarkedText(node))
    return
  }
  if (node.type === 'hardBreak') { text.push('\n'); return }
  if (node.type === 'codeBlock') {
    text.push('```' + (typeof node.attrs?.language === 'string' && node.attrs.language ? node.attrs.language : '') + '\n')
    for (const child of node.content ?? []) serialize(child, text, blocks)
    text.push('\n```\n\n')
    return
  }
  if (node.type === 'bulletList' || node.type === 'orderedList') {
    serializeList(node, text, blocks, 0)
    flushText(text, blocks)
    return
  }
  if (node.type === 'heading') {
    const level = typeof node.attrs?.level === 'number' ? Math.min(6, Math.max(1, node.attrs.level)) : 1
    text.push(`${'#'.repeat(level)} `)
  }
  if (node.type === 'blockquote') text.push('> ')
  for (const child of node.content ?? []) serialize(child, text, blocks)
  if (node.type === 'paragraph' || node.type === 'heading') flushText(text, blocks)
  else if (['blockquote', 'codeBlock'].includes(node.type ?? '')) text.push('\n')
}

function serializeList(node: JSONContent, text: string[], blocks: ChatInputBlock[], depth: number): void {
  const ordered = node.type === 'orderedList'
  const start = ordered && Number.isInteger(node.attrs?.start) ? Number(node.attrs?.start) : 1
  ;(node.content ?? []).forEach((item, index) => {
    if (item.type !== 'listItem') return
    text.push(`${'    '.repeat(depth)}${ordered ? `${start + index}.` : '-'} `)
    for (const child of item.content ?? []) {
      if (child.type === 'bulletList' || child.type === 'orderedList') {
        serializeList(child, text, blocks, depth + 1)
        continue
      }
      if (child.type === 'paragraph') {
        for (const inline of child.content ?? []) serialize(inline, text, blocks)
        text.push('\n')
        continue
      }
      serialize(child, text, blocks)
    }
  })
}

function serializeMarkedText(node: JSONContent): string {
  let value = node.text ?? ''
  for (const mark of node.marks ?? []) {
    if (mark.type === 'bold') value = `**${value}**`
    else if (mark.type === 'italic') value = `*${value}*`
    else if (mark.type === 'strike') value = `~~${value}~~`
    else if (mark.type === 'code') value = `\`${value}\``
    else if (mark.type === 'link' && typeof mark.attrs?.href === 'string') value = `[${value}](${mark.attrs.href})`
    else if (mark.type === 'underline') value = `<u>${value}</u>`
  }
  return value
}
