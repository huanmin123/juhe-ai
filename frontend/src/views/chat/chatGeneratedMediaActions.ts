import { chatAssetContentUrl } from '@/api/domains/chat'

/**
 * 问答音视频工具产物下载（问答音视频工具设计 §5）：沿 chatGeneratedImageActions
 * 的取流 + objectURL + anchor 模式；video/audio 资产只有 original 变体。
 */
export async function downloadGeneratedMedia(conversationId: string, assetId: string, kind: 'video' | 'audio', mimeType?: string): Promise<void> {
  const blob = await fetchGeneratedMediaOriginal(conversationId, assetId, kind)
  const objectUrl = URL.createObjectURL(blob)
  try {
    const anchor = document.createElement('a')
    anchor.href = objectUrl
    anchor.download = generatedMediaFilename(assetId, mimeType ?? blob.type, kind)
    anchor.rel = 'noopener'
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
  } finally {
    URL.revokeObjectURL(objectUrl)
  }
}

async function fetchGeneratedMediaOriginal(conversationId: string, assetId: string, kind: 'video' | 'audio'): Promise<Blob> {
  const response = await fetch(chatAssetContentUrl(conversationId, assetId, 'original'), {
    credentials: 'same-origin',
    headers: { Accept: `${kind}/*,*/*` }
  })
  if (!response.ok) throw new Error(`${kind} request failed: ${response.status}`)
  const blob = await response.blob()
  if (!blob.type.startsWith(`${kind}/`)) throw new Error(`unexpected ${kind} content type`)
  return blob
}

const mediaExtensions: Record<'video' | 'audio', Record<string, string>> = {
  video: { 'video/mp4': 'mp4', 'video/webm': 'webm', 'video/quicktime': 'mov' },
  audio: { 'audio/mpeg': 'mp3', 'audio/mp3': 'mp3', 'audio/wav': 'wav', 'audio/x-wav': 'wav', 'audio/webm': 'webm', 'audio/ogg': 'ogg' }
}

function generatedMediaFilename(assetId: string, mimeType: string, kind: 'video' | 'audio'): string {
  const extension = mediaExtensions[kind][mimeType.toLowerCase()] ?? (mimeType.split('/')[1] || (kind === 'video' ? 'mp4' : 'mp3'))
  return `generated-${assetId}.${extension}`
}
