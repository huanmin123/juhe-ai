import { createHash } from 'node:crypto'

import type { DatabaseClient } from '../../storage/database-client.js'
import { listReadyChatAssetsByIds, type ChatAssetRecord } from '../../storage/chat-assets.repository.js'
import { openChatAssetObject } from '../../storage/chat-asset-storage.js'
import { estimateChatImageTokens } from './chat-token-count.js'

const maxChatImagesPerTurn = 4
const maxChatModelImageBytesPerTurn = 8 * 1024 * 1024

export interface ChatAssetReferenceBlock {
  type: 'input_text' | 'input_image'
  text?: string
  assetId?: string
}

export interface ChatResolvedInputBlock {
  type: 'input_text' | 'input_image'
  text?: string
  dataUrl?: string
}

export class ChatAssetInputError extends Error {
  readonly code = 'chat_asset_unavailable'

  constructor(message: string) {
    super(message)
    this.name = 'ChatAssetInputError'
  }
}

export async function resolveChatAssetInput(input: {
  client: DatabaseClient
  blocks: readonly ChatAssetReferenceBlock[] | undefined
  systemAccountId: string
  conversationId: string
  now: string
}): Promise<{
  blocks: ChatResolvedInputBlock[] | undefined
  assetIds: string[]
  imageCount: number
  imageTokenEstimate: number
  processedBytes: number
}> {
  if (!input.blocks?.length) return { blocks: undefined, assetIds: [], imageCount: 0, imageTokenEstimate: 0, processedBytes: 0 }
  const assetIds = input.blocks
    .filter((block) => block.type === 'input_image')
    .map((block) => block.assetId?.trim() ?? '')
  if (assetIds.some((assetId) => !assetId)) throw new ChatAssetInputError('图片资产 ID 不能为空')
  if (assetIds.length > maxChatImagesPerTurn) throw new ChatAssetInputError(`每条消息最多包含 ${maxChatImagesPerTurn} 张图片`)
  if (new Set(assetIds).size !== assetIds.length) throw new ChatAssetInputError('同一张图片不能在一条消息中重复引用')
  const assets = await listReadyChatAssetsByIds(input.client, {
    assetIds,
    systemAccountId: input.systemAccountId,
    conversationId: input.conversationId,
    now: input.now
  })
  if (assets.length !== assetIds.length) {
    throw new ChatAssetInputError('图片不存在、尚未处理完成或已过期，请重新上传')
  }
  const assetsById = new Map(assets.map((asset) => [asset.id, asset]))
  let processedBytes = 0
  const dataUrls = new Map<string, string>()
  for (const assetId of assetIds) {
    const asset = assetsById.get(assetId)
    if (!asset) throw new ChatAssetInputError('图片资产读取失败，请重新上传')
    const buffer = await readVerifiedChatAsset(asset)
    processedBytes += buffer.byteLength
    if (processedBytes > maxChatModelImageBytesPerTurn) {
      throw new ChatAssetInputError('本轮图片处理后总大小超过 8 MiB，请减少图片数量')
    }
    dataUrls.set(assetId, `data:${asset.processedMimeType};base64,${buffer.toString('base64')}`)
  }
  return {
    blocks: input.blocks.map((block) => block.type === 'input_image'
      ? { type: 'input_image', dataUrl: dataUrls.get(block.assetId?.trim() ?? '') }
      : { type: 'input_text', text: block.text ?? '' }),
    assetIds,
    imageCount: assetIds.length,
    imageTokenEstimate: assets.reduce((total, asset) => total + estimateChatImageTokens(asset.processedWidth, asset.processedHeight), 0),
    processedBytes
  }
}

async function readVerifiedChatAsset(asset: ChatAssetRecord): Promise<Buffer> {
  if (!asset.storageKey || !asset.processedMimeType || !asset.processedBytes || !asset.processedSha256) {
    throw new ChatAssetInputError('图片处理结果不完整，请重新上传')
  }
  const object = await openChatAssetObject(asset.storageKey)
  if (object.bytes !== asset.processedBytes) throw new ChatAssetInputError('图片文件大小校验失败，请重新上传')
  const chunks: Buffer[] = []
  let bytes = 0
  for await (const chunk of object.stream) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    bytes += buffer.byteLength
    if (bytes > asset.processedBytes) {
      object.stream.destroy()
      throw new ChatAssetInputError('图片文件超过已记录大小，请重新上传')
    }
    chunks.push(buffer)
  }
  const result = Buffer.concat(chunks, bytes)
  if (createHash('sha256').update(result).digest('hex') !== asset.processedSha256) {
    throw new ChatAssetInputError('图片文件完整性校验失败，请重新上传')
  }
  return result
}
