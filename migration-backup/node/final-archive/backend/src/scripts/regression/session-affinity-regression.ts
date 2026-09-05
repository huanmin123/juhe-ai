import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'

import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import {
  areOpenAIHighConcurrencyAccountsHardBusy,
  claimOpenAIAccountForSessionAsync,
  forgetOpenAIAccountForSession,
  migrateOpenAIAccountSessionAffinity,
  orderOpenAIAccountsBySessionAffinity,
  rememberOpenAIAccountTrafficMigrationPreference,
  rememberOpenAIAccountForSession,
  resolveOpenAIGatewaySessionAffinityKey
} from '../../modules/gateway/runtime/session-affinity.service.js'
import { resolveGatewaySessionIdentity } from '../../modules/gateway/session-identity/index.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import type { GatewayAccountModelPriority } from '../../modules/gateway/dispatch/model-filter.js'

async function main(): Promise<void> {
  testSessionAffinityMigrationUsesReverseIndex()
  testAffinityKeyUsesLocalIdentityOnly()
  testMissingBoundAccountDoesNotAffectCandidates()
  testForgetOnlyClearsMatchingBoundAccount()
  testAffinityDoesNotPromoteAcrossPriority()
  testAffinityDoesNotPromoteFallbackOverPrimary()
  testAffinityDoesNotPromoteOverBetterQuality()
  testAffinityPromotesWithinSameAvailabilityBucket()
  testAffinityFallbackContinuesAfterPromotedAccountWithinSamePriority()
  testAffinityPromotesAcrossAccountTypesWithinSameBucket()
  testAffinityBindingUsesFirstWriterAcrossAccountTypes()
  await testConcurrentAffinityClaimUsesSingleWinner()
  testScopedMigrationOnlyMovesMatchingBindings()
  testTrafficMigrationPreferenceBiasesNewRequests()
  testTrafficMigrationPreferenceBypassesSuperPriority()
  testTrafficMigrationPreferencePromotesFallbackTarget()
  testTrafficMigrationPreferenceIsScopedByGroup()
  testTrafficMigrationPreferenceOverridesExistingSessionAffinity()
  testTrafficMigrationSessionPreferenceOverridesAvailableSource()
  testTrafficMigrationPreferenceStopsWhenSourceReturns()
  testTrafficMigrationPreferenceBiasesHighConcurrency()
  testTrafficMigrationPreferenceBypassesHighConcurrencySuperPriority()
  testTrafficMigrationPreferencePromotesHighConcurrencyFallbackTarget()
  testTrafficMigrationPreferenceDoesNotBypassHighConcurrencyHardBusy()
  testTrafficMigrationPreferenceDoesNotBypassHighConcurrencyHardBusyWithoutFastFirst()
  testModelPriorityBlocksSessionAffinityPromotion()
  testModelPriorityLimitsTrafficMigrationPreference()
  testHighConcurrencyHonorsModelPriorityBeforeBusinessRank()
  testHighConcurrencyKeepsHardBusyModelMatchedAccountBehindAvailableFallback()
  testHighConcurrencyFallsBackToSuperPriorityWhenModelRankTied()
  testHighConcurrencyUsesQualityWithinSameModelAndBusinessTier()
  testHighConcurrencyUsesLeastLoadedWithinSameTier()
  testHighConcurrencyBreaksAffinityAtSoftLimit()
  testHighConcurrencyKeepsAffinityBelowSoftLimitWhenLoadTied()
  testHighConcurrencyKeepsFallbackIdleUntilPrimarySoftLimit()
  testHighConcurrencyDetectsHardBusyGroup()
  await testHighConcurrencyPenalizesRealtimeSlowInFlight()
  console.log('OpenAI session affinity regression passed')
}

function testSessionAffinityMigrationUsesReverseIndex(): void {
  const source = readFileSync(new URL('../../modules/gateway/runtime/session-affinity.service.ts', import.meta.url), 'utf8')
  assert(source.includes('sessionAffinityKeysByAccountId'), '会话亲和迁移应维护按账号反查的索引')
  assert(source.includes('sessionAffinityKeysByAccountSystemScope'), '会话亲和迁移应维护按账号+系统账户反查的索引')
  assert(source.includes('trafficMigrationPreferenceCache'), '手动迁移流量应维护同分组目标偏向运行态')
  assert(!source.includes('for (const [key, binding] of sessionAffinityCache.entries())'), '迁移会话亲和不能扫描全部亲和缓存')
  assert(source.includes('for (const key of sessionAffinityMigrationCandidateKeys(sourceAccountId, scope))'), '迁移会话亲和应只遍历源账号相关候选 key')
}

function testAffinityKeyUsesLocalIdentityOnly(): void {
  const req = createSessionRequest('shared-cache')
  const sessionIdentity = resolveTestSessionIdentity(req)
  const keyA = resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a',
    routeStrategyId: 'route-a',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID
  })
  const keyARepeat = resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a',
    routeStrategyId: 'route-a',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID
  })

  assert.equal(typeof keyA, 'string', '可识别会话请求应生成亲和 key')
  assert.equal(keyA, keyARepeat, '同一本地系统账户、API Key 和客户端会话应复用同一个亲和 key')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-b',
    groupId: 'group-a'
  }), '不同本地 API Key 必须隔离会话亲和')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-b'
  }), '不同分组必须隔离会话亲和')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, {
    systemAccountId: 'system-b',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  }), '不同调用方系统账户必须隔离会话亲和')

  const hintOnlyRequest = {
    method: 'POST',
    originalUrl: '/v1/responses',
    headers: {},
    body: { prompt_cache_key: 'shared-cache' }
  } as Request
  const hintOnlyIdentity = resolveGatewaySessionIdentity(hintOnlyRequest, {
    clientProfile: 'generic',
    systemAccountId: 'system-a',
    apiKeyId: 'key-a'
  })
  assert.equal(resolveOpenAIGatewaySessionAffinityKey(hintOnlyIdentity, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  }), undefined, 'prompt_cache_key 只能作为缓存提示，不能建立正式会话亲和')
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

function testAffinityFallbackContinuesAfterPromotedAccountWithinSamePriority(): void {
  const sessionKey = 'session-affinity-regression:same-priority-ring'
  rememberOpenAIAccountForSession(sessionKey, 'account-2')
  const accounts = [
    createAccount('account-1', { priority: 0, qualityScore: 300 }),
    createAccount('account-2', { priority: 0, qualityScore: 300 }),
    createAccount('account-3', { priority: 0, qualityScore: 300 }),
    createAccount('account-4', { priority: 0, qualityScore: 300 }),
    createAccount('account-5', { priority: 0, qualityScore: 300 }),
    createAccount('worse-quality', { priority: 0, qualityScore: 500 }),
    createAccount('fallback-low', { priority: 10 })
  ]

  assert.deepEqual(
    orderedIds(accounts, sessionKey),
    ['account-2', 'account-3', 'account-4', 'account-5', 'account-1', 'worse-quality', 'fallback-low'],
    '同优先级同质量层内亲和账号提到队首后，失败兜底应从原位置之后继续，而不是回到原队首或低质量账号'
  )
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

function testAffinityBindingUsesFirstWriterAcrossAccountTypes(): void {
  const req = createSessionRequest('mixed-local-cache')
  const scope = {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-a'
  }
  const sessionIdentity = resolveTestSessionIdentity(req)
  const sessionKey = resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, scope)
  const repeatedSessionKey = resolveOpenAIGatewaySessionAffinityKey(sessionIdentity, scope)
  const accounts = [
    createAccount('api-key-candidate', { priority: 0, type: 'api_key' }),
    createAccount('oauth-candidate', { priority: 0, type: 'oauth' })
  ]

  assert(sessionKey, '可识别本地会话应生成亲和 key')
  assert.equal(sessionKey, repeatedSessionKey, 'OAuth/API Key 切换不应为同一本地会话生成新的亲和分片')
  rememberOpenAIAccountForSession(sessionKey, 'oauth-candidate', scope)
  assert.deepEqual(orderedIds(accounts, sessionKey), ['oauth-candidate', 'api-key-candidate'], '同一本地会话先命中 OAuth 后应保留会话亲和')

  rememberOpenAIAccountForSession(sessionKey, 'api-key-candidate', scope)
  assert.deepEqual(orderedIds(accounts, sessionKey), ['oauth-candidate', 'api-key-candidate'], '后到请求不得覆盖同一亲和槽的首个绑定账号')
  forgetOpenAIAccountForSession(sessionKey, 'oauth-candidate')
  rememberOpenAIAccountForSession(sessionKey, 'api-key-candidate', scope)
  assert.deepEqual(orderedIds(accounts, sessionKey), ['api-key-candidate', 'oauth-candidate'], '首个绑定释放后才允许后续账号接管亲和槽')
  forgetOpenAIAccountForSession(sessionKey)
}

async function testConcurrentAffinityClaimUsesSingleWinner(): Promise<void> {
  const sessionKey = 'session-affinity-regression:concurrent-first-writer'
  forgetOpenAIAccountForSession(sessionKey)
  const scope = { systemAccountId: 'system-a', apiKeyId: 'key-a', groupId: 'group-a' }
  const winners = await Promise.all([
    claimOpenAIAccountForSessionAsync(sessionKey, 'candidate-a', scope),
    claimOpenAIAccountForSessionAsync(sessionKey, 'candidate-b', scope),
    claimOpenAIAccountForSessionAsync(sessionKey, 'candidate-c', scope)
  ])
  assert.equal(new Set(winners).size, 1, '同一会话的并发首批请求必须观察到唯一亲和 winner')
  assert.equal(winners[0], 'candidate-a', '内存驱动应由首个到达的候选原子占有亲和槽')
  assert.deepEqual(orderedIds([
    createAccount('candidate-b', { priority: 0 }),
    createAccount('candidate-a', { priority: 0 }),
    createAccount('candidate-c', { priority: 0 })
  ], sessionKey), ['candidate-a', 'candidate-c', 'candidate-b'], '后到并发请求应在最终派发前服从首写 winner')
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

function testTrafficMigrationPreferenceBiasesNewRequests(): void {
  const scope = { systemAccountId: 'migration-pref-system-a', groupId: 'migration-pref-group-a' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-a', 'target-a', scope)
  const accounts = [
    createAccount('peer-a', { priority: 0 }),
    createAccount('target-a', { priority: 50, qualityScore: 500 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, { trafficMigrationScope: scope }).map((account) => account.id),
    ['target-a', 'peer-a'],
    '手动迁移后没有会话亲和的新请求应优先尝试目标账户'
  )
}

function testTrafficMigrationPreferenceBypassesSuperPriority(): void {
  const scope = { systemAccountId: 'migration-pref-system-super', groupId: 'migration-pref-group-super' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-super', 'target-super', scope)
  const accounts = [
    createAccount('super-peer', { priority: 0, superPriorityEnabled: true }),
    createAccount('target-super', { priority: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, { trafficMigrationScope: scope }).map((account) => account.id),
    ['target-super', 'super-peer'],
    '手动迁移目标应作为短期最高排序覆盖越过同分组超级优先账户'
  )
}

function testTrafficMigrationPreferencePromotesFallbackTarget(): void {
  const scope = { systemAccountId: 'migration-pref-system-fallback', groupId: 'migration-pref-group-fallback' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-fallback', 'target-fallback', scope)
  const accounts = [
    createAccount('primary-peer', { priority: 0 }),
    createAccount('target-fallback', { priority: 0, fallbackEnabled: true })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, { trafficMigrationScope: scope }).map((account) => account.id),
    ['target-fallback', 'primary-peer'],
    '手动迁移目标应作为短期最高排序覆盖把目标备用账户提前到主池账户前'
  )
}

function testTrafficMigrationPreferenceIsScopedByGroup(): void {
  const scope = { systemAccountId: 'migration-pref-system-b', groupId: 'migration-pref-group-b' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-b', 'target-b', scope)
  const accounts = [
    createAccount('peer-b', { priority: 0 }),
    createAccount('target-b', { priority: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      trafficMigrationScope: { systemAccountId: scope.systemAccountId, groupId: 'migration-pref-other-group' }
    }).map((account) => account.id),
    ['peer-b', 'target-b'],
    '迁移目标偏向只能影响当前系统账户和当前分组'
  )
}

function testTrafficMigrationPreferenceOverridesExistingSessionAffinity(): void {
  const scope = { systemAccountId: 'migration-pref-system-c', groupId: 'migration-pref-group-c' }
  const sessionKey = 'session-affinity-regression:migration-pref-existing-session'
  rememberOpenAIAccountTrafficMigrationPreference('source-down-c', 'target-c', scope)
  rememberOpenAIAccountForSession(sessionKey, 'sticky-peer-c', { ...scope, apiKeyId: 'key-c' })
  const accounts = [
    createAccount('sticky-peer-c', { priority: 0 }),
    createAccount('target-c', { priority: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, sessionKey, { trafficMigrationScope: { ...scope, apiKeyId: 'key-c' } }).map((account) => account.id),
    ['target-c', 'sticky-peer-c'],
    '手动迁移目标应作为短期最高排序覆盖已有会话亲和'
  )
  forgetOpenAIAccountForSession(sessionKey)
}

function testTrafficMigrationSessionPreferenceOverridesAvailableSource(): void {
  const sessionKey = 'session-affinity-regression:migration-session-preference'
  const accounts = [
    createAccount('source-still-active', { priority: 0, superPriorityEnabled: true }),
    createAccount('target-session-only', { priority: 50, fallbackEnabled: true }),
    createAccount('peer-session-only', { priority: 10 })
  ]
  rememberOpenAIAccountForSession(sessionKey, 'source-still-active')

  assert.equal(
    migrateOpenAIAccountSessionAffinity('source-still-active', 'target-session-only', undefined, { preferMigratedSessions: true }).migratedSessionCount,
    1,
    '不改源账户状态的迁移仍应迁移当前已识别客户端会话'
  )
  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, sessionKey).map((account) => account.id),
    ['target-session-only', 'source-still-active', 'peer-session-only'],
    '只迁移当前客户端时，即使源账户仍可用，已迁移会话也应优先命中目标账户'
  )
  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined).map((account) => account.id),
    ['source-still-active', 'target-session-only', 'peer-session-only'],
    '只迁移当前客户端不应影响没有会话亲和的新客户端调度'
  )
  forgetOpenAIAccountForSession(sessionKey)
}

function testTrafficMigrationPreferenceStopsWhenSourceReturns(): void {
  const scope = { systemAccountId: 'migration-pref-system-d', groupId: 'migration-pref-group-d' }
  rememberOpenAIAccountTrafficMigrationPreference('source-returned-d', 'target-d', scope)
  const accountsWithSource = [
    createAccount('peer-d', { priority: 0 }),
    createAccount('target-d', { priority: 10 }),
    createAccount('source-returned-d', { priority: 20 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accountsWithSource, undefined, { trafficMigrationScope: scope }).map((account) => account.id),
    ['peer-d', 'target-d', 'source-returned-d'],
    '源账户重新进入候选池后应停止迁移目标偏向'
  )
  const accountsWithoutSource = accountsWithSource.filter((account) => account.id !== 'source-returned-d')
  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accountsWithoutSource, undefined, { trafficMigrationScope: scope }).map((account) => account.id),
    ['peer-d', 'target-d'],
    '源账户恢复后迁移目标偏向应被清理，避免后续再次劫持普通调度'
  )
}

function testTrafficMigrationPreferenceBiasesHighConcurrency(): void {
  const scope = { systemAccountId: 'migration-pref-system-e', groupId: 'migration-pref-group-e' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-e', 'target-e', scope)
  const accounts = [
    createAccount('idle-peer-e', { priority: 0, currentConcurrency: 0, concurrencyLimit: 20 }),
    createAccount('target-e', { priority: 50, currentConcurrency: 8, concurrencyLimit: 20 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      trafficMigrationScope: scope
    }).map((account) => account.id),
    ['target-e', 'idle-peer-e'],
    '高并发分组应在目标未硬满时优先尝试手动迁移目标'
  )
}

function testTrafficMigrationPreferenceBypassesHighConcurrencySuperPriority(): void {
  const scope = { systemAccountId: 'migration-pref-system-hc-super', groupId: 'migration-pref-group-hc-super' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-hc-super', 'target-hc-super', scope)
  const accounts = [
    createAccount('super-peer-hc', { priority: 0, superPriorityEnabled: true, currentConcurrency: 1, concurrencyLimit: 20 }),
    createAccount('target-hc-super', { priority: 0, currentConcurrency: 0, concurrencyLimit: 20 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      trafficMigrationScope: scope
    }).map((account) => account.id),
    ['target-hc-super', 'super-peer-hc'],
    '高并发分组的手动迁移目标应作为短期最高排序覆盖越过超级优先账户'
  )
}

function testTrafficMigrationPreferencePromotesHighConcurrencyFallbackTarget(): void {
  const scope = { systemAccountId: 'migration-pref-system-hc-fallback', groupId: 'migration-pref-group-hc-fallback' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-hc-fallback', 'target-hc-fallback', scope)
  const accounts = [
    createAccount('primary-peer-hc', { priority: 0, currentConcurrency: 0, concurrencyLimit: 20 }),
    createAccount('target-hc-fallback', { priority: 0, currentConcurrency: 0, concurrencyLimit: 20, fallbackEnabled: true })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      trafficMigrationScope: scope
    }).map((account) => account.id),
    ['target-hc-fallback', 'primary-peer-hc'],
    '高并发分组的手动迁移目标应作为短期最高排序覆盖把目标备用账户提前到可用主池前'
  )
}

function testTrafficMigrationPreferenceDoesNotBypassHighConcurrencyHardBusy(): void {
  const scope = { systemAccountId: 'migration-pref-system-hc-hard-busy', groupId: 'migration-pref-group-hc-hard-busy' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-hc-hard-busy', 'target-hc-hard-busy', scope)
  const accounts = [
    createAccount('idle-peer-hc-hard-busy', { priority: 0, currentConcurrency: 0, concurrencyLimit: 20 }),
    createAccount('target-hc-hard-busy', { priority: 50, currentConcurrency: 20, concurrencyLimit: 20 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      trafficMigrationScope: scope
    }).map((account) => account.id),
    ['idle-peer-hc-hard-busy', 'target-hc-hard-busy'],
    '高并发分组的手动迁移目标达到硬并发后不应继续抢首位'
  )
}

function testTrafficMigrationPreferenceDoesNotBypassHighConcurrencyHardBusyWithoutFastFirst(): void {
  const scope = { systemAccountId: 'migration-pref-system-hc-no-fast-first', groupId: 'migration-pref-group-hc-no-fast-first' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-hc-no-fast-first', 'target-hc-no-fast-first', scope)
  const accounts = [
    createAccount('idle-peer-hc-no-fast-first', { priority: 0, currentConcurrency: 0, concurrencyLimit: 20 }),
    createAccount('target-hc-no-fast-first', { priority: 50, currentConcurrency: 20, concurrencyLimit: 20 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      schedulingPolicy: { fastFirstEnabled: false },
      trafficMigrationScope: scope
    }).map((account) => account.id),
    ['idle-peer-hc-no-fast-first', 'target-hc-no-fast-first'],
    '高并发快速优先关闭时，手动迁移目标达到硬并发也不应越过可用账号'
  )
}

function testModelPriorityBlocksSessionAffinityPromotion(): void {
  const sessionKey = 'session-affinity-regression:model-priority-affinity'
  rememberOpenAIAccountForSession(sessionKey, 'sticky-unrestricted')
  const accounts = [
    createAccount('direct-model-match', { priority: 0, supportedModels: ['gpt-5.5'] }),
    createAccount('sticky-unrestricted', { priority: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, sessionKey, {
      modelPriority: modelPriority({
        'direct-model-match': 0,
        'sticky-unrestricted': 2
      })
    }).map((account) => account.id),
    ['direct-model-match', 'sticky-unrestricted'],
    '会话亲和不能把未限制模型账号提升到显式支持模型账号前面'
  )
  forgetOpenAIAccountForSession(sessionKey)
}

function testModelPriorityLimitsTrafficMigrationPreference(): void {
  const scope = { systemAccountId: 'migration-pref-system-model-priority', groupId: 'migration-pref-group-model-priority' }
  rememberOpenAIAccountTrafficMigrationPreference('source-down-model-priority', 'target-unrestricted-model-priority', scope)
  const accounts = [
    createAccount('direct-model-priority-peer', { priority: 50, supportedModels: ['gpt-5.5'] }),
    createAccount('target-unrestricted-model-priority', { priority: 0 })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      trafficMigrationScope: scope,
      modelPriority: modelPriority({
        'direct-model-priority-peer': 0,
        'target-unrestricted-model-priority': 2
      })
    }).map((account) => account.id),
    ['direct-model-priority-peer', 'target-unrestricted-model-priority'],
    '手动迁移目标不能越过更确定支持请求模型的账号'
  )
}

function testHighConcurrencyHonorsModelPriorityBeforeBusinessRank(): void {
  const accounts = [
    createAccount('unrestricted-super-idle', {
      priority: 0,
      superPriorityEnabled: true,
      currentConcurrency: 0
    }),
    createAccount('direct-model-busy-low-priority', {
      priority: 100,
      currentConcurrency: 4,
      supportedModels: ['gpt-5.5']
    })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      modelPriority: modelPriority({
        'unrestricted-super-idle': 2,
        'direct-model-busy-low-priority': 0
      })
    }).map((account) => account.id),
    ['direct-model-busy-low-priority', 'unrestricted-super-idle'],
    '高并发分组应先选择显式支持模型账号，再进入超级优先、priority 和负载排序'
  )
}

function testHighConcurrencyKeepsHardBusyModelMatchedAccountBehindAvailableFallback(): void {
  const accounts = [
    createAccount('direct-model-hard-busy', {
      priority: 100,
      currentConcurrency: 20,
      concurrencyLimit: 20,
      supportedModels: ['gpt-5.5']
    }),
    createAccount('unrestricted-available', {
      priority: 0,
      currentConcurrency: 0,
      concurrencyLimit: 20
    })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      modelPriority: modelPriority({
        'direct-model-hard-busy': 0,
        'unrestricted-available': 2
      })
    }).map((account) => account.id),
    ['unrestricted-available', 'direct-model-hard-busy'],
    '显式支持模型账号硬并发满时，仍应让可用的未限制账号承接'
  )
}

function testHighConcurrencyFallsBackToSuperPriorityWhenModelRankTied(): void {
  const accounts = [
    createAccount('unrestricted-normal-better-priority', {
      priority: 0,
      currentConcurrency: 0
    }),
    createAccount('unrestricted-super-worse-priority', {
      priority: 100,
      superPriorityEnabled: true,
      currentConcurrency: 0
    })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      modelPriority: modelPriority({
        'unrestricted-normal-better-priority': 2,
        'unrestricted-super-worse-priority': 2
      })
    }).map((account) => account.id),
    ['unrestricted-super-worse-priority', 'unrestricted-normal-better-priority'],
    '没有模型确定性差异时，高并发分组应回落到超级优先等业务排序'
  )
}

function testHighConcurrencyUsesQualityWithinSameModelAndBusinessTier(): void {
  const accounts = [
    createAccount('direct-model-slower-quality', {
      priority: 0,
      superPriorityEnabled: true,
      currentConcurrency: 0,
      qualityScore: 500,
      supportedModels: ['gpt-5.5']
    }),
    createAccount('direct-model-faster-quality', {
      priority: 0,
      superPriorityEnabled: true,
      currentConcurrency: 0,
      qualityScore: 100,
      supportedModels: ['gpt-5.5']
    })
  ]

  assert.deepEqual(
    orderOpenAIAccountsBySessionAffinity(accounts, undefined, {
      groupType: 'high_concurrency',
      modelPriority: modelPriority({
        'direct-model-slower-quality': 0,
        'direct-model-faster-quality': 0
      })
    }).map((account) => account.id),
    ['direct-model-faster-quality', 'direct-model-slower-quality'],
    '多个显式命中模型且业务排序一致时，高并发分组应优先选择质量分更低的账号'
  )
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
    supportedModels?: string[]
  }
): OpenAIAccountSecret {
  return {
    id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'system-a',
    accountOwnerSystemAccountId: 'system-a',
    groupOwnerSystemAccountId: 'system-a',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
    type: options.type ?? 'api_key',
    status: 'active',
    supportedModels: options.supportedModels ?? [],
    concurrencyLimit: options.concurrencyLimit ?? 20,
    currentConcurrency: options.currentConcurrency ?? 0,
    priority: options.priority,
    superPriorityEnabled: options.superPriorityEnabled ?? false,
    fallbackEnabled: options.fallbackEnabled ?? false,
    clientCompatibility: 'openai_standard',
    healthCheckEndpointMode: 'responses_sse',
    qualityScore: options.qualityScore,
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-test',
    streamFailureCount: 0,
    credentials: {}
  }
}

function createSessionRequest(sessionId: string): Request {
  const headers: Record<string, string> = {
    'session-id': sessionId
  }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    headers,
    body: {},
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as Request
}

function resolveTestSessionIdentity(req: Request) {
  return resolveGatewaySessionIdentity(req, {
    clientProfile: 'codex',
    systemAccountId: 'system-a',
    apiKeyId: 'key-a'
  })
}

function modelPriority(ranks: Record<string, number>): GatewayAccountModelPriority {
  return {
    requestedModel: 'gpt-5.5',
    rankByAccountId: new Map(Object.entries(ranks))
  }
}

await main()
