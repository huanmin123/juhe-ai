import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

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
  accountCircuitConfirmationFailuresRequired: 2,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  textFirstResponseTimeoutSeconds: 120,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 1800,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 3600,
  imageRequestWallTimeoutSeconds: 3600,
  noAvailableAccountWaitTimeoutSeconds: 270,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}

const accountErrorPolicySource = readFileSync(new URL('../../modules/gateway/policy/account-error-policy.service.ts', import.meta.url), 'utf8')
assert.match(
  accountErrorPolicySource,
  /return requiredRfc3339Instant\(value, '账户运行态 observedAt'\)/,
  '账户错误策略 supplied observedAt 必须复用严格 RFC3339 边界'
)
assert.doesNotMatch(
  accountErrorPolicySource,
  /function normalizedRuntimeObservationAt[\s\S]*?Date\.parse\(/,
  '账户错误策略不得按本机时区解释 observedAt'
)

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
assert.equal(
  validateAccountErrorHandlingRules([tempRule({ source: 'system', inherited: true, editable: false })]).valid,
  false,
  '客户端不得把系统继承规则写入账户凭据'
)

const textKeywordDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_keyword_validation',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  credentials: {
    error_handling_rules: [
      tempRule({ name: '文本 200', status_codes: [429], keywords: ['200'] })
    ]
  },
  status: 'active'
}, 429, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"message":"retry after 200 seconds"}}'), settings)

assert.equal(textKeywordDecision?.action, 'cooldown', '用户显式账户错误策略命中后应允许改变账户状态')
assert.equal(textKeywordDecision?.cooldownStatus, 'temporary_unavailable')
assert.equal(textKeywordDecision?.ruleName, '文本 200')

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

assert.equal(anthropicErrorTypeAsCodeDecision?.action, 'cooldown', '显式规则应按当前协议解析后的错误码命中')
assert.equal(anthropicErrorTypeAsCodeDecision?.ruleName, 'Anthropic overloaded')

const unconfiguredDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_unconfigured',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  credentials: {},
  status: 'active'
}, 503, new Headers(), Buffer.from('failed'), settings)
assert.equal(unconfiguredDecision, undefined, '没有用户显式规则时不能由普通请求自动决定账户状态')

const systemQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_system_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  credentials: {
    error_handling_rules: [tempRule({ name: '账户自定义403重试', status_codes: [403], action: 'retry_next' })]
  },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_quota","message":"余额不足"}}'), settings)
assert.equal(systemQuotaDecision?.action, 'disable', '系统额度规则必须先于账户自定义规则执行')
assert.equal(systemQuotaDecision?.ruleSource, 'system')
assert.equal(systemQuotaDecision?.ruleName, '上游额度不足')

const systemQuotaBeforeInvalidLocalRule = decideAccountErrorPolicy({
  id: 'account_error_policy_invalid_local_rule',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  credentials: { error_handling_rules: [{ enabled: true, name: '' }] },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_quota"}}'), settings)
assert.equal(systemQuotaBeforeInvalidLocalRule?.ruleSource, 'system', '损坏的历史账户规则不能阻断系统额度保护')

for (const [name, statusCode, body, expected] of [
  ['额度码insufficient_user_quota', 403, '{"error":{"code":"insufficient_user_quota"}}', 'disable'],
  ['额度码insufficient_quota', 403, '{"error":{"code":"insufficient_quota"}}', 'disable'],
  ['额度码insufficient_balance', 403, '{"error":{"code":"insufficient_balance"}}', 'disable'],
  ['额度码quota_exceeded', 403, '{"error":{"code":"quota_exceeded"}}', 'disable'],
  ['额度码quota_exhausted', 403, '{"error":{"code":"quota_exhausted"}}', 'disable'],
  ['额度码wallet_balance_exhausted', 403, '{"error":{"code":"WALLET_BALANCE_EXHAUSTED"}}', 'disable'],
  ['额度码pre_consume_token_quota_failed', 403, '{"error":{"code":"pre_consume_token_quota_failed"}}', 'disable'],
  ['NewAPI包装额度码insufficient_user_quota', 403, '{"error":{"type":"new_api_error","code":"insufficient_user_quota"}}', 'disable'],
  ['NewAPI包装额度码pre_consume_token_quota_failed', 403, '{"error":{"type":"new_api_error","code":"pre_consume_token_quota_failed"}}', 'disable'],
  ['billing_error加余额文本', 403, '{"error":{"type":"billing_error","message":"余额不足"}}', 'disable'],
  ['单独billing_error', 403, '{"error":{"type":"billing_error"}}', undefined],
  ['裸403', 403, '{"error":{"message":"forbidden"}}', undefined],
  ['权限403', 403, '{"error":{"code":"permission_denied","message":"forbidden"}}', undefined],
  ['受限403', 403, '{"error":{"code":"client_restricted","message":"insufficient balance"}}', undefined],
  ['中转错误附带明确余额文本403', 403, '{"error":{"code":"new_api_error","message":"insufficient balance"}}', 'disable'],
  ['内容策略403', 403, '{"error":{"code":"content_policy_violation","message":"blocked"}}', undefined],
  ['内容策略回显额度文本403', 403, '{"error":{"message":"content policy violation: 用户输入包含余额不足"}}', undefined],
  ['原始正文回显额度文本403', 403, '{"error":{"message":"forbidden"},"echo":"余额不足"}', undefined],
  ['泛quota403', 403, '{"error":{"message":"quota exceeded for another limit"}}', undefined],
  ['401余额文本', 401, '{"error":{"message":"余额不足"}}', undefined],
  ['429余额文本', 429, '{"error":{"message":"余额不足"}}', undefined],
  ['明确余额文本403', 403, '{"error":{"message":"insufficient balance"}}', 'disable']
] as const) {
  const decision = decideAccountErrorPolicy({
    id: `account_error_policy_system_${name}`,
    providerCode: 'openai',
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    credentials: {},
    status: 'active'
  }, statusCode, new Headers({ 'content-type': 'application/json' }), Buffer.from(body), settings)
  assert.equal(decision?.action, expected, `${name} 的系统额度规则命中结果不符合预期`)
}

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
