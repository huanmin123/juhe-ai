import assert from 'node:assert/strict'

import { decideRequestErrorPolicy, type GatewaySettings } from '../../modules/gateway/request-error-policy.service.js'
import type { ErrorPolicySummary } from '../../storage/error-policy.repository.js'

const settings: GatewaySettings = {
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 180,
  streamIdleTimeoutSeconds: 60,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 10
}

const account = {
  id: 'request_error_policy_validation',
  providerCode: 'gpt',
  type: 'api_key',
  credentials: {},
  status: 'active' as const
}

const policies: ErrorPolicySummary[] = [
  policy('global_429', '全局 429 临时避让', 'global', { statusCodes: [429] }, 'temp_unschedulable', 10),
  policy('provider_429', 'GPT 429 限流', 'provider', { statusCodes: [429] }, 'rate_limited', 10, {
    protocolCode: 'openai',
    providerCode: 'gpt',
    resetStrategy: 'daily',
    dailyResetHour: 0
  }),
  policy('client_429', 'Codex 429 停用', 'client', { statusCodes: [429] }, 'error_disabled', 10, {
    protocolCode: 'openai',
    clientProfile: 'codex'
  }),
  policy('model_429', 'GPT-4 429 只切号', 'model', { statusCodes: [429] }, 'retry_next', 10, {
    protocolCode: 'openai',
    providerCode: 'gpt',
    modelPattern: 'gpt-4',
    modelMatchType: 'prefix'
  })
]

assert.equal(
  decideRequestErrorPolicy(account, 200, jsonHeaders(), Buffer.from('{"error":{"message":"ok"}}'), settings, {
    policies: [policy('invalid_200', '非法 200', 'global', { statusCodes: [200] }, 'error_disabled', 1)],
    context: { protocolCode: 'openai', providerCode: 'gpt', clientProfile: 'codex', model: 'gpt-4o' }
  }),
  undefined,
  '运行时不应对 2xx 状态码命中请求错误策略'
)

assert.deepEqual(
  decideRequestErrorPolicy(account, 429, jsonHeaders(), Buffer.from('{"error":{"message":"quota"}}'), settings, {
    policies,
    context: { protocolCode: 'openai', providerCode: 'gpt', clientProfile: 'codex', model: 'gpt-4o' }
  }),
  { action: 'retry_next', ruleName: 'GPT-4 429 只切号' },
  '模型层策略应覆盖客户端、供应商和全局层策略'
)

assert.deepEqual(
  decideRequestErrorPolicy(account, 429, jsonHeaders(), Buffer.from('{"error":{"message":"quota"}}'), settings, {
    policies,
    context: { protocolCode: 'openai', providerCode: 'gpt', clientProfile: 'codex', model: 'gpt-3.5' }
  }),
  { action: 'disable', ruleName: 'Codex 429 停用' },
  '客户端层策略应覆盖供应商和全局层策略'
)

const providerDecision = decideRequestErrorPolicy(account, 429, jsonHeaders(), Buffer.from('{"error":{"message":"quota"}}'), settings, {
  policies,
  context: { protocolCode: 'openai', providerCode: 'gpt', clientProfile: 'openai_standard', model: 'o3' }
})
assert.equal(providerDecision?.action, 'cooldown')
assert.equal(providerDecision?.cooldownStatus, 'rate_limited')
assert.equal(providerDecision?.ruleName, 'GPT 429 限流')

const globalDecision = decideRequestErrorPolicy(account, 429, jsonHeaders(), Buffer.from('{"error":{"message":"quota"}}'), settings, {
  policies,
  context: { protocolCode: 'openai', providerCode: 'openai', clientProfile: 'openai_standard', model: 'o3' }
})
assert.equal(globalDecision?.action, 'cooldown')
assert.equal(globalDecision?.cooldownStatus, 'temporary_unavailable')
assert.equal(globalDecision?.ruleName, '全局 429 临时避让')

console.log('request error policy validation regression passed')

function jsonHeaders(): Headers {
  return new Headers({ 'content-type': 'application/json' })
}

function policy(
  id: string,
  name: string,
  scopeType: ErrorPolicySummary['scopeType'],
  match: ErrorPolicySummary['match'],
  action: ErrorPolicySummary['action'],
  priority: number,
  patch: Partial<ErrorPolicySummary> = {}
): ErrorPolicySummary {
  return {
    id,
    editable: true,
    name,
    enabled: true,
    priority,
    scopeType,
    match,
    action,
    ...patch
  }
}
