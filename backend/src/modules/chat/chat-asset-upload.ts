import { createHash, randomUUID } from 'node:crypto'
import { createWriteStream } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Readable, Transform } from 'node:stream'
import { pipeline } from 'node:stream/promises'

import Busboy from 'busboy'
import type { Request } from 'express'

import type { DatabaseClient } from '../../storage/database-client.js'
import {
  ChatAssetQuotaExceededError,
  ChatAssetCountExceededError,
  assertChatAssetUploadSlotAvailable,
  claimUncommittedChatAssetForDeletion,
  completeChatAssetDeletion,
  completeChatAssetProcessing,
  createChatAsset,
  failChatAssetProcessing,
  type ChatAssetOriginalMimeType,
  type ChatAssetRecord
} from '../../storage/chat-assets.repository.js'
import {
  chatAssetOriginalMaxBytes,
  removeChatAssetObject,
  storageKeyForChatAsset,
  writeChatAssetObject
} from '../../storage/chat-asset-storage.js'
import { ChatImageProcessingError, processChatImageFile } from './chat-image-processing.js'

export class ChatAssetUploadError extends Error {
  constructor(
    public readonly statusCode: 400 | 413 | 415,
    public readonly code: 'chat_asset_invalid_request' | 'chat_asset_too_large' | 'chat_asset_unsupported_type' | 'chat_asset_quota_exceeded' | 'chat_asset_count_exceeded',
    message: string
  ) {
    super(message)
    this.name = 'ChatAssetUploadError'
  }
}

interface UploadedTemporaryImage {
  filePath: string
  filename: string
  declaredMimeType: ChatAssetOriginalMimeType
  bytes: number
  sha256: string
}

export interface ChatAssetUploadSlotReservation {
  transferToDatabase(): void
  release(): void
}

export class ChatAssetUploadSlotReservations {
  private readonly reservedSlotsByConversation = new Map<string, number>()

  reserve(
    systemAccountId: string,
    conversationId: string,
    availableSlots: number
  ): ChatAssetUploadSlotReservation | undefined {
    const reservationKey = `${systemAccountId}\u0000${conversationId}`
    const reservedSlots = this.reservedSlotsByConversation.get(reservationKey) ?? 0
    if (reservedSlots >= availableSlots) return undefined
    this.reservedSlotsByConversation.set(reservationKey, reservedSlots + 1)
    let released = false
    const release = () => {
      if (released) return
      released = true
      const remainingSlots = (this.reservedSlotsByConversation.get(reservationKey) ?? 1) - 1
      if (remainingSlots > 0) {
        this.reservedSlotsByConversation.set(reservationKey, remainingSlots)
      } else {
        this.reservedSlotsByConversation.delete(reservationKey)
      }
    }
    return { transferToDatabase: release, release }
  }
}

const chatAssetUploadSlotReservations = new ChatAssetUploadSlotReservations()

export async function uploadChatAsset(input: {
  req: Request
  client: DatabaseClient
  systemAccountId: string
  conversationId: string
  now: string
  retentionDays: number
  lifecycle?: {
    afterAssetTransferredToDatabase?: () => Promise<void>
  }
}): Promise<ChatAssetRecord> {
  const uploadSlot = await reserveChatAssetUploadSlot(input).catch((error: unknown) => {
    if (error instanceof ChatAssetCountExceededError) {
      throw new ChatAssetUploadError(400, 'chat_asset_count_exceeded', error.message)
    }
    throw error
  })
  let temporaryDirectory: string | undefined
  let storageKey: string | undefined
  let pendingAsset: ChatAssetRecord | undefined
  try {
    temporaryDirectory = await mkdtemp(join(tmpdir(), 'juhe-ai-chat-upload-'))
    const uploaded = await readMultipartImage(input.req, temporaryDirectory)
    const processed = await processChatImageFile(uploaded.filePath).catch((error: unknown) => {
      if (error instanceof ChatImageProcessingError) {
        throw new ChatAssetUploadError(415, 'chat_asset_unsupported_type', error.message)
      }
      throw new ChatAssetUploadError(415, 'chat_asset_unsupported_type', '图片无法完成解码或压缩，请更换图片后重试')
    })
    if (processed.originalMimeType !== uploaded.declaredMimeType) {
      throw new ChatAssetUploadError(415, 'chat_asset_unsupported_type', '图片实际格式与上传 MIME 不一致')
    }
    pendingAsset = await createChatAsset(input.client, {
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      sourceKind: 'user_upload',
      originalFilename: uploaded.filename,
      originalMimeType: processed.originalMimeType,
      originalWidth: processed.originalWidth,
      originalHeight: processed.originalHeight,
      originalBytes: uploaded.bytes,
      originalSha256: uploaded.sha256,
      quotaBytes: processed.byteSize,
      now: input.now,
      retentionDays: input.retentionDays
    })
    uploadSlot.transferToDatabase()
    await input.lifecycle?.afterAssetTransferredToDatabase?.()
    storageKey = storageKeyForChatAsset({
      assetId: pendingAsset.id,
      sha256: processed.sha256,
      mimeType: processed.mimeType
    })
    await writeChatAssetObject({
      storageKey,
      source: Readable.from(processed.buffer),
      expectedBytes: processed.byteSize,
      expectedSha256: processed.sha256
    })
    return await completeChatAssetProcessing(input.client, {
      assetId: pendingAsset.id,
      systemAccountId: input.systemAccountId,
      conversationId: input.conversationId,
      processedMimeType: processed.mimeType,
      processedWidth: processed.width,
      processedHeight: processed.height,
      processedBytes: processed.byteSize,
      processedSha256: processed.sha256,
      storageKey,
      now: input.now
    })
  } catch (error) {
    const objectRemoved = await removeChatAssetObject(storageKey).then(() => true, () => false)
    if (pendingAsset) {
      if (objectRemoved) {
        const claim = await claimUncommittedChatAssetForDeletion(input.client, {
          assetId: pendingAsset.id,
          systemAccountId: input.systemAccountId,
          conversationId: input.conversationId,
          now: new Date().toISOString()
        }).catch(() => undefined)
        if (claim) await completeChatAssetDeletion(input.client, { assetId: pendingAsset.id, claimId: claim.claimId }).catch(() => false)
      } else {
        await failChatAssetProcessing(input.client, {
          assetId: pendingAsset.id,
          systemAccountId: input.systemAccountId,
          conversationId: input.conversationId,
          errorCode: uploadErrorCode(error),
          now: input.now
        }).catch(() => undefined)
      }
    }
    if (error instanceof ChatAssetQuotaExceededError) {
      throw new ChatAssetUploadError(413, 'chat_asset_quota_exceeded', error.message)
    }
    if (error instanceof ChatAssetCountExceededError) {
      throw new ChatAssetUploadError(400, 'chat_asset_count_exceeded', error.message)
    }
    throw error
  } finally {
    uploadSlot.release()
    if (temporaryDirectory) await rm(temporaryDirectory, { recursive: true, force: true }).catch(() => undefined)
  }
}

async function reserveChatAssetUploadSlot(input: {
  client: DatabaseClient
  systemAccountId: string
  conversationId: string
  now: string
}): Promise<ChatAssetUploadSlotReservation> {
  const availableSlots = await assertChatAssetUploadSlotAvailable(input.client, input)
  const reservation = chatAssetUploadSlotReservations.reserve(input.systemAccountId, input.conversationId, availableSlots)
  if (!reservation) throw new ChatAssetCountExceededError()
  return reservation
}

async function readMultipartImage(req: Request, temporaryDirectory: string): Promise<UploadedTemporaryImage> {
  if (!String(req.headers['content-type'] ?? '').toLowerCase().startsWith('multipart/form-data;')) {
    throw new ChatAssetUploadError(400, 'chat_asset_invalid_request', '图片上传必须使用 multipart/form-data')
  }
  let upload: Promise<UploadedTemporaryImage> | undefined
  let parseError: ChatAssetUploadError | undefined
  let busboy: ReturnType<typeof Busboy>
  try {
    busboy = Busboy({
      headers: req.headers,
      defParamCharset: 'utf8',
      limits: { files: 1, fields: 0, fileSize: chatAssetOriginalMaxBytes, parts: 2 }
    })
  } catch {
    throw new ChatAssetUploadError(400, 'chat_asset_invalid_request', 'multipart 请求格式无效')
  }
  busboy.on('file', (fieldName, stream, info) => {
    if (fieldName !== 'file' || upload) {
      parseError = new ChatAssetUploadError(400, 'chat_asset_invalid_request', '每次只能上传一个 file 图片字段')
      stream.resume()
      return
    }
    try {
      upload = writeTemporaryImage({
        stream,
        filePath: join(temporaryDirectory, `source-${randomUUID().replace(/-/g, '')}`),
        filename: normalizedFilename(info.filename),
        declaredMimeType: normalizedDeclaredMimeType(info.mimeType)
      })
    } catch (error) {
      parseError = error instanceof ChatAssetUploadError
        ? error
        : new ChatAssetUploadError(400, 'chat_asset_invalid_request', '图片上传字段无效')
      stream.resume()
    }
  })
  busboy.on('filesLimit', () => {
    parseError = new ChatAssetUploadError(400, 'chat_asset_invalid_request', '每次只能上传一张图片')
  })
  busboy.on('fieldsLimit', () => {
    parseError = new ChatAssetUploadError(400, 'chat_asset_invalid_request', '图片上传不能包含额外表单字段')
  })
  busboy.on('partsLimit', () => {
    parseError = new ChatAssetUploadError(400, 'chat_asset_invalid_request', '每次只能上传一张图片')
  })
  try {
    await pipeline(req, busboy)
  } catch (error) {
    if (req.aborted || (error as NodeJS.ErrnoException).code === 'ECONNRESET') {
      throw new ChatAssetUploadError(400, 'chat_asset_invalid_request', '图片上传连接已中断')
    }
    throw error
  }
  if (parseError) throw parseError
  if (!upload) throw new ChatAssetUploadError(400, 'chat_asset_invalid_request', '缺少 file 图片字段')
  return await upload
}

async function writeTemporaryImage(input: {
  stream: NodeJS.ReadableStream
  filePath: string
  filename: string
  declaredMimeType: ChatAssetOriginalMimeType
}): Promise<UploadedTemporaryImage> {
  let bytes = 0
  let limitHit = false
  const hash = createHash('sha256')
  input.stream.on('limit', () => { limitHit = true })
  const counter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      bytes += chunk.byteLength
      hash.update(chunk)
      callback(null, chunk)
    }
  })
  await pipeline(input.stream, counter, createWriteStream(input.filePath, { flags: 'wx' }))
  if (limitHit || bytes > chatAssetOriginalMaxBytes) {
    throw new ChatAssetUploadError(413, 'chat_asset_too_large', '单张上传图片不能超过 3 MiB')
  }
  if (bytes <= 0) throw new ChatAssetUploadError(400, 'chat_asset_invalid_request', '上传图片不能为空')
  return {
    filePath: input.filePath,
    filename: input.filename,
    declaredMimeType: input.declaredMimeType,
    bytes,
    sha256: hash.digest('hex')
  }
}

function normalizedFilename(value: string | undefined): string {
  const filename = String(value ?? '').split(/[\\/]/).pop()?.trim().replace(/[\u0000-\u001f\u007f]/g, '')
  if (!filename) throw new ChatAssetUploadError(400, 'chat_asset_invalid_request', '图片文件名不能为空')
  return filename.slice(0, 255)
}

function normalizedDeclaredMimeType(value: string | undefined): ChatAssetOriginalMimeType {
  const mimeType = String(value ?? '').trim().toLowerCase()
  if (mimeType === 'image/jpg' || mimeType === 'image/pjpeg') return 'image/jpeg'
  if (mimeType === 'image/jpeg' || mimeType === 'image/png' || mimeType === 'image/webp' || mimeType === 'image/gif') return mimeType
  throw new ChatAssetUploadError(415, 'chat_asset_unsupported_type', '仅支持 JPEG、PNG、WebP 或 GIF 图片')
}

function uploadErrorCode(error: unknown): string {
  if (error instanceof ChatAssetUploadError || error instanceof ChatImageProcessingError) return error.code
  return 'chat_asset_processing_failed'
}
