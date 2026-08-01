import Compressor from 'compressorjs'

export interface ChatImageUploadPolicy {
  mimeType: 'image/webp'
  maxEdge: number
  quality: number
  maxBytes: number
}

export interface ChatDecodedImage {
  width: number
  height: number
  source?: CanvasImageSource
  close?: () => void
}

export interface ChatImageProcessingRuntime {
  decode: (file: File) => Promise<ChatDecodedImage>
  encodeWebp: (
    image: ChatDecodedImage,
    size: { width: number; height: number },
    quality: number
  ) => Promise<Blob>
}

export function resolveChatImageUploadSize(width: number, height: number, maxEdge: number): { width: number; height: number } {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    throw new Error('图片尺寸无效')
  }
  if (!Number.isFinite(maxEdge) || maxEdge <= 0) throw new Error('图片策略最大边无效')
  const scale = Math.min(1, maxEdge / Math.max(width, height))
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale))
  }
}

export async function prepareChatImageForUpload(
  file: File,
  policy: ChatImageUploadPolicy,
  runtime?: ChatImageProcessingRuntime
): Promise<File> {
  const quality = normalizedQuality(policy.quality)
  let blob: Blob
  if (runtime) {
    const decoded = await runtime.decode(file)
    try {
      blob = await runtime.encodeWebp(decoded, resolveChatImageUploadSize(decoded.width, decoded.height, policy.maxEdge), quality)
    } finally {
      decoded.close?.()
    }
  } else {
    blob = await compressWithCompressorJs(file, policy, quality)
  }
  if (blob.type !== policy.mimeType || blob.size <= 0) throw new Error('图片压缩失败，当前浏览器可能不支持 WebP 编码')
  if (blob.size > policy.maxBytes) {
    throw new Error(`图片压缩后仍超过 ${formatBytes(policy.maxBytes)}，请裁剪图片后重试`)
  }
  return new File([blob], webpFilename(file.name), {
    type: policy.mimeType,
    lastModified: file.lastModified
  })
}

function compressWithCompressorJs(file: File, policy: ChatImageUploadPolicy, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    new Compressor(file, {
      strict: false,
      checkOrientation: true,
      retainExif: false,
      maxWidth: policy.maxEdge,
      maxHeight: policy.maxEdge,
      mimeType: policy.mimeType,
      quality,
      convertSize: 0,
      success: resolve,
      error: reject
    })
  })
}

function webpFilename(filename: string): string {
  const base = filename.trim().replace(/\.[^.]+$/, '') || '图片'
  return `${base}.webp`
}

function normalizedQuality(value: number): number {
  if (!Number.isFinite(value) || value <= 0 || value > 100) throw new Error('图片策略质量无效')
  return value / 100
}

function formatBytes(value: number): string {
  if (value % (1024 * 1024) === 0) return `${value / (1024 * 1024)} MiB`
  return `${Math.ceil(value / 1024)} KiB`
}
