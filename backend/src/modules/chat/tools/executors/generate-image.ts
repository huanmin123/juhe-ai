import type { ChatInternalToolDefinition } from '../contracts.js'
import { normalizeChatImageOutputFormat, normalizeChatImageQuality, normalizeChatImageSize } from '../../chat-image-policy.js'
import { resolveChatImageModel } from '../../chat-image-model-registry.js'
import type { ChatImageEditReference } from '../../chat-image-edit-references.js'
import { removeChatImageTempFile } from '../../chat-image-result-stream.js'

const generateImageInputSchema = {
  type: 'object',
  properties: {
    action: { type: 'string', enum: ['auto', 'generate', 'edit'] },
    prompt: { type: 'string', minLength: 1, maxLength: 65_536 },
    reference_asset_ids: {
      type: 'array',
      minItems: 1,
      maxItems: 5,
      uniqueItems: true,
      items: { type: 'string', pattern: '^chat_asset_[a-f0-9]{32}$' }
    },
    model: { type: 'string', enum: ['gpt-image-2'] },
    size: { type: 'string', minLength: 3, maxLength: 20 },
    quality: { type: 'string', enum: ['auto', 'low', 'medium', 'high'] },
    output_format: { type: 'string', enum: ['webp', 'png', 'jpeg'] }
  },
  required: ['prompt'],
  additionalProperties: false
} as const

export const chatImageGenerationGatewayModel = 'gpt-image-2'

export function createGenerateImageTool(): ChatInternalToolDefinition {
  return {
    id: 'image.generate',
    version: '2.0.0',
    modelName: 'generate_image',
    title: '生成或编辑图片',
    description: '根据用户需求生成图片，或使用同一会话中明确的 assetId 编辑既有图片。编辑时必须传 reference_asset_ids；无法唯一判断目标图片时先询问用户。未指定格式时优先输出 WebP；未指定尺寸时按用途选择常规尺寸和比例，不要无依据选择 2K 或 4K。',
    inputSchema: generateImageInputSchema,
    executionKind: 'network_adapter',
    executionOwner: 'application',
    limits: {
      maxArgumentBytes: 96 * 1024,
      maxResultBytes: 16 * 1024,
      timeoutMs: 600_000
    },
    availability: { requiresImageGenerationEnabled: true },
    duplicatePolicy: 'reuse_exact',
    execute: async (input, context) => {
      if (!context.imageGeneration || !context.artifactSink) throw new Error('图片工具运行时未配置图像适配器或资产接收器')
      const prompt = String(input.prompt).trim()
      if (!prompt) throw new Error('图像提示词不能为空')
      const referenceAssetIds = normalizeReferenceAssetIds(input.reference_asset_ids)
      const requestedAction = input.action === undefined ? 'auto' : String(input.action)
      if (requestedAction !== 'auto' && requestedAction !== 'generate' && requestedAction !== 'edit') throw new Error('图片操作类型无效')
      const operation = requestedAction === 'edit' || (requestedAction === 'auto' && referenceAssetIds.length > 0) ? 'edit' : 'generate'
      if (operation === 'edit' && referenceAssetIds.length === 0) throw new Error('编辑图片必须至少引用一张来源图片')
      if (operation === 'generate' && referenceAssetIds.length > 0) throw new Error('生成图片不能携带来源图片，请改用 edit 或 auto')
      const imageModel = resolveChatImageModel(input.model, context.defaultImageModel ?? chatImageGenerationGatewayModel)
      if (operation === 'edit' && !imageModel.canEdit) throw new Error(`图像模型 ${imageModel.id} 不支持编辑`)
      if (operation === 'generate' && !imageModel.canGenerate) throw new Error(`图像模型 ${imageModel.id} 不支持生成`)
      const references = operation === 'edit'
        ? await requireImageEditReferences(context.loadImageEditReferences, referenceAssetIds)
        : []
      const allowLarge = /(?:\b(?:2k|4k|2048|4096)\b|2\s*千|4\s*千|超高清|原图级)/iu.test(context.userContent ?? '')
      const size = normalizeChatImageSize(input.size, { allowLarge })
      const quality = normalizeChatImageQuality(input.quality)
      const outputFormat = normalizeChatImageOutputFormat(input.output_format)
      try {
        const generated = await context.imageGeneration({
          operation,
          model: imageModel.id,
          prompt,
          size: size.size,
          allowLarge,
          quality,
          outputFormat,
          references,
          signal: context.signal
        })
        try {
          const artifact = await context.artifactSink.commitGeneratedImage({
            result: generated,
            generation: {
              operation,
              model: imageModel.id,
              prompt,
              sourceAssetIds: referenceAssetIds,
              size: size.size,
              quality,
              outputFormat
            }
          })
          const payload = {
            assetId: artifact.assetId,
            mimeType: artifact.mimeType,
            width: artifact.width,
            height: artifact.height,
            bytes: artifact.bytes,
            previewMimeType: artifact.previewMimeType,
            previewWidth: artifact.previewWidth,
            previewHeight: artifact.previewHeight,
            previewBytes: artifact.previewBytes,
            operation,
            model: imageModel.id,
            sourceAssetIds: referenceAssetIds,
            size: size.size,
            sizeAdjusted: size.sizeAdjusted,
            outputFormat,
            ...(generated.revisedPrompt ? { revisedPrompt: generated.revisedPrompt } : {})
          }
          return { modelOutput: JSON.stringify(payload), publicResult: payload }
        } finally {
          await removeChatImageTempFile(generated.path)
        }
      } finally {
        for (const reference of references) reference.stream.destroy()
      }
    },
    projectResult: (result) => result.publicResult
  }
}

function normalizeReferenceAssetIds(value: unknown): string[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) throw new Error('引用图片必须是 assetId 数组')
  const assetIds = value.map((item) => String(item).trim())
  if (assetIds.some((assetId) => !/^chat_asset_[a-f0-9]{32}$/.test(assetId))) throw new Error('引用图片 assetId 无效')
  if (assetIds.length > 5) throw new Error('编辑图片最多引用 5 张来源图片')
  if (new Set(assetIds).size !== assetIds.length) throw new Error('引用图片不能重复')
  return assetIds
}

async function requireImageEditReferences(
  loader: ((assetIds: readonly string[]) => Promise<ChatImageEditReference[]>) | undefined,
  assetIds: readonly string[]
): Promise<ChatImageEditReference[]> {
  if (!loader) throw new Error('图片工具运行时未配置编辑引用装载器')
  return loader(assetIds)
}
