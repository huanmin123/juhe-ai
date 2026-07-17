<template>
  <div class="ai-composer" :class="{ 'is-disabled': disabled }">
    <input ref="fileInput" class="ai-composer-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif" :disabled="disabled || !imageInputSupported || !conversationId" multiple @change="handleFileChange" />
    <EditorContent :editor="editor" class="ai-composer-editor" />
    <div v-if="commandOpen" class="ai-composer-command-menu" role="listbox">
      <button v-for="(item, index) in commandItems" :key="item.key" type="button" role="option" :aria-selected="index === commandIndex" :class="{ 'is-active': index === commandIndex }" @mouseenter="commandIndex = index" @mousedown.prevent="selectCommand(item)">
        <strong>/{{ item.key }}</strong><span>{{ item.label }}</span><small>{{ item.description }}</small>
      </button>
    </div>
    <div class="ai-composer-footer">
      <div class="ai-composer-model-controls">
        <a-tooltip v-if="showConversationButton" title="对话记录"><a-button type="text" size="small" aria-label="对话记录" @click="emit('open-conversations')"><MenuOutlined /></a-button></a-tooltip>
        <a-select :value="modelValue" :options="modelSelectOptions" :loading="modelsLoading" :disabled="disabled" size="small" :bordered="false" aria-label="选择模型" :style="{ width: `${modelControlWidths.triggerWidth}px` }" :dropdown-match-select-width="modelControlWidths.popupWidth" @dropdown-visible-change="handleModelDropdownVisibleChange" @update:value="emit('update:modelValue', $event)" />
        <a-select v-if="reasoningOptions.length" :value="reasoningEffort" :options="reasoningOptions" :disabled="disabled" size="small" :bordered="false" aria-label="思考级别" :style="{ width: `${reasoningControlWidths.triggerWidth}px` }" :dropdown-match-select-width="reasoningControlWidths.popupWidth" @update:value="emit('update:reasoningEffort', $event)" />
        <a-select v-if="serviceTierOptions.length" :value="serviceTier" :options="serviceTierOptions" :disabled="disabled" size="small" :bordered="false" aria-label="服务等级" :style="{ width: `${serviceTierControlWidths.triggerWidth}px` }" :dropdown-match-select-width="serviceTierControlWidths.popupWidth" @update:value="emit('update:serviceTier', $event)" />
      </div>
      <a-tooltip :title="contextTooltip">
        <span class="ai-composer-context" role="img" :aria-label="`上下文用量 ${contextTooltip}`">
          <a-progress type="circle" :percent="contextPercent" :size="18" :stroke-width="12" :show-info="false" :status="contextProgressStatus" />
        </span>
      </a-tooltip>
      <a-tooltip v-if="stoppable" title="停止生成"><a-button danger type="primary" aria-label="停止生成" @click="emit('stop')"><StopOutlined /></a-button></a-tooltip>
      <a-tooltip v-else :title="sendTooltip"><a-button type="primary" aria-label="发送" :disabled="!canSubmit" @click="submit"><SendOutlined /></a-button></a-tooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { MenuOutlined, SendOutlined, StopOutlined } from '@ant-design/icons-vue'
import Placeholder from '@tiptap/extension-placeholder'
import StarterKit from '@tiptap/starter-kit'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { Editor, JSONContent } from '@tiptap/core'
import { chatApi, chatAssetContentUrl } from '@/api/domains/chat'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { composerDocumentToBlocks, composerTextToDocument, type ChatInputBlock } from './chatComposerDocument'
import { createChatComposerSubmission } from './chatComposerSubmission'
import { ChatImageAttachment } from './ChatImageAttachment'
import { chatComposerCommands, filterChatComposerCommands, findChatComposerCommandQuery, moveChatComposerCommandIndex, type ChatComposerCommand } from './chatComposerCommands'
import { chatMixedClipboardParts, type ChatMixedClipboardPart } from './chatMixedClipboard'
import { chatComposerControlWidths } from './chatComposerControlWidths'
import { createChatComposerKeyDownHandler } from './chatComposerKeyDownHandler'
import { reasoningEffortLabel, selectableChatReasoningEfforts } from './chatModelControls'
import { replaceEditorContentWithoutHistory } from './chatEditorDocumentBoundary'
import { maxChatImageCount, selectChatImageFiles, selectChatImageFileSlots } from './chatImageSelection'
import { createChatImagePreparationState } from './chatImagePreparationState'
import { prepareChatImageForUpload } from './chatImageProcessing'
import type { ChatContextStatus, ChatModelOption, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'

const props = defineProps<{
  contextStatus?: ChatContextStatus
  conversationId: string
  disabled: boolean
  stoppable: boolean
  turnLimitReached: boolean
  turnLimitMessage: string
  imageInputSupported: boolean
  modelOptions: ChatModelOption[]
  modelValue?: string
  modelsLoading: boolean
  reasoningEffort: ChatReasoningEffort | ''
  serviceTier: ChatServiceTier | ''
  showConversationButton: boolean
}>()
const emit = defineEmits<{
  (event: 'submit', payload: { blocks: ChatInputBlock[]; snapshot: JSONContent }): void
  (event: 'stop' | 'open-conversations' | 'models-open'): void
  (event: 'update:modelValue', value?: string): void
  (event: 'update:reasoningEffort', value: ChatReasoningEffort | ''): void
  (event: 'update:serviceTier', value: ChatServiceTier | ''): void
}>()

function handleModelDropdownVisibleChange(open: boolean): void {
  if (open) emit('models-open')
}
const fileInput = ref<HTMLInputElement>()
const commandOpen = ref(false)
const commandQuery = ref('')
const commandIndex = ref(0)
const contentRevision = ref(0)
const imagePreparationState = createChatImagePreparationState()
const initialImagePreparationSnapshot = imagePreparationState.snapshot()
const pendingImagePreparationCount = ref(initialImagePreparationSnapshot.pendingCount)
let conversationGeneration = initialImagePreparationSnapshot.generation
let imageSyncScheduled = false

type ImageUploadStatus = 'uploading' | 'uploaded' | 'failed'
interface ImageUploadRecord {
  localId: string
  file?: File
  previewUrl: string
  conversationId: string
  generation: number
  status: ImageUploadStatus
  progress: number
  assetId: string
  fileName: string
  mimeType: string
  width: number
  height: number
  byteSize: number
  error: string
  submitted: boolean
  controller?: AbortController
}
const imageUploadRecords = new Map<string, ImageUploadRecord>()

const handleComposerKeyDown = createChatComposerKeyDownHandler({
  commandOpen: () => commandOpen.value,
  commandItemCount: () => commandItems.value.length,
  moveCommand: (direction) => {
    commandIndex.value = moveChatComposerCommandIndex(commandIndex.value, direction === 'next' ? 1 : -1, commandItems.value.length)
  },
  selectCommand: () => {
    const item = commandItems.value[commandIndex.value]
    if (!item) return false
    selectCommand(item)
    return true
  },
  closeCommand: () => { commandOpen.value = false },
  submit
})

const editor = useEditor({
  extensions: [StarterKit, Placeholder.configure({ placeholder: () => props.imageInputSupported ? '输入消息；Enter 发送，Shift+Enter 换行；支持 Markdown、图片和 / 命令' : '输入消息；Enter 发送，Shift+Enter 换行；支持 Markdown 和 / 命令' }), ChatImageAttachment.configure({ onRetry: retryImageUpload, onRemove: removeImageUpload })],
  content: { type: 'doc', content: [{ type: 'paragraph' }] },
  editable: !props.disabled,
  editorProps: {
    handleKeyDown: handleComposerKeyDown,
    handlePaste: (_view, event) => {
      const files = Array.from(event.clipboardData?.files ?? [])
      const imageFiles = files.filter((file) => file.type.startsWith('image/'))
      if (!imageFiles.length) return false
      if (!props.imageInputSupported) { message.warning('当前模型不支持图片输入'); return true }
      const imageFileSlots = selectChatImageFileSlots(imageFiles, imageItems.value.length + pendingImagePreparationCount.value)
      const selectedFiles = imageFileSlots.filter((file): file is File => Boolean(file))
      if (selectedFiles.length < imageFiles.length) message.warning(`每条消息最多 ${maxChatImageCount} 张图片；原图不能超过 32 MiB，压缩后每张不能超过 1 MiB`)
      const html = event.clipboardData?.getData('text/html') ?? ''
      if (html && selectedFiles.length) {
        const clipboardDocument = new DOMParser().parseFromString(html, 'text/html')
        if (clipboardDocument.body.querySelector('img')) {
          void insertMixedClipboardParts(chatMixedClipboardParts(clipboardDocument.body, imageFileSlots))
          return true
        }
      }
      void insertPlainClipboardParts(event.clipboardData?.getData('text/plain') ?? '', selectedFiles)
      return true
    }
  },
  onUpdate: ({ editor: nextEditor }) => {
    contentRevision.value += 1
    syncCommandQuery(nextEditor)
    scheduleImageNodeSync()
  },
  onSelectionUpdate: ({ editor: nextEditor }) => {
    syncCommandQuery(nextEditor)
  }
})
watch(() => props.disabled, (disabled) => editor.value?.setEditable(!disabled), { immediate: true })

const commandItems = computed(() => (commandOpen.value ? filterChatComposerCommands(commandQuery.value) : chatComposerCommands)
  .filter((item) => props.imageInputSupported || item.key !== 'image'))
const selectedModelOption = computed(() => props.modelOptions.find((item) => item.id === props.modelValue))
const modelSelectOptions = computed(() => props.modelOptions.map((item) => ({ label: item.id, value: item.id, title: item.id })))
const reasoningOptions = computed(() => selectableChatReasoningEfforts(selectedModelOption.value).map((value) => {
  const label = `思考 ${reasoningEffortLabel(value)}`
  return { label, value, title: label }
}))
const serviceTierOptions = computed(() => (selectedModelOption.value?.supportedServiceTiers ?? []).map((value) => {
  const label = value === 'default' ? '服务 默认' : value === 'priority' ? '服务 优先' : '服务 Flex'
  return { label, value, title: label }
}))
const modelControlWidths = computed(() => chatComposerControlWidths('model', props.modelValue, modelSelectOptions.value.map((item) => item.label)))
const reasoningControlWidths = computed(() => chatComposerControlWidths('reasoning', reasoningOptions.value.find((item) => item.value === props.reasoningEffort)?.label, reasoningOptions.value.map((item) => item.label)))
const serviceTierControlWidths = computed(() => chatComposerControlWidths('service', serviceTierOptions.value.find((item) => item.value === props.serviceTier)?.label, serviceTierOptions.value.map((item) => item.label)))
const contextLimitTokens = computed(() => selectedModelOption.value?.maxInputTokens ?? props.contextStatus?.limitTokens)
const contextPercent = computed(() => {
  const limit = contextLimitTokens.value
  return limit ? Math.min(100, Math.max(0, Math.round(((props.contextStatus?.usedTokens ?? 0) / limit) * 100))) : 0
})
const contextProgressStatus = computed(() => props.contextStatus?.state === 'compact_failed' ? 'exception' : contextPercent.value >= 85 ? 'exception' : 'normal')
const contextTooltip = computed(() => {
  if (!props.contextStatus) return '用量暂不可用'
  const used = formatTokenCount(props.contextStatus?.usedTokens ?? 0)
  const limit = contextLimitTokens.value ? formatTokenCount(contextLimitTokens.value) : '未知'
  const state = { ready: '', compact_pending: ' · 等待压缩', compacting: ' · 正在压缩', compact_failed: ' · 压缩失败，将重试' }[props.contextStatus?.state ?? 'ready']
  return `${used} / ${limit}${state}`
})
const imageItems = computed(() => {
  contentRevision.value
  const items: Array<{ localId: string; assetId: string; previewUrl: string; fileName: string; uploadStatus: ImageUploadStatus }> = []
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment') items.push({
      localId: String(node.attrs.localId ?? ''),
      assetId: String(node.attrs.assetId ?? ''),
      previewUrl: String(node.attrs.previewUrl ?? ''),
      fileName: String(node.attrs.fileName ?? '图片'),
      uploadStatus: String(node.attrs.uploadStatus ?? 'uploading') as ImageUploadStatus
    })
  })
  return items
})
const hasContent = computed(() => {
  contentRevision.value
  return Boolean(editor.value && (editor.value.getText().trim() || imageItems.value.length))
})
const imagesReady = computed(() => imageItems.value.every((item) => item.uploadStatus === 'uploaded' && Boolean(item.assetId)))
const canSubmit = computed(() => Boolean(hasContent.value && props.modelValue && !props.modelsLoading && imagesReady.value && pendingImagePreparationCount.value === 0 && !props.disabled && !props.turnLimitReached && (props.imageInputSupported || imageItems.value.length === 0)))
const sendTooltip = computed(() => {
  if (props.turnLimitReached) return props.turnLimitMessage
  if (pendingImagePreparationCount.value > 0) return '图片正在压缩，请稍候'
  if (imageItems.value.some((item) => item.uploadStatus === 'failed')) return '请重试或删除上传失败的图片'
  if (!imagesReady.value) return '请等待图片上传完成'
  if (imageItems.value.length && !props.imageInputSupported) return '当前模型不支持图片输入'
  return '发送'
})
watch(() => props.imageInputSupported, () => {
  if (editor.value) editor.value.view.dispatch(editor.value.state.tr)
  if (!props.imageInputSupported && imageItems.value.length) message.warning('当前模型不支持已粘贴的图片，请移除图片或切换模型')
})
watch(() => props.conversationId, (conversationId, previousConversationId) => {
  if (!previousConversationId || conversationId === previousConversationId) return
  advanceConversationGeneration()
  disposeImageUploadRecords(true)
  if (editor.value) replaceEditorContentWithoutHistory(editor.value, emptyComposerDocument())
  contentRevision.value += 1
})

function submit(): void {
  if (!editor.value || !canSubmit.value || props.disabled || props.turnLimitReached) return
  const snapshot = editor.value.getJSON()
  let blocks: ChatInputBlock[]
  try {
    blocks = composerDocumentToBlocks(snapshot)
  } catch (error) {
    message.warning(error instanceof Error ? error.message : '消息内容暂时无法发送')
    return
  }
  const payload = { blocks, snapshot }
  const submission = createChatComposerSubmission(snapshot as Record<string, unknown>)
  const submittedAssetIds = new Set(blocks.flatMap((block) => block.type === 'input_image' ? [block.assetId] : []))
  for (const record of imageUploadRecords.values()) record.submitted = submittedAssetIds.has(record.assetId)
  replaceEditorContentWithoutHistory(editor.value, emptyComposerDocument())
  contentRevision.value += 1
  emit('submit', { ...payload, snapshot: submission.snapshot as JSONContent })
}
function getSnapshot(): JSONContent {
  return editor.value ? cloneDocument(editor.value.getJSON()) : { type: 'doc', content: [{ type: 'paragraph' }] }
}
function setText(content: string): void {
  if (editor.value) replaceEditorContentWithoutHistory(editor.value, composerTextToDocument(content))
  contentRevision.value += 1
}
function setBlocks(blocks: ChatInputBlock[]): void {
  if (!editor.value) return
  const inline: JSONContent[] = []
  blocks.forEach((block, index) => {
    if (block.type === 'input_text') {
      const paragraph = composerTextToDocument(block.text).content?.[0]
      inline.push(...(paragraph?.content ?? []))
      return
    }
    inline.push({
      type: 'chatImageAttachment',
      attrs: {
        localId: `restored-${block.assetId}-${index}`,
        assetId: block.assetId,
        previewUrl: chatAssetContentUrl(props.conversationId, block.assetId),
        fileName: '已上传图片',
        mimeType: '', width: 0, height: 0, byteSize: 0,
        uploadStatus: 'uploaded', uploadProgress: 100, uploadError: ''
      }
    })
  })
  replaceEditorContentWithoutHistory(editor.value, { type: 'doc', content: [{ type: 'paragraph', ...(inline.length ? { content: inline } : {}) }] })
  contentRevision.value += 1
}
function restore(snapshot: JSONContent): void { for (const record of imageUploadRecords.values()) record.submitted = false; if (editor.value) replaceEditorContentWithoutHistory(editor.value, cloneDocument(snapshot)); contentRevision.value += 1; scheduleImageNodeSync(); editor.value?.commands.focus('end') }
function clear(): void { advanceConversationGeneration(); disposeImageUploadRecords(true); if (editor.value) replaceEditorContentWithoutHistory(editor.value, emptyComposerDocument()); contentRevision.value += 1 }
function focus(): void { editor.value?.commands.focus() }
function releaseSubmittedAssets(): void {
  const retainedLocalIds = new Set<string>()
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment') retainedLocalIds.add(String(node.attrs.localId ?? ''))
  })
  for (const [localId, record] of imageUploadRecords) {
    if (retainedLocalIds.has(localId)) continue
    record.controller?.abort()
    if (record.status === 'uploaded' && !record.submitted) deleteUploadedAsset(record)
    revokePreviewUrl(record.previewUrl)
    imageUploadRecords.delete(localId)
  }
}
function cloneDocument(document: JSONContent): JSONContent { return JSON.parse(JSON.stringify(document)) as JSONContent }
function emptyComposerDocument(): JSONContent { return { type: 'doc', content: [{ type: 'paragraph' }] } }
function formatTokenCount(value: number): string { return value >= 1_000_000 ? `${(value / 1_000_000).toFixed(1)}M` : value >= 1_000 ? `${(value / 1_000).toFixed(1)}K` : String(Math.max(0, Math.round(value))) }
function syncCommandQuery(nextEditor: Editor): void {
  const command = findChatComposerCommandQuery(nextEditor.state)
  if (!command) {
    commandOpen.value = false
    commandQuery.value = ''
    commandIndex.value = 0
    return
  }
  commandOpen.value = true
  commandQuery.value = command.query
  commandIndex.value = 0
}
function selectCommand(item: ChatComposerCommand): void {
  if (!editor.value) return
  const command = findChatComposerCommandQuery(editor.value.state)
  if (!command) {
    commandOpen.value = false
    commandQuery.value = ''
    commandIndex.value = 0
    return
  }
  if (item.key === 'clear') {
    clear()
  } else {
    editor.value.chain().focus().deleteRange(command.range).run()
    if (item.key === 'image') fileInput.value?.click()
    else editor.value.commands.insertContent(item.insert)
  }
  commandOpen.value = false
}
async function insertImage(sourceFile: File): Promise<void> {
  const preparationConversationId = props.conversationId
  const preparationGeneration = conversationGeneration
  const preparationEditor = editor.value
  if (!props.imageInputSupported || !preparationConversationId || !preparationEditor || imageItems.value.length + pendingImagePreparationCount.value >= maxChatImageCount) return
  const preparationToken = imagePreparationState.begin()
  syncPendingImagePreparationCount()
  let file: File
  try {
    file = await prepareChatImageForUpload(sourceFile)
  } catch (error) {
    message.warning(error instanceof Error ? error.message : '图片压缩失败')
    return
  } finally {
    imagePreparationState.release(preparationToken)
    syncPendingImagePreparationCount()
  }
  if (!props.imageInputSupported
    || props.conversationId !== preparationConversationId
    || conversationGeneration !== preparationGeneration
    || !imagePreparationState.isCurrent(preparationToken)
    || editor.value !== preparationEditor
    || imageItems.value.length >= maxChatImageCount) return
  const localId = crypto.randomUUID()
  const previewUrl = URL.createObjectURL(file)
  const record: ImageUploadRecord = {
    localId,
    file,
    previewUrl,
    conversationId: preparationConversationId,
    generation: preparationGeneration,
    status: 'uploading',
    progress: 0,
    assetId: '',
    fileName: file.name || '图片',
    mimeType: file.type,
    width: 0,
    height: 0,
    byteSize: file.size,
    error: '',
    submitted: false
  }
  const inserted = preparationEditor.commands.insertContent({ type: 'chatImageAttachment', attrs: imageNodeAttrs(record) })
  if (!inserted) {
    URL.revokeObjectURL(previewUrl)
    return
  }
  imageUploadRecords.set(localId, record)
  void uploadImage(record)
}
function insertClipboardText(value: string): void {
  if (!value || !editor.value) return
  editor.value.commands.insertContent(composerTextToDocument(value).content?.[0]?.content ?? [])
}
async function insertMixedClipboardParts(parts: readonly ChatMixedClipboardPart[]): Promise<void> {
  for (const part of parts) {
    if (part.type === 'text') insertClipboardText(part.text)
    else await insertImage(part.file)
  }
}
async function insertPlainClipboardParts(text: string, files: readonly File[]): Promise<void> {
  insertClipboardText(text)
  for (const file of files) await insertImage(file)
}
async function enqueueImages(files: readonly File[]): Promise<void> {
  if (!props.imageInputSupported) { message.warning('当前模型不支持图片输入'); return }
  if (!props.conversationId) { message.warning('请先选择对话'); return }
  const selectedFiles = selectChatImageFiles(files, imageItems.value.length + pendingImagePreparationCount.value)
  const imageFileCount = files.filter((file) => file.type.startsWith('image/')).length
  if (selectedFiles.length < imageFileCount) message.warning(`每条消息最多 ${maxChatImageCount} 张图片；原图不能超过 32 MiB，压缩后每张不能超过 1 MiB`)
  for (const file of selectedFiles) await insertImage(file)
}
function handleFileChange(event: Event): void { const input = event.target as HTMLInputElement; void enqueueImages(Array.from(input.files ?? [])); input.value = '' }

async function uploadImage(record: ImageUploadRecord): Promise<void> {
  if (!isCurrentUploadRecord(record) || record.controller) return
  const file = record.file
  if (!file) return
  const controller = new AbortController()
  record.controller = controller
  record.status = 'uploading'
  record.progress = 0
  record.error = ''
  patchImageNode(record.localId, imageNodeAttrs(record))
  try {
    const asset = await chatApi.uploadAsset(record.conversationId, file, {
      signal: controller.signal,
      onProgress: (progress) => {
        if (!isCurrentUploadRecord(record) || record.controller !== controller) return
        record.progress = progress
        patchImageNode(record.localId, { uploadProgress: progress })
      }
    })
    if (!isCurrentUploadRecord(record) || record.controller !== controller) {
      void chatApi.deleteAsset(record.conversationId, asset.id).catch(() => undefined)
      return
    }
    record.status = 'uploaded'
    record.progress = 100
    record.assetId = asset.id
    record.fileName = asset.fileName
    record.mimeType = asset.mimeType
    record.width = asset.width
    record.height = asset.height
    record.byteSize = asset.byteSize
    const previousPreviewUrl = record.previewUrl
    record.previewUrl = chatAssetContentUrl(record.conversationId, asset.id)
    record.file = undefined
    revokePreviewUrl(previousPreviewUrl)
    patchImageNode(record.localId, imageNodeAttrs(record))
  } catch (error) {
    if (controller.signal.aborted || !isCurrentUploadRecord(record)) return
    record.status = 'failed'
    record.error = extractApiErrorMessage(error, '图片上传失败')
    patchImageNode(record.localId, imageNodeAttrs(record))
  } finally {
    if (record.controller === controller) record.controller = undefined
  }
}

function retryImageUpload(localId: string): void {
  const record = imageUploadRecords.get(localId)
  if (!record || record.status === 'uploading' || !isCurrentUploadRecord(record)) return
  void uploadImage(record)
}

function removeImageUpload(localId: string): void {
  const record = imageUploadRecords.get(localId)
  if (!record) return
  if (record.status === 'uploaded') {
    queueMicrotask(pruneDetachedImageRecords)
    return
  }
  if (record.status === 'uploading') {
    record.controller?.abort()
    record.status = 'failed'
    record.progress = 0
    record.error = '上传已取消，可撤销删除后重试'
  }
  queueMicrotask(pruneDetachedImageRecords)
}

function pruneDetachedImageRecords(): void {
  const attached = new Set<string>()
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment') attached.add(String(node.attrs.localId ?? ''))
  })
  const detached = [...imageUploadRecords.entries()].filter(([localId, record]) => !attached.has(localId) && !record.submitted)
  for (const [, record] of detached) {
    if (record.status !== 'uploading') continue
    record.controller?.abort()
    record.status = 'failed'
    record.progress = 0
    record.error = '上传已取消，可撤销删除后重试'
  }
  let retainedBytes = detached.reduce((total, [, record]) => total + (record.file?.size ?? 0), 0)
  while (detached.length > 8 || retainedBytes > 64 * 1024 * 1024) {
    const oldest = detached.shift()
    if (!oldest) break
    retainedBytes -= oldest[1].file?.size ?? 0
    oldest[1].controller?.abort()
    if (oldest[1].status === 'uploaded') deleteUploadedAsset(oldest[1])
    revokePreviewUrl(oldest[1].previewUrl)
    imageUploadRecords.delete(oldest[0])
  }
}

function patchImageNode(localId: string, attrs: Record<string, unknown>): void {
  const currentEditor = editor.value
  if (!currentEditor) return
  let position: number | undefined
  currentEditor.state.doc.descendants((node, pos) => {
    if (position === undefined && node.type.name === 'chatImageAttachment' && node.attrs.localId === localId) position = pos
  })
  if (position === undefined) return
  const node = currentEditor.state.doc.nodeAt(position)
  if (!node) return
  const transaction = currentEditor.state.tr
    .setNodeMarkup(position, undefined, { ...node.attrs, ...attrs })
    .setMeta('addToHistory', false)
  currentEditor.view.dispatch(transaction)
}

function scheduleImageNodeSync(): void {
  if (imageSyncScheduled) return
  imageSyncScheduled = true
  queueMicrotask(() => {
    imageSyncScheduled = false
    const currentEditor = editor.value
    if (!currentEditor) return
    const pending: Array<{ localId: string; attrs: Record<string, unknown> }> = []
    currentEditor.state.doc.descendants((node) => {
      if (node.type.name !== 'chatImageAttachment') return
      const localId = String(node.attrs.localId ?? '')
      const record = imageUploadRecords.get(localId)
      if (record && (node.attrs.uploadStatus !== record.status || node.attrs.assetId !== record.assetId || node.attrs.uploadProgress !== record.progress)) {
        pending.push({ localId, attrs: imageNodeAttrs(record) })
      }
    })
    for (const item of pending) patchImageNode(item.localId, item.attrs)
    pruneDetachedImageRecords()
  })
}

function imageNodeAttrs(record: ImageUploadRecord): Record<string, unknown> {
  return {
    localId: record.localId,
    assetId: record.assetId,
    previewUrl: record.previewUrl,
    fileName: record.fileName,
    mimeType: record.mimeType,
    width: record.width,
    height: record.height,
    byteSize: record.byteSize,
    uploadStatus: record.status,
    uploadProgress: record.progress,
    uploadError: record.error
  }
}

function isCurrentUploadRecord(record: ImageUploadRecord): boolean {
  return imageUploadRecords.get(record.localId) === record
    && record.generation === conversationGeneration
    && record.conversationId === props.conversationId
}

function disposeImageUploadRecords(deleteUploadedAssets = false): void {
  for (const record of imageUploadRecords.values()) {
    record.controller?.abort()
    if (deleteUploadedAssets && record.status === 'uploaded' && !record.submitted) deleteUploadedAsset(record)
    revokePreviewUrl(record.previewUrl)
  }
  imageUploadRecords.clear()
}
function deleteUploadedAsset(record: ImageUploadRecord): void { if (record.assetId) void chatApi.deleteAsset(record.conversationId, record.assetId).catch(() => undefined) }
function revokePreviewUrl(value: string): void { if (value.startsWith('blob:')) URL.revokeObjectURL(value) }
function syncPendingImagePreparationCount(): void { pendingImagePreparationCount.value = imagePreparationState.snapshot().pendingCount }
function advanceConversationGeneration(): void {
  const snapshot = imagePreparationState.advanceGeneration()
  conversationGeneration = snapshot.generation
  pendingImagePreparationCount.value = snapshot.pendingCount
}

onBeforeUnmount(() => {
  advanceConversationGeneration()
  disposeImageUploadRecords(true)
})
defineExpose({ getSnapshot, setText, setBlocks, restore, clear, focus, releaseSubmittedAssets })
</script>

<style scoped>
.ai-composer { position: relative; border: 1px solid #cbd5e1; border-radius: 10px; background: #fff; box-shadow: 0 2px 8px rgba(15, 23, 42, .05); }
.ai-composer:focus-within { border-color: #1677ff; box-shadow: 0 0 0 2px rgba(22, 119, 255, .1); }
.ai-composer-footer { min-height: 44px; display: flex; align-items: center; gap: 4px; padding: 5px 8px; color: #94a3b8; font-size: 11px; border-top: 1px solid #eef2f7; }
.ai-composer-model-controls { min-width: 0; flex: 1; display: flex; align-items: center; gap: 1px; overflow-x: auto; scrollbar-width: none; }
.ai-composer-model-controls::-webkit-scrollbar { display: none; }
.ai-composer-model-controls :deep(.ant-select-selector) { padding-inline: 5px !important; color: #475569; font-size: 12px; }
.ai-composer-context { width: 26px; height: 26px; flex: 0 0 26px; display: inline-flex; align-items: center; justify-content: center; color: #64748b; }
.ai-composer-file { display: none; }
.ai-composer-editor { min-height: 56px; max-height: 220px; overflow-y: auto; padding: 9px 12px; }
.ai-composer-editor :deep(.ProseMirror) { min-height: 38px; outline: none; white-space: pre-wrap; overflow-wrap: anywhere; }
.ai-composer-editor :deep(.ProseMirror p.is-editor-empty:first-child::before) { color: #9aa6b2; content: attr(data-placeholder); float: left; height: 0; pointer-events: none; }
.ai-composer-command-menu { position: absolute; z-index: 3; bottom: 76px; left: 8px; width: min(360px, calc(100% - 16px)); padding: 5px; background: #fff; border: 1px solid #dbe3ec; border-radius: 7px; box-shadow: 0 10px 24px rgba(15, 23, 42, .14); }
.ai-composer-command-menu button { width: 100%; display: grid; grid-template-columns: 90px 90px 1fr; gap: 6px; padding: 7px 8px; text-align: left; border: 0; background: transparent; cursor: pointer; }
.ai-composer-command-menu button:hover { background: #f0f7ff; }
.ai-composer-command-menu button.is-active { background: #eaf3ff; }
.ai-composer-command-menu small { color: #94a3b8; }
@media (max-width: 520px) {
  .ai-composer-footer { align-items: flex-end; }
  .ai-composer-model-controls :deep(.ant-select-selector) { padding-inline: 3px !important; }
}
@media (pointer: coarse) {
  .ai-composer-context { width: 44px; height: 44px; flex-basis: 44px; }
  .ai-composer-footer :deep(.ant-btn) { min-width: 44px; height: 44px; padding: 0; }
  .ai-composer-model-controls :deep(.ant-select-selector) { min-height: 44px !important; align-items: center; }
  .ai-composer-model-controls :deep(.ant-select-selection-search-input) { height: 44px !important; }
}
</style>
