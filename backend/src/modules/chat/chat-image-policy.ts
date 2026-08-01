export type ChatImageOutputFormat = 'webp' | 'png' | 'jpeg'
export type ChatImageQuality = 'auto' | 'low' | 'medium' | 'high'

export function isChatImageGenerationAccount(account: { type?: string }): boolean {
  return account.type === 'api_key'
}

export interface ChatImageSize {
  width?: number
  height?: number
  size: string
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
  maxBytes: 3 * 1024 * 1024
}

export const chatImagePreviewPolicy: ChatImageOptimizationPolicy = {
  mimeType: 'image/webp',
  maxEdge: 640,
  quality: 78,
  maxBytes: 512 * 1024
}

const imageMinPixels = 655_360
const imageMaxPixels = 8_294_400
const imageMaxEdge = 3840
const imageMaxAspectRatio = 3

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

export function normalizeChatImageSize(value: unknown): ChatImageSize {
  if (value === undefined || value === null || String(value).trim() === '') {
    return { size: 'auto' }
  }
  const normalized = String(value).trim().toLowerCase()
  if (normalized === 'auto') return { size: 'auto' }
  const match = normalized.match(/^(\d{2,4})x(\d{2,4})$/u)
  if (!match) throw new Error('图片尺寸必须是 auto 或 WIDTHxHEIGHT 格式')
  const width = Number(match[1])
  const height = Number(match[2])
  if (width % 16 !== 0 || height % 16 !== 0) throw new Error('图片宽高必须是 16px 的倍数')
  if (Math.max(width, height) > imageMaxEdge) throw new Error(`图片最长边不能超过 ${imageMaxEdge}px`)
  if (Math.max(width, height) / Math.min(width, height) > imageMaxAspectRatio) throw new Error('图片长短边比例不能超过 3:1')
  const pixels = width * height
  if (pixels < imageMinPixels || pixels > imageMaxPixels) {
    throw new Error(`图片总像素必须在 ${imageMinPixels.toLocaleString('en-US')} 到 ${imageMaxPixels.toLocaleString('en-US')} 之间`)
  }
  return { width, height, size: `${width}x${height}` }
}
