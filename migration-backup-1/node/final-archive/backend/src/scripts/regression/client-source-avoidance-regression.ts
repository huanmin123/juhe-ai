import assert from 'node:assert/strict'

import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearCodexTurnAccountAvoidanceByFenceAsync,
  clearCodexTurnRetryStateForTest,
  getCodexTurnRetryMemoryStatsForTest
} from '../../modules/gateway/client-profiles/codex-turn-retry.service.js'
import {
  orderOpenAIAccountsByClientSourceAvoidance,
  rememberGatewayClientSourceFailure
} from '../../modules/gateway/client-profiles/client-source-avoidance.service.js'
import {
  resolveAnthropicGatewayClientStrategy,
  resolveGeminiGatewayClientStrategy,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyContext,
  type OpenAIGatewayClientStrategyIdentity
} from '../../modules/gateway/client-profiles/strategy.js'
import { resolveOpenAIGatewaySessionAffinityKeyFromClientSource } from '../../modules/gateway/runtime/session-affinity.service.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

runtimeConfig.runtimeStateDriver = 'memory'
clearCodexTurnRetryStateForTest()

const identity: OpenAIGatewayClientStrategyIdentity = {
  systemAccountId: 'system_source_avoidance',
  apiKeyId: 'api_source_avoidance',
  groupId: 'group_source_avoidance',
  endpoint: '/v1/messages',
  clientIp: '203.0.113.77'
}
const accounts = [account('acct_a'), account('acct_b')]

try {
  const claude = resolveAnthropicGatewayClientStrategy(request({
    method: 'POST',
    path: '/v1/messages',
    headers: {
      accept: 'text/event-stream',
      'user-agent': 'claude-cli/2.0',
      'anthropic-beta': 'claude-code-20250219',
      'x-claude-code-session-id': 'claude-source-session'
    }
  }), identity)
  assert.equal(claude.allowClientSourceAccountAvoidance, true, 'Claude Code 官方会话必须启用公共来源避让')
  assert.ok(claude.clientSourceAvoidanceStateKey)
  assert.ok(resolveOpenAIGatewaySessionAffinityKeyFromClientSource(claude.clientSource, affinityScope()), '官方会话必须可派生公共亲和键')

  const gemini = resolveGeminiGatewayClientStrategy(request({
    method: 'GET',
    path: '/v1beta/interactions/interaction-source-avoidance',
    headers: { accept: 'text/event-stream' }
  }), { ...identity, endpoint: '/v1beta/interactions/interaction-source-avoidance' })
  assert.equal(gemini.allowClientSourceAccountAvoidance, true, 'Gemini interaction 资源必须启用公共来源避让')
  assert.ok(gemini.clientSourceAvoidanceStateKey)
  assert.ok(resolveOpenAIGatewaySessionAffinityKeyFromClientSource(gemini.clientSource, affinityScope()), 'interaction 资源必须可派生公共亲和键')

  const fallback = resolveOpenAIGatewayClientStrategy(request({
    method: 'POST', path: '/v1/chat/completions', headers: {}
  }), { ...identity, endpoint: '/v1/chat/completions' })
  assert.equal(fallback.clientSource?.kind, 'ip_api_key_fallback')
  assert.equal(fallback.allowClientSourceAccountAvoidance, true, '已知 IP + API Key 必须作为最后的来源保护桶')
  assert.equal(resolveOpenAIGatewaySessionAffinityKeyFromClientSource(fallback.clientSource, affinityScope()), undefined, 'IP 软桶绝不能建立会话亲和')

  for (const strategy of [claude, gemini, fallback]) {
    const activation = rememberGatewayClientSourceFailure(strategy, 'acct_a', {
      evidence: 'committed_retry_signal',
      observationId: `${strategy.clientProfile}:activation`
    })?.activation
    assert(activation, `${strategy.clientProfile} 来源失败必须产生独立 activation`)
    assert.match(activation.sourceFenceId, /^[0-9a-f-]{36}$/i, 'activation 必须携带不可复用 fence token')
    assert.deepEqual(
      orderOpenAIAccountsByClientSourceAvoidance(accounts, strategy).accounts.map((item) => item.id),
      ['acct_b', 'acct_a'],
      `${strategy.clientProfile} 只能避让自身来源下的失败账号`
    )
  }

  const fenceStrategy = sourceStrategy('source-fence-reuse')
  const first = rememberGatewayClientSourceFailure(fenceStrategy, 'acct_a', {
    evidence: 'committed_retry_signal', observationId: 'first'
  })!
  assert(first.activation)
  assert.equal(await clearCodexTurnAccountAvoidanceByFenceAsync({
    stateKey: first.stateKey,
    accountId: 'acct_a',
    sourceGeneration: first.activation.sourceGeneration,
    sourceFenceId: first.activation.sourceFenceId
  }), true)
  const second = rememberGatewayClientSourceFailure(fenceStrategy, 'acct_a', {
    evidence: 'committed_retry_signal', observationId: 'second'
  })!
  assert(second.activation)
  assert.notEqual(second.activation.sourceFenceId, first.activation.sourceFenceId, '新 activation 不能复用旧 fence token')
  assert.equal(await clearCodexTurnAccountAvoidanceByFenceAsync({
    stateKey: first.stateKey,
    accountId: 'acct_a',
    sourceGeneration: second.activation.sourceGeneration,
    sourceFenceId: first.activation.sourceFenceId
  }), false, '旧 fence 即使伪造新 generation 也不得清除新避让')

  for (let index = 0; index < 5_128; index += 1) {
    const activation = rememberGatewayClientSourceFailure(sourceStrategy(`source-cap-${index}`), 'acct_a', {
      evidence: 'committed_retry_signal', observationId: `cap-${index}`
    })?.activation
    assert(activation)
  }
  const memoryStats = getCodexTurnRetryMemoryStatsForTest()
  assert(memoryStats.stateEntries <= 5_000, '来源避让状态必须受容量上限保护')
  assert(memoryStats.generationTombstones <= 5_000, 'generation tombstone 必须受容量上限保护')

  console.log('客户端来源避让回归通过：Claude/Gemini/IP 统一来源、亲和资格、fence token 隔离与高基数容量边界符合预期')
} finally {
  clearCodexTurnRetryStateForTest()
}

function sourceStrategy(stateKey: string): OpenAIGatewayClientStrategyContext {
  return {
    clientProfile: 'generic_openai',
    requestClientCompatibility: 'openai_standard',
    downstreamProtocol: 'json',
    upstreamAdapter: 'openai_mixed',
    codexCompactionExpected: false,
    clientSourceAvoidanceStateKey: stateKey,
    retryCoordination: { preCommitFailureSignal: 'http_error', committedFailureSignal: 'disconnect' },
    allowClientSourceAccountAvoidance: true,
    allowCodexTurnAccountAvoidance: false
  }
}

function affinityScope() {
  return {
    systemAccountId: identity.systemAccountId,
    apiKeyId: identity.apiKeyId,
    groupId: identity.groupId,
    routeStrategyId: 'route_source_avoidance',
    providerProtocolProfileId: 'pool_source_avoidance'
  }
}

function account(id: string): UpstreamAccount {
  return { id, name: id, priority: 0 } as UpstreamAccount
}

function request(input: { method: string; path: string; headers: Record<string, string> }): Request {
  return {
    method: input.method,
    path: input.path,
    originalUrl: input.path,
    headers: input.headers,
    header(name: string) {
      const value = input.headers[name.toLowerCase()]
        ?? Object.entries(input.headers).find(([key]) => key.toLowerCase() === name.toLowerCase())?.[1]
      return value
    }
  } as Request
}
