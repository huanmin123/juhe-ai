import { Node } from '@tiptap/core'

export const ChatImageAttachment = Node.create({
  name: 'chatImageAttachment',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,
  draggable: true,
  addAttributes() {
    return {
      assetId: { default: '' },
      previewUrl: { default: '' },
      fileName: { default: '图片' },
      dataUrl: { default: '' }
    }
  },
  parseHTML() { return [{ tag: 'img[data-chat-image]' }] },
  renderHTML({ HTMLAttributes }) {
    return ['img', {
      'data-chat-image': '',
      'data-asset-id': String(HTMLAttributes.assetId ?? ''),
      class: 'chat-composer-image',
      src: String(HTMLAttributes.previewUrl ?? ''),
      alt: String(HTMLAttributes.fileName ?? '图片')
    }]
  }
})
