import type { DatabaseClient } from '../../storage/database-client.js'
import {
  claimExpiredChatAssetsForCleanup,
  completeChatAssetDeletion,
  releaseChatAssetDeletionClaim
} from '../../storage/chat-assets.repository.js'
import { deleteChatAssetObjects } from '../../storage/chat-asset-storage.js'

export interface ChatAssetCleanupResult {
  claimedAssets: number
  deletedAssets: number
  failedAssets: number
  hasMoreAssets: boolean
}

export async function cleanupExpiredChatAssets(input: {
  client: DatabaseClient
  now: string
  limit: number
}): Promise<ChatAssetCleanupResult> {
  const claim = await claimExpiredChatAssetsForCleanup(input.client, { now: input.now, limit: input.limit })
  let deletedAssets = 0
  let failedAssets = 0
  for (const asset of claim.assets) {
    try {
      await deleteChatAssetObjects([asset.storageKey, asset.previewStorageKey])
      const deleted = await completeChatAssetDeletion(input.client, { assetId: asset.id, claimId: claim.claimId })
      if (!deleted) throw new Error('聊天资产清理认领已变化')
      deletedAssets += 1
    } catch (error) {
      failedAssets += 1
      const retryAt = new Date(Date.parse(input.now) + cleanupRetryDelayMs(asset.cleanupAttemptCount)).toISOString()
      await releaseChatAssetDeletionClaim(input.client, {
        assetId: asset.id,
        claimId: claim.claimId,
        errorCode: error instanceof Error ? error.name || 'chat_asset_cleanup_failed' : 'chat_asset_cleanup_failed',
        retryAt,
        now: input.now
      }).catch(() => undefined)
    }
  }
  return {
    claimedAssets: claim.assets.length,
    deletedAssets,
    failedAssets,
    hasMoreAssets: claim.hasMore
  }
}

function cleanupRetryDelayMs(attemptCount: number): number {
  return Math.min(60 * 60_000, 60_000 * 2 ** Math.min(6, Math.max(0, attemptCount - 1)))
}
