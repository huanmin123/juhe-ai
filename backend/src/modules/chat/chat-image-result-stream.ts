import { createHash, randomUUID } from 'node:crypto'
import { mkdir, open, unlink } from 'node:fs/promises'
import { join } from 'node:path'

import sharp from 'sharp'

sharp.cache(false)

export type ChatImageMimeType = 'image/jpeg' | 'image/png' | 'image/webp'

export interface ChatImageResultTempFile {
  path: string
  bytes: number
  sha256: string
  mimeType?: ChatImageMimeType
  width?: number
  height?: number
}

export interface ChatImageResultDecodeOptions {
  tempDir: string
  maxDecodedBytes: number
}

export interface ChatImageResultFieldDecodeOptions extends ChatImageResultDecodeOptions {
  field: 'b64_json' | 'result'
  expectedMimeType?: ChatImageMimeType
}

export async function decodeBase64ChunksToTempFile(
  chunks: Iterable<string> | AsyncIterable<string>,
  options: ChatImageResultDecodeOptions
): Promise<ChatImageResultTempFile> {
  const maxDecodedBytes = requirePositiveInteger(options.maxDecodedBytes, 'maxDecodedBytes')
  await mkdir(options.tempDir, { recursive: true })
  const path = join(options.tempDir, 'chat-image-' + randomUUID().replace(/-/g, '') + '.tmp')
  const handle = await open(path, 'wx')
  const hash = createHash('sha256')
  let bytes = 0
  let carry = ''
  let sawPadding = false

  try {
    for await (const chunk of chunks) {
      if (typeof chunk !== 'string') throw new Error('Base64 chunk 必须是字符串')
      if (chunk.length === 0) continue
      if (sawPadding) throw new Error('Base64 padding 后不能继续接收数据')
      const input = carry + chunk
      if (!/^[A-Za-z0-9+/=]+$/u.test(input)) throw new Error('Base64 字符非法')
      const fullLength = input.length - (input.length % 4)
      carry = input.slice(fullLength)
      for (let offset = 0; offset < fullLength; offset += 4) {
        const quad = input.slice(offset, offset + 4)
        const decoded = decodeQuad(quad)
        if (quad.includes('=')) {
          if (offset + 4 !== fullLength || carry.length > 0) throw new Error('Base64 padding 位置非法')
          sawPadding = true
        }
        bytes = await writeChunk(handle, hash, decoded, bytes, maxDecodedBytes)
      }
    }

    if (sawPadding && carry.length > 0) throw new Error('Base64 padding 后存在残余数据')
    if (carry.length > 0) throw new Error('Base64 字符串被截断或缺少 padding')
    if (bytes === 0) throw new Error('Base64 图片结果不能为空')
    await handle.close()
    return { path, bytes, sha256: hash.digest('hex') }
  } catch (error) {
    await handle.close().catch(() => undefined)
    await removeTempFile(path)
    throw error
  }
}

export async function decodeBase64FieldToTempFile(
  source: Iterable<string> | AsyncIterable<string>,
  options: ChatImageResultFieldDecodeOptions
): Promise<ChatImageResultTempFile & { mimeType: ChatImageMimeType; width: number; height: number }> {
  const pathResult = await decodeBase64ChunksToTempFile(extractBase64FieldChunks(source, options.field), options)
  try {
    const metadata = await inspectImageFile(pathResult.path, options.expectedMimeType)
    return { ...pathResult, ...metadata }
  } catch (error) {
    await removeTempFile(pathResult.path)
    throw error
  }
}

export async function* extractBase64FieldChunks(
  source: Iterable<string> | AsyncIterable<string>,
  field: 'b64_json' | 'result'
): AsyncGenerator<string> {
  const marker = new RegExp(`"${field}"\\s*:\\s*"`, 'u')
  let searchBuffer = ''
  let found = false
  let closed = false
  let emitted = false
  for await (const chunk of source) {
    if (typeof chunk !== 'string') throw new Error('Base64 字段 chunk 必须是字符串')
    if (!found) {
      searchBuffer += chunk
      const match = marker.exec(searchBuffer)
      if (!match || match.index === undefined) {
        if (searchBuffer.length > 16 * 1024) searchBuffer = searchBuffer.slice(-16 * 1024)
        continue
      }
      found = true
      const value = searchBuffer.slice(match.index + match[0].length)
      searchBuffer = ''
      if (value) {
        const closing = value.indexOf('"')
        if (closing >= 0) {
          if (closing === 0) throw new Error(`受控字段 ${field} 不能为空`)
          yield value.slice(0, closing)
          emitted = true
          closed = true
        } else {
          yield value
          emitted = true
        }
      }
      continue
    }
    if (closed) {
      if (marker.test(chunk)) throw new Error(`受控字段 ${field} 重复出现`)
      continue
    }
    if (chunk.includes('\\')) throw new Error(`受控字段 ${field} 不允许 JSON 转义`)
    const closing = chunk.indexOf('"')
    if (closing < 0) {
      yield chunk
      emitted = true
      continue
    }
    if (closing > 0) { yield chunk.slice(0, closing); emitted = true }
    closed = true
  }
  if (!found || !closed || !emitted) throw new Error(`受控字段 ${field} 缺失或截断`)
}

async function inspectImageFile(path: string, expectedMimeType: ChatImageMimeType | undefined): Promise<{
  mimeType: ChatImageMimeType
  width: number
  height: number
}> {
  const handle = await open(path, 'r')
  const header = Buffer.alloc(32)
  try {
    const read = await handle.read(header, 0, header.byteLength, 0)
    const mimeType = detectImageMimeType(header.subarray(0, read.bytesRead))
    if (!mimeType) throw new Error('生成图片结果不是受支持的 JPEG/PNG/WebP')
    if (expectedMimeType !== undefined && mimeType !== expectedMimeType) {
      throw new Error(`图片 MIME 与受控字段不匹配：${mimeType} != ${expectedMimeType}`)
    }
    const metadata = await sharp(path).metadata()
    if (!Number.isSafeInteger(metadata.width) || !Number.isSafeInteger(metadata.height) || metadata.width <= 0 || metadata.height <= 0) {
      throw new Error('生成图片结果缺少有效尺寸')
    }
    return { mimeType, width: metadata.width, height: metadata.height }
  } finally {
    await handle.close().catch(() => undefined)
  }
}

export async function inspectChatImageTempFile(path: string, expectedMimeType?: ChatImageMimeType): Promise<{
  mimeType: ChatImageMimeType
  width: number
  height: number
}> {
  return inspectImageFile(path, expectedMimeType)
}

function detectImageMimeType(header: Buffer): ChatImageMimeType | undefined {
  if (header.length >= 8 && header.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) return 'image/png'
  if (header.length >= 3 && header[0] === 0xff && header[1] === 0xd8 && header[2] === 0xff) return 'image/jpeg'
  if (header.length >= 12 && header.subarray(0, 4).toString('ascii') === 'RIFF' && header.subarray(8, 12).toString('ascii') === 'WEBP') return 'image/webp'
  return undefined
}

function decodeQuad(quad: string): Buffer {
  if (!/^[A-Za-z0-9+/]{4}$/u.test(quad) && !/^[A-Za-z0-9+/]{2}==$/u.test(quad) && !/^[A-Za-z0-9+/]{3}=$/u.test(quad)) {
    throw new Error('Base64 padding 位置非法')
  }
  return Buffer.from(quad, 'base64')
}

async function writeChunk(
  handle: Awaited<ReturnType<typeof open>>,
  hash: ReturnType<typeof createHash>,
  chunk: Buffer,
  currentBytes: number,
  maxBytes: number
): Promise<number> {
  if (currentBytes + chunk.byteLength > maxBytes) throw new Error('生成图片结果超过 ' + maxBytes + ' 字节上限')
  if (chunk.byteLength === 0) return currentBytes
  hash.update(chunk)
  let offset = 0
  while (offset < chunk.byteLength) {
    const result = await handle.write(chunk, offset)
    if (result.bytesWritten <= 0) throw new Error('生成图片临时文件写入未推进')
    offset += result.bytesWritten
  }
  return currentBytes + chunk.byteLength
}

function requirePositiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(name + ' 必须是正整数')
  return value
}

async function removeTempFile(path: string): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    try {
      await unlink(path)
      return
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code
      if (code === 'ENOENT') return
      if (code !== 'EBUSY' && code !== 'EPERM') throw error
      await new Promise((resolve) => setTimeout(resolve, 50))
    }
  }
  await unlink(path)
}

export async function removeChatImageTempFile(path: string): Promise<void> {
  await removeTempFile(path)
}
