export type ChatMixedClipboardPart =
  | { type: 'text'; text: string }
  | { type: 'image'; file: File }

const textNodeType = 3
const elementNodeType = 1
const blockNodeNames = new Set([
  'address', 'article', 'aside', 'blockquote', 'div', 'dl', 'fieldset', 'figcaption',
  'figure', 'footer', 'form', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'header',
  'hr', 'li', 'main', 'nav', 'ol', 'p', 'pre', 'section', 'table', 'tr', 'ul'
])
const ignoredNodeNames = new Set(['head', 'meta', 'script', 'style', 'title'])

export function chatMixedClipboardParts(root: Node, imageFiles: readonly (File | undefined)[]): ChatMixedClipboardPart[] {
  const parts: ChatMixedClipboardPart[] = []
  let imageIndex = 0

  const appendText = (value: string): void => {
    if (!value) return
    const normalized = value.replace(/\u00a0/g, ' ')
    const previous = parts.at(-1)
    if (previous?.type === 'text') previous.text += normalized
    else parts.push({ type: 'text', text: normalized })
  }
  const appendBreak = (): void => {
    const previous = parts.at(-1)
    if (previous?.type === 'text' && !previous.text.endsWith('\n')) previous.text += '\n'
  }
  const visit = (node: Node): void => {
    if (node.nodeType === textNodeType) {
      appendText(node.nodeValue ?? '')
      return
    }
    if (node.nodeType !== elementNodeType && node !== root) return
    const name = node.nodeName.toLowerCase()
    if (ignoredNodeNames.has(name)) return
    if (name === 'br') {
      appendText('\n')
      return
    }
    if (name === 'img') {
      if (!clipboardImageConsumesFile(node)) return
      const file = imageFiles[imageIndex]
      imageIndex += 1
      if (file) parts.push({ type: 'image', file })
      return
    }
    for (const child of Array.from(node.childNodes)) visit(child)
    if (blockNodeNames.has(name)) appendBreak()
  }

  visit(root)
  while (imageIndex < imageFiles.length) {
    const file = imageFiles[imageIndex]
    if (file) parts.push({ type: 'image', file })
    imageIndex += 1
  }
  trimTextBoundaries(parts)
  return parts.filter((part) => part.type === 'image' || part.text.length > 0)
}

function clipboardImageConsumesFile(node: Node): boolean {
  const getAttribute = (node as Element).getAttribute
  if (typeof getAttribute !== 'function') return true
  const source = getAttribute.call(node, 'src')?.trim() ?? ''
  return !source || /^(?:blob|cid|data|file):/i.test(source)
}

function trimTextBoundaries(parts: ChatMixedClipboardPart[]): void {
  const firstText = parts.find((part): part is Extract<ChatMixedClipboardPart, { type: 'text' }> => part.type === 'text')
  const lastText = [...parts].reverse().find((part): part is Extract<ChatMixedClipboardPart, { type: 'text' }> => part.type === 'text')
  if (firstText) firstText.text = firstText.text.replace(/^\s+/, '')
  if (lastText) lastText.text = lastText.text.replace(/\s+$/, '')
}
