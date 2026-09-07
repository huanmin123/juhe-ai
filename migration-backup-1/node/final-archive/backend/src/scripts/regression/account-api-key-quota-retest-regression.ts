import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  resolveAccountApiKeyQuotaRetestDecision
} from '../../modules/background/account-api-key-cooldown-retest.service.js'
import { resolveAccountTestResponseDiagnostics } from '../../modules/accounts/account-test-response-diagnostics.js'
import { quotaRecoveryCooldownUntil } from '../../modules/accounts/quota-recovery-policy.js'
import { passiveScheduleJitterWindowMs } from '../../shared/passive-schedule-jitter.js'

const workerSource = readFileSync(new URL('../../modules/background/account-api-key-cooldown-retest.service.ts', import.meta.url), 'utf8')
const retestItemSource = sourceBetween(workerSource, 'async function runAccountApiKeyCooldownRetestQueueItem', 'async function loadAccountForTestViaDbService')
const delaySource = sourceBetween(workerSource, 'function quotaRecoveryDelaySeconds', '')
assert.match(retestItemSource, /recoverySeed: `\$\{account\.id\}:\$\{item\.keyFingerprint\?\.trim\(\) \|\| 'account'\}`/, 'worker 额度决策 seed 必须将空白 fingerprint 归一为 account')
assert.match(delaySource, /seed: `\$\{input\.account\.id\}:\$\{input\.keyFingerprint\?\.trim\(\) \|\| 'account'\}`/, 'worker 延迟计算 seed 必须将空白 fingerprint 归一为 account')
for (const seed of ['quota-retest-a', 'quota-retest-b', 'quota-retest-c']) {
  const quotaNow = new Date('2026-08-22T00:00:00.000Z')
  const quotaInput = {
    seed,
    accountType: 'api_key',
    now: quotaNow,
    policy: { api_key: { reset_strategy: 'duration', duration_minutes: 60, jitter_minutes: 15, timezone: 'UTC' } }
  } as const
  const cooldownUntil = Date.parse(quotaRecoveryCooldownUntil(quotaInput))
  const repeatedCooldownUntil = Date.parse(quotaRecoveryCooldownUntil(quotaInput))
  assert.equal(repeatedCooldownUntil, cooldownUntil, '同一 seed、策略和基准时间必须返回完全相同的额度恢复 deadline')
  const baselineMs = 60 * 60_000
  const windowMs = passiveScheduleJitterWindowMs(baselineMs)
  assert(
    cooldownUntil >= quotaNow.getTime() + baselineMs - windowMs
      && cooldownUntil <= quotaNow.getTime() + baselineMs + windowMs,
    '额度恢复必须使用统一的全局前后错峰窗口'
  )
}

const observedAt = new Date('2026-08-22T00:00:00.000Z')
const explicitResetAt = '2026-08-23T04:00:00.000Z'

const explicitAttempt = {
  accountId: 'quota-retest-account',
  accountName: 'quota-retest-account',
  upstreamUrl: 'https://upstream.example/v1/responses',
  status: 403,
  responseHeaders: { 'content-type': 'application/json' },
  responseBodyText: JSON.stringify({
    error: { code: 'insufficient_user_quota', message: '余额不足', reset_at: explicitResetAt }
  })
}

const firstExplicit = resolveAccountApiKeyQuotaRetestDecision({
  result: { statusCode: 403, errorCode: 'insufficient_user_quota', message: '上游额度不足' },
  upstreamAttempt: explicitAttempt,
  previousErrorCode: 'api_key_quota_insufficient_reset',
  recoveryStartedAt: undefined,
  observedAt
})
assert.equal(firstExplicit.quotaFailure, true, '带 reset_at 的额度失败必须匹配')
assert.equal(firstExplicit.recoveryMode, 'explicit_reset', '带 reset_at 的额度失败必须保持显式模式')
assert.equal(firstExplicit.cooldownUntil, explicitResetAt, '显式 reset_at 必须原样作为复测边界')
assert.equal(firstExplicit.timedOut, false, '显式恢复模式不得进入 30 天观察超时')

const repeatedExplicit = resolveAccountApiKeyQuotaRetestDecision({
  result: { statusCode: 403, errorCode: 'insufficient_user_quota', message: '上游额度不足' },
  upstreamAttempt: explicitAttempt,
  previousErrorCode: 'api_key_quota_insufficient_reset',
  observedAt: new Date('2026-08-22T01:00:00.000Z')
})
assert.equal(repeatedExplicit.recoveryMode, 'explicit_reset', '重复显式额度失败仍必须识别')
assert.equal(repeatedExplicit.cooldownUntil, explicitResetAt, '重复显式复测不得把当前 reset_at 改成 generic duration')

const explicitToGeneric = resolveAccountApiKeyQuotaRetestDecision({
  result: { statusCode: 403, message: '上游额度不足' },
  upstreamAttempt: {
    ...explicitAttempt,
    responseBodyText: JSON.stringify({ error: { message: '余额不足' } })
  },
  previousErrorCode: 'api_key_quota_insufficient_reset',
  recoveryStartedAt: undefined,
  observedAt
})
assert.equal(explicitToGeneric.quotaFailure, true, '显式模式后无 hint 仍必须匹配额度失败')
assert.equal(explicitToGeneric.previousRecoveryMode, 'explicit_reset')
assert.equal(explicitToGeneric.recoveryMode, 'generic', '当前无 hint 必须切换通用模式')
assert.equal(explicitToGeneric.timedOut, false, '显式转通用必须重新开始 30 天观察')
const explicitToGenericDelay = Date.parse(explicitToGeneric.cooldownUntil!) - observedAt.getTime()
const explicitToGenericWindow = passiveScheduleJitterWindowMs(60 * 60_000)
assert(
  explicitToGenericDelay >= 60 * 60_000 - explicitToGenericWindow
    && explicitToGenericDelay <= 60 * 60_000 + explicitToGenericWindow,
  '通用模式首次复测应从当前观察时刻等待 1 小时并按统一全局前后错峰'
)

const textOnlyAttempt = {
  ...explicitAttempt,
  responseHeaders: { 'content-type': 'text/plain' },
  responseBodyText: '余额不足，请充值后重试'
}
const textOnlyFirst = resolveAccountApiKeyQuotaRetestDecision({
  result: { statusCode: 403, message: '上游请求失败' },
  upstreamAttempt: textOnlyAttempt,
  previousErrorCode: 'api_key_quota_insufficient',
  recoveryStartedAt: '2026-08-21T23:00:00.000Z',
  observedAt
})
assert.equal(textOnlyFirst.quotaFailure, true, '纯高置信文本额度失败必须进入通用额度恢复')
assert.equal(textOnlyFirst.recoveryMode, 'generic')
assert.equal(textOnlyFirst.timedOut, false)

const textOnlyRepeated = resolveAccountApiKeyQuotaRetestDecision({
  result: { statusCode: 403, message: '上游请求失败' },
  upstreamAttempt: textOnlyAttempt,
  previousErrorCode: 'api_key_quota_insufficient',
  recoveryStartedAt: '2026-08-21T23:00:00.000Z',
  observedAt: new Date('2026-08-22T01:00:00.000Z')
})
assert.equal(textOnlyRepeated.quotaFailure, true, '重复纯文本额度失败仍必须被识别')
assert.equal(textOnlyRepeated.recoveryMode, 'generic')

const relativeRetryAfter = resolveAccountApiKeyQuotaRetestDecision({
  result: { statusCode: 403, message: '上游请求失败' },
  upstreamAttempt: {
    ...textOnlyAttempt,
    responseHeaders: { 'retry-after': '7200' }
  },
  observedAt
})
assert.equal(relativeRetryAfter.recoveryMode, 'explicit_reset')
assert.equal(relativeRetryAfter.cooldownUntil, '2026-08-22T02:00:00.000Z', '相对 Retry-After 必须从响应观测时刻计算')

const headersOnly = resolveAccountTestResponseDiagnostics({
  downstreamResponseText: '{"error":{"message":"上游请求失败"}}',
  downstreamResponseHeaders: { 'content-type': 'application/json' },
  downstreamResponseTruncated: false,
  upstreamAttempt: {
    accountId: 'quota-retest-account',
    accountName: 'quota-retest-account',
    upstreamUrl: 'https://upstream.example/v1/responses',
    responseHeaders: { 'retry-after': '7200' },
    responseBodyText: ''
  }
})
assert.deepEqual(headersOnly.responseHeaders, { 'retry-after': '7200' }, 'upstream body 为空时仍必须保留当前 upstream headers')

const statusOnly = resolveAccountTestResponseDiagnostics({
  downstreamResponseText: '{"error":{"code":"insufficient_user_quota","message":"余额不足"}}',
  downstreamResponseHeaders: { 'retry-after': '7200' },
  downstreamResponseTruncated: false,
  upstreamAttempt: {
    accountId: 'quota-retest-account',
    accountName: 'quota-retest-account',
    upstreamUrl: 'https://upstream.example/v1/responses',
    status: 403
  }
})
assert.match(statusOnly.responseText, /insufficient_user_quota/, 'status-only upstream callback 必须回填下游捕获的额度正文')
assert.deepEqual(statusOnly.responseHeaders, { 'retry-after': '7200' }, 'status-only upstream callback 必须保留下游恢复响应头')

console.log('account api key quota retest regression passed')

function sourceBetween(source: string, startMarker: string, endMarker: string): string {
  const start = source.indexOf(startMarker)
  const end = endMarker ? source.indexOf(endMarker, start + startMarker.length) : source.length
  assert(start >= 0 && end > start, `无法定位 ${startMarker}`)
  return source.slice(start, end)
}
