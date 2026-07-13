import { createHash } from 'node:crypto'

import pLimit from 'p-limit'
import sharp, { type Metadata } from 'sharp'

const maxDecodedPixels = 40_000_000
const maxModelImageBytes = 2 * 1024 * 1024
const maxModelImageEdge = 2_048
const maxModelImagePatches = 2_500
const patchEdge = 32
const imageProcessingLimit = pLimit(2)

export type ChatProcessedImageMimeType = 'image/jpeg' | 'image/webp'

export interface ChatProcessedImage {
  buffer: Buffer
  originalMimeType: 'image/jpeg' | 'image/png' | 'image/webp' | 'image/gif'
  originalWidth: number
  originalHeight: number
  mimeType: ChatProcessedImageMimeType
  width: number
  height: number
  byteSize: number
  sha256: string
}

export class ChatImageProcessingError extends Error {
  readonly code = 'chat_image_invalid'

  constructor(message: string) {
    super(message)
    this.name = 'ChatImageProcessingError'
  }
}

export async function processChatImageFile(filePath: string): Promise<ChatProcessedImage> {
  return imageProcessingLimit(async () => {
    let metadata: Metadata
    try {
      metadata = await sharp(filePath, {
        failOn: 'error',
        limitInputPixels: maxDecodedPixels,
        sequentialRead: true
      }).metadata()
    } catch {
      throw new ChatImageProcessingError('图片无法解码、像素过大或文件已损坏')
    }
    if (!isSupportedInputFormat(metadata.format)) {
      throw new ChatImageProcessingError('仅支持 JPEG、PNG、WebP 或 GIF 图片')
    }
    const dimensions = orientedDimensions(metadata)
    if (!dimensions) throw new ChatImageProcessingError('无法读取图片尺寸')
    const target = boundedImageDimensions(dimensions.width, dimensions.height)
    const outputMimeType: ChatProcessedImageMimeType = metadata.hasAlpha ? 'image/webp' : 'image/jpeg'
    const attempts = [
      { quality: 85, scale: 1 },
      { quality: 75, scale: 1 },
      { quality: 68, scale: 0.9 },
      { quality: 60, scale: 0.8 },
      { quality: 55, scale: 0.7 }
    ] as const
    let last: Awaited<ReturnType<typeof encodeModelImage>> | undefined
    for (const attempt of attempts) {
      last = await encodeModelImage({
        filePath,
        width: Math.max(1, Math.floor(target.width * attempt.scale)),
        height: Math.max(1, Math.floor(target.height * attempt.scale)),
        quality: attempt.quality,
        mimeType: outputMimeType
      })
      if (last.buffer.byteLength <= maxModelImageBytes) break
    }
    if (!last || last.buffer.byteLength > maxModelImageBytes) {
      throw new ChatImageProcessingError('图片处理后仍超过 2 MiB，请裁剪图片后重试')
    }
    return {
      buffer: last.buffer,
      originalMimeType: originalMimeType(metadata.format),
      originalWidth: dimensions.width,
      originalHeight: dimensions.height,
      mimeType: outputMimeType,
      width: last.width,
      height: last.height,
      byteSize: last.buffer.byteLength,
      sha256: createHash('sha256').update(last.buffer).digest('hex')
    }
  })
}

async function encodeModelImage(input: {
  filePath: string
  width: number
  height: number
  quality: number
  mimeType: ChatProcessedImageMimeType
}): Promise<{ buffer: Buffer; width: number; height: number }> {
  let pipeline = sharp(input.filePath, {
    failOn: 'error',
    limitInputPixels: maxDecodedPixels,
    sequentialRead: true
  }).rotate().resize(input.width, input.height, {
    fit: 'inside',
    withoutEnlargement: true,
    fastShrinkOnLoad: true
  })
  pipeline = input.mimeType === 'image/webp'
    ? pipeline.webp({ quality: input.quality, effort: 4 })
    : pipeline.jpeg({ quality: input.quality, mozjpeg: true, chromaSubsampling: '4:2:0' })
  const { data, info } = await pipeline.toBuffer({ resolveWithObject: true })
  return { buffer: data, width: info.width, height: info.height }
}

function boundedImageDimensions(width: number, height: number): { width: number; height: number } {
  let scale = Math.min(1, maxModelImageEdge / Math.max(width, height))
  const areaScale = Math.sqrt((maxModelImagePatches * patchEdge * patchEdge) / (width * height))
  scale = Math.min(scale, areaScale)
  let targetWidth = Math.max(1, Math.floor(width * scale))
  let targetHeight = Math.max(1, Math.floor(height * scale))
  while (imagePatchCount(targetWidth, targetHeight) > maxModelImagePatches) {
    targetWidth = Math.max(1, Math.floor(targetWidth * 0.98))
    targetHeight = Math.max(1, Math.floor(targetHeight * 0.98))
  }
  return { width: targetWidth, height: targetHeight }
}

function imagePatchCount(width: number, height: number): number {
  return Math.ceil(width / patchEdge) * Math.ceil(height / patchEdge)
}

function orientedDimensions(metadata: Metadata): { width: number; height: number } | undefined {
  if (!metadata.width || !metadata.height) return undefined
  return metadata.orientation && metadata.orientation >= 5 && metadata.orientation <= 8
    ? { width: metadata.height, height: metadata.width }
    : { width: metadata.width, height: metadata.height }
}

function isSupportedInputFormat(format: string | undefined): boolean {
  return format === 'jpeg' || format === 'png' || format === 'webp' || format === 'gif'
}

function originalMimeType(format: string | undefined): ChatProcessedImage['originalMimeType'] {
  if (format === 'jpeg') return 'image/jpeg'
  if (format === 'png') return 'image/png'
  if (format === 'webp') return 'image/webp'
  if (format === 'gif') return 'image/gif'
  throw new ChatImageProcessingError('无法识别图片格式')
}
