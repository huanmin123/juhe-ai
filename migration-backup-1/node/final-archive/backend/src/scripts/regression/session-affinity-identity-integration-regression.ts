import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import type { RedisCommandClient } from '../../shared/redis-client.js'
import type { ApiKeyHybridRoutingConfig } from '../../domain/types.js'
import {
  clearHybridRouteAffinityForTest,
  applyHybridRouteAffinity
} from '../../modules/gateway/hybrid/affinity.service.js'
import {
  resolveGatewaySessionIdentity
} from '../../modules/gateway/session-identity/index.js'
import {
  claimOpenAIAccountForSessionAsync,
  forgetOpenAIAccountForSession,
  resolveOpenAIGatewaySessionAffinityKey,
  setOpenAISessionAffinityRedisClientForTest
} from '../../modules/gateway/runtime/session-affinity.service.js'

const scope = {
  clientProfile: 'codex',
  systemAccountId: 'system-a',
  apiKeyId: 'api-key-a'
}

const hybridConfig: ApiKeyHybridRoutingConfig = {
  scoringModel: 'scoring-model',
  scoringContextMode: 'full_request',
  qualityPreference: 'balanced',
  scoringTimeoutMs: 10_000,
  scoringFallbackMaxLevel: 5,
  scoringCacheEnabled: true,
  scoringCacheTtlSeconds: 300,
  cacheAffinityEnabled: true,
  affinityTtlSeconds: 900,
  switchMinLevelDelta: 2,
  downgradeConsecutiveLowCount: 2,
  levelRoutes: [
    { minLevel: 3, maxLevel: 6, targetModel: 'standard-model', enabled: true },
    { minLevel: 7, maxLevel: 8, targetModel: 'advanced-model', enabled: true }
  ]
}

async function main(): Promise<void> {
  testCanonicalIdentityScopesNormalAffinity()
  testClientSpecificAffinityNamespaces()
  testHintsDoNotCreateFormalAffinity()
  testHybridAffinityConsumesCachedCanonicalIdentity()
  await testConcurrentClaimHasOneWinner()
  await testRedisConcurrentClaimHasOneWinner()
  console.log('session affinity identity integration regression: passed')
}

async function testRedisConcurrentClaimHasOneWinner(): Promise<void> {
  const originalCacheDriver = runtimeConfig.cacheDriver
  const client = new AtomicSessionAffinityRedisClient()
  runtimeConfig.cacheDriver = 'redis'
  setOpenAISessionAffinityRedisClientForTest(client)
  try {
    const identity = resolveGatewaySessionIdentity(sessionRequest('redis-concurrent-session'), scope)
    const affinityKey = resolveOpenAIGatewaySessionAffinityKey(identity, {
      systemAccountId: scope.systemAccountId,
      apiKeyId: scope.apiKeyId,
      routeStrategyId: 'route-a',
      groupId: 'group-a'
    })
    assert(affinityKey)
    const affinityScope = {
      systemAccountId: scope.systemAccountId,
      apiKeyId: scope.apiKeyId,
      groupId: 'group-a'
    }
    const winners = await Promise.all([
      claimOpenAIAccountForSessionAsync(affinityKey, 'redis-account-a', affinityScope),
      claimOpenAIAccountForSessionAsync(affinityKey, 'redis-account-b', affinityScope),
      claimOpenAIAccountForSessionAsync(affinityKey, 'redis-account-c', affinityScope)
    ])
    assert.equal(new Set(winners).size, 1, 'Redis CAS 并发首写必须只产生一个粘黏账号')
    assert(client.bindingCompareSetCalls >= 3, '回归必须真实经过 Redis Lua CAS 首写竞争路径')
  } finally {
    setOpenAISessionAffinityRedisClientForTest(undefined)
    runtimeConfig.cacheDriver = originalCacheDriver
  }
}

function testClientSpecificAffinityNamespaces(): void {
  const rawSessionId = 'shared-raw-session'
  const codexIdentity = resolveGatewaySessionIdentity(sessionRequest(rawSessionId), scope)
  const claudeRequest = {
    method: 'POST',
    originalUrl: '/v1/messages',
    headers: { 'x-claude-code-session-id': rawSessionId },
    body: {}
  } as unknown as Request
  const claudeIdentity = resolveGatewaySessionIdentity(claudeRequest, {
    ...scope,
    clientProfile: 'claude_code'
  })
  const keyScope = {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    routeStrategyId: 'route-a',
    groupId: 'group-a'
  }
  const codexKey = resolveOpenAIGatewaySessionAffinityKey(codexIdentity, keyScope)
  const claudeKey = resolveOpenAIGatewaySessionAffinityKey(claudeIdentity, keyScope)
  assert(codexKey)
  assert(claudeKey)
  assert.notEqual(codexKey, claudeKey, '不同客户端必须使用隔离的会话 namespace，不能共享粘黏槽')
}

function testCanonicalIdentityScopesNormalAffinity(): void {
  const identity = resolveGatewaySessionIdentity(sessionRequest('official-session'), scope)
  const groupAKey = resolveOpenAIGatewaySessionAffinityKey(identity, {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    routeStrategyId: 'route-a',
    groupId: 'group-a'
  })
  const repeatedIdentity = resolveGatewaySessionIdentity(sessionRequest('official-session'), scope)
  const repeatedKey = resolveOpenAIGatewaySessionAffinityKey(repeatedIdentity, {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    routeStrategyId: 'route-a',
    groupId: 'group-a'
  })
  const groupBKey = resolveOpenAIGatewaySessionAffinityKey(identity, {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    routeStrategyId: 'route-a',
    groupId: 'group-b'
  })

  assert.match(identity.conversationKey ?? '', /^conv_v1_/)
  assert.equal(groupAKey, repeatedKey, '同一官方会话和路由池必须生成稳定亲和键')
  assert.notEqual(groupAKey, groupBKey, '同一会话在不同分组必须隔离亲和绑定')
}

function testHintsDoNotCreateFormalAffinity(): void {
  const request = {
    method: 'POST',
    originalUrl: '/v1/responses',
    headers: { 'x-client-request-id': 'request-only' },
    body: {
      previous_response_id: 'response-before',
      prompt_cache_key: 'cache-only'
    }
  } as unknown as Request
  const identity = resolveGatewaySessionIdentity(request, {
    clientProfile: 'generic',
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId
  })

  assert.equal(identity.conversationKey, undefined)
  assert.equal(resolveOpenAIGatewaySessionAffinityKey(identity, {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    groupId: 'group-a'
  }), undefined, '续接边和缓存提示不能提升为正式会话亲和')
}

function testHybridAffinityConsumesCachedCanonicalIdentity(): void {
  clearHybridRouteAffinityForTest()
  const firstRequest = sessionRequest('hybrid-session')
  resolveGatewaySessionIdentity(firstRequest, scope)
  const advancedRoute = hybridConfig.levelRoutes[1]!
  const standardRoute = hybridConfig.levelRoutes[0]!
  const first = applyHybridRouteAffinity({
    req: firstRequest,
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    config: hybridConfig,
    level: 8,
    route: advancedRoute
  })
  assert.equal(first.applied, false)

  const secondRequest = sessionRequest('hybrid-session')
  resolveGatewaySessionIdentity(secondRequest, scope)
  const second = applyHybridRouteAffinity({
    req: secondRequest,
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    config: hybridConfig,
    level: 5,
    route: standardRoute
  })
  assert.equal(second.applied, true, '混合路由应复用统一身份缓存，而不是自行扫描 Request')
  assert.equal(second.route.targetModel, advancedRoute.targetModel)
  clearHybridRouteAffinityForTest()
}

async function testConcurrentClaimHasOneWinner(): Promise<void> {
  const identity = resolveGatewaySessionIdentity(sessionRequest('concurrent-session'), scope)
  const affinityKey = resolveOpenAIGatewaySessionAffinityKey(identity, {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    routeStrategyId: 'route-a',
    groupId: 'group-a'
  })
  assert(affinityKey)
  forgetOpenAIAccountForSession(affinityKey)

  const affinityScope = {
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    groupId: 'group-a'
  }
  const winners = await Promise.all([
    claimOpenAIAccountForSessionAsync(affinityKey, 'account-a', affinityScope),
    claimOpenAIAccountForSessionAsync(affinityKey, 'account-b', affinityScope),
    claimOpenAIAccountForSessionAsync(affinityKey, 'account-c', affinityScope)
  ])
  assert.deepEqual([...new Set(winners)], ['account-a'], '并发首批请求必须由首写账号赢得同一亲和槽')
  forgetOpenAIAccountForSession(affinityKey)
}

function sessionRequest(sessionId: string): Request {
  const headers: Record<string, string> = { 'session-id': sessionId }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    headers,
    body: {},
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

class AtomicSessionAffinityRedisClient implements RedisCommandClient {
  private readonly values = new Map<string, string>()
  isOpen = true
  isReady = true
  bindingCompareSetCalls = 0

  async connect(): Promise<void> {}
  async get(key: string): Promise<string | null> {
    await new Promise<void>((resolve) => setImmediate(resolve))
    return this.values.get(key) ?? null
  }
  async set(key: string, value: string): Promise<string> {
    this.values.set(key, value)
    return 'OK'
  }
  async del(key: string): Promise<number> {
    return this.values.delete(key) ? 1 : 0
  }
  async eval(script: string, options: { keys: string[]; arguments: string[] }): Promise<number> {
    await new Promise<void>((resolve) => setImmediate(resolve))
    const key = options.keys[0]!
    if (script.includes('old_index_count')) {
      this.bindingCompareSetCalls += 1
      const current = this.values.get(key)
      const expected = options.arguments[0]!
      if ((expected === '' && current !== undefined) || (expected !== '' && current !== expected)) return 0
      this.values.set(key, options.arguments[1]!)
      return 1
    }
    if (script.includes("redis.call('DEL', KEYS[1])")) {
      if (this.values.get(key) !== options.arguments[0]) return 0
      this.values.delete(key)
      return 1
    }
    return this.values.get(key) === options.arguments[0] ? 1 : 0
  }
  async sendCommand(): Promise<unknown> { return undefined }
  async quit(): Promise<void> {}
  destroy(): void {}
  on(): this { return this }
}

void main()
