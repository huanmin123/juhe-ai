import { createReadStream } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import type { DatabaseClient } from '../../../storage/database-client.js'
import { commitChatGeneratedAsset, newChatAssetId } from '../../../storage/chat-assets.repository.js'
import {
  chatAssetGeneratedMaxBytes,
  chatAssetPreviewMaxBytes,
  deleteChatAssetObjects,
  storageKeyForChatAsset,
  writeChatAssetObject,
  writeChatGeneratedAssetObject
} from '../../../storage/chat-asset-storage.js'
import { createChatImagePreview } from '../chat-image-preview.js'
import type { ChatGeneratedImageArtifactSink } from './contracts.js'

export function createChatGeneratedImageArtifactSink(input: {
  client: DatabaseClient
  systemAccountId: string
  conversationId: string
  turnId: string
  messageId: string
  retentionDays: number
  nextContentOrder: () => number
  now?: () => string
}): ChatGeneratedImageArtifactSink {
  return {
    commitGeneratedImage: async ({ result: generated, generation }) => {
      const assetId = newChatAssetId()
      const temporaryDirectory = await mkdtemp(join(tmpdir(), 'juhe-chat-generated-preview-'))
      const previewPath = join(temporaryDirectory, 'preview.webp')
      const storedKeys: string[] = []
      try {
        const preview = await createChatImagePreview({ sourcePath: generated.path, destinationPath: previewPath })
        const originalStorageKey = storageKeyForChatAsset({
          assetId,
          sha256: generated.sha256,
          mimeType: generated.mimeType,
          variant: 'original'
        })
        const previewStorageKey = storageKeyForChatAsset({
          assetId,
          sha256: preview.sha256,
          mimeType: preview.mimeType,
          variant: 'preview'
        })
        await writeChatGeneratedAssetObject({
          storageKey: originalStorageKey,
          source: createReadStream(generated.path),
          maxBytes: chatAssetGeneratedMaxBytes,
          expectedBytes: generated.bytes,
          expectedSha256: generated.sha256
        })
        storedKeys.push(originalStorageKey)
        await writeChatAssetObject({
          storageKey: previewStorageKey,
          source: createReadStream(previewPath),
          maxBytes: chatAssetPreviewMaxBytes,
          expectedBytes: preview.bytes,
          expectedSha256: preview.sha256
        })
        storedKeys.push(previewStorageKey)
        const now = input.now?.() ?? new Date().toISOString()
        const asset = await commitChatGeneratedAsset(input.client, {
          id: assetId,
          systemAccountId: input.systemAccountId,
          conversationId: input.conversationId,
          turnId: input.turnId,
          messageId: input.messageId,
          contentOrder: input.nextContentOrder(),
          mimeType: generated.mimeType,
          width: generated.width,
          height: generated.height,
          bytes: generated.bytes,
          sha256: generated.sha256,
          storageKey: originalStorageKey,
          previewMimeType: preview.mimeType,
          previewWidth: preview.width,
          previewHeight: preview.height,
          previewBytes: preview.bytes,
          previewSha256: preview.sha256,
          previewStorageKey,
          now,
          retentionDays: input.retentionDays,
          generation
        })
        storedKeys.length = 0
        return {
          assetId: asset.id,
          mimeType: asset.processedMimeType!,
          width: asset.processedWidth!,
          height: asset.processedHeight!,
          bytes: asset.processedBytes!,
          previewMimeType: asset.previewMimeType!,
          previewWidth: asset.previewWidth!,
          previewHeight: asset.previewHeight!,
          previewBytes: asset.previewBytes!
        }
      } catch (error) {
        await deleteChatAssetObjects(storedKeys).catch(() => undefined)
        throw error
      } finally {
        await rm(temporaryDirectory, { recursive: true, force: true }).catch(() => undefined)
      }
    }
  }
}
