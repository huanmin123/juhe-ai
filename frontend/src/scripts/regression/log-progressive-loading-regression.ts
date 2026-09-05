import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { AuditLogDetailSupplement, AuditLogPayloadDetail, AuditLogSummary } from '../../types/domain'
import { useAuditLogDetailPayload } from '../../views/audit-logs/useAuditLogDetailPayload'

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

const auditViewSource = source('../../views/audit-logs/AuditLogsView.vue')
const auditModeBridgeSource = source('../../views/audit-logs/useAuditLogModeBridge.ts')
const auditPayloadStateSource = source('../../views/audit-logs/useAuditLogDetailPayload.ts')
const auditDetailDrawerSource = source('../../views/audit-logs/AuditLogDetailDrawer.vue')
const runtimeViewSource = source('../../views/runtime-logs/RuntimeLogsView.vue')
const runtimePageContentSource = source('../../views/runtime-logs/RuntimeLogPageContent.vue')
const runtimeFilterToolbarSource = source('../../views/runtime-logs/RuntimeLogFilterToolbar.vue')
const runtimeIndexStateSource = source('../../views/runtime-logs/useRuntimeLogIndexSearchState.ts')
const runtimeFacetsStateSource = source('../../views/runtime-logs/useRuntimeLogFacetsState.ts')
const logsApiSource = source('../../api/domains/logs.ts')
const runtimeTypesSource = source('../../types/domain/runtime-logs.ts')
const runtimeRouteSource = source('../../../../migration-backup/node/final-archive/backend/src/modules/runtime-logs/runtime-logs.routes.ts')
const auditRouteSource = source('../../../../migration-backup/node/final-archive/backend/src/modules/audit-logs/audit-logs.routes.ts')
const globalStylesSource = source('../../styles/global.css')

const auditFetchPageSource = auditViewSource.match(/fetchPage: async[\s\S]*?requestSignature:/)?.[0] ?? ''
assert.doesNotMatch(
  auditFetchPageSource,
  /refreshAuditRuntimeQuietly/,
  '审计列表和翻页不得连带请求完整运行态'
)
assert.doesNotMatch(
  auditModeBridgeSource,
  /refreshAuditRuntimeQuietly/,
  '审计列表刷新和模式切换不得连带请求完整运行态'
)
assert.doesNotMatch(
  auditViewSource,
  /auditLogs\.runtime|scheduleAuditRuntimeRefresh|refreshAuditRuntimeQuietly|useAuditLogRuntimeAlert/,
  '审计页面无交互时不得请求非首屏必要的完整运行态'
)

assert.doesNotMatch(
  runtimeIndexStateSource,
  /loadRuntimeLogFacets/,
  '运行日志列表和翻页不得连带加载筛选项或运行态'
)
assert.match(
  runtimeFilterToolbarSource,
  /@dropdown-visible-change="handleFacetsOpen"/,
  '事件筛选项应在用户打开下拉时才加载'
)
assert.match(
  runtimeFilterToolbarSource,
  /@open-change="handleFacetsOpen"/,
  '时间筛选项应在用户打开选择器时才加载'
)
assert.match(runtimePageContentSource, /@facets-open="emit\('facetsOpen'\)"/, '筛选打开事件必须透传到页面')
assert.match(runtimeViewSource, /@facets-open="loadRuntimeLogFacets"/, '页面只在筛选交互时读取轻 facets')
assert.doesNotMatch(runtimeViewSource, /scheduleRuntimeStatusRefresh|loadRuntimeLogRuntime/, '运行日志页无交互时不得补请求运行态')
assert.doesNotMatch(runtimeFacetsStateSource, /loadRuntimeLogRuntime|unavailableRuntimeLogRuntime/, '轻 facets 状态不得夹带独立运行态请求')
assert.doesNotMatch(auditViewSource, /<RuntimeAvailabilityAlert|<a-alert/, '审计日志页面不得显示运行态或搜索结果横幅')
assert.doesNotMatch(source('../../views/runtime-logs/RuntimeLogListSection.vue'), /<a-alert/, '运行日志页面不得显示搜索结果横幅')
assert.match(globalStylesSource, /\.ant-alert\s*\{\s*display:\s*none\s*!important;/, '全局样式必须禁止页面横幅展示')
assert.doesNotMatch(logsApiSource, /runtime:\s*\(\)/, '前端日志 API 不得保留无消费者的运行态入口')
assert.doesNotMatch(runtimeRouteSource, /runtimeLogsRouter\.get\('\/runtime'/, '后端不得保留无页面消费者的运行态接口')

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

const detailRequests = new Map<string, Deferred<AuditLogDetailSupplement>>()
const staleErrors: string[] = []
const detailState = useAuditLogDetailPayload({
  loadDetail: (id) => {
    const request = deferred<AuditLogDetailSupplement>()
    detailRequests.set(id, request)
    return request.promise
  },
  reportError: (text) => staleErrors.push(text)
})
const detailA = detailState.openDetail({ id: 'audit-race-a' } as AuditLogSummary)
const detailB = detailState.openDetail({ id: 'audit-race-b' } as AuditLogSummary)
detailRequests.get('audit-race-a')?.reject(new Error('stale detail failure'))
detailRequests.get('audit-race-b')?.resolve({ attempts: [], payloads: [] } as AuditLogDetailSupplement)
await Promise.all([detailA, detailB])
assert.equal(detailState.detail.value?.id, 'audit-race-b', '旧详情响应不得覆盖新详情')
assert.deepEqual(staleErrors, [], '过期详情失败不得在新详情上弹错')

let payloadCallsBeforeExplicitAction = 0
const detailMergeRequest = deferred<AuditLogDetailSupplement>()
const detailMergeState = useAuditLogDetailPayload({
  loadDetail: async () => detailMergeRequest.promise,
  loadPayload: async () => {
    payloadCallsBeforeExplicitAction += 1
    return { id: 'unexpected-payload' } as AuditLogPayloadDetail
  }
})
const detailMergeOpen = detailMergeState.openDetail({
  id: 'audit-delta',
  traceId: 'trace-from-row',
  accountName: '列表账户',
  path: '/v1/responses'
} as AuditLogSummary)
assert.equal(detailMergeState.detail.value, undefined, '详情 supplement 返回前应隐藏上一条详情')
assert.equal(payloadCallsBeforeExplicitAction, 0, '打开详情不得自动请求 payload')
detailMergeRequest.resolve({
  id: 'audit-delta',
  accountName: '详情账户',
  attempts: [],
  payloads: []
} as AuditLogDetailSupplement)
await detailMergeOpen
assert.equal(detailMergeState.detail.value?.traceId, 'trace-from-row', '详情 supplement 不得清空列表已有字段')
assert.equal(detailMergeState.detail.value?.accountName, '详情账户', '详情 supplement 应覆盖同名列表字段')
assert.equal(detailMergeState.detail.value?.path, '/v1/responses', '详情 supplement 未提供的可展示字段应保留')
assert.equal(payloadCallsBeforeExplicitAction, 0, '详情 supplement 返回后仍不得自动请求 payload')

const payloadFailure = deferred<never>()
const payloadErrors: string[] = []
const payloadState = useAuditLogDetailPayload({
  loadDetail: async () => ({ attempts: [], payloads: [] } as AuditLogDetailSupplement),
  loadPayload: async () => payloadFailure.promise,
  reportError: (text) => payloadErrors.push(text)
})
await payloadState.openDetail({ id: 'audit-payload-race-a' } as AuditLogSummary)
const stalePayload = payloadState.loadPayload('payload-a')
await payloadState.openDetail({ id: 'audit-payload-race-b' } as AuditLogSummary)
payloadFailure.reject(new Error('stale payload failure'))
await stalePayload
assert.equal(payloadState.detail.value?.id, 'audit-payload-race-b', '切换详情后应保留新详情')
assert.equal(payloadState.selectedPayload.value, undefined, '旧 payload 失败不得写入新详情')
assert.deepEqual(payloadErrors, [], '过期 payload 失败不得在新详情上弹错')

const fullPayload = {
  id: 'payload-full',
  bodyText: '完整正文',
  bodyTruncated: false
} as AuditLogPayloadDetail
const payloadCalls: Array<[string, string]> = []
const fullPayloadState = useAuditLogDetailPayload({
  loadDetail: async () => ({ attempts: [], payloads: [] } as AuditLogDetailSupplement),
  loadPayload: async (auditLogId, payloadId) => {
    payloadCalls.push([auditLogId, payloadId])
    return fullPayload
  }
})
await fullPayloadState.openDetail({ id: 'audit-full' } as AuditLogSummary)
await fullPayloadState.loadPayload('payload-full')
assert.deepEqual(payloadCalls, [['audit-full', 'payload-full']], '查看审计 payload 只能发起一次完整内容请求')
assert.deepEqual(fullPayloadState.selectedPayload.value, fullPayload, '完整 payload 响应应直接展示，不再做前端分段拼接')
assert.match(logsApiSource, /grepOptions: \(\) => unwrap<RuntimeLogGrepRuntime>/, 'grep 文件范围应有独立按需 API')
assert.match(runtimeViewSource, /loadRuntimeLogGrepOptions/, '只有 grep 模式才应加载文件范围选项')
const facetsType = runtimeTypesSource.match(/export interface RuntimeLogFacets \{[\s\S]*?\n\}/)?.[0] ?? ''
assert.doesNotMatch(facetsType, /grep:/, '索引 facets 不得夹带 grep 文件扫描结果')
assert.deepEqual(
  [...facetsType.matchAll(/^\s{2}([A-Za-z][A-Za-z0-9]*)(?:\?|):/gm)].map((match) => match[1]),
  ['retentionDays', 'earliestIndexedAt', 'latestIndexedAt', 'totalIndexed', 'levels', 'events'],
  '索引 facets 类型只应包含保留范围、索引数量与筛选项'
)
const runtimeFacetsRouteSource = runtimeRouteSource.match(
  /runtimeLogsRouter\.get\('\/facets'[\s\S]*?(?=\nruntimeLogsRouter\.get\('\/grep-options')/
)?.[0] ?? ''
assert.match(runtimeFacetsRouteSource, /requestDbService\(\{ type: 'get_runtime_log_facets' \}\)/, 'facets 必须只读取专用只读 worker 结果')
assert.doesNotMatch(runtimeFacetsRouteSource, /requestServerRuntime|type: 'status'|getRuntimeLogGrepRuntime|buildBackgroundQueueHealthSnapshot|gatewayAccountSideEffects/, 'facets 不得读取 server runtime、DB status、grep 或网关副作用')

const openAuditDetailSource = auditPayloadStateSource.match(/async function openDetail[\s\S]*?async function loadPayload/)?.[0] ?? ''
assert.match(openAuditDetailSource, /payloadRequestId \+= 1/, '切换审计详情时必须作废上一条记录的 payload 请求')
assert.match(openAuditDetailSource, /detail\.value = undefined/, '加载新审计详情时必须立即隐藏旧详情，避免旧 payload 操作串页')
assert.match(openAuditDetailSource, /detail\.value = \{ \.\.\.record, \.\.\.nextDetail \}/, '详情响应必须作为 supplement 与当前列表行合并，不能丢失列表已有展示字段')
assert.doesNotMatch(auditPayloadStateSource, /loadNextPayloadWindow|auditPayloadReadWindowBytes/, '前端不得保留审计正文分段加载状态')
assert.doesNotMatch(auditDetailDrawerSource, /加载下一段|load-next-payload/, '审计详情不得显示下一段交互')
assert.match(logsApiSource, /payload: \(id: string, payloadId: string\)/, '审计 payload API 不应再暴露 offset/limit 参数')
assert.match(logsApiSource, /detail: \(id: string\) => unwrap<AuditLogDetailSupplement>/, '审计详情 API 必须声明为列表行补充字段，不能伪装成完整详情')
assert.doesNotMatch(logsApiSource, /auditLogsApi[\s\S]{0,500}runtime:/, '前端不得保留无人使用且容易重新接入首屏的审计运行态 API')
const auditPayloadRouteSource = auditRouteSource.match(/auditLogsRouter\.get\('\/:id\/payloads\/:payloadId'[\s\S]*?\n}\)/)?.[0] ?? ''
assert.match(auditPayloadRouteSource, /full: true/, '管理员审计 payload 接口必须一次返回完整正文')
assert.doesNotMatch(auditPayloadRouteSource, /req\.query\.(offset|limit)/, '管理员审计 payload 接口不得继续接受窗口参数')

const grepItemType = runtimeTypesSource.match(/export interface RuntimeLogGrepItem \{[\s\S]*?\n\}/)?.[0] ?? ''
assert.doesNotMatch(grepItemType, /rawJson:|\n  line:|\n  file:/, 'grep 列表不得提前返回原始行或服务器完整路径')
assert.match(logsApiSource, /grepDetail: \(item: \{ id: string; fileName: string; lineNumber: number \}\)/, 'grep 原始行必须由点击详情后的定位接口读取')
assert.match(runtimeRouteSource, /runtimeLogsRouter\.get\('\/grep-detail'/, '后端必须提供 grep 增量详情端点')
assert.match(runtimeRouteSource, /type: 'get_runtime_log_detail_delta'/, '索引详情路由只应读取 rawJson 增量')
const runtimeDetailStateSource = source('../../views/runtime-logs/useRuntimeLogDetailState.ts')
assert.match(runtimeDetailStateSource, /selectedLog\.value = \{ \.\.\.record, \.\.\.detail \}/, '索引详情必须把行摘要与详情增量合并')
assert.match(runtimeDetailStateSource, /selectedGrepItem\.value = \{ \.\.\.record, \.\.\.detail \}/, 'grep 详情必须把行摘要与原始行增量合并')

console.log('日志加载回归通过：运行日志首屏无 runtime 请求，facets、grep options 与两类原文详情均按交互增量读取')
