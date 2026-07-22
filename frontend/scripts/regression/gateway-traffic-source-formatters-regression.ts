import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'

import type { AuditTrafficSource, UsageRecordSummary } from '../../src/types/domain'
import {
  trafficSourceColor as auditTrafficSourceColor,
  trafficSourceText as auditTrafficSourceText
} from '../../src/views/audit-logs/auditLogFormatters'
import { providerEmptyDescriptionForScope } from '../../src/views/providers/providerTableConfig'
import {
  trafficSourceColor as usageTrafficSourceColor,
  trafficSourceText as usageTrafficSourceText
} from '../../src/views/usage-records/usageRecordFormatters'

const hybridUsageRecord: UsageRecordSummary = {
  id: 'usage_hybrid',
  traceId: 'trace_hybrid',
  trafficSource: 'hybrid_scoring',
  stream: false,
  success: true,
  createdAt: '2026-06-21T00:00:00.000Z'
}

assert.equal(usageTrafficSourceText(hybridUsageRecord), '混合评分', '用量记录应识别混合评分来源')
assert.equal(usageTrafficSourceColor(hybridUsageRecord), 'blue', '用量记录混合评分应有独立标签颜色')

const hybridQualityUsageRecord: UsageRecordSummary = {
  id: 'usage_hybrid_quality',
  traceId: 'trace_hybrid_quality',
  trafficSource: 'hybrid_quality_scoring',
  stream: false,
  success: true,
  createdAt: '2026-06-21T00:00:00.000Z'
}

assert.equal(usageTrafficSourceText(hybridQualityUsageRecord), '混合质量评分', '用量记录应识别混合质量评分来源')
assert.equal(usageTrafficSourceColor(hybridQualityUsageRecord), 'purple', '用量记录混合质量评分应有独立标签颜色')

const runtimeRecoveryProbeUsageRecord: UsageRecordSummary = {
  id: 'usage_runtime_recovery_probe',
  traceId: 'trace_runtime_recovery_probe',
  trafficSource: 'runtime_recovery_probe',
  stream: false,
  success: true,
  createdAt: '2026-06-21T00:00:00.000Z'
}

assert.equal(usageTrafficSourceText(runtimeRecoveryProbeUsageRecord), '运行态恢复探针', '用量记录应识别运行态恢复探针来源')
assert.equal(usageTrafficSourceColor(runtimeRecoveryProbeUsageRecord), 'orange', '用量记录运行态恢复探针应有独立标签颜色')

const hybridAuditSource: AuditTrafficSource = 'hybrid_scoring'
assert.equal(auditTrafficSourceText(hybridAuditSource), '混合评分', '审计日志应识别混合评分来源')
assert.equal(auditTrafficSourceColor(hybridAuditSource), 'blue', '审计日志混合评分应有独立标签颜色')

const hybridQualityAuditSource: AuditTrafficSource = 'hybrid_quality_scoring'
assert.equal(auditTrafficSourceText(hybridQualityAuditSource), '混合质量评分', '审计日志应识别混合质量评分来源')
assert.equal(auditTrafficSourceColor(hybridQualityAuditSource), 'purple', '审计日志混合质量评分应有独立标签颜色')

const runtimeRecoveryProbeAuditSource: AuditTrafficSource = 'runtime_recovery_probe'
assert.equal(auditTrafficSourceText(runtimeRecoveryProbeAuditSource), '运行态恢复探针', '审计日志应识别运行态恢复探针来源')
assert.equal(auditTrafficSourceColor(runtimeRecoveryProbeAuditSource), 'orange', '审计日志运行态恢复探针应有独立标签颜色')

const legacyApiKeyGroupOptionsUrl = new URL('../../src/views/api-keys/useApiKeyGroupOptions.ts', import.meta.url)
assert.equal(existsSync(legacyApiKeyGroupOptionsUrl), false, '旧 API Key 分组选项 helper 必须删除')
const apiKeyModalSource = readFileSync(new URL('../../src/views/api-keys/ApiKeyEditModal.vue', import.meta.url), 'utf8')
const apiKeysApiSource = readFileSync(new URL('../../src/api/domains/apiKeys.ts', import.meta.url), 'utf8')
assert.match(apiKeyModalSource, /routeStrategyId/, 'API Key 表单必须由 routeStrategyId 选择当前路由 owner')
assert.doesNotMatch(apiKeyModalSource, /groupBindings/, 'API Key 表单不得恢复直接分组绑定')
assert.match(apiKeysApiSource, /routeStrategyId\?: string/, 'API Key API payload 必须只引用策略路由 ID')
assert.match(providerEmptyDescriptionForScope(true), /智谱 GLM/, '供应商空态应包含已内置的 GLM')

console.log('网关来源与供应商前端契约回归通过：混合评分来源、混合质量评分来源、API Key 策略路由 owner 和 GLM 空态均符合预期')
