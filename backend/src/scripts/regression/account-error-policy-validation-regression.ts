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
import { genericApiKeyQuotaCooldownUntil } from '../../modules/gateway/policy/api-key-quota-recovery.js'
import { quotaRecoveryCooldownUntil } from '../../modules/accounts/quota-recovery-policy.js'

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

const stableGenericNow = new Date('2026-08-24T00:00:00.000Z')
const stableGenericFirst = genericApiKeyQuotaCooldownUntil({
  now: stableGenericNow,
  seed: 'quota-regression:account-a:key-a:g1'
})
const stableGenericSecond = genericApiKeyQuotaCooldownUntil({
  now: stableGenericNow,
  seed: 'quota-regression:account-a:key-a:g1'
})
assert.equal(stableGenericFirst, stableGenericSecond, '同一账户、Key 和恢复代次必须复用稳定错峰')
assert(Date.parse(stableGenericFirst) - stableGenericNow.getTime() >= 60 * 60_000, '通用额度恢复不得早于 1 小时')
assert(Date.parse(stableGenericFirst) - stableGenericNow.getTime() <= 75 * 60_000, '通用额度恢复错峰不得超过 15 分钟')
const configuredDuration = quotaRecoveryCooldownUntil({
  accountType: 'api_key',
  seed: 'quota-regression:configured',
  now: stableGenericNow,
  policy: { api_key: { reset_strategy: 'duration', duration_minutes: 90, timezone: 'UTC' } }
})
assert(Date.parse(configuredDuration) - stableGenericNow.getTime() >= 90 * 60_000, '账户级 API Key 恢复间隔配置必须生效')
assert(Date.parse(configuredDuration) - stableGenericNow.getTime() <= 105 * 60_000, '账户级 API Key 恢复间隔仍必须受系统错峰上限约束')
const configuredOAuthDaily = quotaRecoveryCooldownUntil({
  accountType: 'oauth',
  seed: 'quota-regression:oauth',
  now: new Date('2026-08-24T01:00:00.000Z'),
  policy: { oauth: { reset_strategy: 'daily', daily_reset_hour: 3, timezone: 'UTC' } }
})
assert(Date.parse(configuredOAuthDaily) - Date.parse('2026-08-24T03:00:00.000Z') >= 0, 'OAuth daily 恢复策略必须可配置且按 UTC 计算')
assert(Date.parse(configuredOAuthDaily) - Date.parse('2026-08-24T03:00:00.000Z') <= 15 * 60_000, 'OAuth daily 仍必须使用系统稳定错峰')

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
assert.equal(systemQuotaDecision?.action, 'cooldown', '系统额度规则必须先于账户自定义规则执行')
assert.equal(systemQuotaDecision?.ruleSource, 'system')
assert.equal(systemQuotaDecision?.ruleId, 'system.upstream_insufficient_quota')
assert.equal(systemQuotaDecision?.ruleName, '上游额度不足')
assert.equal(systemQuotaDecision?.cooldownStatus, 'rate_limited')
assert.ok(systemQuotaDecision?.cooldownUntil, '系统额度规则命中必须返回下一次每日恢复时间')
assert(Number.isFinite(Date.parse(systemQuotaDecision!.cooldownUntil!)) && Date.parse(systemQuotaDecision!.cooldownUntil!) > Date.now(), '系统额度规则 cooldownUntil 必须是未来 RFC3339 时间')

const oauthQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_oauth_fixed_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'oauth',
  credentials: {},
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_user_quota","reset_at":"2099-01-02T03:04:05Z"}}'), settings)
assert.equal(oauthQuotaDecision?.quotaRecoveryMode, undefined, 'OAuth 额度场景应使用 daily/duration/weekly 账户策略，而不是 API Key explicit_reset 模式')
assert.notEqual(oauthQuotaDecision?.cooldownUntil, '2099-01-02T03:04:05.000Z', 'OAuth 不得消费 API Key 的 reset_at hint')

const multiKeyApiKeyQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_multi_key_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  apiKeys: ['sk-quota-a', 'sk-quota-b'],
  selectedApiKeyFingerprint: 'selected-key-fingerprint',
  credentials: { api_keys: ['sk-quota-a', 'sk-quota-b'] },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_user_quota"}}'), settings)
assert.equal(multiKeyApiKeyQuotaDecision?.keyScoped, true, '多 Key API Key 额度不足必须只作用于当前 fingerprint')
assert.equal(multiKeyApiKeyQuotaDecision?.quotaRecoveryMode, 'generic', '没有 reset_at 时 API Key 必须走通用恢复模式')
assert.equal(multiKeyApiKeyQuotaDecision?.cooldownStatus, 'rate_limited')
assert(multiKeyApiKeyQuotaDecision?.cooldownUntil, '通用 API Key 额度恢复必须提供复测边界')
const genericApiKeyDelay = Date.parse(multiKeyApiKeyQuotaDecision!.cooldownUntil!) - Date.now()
assert(genericApiKeyDelay >= 60 * 60_000 - 10_000 && genericApiKeyDelay <= 75 * 60_000 + 10_000, '通用 API Key 额度复测默认应为 1 小时并带 0-15 分钟错峰')

const configuredApiKeyQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_configured_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  apiKeys: ['sk-configured-a', 'sk-configured-b'],
  selectedApiKeyFingerprint: 'configured-key-fingerprint',
  credentials: {
    api_keys: ['sk-configured-a', 'sk-configured-b'],
    quota_recovery_policy: {
      api_key: { reset_strategy: 'duration', duration_minutes: 90, timezone: 'UTC' }
    }
  },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_user_quota"}}'), settings)
const configuredApiKeyDelay = Date.parse(configuredApiKeyQuotaDecision!.cooldownUntil!) - Date.now()
assert(configuredApiKeyDelay >= 90 * 60_000 - 10_000 && configuredApiKeyDelay <= 105 * 60_000 + 10_000, '账户 API Key 额度恢复策略必须影响系统额度决策')

const normalizedRuntimeApiKeysQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_runtime_api_keys_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  apiKeys: ['sk-runtime-a', 'sk-runtime-b'],
  selectedApiKeyFingerprint: 'selected-runtime-key',
  credentials: { api_key: 'sk-runtime-a' },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_user_quota"}}'), settings)
assert.equal(normalizedRuntimeApiKeysQuotaDecision?.keyScoped, true, '运行态已经展开 apiKeys 时，即使 credentials 只保留主 Key 也必须按当前 Key 隔离')

const explicitResetApiKeyQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_explicit_reset',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  apiKeys: ['sk-reset-a', 'sk-reset-b'],
  selectedApiKeyFingerprint: 'selected-reset-key',
  credentials: { api_keys: ['sk-reset-a', 'sk-reset-b'] },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_user_quota","reset_at":"2099-01-02T03:04:05Z"}}'), settings)
assert.equal(explicitResetApiKeyQuotaDecision?.keyScoped, true)
assert.equal(explicitResetApiKeyQuotaDecision?.quotaRecoveryMode, 'explicit_reset', '供应商提供 reset_at 时必须进入显式恢复模式')
assert.equal(explicitResetApiKeyQuotaDecision?.cooldownUntil, '2099-01-02T03:04:05.000Z')

const singleKeyApiKeyQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_single_key_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  apiKeys: ['sk-single'],
  selectedApiKeyFingerprint: 'single-key-fingerprint',
  credentials: { api_key: 'sk-single' },
  status: 'active'
}, 403, new Headers({ 'retry-after': '7200' }), Buffer.from('{"error":{"code":"insufficient_user_quota"}}'), settings)
assert.equal(singleKeyApiKeyQuotaDecision?.keyScoped, false, '单 Key 账户不应伪造多 Key fingerprint 隔离')
assert.equal(singleKeyApiKeyQuotaDecision?.quotaRecoveryMode, 'explicit_reset')
assert(Date.parse(singleKeyApiKeyQuotaDecision!.cooldownUntil!) > Date.now() + 7_100_000, 'Retry-After 必须覆盖 API Key 通用默认间隔')

const numericResetHeaderDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_numeric_reset_header',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  apiKeys: ['sk-numeric-reset-a', 'sk-numeric-reset-b'],
  selectedApiKeyFingerprint: 'numeric-reset-key',
  credentials: { api_key: 'sk-numeric-reset-a' },
  status: 'active'
}, 403, new Headers({ 'x-ratelimit-reset': String(Math.ceil((Date.now() + 7_200_000) / 1000)) }), Buffer.from('{"error":{"code":"insufficient_user_quota"}}'), settings)
assert.equal(numericResetHeaderDecision?.quotaRecoveryMode, 'explicit_reset', 'Unix 秒级 reset header 必须被识别为明确恢复时间')
assert(Date.parse(numericResetHeaderDecision!.cooldownUntil!) > Date.now() + 7_100_000, 'Unix 秒级 reset header 必须覆盖 API Key 通用默认间隔')

const systemQuotaBeforeInvalidLocalRule = decideAccountErrorPolicy({
  id: 'account_error_policy_invalid_local_rule',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  credentials: { error_handling_rules: [{ enabled: true, name: '' }] },
  status: 'active'
}, 403, new Headers({ 'content-type': 'application/json' }), Buffer.from('{"error":{"code":"insufficient_quota"}}'), settings)
assert.equal(systemQuotaBeforeInvalidLocalRule?.ruleSource, 'system', '损坏的历史账户规则不能阻断系统额度保护')

const plainTextQuotaDecision = decideAccountErrorPolicy({
  id: 'account_error_policy_plain_text_quota',
  providerCode: 'openai',
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  type: 'api_key',
  credentials: {},
  status: 'active'
}, 403, new Headers({ 'content-type': 'text/plain' }), Buffer.from('余额不足，请充值'), settings)
assert.equal(plainTextQuotaDecision?.action, 'cooldown', 'text/plain 403 额度正文也必须命中系统额度规则')
assert.equal(plainTextQuotaDecision?.cooldownStatus, 'rate_limited')

for (const [name, statusCode, body, expected] of [
  ['额度码insufficient_user_quota', 403, '{"error":{"code":"insufficient_user_quota"}}', 'cooldown'],
  ['额度码insufficient_quota', 403, '{"error":{"code":"insufficient_quota"}}', 'cooldown'],
  ['额度码insufficient_balance', 403, '{"error":{"code":"insufficient_balance"}}', 'cooldown'],
  ['额度码quota_exceeded', 403, '{"error":{"code":"quota_exceeded"}}', 'cooldown'],
  ['额度码quota_exhausted', 403, '{"error":{"code":"quota_exhausted"}}', 'cooldown'],
  ['额度码wallet_balance_exhausted', 403, '{"error":{"code":"WALLET_BALANCE_EXHAUSTED"}}', 'cooldown'],
  ['额度码pre_consume_token_quota_failed', 403, '{"error":{"code":"pre_consume_token_quota_failed"}}', 'cooldown'],
  ['NewAPI包装额度码insufficient_user_quota', 403, '{"error":{"type":"new_api_error","code":"insufficient_user_quota"}}', 'cooldown'],
  ['NewAPI包装额度码pre_consume_token_quota_failed', 403, '{"error":{"type":"new_api_error","code":"pre_consume_token_quota_failed"}}', 'cooldown'],
  ['billing_error加余额文本', 403, '{"error":{"type":"billing_error","message":"余额不足"}}', 'cooldown'],
  ['单独billing_error', 403, '{"error":{"type":"billing_error"}}', undefined],
  ['裸403', 403, '{"error":{"message":"forbidden"}}', undefined],
  ['权限403', 403, '{"error":{"code":"permission_denied","message":"forbidden"}}', undefined],
  ['受限403', 403, '{"error":{"code":"client_restricted","message":"insufficient balance"}}', undefined],
  ['中转错误附带明确余额文本403', 403, '{"error":{"code":"new_api_error","message":"insufficient balance"}}', 'cooldown'],
  ['内容策略403', 403, '{"error":{"code":"content_policy_violation","message":"blocked"}}', undefined],
  ['内容策略回显额度文本403', 403, '{"error":{"message":"content policy violation: 用户输入包含余额不足"}}', undefined],
  ['原始正文回显额度文本403', 403, '{"error":{"message":"forbidden"},"echo":"余额不足"}', undefined],
  ['泛quota403', 403, '{"error":{"message":"quota exceeded for another limit"}}', undefined],
  ['纯文本余额不足403', 403, '余额不足，请充值', 'cooldown'],
  ['401余额文本', 401, '{"error":{"message":"余额不足"}}', undefined],
  ['429余额文本', 429, '{"error":{"message":"余额不足"}}', undefined],
  ['明确余额文本403', 403, '{"error":{"message":"insufficient balance"}}', 'cooldown']
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
  if (expected === 'cooldown') {
    assert.equal(decision?.ruleSource, 'system', `${name} 必须记录系统规则来源`)
    assert.equal(decision?.cooldownStatus, 'rate_limited', `${name} 必须进入 rate_limited`)
    assert.ok(decision?.cooldownUntil, `${name} 必须提供下一次每日恢复时间`)
    assert(Number.isFinite(Date.parse(decision!.cooldownUntil!)) && Date.parse(decision!.cooldownUntil!) > Date.now(), `${name} 的 cooldownUntil 必须是未来 RFC3339 时间`)
  }
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
