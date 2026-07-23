import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Readable } from 'node:stream'

import { createGenerateImageTool } from '../../modules/chat/tools/executors/generate-image.js'

const executorSource = readFileSync('src/modules/chat/tools/executors/generate-image.ts', 'utf8')
assert.match(executorSource, /removeChatImageTempFile\(generated\.path\)/, '生图工具必须复用带 Windows 文件锁重试的临时文件清理')
assert.doesNotMatch(executorSource, /unlink\(generated\.path\)\.catch/, '生图工具不得吞掉 EBUSY\/EPERM 并永久泄漏临时文件')

const tempDir = await mkdtemp(join(tmpdir(), 'juhe-chat-image-tool-'))
const sourcePath = join(tempDir, 'generated.webp')
await writeFile(sourcePath, Buffer.from('fixture'))
let request: Record<string, unknown> | undefined
let committed = false
const tool = createGenerateImageTool()
const result = await tool.execute({ prompt: '一张普通尺寸的测试图' }, {
  environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1', turnId: 'turn-1', assistantMessageId: 'assistant-1', signal: new AbortController().signal,
  defaultImageModel: 'gpt-image-2',
  imageGeneration: async (input) => {
    request = input as unknown as Record<string, unknown>
    return { path: sourcePath, bytes: 7, sha256: 'a'.repeat(64), mimeType: 'image/webp' as const, width: 1024, height: 1024 }
  },
  artifactSink: {
    commitGeneratedImage: async (input) => {
      committed = true
      assert.equal(input.generation.operation, 'generate')
      assert.deepEqual(input.generation.sourceAssetIds, [])
      return { assetId: 'asset-1', mimeType: 'image/webp', width: 1024, height: 1024, bytes: 7, previewMimeType: 'image/webp', previewWidth: 640, previewHeight: 640, previewBytes: 100 }
    }
  }
})
assert.equal(request?.model, 'gpt-image-2')
assert.equal(request?.size, 'auto')
assert.equal(request?.outputFormat, 'webp')
assert.equal(committed, true)
assert.equal(JSON.parse(result.modelOutput).assetId, 'asset-1')

await assert.rejects(tool.execute({ prompt: '生成图片', size: '800x800' }, {
  environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1', turnId: 'turn-invalid-size', assistantMessageId: 'assistant-invalid-size', signal: new AbortController().signal,
  defaultImageModel: 'gpt-image-2', imageGeneration: async () => { throw new Error('非法尺寸不得调用上游') }, artifactSink: { commitGeneratedImage: async () => { throw new Error('非法尺寸不得提交') } }
}), /总像素/)

let editRequest: Record<string, unknown> | undefined
const editReferenceId = `chat_asset_${'4'.repeat(32)}`
const editResult = await tool.execute({ prompt: '把背景改成夜晚', action: 'edit', reference_asset_ids: [editReferenceId] }, {
  environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1', turnId: 'turn-edit', assistantMessageId: 'assistant-edit', signal: new AbortController().signal,
  defaultImageModel: 'gpt-image-2',
  loadImageEditReferences: async (assetIds) => [{ assetId: assetIds[0], stream: Readable.from(Buffer.from('fixture')), bytes: 7, mimeType: 'image/webp', filename: 'source.webp' }],
  imageGeneration: async (input) => {
    editRequest = input as unknown as Record<string, unknown>
    return { path: sourcePath, bytes: 7, sha256: 'a'.repeat(64), mimeType: 'image/webp' as const, width: 1024, height: 1024 }
  },
  artifactSink: {
    commitGeneratedImage: async (input) => {
      assert.equal(input.generation.operation, 'edit')
      assert.deepEqual(input.generation.sourceAssetIds, [editReferenceId])
      return { assetId: 'asset-edited', mimeType: 'image/webp', width: 1024, height: 1024, bytes: 7, previewMimeType: 'image/webp', previewWidth: 640, previewHeight: 640, previewBytes: 100 }
    }
  }
})
assert.equal(editRequest?.operation, 'edit')
assert.equal((editRequest?.references as unknown[])?.length, 1)
assert.equal(JSON.parse(editResult.modelOutput).operation, 'edit')

await assert.rejects(tool.execute({ prompt: '编辑一下', action: 'edit' }, {
  environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1', turnId: 'turn-no-ref', assistantMessageId: 'assistant-no-ref', signal: new AbortController().signal,
  defaultImageModel: 'gpt-image-2', imageGeneration: async () => { throw new Error('不应调用') }, artifactSink: { commitGeneratedImage: async () => { throw new Error('不应提交') } }
}), /至少引用一张/)

await assert.rejects(tool.execute({ prompt: '图片', output_format: 'gif' }, {
  environment: 'test', ownerId: 'owner-1', conversationId: 'conversation-1', turnId: 'turn-2', assistantMessageId: 'assistant-2', signal: new AbortController().signal,
  defaultImageModel: 'gpt-image-2',
  imageGeneration: async () => ({ path: sourcePath, bytes: 7, sha256: 'a'.repeat(64), mimeType: 'image/webp' as const, width: 1024, height: 1024 }),
  artifactSink: { commitGeneratedImage: async () => ({ assetId: 'asset-2', mimeType: 'image/webp', width: 1, height: 1, bytes: 1, previewMimeType: 'image/webp', previewWidth: 1, previewHeight: 1, previewBytes: 1 }) }
}), /输出格式/)

console.log('AI 问答 generate_image 工具回归通过')
