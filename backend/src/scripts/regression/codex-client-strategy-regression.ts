import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  resolveOpenAIGatewayClientStrategy
} from '../../modules/gateway/openai-gateway-client-strategy.js'
import {
  clearCodexTurnRetryStateForTest,
  getCodexTurnRetryStateForTest,
  orderOpenAIAccountsByCodexTurnAvoidance,
  rememberCodexTurnStreamFailure
} from '../../modules/gateway/openai-gateway-codex-turn-retry.service.js'
import type { UpstreamAccount } from '../../modules/gateway/openai-gateway-route-helpers.js'
import type { GatewayRawBodyRequest } from '../../modules/gateway/openai-gateway-request-body.js'

const identity = {
  systemAccountId: 'sys_a',
  apiKeyId: 'key_a',
  groupId: 'group_a',
  endpoint: 'POST /v1/responses'
}

function main(): void {
  clearCodexTurnRetryStateForTest()
  testCodexTurnProfileRequiresPreciseTurnId()
  testSessionOnlyDoesNotBecomeCodex()
  testInvalidTurnMetadataDoesNotBecomeCodex()
  testNonCodexMetadataShapesDoNotFallback()
  testRawBodyHashIsPartOfTurnStateKey()
  testFourthCodexRetryAvoidsFailedAccounts()
  testAllFailedAccountsBypassAvoidance()
  testMissingTurnStateDoesNotAvoidAccounts()
  testNonResponsesStreamDoesNotUseCodexProfile()
  console.log('Codex 客户端策略回归通过：精确 turn_id 识别、无 fallback、非法/非 Codex metadata 不升级、body hash 隔离、第 4 次 turn 级切号、状态丢失不避让和非 Responses 隔离符合预期')
}

function testCodexTurnProfileRequiresPreciseTurnId(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'hello',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({
      session_id: 'session_a',
      thread_id: 'thread_a',
      turn_id: 'turn_a'
    })
  }), identity)

  assert.equal(strategy.clientProfile, 'codex')
  assert.equal(strategy.downstreamProtocol, 'responses_sse')
  assert.equal(strategy.codexTurn?.turnId, 'turn_a')
  assert.equal(strategy.codexTurn?.sessionId, 'session_a')
  assert.equal(strategy.codexTurn?.threadId, 'thread_a')
  assert.equal(strategy.allowCodexStreamClientRetry, true)
  assert.equal(strategy.allowCodexTurnAccountAvoidance, true)
}

function testSessionOnlyDoesNotBecomeCodex(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'hello',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({
      session_id: 'session_only',
      thread_id: 'thread_only'
    }),
    'x-client-request-id': 'client-request-a',
    session_id: 'legacy-session-a'
  }), identity)

  assert.equal(strategy.clientProfile, 'generic_openai')
  assert.equal(strategy.codexTurn, undefined)
  assert.equal(strategy.allowCodexStreamClientRetry, false)
}

function testInvalidTurnMetadataDoesNotBecomeCodex(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'hello',
    stream: true
  }, {
    'x-codex-turn-metadata': '{not-json',
    'user-agent': 'codex_cli_rs/0.125.0'
  }), identity)

  assert.equal(strategy.clientProfile, 'generic_openai')
  assert.equal(strategy.codexTurn, undefined)
  assert.equal(strategy.allowCodexTurnAccountAvoidance, false)
}

function testNonCodexMetadataShapesDoNotFallback(): void {
  const cases: Array<[string, string]> = [
    ['camelCase turnId', JSON.stringify({ turnId: 'turn_camel' })],
    ['url encoded json', encodeURIComponent(JSON.stringify({ turn_id: 'turn_encoded' }))],
    ['array json', JSON.stringify([{ turn_id: 'turn_array' }])]
  ]

  for (const [label, metadata] of cases) {
    const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
      model: 'gpt-5.3-codex',
      input: label,
      stream: true
    }, {
      'x-codex-turn-metadata': metadata,
      'user-agent': 'codex_cli_rs/0.125.0'
    }), identity)

    assert.equal(strategy.clientProfile, 'generic_openai', label)
    assert.equal(strategy.codexTurn, undefined, label)
    assert.equal(strategy.allowCodexStreamClientRetry, false, label)
    assert.equal(strategy.allowCodexTurnAccountAvoidance, false, label)
  }
}

function testRawBodyHashIsPartOfTurnStateKey(): void {
  const reqA = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'first payload',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_same' })
  })
  const reqB = createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'changed payload',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_same' })
  })

  const strategyA = resolveOpenAIGatewayClientStrategy(reqA, identity)
  const strategyB = resolveOpenAIGatewayClientStrategy(reqB, identity)

  assert(strategyA.codexTurn?.stateKey, '请求 A 应解析出 Codex turn key')
  assert(strategyB.codexTurn?.stateKey, '请求 B 应解析出 Codex turn key')
  assert.notEqual(strategyA.codexTurn.stateKey, strategyB.codexTurn.stateKey)
  assert.notEqual(strategyA.codexTurn.rawBodyHash, strategyB.codexTurn.rawBodyHash)
}

function testFourthCodexRetryAvoidsFailedAccounts(): void {
  clearCodexTurnRetryStateForTest()
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'same turn retry',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_retry' })
  }), identity)
  assert(strategy.codexTurn?.stateKey, 'Codex strategy should include turn state key')

  rememberCodexTurnStreamFailure(strategy, 'acct_a', { errorCode: 'upstream_retryable_error' })
  rememberCodexTurnStreamFailure(strategy, 'acct_a', { errorCode: 'upstream_retryable_error' })
  rememberCodexTurnStreamFailure(strategy, 'acct_a', { errorCode: 'upstream_retryable_error' })

  const state = getCodexTurnRetryStateForTest(strategy.codexTurn.stateKey)
  assert.equal(state?.failureCount, 3)
  assert.deepEqual(state?.failedAccountIds, ['acct_a'])

  const accounts = [account('acct_a'), account('acct_b'), account('acct_c')]
  const avoidance = orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy)
  assert.equal(avoidance.applied, true)
  assert.equal(avoidance.failureCount, 3)
  assert.deepEqual(avoidance.avoidedAccountIds, ['acct_a'])
  assert.deepEqual(avoidance.accounts.map((item) => item.id), ['acct_b', 'acct_c', 'acct_a'])
}

function testAllFailedAccountsBypassAvoidance(): void {
  clearCodexTurnRetryStateForTest()
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'all failed',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_all_failed' })
  }), identity)

  rememberCodexTurnStreamFailure(strategy, 'acct_a')
  rememberCodexTurnStreamFailure(strategy, 'acct_b')
  rememberCodexTurnStreamFailure(strategy, 'acct_c')

  const accounts = [account('acct_a'), account('acct_b'), account('acct_c')]
  const avoidance = orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy)
  assert.equal(avoidance.applied, false)
  assert.equal(avoidance.bypassedAllAvoided, true)
  assert.deepEqual(avoidance.accounts.map((item) => item.id), ['acct_a', 'acct_b', 'acct_c'])
}

function testMissingTurnStateDoesNotAvoidAccounts(): void {
  clearCodexTurnRetryStateForTest()
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'state lost',
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_state_lost' })
  }), identity)

  const accounts = [account('acct_a'), account('acct_b')]
  const avoidance = orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy)
  assert.equal(avoidance.applied, false)
  assert.equal(avoidance.failureCount, 0)
  assert.equal(avoidance.bypassedAllAvoided, false)
  assert.deepEqual(avoidance.accounts.map((item) => item.id), ['acct_a', 'acct_b'])
}

function testNonResponsesStreamDoesNotUseCodexProfile(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/chat/completions', {
    model: 'gpt-4o-mini',
    messages: [{ role: 'user', content: 'hello' }],
    stream: true
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_chat' })
  }), {
    ...identity,
    endpoint: 'POST /v1/chat/completions'
  })

  assert.equal(strategy.downstreamProtocol, 'chat_completions_sse')
  assert.equal(strategy.clientProfile, 'generic_openai')
  assert.equal(strategy.codexTurn, undefined)
}

function createRequest(
  originalUrl: string,
  body: Record<string, unknown>,
  headers: Record<string, string> = {}
): GatewayRawBodyRequest {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  const normalizedHeaders = {
    'content-type': 'application/json',
    accept: body.stream === true ? 'text/event-stream' : 'application/json',
    ...headers
  }
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: normalizedHeaders,
    body,
    rawBody,
    header(name: string): string | undefined {
      const lowerName = name.toLowerCase()
      return Object.entries(normalizedHeaders)
        .find(([headerName]) => headerName.toLowerCase() === lowerName)?.[1]
    }
  } as unknown as Request & GatewayRawBodyRequest
}

function account(id: string): UpstreamAccount {
  return { id, name: id } as UpstreamAccount
}

main()
