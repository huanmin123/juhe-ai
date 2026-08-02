import { strict as assert } from 'node:assert'

import type { UsageRecordSummary } from '../../src/types/domain'
import { apiKeyGroupOptionsProviderProtocolProfileId } from '../../src/views/api-keys/useApiKeyGroupOptions'
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

assert.equal(usageTrafficSourceText(hybridUsageRecord), '混合路由选型', '用量记录应识别混合路由选型来源')
assert.equal(usageTrafficSourceColor(hybridUsageRecord), 'blue', '用量记录混合路由选型应有独立标签颜色')

const hybridQualityUsageRecord: UsageRecordSummary = {
  id: 'usage_hybrid_quality',
  traceId: 'trace_hybrid_quality',
  trafficSource: 'hybrid_quality_scoring',
  stream: false,
  success: true,
  createdAt: '2026-06-21T00:00:00.000Z'
}

assert.equal(usageTrafficSourceText(hybridQualityUsageRecord), '回答质量复核', '用量记录应识别回答质量复核来源')
assert.equal(usageTrafficSourceColor(hybridQualityUsageRecord), 'purple', '用量记录回答质量复核应有独立标签颜色')

const runtimeRecoveryProbeUsageRecord: UsageRecordSummary = {
  id: 'usage_runtime_recovery_probe',
  traceId: 'trace_runtime_recovery_probe',
  trafficSource: 'runtime_recovery_probe',
  stream: false,
  success: true,
  createdAt: '2026-06-21T00:00:00.000Z'
}

assert.equal(usageTrafficSourceText(runtimeRecoveryProbeUsageRecord), '快速恢复检测', '用量记录应识别快速恢复检测来源')
assert.equal(usageTrafficSourceColor(runtimeRecoveryProbeUsageRecord), 'orange', '用量记录快速恢复检测应有独立标签颜色')

const cooldownRetestUsageRecord: UsageRecordSummary = {
  id: 'usage_cooldown_retest',
  traceId: 'trace_cooldown_retest',
  trafficSource: 'cooldown_retest',
  stream: false,
  success: true,
  createdAt: '2026-06-21T00:00:00.000Z'
}

assert.equal(usageTrafficSourceText(cooldownRetestUsageRecord), '冷却账户复测', '用量记录应识别冷却账户复测来源')
assert.equal(usageTrafficSourceColor(cooldownRetestUsageRecord), 'gold', '用量记录冷却账户复测应有独立标签颜色')

assert.equal(
  apiKeyGroupOptionsProviderProtocolProfileId({
    formContext: true,
    allowMixedProviderProtocolProfiles: false,
    formBindings: [{ providerProtocolProfileId: 'profile_gpt_openai_v1' }]
  }),
  'profile_gpt_openai_v1',
  '普通 API Key 分组选项应按已选协议档案收窄请求 scope'
)
assert.equal(
  apiKeyGroupOptionsProviderProtocolProfileId({
    formContext: true,
    allowMixedProviderProtocolProfiles: true,
    formBindings: [{ providerProtocolProfileId: 'profile_gpt_openai_v1' }]
  }),
  '',
  '混合路由 API Key 分组选项不能按单一协议档案收窄'
)
assert.match(providerEmptyDescriptionForScope(true), /智谱 GLM/, '供应商空态应包含已内置的 GLM')

console.log('网关来源与供应商前端契约回归通过：用量后台来源、API Key 协议档案 scope 和 GLM 空态均符合预期')
