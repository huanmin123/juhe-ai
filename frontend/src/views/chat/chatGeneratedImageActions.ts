import { chatAssetContentUrl } from '@/api/domains/chat'

export async function downloadGeneratedImage(conversationId: string, assetId: string, mimeType?: string): Promise<void> {
  const blob = await fetchGeneratedImageOriginal(conversationId, assetId)
  const objectUrl = URL.createObjectURL(blob)
  try {
    const anchor = document.createElement('a')
    anchor.href = objectUrl
    anchor.download = generatedImageFilename(assetId, mimeType ?? blob.type)
    anchor.rel = 'noopener'
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
  } finally {
    URL.revokeObjectURL(objectUrl)
  }
}

export async function copyGeneratedImageToClipboard(conversationId: string, assetId: string): Promise<void> {
  const clipboard = navigator.clipboard
  if (!clipboard?.write || typeof ClipboardItem === 'undefined') throw new Error('image clipboard unavailable')
  // 先在点击事件仍有效时调用 write；ClipboardItem 会等待原图读取与 PNG 转码完成。
  const png = fetchGeneratedImageOriginal(conversationId, assetId).then(convertImageToPng)
  await clipboard.write([new ClipboardItem({ 'image/png': png })])
}

async function fetchGeneratedImageOriginal(conversationId: string, assetId: string): Promise<Blob> {
  const response = await fetch(chatAssetContentUrl(conversationId, assetId, 'original'), {
    credentials: 'same-origin',
    headers: { Accept: 'image/*,*/*' }
  })
  if (!response.ok) throw new Error(`image request failed: ${response.status}`)
  const blob = await response.blob()
  if (!blob.type.startsWith('image/')) throw new Error('unexpected image content type')
  return blob
}

async function convertImageToPng(blob: Blob): Promise<Blob> {
  if (blob.type === 'image/png') return blob
  const bitmap = await createImageBitmap(blob)
  try {
    const canvas = document.createElement('canvas')
    canvas.width = bitmap.width
    canvas.height = bitmap.height
    const context = canvas.getContext('2d')
    if (!context) throw new Error('canvas unavailable')
    context.drawImage(bitmap, 0, 0)
    const png = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/png'))
    if (!png) throw new Error('image conversion failed')
    return png
  } finally {
    bitmap.close()
  }
}

function generatedImageFilename(assetId: string, mimeType: string): string {
  const extension = ({ 'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp' } as Record<string, string>)[mimeType.toLowerCase()] ?? 'png'
  return `generated-${assetId}.${extension}`
}
