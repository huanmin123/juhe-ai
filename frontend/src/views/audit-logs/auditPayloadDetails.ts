import type {
  AuditLogDetail,
  AuditLogPayloadDetail,
  AuditPayloadBlobStorageStatus
} from '@/types/domain'

export type AuditPayloadRow = AuditLogDetail['payloads'][number] | AuditLogPayloadDetail

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

export function payloadStorageStatusText(
  label: 'Headers' | 'Body',
  hasReference: boolean,
  status: AuditPayloadBlobStorageStatus
): string {
  if (!hasReference || status === 'not_saved') return `${label} 未保存`
  if (status === 'file_missing') return `${label} 文件缺失`
  if (status === 'metadata_missing') return `${label} 元数据缺失`
  return `${label} 可读取`
}

export function payloadStorageStatusColor(status: AuditPayloadBlobStorageStatus): string | undefined {
  if (status === 'file_missing' || status === 'metadata_missing') return 'error'
  if (status === 'available') return 'success'
  return undefined
}

export function mergeAuditPayloadWindow(
  current: AuditLogPayloadDetail,
  next: AuditLogPayloadDetail
): AuditLogPayloadDetail {
  const body = mergePayloadBody(current, next)
  return {
    ...next,
    headers: current.headers ?? next.headers,
    bodyText: body.bodyText,
    bodyBase64: body.bodyBase64,
    bodyOffset: current.bodyOffset,
    bodyLimit: current.bodyLimit,
    bodyBytesReturned: current.bodyBytesReturned + next.bodyBytesReturned,
    bodyTotalBytes: Math.max(current.bodyTotalBytes, next.bodyTotalBytes),
    bodyNextOffset: next.bodyNextOffset,
    bodyTruncated: next.bodyTruncated
  }
}

export function finalizeMergedPayloadBody(payload: AuditLogPayloadDetail): AuditLogPayloadDetail {
  if (!payload.bodyBase64 || payload.bodyText !== undefined) return payload
  const decodedText = base64ToUtf8Text(payload.bodyBase64)
  if (decodedText === undefined) return payload
  return {
    ...payload,
    bodyText: decodedText,
    bodyBase64: undefined
  }
}

function mergePayloadBody(
  current: AuditLogPayloadDetail,
  next: AuditLogPayloadDetail
): Pick<AuditLogPayloadDetail, 'bodyText' | 'bodyBase64'> {
  if (current.bodyText !== undefined && next.bodyText !== undefined) {
    return { bodyText: current.bodyText + next.bodyText }
  }
  const currentBase64 = payloadBodyWindowBase64(current)
  const nextBase64 = payloadBodyWindowBase64(next)
  return currentBase64 || nextBase64
    ? { bodyBase64: `${currentBase64}${nextBase64}` }
    : {}
}

function payloadBodyWindowBase64(payload: AuditLogPayloadDetail): string {
  if (payload.bodyBase64 !== undefined) return payload.bodyBase64
  if (payload.bodyText !== undefined) return textToBase64(payload.bodyText)
  return ''
}

function textToBase64(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let binary = ''
  const chunkSize = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize))
  }
  return btoa(binary)
}

function base64ToUtf8Text(base64: string): string | undefined {
  try {
    const binary = atob(base64)
    const bytes = new Uint8Array(binary.length)
    for (let index = 0; index < binary.length; index += 1) {
      bytes[index] = binary.charCodeAt(index)
    }
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    return undefined
  }
}
