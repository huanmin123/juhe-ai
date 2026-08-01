import { createHash } from 'node:crypto'
import { readFile, stat } from 'node:fs/promises'

import pLimit from 'p-limit'
import sharp, { type Metadata } from 'sharp'
import { chatImageInputPolicy } from './chat-image-policy.js'

// libvips 在 Windows 会缓存最近读取的文件句柄，导致上传临时目录无法及时清理。
sharp.cache({ files: 0 })

const maxDecodedPixels = 40_000_000
const maxModelImageBytes = chatImageInputPolicy.maxBytes
export const chatImageMaxEdge = chatImageInputPolicy.maxEdge
export const chatImageWebpQuality = chatImageInputPolicy.quality
const maxModelImagePatches = 2_500
const patchEdge = 32
const imageProcessingLimit = pLimit(2)

export type ChatProcessedImageMimeType = 'image/webp'

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
    const output = await reuseCompliantWebp(filePath, metadata, dimensions, target)
      ?? await encodeModelImage({ filePath, width: target.width, height: target.height })
    if (output.buffer.byteLength > maxModelImageBytes) throw new ChatImageProcessingError('图片按 WebP 82 处理后仍超过 3 MiB，请裁剪图片后重试')
    return {
      buffer: output.buffer,
      originalMimeType: originalMimeType(metadata.format),
      originalWidth: dimensions.width,
      originalHeight: dimensions.height,
      mimeType: 'image/webp',
      width: output.width,
      height: output.height,
      byteSize: output.buffer.byteLength,
      sha256: createHash('sha256').update(output.buffer).digest('hex')
    }
  })
}

async function encodeModelImage(input: {
  filePath: string
  width: number
  height: number
}): Promise<{ buffer: Buffer; width: number; height: number }> {
  const pipeline = sharp(input.filePath, {
    failOn: 'error',
    limitInputPixels: maxDecodedPixels,
    sequentialRead: true
  }).rotate().resize(input.width, input.height, {
    fit: 'inside',
    withoutEnlargement: true,
    fastShrinkOnLoad: true
  }).webp({ quality: chatImageWebpQuality, smartSubsample: true })
  const { data, info } = await pipeline.toBuffer({ resolveWithObject: true })
  return { buffer: data, width: info.width, height: info.height }
}

async function reuseCompliantWebp(
  filePath: string,
  metadata: Metadata,
  dimensions: { width: number; height: number },
  target: { width: number; height: number }
): Promise<{ buffer: Buffer; width: number; height: number } | undefined> {
  if (metadata.format !== 'webp' || (metadata.orientation !== undefined && metadata.orientation !== 1)) return undefined
  if (dimensions.width !== target.width || dimensions.height !== target.height) return undefined
  const file = await stat(filePath)
  if (!file.isFile() || file.size <= 0 || file.size > maxModelImageBytes) return undefined
  const buffer = await readFile(filePath)
  return { buffer, width: dimensions.width, height: dimensions.height }
}

function boundedImageDimensions(width: number, height: number): { width: number; height: number } {
  let scale = Math.min(1, chatImageMaxEdge / Math.max(width, height))
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
