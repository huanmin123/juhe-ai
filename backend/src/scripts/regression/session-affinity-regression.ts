import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  forgetOpenAIAccountForSession,
  migrateOpenAIAccountSessionAffinity,
  orderOpenAIAccountsBySessionAffinity,
  rememberOpenAIAccountForSession,
  resolveOpenAIGatewaySessionAffinityKey
} from '../../modules/gateway/openai-gateway-session-affinity.service.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

function main(): void {
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
  console.log('OpenAI session affinity regression passed')
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
  assert.equal(keyA, keyARepeat, '同一本地系统账户、API Key、分组和客户端会话应复用同一个亲和 key')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-b',
    groupId: 'group-a'
  }), '不同本地 API Key 必须隔离会话亲和')
  assert.notEqual(keyA, resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId: 'system-a',
    apiKeyId: 'key-a',
    groupId: 'group-b'
  }), '不同分组必须隔离会话亲和')
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
  const otherGranteeKey = 'session-affinity-regression:scoped-migration-other-grantee'
  const ownerKey = 'session-affinity-regression:scoped-migration-owner'
  const legacyKey = 'session-affinity-regression:scoped-migration-legacy'
  rememberOpenAIAccountForSession(scopedKey, 'shared-source', { systemAccountId: 'grantee-a', apiKeyId: 'key-a', groupId: 'group-a' })
  rememberOpenAIAccountForSession(otherGranteeKey, 'shared-source', { systemAccountId: 'grantee-b', apiKeyId: 'key-b', groupId: 'group-b' })
  rememberOpenAIAccountForSession(ownerKey, 'shared-source', { systemAccountId: 'owner', apiKeyId: 'owner-key', groupId: 'owner-group' })
  rememberOpenAIAccountForSession(legacyKey, 'shared-source')
  const accounts = [
    createAccount('shared-source', { priority: 0 }),
    createAccount('scoped-target', { priority: 0 })
  ]

  assert.equal(
    migrateOpenAIAccountSessionAffinity('shared-source', 'scoped-target', { systemAccountId: 'grantee-a', groupId: 'group-a' }).migratedSessionCount,
    1,
    '授权账户本地迁移只应迁移当前被授权人当前分组的会话绑定'
  )
  assert.deepEqual(orderedIds(accounts, scopedKey), ['scoped-target', 'shared-source'])
  assert.deepEqual(orderedIds(accounts, otherGranteeKey), ['shared-source', 'scoped-target'])
  assert.deepEqual(orderedIds(accounts, ownerKey), ['shared-source', 'scoped-target'])
  assert.deepEqual(orderedIds(accounts, legacyKey), ['shared-source', 'scoped-target'])

  assert.equal(migrateOpenAIAccountSessionAffinity('shared-source', 'scoped-target').migratedSessionCount, 3)
  forgetOpenAIAccountForSession(scopedKey)
  forgetOpenAIAccountForSession(otherGranteeKey)
  forgetOpenAIAccountForSession(ownerKey)
  forgetOpenAIAccountForSession(legacyKey)
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
    concurrencyLimit: 20,
    priority: options.priority,
    superPriorityEnabled: options.superPriorityEnabled ?? false,
    fallbackEnabled: options.fallbackEnabled ?? false,
    qualityScore: options.qualityScore,
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-test',
    passthroughEnabled: true,
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

main()
