import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { AuditLogDetail, AuditLogSummary } from '../../types/domain'
import { useAuditLogDetailPayload } from '../../views/audit-logs/useAuditLogDetailPayload'

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

const auditViewSource = source('../../views/audit-logs/AuditLogsView.vue')
const auditModeBridgeSource = source('../../views/audit-logs/useAuditLogModeBridge.ts')
const auditPayloadStateSource = source('../../views/audit-logs/useAuditLogDetailPayload.ts')
const auditPayloadDetailsSource = source('../../views/audit-logs/auditPayloadDetails.ts')
const auditDetailDrawerSource = source('../../views/audit-logs/AuditLogDetailDrawer.vue')
const runtimeViewSource = source('../../views/runtime-logs/RuntimeLogsView.vue')
const runtimePageContentSource = source('../../views/runtime-logs/RuntimeLogPageContent.vue')
const runtimeFilterToolbarSource = source('../../views/runtime-logs/RuntimeLogFilterToolbar.vue')
const runtimeIndexStateSource = source('../../views/runtime-logs/useRuntimeLogIndexSearchState.ts')
const runtimeFacetsStateSource = source('../../views/runtime-logs/useRuntimeLogFacetsState.ts')
const logsApiSource = source('../../api/domains/logs.ts')
const runtimeTypesSource = source('../../types/domain/runtime-logs.ts')
const runtimeRouteSource = source('../../../../backend/src/modules/runtime-logs/runtime-logs.routes.ts')
const dbServiceIpcSource = source('../../../../backend/src/modules/db-service/db-service-ipc.ts')

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
assert.match(
  auditViewSource,
  /scheduleAuditRuntimeRefresh/,
  '审计运行态应在列表首屏之外通过空闲任务补充'
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
assert.match(runtimeViewSource, /scheduleRuntimeStatusRefresh/, '运行日志重量运行态应在首屏之外空闲补充')
assert.match(runtimeViewSource, /loadRuntimeLogRuntime\(true\)/, '页面重新激活时应刷新而不是永久复用旧运行态')
assert.match(runtimeFacetsStateSource, /catch \(error\)[\s\S]*runtime\.value = unavailableRuntimeLogRuntime\(\)/, '运行态请求失败必须显式进入 unavailable，不能沿用旧健康状态或隐藏告警')
assert.match(logsApiSource, /runtime: \(\) => unwrap<RuntimeLogRuntime>/, '运行日志运行态应有独立 API')
assert.doesNotMatch(runtimeTypesSource, /RuntimeLogRuntime[\s\S]*?queueHealth:/, '运行日志告警接口类型不得携带完整队列指标')
assert.doesNotMatch(runtimeTypesSource, /RuntimeLogRuntime[\s\S]*?gatewayAccountSideEffects:/, '运行日志告警接口类型不得携带网关副作用明细')
const runtimeAvailabilityRouteSource = runtimeRouteSource.match(
  /runtimeLogsRouter\.get\('\/runtime'[\s\S]*?(?=\nexport function runtimeLogFileConsumerRuntimeDto)/
)?.[0] ?? ''
assert.match(runtimeAvailabilityRouteSource, /requestServerRuntimeLogAvailabilitySnapshot\(\)/, '运行日志运行态接口必须调用轻量 availability scope')
assert.doesNotMatch(runtimeAvailabilityRouteSource, /requestServerRuntimeSnapshot\(\)/, '运行日志运行态接口不得调用 full runtime snapshot')
const runtimeAvailabilityBuilderSource = dbServiceIpcSource.match(/async function buildServerRuntimeLogAvailabilitySnapshot[\s\S]*?\n}\n/)?.[0] ?? ''
assert.match(runtimeAvailabilityBuilderSource, /getIngestWorkerRuntimeLogAvailability/, '运行日志轻量运行态只应读取 ingest worker O(1) 可用性')
assert.doesNotMatch(runtimeAvailabilityBuilderSource, /requestIngestWorkerSnapshot/, '运行日志轻量运行态不得请求完整 ingest worker 快照')
assert.doesNotMatch(runtimeAvailabilityBuilderSource, /requestStatsWorkerSnapshot|requestOpsWorkerSnapshot/, '运行日志轻量运行态不得读取无关 worker 快照')
assert.doesNotMatch(runtimeAvailabilityBuilderSource, /import\([^)]*(audit|account-concurrency|high-concurrency)/, '运行日志轻量运行态不得加载审计、账户并发或高并发队列模块')
assert.match(dbServiceIpcSource, /record\.scope === 'runtime_logs'/, '父进程接收端必须保留 runtime_logs scope，不能降级为 full')
assert.doesNotMatch(runtimeAvailabilityBuilderSource, /snapshotGatewayAccountRuntimeAvailability/, '运行日志轻量运行态不得遍历并复制全部账户运行态')

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

const detailRequests = new Map<string, Deferred<AuditLogDetail>>()
const staleErrors: string[] = []
const detailState = useAuditLogDetailPayload({
  loadDetail: (id) => {
    const request = deferred<AuditLogDetail>()
    detailRequests.set(id, request)
    return request.promise
  },
  reportError: (text) => staleErrors.push(text)
})
const detailA = detailState.openDetail({ id: 'audit-race-a' } as AuditLogSummary)
const detailB = detailState.openDetail({ id: 'audit-race-b' } as AuditLogSummary)
detailRequests.get('audit-race-a')?.reject(new Error('stale detail failure'))
detailRequests.get('audit-race-b')?.resolve({ id: 'audit-race-b' } as AuditLogDetail)
await Promise.all([detailA, detailB])
assert.equal(detailState.detail.value?.id, 'audit-race-b', '旧详情响应不得覆盖新详情')
assert.deepEqual(staleErrors, [], '过期详情失败不得在新详情上弹错')

const payloadFailure = deferred<never>()
const payloadErrors: string[] = []
const payloadState = useAuditLogDetailPayload({
  loadDetail: async (id) => ({ id } as AuditLogDetail),
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
assert.match(logsApiSource, /grepOptions: \(\) => unwrap<RuntimeLogGrepRuntime>/, 'grep 文件范围应有独立按需 API')
assert.match(runtimeViewSource, /loadRuntimeLogGrepOptions/, '只有 grep 模式才应加载文件范围选项')
const facetsType = runtimeTypesSource.match(/export interface RuntimeLogFacets \{[\s\S]*?\n\}/)?.[0] ?? ''
assert.doesNotMatch(facetsType, /grep:/, '索引 facets 不得夹带 grep 文件扫描结果')

assert.doesNotMatch(
  auditPayloadStateSource,
  /while\s*\(/,
  '点击审计 payload 后不得循环拉取完整正文'
)
assert.match(auditPayloadStateSource, /loadNextPayloadWindow/, '审计 payload 应提供显式下一段加载')
const openAuditDetailSource = auditPayloadStateSource.match(/async function openDetail[\s\S]*?async function loadPayload/)?.[0] ?? ''
assert.match(openAuditDetailSource, /payloadRequestId \+= 1/, '切换审计详情时必须作废上一条记录的 payload 请求')
assert.match(openAuditDetailSource, /detail\.value = undefined/, '加载新审计详情时必须立即隐藏旧详情，避免旧 payload 操作串页')
assert.match(auditPayloadStateSource, /bodyBytesReturned <= 0/, '下一段未读取到字节时必须停止继续加载')
assert.match(auditPayloadStateSource, /bodyNextOffset <= requestedOffset/, '下一段 offset 未前进时必须停止继续加载')
assert.match(auditPayloadDetailsSource, /headersIncluded: current\.headersIncluded/, '窗口合并应保留首段 Headers 状态')
assert.match(auditDetailDrawerSource, /加载下一段/, '审计详情应提供用户可见的下一段按钮')
assert.match(auditDetailDrawerSource, /load-next-payload/, '下一段按钮必须通过独立事件触发')

const grepItemType = runtimeTypesSource.match(/export interface RuntimeLogGrepItem \{[\s\S]*?\n\}/)?.[0] ?? ''
assert.doesNotMatch(grepItemType, /rawJson:/, 'grep 命中项不得重复返回 rawJson 和 line')
assert.match(grepItemType, /line:/, 'grep 命中项应保留单份原始行')

console.log('日志渐进式加载回归通过：运行态、facets、审计正文和 grep 原文均按需读取')
