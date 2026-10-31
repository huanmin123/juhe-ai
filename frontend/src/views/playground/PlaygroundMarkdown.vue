<template>
  <div class="playground-markdown" v-html="html" />
</template>

<script setup lang="ts">
// 演练场消息 markdown 渲染：复用项目共享依赖 marked + DOMPurify + highlight.js，
// 不引用 views/chat 组件（旧 AI 对话面整体退役，演练场对其零接触）。
// 试玩台只需最小渲染面：原始 HTML 转义、安全链接（新窗口）、https 图片、代码高亮。
import DOMPurify from 'dompurify'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import python from 'highlight.js/lib/languages/python'
import shell from 'highlight.js/lib/languages/shell'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import hljs from 'highlight.js/lib/core'
import { marked, type RendererObject } from 'marked'
import { computed } from 'vue'
import 'highlight.js/styles/github.css'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('json', json)
hljs.registerLanguage('shell', shell)
hljs.registerLanguage('bash', shell)
hljs.registerLanguage('sh', shell)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('python', python)
hljs.registerLanguage('py', python)
hljs.registerLanguage('go', go)
hljs.registerLanguage('golang', go)

const props = defineProps<{ content: string }>()

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
    if (!/^https:\/\//i.test(href)) return escapeHtml(text || '图片')
    const titleAttribute = title ? ` title="${escapeAttribute(title)}"` : ''
    return `<img src="${escapeAttribute(href)}" alt="${escapeAttribute(text || '')}"${titleAttribute} loading="lazy" referrerpolicy="no-referrer" />`
  },
  code({ text, lang }) {
    const language = normalizeLanguage(lang)
    const highlighted = language && hljs.getLanguage(language)
      ? hljs.highlight(text, { language }).value
      : escapeHtml(text)
    const label = escapeHtml(language || 'text')
    const codeClass = escapeAttribute(language || 'text')
    return `<div class="playground-code-block"><div class="playground-code-language">${label}</div><pre><code class="hljs language-${codeClass}">${highlighted}</code></pre></div>`
  }
}

marked.use({ gfm: true, breaks: true, renderer })

const html = computed(() => DOMPurify.sanitize(marked.parse(props.content, { async: false }) as string, {
  ADD_ATTR: ['target', 'rel', 'loading', 'referrerpolicy'],
  FORBID_TAGS: ['style', 'script', 'iframe', 'object', 'embed']
}))

function isSafeHref(value: string): boolean {
  return /^(https?:|mailto:)/i.test(value)
}

function normalizeLanguage(value: string | undefined): string {
  const language = (value || '').trim().toLowerCase().replace(/[^a-z0-9_+#-]/g, '')
  return ({ py: 'python', 'c++': 'cpp', 'c#': 'csharp', sh: 'shell', yml: 'yaml' }[language] ?? language)
}

function escapeAttribute(value: string): string {
  return escapeHtml(value).replace(/`/g, '&#96;')
}

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]!)
}
</script>

<style scoped>
.playground-markdown { color: var(--juhe-fg); font-size: 14px; line-height: 1.72; overflow-wrap: anywhere; }
.playground-markdown :deep(p) { margin: 0 0 9px; }
.playground-markdown :deep(p:last-child) { margin-bottom: 0; }
.playground-markdown :deep(h1), .playground-markdown :deep(h2), .playground-markdown :deep(h3), .playground-markdown :deep(h4) { margin: 16px 0 7px; color: var(--juhe-fg); font-weight: 650; line-height: 1.35; }
.playground-markdown :deep(h1:first-child), .playground-markdown :deep(h2:first-child), .playground-markdown :deep(h3:first-child) { margin-top: 0; }
.playground-markdown :deep(h1) { font-size: 20px; }
.playground-markdown :deep(h2) { font-size: 17px; }
.playground-markdown :deep(h3), .playground-markdown :deep(h4) { font-size: 15px; }
.playground-markdown :deep(ul), .playground-markdown :deep(ol) { margin: 7px 0 9px; padding-left: 23px; }
.playground-markdown :deep(li) { margin: 2px 0; }
.playground-markdown :deep(blockquote) { margin: 10px 0; padding: 3px 0 3px 12px; color: var(--juhe-muted); border-left: 3px solid var(--juhe-border); }
.playground-markdown :deep(blockquote p) { margin: 0; }
.playground-markdown :deep(a) { color: var(--juhe-accent); text-underline-offset: 2px; }
.playground-markdown :deep(:not(pre) > code) { padding: 1px 5px; color: var(--juhe-coral); background: var(--juhe-surface-soft); border: 1px solid var(--juhe-border); border-radius: 4px; font-size: .92em; }
.playground-markdown :deep(code) { font-family: "Cascadia Code", Consolas, monospace; }
.playground-markdown :deep(.playground-code-block) { max-width: 100%; margin: 10px 0; overflow: hidden; background: var(--juhe-surface-soft); border: 1px solid var(--juhe-border); border-radius: 6px; }
.playground-markdown :deep(.playground-code-language) { padding: 5px 12px; color: var(--juhe-muted); border-bottom: 1px solid var(--juhe-border); text-transform: lowercase; font-size: 11px; }
.playground-markdown :deep(.playground-code-block > pre) { max-width: 100%; max-height: 420px; margin: 0; padding: 12px 14px; overflow: auto; background: transparent; }
.playground-markdown :deep(table) { display: block; max-width: 100%; margin: 10px 0; overflow-x: auto; border-collapse: collapse; }
.playground-markdown :deep(th), .playground-markdown :deep(td) { padding: 6px 9px; border: 1px solid var(--juhe-border); text-align: left; white-space: nowrap; }
.playground-markdown :deep(th) { background: var(--juhe-surface-soft); font-weight: 600; }
.playground-markdown :deep(img) { display: block; max-width: 100%; max-height: 420px; margin: 10px 0; object-fit: contain; border-radius: 6px; }
</style>
