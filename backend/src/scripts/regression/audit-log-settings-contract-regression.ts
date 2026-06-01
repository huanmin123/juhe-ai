import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const auditSettings = await import('../../modules/audit-logs/audit-log-settings.js')

const nowMs = Date.parse('2026-01-01T00:00:00.000Z')
const auditDesignDoc = readFileSync(new URL('../../../../docs/functions/原始审计日志设计.md', import.meta.url), 'utf8')

assert.equal(auditSettings.fixedAuditLogSettings.queueMaxItems, 5000, '审计日志 worker 本地队列请求数必须有固定硬上限')
assert.equal(auditSettings.fixedAuditLogSettings.queueMaxBytes, 128 * 1024 * 1024, '审计日志 worker 本地队列字节数必须有固定硬上限')
assert(auditDesignDoc.includes('| `queueMaxItems` | `5000` |'), '原始审计日志设计文档必须声明 queueMaxItems 固定硬上限')
assert(auditDesignDoc.includes('| `queueMaxBytes` | `128MB` |'), '原始审计日志设计文档必须声明 queueMaxBytes 固定硬上限')
assert(!auditDesignDoc.includes('Number.MAX_SAFE_INTEGER'), '原始审计日志设计文档不能再把 worker 队列描述为无限制')

const valid = auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  includeSuccess: false,
  durationMinutes: 15
}, nowMs)
assert.equal(valid.enabled, true, '合法临时全量捕获配置应保持启用')
assert.equal(valid.expiresAt, '2026-01-01T00:15:00.000Z', 'durationMinutes 应按整数分钟计算过期时间')

const expired = auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  expiresAt: '2025-12-31T23:59:00Z'
}, nowMs)
assert.equal(expired.enabled, false, '已过期的临时全量捕获配置应立即关闭')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: 'true',
  scope: 'global'
} as never, nowMs), /enabled 必须是布尔值/, 'enabled 字符串不应被兼容为布尔值')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'legacy'
} as never, nowMs), /scope 无效/, '未知 scope 不应回退为 global')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'account',
  accountId: ''
}, nowMs), /请选择要定向捕获的 AI 账户/, 'account scope 下空 accountId 不应被静默清空')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  accountId: 123
} as never, nowMs), /accountId 必须是字符串/, 'accountId 非字符串不应被静默忽略')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  includeSuccess: 'true'
} as never, nowMs), /includeSuccess 必须是布尔值/, 'includeSuccess 字符串不应被兼容为布尔值')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  durationMinutes: 15.5
}, nowMs), /durationMinutes 必须是 1 到 1440 的整数/, 'durationMinutes 小数不应被截断')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  durationMinutes: 0
}, nowMs), /durationMinutes 必须是 1 到 1440 的整数/, 'durationMinutes 低于范围不应被夹紧')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  durationMinutes: 1441
}, nowMs), /durationMinutes 必须是 1 到 1440 的整数/, 'durationMinutes 超出范围不应被夹紧')

assert.throws(() => auditSettings.normalizeAuditFullBodyCaptureConfig({
  enabled: true,
  scope: 'global',
  expiresAt: '2026-02-31T00:00:00'
}, nowMs), /过期时间无效/, '不存在的日历日期不应被 Date 自动修正')

console.log('审计日志设置契约回归通过：worker 队列有固定硬上限，临时全量捕获非法字段不再兜底、截断或宽松解析')
