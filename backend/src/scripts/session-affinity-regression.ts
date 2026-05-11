import { strict as assert } from 'node:assert'

import {
  forgetOpenAIAccountForSession,
  migrateOpenAIAccountSessionAffinity,
  orderOpenAIAccountsBySessionAffinity,
  rememberOpenAIAccountForSession
} from '../modules/gateway/openai-gateway-session-affinity.service.js'
import type { OpenAIAccountSecret } from '../storage/repositories.js'

function main(): void {
  testMissingBoundAccountDoesNotAffectCandidates()
  testAffinityDoesNotPromoteAcrossPriority()
  testAffinityDoesNotPromoteFallbackOverPrimary()
  testAffinityDoesNotPromoteOverBetterQuality()
  testAffinityPromotesWithinSameAvailabilityBucket()
  testScopedMigrationOnlyMovesMatchingBindings()
  console.log('OpenAI session affinity regression passed')
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
    type: 'api_key',
    status: 'active',
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

main()
