import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { parseAuditLogRuntimeConfig, runtimeConfig } from '../../config/runtime.js'
import { auditSuccessRetentionCutoffIso } from '../../modules/audit-logs/audit-log-retention-policy.js'
import { summarizeAuditPayloadForLimit } from '../../modules/audit-logs/audit-payload-summary.js'
import type { AuditLogPayloadInput } from '../../storage/audit-log-types.js'
import { fixedAuditLogSettings, readAuditLogSettings } from '../../modules/audit-logs/audit-log-settings.js'

const auditDesignDoc = readFileSync(new URL('../../../../docs/functions/原始审计日志设计.md', import.meta.url), 'utf8')
const runtimeSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
const dockerCompose = readFileSync(new URL('../../../../docker/compose.yml', import.meta.url), 'utf8')
const performanceCompose = readFileSync(new URL('../../../../docker/compose.performance.yml', import.meta.url), 'utf8')
const dockerEntrypoint = readFileSync(new URL('../../../../docker/entrypoint.sh', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('../../modules/audit-logs/audit-log-settings.ts', import.meta.url), 'utf8')
const queueSource = readFileSync(new URL('../../modules/audit-logs/audit-log-queue.service.ts', import.meta.url), 'utf8')
const transportServiceSource = readFileSync(new URL('../../modules/audit-logs/audit-log-transport.service.ts', import.meta.url), 'utf8')
const settings = readAuditLogSettings()

assert.equal(settings, fixedAuditLogSettings, '审计日志设置应直接返回固定配置对象')
assert.equal(settings.enabled, runtimeConfig.auditLog.enabled, '审计启用状态必须来自 runtimeConfig.auditLog.enabled')
assert.equal(parseAuditLogRuntimeConfig({}).enabled, true, '未配置总开关时审计默认启用')
for (const value of ['true', '1', 'yes', 'on'] as const) {
  assert.equal(parseAuditLogRuntimeConfig({ JUHE_AI_AUDIT_LOG_ENABLED: value }).enabled, true, `${value} 应启用审计`)
}
for (const value of ['false', '0', 'no', 'off'] as const) {
  assert.equal(parseAuditLogRuntimeConfig({ JUHE_AI_AUDIT_LOG_ENABLED: value }).enabled, false, `${value} 应关闭审计`)
}
assert.throws(
  () => parseAuditLogRuntimeConfig({ JUHE_AI_AUDIT_LOG_ENABLED: 'disabled' }),
  /JUHE_AI_AUDIT_LOG_ENABLED/,
  '非法总开关值必须启动失败'
)
for (const value of ['', '   '] as const) {
  assert.throws(
    () => parseAuditLogRuntimeConfig({ JUHE_AI_AUDIT_LOG_ENABLED: value }),
    /JUHE_AI_AUDIT_LOG_ENABLED/,
    '审计总开关显式空值必须启动失败，不能静默回退为启用'
  )
}
assert.equal(settings.batchSize, 500, '审计日志 worker 单批写入需要支撑 50 并发真实网关流量')
assert.equal(settings.queueMaxItems, 50000, '审计日志 worker 本地队列请求数必须支撑 50 并发短期写入波峰')
assert.equal(settings.queueMaxBytes, 256 * 1024 * 1024, '审计日志 worker 本地队列字节数必须按轻量部署控制在固定硬上限内')
assert.equal(settings.successHotRetentionHours, 1, '普通成功请求最近内容必须固定保留 1 小时')
assert.equal(settings.successRetentionDays, 3, '成功长期样本默认保留 3 天')
assert.equal(settings.problemRetentionDays, 7, '问题链路和错误聚合组默认统一保留 7 天')
assert.equal(settings.successFullBodyLimitBytes, 512 * 1024, '成功正文默认完整保留 512KB')
assert.equal(settings.problemFullBodyLimitBytes, 2 * 1024 * 1024, '问题正文默认完整保留 2MB')
assert.equal(Object.hasOwn(settings, 'fullBodyCapture'), false, '审计设置不应再暴露临时全量捕获配置')

runtimeConfig.runtimeMode = 'performance'
runtimeConfig.databaseDriver = 'postgres'
const performanceSettings = readAuditLogSettings()
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.databaseDriver = 'sqlite'
assert.equal(performanceSettings.successSampleRate, 0.1, '高性能 PostgreSQL 模式成功审计长期采样必须和单机模式一致')
assert.equal(performanceSettings.successHotRetentionHours, 1, '高性能 PostgreSQL 模式成功审计最近 1 小时热保留必须和单机模式一致')

const customized = parseAuditLogRuntimeConfig({
  JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS: '48',
  JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE: '0.0125',
  JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS: '3',
  JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS: '14',
  JUHE_AI_AUDIT_LOG_SUCCESS_FULL_BODY_LIMIT_KB: '0',
  JUHE_AI_AUDIT_LOG_PROBLEM_FULL_BODY_LIMIT_KB: '0'
})
assert.equal(customized.successHotRetentionHours, 48)
assert.equal(customized.successSampleRate, 0.0125)
assert.equal(customized.problemRetentionDays, 14)
assert.equal(customized.successFullBodyLimitBytes, 0)
assert.equal(customized.problemFullBodyLimitBytes, 0)
const successAuditDisabled = parseAuditLogRuntimeConfig({
  JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS: '0',
  JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE: '0',
  JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS: '0'
})
assert.equal(successAuditDisabled.successHotRetentionHours, 0)
assert.equal(successAuditDisabled.successSampleRate, 0)
assert.equal(successAuditDisabled.successRetentionDays, 0)
assert.equal(successAuditDisabled.problemRetentionDays, 7, '关闭成功审计不能影响问题链路保留')
const hotOnlySuccessAudit = parseAuditLogRuntimeConfig({
  JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS: '6',
  JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE: '0',
  JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS: '0'
})
assert.equal(hotOnlySuccessAudit.successHotRetentionHours, 6, '关闭长期采样时仍应允许保留成功热窗口')
assert.equal(
  auditSuccessRetentionCutoffIso(Date.parse('2026-07-14T12:00:00.000Z'), 6, 0),
  '2026-07-14T06:00:00.000Z',
  '长期采样关闭时统一保留清理不得越过成功热窗口'
)
assert.throws(() => parseAuditLogRuntimeConfig({
  JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE: '0',
  JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS: '3'
}), /必须同时为 0 或同时大于 0/)
assert.throws(() => parseAuditLogRuntimeConfig({
  JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE: '0.1',
  JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS: '0'
}), /必须同时为 0 或同时大于 0/)
assert.throws(() => parseAuditLogRuntimeConfig({ JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE: '0.00001' }), /最多 4 位小数/)
assert.throws(() => parseAuditLogRuntimeConfig({
  JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS: '49',
  JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS: '2'
}), /必须覆盖/)
const hashOnlyPayload: Omit<AuditLogPayloadInput, 'sequenceIndex'> = {
  partType: 'gateway_error' as const,
  body: 'problem-body',
  contentType: 'text/plain'
}
assert.equal(summarizeAuditPayloadForLimit(hashOnlyPayload, 0), true, '正文上限为 0 时必须应用 hash_only')
assert.equal(hashOnlyPayload.captureStatus, 'hash_only')
assert.equal(hashOnlyPayload.body, undefined)
assert.match(hashOnlyPayload.bodySha256 ?? '', /^[a-f0-9]{64}$/)
assert.equal(hashOnlyPayload.rawBodySizeBytes, 12)

const smallLimitBody = Buffer.alloc(64 * 1024, 0x41)
smallLimitBody.fill(0x42, smallLimitBody.byteLength / 2)
const smallLimitPayload: Omit<AuditLogPayloadInput, 'sequenceIndex'> = {
  partType: 'gateway_error' as const,
  body: smallLimitBody,
  contentType: 'application/octet-stream'
}
const smallFullBodyLimitBytes = 1024
assert.equal(summarizeAuditPayloadForLimit(smallLimitPayload, smallFullBodyLimitBytes), true)
const smallLimitEncoded = String(smallLimitPayload.body ?? '')
const smallLimitSummary = JSON.parse(smallLimitEncoded) as Record<string, unknown>
const smallLimitHead = Buffer.from(String(smallLimitSummary.headBase64 ?? ''), 'base64')
const smallLimitTail = Buffer.from(String(smallLimitSummary.tailBase64 ?? ''), 'base64')
assert.equal(smallLimitHead.byteLength + smallLimitTail.byteLength, smallFullBodyLimitBytes, '小限额摘要的头尾原文窗口总量不能超过配置上限')
assert(smallLimitHead.every((value) => value === 0x41), '小限额摘要头部窗口必须来自原文头部')
assert(smallLimitTail.every((value) => value === 0x42), '小限额摘要尾部窗口必须来自原文尾部且不能与头部重叠')
assert(Buffer.byteLength(smallLimitEncoded, 'utf8') <= smallFullBodyLimitBytes * 2, '小限额摘要编码后大小必须保持有界，不能因重复头尾而膨胀数倍')

assert(auditDesignDoc.includes('| `batchSize` | `500` |'), '原始审计日志设计文档必须声明 batchSize 固定为 500')
assert(auditDesignDoc.includes('| `queueMaxItems` | `50000` |'), '原始审计日志设计文档必须声明 queueMaxItems 固定硬上限')
assert(auditDesignDoc.includes('| `queueMaxBytes` | `256MB` |'), '原始审计日志设计文档必须声明 queueMaxBytes 固定硬上限')
assert(auditDesignDoc.includes('| `successHotRetentionHours` | `1` |'), '原始审计日志设计文档必须声明成功请求 1 小时热保留')
assert(!settingsSource.includes('successSampleRate: 0.02'), '审计设置模块不能按高性能模式降级成功审计长期采样率')
assert(!settingsSource.includes('successHotRetentionHours: 0'), '审计设置模块不能按高性能模式关闭成功审计热窗口')
assert(!runtimeSource.includes('JUHE_AI_AUDIT_FULL_BODY_CAPTURE_ENABLED'), '运行时配置不能再读取审计正文捕获环境变量')
assert(!dockerCompose.includes('JUHE_AI_AUDIT_FULL_BODY_CAPTURE_ENABLED'), '轻量 Docker Compose 不能再暴露审计正文捕获环境变量')
assert(!performanceCompose.includes('JUHE_AI_AUDIT_FULL_BODY_CAPTURE_ENABLED'), '高性能 Docker Compose 不能再暴露审计正文捕获环境变量')
assert(performanceCompose.includes('JUHE_AI_POSTGRES_STATEMENT_TIMEOUT_MS: ${JUHE_AI_POSTGRES_STATEMENT_TIMEOUT_MS:-30000}'), '高性能 Docker Compose 必须传递 PostgreSQL statement_timeout')
assert(performanceCompose.includes('JUHE_AI_POSTGRES_LOCK_TIMEOUT_MS: ${JUHE_AI_POSTGRES_LOCK_TIMEOUT_MS:-2000}'), '高性能 Docker Compose 必须传递 PostgreSQL lock_timeout')
assert(performanceCompose.includes('JUHE_AI_POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT_MS: ${JUHE_AI_POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT_MS:-30000}'), '高性能 Docker Compose 必须传递 PostgreSQL idle_in_transaction_session_timeout')
assert(!dockerEntrypoint.includes('JUHE_AI_AUDIT_FULL_BODY_CAPTURE_ENABLED'), 'Docker entrypoint 不能再导出审计正文捕获环境变量')
assert(!auditDesignDoc.includes('Number.MAX_SAFE_INTEGER'), '原始审计日志设计文档不能再把 worker 队列描述为无限制')
assert(!auditDesignDoc.includes('fullBodyCapture'), '原始审计日志设计文档不应再暴露临时全量捕获字段')
assert(transportServiceSource.includes('workerData:'), '审计 transport worker 必须通过 workerData 接收正文保全快照')
assert(transportServiceSource.includes('problemFullBodyLimitBytes: settings.problemFullBodyLimitBytes'), '问题正文限额必须传入 transport worker')

assert(!settingsSource.includes('normalizeAuditFullBodyCaptureConfig'), '审计设置模块不应再保留临时全量捕获解析函数')
assert(!settingsSource.includes('setAuditLogFullBodyCaptureConfig'), '审计设置模块不应再保留临时全量捕获写入函数')
assert(!settingsSource.includes('process.env'), '审计设置必须从 runtimeConfig 单一入口读取，不能绕过 .env overlay')
assert(runtimeSource.includes('auditLog: auditLogRuntimeConfig()'), 'runtimeConfig 必须统一解析审计保全环境变量')
assert(queueSource.includes('const auditLogScheduledFlushMaxBatches = runtimeConfig.background.auditLogScheduledFlushMaxBatches'), '审计日志定时 flush 必须从运行配置读取有限连续 drain 上限，避免高并发下单 batch 追不上')
assert(queueSource.includes('flushAuditLogQueueAsync({ drain: true, retryOnFailure: false })') || queueSource.includes('flushAuditLogQueueAsync({ drain: true, maxBatches: auditLogScheduledFlushMaxBatches })'), '审计日志定时 flush 必须使用 drain 模式追赶积压')
const backgroundIpcSource = readFileSync(new URL('../../modules/background/background-ipc.ts', import.meta.url), 'utf8')
assert(backgroundIpcSource.includes('function coalesceAuditLogMessage'), 'server 到 ingest-worker 的 audit IPC 消息必须支持合并，避免 50 并发下大量单条 IPC 消息排队')
assert(backgroundIpcSource.includes("message.type === 'background_worker_audit_logs'"), 'background IPC 必须识别审计日志消息并走合并路径')

console.log('审计日志设置契约回归通过：默认保留 1 小时最近内容，允许显式关闭成功审计且不影响问题链路')
