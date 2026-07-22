<template>
  <NodeViewWrapper
    as="span"
    class="chat-image-node"
    :class="[`is-${uploadStatus}`, { 'is-selected': selected }]"
    data-chat-image
    :data-asset-id="assetId"
    contenteditable="false"
  >
    <img :src="previewUrl" :alt="fileName" draggable="false" />
    <span v-if="uploadStatus === 'preparing'" class="chat-image-node-status" aria-live="polite">
      <LoadingOutlined spin />
      <span>处理中</span>
    </span>
    <span v-else-if="uploadStatus === 'uploading'" class="chat-image-node-status" aria-live="polite">
      <LoadingOutlined spin />
      <span>{{ uploadProgress > 0 ? `${uploadProgress}%` : '上传中' }}</span>
    </span>
    <span v-else-if="uploadStatus === 'failed'" class="chat-image-node-status is-error" :title="uploadError || '图片上传失败'">
      <WarningOutlined />
      <span>上传失败</span>
      <button type="button" title="重试上传" aria-label="重试上传" @mousedown.prevent.stop @click.stop="retryUpload"><RedoOutlined /></button>
    </span>
    <button class="chat-image-node-remove" type="button" title="删除图片" aria-label="删除图片" @mousedown.prevent.stop @click.stop="removeImage"><CloseOutlined /></button>
  </NodeViewWrapper>
</template>

<script setup lang="ts">
import type { NodeViewProps } from '@tiptap/core'
import { CloseOutlined, LoadingOutlined, RedoOutlined, WarningOutlined } from '@ant-design/icons-vue'
import { NodeViewWrapper } from '@tiptap/vue-3'
import { computed } from 'vue'
import type { ChatImageAttachmentOptions } from './ChatImageAttachment'

const props = defineProps<NodeViewProps>()

const attrs = computed(() => props.node.attrs as Record<string, unknown>)
const localId = computed(() => String(attrs.value.localId ?? ''))
const assetId = computed(() => String(attrs.value.assetId ?? ''))
const previewUrl = computed(() => String(attrs.value.previewUrl ?? ''))
const fileName = computed(() => String(attrs.value.fileName ?? '图片'))
const uploadStatus = computed(() => String(attrs.value.uploadStatus ?? 'uploading'))
const uploadProgress = computed(() => Number(attrs.value.uploadProgress ?? 0))
const uploadError = computed(() => String(attrs.value.uploadError ?? ''))

function retryUpload(): void {
  const options = props.extension.options as ChatImageAttachmentOptions
  if (localId.value) options.onRetry(localId.value)
}

function removeImage(): void {
  props.deleteNode()
  const options = props.extension.options as ChatImageAttachmentOptions
  if (localId.value) options.onRemove(localId.value)
}
</script>

<style scoped>
.chat-image-node { position: relative; width: min(180px, 42vw); height: 120px; display: inline-flex; align-items: center; justify-content: center; overflow: hidden; margin: 2px 4px; vertical-align: middle; background: #f8fafc; border: 1px solid #dbe3ec; border-radius: 6px; }
.chat-image-node.is-selected { border-color: #1677ff; box-shadow: 0 0 0 2px rgba(22, 119, 255, .12); }
.chat-image-node img { width: 100%; height: 100%; display: block; object-fit: contain; }
.chat-image-node.is-preparing img, .chat-image-node.is-uploading img, .chat-image-node.is-failed img { opacity: .55; }
.chat-image-node-status { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; gap: 6px; color: #334155; font-size: 12px; background: rgba(248, 250, 252, .72); }
.chat-image-node-status.is-error { color: #b42318; background: rgba(255, 247, 237, .84); }
.chat-image-node-status button, .chat-image-node-remove { width: 24px; height: 24px; display: inline-flex; align-items: center; justify-content: center; padding: 0; color: inherit; background: rgba(255, 255, 255, .92); border: 1px solid currentColor; border-radius: 50%; cursor: pointer; }
.chat-image-node-remove { position: absolute; top: 5px; right: 5px; color: #475569; border-color: #cbd5e1; opacity: 0; transition: opacity .15s ease; }
.chat-image-node:hover .chat-image-node-remove, .chat-image-node:focus-within .chat-image-node-remove, .chat-image-node.is-selected .chat-image-node-remove, .chat-image-node.is-failed .chat-image-node-remove { opacity: 1; }
@media (pointer: coarse) {
  .chat-image-node-status button, .chat-image-node-remove { width: 44px; height: 44px; }
  .chat-image-node-remove { top: 2px; right: 2px; opacity: 1; }
}
</style>
