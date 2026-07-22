import assert from 'node:assert/strict'
import { mkdtemp, readdir, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import sharp from 'sharp'

import {
  decodeBase64ChunksToTempFile,
  decodeBase64FieldToTempFile,
  removeChatImageTempFile
} from '../../modules/chat/chat-image-result-stream.js'
import {
  chatAssetGeneratedMaxBytes,
  chatAssetOriginalMaxBytes,
  chatAssetProcessedMaxBytes,
  storageKeyForChatAsset
} from '../../storage/chat-asset-storage.js'

async function main(): Promise<void> {
  const tempDir = await mkdtemp(join(tmpdir(), 'juhe-chat-image-result-'))
  try {
    assert.equal(chatAssetOriginalMaxBytes, 1024 * 1024)
    assert.equal(chatAssetProcessedMaxBytes, 1024 * 1024)
    assert.equal(chatAssetGeneratedMaxBytes, 16 * 1024 * 1024)

    const pngBase64 = Buffer.from('89504e470d0a1a0a', 'hex').toString('base64')
    const result = await decodeBase64ChunksToTempFile([pngBase64.slice(0, 2), pngBase64.slice(2, 7), pngBase64.slice(7)], {
      tempDir,
      maxDecodedBytes: 16 * 1024 * 1024
    })
    assert.equal(result.bytes, 8)
    assert.equal(result.sha256.length, 64)
    assert.deepEqual(await readFile(result.path), Buffer.from('89504e470d0a1a0a', 'hex'))
    assert.match(storageKeyForChatAsset({ assetId: 'asset', sha256: result.sha256, mimeType: 'image/png' }), /\.png$/u)
    assert.match(storageKeyForChatAsset({ assetId: 'asset', sha256: result.sha256, mimeType: 'image/jpeg' }), /\.jpg$/u)
    assert.match(storageKeyForChatAsset({ assetId: 'asset', sha256: result.sha256, mimeType: 'image/webp' }), /\.webp$/u)
    await rm(result.path, { force: true, maxRetries: 10, retryDelay: 100 })

    const validPng = await sharp({ create: { width: 2, height: 1, channels: 4, background: { r: 20, g: 40, b: 80, alpha: 1 } } }).png().toBuffer()
    const pngField = JSON.stringify({ b64_json: validPng.toString('base64') })
    const fieldResult = await decodeBase64FieldToTempFile(chunkEvery(pngField, 5), {
      field: 'b64_json',
      tempDir,
      maxDecodedBytes: 16 * 1024 * 1024,
      expectedMimeType: 'image/png'
    })
    assert.equal(fieldResult.mimeType, 'image/png')
    assert.equal(fieldResult.bytes, validPng.byteLength)
    await removeChatImageTempFile(fieldResult.path)

    const jpeg = await sharp({ create: { width: 2, height: 1, channels: 3, background: { r: 20, g: 40, b: 80 } } }).jpeg().toBuffer()
    const webp = await sharp({ create: { width: 2, height: 1, channels: 3, background: { r: 20, g: 40, b: 80 } } }).webp().toBuffer()
    for (const [mimeType, bytes] of [['image/jpeg', jpeg], ['image/webp', webp]] as const) {
      const image = await decodeBase64FieldToTempFile(chunkEvery(`data: ${JSON.stringify({ result: bytes.toString('base64') })}`, 7), {
        field: 'result',
        tempDir,
        maxDecodedBytes: 16 * 1024 * 1024,
        expectedMimeType: mimeType
      })
      assert.equal(image.mimeType, mimeType)
      await removeChatImageTempFile(image.path)
    }

    await assert.rejects(
      decodeBase64ChunksToTempFile(['%%%%'], { tempDir, maxDecodedBytes: 1024 }),
      /Base64/
    )
    await assert.rejects(
      decodeBase64ChunksToTempFile(['iVBORw0KGgo'], { tempDir, maxDecodedBytes: 1024 }),
      /截断|padding/
    )
    await assert.rejects(
      decodeBase64ChunksToTempFile(['iV=O'], { tempDir, maxDecodedBytes: 1024 }),
      /Base64/
    )
    await assert.rejects(
      decodeBase64ChunksToTempFile(['aA==', 'Yg=='], { tempDir, maxDecodedBytes: 1024 }),
      /padding/
    )
    await assert.rejects(
      decodeBase64ChunksToTempFile([123 as unknown as string], { tempDir, maxDecodedBytes: 1024 }),
      /字符串|chunk|类型/
    )
    await assert.rejects(
      decodeBase64ChunksToTempFile([Buffer.alloc(17).toString('base64')], { tempDir, maxDecodedBytes: 16 }),
      /超过/
    )
    await assert.rejects(
      decodeBase64FieldToTempFile(chunkEvery(JSON.stringify({ other: pngBase64 }), 3), {
        field: 'b64_json',
        tempDir,
        maxDecodedBytes: 1024
      }),
      /字段|定位|b64_json/
    )
    await assert.rejects(
      decodeBase64FieldToTempFile(chunkEvery(JSON.stringify({ b64_json: 'aGVsbG8=' }), 3), {
        field: 'b64_json',
        tempDir,
        maxDecodedBytes: 1024
      }),
      /图片|MIME|格式|PNG|JPEG|WebP/
    )
    const leaked = await readdir(tempDir)
    assert.deepEqual(leaked, [], '所有失败路径都必须清理临时对象')
  } finally {
    await rm(tempDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 })
  }
}

function chunkEvery(value: string, size: number): string[] {
  const chunks: string[] = []
  for (let offset = 0; offset < value.length; offset += size) chunks.push(value.slice(offset, offset + size))
  return chunks
}

await main()
console.log('AI 问答生成图片结果流回归通过')
