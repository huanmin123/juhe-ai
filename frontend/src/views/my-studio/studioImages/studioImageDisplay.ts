import type { StudioAsset, StudioModelOption } from '@/types/domain'
import {
  formatStudioBytes,
  formatStudioCostEstimate,
  formatStudioDateTime,
  groupStudioAssetsByDate,
  type StudioAssetDateGroup
} from '../studioDisplayCommon'
import { studioCapabilityUnitPrice } from '../shared/studioCapabilities'

/** 图像工作台展示纯函数（日期分组、成本、资产元信息、参数快照回填）。 */

export function groupImageGalleryByDate(assets: StudioAsset[], nowMs?: number): StudioAssetDateGroup[] {
  return groupStudioAssetsByDate(assets, nowMs)
}

export function formatImageAssetTime(asset: StudioAsset, nowMs?: number): string {
  return formatStudioDateTime(asset.createdAt, nowMs)
}

/** 尺寸像素展示（仅 image 落库宽高；来源缺失 '-'）。 */
export function formatImageDimensions(asset: StudioAsset): string {
  if (typeof asset.width !== 'number' || typeof asset.height !== 'number') return '-'
  return `${asset.width}×${asset.height}`
}

export function formatImageBytes(asset: StudioAsset): string {
  return formatStudioBytes(asset.bytes)
}

/** 成本预估：单价取目录能力载荷 unitPrice（未声明 → 显式"无法估算"）；首版单张数量恒 1。 */
export function studioImageCostEstimate(model: StudioModelOption | undefined): string {
  return formatStudioCostEstimate(model ? studioCapabilityUnitPrice(model.capabilities) : undefined, 1)
}

/** 生成参数快照（画廊"回填参数重生成"依据，§3.1 params_json）。 */
export interface StudioImageParamsSnapshot {
  accountId?: string
  model?: string
  prompt?: string
  negativePrompt?: string
  size?: string
  referenceAssetId?: string
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

export function pickStudioImageParams(params: Record<string, unknown> | undefined | null): StudioImageParamsSnapshot {
  const source = params ?? {}
  return {
    accountId: optionalString(source.accountId),
    model: optionalString(source.model),
    prompt: optionalString(source.prompt),
    negativePrompt: optionalString(source.negativePrompt),
    size: optionalString(source.size),
    referenceAssetId: optionalString(source.referenceAssetId)
  }
}
