import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import sharp from 'sharp'

import { chatImageJpegQuality, chatImageMaxEdge, processChatImageFile } from '../../modules/chat/chat-image-processing.js'

assert.equal(chatImageMaxEdge, 1_024)
assert.equal(chatImageJpegQuality, 85)

const root = await mkdtemp(join(tmpdir(), 'juhe-ai-chat-image-policy-'))
try {
  const transparentPath = join(root, 'transparent.png')
  const transparent = await sharp({
    create: { width: 2_400, height: 1_200, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } }
  }).composite([{ input: Buffer.from('<svg width="1200" height="1200"><rect width="1200" height="1200" fill="#ef4444"/></svg>'), left: 0, top: 0 }]).png().toBuffer()
  await writeFile(transparentPath, transparent)

  const processedTransparent = await processChatImageFile(transparentPath)
  assert.equal(processedTransparent.mimeType, 'image/jpeg', '透明 PNG 也必须统一转换为 JPEG')
  assert.equal(Math.max(processedTransparent.width, processedTransparent.height), 1_024, '处理后最长边必须收敛到 1024')
  const transparentMetadata = await sharp(processedTransparent.buffer).metadata()
  assert.equal(transparentMetadata.format, 'jpeg')
  assert.equal(transparentMetadata.hasAlpha, false)
  const corner = await sharp(processedTransparent.buffer).extract({
    left: processedTransparent.width - 2,
    top: 1,
    width: 1,
    height: 1
  }).raw().toBuffer()
  assert(corner[0]! >= 245 && corner[1]! >= 245 && corner[2]! >= 245, '透明区域必须铺成白色背景，不能变黑')

  const rotatedPath = join(root, 'rotated.jpg')
  await sharp({
    create: { width: 800, height: 1_600, channels: 3, background: { r: 30, g: 120, b: 210 } }
  }).jpeg({ quality: 95 }).withMetadata({ orientation: 6 }).toFile(rotatedPath)
  const processedRotated = await processChatImageFile(rotatedPath)
  assert.equal(processedRotated.mimeType, 'image/jpeg')
  assert.equal(Math.max(processedRotated.width, processedRotated.height), 1_024, 'EXIF 旋转后的视觉方向仍必须受 1024 最长边限制')
  assert.equal(processedRotated.width, 1_024)
  assert.equal(processedRotated.height, 512)

  const smallWebpPath = join(root, 'small.webp')
  const smallWebp = await sharp({ create: { width: 320, height: 240, channels: 4, background: { r: 10, g: 20, b: 30, alpha: 0.4 } } }).webp().toBuffer()
  await writeFile(smallWebpPath, smallWebp)
  const processedSmall = await processChatImageFile(smallWebpPath)
  assert.equal(processedSmall.mimeType, 'image/jpeg', 'WebP 输入也必须统一为 JPEG')
  assert.equal(processedSmall.width, 320, '小图不得放大')
  assert.equal(processedSmall.height, 240, '小图不得放大')
  console.log('AI 问答图片统一 JPEG 85、白底和 1024 最长边回归通过', {
    transparent: `${processedTransparent.width}x${processedTransparent.height}/${processedTransparent.byteSize}B`,
    exifRotated: `${processedRotated.width}x${processedRotated.height}/${processedRotated.byteSize}B`,
    smallWebp: `${processedSmall.width}x${processedSmall.height}/${processedSmall.byteSize}B`
  })
} finally {
  await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
}
