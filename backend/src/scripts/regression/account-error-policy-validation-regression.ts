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

assert.equal(validateAccountErrorHandlingRules([{ name: '429', status_codes: [429], action: 'temp_unschedulable' }]).valid, true)
assert.equal(validateAccountErrorHandlingRules([{ name: '200', status_codes: [200], action: 'temp_unschedulable' }]).valid, false)
assert.equal(validateAccountErrorHandlingRules([{ name: 'error code 200', error_codes: ['200'], action: 'temp_unschedulable' }]).valid, false)
assert.equal(validateAccountErrorHandlingRules([{ name: '2xx', status_codes: '2xx', action: 'temp_unschedulable' }]).valid, false)
assert.equal(validateAccountErrorHandlingRules([{ name: 'range', match: { status_codes: '200-299' }, action: 'temp_unschedulable' }]).valid, false)
assert.equal(validateAccountErrorHandlingRules([{ enabled: false, name: 'disabled 200', status_codes: [200], action: 'temp_unschedulable' }]).valid, true)
assert.equal(validateAccountCredentialsErrorHandlingRules({ error_handling_rules: [{ name: '201', status_codes: [201] }] }).valid, false)

const decision = decideAccountErrorPolicy({
  id: 'account_error_policy_validation',
  providerCode: 'openai',
  type: 'api_key',
  credentials: {
    error_handling_rules: [
      { name: 'manual 200', status_codes: [200], action: 'temp_unschedulable', duration_minutes: 5 }
    ]
  },
  status: 'active'
}, 200, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"message":"failed"}}'), settings)

assert.equal(decision, undefined, '运行时不应对 2xx 状态码命中账号错误策略')

console.log('account error policy validation regression passed')
