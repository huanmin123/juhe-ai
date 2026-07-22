export type ChatImageOutputFormat = 'webp' | 'png' | 'jpeg'
export type ChatImageQuality = 'auto' | 'low' | 'medium' | 'high'

export function isChatImageGenerationAccount(account: { type?: string }): boolean {
  return account.type === 'api_key'
}

export interface ChatImageSize {
  width: number
  height: number
  size: string
  sizeAdjusted: boolean
}

export interface ChatImageOptimizationPolicy {
  mimeType: 'image/webp'
  maxEdge: number
  quality: number
  maxBytes: number
}

export const chatImageInputPolicy: ChatImageOptimizationPolicy = {
  mimeType: 'image/webp',
  maxEdge: 1024,
  quality: 82,
  maxBytes: 1024 * 1024
}

export const chatImagePreviewPolicy: ChatImageOptimizationPolicy = {
  mimeType: 'image/webp',
  maxEdge: 640,
  quality: 78,
  maxBytes: 512 * 1024
}

const regularMaxEdge = 1536
const regularMaxPixels = 1_572_864
const explicitMaxEdge = 4096
const explicitMaxPixels = 16_777_216
const maxAspectRatio = 3

export function normalizeChatImageOutputFormat(value: unknown): ChatImageOutputFormat {
  if (value === undefined || value === null || String(value).trim() === '') return 'webp'
  const normalized = String(value).trim().toLowerCase()
  if (normalized === 'webp') return 'webp'
  if (normalized === 'png') return 'png'
  if (normalized === 'jpeg' || normalized === 'jpg') return 'jpeg'
  throw new Error('图片输出格式只支持 WebP、PNG 或 JPEG')
}

export function normalizeChatImageQuality(value: unknown): ChatImageQuality {
  if (value === undefined || value === null || String(value).trim() === '') return 'auto'
  const normalized = String(value).trim().toLowerCase()
  if (normalized === 'auto' || normalized === 'low' || normalized === 'medium' || normalized === 'high') return normalized
  throw new Error('图片质量只支持 auto、low、medium 或 high')
}

export function normalizeChatImageSize(value: unknown, options: { allowLarge: boolean }): ChatImageSize {
  if (value === undefined || value === null || String(value).trim() === '') {
    return { width: 1024, height: 1024, size: '1024x1024', sizeAdjusted: false }
  }
  const match = String(value).trim().match(/^(\d{2,5})\s*x\s*(\d{2,5})$/iu)
  if (!match) return { width: 1024, height: 1024, size: '1024x1024', sizeAdjusted: true }
  const requestedWidth = Number(match[1])
  const requestedHeight = Number(match[2])
  if (!Number.isSafeInteger(requestedWidth) || !Number.isSafeInteger(requestedHeight) || requestedWidth <= 0 || requestedHeight <= 0) {
    return { width: 1024, height: 1024, size: '1024x1024', sizeAdjusted: true }
  }
  const maxEdge = options.allowLarge ? explicitMaxEdge : regularMaxEdge
  const maxPixels = options.allowLarge ? explicitMaxPixels : regularMaxPixels
  const scale = Math.min(1, maxEdge / Math.max(requestedWidth, requestedHeight), Math.sqrt(maxPixels / (requestedWidth * requestedHeight)))
  let width = Math.max(16, Math.floor(requestedWidth * scale / 16) * 16)
  let height = Math.max(16, Math.floor(requestedHeight * scale / 16) * 16)
  if (Math.max(width, height) / Math.min(width, height) > maxAspectRatio) {
    if (width < height) width = Math.ceil(height / maxAspectRatio / 16) * 16
    else height = Math.ceil(width / maxAspectRatio / 16) * 16
  }
  if (width * height > maxPixels || Math.max(width, height) > maxEdge) {
    const finalScale = Math.min(maxEdge / Math.max(width, height), Math.sqrt(maxPixels / (width * height)))
    width = Math.max(16, Math.floor(width * finalScale / 16) * 16)
    height = Math.max(16, Math.floor(height * finalScale / 16) * 16)
  }
  const size = `${width}x${height}`
  const requestedSize = `${requestedWidth}x${requestedHeight}`
  return { width, height, size, sizeAdjusted: size !== requestedSize }
}
