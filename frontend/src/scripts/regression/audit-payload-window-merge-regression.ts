import assert from 'node:assert/strict'

import type { AuditLogPayloadDetail } from '../../types/domain'
import { finalizeMergedPayloadBody, mergeAuditPayloadWindow } from '../../views/audit-logs/auditPayloadDetails'

const first = payloadWindow({ bodyText: 'a', bodyOffset: 0, bodyBytesReturned: 1, bodyNextOffset: 1 })
const second = payloadWindow({ bodyBase64: btoa('bc'), bodyOffset: 1, bodyBytesReturned: 2, bodyTruncated: false, headersIncluded: false })
const merged = finalizeMergedPayloadBody(mergeAuditPayloadWindow(first, second))

assert.equal(merged.bodyText, 'abc', '任意字节长度的相邻窗口必须按解码后的字节合并，不能直接拼接带 padding 的 base64')
assert.equal(merged.bodyBytesReturned, 3)
assert.equal(merged.bodyNextOffset, undefined)
assert.equal(merged.bodyTruncated, false)
assert.equal(merged.headersIncluded, true, '后续窗口不含 Headers 时应保留首窗口状态')

const utf8First = payloadWindow({ bodyBase64: btoa(String.fromCharCode(0xe2, 0x82)), bodyOffset: 0, bodyBytesReturned: 2, bodyNextOffset: 2 })
const utf8Second = payloadWindow({ bodyBase64: btoa(String.fromCharCode(0xac)), bodyOffset: 2, bodyBytesReturned: 1, bodyTruncated: false, headersIncluded: false })
assert.equal(
  finalizeMergedPayloadBody(mergeAuditPayloadWindow(utf8First, utf8Second)).bodyText,
  '€',
  'UTF-8 多字节字符跨窗口时必须按原始字节合并'
)

console.log('审计 payload 窗口合并回归通过：任意窗口边界均可按字节无损合并')

function payloadWindow(overrides: Partial<AuditLogPayloadDetail>): AuditLogPayloadDetail {
  return {
    id: 'payload-window',
    partType: 'client_request',
    sequenceIndex: 0,
    sizeBytes: 3,
    compressedSizeBytes: 3,
    captureStatus: 'complete',
    createdAt: '2026-07-20T00:00:00.000Z',
    hasHeaders: false,
    hasBody: true,
    headersStorageStatus: 'not_saved',
    headersIncluded: true,
    bodyStorageStatus: 'available',
    bodyOffset: 0,
    bodyLimit: 1,
    bodyBytesReturned: 1,
    bodyTotalBytes: 3,
    bodyTruncated: true,
    ...overrides
  }
}
