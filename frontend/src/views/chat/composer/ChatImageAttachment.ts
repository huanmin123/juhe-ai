import { Node, mergeAttributes } from '@tiptap/core'

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
    return ['img', mergeAttributes(HTMLAttributes, { 'data-chat-image': '', class: 'chat-composer-image' })]
  }
})
