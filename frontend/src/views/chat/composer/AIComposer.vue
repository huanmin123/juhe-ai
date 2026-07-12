<template>
  <div class="ai-composer" :class="{ 'is-disabled': disabled }">
    <div class="ai-composer-toolbar">
      <a-tooltip title="撤销"><a-button type="text" size="small" :disabled="disabled || !editor?.can().undo()" aria-label="撤销" @click="editor?.chain().focus().undo().run()"><UndoOutlined /></a-button></a-tooltip>
      <a-tooltip title="重做"><a-button type="text" size="small" :disabled="disabled || !editor?.can().redo()" aria-label="重做" @click="editor?.chain().focus().redo().run()"><RedoOutlined /></a-button></a-tooltip>
      <a-tooltip title="添加图片"><a-button type="text" size="small" :disabled="disabled" aria-label="添加图片" @click="fileInput?.click()"><PictureOutlined /></a-button></a-tooltip>
      <span class="ai-composer-hint">支持 Markdown，Enter 发送，Shift+Enter 换行</span>
      <input ref="fileInput" class="ai-composer-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif" multiple @change="handleFileChange" />
    </div>
    <EditorContent :editor="editor" class="ai-composer-editor" />
    <div v-if="commandOpen" class="ai-composer-command-menu" role="listbox">
      <button v-for="item in commandItems" :key="item.key" type="button" role="option" @mousedown.prevent="selectCommand(item)">
        <strong>/{{ item.key }}</strong><span>{{ item.label }}</span><small>{{ item.description }}</small>
      </button>
    </div>
    <div v-if="imageItems.length" class="ai-composer-attachments">
      <div v-for="image in imageItems" :key="image.assetId" class="ai-composer-attachment">
        <img :src="image.previewUrl" :alt="image.fileName" />
        <span>{{ image.fileName }}</span>
        <button type="button" :aria-label="`移除${image.fileName}`" @click="removeImage(image.assetId)"><CloseOutlined /></button>
      </div>
    </div>
    <div class="ai-composer-footer">
      <span>{{ characterCount }} / 192 KiB</span>
      <a-tooltip v-if="disabled" title="停止生成"><a-button danger type="primary" aria-label="停止生成" @click="emit('stop')"><StopOutlined /></a-button></a-tooltip>
      <a-tooltip v-else title="发送"><a-button type="primary" aria-label="发送" :disabled="!hasContent" @click="submit"><SendOutlined /></a-button></a-tooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CloseOutlined, PictureOutlined, RedoOutlined, SendOutlined, StopOutlined, UndoOutlined } from '@ant-design/icons-vue'
import CharacterCount from '@tiptap/extension-character-count'
import Placeholder from '@tiptap/extension-placeholder'
import StarterKit from '@tiptap/starter-kit'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { JSONContent } from '@tiptap/core'
import { composerDocumentToBlocks, type ChatInputBlock } from './chatComposerDocument'
import { createChatComposerSubmission } from './chatComposerSubmission'
import { ChatImageAttachment } from './ChatImageAttachment'
import { chatComposerCommands, filterChatComposerCommands, type ChatComposerCommand } from './chatComposerCommands'

const props = defineProps<{ disabled: boolean }>()
const emit = defineEmits<{ (event: 'submit', payload: { blocks: ChatInputBlock[]; snapshot: JSONContent }): void; (event: 'stop'): void }>()
const fileInput = ref<HTMLInputElement>()
const commandOpen = ref(false)
const commandQuery = ref('')
const objectUrls = new Set<string>()

const editor = useEditor({
  extensions: [StarterKit, Placeholder.configure({ placeholder: '输入消息，支持 Markdown' }), CharacterCount, ChatImageAttachment],
  content: { type: 'doc', content: [{ type: 'paragraph' }] },
  editable: !props.disabled,
  editorProps: {
    handleKeyDown: (_view, event) => {
      if (event.isComposing) return false
      if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); submit(); return true }
      if (event.key === 'Escape' && commandOpen.value) { commandOpen.value = false; return true }
      return false
    },
    handlePaste: (_view, event) => {
      const files = Array.from(event.clipboardData?.files ?? [])
      const image = files.find((file) => file.type.startsWith('image/'))
      if (!image) return false
      insertImage(image)
      return true
    }
  },
  onUpdate: ({ editor: nextEditor }) => {
    const text = nextEditor.getText()
    const slash = /(?:^|\s)\/([^\s]*)$/.exec(text)
    commandOpen.value = Boolean(slash)
    commandQuery.value = slash?.[1] ?? ''
  }
})
watch(() => props.disabled, (disabled) => editor.value?.setEditable(!disabled), { immediate: true })

const commandItems = computed(() => commandOpen.value ? filterChatComposerCommands(commandQuery.value) : chatComposerCommands)
const characterCount = computed(() => editor.value?.storage.characterCount?.characters?.() ?? 0)
const hasContent = computed(() => Boolean(editor.value && (editor.value.getText().trim() || imageItems.value.length)))
const imageItems = computed(() => {
  const items: Array<{ assetId: string; previewUrl: string; fileName: string }> = []
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment') items.push({ assetId: String(node.attrs.assetId ?? ''), previewUrl: String(node.attrs.previewUrl ?? ''), fileName: String(node.attrs.fileName ?? '图片') })
  })
  return items
})

function submit(): void {
  if (!editor.value || !hasContent.value || props.disabled) return
  const snapshot = editor.value.getJSON()
  const payload = { blocks: composerDocumentToBlocks(snapshot), snapshot }
  const submission = createChatComposerSubmission(snapshot as Record<string, unknown>)
  editor.value.commands.clearContent(true)
  emit('submit', { ...payload, snapshot: submission.snapshot as JSONContent })
}
function restore(snapshot: JSONContent): void { editor.value?.commands.setContent(snapshot, { emitUpdate: false }); editor.value?.commands.focus('end') }
function selectCommand(item: ChatComposerCommand): void {
  if (!editor.value) return
  if (item.key === 'clear') editor.value.commands.clearContent(true)
  else if (item.key !== 'image') editor.value.commands.insertContent(item.insert)
  commandOpen.value = false
}
async function insertImage(file: File): Promise<void> {
  if (!editor.value || !file.type.startsWith('image/') || file.size > 10 * 1024 * 1024 || imageItems.value.length >= 4) return
  const dataUrl = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result ?? '')); reader.onerror = () => reject(reader.error); reader.readAsDataURL(file) })
  const previewUrl = URL.createObjectURL(file); objectUrls.add(previewUrl)
  editor.value.commands.insertContent({ type: 'chatImageAttachment', attrs: { assetId: `local-${crypto.randomUUID()}`, previewUrl, dataUrl, fileName: file.name || '图片' } })
}
function handleFileChange(event: Event): void { const files = Array.from((event.target as HTMLInputElement).files ?? []); files.forEach((file) => { void insertImage(file) }); (event.target as HTMLInputElement).value = '' }
function removeImage(assetId: string): void {
  const target = imageItems.value.find((item) => item.assetId === assetId)
  if (!target || !editor.value) return
  let found = false
  editor.value.state.doc.descendants((node, position) => {
    if (!found && node.type.name === 'chatImageAttachment' && node.attrs.assetId === assetId) {
      editor.value?.commands.deleteRange({ from: position, to: position + node.nodeSize }); found = true
    }
  })
  if (target.previewUrl) { URL.revokeObjectURL(target.previewUrl); objectUrls.delete(target.previewUrl) }
}
defineExpose({ restore, focus: () => editor.value?.commands.focus() })
onBeforeUnmount(() => objectUrls.forEach((url) => URL.revokeObjectURL(url)))
</script>

<style scoped>
.ai-composer { position: relative; border: 1px solid #cbd5e1; border-radius: 10px; background: #fff; box-shadow: 0 2px 8px rgba(15, 23, 42, .05); }
.ai-composer:focus-within { border-color: #1677ff; box-shadow: 0 0 0 2px rgba(22, 119, 255, .1); }
.ai-composer-toolbar, .ai-composer-footer { display: flex; align-items: center; gap: 4px; padding: 6px 8px; }
.ai-composer-toolbar { border-bottom: 1px solid #eef2f7; }
.ai-composer-footer { justify-content: flex-end; color: #94a3b8; font-size: 11px; border-top: 1px solid #eef2f7; }
.ai-composer-hint { margin-left: 6px; color: #94a3b8; font-size: 11px; }
.ai-composer-file { display: none; }
.ai-composer-editor { min-height: 56px; max-height: 220px; overflow-y: auto; padding: 9px 12px; }
.ai-composer-editor :deep(.ProseMirror) { min-height: 38px; outline: none; white-space: pre-wrap; overflow-wrap: anywhere; }
.ai-composer-editor :deep(.ProseMirror p.is-editor-empty:first-child::before) { color: #9aa6b2; content: attr(data-placeholder); float: left; height: 0; pointer-events: none; }
.ai-composer-editor :deep(.chat-composer-image) { display: inline-block; vertical-align: middle; max-width: 180px; max-height: 120px; object-fit: contain; border-radius: 6px; border: 1px solid #dbe3ec; }
.ai-composer-attachments { display: flex; gap: 8px; padding: 0 10px 8px; }
.ai-composer-attachment { position: relative; display: flex; align-items: center; gap: 5px; padding: 4px; color: #475569; font-size: 11px; background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px; }
.ai-composer-attachment img { width: 34px; height: 28px; object-fit: cover; border-radius: 4px; }
.ai-composer-attachment button { border: 0; background: transparent; cursor: pointer; }
.ai-composer-command-menu { position: absolute; z-index: 3; bottom: 76px; left: 8px; width: min(360px, calc(100% - 16px)); padding: 5px; background: #fff; border: 1px solid #dbe3ec; border-radius: 7px; box-shadow: 0 10px 24px rgba(15, 23, 42, .14); }
.ai-composer-command-menu button { width: 100%; display: grid; grid-template-columns: 90px 90px 1fr; gap: 6px; padding: 7px 8px; text-align: left; border: 0; background: transparent; cursor: pointer; }
.ai-composer-command-menu button:hover { background: #f0f7ff; }
.ai-composer-command-menu small { color: #94a3b8; }
</style>
