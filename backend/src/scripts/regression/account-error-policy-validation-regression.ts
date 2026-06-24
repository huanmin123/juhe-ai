import assert from 'node:assert/strict'

import {
  validateAccountCredentialsErrorHandlingRules,
  validateAccountErrorHandlingRules
} from '../../modules/accounts/account-error-policy-validation.js'
import { decideAccountErrorPolicy, type GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import {
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../../domain/provider-protocol.js'

const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 120,
  streamIdleTimeoutSeconds: 30,
  streamClientTotalWaitTimeoutSeconds: 270,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}

assert.equal(validateAccountErrorHandlingRules([tempRule({ name: '429', status_codes: [429] })]).valid, true)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: '200', status_codes: [200] })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'error code 200', error_codes: ['200'] })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'type text 200', error_types: ['200'] })]).valid, true)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'keyword text 200', keywords: ['200'] })]).valid, true)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: '2xx', status_codes: '2xx' })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'range', status_codes: '200-299' })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ name: 'legacy duration', durationMinutes: 5 })]).valid, false)
assert.equal(validateAccountErrorHandlingRules([tempRule({ enabled: false, name: 'disabled 429', status_codes: [429] })]).valid, true)
assert.equal(validateAccountCredentialsErrorHandlingRules({ error_handling_rules: [{ name: '201', status_codes: [201] }] }).valid, false)

const textKeywordDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_keyword_validation',
  providerCode: 'openai',
  type: 'api_key',
  credentials: {
    error_handling_rules: [
      tempRule({ name: '文本 200', status_codes: [429], keywords: ['200'] })
    ]
  },
  status: 'active'
}, 429, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"message":"retry after 200 seconds"}}'), settings)

assert.equal(textKeywordDecision?.ruleName, '文本 200', '关键词中的普通文本数字 200 应允许参与非 2xx 错误匹配')

const successDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_validation',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  credentials: {
    error_handling_rules: [
      tempRule({ name: 'manual 200', status_codes: [200] })
    ]
  },
  status: 'active'
}, 200, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"message":"failed"}}'), settings)

assert.equal(successDecision, undefined, '运行时不应对 2xx 状态码命中账号错误策略')

const anthropicErrorTypeAsCodeDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_anthropic_error_type',
  providerCode: 'anthropic',
  protocolCode: ANTHROPIC_PROTOCOL_CODE,
  protocolVersion: ANTHROPIC_PROTOCOL_VERSION,
  type: 'api_key',
  credentials: {
    error_handling_rules: [
      tempRule({ name: 'Anthropic overloaded', status_codes: [503], error_codes: ['overloaded_error'] })
    ]
  },
  status: 'active'
}, 503, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"type":"error","error":{"type":"overloaded_error","message":"mock overloaded"}}'), settings)

assert.equal(anthropicErrorTypeAsCodeDecision?.ruleName, 'Anthropic overloaded', 'Anthropic 协议应把官方 error.type 暴露给错误策略 code 匹配')

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
