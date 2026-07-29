import type {
  AuditLogDetailPayloadSupplement,
  AuditLogPayloadDetail,
  AuditPayloadBlobStorageStatus
} from '@/types/domain'

export type AuditPayloadRow = AuditLogDetailPayloadSupplement | AuditLogPayloadDetail

export function payloadCaptureStatusDescription(record: AuditPayloadRow): string {
  const available = [
    record.hasHeaders ? 'Headers' : '',
    record.hasBody ? 'Body' : ''
  ].filter(Boolean).join('、') || '无原文'
  const suffix = `当前可读：${available}。`
  if (record.captureStatus === 'complete') {
    return record.hasBody || record.hasHeaders
      ? `已保存捕获到的原始请求。${suffix}`
      : `该部分没有可保存的原始请求。${suffix}`
  }
  if (record.captureStatus === 'summary_only') {
    return `正文超过全量保存限制，已保存摘要和原始 Body SHA256。${suffix}`
  }
  if (record.captureStatus === 'hash_only') {
    return `正文未保存，只保留大小和 Body SHA256。${suffix}`
  }
  if (record.captureStatus === 'overflow') {
    return `请求超过审计活跃捕获上限，原始请求未完整保存。${suffix}`
  }
  if (record.captureStatus === 'dropped') {
    return `原始请求被审计保护裁剪，只保留大小、状态和仍可用的部分。${suffix}`
  }
  if (record.captureStatus === 'expired') {
    return `原始请求已按保留策略过期。${suffix}`
  }
  return suffix
}

export function payloadHeadersHashMissingText(record: AuditPayloadRow): string {
  if (record.hasHeaders) return 'Headers 已保存，但 Headers SHA256 未返回。'
  return 'Headers 未保存或该部分没有 Headers。'
}

export function payloadBodyHashMissingText(record: AuditPayloadRow): string {
  if (record.hasBody) return 'Body 已保存，但 Body SHA256 未返回。'
  if (record.captureStatus === 'hash_only') return '正文未保存，仅 Hash 状态下未返回 Body SHA256。'
  if (record.captureStatus === 'summary_only') return '正文仅保存摘要，Body SHA256 未返回。'
  if (record.captureStatus === 'dropped') return '正文未保存，因此没有 Body SHA256。'
  if (record.captureStatus === 'overflow') return '正文超过捕获上限，未生成 Body SHA256。'
  return '正文为空或未保存。'
}

export function payloadBodyUnavailableText(payload: AuditPayloadRow): string {
  const storageStatus = payloadStorageStatus(payload, 'body')
  if (storageStatus === 'file_missing') {
    return '正文文件缺失：数据库仍有 blob 引用，但 data/audit/blobs 下没有对应文件。'
  }
  if (storageStatus === 'metadata_missing') {
    return '正文元数据缺失：payload 引用了不存在的 blob 记录。'
  }
  if (payload.captureStatus === 'hash_only') {
    return '正文未保存：该 payload 只保留 Body SHA256 和大小。'
  }
  if (payload.captureStatus === 'summary_only') {
    return '正文未保存为原文：该 payload 只保留摘要。'
  }
  if (payload.captureStatus === 'overflow') {
    return '正文未保存：请求超过审计活跃捕获上限。'
  }
  if (payload.captureStatus === 'dropped') {
    return '正文未保存：该 payload 被审计保护裁剪，只保留大小、状态和可用的 Headers。'
  }
  return '正文未保存或该部分没有正文。'
}

export function payloadStorageStatus(
  payload: AuditPayloadRow,
  part: 'headers' | 'body'
): AuditPayloadBlobStorageStatus | undefined {
  if (!('headersStorageStatus' in payload)) return undefined
  return part === 'headers' ? payload.headersStorageStatus : payload.bodyStorageStatus
}
