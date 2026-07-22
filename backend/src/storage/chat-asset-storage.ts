import { createHash, randomUUID } from 'node:crypto'
import { createWriteStream, mkdirSync, type ReadStream } from 'node:fs'
import { open, rename, unlink } from 'node:fs/promises'
import { dirname, relative, resolve } from 'node:path'
import { Transform, type Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'

import { runtimeConfig } from '../config/runtime.js'

export const chatAssetOriginalMaxBytes = 1024 * 1024
export const chatAssetProcessedMaxBytes = 1024 * 1024
export const chatAssetGeneratedMaxBytes = 16 * 1024 * 1024
export const chatAssetPreviewMaxBytes = 512 * 1024
export const chatAssetGeneratedQuotaMaxBytes = chatAssetGeneratedMaxBytes + chatAssetPreviewMaxBytes

export class ChatAssetStorageError extends Error {
  constructor(public readonly code: 'chat_asset_empty' | 'chat_asset_too_large' | 'chat_asset_integrity_mismatch', message: string) {
    super(message)
    this.name = 'ChatAssetStorageError'
  }
}

export interface ChatAssetObjectWriteResult {
  storageKey: string
  bytes: number
  sha256: string
}

export interface ChatAssetObjectReadResult {
  stream: ReadStream
  bytes: number
}

export function storageKeyForChatAsset(input: {
  assetId: string
  sha256: string
  mimeType: string
  variant?: 'original' | 'preview'
}): string {
  const assetId = safeStorageSegment(input.assetId, 120)
  const sha256 = normalizedSha256(input.sha256)
  const extension = extensionForChatAssetMimeType(input.mimeType)
  const variant = input.variant ? `-${input.variant}` : ''
  return `${sha256.slice(0, 2)}/${sha256.slice(2, 4)}/${assetId}${variant}-${sha256.slice(0, 16)}${extension}`
}

export function chatAssetObjectPath(storageKey: string): string {
  const root = chatAssetsRoot()
  const normalizedKey = normalizedStorageKey(storageKey)
  const target = resolve(root, normalizedKey)
  const relativePath = relative(root, target)
  if (relativePath.startsWith('..') || relativePath === '' || /^[A-Za-z]:/.test(relativePath)) {
    throw new Error('聊天资产存储键越出受限目录')
  }
  return target
}

export async function writeChatAssetObject(input: {
  storageKey: string
  source: Readable
  maxBytes?: number
  expectedBytes?: number
  expectedSha256?: string
}): Promise<ChatAssetObjectWriteResult> {
  const maxBytes = normalizedObjectMaxBytes(input.maxBytes, chatAssetProcessedMaxBytes)
  const filePath = chatAssetObjectPath(input.storageKey)
  mkdirSync(dirname(filePath), { recursive: true })
  const temporaryPath = `${filePath}.tmp-${randomUUID().replace(/-/g, '')}`
  const hash = createHash('sha256')
  let bytes = 0
  const limiter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      bytes += chunk.byteLength
      if (bytes > maxBytes) {
        callback(new ChatAssetStorageError('chat_asset_too_large', `处理后图片不能超过 ${maxBytes} 字节`))
        return
      }
      hash.update(chunk)
      callback(null, chunk)
    }
  })
  try {
    await pipeline(input.source, limiter, createWriteStream(temporaryPath, { flags: 'wx' }))
    if (bytes <= 0) throw new ChatAssetStorageError('chat_asset_empty', '处理后图片不能为空')
    const sha256 = hash.digest('hex')
    if (input.expectedBytes !== undefined && bytes !== normalizedPositiveInteger(input.expectedBytes, 'expectedBytes')) {
      throw new ChatAssetStorageError('chat_asset_integrity_mismatch', '处理后图片字节数校验失败')
    }
    if (input.expectedSha256 !== undefined && sha256 !== normalizedSha256(input.expectedSha256)) {
      throw new ChatAssetStorageError('chat_asset_integrity_mismatch', '处理后图片哈希校验失败')
    }
    await rename(temporaryPath, filePath)
    return { storageKey: input.storageKey, bytes, sha256 }
  } catch (error) {
    await unlink(temporaryPath).catch(() => undefined)
    throw error
  }
}

export async function openChatAssetObject(storageKey: string, maxBytes = chatAssetProcessedMaxBytes): Promise<ChatAssetObjectReadResult> {
  const normalizedMaxBytes = normalizedObjectMaxBytes(maxBytes, chatAssetProcessedMaxBytes)
  const handle = await open(chatAssetObjectPath(storageKey), 'r')
  try {
    const stat = await handle.stat()
    if (!stat.isFile()) throw new Error('聊天资产不是普通文件')
    if (stat.size <= 0) throw new ChatAssetStorageError('chat_asset_empty', '聊天资产文件为空')
    if (stat.size > normalizedMaxBytes) {
      throw new ChatAssetStorageError('chat_asset_too_large', `聊天资产超过读取上限 ${normalizedMaxBytes} 字节`)
    }
    return {
      stream: handle.createReadStream({
        autoClose: true,
        start: 0,
        end: stat.size - 1
      }),
      bytes: stat.size
    }
  } catch (error) {
    await handle.close().catch(() => undefined)
    throw error
  }
}

export async function removeChatAssetObject(storageKey: string | undefined): Promise<void> {
  if (!storageKey) return
  const filePath = chatAssetObjectPath(storageKey)
  try {
    await unlink(filePath)
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
  }
}

export async function deleteChatAssetObjects(storageKeys: readonly (string | undefined)[]): Promise<void> {
  const keys = [...new Set(storageKeys.filter((key): key is string => Boolean(key)))]
  if (keys.length > 2) throw new Error('单个聊天资产最多包含两个待删除对象')
  await Promise.all(keys.map((key) => removeChatAssetObject(key)))
}

function chatAssetsRoot(): string {
  const root = resolve(runtimeConfig.chatAssetsRoot)
  mkdirSync(root, { recursive: true })
  return root
}

function normalizedStorageKey(value: string): string {
  const normalized = value.trim().replace(/\\/g, '/')
  if (!normalized || normalized.startsWith('/') || normalized.includes('\0')) {
    throw new Error('聊天资产存储键无效')
  }
  if (normalized.length > 512) throw new Error('聊天资产存储键过长')
  return normalized
}

function safeStorageSegment(value: string, maxLength: number): string {
  return value.trim().replace(/[^A-Za-z0-9_.-]/g, '_').slice(0, maxLength) || 'asset'
}

function extensionForChatAssetMimeType(mimeType: string): string {
  const normalized = mimeType.trim().toLowerCase()
  if (normalized === 'image/jpeg') return '.jpg'
  if (normalized === 'image/png') return '.png'
  if (normalized === 'image/webp') return '.webp'
  throw new Error(`不支持的聊天资产 MIME：${mimeType}`)
}

export async function writeChatGeneratedAssetObject(input: {
  storageKey: string
  source: Readable
  maxBytes?: number
  expectedBytes?: number
  expectedSha256?: string
}): Promise<ChatAssetObjectWriteResult> {
  return writeChatAssetObject({ ...input, maxBytes: input.maxBytes ?? chatAssetGeneratedMaxBytes })
}

export function openChatGeneratedAssetObject(storageKey: string, maxBytes = chatAssetGeneratedMaxBytes): Promise<ChatAssetObjectReadResult> {
  return openChatAssetObject(storageKey, maxBytes)
}

function normalizedObjectMaxBytes(value: number | undefined, fallback: number): number {
  const normalized = normalizedPositiveInteger(value ?? fallback, 'maxBytes')
  return Math.min(normalized, chatAssetGeneratedMaxBytes)
}

function normalizedPositiveInteger(value: number, field: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${field} 必须是正整数`)
  return value
}

function normalizedSha256(value: string): string {
  const normalized = value.trim().toLowerCase()
  if (!/^[a-f0-9]{64}$/.test(normalized)) throw new Error('聊天资产 SHA-256 无效')
  return normalized
}
