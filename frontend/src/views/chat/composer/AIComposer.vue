<template>
  <div class="ai-composer" :class="{ 'is-disabled': disabled }">
    <input ref="fileInput" class="ai-composer-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif" :disabled="disabled || !imageInputSupported || !imagePolicy || !conversationId" multiple @change="handleFileChange" />
    <EditorContent :editor="editor" class="ai-composer-editor" />
    <div v-if="commandOpen" class="ai-composer-command-menu" role="listbox">
      <button v-for="(item, index) in commandItems" :key="item.key" type="button" role="option" :aria-selected="index === commandIndex" :aria-label="`/${item.key}：${item.description}`" :class="{ 'is-active': index === commandIndex }" @mouseenter="commandIndex = index" @mousedown.prevent="selectCommand(item)">
        <strong>/{{ item.key }}</strong><span class="ai-composer-command-description">{{ item.description }}</span>
      </button>
    </div>
    <a-modal
      v-model:open="generationParametersModalOpen"
      centered
      :width="640"
      :footer="null"
      :mask-closable="true"
      :destroy-on-close="false"
      wrap-class-name="chat-generation-parameters-modal"
      @after-close="focus"
    >
      <template #title>
        <div class="chat-generation-modal-title">
          <div>
            <strong>生成参数</strong>
            <span>仅显示当前模型和路由实际支持的控制项</span>
          </div>
          <span class="chat-generation-modal-count">{{ generationParameterCapabilities.length }} 项可用</span>
        </div>
      </template>
      <section class="chat-generation-parameters" aria-label="生成参数设置">
        <p class="chat-generation-modal-intro">参数会在下一次发送时生效。保留关闭状态即可继续使用模型默认行为。</p>
        <div v-if="generationParameterCapabilities.length" class="chat-generation-parameter-list">
          <article v-for="capability in generationParameterCapabilities" :key="capability.parameter" class="chat-generation-parameter-card" :class="{ 'is-enabled': generationParameterEnabled(capability.parameter) }">
            <div class="chat-generation-parameter-card-heading">
              <div>
                <h3>{{ generationParameterLabel(capability.parameter) }}</h3>
                <p>{{ generationParameterDescription(capability.parameter) }}</p>
              </div>
              <a-switch :checked="generationParameterEnabled(capability.parameter)" :disabled="disabled" checked-children="启用" un-checked-children="默认" @update:checked="toggleGenerationParameter(capability.parameter, $event)" />
            </div>
            <template v-if="generationParameterEnabled(capability.parameter)">
              <a-input-number
                v-if="capability.parameter === 'maxOutputTokens' || capability.parameter === 'seed'"
                :value="generationParameters[capability.parameter]"
                :min="capability.min"
                :max="capability.max"
                :step="capability.step"
                :precision="0"
                :disabled="disabled"
                size="large"
                class="chat-generation-parameter-number"
                @update:value="updateGenerationParameter(capability.parameter, $event)"
              />
              <a-slider
                v-else
                :value="generationParameters[capability.parameter]"
                :min="capability.min"
                :max="capability.max"
                :step="capability.step"
                :disabled="disabled"
                :tooltip="{ formatter: (value: number) => formatGenerationParameterValue(capability.parameter, value) }"
                @update:value="updateGenerationParameter(capability.parameter, $event)"
              />
            </template>
            <div class="chat-generation-parameter-meta">
              <span>范围 {{ formatGenerationParameterRange(capability.min, capability.max, capability.step) }}</span>
              <span v-if="generationParameterEnabled(capability.parameter)">当前 {{ formatGenerationParameterValue(capability.parameter, generationParameters[capability.parameter] ?? capability.defaultValue) }}</span>
              <span v-else>推荐 {{ formatGenerationParameterValue(capability.parameter, capability.defaultValue) }}</span>
            </div>
          </article>
        </div>
        <a-empty v-else :image="aEmptyImageSimple" description="当前模型没有可调整的生成参数" />
        <div class="chat-generation-modal-actions">
          <span>温度和 Top P 不能同时启用。</span>
          <div>
            <a-button :disabled="disabled || !hasEnabledGenerationParameters" @click="restoreGenerationParameterDefaults">恢复推荐值</a-button>
            <a-button type="primary" @click="generationParametersModalOpen = false">完成</a-button>
          </div>
        </div>
      </section>
    </a-modal>
    <div class="ai-composer-footer">
      <div class="ai-composer-model-controls">
        <a-dropdown :trigger="['click']" placement="topLeft">
          <a-tooltip title="打开工具箱"><a-button class="ai-composer-toolbox-trigger" type="text" size="small" aria-label="打开工具箱"><PlusOutlined /></a-button></a-tooltip>
          <template #overlay>
            <a-menu @click="handleToolboxMenuClick">
              <a-menu-item key="image" :disabled="Boolean(imageToolDisabledReason)" :title="imageToolDisabledReason || '添加图片'">
                <a-tooltip :title="imageToolDisabledReason || '从本地添加图片'" placement="right">
                  <span><PictureOutlined /> 添加图片</span>
                </a-tooltip>
              </a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
        <a-select :value="modelValue" :options="modelSelectOptions" :loading="modelsLoading" :disabled="disabled" size="small" :bordered="false" aria-label="选择模型" :style="{ width: `${modelControlWidths.triggerWidth}px` }" :dropdown-match-select-width="modelControlWidths.popupWidth" @dropdown-visible-change="handleModelDropdownVisibleChange" @update:value="emit('update:modelValue', $event)" />
        <a-select v-if="reasoningOptions.length" :value="reasoningEffort" :options="reasoningOptions" :disabled="disabled" allow-clear size="small" :bordered="false" aria-label="思考级别" :style="{ width: `${reasoningControlWidths.triggerWidth}px` }" :dropdown-match-select-width="reasoningControlWidths.popupWidth" @update:value="handleReasoningEffortUpdate" />
        <a-select v-if="serviceTierOptions.length" :value="serviceTier" :options="serviceTierOptions" :disabled="disabled" allow-clear size="small" :bordered="false" aria-label="服务等级" :style="{ width: `${serviceTierControlWidths.triggerWidth}px` }" :dropdown-match-select-width="serviceTierControlWidths.popupWidth" @update:value="handleServiceTierUpdate" />
      </div>
      <a-tooltip :title="contextTooltip">
        <span class="ai-composer-context" role="img" :aria-label="`上下文用量 ${contextTooltip}`">
          <a-spin v-if="contextStatusLoading" size="small" />
          <a-progress v-else type="circle" :percent="contextPercent" :size="18" :stroke-width="12" :show-info="false" :status="contextProgressStatus" />
        </span>
      </a-tooltip>
      <a-tooltip v-if="stoppable" title="停止生成"><a-button danger type="primary" aria-label="停止生成" @click="emit('stop')"><StopOutlined /></a-button></a-tooltip>
      <a-tooltip v-else :title="sendTooltip"><a-button type="primary" aria-label="发送" :disabled="!canSubmit" @click="submit"><SendOutlined /></a-button></a-tooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { Empty as AEmpty } from 'ant-design-vue'
import { PictureOutlined, PlusOutlined, SendOutlined, StopOutlined } from '@ant-design/icons-vue'
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
import { chatGenerationParameterDescription, chatGenerationParameterLabel, reasoningEffortLabel, selectableChatReasoningEfforts } from './chatModelControls'
import { replaceEditorContentWithoutHistory } from './chatEditorDocumentBoundary'
import { maxChatImageCount, selectChatImageFiles, selectChatImageFileSlots } from './chatImageSelection'
import { ChatImagePreparationQueue } from './chatImagePreparationQueue'
import { prepareChatImageForUpload } from './chatImageProcessing'
import type { ChatContextStatus, ChatGenerationParameter, ChatGenerationParameters, ChatModelCapabilities, ChatModelListOption, ChatReasoningEffort, ChatServiceTier } from '@/types/domain/chat'
import type { ChatImagePolicy } from '@/types/domain/chat'

const props = defineProps<{
  contextStatus?: ChatContextStatus
  contextStatusLoading: boolean
  conversationId: string
  disabled: boolean
  stoppable: boolean
  turnLimitReached: boolean
  turnLimitMessage: string
  imageInputSupported: boolean
  imagePolicy?: ChatImagePolicy
  modelOptions: ChatModelListOption[]
  modelCapabilities?: ChatModelCapabilities
  modelValue?: string
  modelsLoading: boolean
  modelCapabilitiesLoading: boolean
  reasoningEffort: ChatReasoningEffort | ''
  serviceTier: ChatServiceTier | ''
  generationParameters: ChatGenerationParameters
  mobile: boolean
}>()
const emit = defineEmits<{
  (event: 'submit', payload: { blocks: ChatInputBlock[]; snapshot: JSONContent }): void
  (event: 'stop' | 'models-open'): void
  (event: 'conversation-action', action: 'set-image-model' | 'compact-context' | 'clear-conversation'): void
  (event: 'update:modelValue', value?: string): void
  (event: 'update:reasoningEffort', value: ChatReasoningEffort | ''): void
  (event: 'update:serviceTier', value: ChatServiceTier | ''): void
  (event: 'update:generationParameters', value: ChatGenerationParameters): void
}>()

function handleModelDropdownVisibleChange(open: boolean): void {
  if (open) emit('models-open')
}
function handleReasoningEffortUpdate(value?: ChatReasoningEffort): void {
  emit('update:reasoningEffort', value ?? '')
}
function handleServiceTierUpdate(value?: ChatServiceTier): void {
  emit('update:serviceTier', value ?? '')
}
function generationParameterEnabled(parameter: ChatGenerationParameter): boolean {
  return props.generationParameters[parameter] !== undefined
}
function updateGenerationParameter(parameter: ChatGenerationParameter, value: number | number[] | null | undefined): void {
  if (typeof value !== 'number') return
  const capability = generationParameterCapabilities.value.find((item) => item.parameter === parameter)
  if (!capability) return
  const next = { ...props.generationParameters, [parameter]: value }
  if (parameter === 'temperature') delete next.topP
  if (parameter === 'topP') delete next.temperature
  emit('update:generationParameters', next)
}
function toggleGenerationParameter(parameter: ChatGenerationParameter, enabled: boolean): void {
  const next = { ...props.generationParameters }
  if (!enabled) delete next[parameter]
  else {
    const capability = generationParameterCapabilities.value.find((item) => item.parameter === parameter)
    if (capability) next[parameter] = capability.defaultValue
    if (parameter === 'temperature') delete next.topP
    if (parameter === 'topP') delete next.temperature
  }
  emit('update:generationParameters', next)
}
function handleToolboxMenuClick(event: { key: string | number }): void {
  if (String(event.key) === 'image') openImagePicker()
}
function openImagePicker(): void {
  if (imageToolDisabledReason.value) {
    message.warning(imageToolDisabledReason.value)
    return
  }
  fileInput.value?.click()
}
const fileInput = ref<HTMLInputElement>()
const commandOpen = ref(false)
const commandQuery = ref('')
const commandIndex = ref(0)
const generationParametersModalOpen = ref(false)
const contentRevision = ref(0)
const pendingImagePreparationCount = ref(0)
let conversationGeneration = 0
let imageSyncScheduled = false

type ImageUploadStatus = 'preparing' | 'uploading' | 'uploaded' | 'failed'
interface ImageUploadRecord {
  localId: string
  file?: File
  sourceFile?: File
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
  canceled: boolean
  controller?: AbortController
}
const imageUploadRecords = new Map<string, ImageUploadRecord>()
const imagePreparationQueue = new ChatImagePreparationQueue<ImageUploadRecord>({
  maxConcurrency: 2,
  run: prepareImageRecord,
  onChange: (snapshot) => { pendingImagePreparationCount.value = snapshot.pendingCount }
})

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
      if (imageToolDisabledReason.value) { message.warning(imageToolDisabledReason.value); return true }
      const imageFileSlots = selectChatImageFileSlots(imageFiles, currentComposerImageCount())
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

const commandItems = computed(() => commandOpen.value ? filterChatComposerCommands(commandQuery.value) : chatComposerCommands)
const imageToolDisabledReason = computed(() => props.disabled
  ? '当前正在处理消息，请稍后添加图片'
  : !props.conversationId
    ? '请先选择对话'
    : !props.imageInputSupported
      ? '当前模型不支持图片输入'
      : !props.imagePolicy
        ? '图片处理策略正在加载'
      : '')
const selectedModelOption = computed(() => props.modelCapabilities?.id === props.modelValue ? props.modelCapabilities : undefined)
const modelSelectOptions = computed(() => props.modelOptions.map((item) => ({ label: item.name, value: item.id, title: item.name })))
const reasoningOptions = computed(() => selectableChatReasoningEfforts(selectedModelOption.value).map((value) => {
  const label = `思考 ${reasoningEffortLabel(value)}`
  return { label, value, title: label }
}))
const serviceTierOptions = computed(() => (selectedModelOption.value?.supportedServiceTiers ?? []).map((value) => {
  const label = value === 'default' ? '服务 默认' : value === 'priority' ? '服务 优先' : '服务 Flex'
  return { label, value, title: label }
}))
const generationParameterCapabilities = computed(() => selectedModelOption.value?.generationParameters ?? [])
const generationParameterLabel = chatGenerationParameterLabel
const generationParameterDescription = chatGenerationParameterDescription
const aEmptyImageSimple = AEmpty.PRESENTED_IMAGE_SIMPLE
const hasEnabledGenerationParameters = computed(() => generationParameterCapabilities.value.some((item) => generationParameterEnabled(item.parameter)))
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
  if (props.contextStatusLoading) return '正在加载上下文用量'
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
const canSubmit = computed(() => Boolean(hasContent.value && props.modelValue && props.modelCapabilities && !props.modelsLoading && !props.modelCapabilitiesLoading && imagesReady.value && pendingImagePreparationCount.value === 0 && !props.disabled && !props.turnLimitReached && (props.imageInputSupported || imageItems.value.length === 0)))
const sendTooltip = computed(() => {
  if (props.turnLimitReached) return props.turnLimitMessage
  if (pendingImagePreparationCount.value > 0) return '图片正在压缩，请稍候'
  if (imageItems.value.some((item) => item.uploadStatus === 'preparing')) return '图片正在压缩，请稍候'
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
        previewUrl: chatAssetContentUrl(props.conversationId, block.assetId, 'preview'),
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
    record.canceled = true
    imagePreparationQueue.cancel(record)
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
  if (item.kind === 'conversation') {
    editor.value.chain().focus().deleteRange(command.range).run()
    emit('conversation-action', item.action)
  } else {
    editor.value.chain().focus().deleteRange(command.range).run()
    if (item.kind === 'image') openImagePicker()
    else if (item.kind === 'generation') openGenerationParameters()
  }
  commandOpen.value = false
}
function openGenerationParameters(): void {
  if (!generationParameterCapabilities.value.length) {
    message.info('当前模型没有可调整的生成参数')
    return
  }
  generationParametersModalOpen.value = true
}
function restoreGenerationParameterDefaults(): void {
  const next = { ...props.generationParameters }
  for (const capability of generationParameterCapabilities.value) {
    if (next[capability.parameter] !== undefined) next[capability.parameter] = capability.defaultValue
  }
  emit('update:generationParameters', next)
}
function formatGenerationParameterRange(min: number, max: number, step: number): string {
  return `${formatCompactNumber(min)} – ${formatCompactNumber(max)}${step > 0 && step !== 1 ? `，步长 ${formatCompactNumber(step)}` : ''}`
}
function formatGenerationParameterValue(parameter: ChatGenerationParameter, value: number): string {
  return parameter === 'maxOutputTokens' || parameter === 'seed' ? formatCompactNumber(Math.trunc(value)) : formatCompactNumber(value)
}
function formatCompactNumber(value: number): string {
  return Number.isInteger(value) ? value.toLocaleString('zh-CN') : String(Number(value.toFixed(4)))
}
function currentComposerImageCount(): number {
  let count = 0
  editor.value?.state.doc.descendants((node) => { if (node.type.name === 'chatImageAttachment') count += 1 })
  return count
}
function insertImage(sourceFile: File): void {
  const preparationConversationId = props.conversationId
  const preparationGeneration = conversationGeneration
  const preparationEditor = editor.value
  if (!props.imageInputSupported || !preparationConversationId || !preparationEditor || currentComposerImageCount() >= maxChatImageCount) return
  const localId = crypto.randomUUID()
  const previewUrl = URL.createObjectURL(sourceFile)
  const record: ImageUploadRecord = {
    localId,
    sourceFile,
    previewUrl,
    conversationId: preparationConversationId,
    generation: preparationGeneration,
    status: 'preparing',
    progress: 0,
    assetId: '',
    fileName: sourceFile.name || '图片',
    mimeType: sourceFile.type,
    width: 0,
    height: 0,
    byteSize: sourceFile.size,
    error: '',
    submitted: false,
    canceled: false
  }
  const inserted = preparationEditor.commands.insertContent({ type: 'chatImageAttachment', attrs: imageNodeAttrs(record) })
  if (!inserted) {
    URL.revokeObjectURL(previewUrl)
    return
  }
  imageUploadRecords.set(localId, record)
  imagePreparationQueue.enqueue(record)
}
async function prepareImageRecord(record: ImageUploadRecord): Promise<void> {
  const sourceFile = record.sourceFile
  if (!sourceFile || !isCurrentUploadRecord(record)) return
  record.status = 'preparing'
  record.progress = 0
  record.error = ''
  patchImageNode(record.localId, imageNodeAttrs(record))
  let file: File
  try {
    if (!props.imagePolicy) throw new Error('图片处理策略尚未加载，请稍后重试')
    file = await prepareChatImageForUpload(sourceFile, props.imagePolicy.input)
  } catch (error) {
    if (!isCurrentUploadRecord(record)) return
    record.status = 'failed'
    record.error = error instanceof Error ? error.message : '图片压缩失败'
    patchImageNode(record.localId, imageNodeAttrs(record))
    message.warning(record.error)
    return
  }
  if (!isCurrentUploadRecord(record)) return
  record.file = file
  record.sourceFile = undefined
  record.fileName = file.name || record.fileName
  record.mimeType = file.type
  record.byteSize = file.size
  void uploadImage(record)
}
function insertClipboardText(value: string): void {
  if (!value || !editor.value) return
  editor.value.commands.insertContent(composerTextToDocument(value).content?.[0]?.content ?? [])
}
function insertMixedClipboardParts(parts: readonly ChatMixedClipboardPart[]): void {
  for (const part of parts) {
    if (part.type === 'text') insertClipboardText(part.text)
    else insertImage(part.file)
  }
}
function insertPlainClipboardParts(text: string, files: readonly File[]): void {
  insertClipboardText(text)
  for (const file of files) insertImage(file)
}
function enqueueImages(files: readonly File[]): void {
  if (!props.imageInputSupported) { message.warning('当前模型不支持图片输入'); return }
  if (!props.conversationId) { message.warning('请先选择对话'); return }
  const selectedFiles = selectChatImageFiles(files, currentComposerImageCount())
  const imageFileCount = files.filter((file) => file.type.startsWith('image/')).length
  if (selectedFiles.length < imageFileCount) message.warning(`每条消息最多 ${maxChatImageCount} 张图片；原图不能超过 32 MiB，压缩后每张不能超过 1 MiB`)
  for (const file of selectedFiles) insertImage(file)
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
    record.previewUrl = chatAssetContentUrl(record.conversationId, asset.id, 'preview')
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
  if (!record) {
    message.warning('图片已删除，请重新选择图片')
    return
  }
  if (record.status === 'preparing' || record.status === 'uploading') return
  record.canceled = false
  if (!isCurrentUploadRecord(record)) return
  if (record.file) void uploadImage(record)
  else imagePreparationQueue.enqueue(record)
}

function removeImageUpload(localId: string): void {
  const record = imageUploadRecords.get(localId)
  if (!record) return
  record.canceled = true
  imagePreparationQueue.cancel(record)
  if (record.status === 'uploaded') {
    queueMicrotask(pruneDetachedImageRecords)
    return
  }
  if (record.status === 'preparing' || record.status === 'uploading') {
    record.controller?.abort()
    record.status = 'failed'
    record.progress = 0
    record.error = '上传已取消，请重新选择图片'
  }
  queueMicrotask(pruneDetachedImageRecords)
}

function pruneDetachedImageRecords(): void {
  const attached = new Set<string>()
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment') attached.add(String(node.attrs.localId ?? ''))
  })
  const detached = [...imageUploadRecords.entries()].filter(([localId, record]) => !attached.has(localId) && !record.submitted)
  for (const [localId, record] of detached) {
    record.canceled = true
    imagePreparationQueue.cancel(record)
    record.controller?.abort()
    if (record.status === 'uploaded') deleteUploadedAsset(record)
    record.sourceFile = undefined
    record.file = undefined
    revokePreviewUrl(record.previewUrl)
    imageUploadRecords.delete(localId)
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
    && !record.canceled
    && record.generation === conversationGeneration
    && record.conversationId === props.conversationId
    && isImageNodeAttached(record.localId)
}

function isImageNodeAttached(localId: string): boolean {
  let attached = false
  editor.value?.state.doc.descendants((node) => {
    if (node.type.name === 'chatImageAttachment' && node.attrs.localId === localId) attached = true
  })
  return attached
}

function disposeImageUploadRecords(deleteUploadedAssets = false): void {
  for (const record of imageUploadRecords.values()) {
    record.canceled = true
    imagePreparationQueue.cancel(record)
    record.controller?.abort()
    if (deleteUploadedAssets && record.status === 'uploaded' && !record.submitted) deleteUploadedAsset(record)
    revokePreviewUrl(record.previewUrl)
  }
  imageUploadRecords.clear()
  imagePreparationQueue.clear()
}
function deleteUploadedAsset(record: ImageUploadRecord): void { if (record.assetId) void chatApi.deleteAsset(record.conversationId, record.assetId).catch(() => undefined) }
function revokePreviewUrl(value: string): void { if (value.startsWith('blob:')) URL.revokeObjectURL(value) }
function advanceConversationGeneration(): void {
  conversationGeneration += 1
  imagePreparationQueue.clear()
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
.ai-composer-toolbox-trigger { flex: 0 0 auto; }
.ai-composer-file { display: none; }
.ai-composer-editor { min-height: 56px; max-height: 220px; overflow-y: auto; padding: 9px 12px; }
.ai-composer-editor :deep(.ProseMirror) { min-height: 38px; outline: none; white-space: pre-wrap; overflow-wrap: anywhere; }
.ai-composer-editor :deep(.ProseMirror p.is-editor-empty:first-child::before) { color: #9aa6b2; content: attr(data-placeholder); float: left; height: 0; pointer-events: none; }
.ai-composer-command-menu { position: absolute; z-index: 3; bottom: 76px; left: 8px; width: min(430px, calc(100% - 16px)); padding: 5px; background: #fff; border: 1px solid #dbe3ec; border-radius: 7px; box-shadow: 0 10px 24px rgba(15, 23, 42, .14); }
.ai-composer-command-menu button { width: 100%; display: grid; grid-template-columns: minmax(96px, auto) minmax(0, 1fr); align-items: start; column-gap: 12px; padding: 8px; text-align: left; border: 0; background: transparent; cursor: pointer; }
.ai-composer-command-menu strong { color: #1f2937; font-size: 13px; line-height: 19px; white-space: nowrap; }
.ai-composer-command-description { color: #64748b; font-size: 12px; line-height: 19px; }
.ai-composer-command-menu button:hover { background: #f0f7ff; }
.ai-composer-command-menu button.is-active { background: #eaf3ff; }
:global(.chat-generation-parameters-modal .ant-modal) { max-width: calc(100vw - 24px); }
:global(.chat-generation-parameters-modal .ant-modal-content) { overflow: hidden; padding: 0; border: 1px solid #dbe7f5; border-radius: 16px; box-shadow: 0 24px 64px rgba(15, 23, 42, .2); }
:global(.chat-generation-parameters-modal .ant-modal-header) { margin: 0; padding: 20px 24px 16px; border-bottom: 1px solid #e8eef6; background: linear-gradient(135deg, #f8fbff 0%, #ffffff 70%); }
:global(.chat-generation-parameters-modal .ant-modal-body) { padding: 20px 24px 18px; }
:global(.chat-generation-parameters-modal .ant-modal-close) { top: 17px; inset-inline-end: 18px; color: #64748b; }
.chat-generation-modal-title { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; padding-right: 28px; }
.chat-generation-modal-title strong { display: block; color: #172033; font-size: 18px; line-height: 25px; }
.chat-generation-modal-title span { display: block; margin-top: 3px; color: #718096; font-size: 12px; line-height: 18px; font-weight: 400; }
.chat-generation-modal-title .chat-generation-modal-count { flex: 0 0 auto; margin-top: 2px; padding: 3px 8px; border: 1px solid #cfe1fa; border-radius: 999px; color: #1768d6; background: #edf6ff; font-size: 12px; line-height: 18px; }
.chat-generation-parameters { display: grid; gap: 16px; color: #334155; }
.chat-generation-modal-intro { margin: 0; color: #64748b; font-size: 13px; line-height: 20px; }
.chat-generation-parameter-list { display: grid; gap: 10px; }
.chat-generation-parameter-card { display: grid; gap: 12px; padding: 14px 15px 12px; border: 1px solid #e1e8f0; border-radius: 12px; background: #fbfdff; transition: border-color .18s ease, background-color .18s ease, box-shadow .18s ease; }
.chat-generation-parameter-card.is-enabled { border-color: #bcd8fc; background: #f7fbff; box-shadow: 0 4px 12px rgba(22, 119, 255, .07); }
.chat-generation-parameter-card-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.chat-generation-parameter-card-heading h3 { margin: 0; color: #24344d; font-size: 14px; line-height: 21px; font-weight: 650; }
.chat-generation-parameter-card-heading p { max-width: 450px; margin: 3px 0 0; color: #718096; font-size: 12px; line-height: 18px; }
.chat-generation-parameter-card-heading :deep(.ant-switch) { flex: 0 0 auto; margin-top: 2px; }
.chat-generation-parameter-card :deep(.ant-slider) { margin: 3px 7px 1px; }
.chat-generation-parameter-number { width: 100%; }
.chat-generation-parameter-meta { display: flex; align-items: center; justify-content: space-between; gap: 12px; color: #8895a7; font-size: 12px; line-height: 18px; }
.chat-generation-parameter-meta span:last-child { color: #4d6482; font-variant-numeric: tabular-nums; }
.chat-generation-modal-actions { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding-top: 4px; border-top: 1px solid #edf1f6; color: #7c899a; font-size: 12px; line-height: 18px; }
.chat-generation-modal-actions > div { display: flex; gap: 8px; }
@media (max-width: 520px) {
  .ai-composer-footer { align-items: flex-end; }
  .ai-composer-model-controls :deep(.ant-select-selector) { padding-inline: 3px !important; }
  :global(.chat-generation-parameters-modal .ant-modal-header) { padding: 17px 18px 14px; }
  :global(.chat-generation-parameters-modal .ant-modal-body) { padding: 16px 18px; }
  .chat-generation-modal-title { gap: 8px; }
  .chat-generation-modal-title strong { font-size: 16px; }
  .chat-generation-modal-title .chat-generation-modal-count { display: none; }
  .chat-generation-parameter-card { padding: 13px; }
  .chat-generation-parameter-card-heading { gap: 10px; }
  .chat-generation-modal-actions { align-items: flex-start; flex-direction: column; }
  .chat-generation-modal-actions > div { width: 100%; }
  .chat-generation-modal-actions :deep(.ant-btn) { flex: 1; }
}
@media (pointer: coarse) {
  .ai-composer-context { width: 44px; height: 44px; flex-basis: 44px; }
  .ai-composer-footer :deep(.ant-btn) { min-width: 44px; height: 44px; padding: 0; }
  .ai-composer-model-controls :deep(.ant-select-selector) { min-height: 44px !important; align-items: center; }
  .ai-composer-model-controls :deep(.ant-select-selection-search-input) { height: 44px !important; }
}
</style>
