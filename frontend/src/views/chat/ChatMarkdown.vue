<template>
  <div ref="root" class="chat-markdown" v-html="html" />
</template>

<script setup lang="ts">
import DOMPurify from 'dompurify'
import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import shell from 'highlight.js/lib/languages/shell'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import katex from 'katex'
import { marked, type RendererObject } from 'marked'
import { computed, nextTick, ref, watch } from 'vue'
import 'highlight.js/styles/github.css'
import 'katex/dist/katex.min.css'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('json', json)
hljs.registerLanguage('shell', shell)
hljs.registerLanguage('bash', shell)
hljs.registerLanguage('html', xml)

const props = defineProps<{ content: string }>()
const root = ref<HTMLElement>()
let renderVersion = 0

const renderer: RendererObject = {
  link({ href, title, tokens }) {
    const label = this.parser.parseInline(tokens)
    if (!isSafeHref(href)) return label
    const titleAttribute = title ? ` title="${escapeAttribute(title)}"` : ''
    return `<a href="${escapeAttribute(href)}"${titleAttribute} target="_blank" rel="noopener noreferrer nofollow">${label}</a>`
  },
  image({ href, title, text }) {
    if (!/^https:\/\//i.test(href)) return escapeHtml(text || '图片')
    const titleAttribute = title ? ` title="${escapeAttribute(title)}"` : ''
    return `<img src="${escapeAttribute(href)}" alt="${escapeAttribute(text || '')}"${titleAttribute} loading="lazy" referrerpolicy="no-referrer" />`
  },
  code({ text, lang }) {
    const language = (lang || '').trim().toLowerCase()
    if (language === 'mermaid') return `<pre class="mermaid-source"><code>${escapeHtml(text)}</code></pre>`
    const highlighted = language && hljs.getLanguage(language)
      ? hljs.highlight(text, { language }).value
      : escapeHtml(text)
    return `<pre><code class="hljs language-${escapeAttribute(language || 'text')}">${highlighted}</code></pre>`
  }
}

marked.use({ gfm: true, breaks: true, renderer })

const html = computed(() => DOMPurify.sanitize(enforceSafeImages(renderMathInTextNodes(marked.parse(props.content, { async: false }) as string)), {
  ADD_ATTR: ['target', 'rel', 'loading', 'referrerpolicy'],
  FORBID_TAGS: ['style', 'script', 'iframe', 'object', 'embed']
}))

watch(html, async () => {
  const version = ++renderVersion
  await nextTick()
  if (version !== renderVersion || !root.value) return
  const sources = [...root.value.querySelectorAll<HTMLElement>('pre.mermaid-source')]
  if (!sources.length) return
  const mermaid = (await import('mermaid')).default
  mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'neutral' })
  for (const source of sources) {
    if (version !== renderVersion) return
    const container = document.createElement('div')
    container.className = 'mermaid'
    container.textContent = source.textContent ?? ''
    source.replaceWith(container)
    try { await mermaid.run({ nodes: [container], suppressErrors: true }) } catch { container.className = 'mermaid-error'; container.textContent = source.textContent ?? '' }
  }
}, { immediate: true, flush: 'post' })

function renderMathInTextNodes(value: string): string {
  const template = document.createElement('template')
  template.innerHTML = value
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  while (walker.nextNode()) nodes.push(walker.currentNode as Text)
  for (const node of nodes) {
    if (node.parentElement?.closest('code, pre, a')) continue
    const rendered = renderMathText(node.data)
    if (rendered === escapeHtml(node.data)) continue
    const replacement = document.createElement('template')
    replacement.innerHTML = rendered
    node.replaceWith(replacement.content)
  }
  return template.innerHTML
}
function enforceSafeImages(value: string): string {
  const template = document.createElement('template')
  template.innerHTML = value
  for (const image of template.content.querySelectorAll('img')) {
    const source = image.getAttribute('src') ?? ''
    if (/^https:\/\//i.test(source)) continue
    image.replaceWith(document.createTextNode(image.getAttribute('alt') || '图片'))
  }
  return template.innerHTML
}
function renderMathText(value: string): string {
  return escapeHtml(value)
    .replace(/\$\$([\s\S]+?)\$\$/g, (_match, source: string) => safeKatex(decodeHtml(source), true))
    .replace(/(^|[^\\])\$([^\n$]+?)\$/g, (_match, prefix: string, source: string) => `${prefix}${safeKatex(decodeHtml(source), false)}`)
}
function safeKatex(source: string, displayMode: boolean): string { try { return katex.renderToString(source, { displayMode, throwOnError: false, strict: 'warn' }) } catch { return escapeHtml(source) } }
function decodeHtml(value: string): string { const textarea = document.createElement('textarea'); textarea.innerHTML = value; return textarea.value }
function isSafeHref(value: string): boolean { return /^(https?:|mailto:)/i.test(value) }
function escapeAttribute(value: string): string { return escapeHtml(value).replace(/`/g, '&#96;') }
function escapeHtml(value: string): string { return value.replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]!) }
</script>

<style scoped>
.chat-markdown { color: #1f2937; font-size: 14px; line-height: 1.75; overflow-wrap: anywhere; }
.chat-markdown :deep(p:first-child) { margin-top: 0; }
.chat-markdown :deep(p:last-child) { margin-bottom: 0; }
.chat-markdown :deep(pre) { max-width: 100%; overflow: auto; margin: 10px 0; padding: 12px 14px; background: #f6f8fa; border: 1px solid #e5e7eb; border-radius: 6px; }
.chat-markdown :deep(code) { font-family: "Cascadia Code", Consolas, monospace; }
.chat-markdown :deep(table) { display: block; max-width: 100%; overflow-x: auto; border-collapse: collapse; }
.chat-markdown :deep(th), .chat-markdown :deep(td) { padding: 7px 10px; border: 1px solid #d9d9d9; }
.chat-markdown :deep(img) { display: block; max-width: 100%; max-height: 520px; margin: 10px 0; object-fit: contain; border-radius: 6px; }
.chat-markdown :deep(.katex-display) { overflow-x: auto; overflow-y: hidden; }
.chat-markdown :deep(.mermaid) { overflow-x: auto; margin: 12px 0; text-align: center; }
.chat-markdown :deep(.mermaid-error) { white-space: pre-wrap; color: #b42318; }
</style>
