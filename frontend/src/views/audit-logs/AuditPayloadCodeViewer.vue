<template>
  <div class="audit-code-viewer" :class="{ 'audit-code-viewer-loading': formatting }">
    <div v-if="showToolbar" class="audit-code-viewer-toolbar">
      <div class="audit-code-viewer-meta">
        <a-tag :color="isJson ? 'blue' : 'default'">{{ languageLabel }}</a-tag>
        <span>{{ formattedSizeText }}</span>
        <span v-if="formatError" class="audit-code-viewer-warning">{{ formatError }}</span>
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
      <div ref="editorRoot" class="audit-code-viewer-editor" />
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'
import { json } from '@codemirror/lang-json'
import { bracketMatching, defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorState } from '@codemirror/state'
import { EditorView, highlightActiveLine, highlightSpecialChars, lineNumbers } from '@codemirror/view'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { message } from '@/lib/antd'
import { formatBytes } from './auditLogFormatters'

const props = withDefaults(defineProps<{
  contentType?: string
  showToolbar?: boolean
  text?: string
  title?: string
}>(), {
  contentType: '',
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

const isJson = computed(() => isJsonLike(props.contentType, props.text))
const languageLabel = computed(() => isJson.value ? 'JSON' : 'Text')
const formattedSizeText = computed(() => formatBytes(new Blob([displayText.value]).size))

watch(
  () => [props.text, props.contentType] as const,
  () => {
    scheduleFormat()
  },
  { immediate: true }
)

onMounted(() => {
  updateEditor()
})

onBeforeUnmount(() => {
  editorView?.destroy()
  editorView = undefined
})

async function scheduleFormat(): Promise<void> {
  const taskId = ++formatTaskId
  formatting.value = true
  await nextTick()
  window.setTimeout(() => {
    if (taskId !== formatTaskId) return
    const result = formatForDisplay(props.text || '', isJson.value)
    displayText.value = result.text
    formatError.value = result.error
    formatting.value = false
    void nextTick(updateEditor)
  }, 0)
}

function formatForDisplay(text: string, shouldFormatJson: boolean): { text: string; error: string } {
  if (!text) return { text: '', error: '' }
  if (!shouldFormatJson) return { text, error: '' }
  try {
    return { text: JSON.stringify(JSON.parse(text), null, 2), error: '' }
  } catch {
    return { text, error: 'JSON 格式化失败，已按原文展示' }
  }
}

function updateEditor(): void {
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

function createEditorState(doc: string, shouldUseJson: boolean): EditorState {
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
          height: '420px',
          border: '1px solid #e2e8f0',
          borderRadius: '0 0 8px 8px',
          fontSize: '12px'
        },
        '&.cm-focused': {
          outline: 'none'
        },
        '.cm-scroller': {
          fontFamily: "Consolas, 'Courier New', monospace"
        },
        '.cm-content': {
          minHeight: '420px'
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
  if (!displayText.value) return
  if (!navigator.clipboard?.writeText) {
    message.error('当前浏览器不支持自动复制，请手动选择内容复制')
    return
  }
  try {
    await navigator.clipboard.writeText(displayText.value)
    message.success('内容已复制')
  } catch (error) {
    console.error(error)
    message.error('复制失败，请手动选择内容复制')
  }
}

defineExpose({ copyDisplayText })

function isJsonLike(contentType: string | undefined, text: string | undefined): boolean {
  const normalizedContentType = (contentType || '').toLowerCase()
  if (normalizedContentType.includes('json') || normalizedContentType.includes('+json')) return true
  const trimmed = (text || '').trimStart()
  return trimmed.startsWith('{') || trimmed.startsWith('[')
}
</script>

<style scoped>
.audit-code-viewer {
  background: #fff;
}

.audit-code-viewer-toolbar {
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

.audit-code-viewer-meta {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 8px;
  color: #64748b;
  font-size: 12px;
}

.audit-code-viewer-warning {
  color: #d97706;
}

.audit-code-viewer-editor {
  overflow: hidden;
}

.audit-code-viewer-loading .audit-code-viewer-editor {
  min-height: 420px;
}
</style>
