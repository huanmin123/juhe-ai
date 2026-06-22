import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { fixedAuditLogSettings, readAuditLogSettings } from '../../modules/audit-logs/audit-log-settings.js'

const auditDesignDoc = readFileSync(new URL('../../../../docs/functions/原始审计日志设计.md', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('../../modules/audit-logs/audit-log-settings.ts', import.meta.url), 'utf8')
const settings = readAuditLogSettings()

assert.equal(settings, fixedAuditLogSettings, '审计日志设置应直接返回固定配置对象')
assert.equal(settings.enabled, true, '原始审计日志应作为固定排障能力启用')
assert.equal(settings.queueMaxItems, 5000, '审计日志 worker 本地队列请求数必须有固定硬上限')
assert.equal(settings.queueMaxBytes, 128 * 1024 * 1024, '审计日志 worker 本地队列字节数必须按小内存部署控制在固定硬上限内')
assert.equal(settings.successHotRetentionHours, 1, '普通成功请求最近内容必须固定保留 1 小时')
assert.equal(Object.hasOwn(settings, 'fullBodyCapture'), false, '审计设置不应再暴露临时全量捕获配置')

assert(auditDesignDoc.includes('| `queueMaxItems` | `5000` |'), '原始审计日志设计文档必须声明 queueMaxItems 固定硬上限')
assert(auditDesignDoc.includes('| `queueMaxBytes` | `128MB` |'), '原始审计日志设计文档必须声明 queueMaxBytes 固定硬上限')
assert(auditDesignDoc.includes('| `successHotRetentionHours` | `1` |'), '原始审计日志设计文档必须声明成功请求 1 小时热保留')
assert(!auditDesignDoc.includes('Number.MAX_SAFE_INTEGER'), '原始审计日志设计文档不能再把 worker 队列描述为无限制')
assert(!auditDesignDoc.includes('fullBodyCapture'), '原始审计日志设计文档不应再暴露临时全量捕获字段')

assert(!settingsSource.includes('normalizeAuditFullBodyCaptureConfig'), '审计设置模块不应再保留临时全量捕获解析函数')
assert(!settingsSource.includes('setAuditLogFullBodyCaptureConfig'), '审计设置模块不应再保留临时全量捕获写入函数')

console.log('审计日志设置契约回归通过：固定审计配置保留 1 小时最近内容，不再暴露临时全量捕获开关')
