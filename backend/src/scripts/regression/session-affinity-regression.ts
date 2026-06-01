import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'

import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import {
  areOpenAIHighConcurrencyAccountsHardBusy,
  forgetOpenAIAccountForSession,
  migrateOpenAIAccountSessionAffinity,
  orderOpenAIAccountsBySessionAffinity,
  rememberOpenAIAccountForSession,
  resolveOpenAIGatewaySessionAffinityKey
} from '../../modules/gateway/openai-gateway-session-affinity.service.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

async function main(): Promise<void> {
  testSessionAffinityMigrationUsesReverseIndex()
  testAffinityKeyUsesLocalIdentityOnly()
  testMissingBoundAccountDoesNotAffectCandidates()
  testForgetOnlyClearsMatchingBoundAccount()
  testAffinityDoesNotPromoteAcrossPriority()
  testAffinityDoesNotPromoteFallbackOverPrimary()
  testAffinityDoesNotPromoteOverBetterQuality()
  testAffinityPromotesWithinSameAvailabilityBucket()
  testAffinityPromotesAcrossAccountTypesWithinSameBucket()
  testAffinityBindingCanSwitchAcrossAccountTypesWithoutNewShard()
  testScopedMigrationOnlyMovesMatchingBindings()
  testHighConcurrencyUsesLeastLoadedWithinSameTier()
  testHighConcurrencyBreaksAffinityAtSoftLimit()
  testHighConcurrencyKeepsAffinityBelowSoftLimitWhenLoadTied()
  testHighConcurrencyKeepsFallbackIdleUntilPrimarySoftLimit()
  testHighConcurrencyDetectsHardBusyGroup()
  await testHighConcurrencyPenalizesRealtimeSlowInFlight()
  console.log('OpenAI session affinity regression passed')
}

function testSessionAffinityMigrationUsesReverseIndex(): void {
  const source = readFileSync(new URL('../../modules/gateway/openai-gateway-session-affinity.service.ts', import.meta.url), 'utf8')
  assert(source.includes('sessionAffinityKeysByAccountId'), '会话亲和迁移应维护按账号反查的索引')
  assert(source.includes('sessionAffinityKeysByAccountSystemScope'), '会话亲和迁移应维护按账号+系统账户反查的索引')
  assert(!source.includes('for (const [key, binding] of sessionAffinityCache.entries())'), '迁移会话亲和不能扫描全部亲和缓存')
  assert(source.includes('for (const key of sessionAffinityMigrationCandidateKeys(sourceAccountId, scope))'), '迁移会话亲和应只遍历源账号相关候选 key')
}

function testAffinityKeyUsesLocalIdentityOnly(): void {
  const req = createSessionRequest('shared-cache')
  const keyA = resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  })
  const keyARepeat = resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  })

  assert.equal(typeof keyA, 'string', '可识别会话请求应生成亲和 key')
  assert.equal(keyA, keyARepeat, '同一本地系统账户、API Key 和客户端会话应复用同一个亲和 key')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-b',
    groupId: 'group-a'
  }), '不同本地 API Key 必须隔离会话亲和')
  assert.equal(keyA, resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-b'
  }), '同一 API Key 下不同分组不应重新切分会话亲和')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-b',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  }), '不同调用方系统账户必须隔离会话亲和')
}

function testMissingBoundAccountDoesNotAffectCandidates(): void {
  const sessionKey = 'session-affinity-regression:missing'
  rememberOpenAIAccountForSession(sessionKey, 'missing-account')
  const accounts = [
    createAccount('stable-a', { priority: 0 }),
    createAccount('stable-b', { priority: 10 })
  ]

  assert.deepEqual(orderedIds(accounts, sessionKey), ['stable-a', 'stable-b'])
  forgetOpenAIAccountForSession(sessionKey)
}

function testForgetOnlyClearsMatchingBoundAccount(): void {
  const sessionKey = 'session-affinity-regression:forget-matching-account'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-oauth')
  const accounts = [
    createAccount('api-key-candidate', { priority: 0, type: 'api_key' }),
    createAccount('sticky-oauth', { priority: 0, type: 'oauth' })
  ]

  forgetOpenAIAccountForSession(sessionKey, 'api-key-candidate')
  assert.deepEqual(orderedIds(accounts, sessionKey), ['sticky-oauth', 'api-key-candidate'], '非绑定账号失败不应误删当前会话亲和')
  forgetOpenAIAccountForSession(sessionKey, 'sticky-oauth')
  assert.deepEqual(orderedIds(accounts, sessionKey), ['api-key-candidate', 'sticky-oauth'], '绑定账号失败才清理当前会话亲和')
}

function testAffinityDoesNotPromoteAcrossPriority(): void {
  const sessionKey = 'session-affinity-regression:priority'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-low-priority')
  const accounts = [
    createAccount('better-priority', { priority: 0 }),
    createAccount('sticky-low-priority', { priority: 10 })
  ]

  assert.deepEqual(orderedIds(accounts, sessionKey), ['better-priority', 'sticky-low-priority'])
  forgetOpenAIAccountForSession(sessionKey)
}

function testAffinityDoesNotPromoteFallbackOverPrimary(): void {
  const sessionKey = 'session-affinity-regression:fallback'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-fallback')
  const accounts = [
    createAccount('primary', { priority: 0 }),
    createAccount('sticky-fallback', { priority: 0, fallbackEnabled: true })
  ]

  assert.deepEqual(orderedIds(accounts, sessionKey), ['primary', 'sticky-fallback'])
  forgetOpenAIAccountForSession(sessionKey)
}

function testAffinityDoesNotPromoteOverBetterQuality(): void {
  const sessionKey = 'session-affinity-regression:quality'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-slower')
  const accounts = [
    createAccount('faster', { priority: 0, qualityScore: 100 }),
    createAccount('sticky-slower', { priority: 0, qualityScore: 300 }),
    createAccount('slower', { priority: 0, qualityScore: 500 })
  ]

  assert.deepEqual(orderedIds(accounts, sessionKey), ['faster', 'sticky-slower', 'slower'])
  forgetOpenAIAccountForSession(sessionKey)
}

function testAffinityPromotesWithinSameAvailabilityBucket(): void {
  const sessionKey = 'session-affinity-regression:same-bucket'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-good')
  const accounts = [
    createAccount('same-quality-a', { priority: 0, qualityScore: 300 }),
    createAccount('same-quality-b', { priority: 0, qualityScore: 300 }),
    createAccount('sticky-good', { priority: 0, qualityScore: 300 })
  ]

  assert.deepEqual(orderedIds(accounts, sessionKey), ['sticky-good', 'same-quality-a', 'same-quality-b'])
  forgetOpenAIAccountForSession(sessionKey)
}

function testAffinityPromotesAcrossAccountTypesWithinSameBucket(): void {
  const sessionKey = 'session-affinity-regression:account-type'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-oauth')
  const accounts = [
    createAccount('api-key-candidate', { priority: 0, type: 'api_key' }),
    createAccount('sticky-oauth', { priority: 0, type: 'oauth' })
  ]

  assert.deepEqual(orderedIds(accounts, sessionKey), ['sticky-oauth', 'api-key-candidate'])
  forgetOpenAIAccountForSession(sessionKey)
}

function testAffinityBindingCanSwitchAcrossAccountTypesWithoutNewShard(): void {
  const req = createSessionRequest('mixed-local-cache')
  const identity = {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  }
  const sessionKey = resolveOpenAIGatewaySessionAffinityKey(req, identity)
  const repeatedSessionKey = resolveOpenAIGatewaySessionAffinityKey(req, identity)
  const accounts = [
    createAccount('api-key-candidate', { priority: 0, type: 'api_key' }),
    createAccount('oauth-candidate', { priority: 0, type: 'oauth' })
  ]

  assert(sessionKey, '可识别本地会话应生成亲和 key')
  assert.equal(sessionKey, repeatedSessionKey, 'OAuth/API Key 切换不应为同一本地会话生成新的亲和分片')
  rememberOpenAIAccountForSession(sessionKey, 'oauth-candidate', identity)
  assert.deepEqual(orderedIds(accounts, sessionKey), ['oauth-candidate', 'api-key-candidate'], '同一本地会话先命中 OAuth 后应保留会话亲和')

  rememberOpenAIAccountForSession(sessionKey, 'api-key-candidate', identity)
  assert.deepEqual(orderedIds(accounts, sessionKey), ['api-key-candidate', 'oauth-candidate'], '同一本地会话切到 API Key 后应覆盖同一个亲和槽，而不是按账号类型另起分片')
  forgetOpenAIAccountForSession(sessionKey)
}

function testScopedMigrationOnlyMovesMatchingBindings(): void {
  const scopedKey = 'session-affinity-regression:scoped-migration'
  const sameApiKeyOtherGroupKey = 'session-affinity-regression:scoped-migration-same-key-other-group'
  const otherApiKey = 'session-affinity-regression:scoped-migration-other-api-key'
  const ownerKey = 'session-affinity-regression:scoped-migration-owner'
  const unscopedKey = 'session-affinity-regression:scoped-migration-unscoped'
  rememberOpenAIAccountForSession(scopedKey, 'shared-source', { systemAccountId: 'grantee-a', apiKeyId: 'key-a', groupId: 'group-a' })
  rememberOpenAIAccountForSession(sameApiKeyOtherGroupKey, 'shared-source', { systemAccountId: 'grantee-a', apiKeyId: 'key-a', groupId: 'group-b' })
  rememberOpenAIAccountForSession(otherApiKey, 'shared-source', { systemAccountId: 'grantee-a', apiKeyId: 'key-b', groupId: 'group-b' })
  rememberOpenAIAccountForSession(ownerKey, 'shared-source', { systemAccountId: 'owner', apiKeyId: 'owner-key', groupId: 'owner-group' })
  rememberOpenAIAccountForSession(unscopedKey, 'shared-source')
  const accounts = [
    createAccount('shared-source', { priority: 0 }),
    createAccount('scoped-target', { priority: 0 })
  ]

  assert.equal(
    migrateOpenAIAccountSessionAffinity('shared-source', 'scoped-target', { systemAccountId: 'grantee-a', apiKeyId: 'key-a', groupId: 'group-a' }).migratedSessionCount,
    2,
    '授权账户本地迁移应按当前 API Key 迁移，同一 API Key 下不同分组不应切开会话亲和'
  )
  assert.deepEqual(orderedIds(accounts, scopedKey), ['scoped-target', 'shared-source'])
  assert.deepEqual(orderedIds(accounts, sameApiKeyOtherGroupKey), ['scoped-target', 'shared-source'])
  assert.deepEqual(orderedIds(accounts, otherApiKey), ['shared-source', 'scoped-target'])
  assert.deepEqual(orderedIds(accounts, ownerKey), ['shared-source', 'scoped-target'])
  assert.deepEqual(orderedIds(accounts, unscopedKey), ['shared-source', 'scoped-target'])

  assert.equal(migrateOpenAIAccountSessionAffinity('shared-source', 'scoped-target').migratedSessionCount, 3)
  forgetOpenAIAccountForSession(scopedKey)
  forgetOpenAIAccountForSession(sameApiKeyOtherGroupKey)
  forgetOpenAIAccountForSession(otherApiKey)
  forgetOpenAIAccountForSession(ownerKey)
  forgetOpenAIAccountForSession(unscopedKey)
}

function testHighConcurrencyUsesLeastLoadedWithinSameTier(): void {
  const accounts = [
    createAccount('busy-primary', { priority: 0, currentConcurrency: 4 }),
    createAccount('idle-primary', { priority: 0, currentConcurrency: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, { groupType: 'high_concurrency' }).map((account) => account.id),
    ['idle-primary', 'busy-primary'],
    '高并发分组应在同层级内优先选择更空闲账号'
  )
}

function testHighConcurrencyBreaksAffinityAtSoftLimit(): void {
  const sessionKey = 'session-affinity-regression:high-concurrency-soft-break'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-soft-full')
  const accounts = [
    createAccount('idle-peer', { priority: 0, currentConcurrency: 0 }),
    createAccount('sticky-soft-full', { priority: 0, currentConcurrency: 5 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, sessionKey, { groupType: 'high_concurrency' }).map((account) => account.id),
    ['idle-peer', 'sticky-soft-full'],
    '高并发分组达到默认软并发后应打破会话亲和'
  )
  forgetOpenAIAccountForSession(sessionKey)
}

function testHighConcurrencyKeepsAffinityBelowSoftLimitWhenLoadTied(): void {
  const sessionKey = 'session-affinity-regression:high-concurrency-affinity-tie'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-idle')
  const accounts = [
    createAccount('same-tier-idle', { priority: 0, currentConcurrency: 0 }),
    createAccount('sticky-idle', { priority: 0, currentConcurrency: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, sessionKey, { groupType: 'high_concurrency' }).map((account) => account.id),
    ['sticky-idle', 'same-tier-idle'],
    '高并发分组在负载相同且未达软并发时仍可保留会话亲和'
  )
  forgetOpenAIAccountForSession(sessionKey)
}

function testHighConcurrencyKeepsFallbackIdleUntilPrimarySoftLimit(): void {
  const primaryAvailable = [
    createAccount('primary-under-soft', { priority: 0, currentConcurrency: 1 }),
    createAccount('fallback-idle', { priority: 0, currentConcurrency: 0, fallbackEnabled: true })
  ]
  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(primaryAvailable, undefined, { groupType: 'high_concurrency' }).map((account) => account.id),
    ['primary-under-soft', 'fallback-idle'],
    '高并发分组在主池未达软并发前不应让备用账号抢首流量'
  )

  const primarySoftFull = [
    createAccount('primary-soft-full', { priority: 0, currentConcurrency: 5 }),
    createAccount('fallback-idle', { priority: 0, currentConcurrency: 0, fallbackEnabled: true })
  ]
  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(primarySoftFull, undefined, { groupType: 'high_concurrency' }).map((account) => account.id),
    ['fallback-idle', 'primary-soft-full'],
    '高并发分组在主池达到软并发后应允许备用账号接单'
  )
}

function testHighConcurrencyDetectsHardBusyGroup(): void {
  assert.equal(
    areOpenAIHighConcurrencyAccountsHardBusy([
      createAccount('limit-a', { priority: 0, currentConcurrency: 2, concurrencyLimit: 2 }),
      createAccount('limit-b', { priority: 0, currentConcurrency: 1, concurrencyLimit: 1 })
    ], { groupType: 'high_concurrency' }),
    true,
    '高并发分组全部账号达到硬并发时应判定为分组繁忙'
  )
  assert.equal(
    areOpenAIHighConcurrencyAccountsHardBusy([
      createAccount('available-a', { priority: 0, currentConcurrency: 1, concurrencyLimit: 2 })
    ], { groupType: 'high_concurrency' }),
    false,
    '仍有账号低于硬并发时不应判定分组繁忙'
  )
}

async function testHighConcurrencyPenalizesRealtimeSlowInFlight(): Promise<void> {
  clearAccountConcurrency()
  const slowSlot = tryAcquireAccountConcurrency('slow-runtime', 10)
  assert.equal(slowSlot.acquired, true, '测试前应成功占用慢请求账号并发槽')
  await new Promise((resolve) => setTimeout(resolve, 5))
  const accounts = [
    createAccount('slow-runtime', { priority: 0, currentConcurrency: 1 }),
    createAccount('fast-runtime', { priority: 0, currentConcurrency: 1 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      schedulingPolicy: {
        slowRequestThresholdMs: 1,
        firstOutputSlowThresholdMs: 1
      }
    }).map((account) => account.id),
    ['fast-runtime', 'slow-runtime'],
    '高并发分组应让进行中慢请求账号降低新请求优先级'
  )
  slowSlot.release()
  clearAccountConcurrency()
}

function orderedIds(accounts: OpenAIAccountSecret[], sessionKey: string): string[] {
  return orderOpenAIAccountsBySessionAffinity(accounts, sessionKey).map((account) => account.id)
}

function createAccount(
  id: string,
  options: {
    priority: number
    qualityScore?: number
    superPriorityEnabled?: boolean
    fallbackEnabled?: boolean
    type?: 'api_key' | 'oauth'
    currentConcurrency?: number
    concurrencyLimit?: number
  }
): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: 'system-a',
    accountOwnerSystemAccountId: 'system-a',
    groupOwnerSystemAccountId: 'system-a',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: options.type ?? 'api_key',
    status: 'active',
    supportedModels: [],
    concurrencyLimit: options.concurrencyLimit ?? 20,
    currentConcurrency: options.currentConcurrency ?? 0,
    priority: options.priority,
    superPriorityEnabled: options.superPriorityEnabled ?? false,
    fallbackEnabled: options.fallbackEnabled ?? false,
    qualityScore: options.qualityScore,
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-test',
    streamFailureCount: 0,
    credentials: {}
  }
}

function createSessionRequest(promptCacheKey: string): Request {
  const headers: Record<string, string> = {
    prompt_cache_key: promptCacheKey
  }
  return {
    headers,
    body: {},
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as Request
}

await main()
