import assert from 'node:assert/strict'

import {
  validateAccountCredentialsErrorHandlingRules,
  validateAccountErrorHandlingRules
} from '../../modules/accounts/account-error-policy-validation.js'
import { decideAccountErrorPolicy, type GatewaySettings } from '../../modules/gateway/account-error-policy.service.js'

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

assert.equal(validateAccountErrorHandlingRules([tempRule({ name: '429', status_codes: [429] })]).valid, true)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: '200', status_codes: [200] })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'error code 200', error_codes: ['200'] })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: '2xx', status_codes: '2xx' })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'range', status_codes: '200-299' })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'legacy duration', durationMinutes: 5 })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ enabled: false, name: 'disabled 429', status_codes: [429] })]).valid, true)
assert.equal(validateAccountCredentialsErrorHandlingRules({ error_handling_rules: [{ name: '201', status_codes: [201] }] }).valid, false)

const decision = decideAccountErrorPolicy({
  id: 'account_error_policy_validation',
  providerCode: 'gpt',
  type: 'api_key',
  credentials: {
    error_handling_rules: [
      tempRule({ name: 'manual 200', status_codes: [200] })
    ]
  },
  status: 'active'
}, 200, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"message":"failed"}}'), settings)

assert.equal(decision, undefined, '运行时不应对 2xx 状态码命中账号错误策略')

console.log('account error policy validation regression passed')

function tempRule(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    enabled: true,
    name: '临时避让规则',
    priority: 10,
    status_codes: [429],
    action: 'temp_unschedulable',
    ...overrides
  }
}
