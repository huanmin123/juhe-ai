<template>
  <div class="readonly-code-viewer" :class="{ 'readonly-code-viewer-loading': formatting }">
    <div v-if="showToolbar" class="readonly-code-viewer-toolbar">
      <div class="readonly-code-viewer-meta">
        <strong v-if="title" class="readonly-code-viewer-title">{{ title }}</strong>
        <a-tag :color="isJson ? 'blue' : 'default'">{{ languageLabel }}</a-tag>
        <span>{{ formattedSizeText }}</span>
        <span v-if="formatError" class="readonly-code-viewer-warning">{{ formatError }}</span>
      </div>
      <a-space size="small">
        <a-tooltip title="复制当前内容">
          <a-button size="small" :disabled="!displayText" @click="copyDisplayText">
            <template #icon><copy-outlined /></template>
          </a-button>
        </a-tooltip>
      </a-space>
    </div>
    <a-spin :spinning="formatting">
      <div ref="editorRoot" class="readonly-code-viewer-editor" />
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'
import { json } from '@codemirror/lang-json'
import { bracketMatching, defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorState } from '@codemirror/state'
import { EditorView, highlightActiveLine, highlightSpecialChars, lineNumbers } from '@codemirror/view'
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
const displayText = ref('')
const formatError = ref('')
const formatting = ref(false)

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
    return
  }
  editorView.setState(state)
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
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      EditorView.lineWrapping,
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

async function copyDisplayText(): Promise<void> {
  await copyTextToClipboard(displayText.value, '内容已复制')
}

defineExpose({ copyDisplayText })

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

.readonly-code-viewer-editor {
  overflow: hidden;
}

.readonly-code-viewer-loading .readonly-code-viewer-editor {
  min-height: 280px;
}
</style>
