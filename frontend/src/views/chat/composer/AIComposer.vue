<template>
  <div class="ai-composer" :class="{ 'is-disabled': disabled }">
    <input ref="fileInput" class="ai-composer-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif" :disabled="!imageInputSupported" multiple @change="handleFileChange" />
    <EditorContent :editor="editor" class="ai-composer-editor" />
    <div v-if="commandOpen" class="ai-composer-command-menu" role="listbox">
      <button v-for="(item, index) in commandItems" :key="item.key" type="button" role="option" :aria-selected="index === commandIndex" :class="{ 'is-active': index === commandIndex }" @mouseenter="commandIndex = index" @mousedown.prevent="selectCommand(item)">
        <strong>/{{ item.key }}</strong><span>{{ item.label }}</span><small>{{ item.description }}</small>
      </button>
    </div>
    <div class="ai-composer-footer">
      <div class="ai-composer-model-controls">
        <a-tooltip v-if="showConversationButton" title="对话记录"><a-button type="text" size="small" aria-label="对话记录" @click="emit('open-conversations')"><MenuOutlined /></a-button></a-tooltip>
        <a-select :value="modelValue" :options="modelSelectOptions" :loading="modelsLoading" :disabled="disabled" size="small" :bordered="false" :popup-match-select-width="false" aria-label="选择模型" @update:value="emit('update:modelValue', $event)" />
        <a-select v-if="reasoningOptions.length" :value="reasoningEffort" :options="reasoningOptions" :disabled="disabled" size="small" :bordered="false" :popup-match-select-width="false" aria-label="思考级别" @update:value="emit('update:reasoningEffort', $event)" />
        <a-select v-if="serviceTierOptions.length" :value="serviceTier" :options="serviceTierOptions" :disabled="disabled" size="small" :bordered="false" :popup-match-select-width="false" aria-label="服务等级" @update:value="emit('update:serviceTier', $event)" />
      </div>
      <a-tooltip v-if="disabled" title="停止生成"><a-button danger type="primary" aria-label="停止生成" @click="emit('stop')"><StopOutlined /></a-button></a-tooltip>
      <a-tooltip v-else title="发送"><a-button type="primary" aria-label="发送" :disabled="!canSubmit" @click="submit"><SendOutlined /></a-button></a-tooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { MenuOutlined, SendOutlined, StopOutlined } from '@ant-design/icons-vue'
import Placeholder from '@tiptap/extension-placeholder'
import StarterKit from '@tiptap/starter-kit'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import { computed, ref, watch } from 'vue'
import type { JSONContent } from '@tiptap/core'
import { message } from '@/lib/antd'
import { composerDocumentToBlocks, composerTextToDocument, type ChatInputBlock } from './chatComposerDocument'
import { createChatComposerSubmission } from './chatComposerSubmission'
import { ChatImageAttachment } from './ChatImageAttachment'
import { chatComposerCommandQueryRange, chatComposerCommands, filterChatComposerCommands, moveChatComposerCommandIndex, type ChatComposerCommand } from './chatComposerCommands'
import { reasoningEffortLabel, selectableChatReasoningEfforts } from './chatModelControls'
import { replaceEditorContentWithoutHistory } from './chatEditorDocumentBoundary'
import { maxChatImageCount, selectChatImageFiles } from './chatImageSelection'
import type { ChatModelOption, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'

const props = defineProps<{ disabled: boolean; imageInputSupported: boolean; modelOptions: ChatModelOption[]; modelValue?: string; modelsLoading: boolean; reasoningEffort: ChatReasoningEffort | ''; serviceTier: ChatServiceTier | ''; showConversationButton: boolean }>()
const emit = defineEmits<{
  (event: 'submit', payload: { blocks: ChatInputBlock[]; snapshot: JSONContent }): void
  (event: 'stop' | 'open-conversations'): void
  (event: 'update:modelValue', value?: string): void
  (event: 'update:reasoningEffort', value: ChatReasoningEffort | ''): void
  (event: 'update:serviceTier', value: ChatServiceTier | ''): void
}>()
const fileInput = ref<HTMLInputElement>()
const commandOpen = ref(false)
const commandQuery = ref('')
const commandIndex = ref(0)
const contentRevision = ref(0)
let imageInsertionQueue = Promise.resolve()
let editorDocumentGeneration = 0

const editor = useEditor({
  extensions: [StarterKit, Placeholder.configure({ placeholder: () => props.imageInputSupported ? '输入消息；Enter 发送，Shift+Enter 换行，可粘贴图片，/ 打开命令' : '输入消息；Enter 发送，Shift+Enter 换行，/ 打开命令' }), ChatImageAttachment],
  content: { type: 'doc', content: [{ type: 'paragraph' }] },
  editable: !props.disabled,
  editorProps: {
    handleKeyDown: (_view, event) => {
      if (event.isComposing) return false
      if (commandOpen.value && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
        event.preventDefault()
        const count = commandItems.value.length
        commandIndex.value = moveChatComposerCommandIndex(commandIndex.value, event.key === 'ArrowDown' ? 1 : -1, count)
        return true
      }
      if (commandOpen.value && event.key === 'Enter') {
        event.preventDefault()
        const item = commandItems.value[commandIndex.value]
        if (item) selectCommand(item)
        return true
      }
      if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); submit(); return true }
      if (event.key === 'Escape' && commandOpen.value) { commandOpen.value = false; return true }
      return false
    },
    handlePaste: (_view, event) => {
      const files = Array.from(event.clipboardData?.files ?? [])
      if (!files.some((file) => file.type.startsWith('image/'))) return false
      if (!props.imageInputSupported) { message.warning('当前模型不支持图片输入'); return true }
      enqueueImages(files)
      return true
    }
  },
  onUpdate: ({ editor: nextEditor }) => {
    contentRevision.value += 1
    const text = nextEditor.getText()
    const slash = /(?:^|\s)\/([^\s]*)$/.exec(text)
    commandOpen.value = Boolean(slash)
    commandQuery.value = slash?.[1] ?? ''
    commandIndex.value = 0
  }
})
watch(() => props.disabled, (disabled) => editor.value?.setEditable(!disabled), { immediate: true })

const commandItems = computed(() => (commandOpen.value ? filterChatComposerCommands(commandQuery.value) : chatComposerCommands)
  .filter((item) => props.imageInputSupported || item.key !== 'image'))
const selectedModelOption = computed(() => props.modelOptions.find((item) => item.id === props.modelValue))
const modelSelectOptions = computed(() => props.modelOptions.map((item) => ({ label: item.id, value: item.id })))
const reasoningOptions = computed(() => selectableChatReasoningEfforts(selectedModelOption.value).map((value) => ({ label: `思考 ${reasoningEffortLabel(value)}`, value })))
const serviceTierOptions = computed(() => (selectedModelOption.value?.supportedServiceTiers ?? []).map((value) => ({ label: value === 'default' ? '服务 默认' : value === 'priority' ? '服务 优先' : '服务 Flex', value })))
const imageItems = computed(() => {
  contentRevision.value
  const items: Array<{ assetId: string; previewUrl: string; fileName: string }> = []
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment') items.push({ assetId: String(node.attrs.assetId ?? ''), previewUrl: String(node.attrs.previewUrl ?? ''), fileName: String(node.attrs.fileName ?? '图片') })
  })
  return items
})
const hasContent = computed(() => {
  contentRevision.value
  return Boolean(editor.value && (editor.value.getText().trim() || imageItems.value.length))
})
const canSubmit = computed(() => Boolean(hasContent.value && props.modelValue && !props.modelsLoading && (props.imageInputSupported || imageItems.value.length === 0)))
watch(() => props.imageInputSupported, () => {
  if (editor.value) editor.value.view.dispatch(editor.value.state.tr)
  if (!props.imageInputSupported && imageItems.value.length) message.warning('当前模型不支持已粘贴的图片，请移除图片或切换模型')
})

function submit(): void {
  if (!editor.value || !canSubmit.value || props.disabled) return
  const snapshot = editor.value.getJSON()
  const payload = { blocks: composerDocumentToBlocks(snapshot), snapshot }
  const submission = createChatComposerSubmission(snapshot as Record<string, unknown>)
  editorDocumentGeneration += 1
  replaceEditorContentWithoutHistory(editor.value, emptyComposerDocument())
  contentRevision.value += 1
  emit('submit', { ...payload, snapshot: submission.snapshot as JSONContent })
}
function getSnapshot(): JSONContent {
  return editor.value ? cloneDocument(editor.value.getJSON()) : { type: 'doc', content: [{ type: 'paragraph' }] }
}
function setText(content: string): void {
  editorDocumentGeneration += 1
  if (editor.value) replaceEditorContentWithoutHistory(editor.value, composerTextToDocument(content))
  contentRevision.value += 1
}
function restore(snapshot: JSONContent): void { editorDocumentGeneration += 1; if (editor.value) replaceEditorContentWithoutHistory(editor.value, cloneDocument(snapshot)); contentRevision.value += 1; editor.value?.commands.focus('end') }
function clear(): void { editorDocumentGeneration += 1; if (editor.value) replaceEditorContentWithoutHistory(editor.value, emptyComposerDocument()); contentRevision.value += 1 }
function focus(): void { editor.value?.commands.focus() }
function cloneDocument(document: JSONContent): JSONContent { return JSON.parse(JSON.stringify(document)) as JSONContent }
function emptyComposerDocument(): JSONContent { return { type: 'doc', content: [{ type: 'paragraph' }] } }
function selectCommand(item: ChatComposerCommand): void {
  if (!editor.value) return
  if (item.key === 'clear') {
    clear()
  } else {
    const cursor = editor.value.state.selection.from
    editor.value.chain().focus().deleteRange(chatComposerCommandQueryRange(cursor, commandQuery.value)).run()
    if (item.key === 'image') fileInput.value?.click()
    else editor.value.commands.insertContent(item.insert)
  }
  commandOpen.value = false
}
async function insertImage(file: File, generation: number): Promise<void> {
  if (!props.imageInputSupported || !editor.value || generation !== editorDocumentGeneration || imageItems.value.length >= maxChatImageCount) return
  const dataUrl = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result ?? '')); reader.onerror = () => reject(reader.error); reader.readAsDataURL(file) })
  if (!props.imageInputSupported || !editor.value || generation !== editorDocumentGeneration || imageItems.value.length >= maxChatImageCount) return
  const previewUrl = dataUrl
  editor.value.commands.insertContent({ type: 'chatImageAttachment', attrs: { assetId: `local-${crypto.randomUUID()}`, previewUrl, dataUrl, fileName: file.name || '图片' } })
}
function enqueueImages(files: readonly File[]): void {
  if (!props.imageInputSupported) { message.warning('当前模型不支持图片输入'); return }
  const generation = editorDocumentGeneration
  imageInsertionQueue = imageInsertionQueue.then(async () => {
    if (generation !== editorDocumentGeneration) return
    for (const file of selectChatImageFiles(files, imageItems.value.length)) {
      if (!props.imageInputSupported) break
      await insertImage(file, generation)
    }
  }).catch(() => undefined)
}
function handleFileChange(event: Event): void { const input = event.target as HTMLInputElement; enqueueImages(Array.from(input.files ?? [])); input.value = '' }
defineExpose({ getSnapshot, setText, restore, clear, focus })
</script>

<style scoped>
.ai-composer { position: relative; border: 1px solid #cbd5e1; border-radius: 10px; background: #fff; box-shadow: 0 2px 8px rgba(15, 23, 42, .05); }
.ai-composer:focus-within { border-color: #1677ff; box-shadow: 0 0 0 2px rgba(22, 119, 255, .1); }
.ai-composer-footer { min-height: 44px; display: flex; align-items: center; gap: 4px; padding: 5px 8px; color: #94a3b8; font-size: 11px; border-top: 1px solid #eef2f7; }
.ai-composer-model-controls { min-width: 0; flex: 1; display: flex; align-items: center; gap: 1px; overflow-x: auto; scrollbar-width: none; }
.ai-composer-model-controls::-webkit-scrollbar { display: none; }
.ai-composer-model-controls :deep(.ant-select-selector) { padding-inline: 5px !important; color: #475569; font-size: 12px; }
.ai-composer-file { display: none; }
.ai-composer-editor { min-height: 56px; max-height: 220px; overflow-y: auto; padding: 9px 12px; }
.ai-composer-editor :deep(.ProseMirror) { min-height: 38px; outline: none; white-space: pre-wrap; overflow-wrap: anywhere; }
.ai-composer-editor :deep(.ProseMirror p.is-editor-empty:first-child::before) { color: #9aa6b2; content: attr(data-placeholder); float: left; height: 0; pointer-events: none; }
.ai-composer-editor :deep(.chat-composer-image) { display: inline-block; vertical-align: middle; max-width: 180px; max-height: 120px; object-fit: contain; border-radius: 6px; border: 1px solid #dbe3ec; }
.ai-composer-command-menu { position: absolute; z-index: 3; bottom: 76px; left: 8px; width: min(360px, calc(100% - 16px)); padding: 5px; background: #fff; border: 1px solid #dbe3ec; border-radius: 7px; box-shadow: 0 10px 24px rgba(15, 23, 42, .14); }
.ai-composer-command-menu button { width: 100%; display: grid; grid-template-columns: 90px 90px 1fr; gap: 6px; padding: 7px 8px; text-align: left; border: 0; background: transparent; cursor: pointer; }
.ai-composer-command-menu button:hover { background: #f0f7ff; }
.ai-composer-command-menu button.is-active { background: #eaf3ff; }
.ai-composer-command-menu small { color: #94a3b8; }
@media (max-width: 520px) {
  .ai-composer-footer { align-items: flex-end; }
  .ai-composer-model-controls { flex-wrap: wrap; row-gap: 0; overflow-x: visible; }
  .ai-composer-model-controls :deep(.ant-select-selector) { padding-inline: 3px !important; }
}
</style>
