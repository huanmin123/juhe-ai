<template>
  <div ref="root" class="chat-markdown" v-html="html" />
</template>

<script setup lang="ts">
import DOMPurify from 'dompurify'
import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import shell from 'highlight.js/lib/languages/shell'
import powershell from 'highlight.js/lib/languages/powershell'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import python from 'highlight.js/lib/languages/python'
import go from 'highlight.js/lib/languages/go'
import java from 'highlight.js/lib/languages/java'
import cpp from 'highlight.js/lib/languages/cpp'
import csharp from 'highlight.js/lib/languages/csharp'
import rust from 'highlight.js/lib/languages/rust'
import sql from 'highlight.js/lib/languages/sql'
import yaml from 'highlight.js/lib/languages/yaml'
import php from 'highlight.js/lib/languages/php'
import ruby from 'highlight.js/lib/languages/ruby'
import katex from 'katex'
import { marked, type RendererObject } from 'marked'
import { message as antdMessage } from 'ant-design-vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { writeTextToClipboard } from '@/shared/clipboard'
import { ChatCodeCopyLifecycle, ChatCodeCopyResetController } from './chatCodeCopyState'
import { isCompleteMarkdownCodeFence } from './chatMarkdownFences'
import { normalizeChatMarkdownMathDelimiters } from './chatMarkdownMath'
import { isCompleteStaticSvg, resolveChatSvgPreviewSize } from './chatSvgPreview'
import 'highlight.js/styles/github.css'
import 'katex/dist/katex.min.css'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('json', json)
hljs.registerLanguage('shell', shell)
hljs.registerLanguage('bash', shell)
hljs.registerLanguage('sh', shell)
hljs.registerLanguage('powershell', powershell)
hljs.registerLanguage('pwsh', powershell)
hljs.registerLanguage('ps1', powershell)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('css', css)
hljs.registerLanguage('python', python)
hljs.registerLanguage('py', python)
hljs.registerLanguage('go', go)
hljs.registerLanguage('golang', go)
hljs.registerLanguage('java', java)
hljs.registerLanguage('c', cpp)
hljs.registerLanguage('cpp', cpp)
hljs.registerLanguage('c++', cpp)
hljs.registerLanguage('csharp', csharp)
hljs.registerLanguage('c#', csharp)
hljs.registerLanguage('rust', rust)
hljs.registerLanguage('sql', sql)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('yml', yaml)
hljs.registerLanguage('php', php)
hljs.registerLanguage('ruby', ruby)

const props = defineProps<{ content: string }>()
const root = ref<HTMLElement>()
const mermaidPrefix = `chat-mermaid-${Date.now()}-${Math.random().toString(36).slice(2)}`
const codeCopyResetController = new ChatCodeCopyResetController(
  (callback, delay) => window.setTimeout(callback, delay),
  (timer) => window.clearTimeout(timer)
)
const codeCopyLifecycle = new ChatCodeCopyLifecycle(codeCopyResetController)
let renderVersion = 0

const renderer: RendererObject = {
  html({ text }) {
    return escapeHtml(text)
  },
  link({ href, title, tokens }) {
    const label = this.parser.parseInline(tokens)
    if (!isSafeHref(href)) return label
    const titleAttribute = title ? ` title="${escapeAttribute(title)}"` : ''
    return `<a href="${escapeAttribute(href)}"${titleAttribute} target="_blank" rel="noopener noreferrer nofollow">${label}</a>`
  },
  image({ href, title, text }) {
    if (/^attachment:\/\//i.test(href)) return ''
    if (!/^https:\/\//i.test(href)) return escapeHtml(text || '图片')
    const titleAttribute = title ? ` title="${escapeAttribute(title)}"` : ''
    return `<img src="${escapeAttribute(href)}" alt="${escapeAttribute(text || '')}"${titleAttribute} loading="lazy" referrerpolicy="no-referrer" />`
  },
  code({ text, lang, raw }) {
    const language = normalizeLanguage(lang)
    const fenceComplete = isCompleteMarkdownCodeFence(raw)
    if (language === 'mermaid' && !fenceComplete) return `<pre class="mermaid-pending"><code>${escapeHtml(text)}</code></pre>`
    if (language === 'mermaid') return `<pre class="mermaid-source"><code>${escapeHtml(text)}</code></pre>`
    if (language === 'svg' && !fenceComplete) return `<div class="chat-svg-pending"><pre><code>${escapeHtml(text)}</code></pre></div>`
    if (language === 'svg') return `<div class="chat-svg-source"><pre><code>${escapeHtml(text)}</code></pre></div>`
    const highlighted = language && hljs.getLanguage(language)
      ? hljs.highlight(text, { language }).value
      : escapeHtml(text)
    const label = escapeHtml(language || 'text')
    const codeClass = escapeAttribute(language || 'text')
    return `<div class="chat-code-block"><div class="chat-code-header"><span class="chat-code-language">${label}</span><button class="chat-code-copy" type="button" data-copy-code aria-label="复制代码">复制</button></div><pre><code class="hljs language-${codeClass}">${highlighted}</code></pre></div>`
  }
}

marked.use({ gfm: true, breaks: true, renderer })

const html = computed(() => DOMPurify.sanitize(enforceSafeImages(renderMathInTextNodes(marked.parse(normalizeChatMarkdownMathDelimiters(props.content), { async: false }) as string)), {
  ADD_ATTR: ['target', 'rel', 'loading', 'referrerpolicy', 'data-copy-code', 'aria-label'],
  FORBID_TAGS: ['style', 'script', 'iframe', 'object', 'embed']
}))

watch(html, async () => {
  const version = ++renderVersion
  await nextTick()
  if (version !== renderVersion || !root.value) return
  const sources = [...root.value.querySelectorAll<HTMLElement>('pre.mermaid-source')]
  if (sources.length) {
    const mermaid = (await import('mermaid')).default
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'neutral', htmlLabels: false })
    for (const [index, source] of sources.entries()) {
      if (version !== renderVersion) return
      const diagram = source.textContent ?? ''
      try {
        const { svg } = await mermaid.render(`${mermaidPrefix}-${version}-${index}`, diagram)
        if (version !== renderVersion || !source.isConnected) return
        const container = document.createElement('div')
        container.className = 'mermaid'
        container.innerHTML = DOMPurify.sanitize(svg, {
          USE_PROFILES: { svg: true, svgFilters: true },
          FORBID_TAGS: ['script', 'foreignObject'],
          FORBID_ATTR: ['onload', 'onclick', 'onerror']
        })
        source.replaceWith(container)
      } catch {
        if (version !== renderVersion || !source.isConnected) return
        const error = document.createElement('pre')
        error.className = 'mermaid-error'
        error.textContent = diagram
        source.replaceWith(error)
      }
    }
  }
  for (const source of [...root.value.querySelectorAll<HTMLElement>('.chat-svg-source')]) {
    if (version !== renderVersion || !source.isConnected) return
    const svg = source.querySelector('code')?.textContent ?? ''
    if (!isCompleteStaticSvg(svg)) continue
    try {
      const frame = document.createElement('iframe')
      const size = resolveChatSvgPreviewSize(svg)
      frame.className = 'chat-svg-preview'
      frame.setAttribute('sandbox', 'allow-scripts')
      frame.setAttribute('title', 'SVG 预览')
      frame.width = String(size.width)
      frame.height = String(size.height)
      frame.srcdoc = svg
      if (version !== renderVersion || !source.isConnected) return
      source.replaceWith(frame)
    } catch {
      const error = document.createElement('pre')
      error.className = 'chat-svg-error'
      error.textContent = svg
      source.replaceWith(error)
    }
  }
}, { immediate: true, flush: 'post' })

onMounted(() => {
  codeCopyLifecycle.activate()
  root.value?.addEventListener('click', handleRootClick)
})
onBeforeUnmount(() => {
  root.value?.removeEventListener('click', handleRootClick)
  codeCopyLifecycle.dispose()
})

async function handleRootClick(event: MouseEvent): Promise<void> {
  const target = event.target
  if (!(target instanceof Element)) return
  const button = target.closest<HTMLButtonElement>('button.chat-code-copy[data-copy-code]')
  if (!button || !root.value?.contains(button)) return
  const wrapper = button.closest<HTMLElement>('.chat-code-block')
  if (!wrapper) return
  const code = wrapper.querySelector<HTMLElement>(':scope > pre > code')
  if (!code) return
  await codeCopyLifecycle.copy(
    button,
    code.textContent ?? '',
    (value) => writeTextToClipboard(value),
    () => Boolean(root.value?.contains(button)),
    () => antdMessage.error('复制失败，请稍后重试')
  )
}

function renderMathInTextNodes(value: string): string {
  const template = document.createElement('template')
  template.innerHTML = value
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  while (walker.nextNode()) nodes.push(walker.currentNode as Text)
  for (const node of nodes) {
    if (node.parentElement?.closest('code, pre, a, button')) continue
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
function normalizeLanguage(value: string | undefined): string {
  const language = (value || '').trim().toLowerCase().replace(/[^a-z0-9_+#-]/g, '')
  return ({ py: 'python', 'c++': 'cpp', 'c#': 'csharp', sh: 'shell', yml: 'yaml' }[language] ?? language)
}
function escapeAttribute(value: string): string { return escapeHtml(value).replace(/`/g, '&#96;') }
function escapeHtml(value: string): string { return value.replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]!) }
</script>

<style scoped>
.chat-markdown { color: #202936; font-size: 14px; line-height: 1.72; overflow-wrap: anywhere; }
.chat-markdown :deep(p) { margin: 0 0 9px; }
.chat-markdown :deep(p:last-child) { margin-bottom: 0; }
.chat-markdown :deep(h1), .chat-markdown :deep(h2), .chat-markdown :deep(h3), .chat-markdown :deep(h4) { margin: 16px 0 7px; color: #182230; font-weight: 650; line-height: 1.35; }
.chat-markdown :deep(h1:first-child), .chat-markdown :deep(h2:first-child), .chat-markdown :deep(h3:first-child) { margin-top: 0; }
.chat-markdown :deep(h1) { font-size: 20px; }
.chat-markdown :deep(h2) { font-size: 17px; }
.chat-markdown :deep(h3), .chat-markdown :deep(h4) { font-size: 15px; }
.chat-markdown :deep(ul), .chat-markdown :deep(ol) { margin: 7px 0 9px; padding-left: 23px; }
.chat-markdown :deep(li) { margin: 2px 0; }
.chat-markdown :deep(.task-list-item) { list-style: none; }
.chat-markdown :deep(.task-list-item input) { margin: 0 7px 0 -21px; }
.chat-markdown :deep(blockquote) { margin: 10px 0; padding: 3px 0 3px 12px; color: #5f6c7b; border-left: 3px solid #d8dee8; }
.chat-markdown :deep(blockquote p) { margin: 0; }
.chat-markdown :deep(a) { color: #1677ff; text-underline-offset: 2px; }
.chat-markdown :deep(:not(pre) > code) { padding: 1px 5px; color: #9f1239; background: #f5f6f8; border: 1px solid #e8ebef; border-radius: 4px; font-size: .92em; }
.chat-markdown :deep(code) { font-family: "Cascadia Code", Consolas, monospace; }
.chat-markdown :deep(.chat-code-block) { max-width: 100%; margin: 10px 0; overflow: hidden; background: #f7f8fa; border: 1px solid #e3e7ec; border-radius: 6px; }
.chat-markdown :deep(.chat-code-header) { min-height: 30px; display: flex; align-items: center; justify-content: space-between; padding: 0 6px 0 12px; color: #7a8491; border-bottom: 1px solid #e6e9ed; font-size: 11px; }
.chat-markdown :deep(.chat-code-language) { text-transform: lowercase; }
.chat-markdown :deep(.chat-code-copy) { min-width: 48px; min-height: 28px; padding: 0 7px; color: #667085; background: transparent; border: 0; border-radius: 4px; cursor: pointer; font: inherit; }
.chat-markdown :deep(.chat-code-copy:hover), .chat-markdown :deep(.chat-code-copy:focus-visible) { color: #182230; background: #eceff3; outline: none; }
.chat-markdown :deep(.chat-code-block > pre) { max-width: 100%; margin: 0; padding: 12px 14px; overflow-x: auto; background: transparent; }
.chat-markdown :deep(table) { display: block; max-width: 100%; margin: 10px 0; overflow-x: auto; border-collapse: collapse; }
.chat-markdown :deep(th), .chat-markdown :deep(td) { padding: 6px 9px; border: 1px solid #dfe3e8; text-align: left; white-space: nowrap; }
.chat-markdown :deep(th) { background: #f7f8fa; font-weight: 600; }
.chat-markdown :deep(img) { display: block; max-width: 100%; max-height: 520px; margin: 10px 0; object-fit: contain; border-radius: 6px; }
.chat-markdown :deep(.katex-display) { max-width: 100%; overflow-x: auto; overflow-y: hidden; }
.chat-markdown :deep(.mermaid) { max-width: 100%; margin: 12px 0; overflow-x: auto; text-align: center; }
.chat-markdown :deep(.mermaid svg) { max-width: 100%; height: auto; }
.chat-markdown :deep(.mermaid-error) { max-height: 168px; margin: 10px 0; padding: 9px; overflow: auto; white-space: pre-wrap; color: #b42318; background: #fff7f6; border: 1px solid #ffd8d3; border-radius: 5px; }
.chat-markdown :deep(.chat-svg-preview) { display: block; max-width: 100%; margin: 10px 0; border: 1px solid #e3e7ec; border-radius: 6px; background: #fff; }
.chat-markdown :deep(.chat-svg-error) { max-height: 220px; margin: 10px 0; padding: 9px; overflow: auto; white-space: pre-wrap; color: #b42318; background: #fff7f6; border: 1px solid #ffd8d3; border-radius: 5px; }
</style>
