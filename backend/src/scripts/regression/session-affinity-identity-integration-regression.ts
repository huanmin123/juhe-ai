import { strict as assert } from 'node:assert'
import type { Request } from 'express'

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
  resolveOpenAIGatewaySessionAffinityKey
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
  testHintsDoNotCreateFormalAffinity()
  testHybridAffinityConsumesCachedCanonicalIdentity()
  await testConcurrentClaimHasOneWinner()
  console.log('session affinity identity integration regression: passed')
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

void main()
