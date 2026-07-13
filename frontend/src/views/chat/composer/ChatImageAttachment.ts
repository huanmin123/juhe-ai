import { Node } from '@tiptap/core'
import { VueNodeViewRenderer } from '@tiptap/vue-3'
import ChatImageAttachmentView from './ChatImageAttachmentView.vue'

export interface ChatImageAttachmentOptions {
  onRetry: (localId: string) => void
  onRemove: (localId: string) => void
}

export const ChatImageAttachment = Node.create<ChatImageAttachmentOptions>({
  name: 'chatImageAttachment',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,
  draggable: true,
  addOptions() {
    return { onRetry: () => undefined, onRemove: () => undefined }
  },
  addAttributes() {
    return {
      localId: { default: '' },
      assetId: { default: '' },
      previewUrl: { default: '' },
      fileName: { default: '图片' },
      mimeType: { default: '' },
      width: { default: 0 },
      height: { default: 0 },
      byteSize: { default: 0 },
      uploadStatus: { default: 'uploading' },
      uploadProgress: { default: 0 },
      uploadError: { default: '' }
    }
  },
  parseHTML() { return [{ tag: 'span[data-chat-image]' }] },
  renderHTML({ HTMLAttributes }) {
    return ['span', {
      'data-chat-image': '',
      'data-asset-id': String(HTMLAttributes.assetId ?? ''),
      class: 'chat-composer-image',
    }]
  },
  addNodeView() {
    return VueNodeViewRenderer(ChatImageAttachmentView)
  }
})
