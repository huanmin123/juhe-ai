import assert from 'node:assert/strict'
import { composerDocumentToBlocks } from '../../views/chat/composer/chatComposerDocument'

const blocks = composerDocumentToBlocks({ type: 'doc', content: [
  { type: 'paragraph', content: [{ type: 'text', text: '图片前' }, { type: 'chatImageAttachment', attrs: { assetId: 'asset-inline', previewUrl: 'blob:inline' } }, { type: 'text', text: '图片后' }] },
  { type: 'bulletList', content: [{ type: 'listItem', content: [{ type: 'paragraph', content: [{ type: 'text', text: '列表项' }] }] }] },
  { type: 'codeBlock', attrs: { language: 'ts' }, content: [{ type: 'text', text: 'const answer = 42' }] },
  { type: 'chatImageAttachment', attrs: { assetId: 'asset-1', previewUrl: 'blob:test' } }
] })

assert.equal(blocks[0]?.type, 'input_text')
assert.equal((blocks[0] as { text: string }).text, '图片前')
assert.deepEqual(blocks[1], { type: 'input_image', assetId: 'asset-inline', previewUrl: 'blob:inline' })
assert.equal((blocks[2] as { text: string }).text, '图片后')
assert.match((blocks[3] as { text: string }).text, /列表项/)
assert.match((blocks[4] as { text: string }).text, /```ts\nconst answer = 42\n```/)
assert.deepEqual(blocks[5], { type: 'input_image', assetId: 'asset-1', previewUrl: 'blob:test' })
console.log('chat composer document regression passed')
