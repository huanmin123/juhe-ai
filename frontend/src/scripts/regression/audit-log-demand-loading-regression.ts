import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { AuditLogDetailSupplement, AuditLogListItem, AuditLogPayloadDetail } from '../../types/domain'
import { useAuditLogDetailPayload } from '../../views/audit-logs/useAuditLogDetailPayload'

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

const auditViewSource = source('../../views/audit-logs/AuditLogsView.vue')
const logsApiSource = source('../../api/domains/logs.ts')

assert.doesNotMatch(
  auditViewSource,
  /auditLogs\.runtime|scheduleAuditRuntimeRefresh|refreshAuditRuntimeQuietly|useAuditLogRuntimeAlert/,
  '审计页面无交互时不得请求非首屏必要的完整运行态'
)
assert.match(
  logsApiSource,
  /detail: \(id: string\) => unwrap<AuditLogDetailSupplement>/,
  '审计详情 API 必须声明为列表行补充字段'
)
assert.doesNotMatch(
  logsApiSource,
  /auditLogsApi[\s\S]{0,500}runtime:/,
  '前端不得保留无人使用且容易重新接入首屏的审计运行态 API'
)

type Deferred<T> = {
  promise: Promise<T>
  reject: (error: Error) => void
  resolve: (value: T) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, reject, resolve }
}

const supplementRequest = deferred<AuditLogDetailSupplement>()
let payloadCalls = 0
const payloadDetail = {
  id: 'payload-full',
  bodyText: '完整正文',
  bodyTruncated: false
} as AuditLogPayloadDetail
const state = useAuditLogDetailPayload({
  loadDetail: async () => supplementRequest.promise,
  loadPayload: async (auditLogId, payloadId) => {
    payloadCalls += 1
    assert.deepEqual([auditLogId, payloadId], ['audit-delta', 'payload-full'])
    return payloadDetail
  }
})
const opening = state.openDetail(auditListItem('audit-delta', {
  accountName: '列表账户',
  path: '/v1/responses',
  traceId: 'trace-from-row'
}))
assert.equal(payloadCalls, 0, '打开详情不得自动请求 payload')
supplementRequest.resolve({
  attempts: [],
  payloads: [],
  sampleBucket: 0,
  sampleReason: 'problem',
  startedAt: '2026-07-29T00:00:00.000Z',
  endedAt: '2026-07-29T00:00:01.000Z'
} satisfies AuditLogDetailSupplement)
await opening
assert.equal(state.detail.value?.traceId, 'trace-from-row', '详情 supplement 不得清空列表已有字段')
assert.equal(state.detail.value?.accountName, '列表账户', '详情 supplement 类型不得允许覆盖列表已有字段')
assert.equal(state.detail.value?.path, '/v1/responses', '详情 supplement 未提供的可展示字段应保留')
assert.equal(payloadCalls, 0, '详情 supplement 返回后仍不得自动请求 payload')
await state.loadPayload('payload-full')
assert.equal(payloadCalls, 1, '用户明确打开 payload 后只应发起一次完整内容请求')
assert.deepEqual(state.selectedPayload.value, payloadDetail)

const detailRequests = new Map<string, Deferred<AuditLogDetailSupplement>>()
const staleErrors: string[] = []
const raceState = useAuditLogDetailPayload({
  loadDetail: (id) => {
    const request = deferred<AuditLogDetailSupplement>()
    detailRequests.set(id, request)
    return request.promise
  },
  reportError: (text) => staleErrors.push(text)
})
const detailA = raceState.openDetail(auditListItem('audit-race-a'))
const detailB = raceState.openDetail(auditListItem('audit-race-b'))
detailRequests.get('audit-race-a')?.reject(new Error('stale detail failure'))
detailRequests.get('audit-race-b')?.resolve({
  attempts: [],
  payloads: [],
  sampleBucket: 0,
  sampleReason: 'problem',
  startedAt: '2026-07-29T00:00:00.000Z',
  endedAt: '2026-07-29T00:00:01.000Z'
})
await Promise.all([detailA, detailB])
assert.equal(raceState.detail.value?.id, 'audit-race-b', '旧详情响应不得覆盖新详情')
assert.deepEqual(staleErrors, [], '过期详情失败不得在新详情上弹错')

console.log('审计日志按需加载回归通过：首屏零 runtime，详情 delta 合并，payload 仅显式点击后单次读取')

function auditListItem(id: string, overrides: Partial<AuditLogListItem> = {}): AuditLogListItem {
  return {
    id,
    traceId: `trace-${id}`,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    stream: true,
    auditOutcome: 'success',
    success: true,
    createdAt: '2026-07-29T00:00:00.000Z',
    ...overrides
  }
}
