<template>
  <div class="readonly-code-viewer" :class="{ 'readonly-code-viewer-loading': formatting, 'readonly-code-viewer-search-open': searchOpen }">
    <div v-if="showToolbar" class="readonly-code-viewer-toolbar">
      <div class="readonly-code-viewer-meta">
        <strong v-if="title" class="readonly-code-viewer-title">{{ title }}</strong>
        <a-tag :color="isJson ? 'blue' : 'default'">{{ languageLabel }}</a-tag>
        <span>{{ formattedSizeText }}</span>
        <span v-if="formatError" class="readonly-code-viewer-warning">{{ formatError }}</span>
      </div>
      <a-space size="small">
        <a-tooltip title="搜索当前内容">
          <a-button size="small" :disabled="!displayText" @click="openSearch">
            <template #icon><search-outlined /></template>
          </a-button>
        </a-tooltip>
        <a-tooltip title="复制当前内容">
          <a-button size="small" :disabled="!displayText" @click="copyDisplayText">
            <template #icon><copy-outlined /></template>
          </a-button>
        </a-tooltip>
      </a-space>
    </div>
    <div
      v-if="searchOpen"
      class="readonly-code-viewer-search"
      :class="{ 'readonly-code-viewer-search-attached': showToolbar || attachedToolbar }"
    >
      <a-input
        ref="searchInput"
        v-model:value="searchKeyword"
        allow-clear
        size="small"
        placeholder="搜索当前内容"
        @press-enter="jumpToNextMatchFromInput"
      >
        <template #prefix><search-outlined /></template>
      </a-input>
      <a-tooltip title="上一个匹配">
        <a-button size="small" :disabled="!canSearch" @click="jumpToPreviousMatch">
          <template #icon><arrow-up-outlined /></template>
        </a-button>
      </a-tooltip>
      <a-tooltip title="下一个匹配">
        <a-button size="small" :disabled="!canSearch" @click="jumpToNextMatch">
          <template #icon><arrow-down-outlined /></template>
        </a-button>
      </a-tooltip>
      <span class="readonly-code-viewer-search-status">{{ searchStatusText }}</span>
      <a-tooltip title="关闭搜索">
        <a-button size="small" @click="closeSearch">
          <template #icon><close-outlined /></template>
        </a-button>
      </a-tooltip>
    </div>
    <a-spin :spinning="formatting">
      <div ref="editorRoot" class="readonly-code-viewer-editor" />
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import { ArrowDownOutlined, ArrowUpOutlined, CloseOutlined, CopyOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { json } from '@codemirror/lang-json'
import { bracketMatching, defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorState } from '@codemirror/state'
import { SearchQuery, findNext, findPrevious, getSearchQuery, highlightSelectionMatches, search, setSearchQuery } from '@codemirror/search'
import { EditorView, highlightActiveLine, highlightSpecialChars, keymap, lineNumbers } from '@codemirror/view'
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'

import { copyTextToClipboard } from '@/shared/clipboard'

const jsonAutoFormatMaxChars = 256 * 1024

const props = withDefaults(defineProps<{
  attachedToolbar?: boolean
  contentType?: string
  emptyText?: string
  height?: number
  showToolbar?: boolean
  text?: string
  title?: string
}>(), {
  attachedToolbar: false,
  contentType: '',
  emptyText: '',
  height: 420,
  showToolbar: true,
  text: '',
  title: ''
})

const editorRoot = ref<HTMLElement>()
const searchInput = ref<{ focus: () => void }>()
const displayText = ref('')
const formatError = ref('')
const formatting = ref(false)
const searchKeyword = ref('')
const searchOpen = ref(false)
const searchNoMatch = ref(false)

let editorView: EditorView | undefined
let formatTaskId = 0
let formatTimer: ReturnType<typeof window.setTimeout> | undefined
let componentDisposed = false
let componentActive = true

const hasText = computed(() => Boolean(props.text))
const sourceText = computed(() => props.text || props.emptyText)
const isJson = computed(() => hasText.value
  ? isJsonLike(props.contentType, props.text)
  : isJsonLike('', props.emptyText))
const languageLabel = computed(() => isJson.value ? 'JSON' : 'Text')
const formattedSizeText = computed(() => formatBytes(new Blob([displayText.value]).size))
const canSearch = computed(() => Boolean(displayText.value && searchKeyword.value.trim()))
const searchStatusText = computed(() => {
  if (!searchKeyword.value.trim()) return '输入关键词后按 Enter'
  if (searchNoMatch.value) return '没有匹配'
  return '按 Enter 查找下一个'
})

watch(
  () => [props.text, props.contentType, props.emptyText] as const,
  () => {
    scheduleFormat()
  },
  { immediate: true }
)

watch(
  () => props.height,
  () => {
    void nextTick(updateEditor)
  }
)

watch(searchKeyword, () => {
  searchNoMatch.value = false
  syncSearchQueryToEditor()
})

onMounted(() => {
  componentDisposed = false
  componentActive = true
  updateEditor()
})

onActivated(() => {
  componentActive = true
  void scheduleFormat()
  void nextTick(updateEditor)
})

onDeactivated(() => {
  componentActive = false
  cancelScheduledFormat()
  destroyEditor()
})

onBeforeUnmount(() => {
  componentDisposed = true
  componentActive = false
  cancelScheduledFormat()
  destroyEditor()
})

async function scheduleFormat(): Promise<void> {
  const taskId = ++formatTaskId
  formatting.value = true
  await nextTick()
  if (componentDisposed || !componentActive) return
  cancelScheduledFormat()
  formatTimer = window.setTimeout(() => {
    formatTimer = undefined
    if (componentDisposed || !componentActive || taskId !== formatTaskId) return
    const result = formatForDisplay(sourceText.value || '', isJson.value)
    displayText.value = result.text
    formatError.value = result.error
    formatting.value = false
    void nextTick(updateEditor)
  }, 0)
}

function cancelScheduledFormat(): void {
  if (formatTimer && typeof window !== 'undefined') {
    window.clearTimeout(formatTimer)
    formatTimer = undefined
  }
}

function formatForDisplay(text: string, shouldFormatJson: boolean): { text: string; error: string } {
  if (!text) return { text: '', error: '' }
  if (!shouldFormatJson) return { text, error: '' }
  if (text.length > jsonAutoFormatMaxChars) {
    return { text, error: '内容较大，已按原文展示' }
  }
  try {
    return { text: JSON.stringify(JSON.parse(text), null, 2), error: '' }
  } catch {
    return { text, error: 'JSON 格式化失败，已按原文展示' }
  }
}

function updateEditor(): void {
  if (componentDisposed || !componentActive) return
  if (!editorRoot.value) return
  const state = createEditorState(displayText.value, isJson.value)
  if (!editorView) {
    editorView = new EditorView({
      parent: editorRoot.value,
      state
    })
    syncSearchQueryToEditor()
    return
  }
  editorView.setState(state)
  syncSearchQueryToEditor()
}

function destroyEditor(): void {
  editorView?.destroy()
  editorView = undefined
}

function createEditorState(doc: string, shouldUseJson: boolean): EditorState {
  const editorHeight = `${props.height}px`
  const editorBorderRadius = props.showToolbar || props.attachedToolbar ? '0 0 8px 8px' : '8px'
  return EditorState.create({
    doc,
    extensions: [
      lineNumbers(),
      highlightSpecialChars(),
      highlightActiveLine(),
      bracketMatching(),
      syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
      search({ top: true }),
      highlightSelectionMatches(),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      EditorView.lineWrapping,
      keymap.of([
        {
          key: 'Mod-f',
          run: () => {
            void openSearch()
            return true
          }
        },
        {
          key: 'F3',
          run: () => {
            jumpToNextMatch()
            return true
          }
        },
        {
          key: 'Shift-F3',
          run: () => {
            jumpToPreviousMatch()
            return true
          }
        }
      ]),
      EditorView.theme({
        '&': {
          height: editorHeight,
          border: '1px solid #e2e8f0',
          borderRadius: editorBorderRadius,
          fontSize: '12px'
        },
        '&.cm-focused': {
          outline: 'none'
        },
        '.cm-scroller': {
          fontFamily: "Consolas, 'Courier New', monospace"
        },
        '.cm-content': {
          minHeight: editorHeight
        },
        '.cm-gutters': {
          backgroundColor: '#f8fafc',
          borderRight: '1px solid #e2e8f0',
          color: '#64748b'
        },
        '.cm-activeLine': {
          backgroundColor: '#f8fafc'
        },
        '.cm-activeLineGutter': {
          backgroundColor: '#eef6ff'
        }
      }),
      shouldUseJson ? json() : []
    ]
  })
}

async function openSearch(): Promise<void> {
  searchOpen.value = true
  await nextTick()
  searchInput.value?.focus()
  syncSearchQueryToEditor()
}

function closeSearch(): void {
  searchOpen.value = false
  searchNoMatch.value = false
  searchKeyword.value = ''
  syncSearchQueryToEditor()
  editorView?.focus()
}

function syncSearchQueryToEditor(): void {
  if (!editorView) return
  const query = createSearchQuery()
  const currentQuery = getSearchQuery(editorView.state)
  if (currentQuery.eq(query)) return
  editorView.dispatch({ effects: setSearchQuery.of(query) })
}

function createSearchQuery(): SearchQuery {
  return new SearchQuery({
    search: searchKeyword.value.trim(),
    caseSensitive: false,
    literal: true,
    regexp: false,
    wholeWord: false
  })
}

function jumpToNextMatch(): void {
  jumpToMatch('next', true)
}

function jumpToNextMatchFromInput(): void {
  jumpToMatch('next', false)
}

function jumpToPreviousMatch(): void {
  jumpToMatch('previous', true)
}

function jumpToMatch(direction: 'next' | 'previous', focusEditor: boolean): void {
  if (!editorView || !canSearch.value) {
    void openSearch()
    return
  }
  syncSearchQueryToEditor()
  const matched = direction === 'next'
    ? findNext(editorView)
    : findPrevious(editorView)
  if (!matched) {
    searchNoMatch.value = true
    return
  }
  searchNoMatch.value = false
  if (focusEditor) {
    editorView.focus()
  } else {
    void nextTick(() => searchInput.value?.focus())
  }
}

async function copyDisplayText(): Promise<void> {
  await copyTextToClipboard(displayText.value, '内容已复制')
}

defineExpose({ closeSearch, copyDisplayText, openSearch })

function isJsonLike(contentType: string | undefined, text: string | undefined): boolean {
  const normalizedContentType = (contentType || '').toLowerCase()
  if (normalizedContentType.includes('json') || normalizedContentType.includes('+json')) return true
  const trimmed = (text || '').trimStart()
  return trimmed.startsWith('{') || trimmed.startsWith('[')
}

function formatBytes(value: number): string {
  if (value >= 1024 * 1024) return `${(value / 1024 / 1024).toFixed(2)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${value} B`
}
</script>

<style scoped>
.readonly-code-viewer {
  background: #fff;
}

.readonly-code-viewer-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 10px;
  border: 1px solid #e2e8f0;
  border-bottom: 0;
  border-radius: 8px 8px 0 0;
  background: #f8fafc;
}

.readonly-code-viewer-meta {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 8px;
  color: #64748b;
  font-size: 12px;
}

.readonly-code-viewer-title {
  flex: 0 0 auto;
  color: #0f172a;
  font-size: 13px;
  font-weight: 700;
}

.readonly-code-viewer-warning {
  color: #d97706;
}

.readonly-code-viewer-search {
  display: grid;
  grid-template-columns: minmax(160px, 280px) auto auto minmax(96px, auto) auto;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border: 1px solid #e2e8f0;
  border-bottom: 0;
  border-radius: 8px 8px 0 0;
  background: #f8fafc;
}

.readonly-code-viewer-search-attached {
  border-radius: 0;
}

.readonly-code-viewer-search-status {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.readonly-code-viewer-editor {
  overflow: hidden;
}

.readonly-code-viewer-search-open .readonly-code-viewer-editor :deep(.cm-editor) {
  border-radius: 0 0 8px 8px;
}

.readonly-code-viewer-loading .readonly-code-viewer-editor {
  min-height: 280px;
}

@media (max-width: 640px) {
  .readonly-code-viewer-search {
    grid-template-columns: minmax(0, 1fr) auto auto auto;
  }

  .readonly-code-viewer-search-status {
    display: none;
  }
}
</style>
