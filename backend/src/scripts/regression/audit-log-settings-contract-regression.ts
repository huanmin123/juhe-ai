import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import { fixedAuditLogSettings, readAuditLogSettings } from '../../modules/audit-logs/audit-log-settings.js'

const auditDesignDoc = readFileSync(new URL('../../../../docs/functions/原始审计日志设计.md', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('../../modules/audit-logs/audit-log-settings.ts', import.meta.url), 'utf8')
const queueSource = readFileSync(new URL('../../modules/audit-logs/audit-log-queue.service.ts', import.meta.url), 'utf8')
const settings = readAuditLogSettings()

assert.equal(settings, fixedAuditLogSettings, '审计日志设置应直接返回固定配置对象')
assert.equal(settings.enabled, true, '原始审计日志应作为固定排障能力启用')
assert.equal(settings.batchSize, 500, '审计日志 worker 单批写入需要支撑 50 并发真实网关流量')
assert.equal(settings.queueMaxItems, 50000, '审计日志 worker 本地队列请求数必须支撑 50 并发短期写入波峰')
assert.equal(settings.queueMaxBytes, 256 * 1024 * 1024, '审计日志 worker 本地队列字节数必须按轻量部署控制在固定硬上限内')
assert.equal(settings.successHotRetentionHours, 1, '普通成功请求最近内容必须固定保留 1 小时')
assert.equal(Object.hasOwn(settings, 'fullBodyCapture'), false, '审计设置不应再暴露临时全量捕获配置')

runtimeConfig.runtimeMode = 'performance'
runtimeConfig.databaseDriver = 'postgres'
const performanceSettings = readAuditLogSettings()
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.databaseDriver = 'sqlite'
assert.equal(performanceSettings.successSampleRate, 0.03, '高性能 PostgreSQL 模式成功审计应降为 3% 抽样，避免审计尾部拖垮网关')
assert.equal(performanceSettings.successHotRetentionHours, 0, '高性能 PostgreSQL 模式不启用成功审计热窗口，失败审计仍全量保留')

assert(auditDesignDoc.includes('| `batchSize` | `500` |'), '原始审计日志设计文档必须声明 batchSize 固定为 500')
assert(auditDesignDoc.includes('| `queueMaxItems` | `50000` |'), '原始审计日志设计文档必须声明 queueMaxItems 固定硬上限')
assert(auditDesignDoc.includes('| `queueMaxBytes` | `256MB` |'), '原始审计日志设计文档必须声明 queueMaxBytes 固定硬上限')
assert(auditDesignDoc.includes('| `successHotRetentionHours` | `1` |'), '原始审计日志设计文档必须声明成功请求 1 小时热保留')
assert(!auditDesignDoc.includes('Number.MAX_SAFE_INTEGER'), '原始审计日志设计文档不能再把 worker 队列描述为无限制')
assert(!auditDesignDoc.includes('fullBodyCapture'), '原始审计日志设计文档不应再暴露临时全量捕获字段')

assert(!settingsSource.includes('normalizeAuditFullBodyCaptureConfig'), '审计设置模块不应再保留临时全量捕获解析函数')
assert(!settingsSource.includes('setAuditLogFullBodyCaptureConfig'), '审计设置模块不应再保留临时全量捕获写入函数')
assert(queueSource.includes('const auditLogScheduledFlushMaxBatches = 20'), '审计日志定时 flush 必须支持有限连续 drain，避免高并发下单 batch 追不上')
assert(queueSource.includes('flushAuditLogQueueAsync({ drain: true, maxBatches: auditLogScheduledFlushMaxBatches })'), '审计日志定时 flush 必须使用 drain 模式追赶积压')
const backgroundIpcSource = readFileSync(new URL('../../modules/background/background-ipc.ts', import.meta.url), 'utf8')
assert(backgroundIpcSource.includes('function coalesceAuditLogMessage'), 'server 到 ingest-worker 的 audit IPC 消息必须支持合并，避免 50 并发下大量单条 IPC 消息排队')
assert(backgroundIpcSource.includes("message.type === 'background_worker_audit_logs'"), 'background IPC 必须识别审计日志消息并走合并路径')

console.log('审计日志设置契约回归通过：固定审计配置保留 1 小时最近内容，不再暴露临时全量捕获开关')
