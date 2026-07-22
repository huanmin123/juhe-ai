import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import sharp from 'sharp'

import { chatImageMaxEdge, chatImageWebpQuality, processChatImageFile } from '../../modules/chat/chat-image-processing.js'
import { chatAssetOriginalMaxBytes, chatAssetProcessedMaxBytes } from '../../storage/chat-asset-storage.js'

assert.equal(chatImageMaxEdge, 1_024)
assert.equal(chatImageWebpQuality, 82)
assert.equal(chatAssetOriginalMaxBytes, 1024 * 1024, '后端必须在读取请求体时把单张上传硬限制为 1 MiB')
assert.equal(chatAssetProcessedMaxBytes, 1024 * 1024, '后端处理后的图片也不得超过 1 MiB')

const root = await mkdtemp(join(tmpdir(), 'juhe-ai-chat-image-policy-'))
try {
  const transparentPath = join(root, 'transparent.png')
  const transparent = await sharp({
    create: { width: 2_400, height: 1_200, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } }
  }).composite([{ input: Buffer.from('<svg width="1200" height="1200"><rect width="1200" height="1200" fill="#ef4444"/></svg>'), left: 0, top: 0 }]).png().toBuffer()
  await writeFile(transparentPath, transparent)

  const processedTransparent = await processChatImageFile(transparentPath)
  assert.equal(processedTransparent.mimeType, 'image/webp', '透明 PNG 也必须统一转换为 WebP')
  assert.equal(Math.max(processedTransparent.width, processedTransparent.height), 1_024, '处理后最长边必须收敛到 1024')
  const transparentMetadata = await sharp(processedTransparent.buffer).metadata()
  assert.equal(transparentMetadata.format, 'webp')
  assert.equal(transparentMetadata.hasAlpha, true)
  const corner = await sharp(processedTransparent.buffer).extract({
    left: processedTransparent.width - 2,
    top: 1,
    width: 1,
    height: 1
  }).raw().toBuffer()
  assert(corner[3] === 0, '透明区域必须保留透明度，不能强制铺白')

  const rotatedPath = join(root, 'rotated.jpg')
  await sharp({
    create: { width: 800, height: 1_600, channels: 3, background: { r: 30, g: 120, b: 210 } }
  }).jpeg({ quality: 95 }).withMetadata({ orientation: 6 }).toFile(rotatedPath)
  const processedRotated = await processChatImageFile(rotatedPath)
  assert.equal(processedRotated.mimeType, 'image/webp')
  assert.equal(Math.max(processedRotated.width, processedRotated.height), 1_024, 'EXIF 旋转后的视觉方向仍必须受 1024 最长边限制')
  assert.equal(processedRotated.width, 1_024)
  assert.equal(processedRotated.height, 512)

  const smallWebpPath = join(root, 'small.webp')
  const smallWebp = await sharp({ create: { width: 320, height: 240, channels: 4, background: { r: 10, g: 20, b: 30, alpha: 0.4 } } }).webp().toBuffer()
  await writeFile(smallWebpPath, smallWebp)
  const processedSmall = await processChatImageFile(smallWebpPath)
  assert.equal(processedSmall.mimeType, 'image/webp', '合规 WebP 输入必须直接复用，不再二次有损编码')
  assert.equal(processedSmall.width, 320, '小图不得放大')
  assert.equal(processedSmall.height, 240, '小图不得放大')
  assert.equal(processedSmall.byteSize, smallWebp.byteLength, '合规 WebP 应保留原始字节，不发生二次编码')
  console.log('AI 问答图片统一 WebP 82、透明度和 1024 最长边回归通过', {
    transparent: `${processedTransparent.width}x${processedTransparent.height}/${processedTransparent.byteSize}B`,
    exifRotated: `${processedRotated.width}x${processedRotated.height}/${processedRotated.byteSize}B`,
    smallWebp: `${processedSmall.width}x${processedSmall.height}/${processedSmall.byteSize}B`
  })
} finally {
  await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
}
