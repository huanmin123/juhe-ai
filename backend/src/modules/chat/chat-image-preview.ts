import { createHash } from 'node:crypto'
import { mkdir, rename, stat, unlink, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import sharp from 'sharp'

import { chatImagePreviewPolicy } from './chat-image-policy.js'

sharp.cache({ files: 0 })

export interface ChatImagePreviewResult {
  mimeType: 'image/webp'
  width: number
  height: number
  bytes: number
  sha256: string
}

export async function createChatImagePreview(input: {
  sourcePath: string
  destinationPath: string
  maxBytes?: number
}): Promise<ChatImagePreviewResult> {
  const maxBytes = input.maxBytes ?? chatImagePreviewPolicy.maxBytes
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new Error('预览图字节上限无效')
  await mkdir(dirname(input.destinationPath), { recursive: true })
  let lastBytes = 0
  for (const attempt of [
    { maxEdge: chatImagePreviewPolicy.maxEdge, quality: chatImagePreviewPolicy.quality },
    { maxEdge: 560, quality: 68 },
    { maxEdge: 480, quality: 58 },
    { maxEdge: 384, quality: 48 },
    { maxEdge: 256, quality: 38 }
  ]) {
    const result = await sharp(input.sourcePath)
      .rotate()
      .resize({ width: attempt.maxEdge, height: attempt.maxEdge, fit: 'inside', withoutEnlargement: true })
      .webp({ quality: attempt.quality })
      .toBuffer({ resolveWithObject: true })
    lastBytes = result.data.byteLength
    if (lastBytes > maxBytes) continue
    const temporaryPath = `${input.destinationPath}.tmp-${process.pid}-${Date.now()}`
    try {
      await writeFile(temporaryPath, result.data, { flag: 'wx' })
      await rename(temporaryPath, input.destinationPath)
    } catch (error) {
      await unlink(temporaryPath).catch(() => undefined)
      throw error
    }
    const metadata = await stat(input.destinationPath)
    return {
      mimeType: 'image/webp',
      width: result.info.width,
      height: result.info.height,
      bytes: metadata.size,
      sha256: createHash('sha256').update(result.data).digest('hex')
    }
  }
  throw new Error(`预览图超过 ${maxBytes} 字节上限（最小尝试 ${lastBytes} 字节）`)
}
