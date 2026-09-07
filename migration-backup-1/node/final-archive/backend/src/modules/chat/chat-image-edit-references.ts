import type { Readable } from 'node:stream'

import { chatAssetGeneratedMaxBytes, openChatGeneratedAssetObject } from '../../storage/chat-asset-storage.js'
import { listReadyChatAssetsByIds, type ChatAssetProcessedMimeType } from '../../storage/chat-assets.repository.js'
import type { DatabaseClient } from '../../storage/database-client.js'

export interface ChatImageEditReference {
  assetId: string
  stream: Readable
  bytes: number
  mimeType: ChatAssetProcessedMimeType
  filename: string
}

export const chatImageEditMaxReferenceImages = 5
export const chatImageEditMaxReferenceBytes = 48 * 1024 * 1024

export function validateChatImageEditReferenceLimits(references: readonly { bytes: number }[]): void {
  if (references.length === 0) throw new Error('编辑图片必须至少引用一张图片')
  if (references.length > chatImageEditMaxReferenceImages) throw new Error(`编辑图片最多引用 ${chatImageEditMaxReferenceImages} 张图片`)
  let totalBytes = 0
  for (const reference of references) {
    if (!Number.isSafeInteger(reference.bytes) || reference.bytes <= 0 || reference.bytes > chatAssetGeneratedMaxBytes) {
      throw new Error('引用图片字节数无效')
    }
    totalBytes += reference.bytes
    if (totalBytes > chatImageEditMaxReferenceBytes) throw new Error('编辑图片引用总大小不能超过 48 MiB')
  }
}

export async function loadChatImageEditReferences(client: DatabaseClient, input: {
  assetIds: readonly string[]
  systemAccountId: string
  conversationId: string
  now: string
}): Promise<ChatImageEditReference[]> {
  if (input.assetIds.length === 0) throw new Error('编辑图片必须至少引用一张图片')
  if (input.assetIds.length > chatImageEditMaxReferenceImages) throw new Error(`编辑图片最多引用 ${chatImageEditMaxReferenceImages} 张图片`)
  const assets = await listReadyChatAssetsByIds(client, input)
  if (assets.length !== input.assetIds.length) throw new Error('引用图片不存在、已过期或不属于当前会话')
  const metadata = assets.map((asset) => {
    if (!asset.storageKey || !asset.processedMimeType || !asset.processedBytes) throw new Error('引用图片没有可读取的处理结果')
    return {
      assetId: asset.id,
      storageKey: asset.storageKey,
      bytes: asset.processedBytes,
      mimeType: asset.processedMimeType,
      filename: `${asset.id}.${extensionForMimeType(asset.processedMimeType)}`
    }
  })
  validateChatImageEditReferenceLimits(metadata)
  const opened: ChatImageEditReference[] = []
  try {
    for (const item of metadata) {
      const object = await openChatGeneratedAssetObject(item.storageKey, chatAssetGeneratedMaxBytes)
      if (object.bytes !== item.bytes) throw new Error('引用图片存储字节与元数据不一致')
      opened.push({
        assetId: item.assetId,
        stream: object.stream,
        bytes: object.bytes,
        mimeType: item.mimeType,
        filename: item.filename
      })
    }
    return opened
  } catch (error) {
    for (const reference of opened) reference.stream.destroy()
    throw error
  }
}

function extensionForMimeType(mimeType: ChatAssetProcessedMimeType): string {
  if (mimeType === 'image/jpeg') return 'jpg'
  if (mimeType === 'image/png') return 'png'
  return 'webp'
}
