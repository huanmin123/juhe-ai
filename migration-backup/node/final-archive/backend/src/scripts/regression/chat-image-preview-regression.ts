import assert from 'node:assert/strict'
import { mkdtemp, stat, unlink } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import sharp from 'sharp'

import { createChatImagePreview } from '../../modules/chat/chat-image-preview.js'

const directory = await mkdtemp(join(tmpdir(), 'juhe-chat-preview-'))
const sourcePath = join(directory, 'source.png')
const previewPath = join(directory, 'preview.webp')
try {
  await sharp({
    create: { width: 1800, height: 1200, channels: 3, background: { r: 24, g: 110, b: 180 } }
  }).png().toFile(sourcePath)
  const preview = await createChatImagePreview({ sourcePath, destinationPath: previewPath })
  assert.equal(preview.mimeType, 'image/webp')
  assert(preview.width <= 640)
  assert(preview.height <= 640)
  assert(preview.bytes <= 512 * 1024)
  assert.equal((await stat(previewPath)).size, preview.bytes)
  const metadata = await sharp(previewPath).metadata()
  assert.equal(metadata.format, 'webp')
  await assert.rejects(createChatImagePreview({ sourcePath, destinationPath: previewPath, maxBytes: 20 }), /预览图|上限/)
} finally {
  await unlink(sourcePath).catch(() => undefined)
  await unlink(previewPath).catch(() => undefined)
}

console.log('AI 问答图片 preview 回归通过')
