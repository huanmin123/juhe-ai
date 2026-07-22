import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import type { Request } from 'express'

import {
  resolveOpenAIGatewayClientStrategy
} from '../../modules/gateway/client-profiles/strategy.js'
import {
  clearCodexTurnRetryStateForTest,
  getCodexTurnRetryStateForTest,
  orderOpenAIAccountsByCodexTurnAvoidance,
  rememberCodexTurnStreamFailure
} from '../../modules/gateway/client-profiles/codex-turn-retry.service.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import type { GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'

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
  testRawBodyHashDoesNotSplitTurnStateKey()
  testLargeRawBodyHashUsesBoundedSample()
  testMissingRawBodyHashDoesNotStringifyFullBody()
  testCodexTurnStateKeyIgnoresGroupAndKeepsApiKeyBoundary()
  testSecondCodexRetryAvoidsFailedAccounts()
  testChangedBodyRetryAvoidsFailedAccount()
  testAllFailedAccountsBypassAvoidance()
  testMissingTurnStateDoesNotAvoidAccounts()
  testNonResponsesStreamDoesNotUseCodexProfile()
  testCodexCompactRequestUsesCodexProfile()
  console.log('Codex 客户端策略回归通过：精确 turn_id 识别、无 fallback、非法/非 Codex metadata 不升级、body hash 不切分 turn 状态、同一 turn 失败两次后才切号、状态丢失不避让和非 Responses 隔离符合预期')
}

function testLargeRawBodyHashUsesBoundedSample(): void {
  const rawBodyA = Buffer.alloc(768 * 1024, 'a')
  const rawBodyB = Buffer.from(rawBodyA)
  rawBodyB[256 * 1024] = 'b'.charCodeAt(0)
  const headers = {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_large_body' })
  }
  const strategyA = resolveOpenAIGatewayClientStrategy(createRequestWithRawBody('/v1/responses', rawBodyA, headers), identity)
  const strategyB = resolveOpenAIGatewayClientStrategy(createRequestWithRawBody('/v1/responses', rawBodyB, headers), identity)

  assert(strategyA.codexTurn?.rawBodyHash, '大请求体应生成 Codex body hash')
  assert(strategyB.codexTurn?.rawBodyHash, '变更后的大请求体应生成 Codex body hash')
  assert.notEqual(strategyA.codexTurn.rawBodyHash, createHash('sha256').update(rawBodyA).digest('hex'), '大请求体不应同步计算完整 body SHA-256')
  assert.notEqual(strategyA.codexTurn.rawBodyHash, strategyB.codexTurn.rawBodyHash, '大请求体采样 hash 应能感知采样窗口内的内容变化')
}

function testMissingRawBodyHashDoesNotStringifyFullBody(): void {
  const body = buildHashTrapBody()
  const strategy = resolveOpenAIGatewayClientStrategy(createRequestWithoutRawBody('/v1/responses', body, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_hash_trap' })
  }), identity)

  assert(strategy.codexTurn?.rawBodyHash, '缺少 rawBody 的请求仍应生成有界 body hash')
  assert(strategy.codexTurn?.stateKey, '缺少 rawBody 的请求仍应生成 Codex turn state key')
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
  assert.equal(strategy.requestClientCompatibility, 'codex_responses')
  assert.equal(strategy.downstreamProtocol, 'responses_sse')
  assert.equal(strategy.codexTurn?.turnId, 'turn_a')
  assert.equal(strategy.codexTurn?.sessionId, 'session_a')
  assert.equal(strategy.codexTurn?.threadId, 'thread_a')
  assert.equal(strategy.retryCoordination.preCommitFailureSignal, 'protocol_error_event')
  assert.equal(strategy.retryCoordination.committedFailureSignal, 'protocol_error_event')
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
    session_id: 'session-a'
  }), identity)

  assert.equal(strategy.clientProfile, 'generic_openai')
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
  assert.equal(strategy.codexTurn, undefined)
  assert.equal(strategy.retryCoordination.preCommitFailureSignal, 'http_error')
  assert.equal(strategy.retryCoordination.committedFailureSignal, 'disconnect')
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
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
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
    assert.equal(strategy.requestClientCompatibility, 'openai_standard', label)
    assert.equal(strategy.codexTurn, undefined, label)
    assert.equal(strategy.retryCoordination.preCommitFailureSignal, 'http_error', label)
    assert.equal(strategy.retryCoordination.committedFailureSignal, 'disconnect', label)
    assert.equal(strategy.allowCodexTurnAccountAvoidance, false, label)
  }
}

function testRawBodyHashDoesNotSplitTurnStateKey(): void {
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
  assert.equal(strategyA.codexTurn.stateKey, strategyB.codexTurn.stateKey, '同一 Codex turn 不应因为重试 body 变化切开失败状态')
  assert.notEqual(strategyA.codexTurn.rawBodyHash, strategyB.codexTurn.rawBodyHash)
}

function testCodexTurnStateKeyIgnoresGroupAndKeepsApiKeyBoundary(): void {
  const body = {
    model: 'gpt-5.3-codex',
    input: 'same turn across groups',
    stream: true
  }
  const headers = {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_group_route' })
  }
  const strategyGroupA = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', body, headers), {
    ...identity,
    groupId: 'group_a'
  })
  const strategyGroupB = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', body, headers), {
    ...identity,
    groupId: 'group_b'
  })
  const strategyApiKeyB = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', body, headers), {
    ...identity,
    apiKeyId: 'key_b',
    groupId: 'group_b'
  })

  assert(strategyGroupA.codexTurn?.stateKey, '分组 A 应解析出 Codex turn key')
  assert(strategyGroupB.codexTurn?.stateKey, '分组 B 应解析出 Codex turn key')
  assert(strategyApiKeyB.codexTurn?.stateKey, 'API Key B 应解析出 Codex turn key')
  assert.equal(strategyGroupA.codexTurn.stateKey, strategyGroupB.codexTurn.stateKey, '同一 API Key 下不同分组不应切开 Codex turn 状态')
  assert.notEqual(strategyGroupA.codexTurn.stateKey, strategyApiKeyB.codexTurn.stateKey, '不同 API Key 仍应隔离 Codex turn 状态')
}

function testSecondCodexRetryAvoidsFailedAccounts(): void {
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

  const state = getCodexTurnRetryStateForTest(strategy.codexTurn.stateKey)
  assert.equal(state?.failureCount, 1)
  assert.deepEqual(state?.failedAccountIds, ['acct_a'])

  const accounts = [account('acct_a'), account('acct_b'), account('acct_c')]
  const firstFailureAvoidance = orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy)
  assert.equal(firstFailureAvoidance.applied, false)
  assert.equal(firstFailureAvoidance.failureCount, 1)
  assert.deepEqual(firstFailureAvoidance.accounts.map((item) => item.id), ['acct_a', 'acct_b', 'acct_c'])

  rememberCodexTurnStreamFailure(strategy, 'acct_a', { errorCode: 'upstream_retryable_error' })
  const avoidance = orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy)
  assert.equal(avoidance.applied, true)
  assert.equal(avoidance.failureCount, 2)
  assert.deepEqual(avoidance.avoidedAccountIds, ['acct_a'])
  assert.deepEqual(avoidance.accounts.map((item) => item.id), ['acct_b', 'acct_c', 'acct_a'])
}

function testChangedBodyRetryAvoidsFailedAccount(): void {
  clearCodexTurnRetryStateForTest()
  const headers = {
    'x-codex-turn-metadata': JSON.stringify({
      turn_id: 'turn_changed_body_retry',
      session_id: 'session_changed_body_retry',
      thread_id: 'thread_changed_body_retry'
    })
  }
  const firstStrategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'codex first request body',
    stream: true
  }, headers), identity)
  const retryStrategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.3-codex',
    input: 'codex rebuilt retry body',
    stream: true
  }, headers), identity)

  assert(firstStrategy.codexTurn?.stateKey, '首次请求应解析出 Codex turn key')
  assert(retryStrategy.codexTurn?.stateKey, '重试请求应解析出 Codex turn key')
  assert.equal(firstStrategy.codexTurn.stateKey, retryStrategy.codexTurn.stateKey, '同一 turn 的重试 body 变化不应切开状态')
  assert.notEqual(firstStrategy.codexTurn.rawBodyHash, retryStrategy.codexTurn.rawBodyHash, '测试应覆盖 body hash 变化')

  rememberCodexTurnStreamFailure(firstStrategy, 'acct_a', { errorCode: 'upstream_retryable_error' })

  const state = getCodexTurnRetryStateForTest(retryStrategy.codexTurn.stateKey)
  assert.equal(state?.failureCount, 1)
  assert.deepEqual(state?.failedAccountIds, ['acct_a'])

  const firstFailureAvoidance = orderOpenAIAccountsByCodexTurnAvoidance([account('acct_a'), account('acct_b')], retryStrategy)
  assert.equal(firstFailureAvoidance.applied, false)
  assert.deepEqual(firstFailureAvoidance.accounts.map((item) => item.id), ['acct_a', 'acct_b'])

  rememberCodexTurnStreamFailure(retryStrategy, 'acct_a', { errorCode: 'upstream_retryable_error' })
  const avoidance = orderOpenAIAccountsByCodexTurnAvoidance([account('acct_a'), account('acct_b')], retryStrategy)
  assert.equal(avoidance.applied, true)
  assert.equal(avoidance.failureCount, 2)
  assert.deepEqual(avoidance.accounts.map((item) => item.id), ['acct_b', 'acct_a'])
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
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
  assert.equal(strategy.codexTurn, undefined)
}

function testCodexCompactRequestUsesCodexProfile(): void {
  const genericStrategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses/compact', {
    model: 'gpt-5.5',
    input: 'compact'
  }), {
    ...identity,
    endpoint: 'POST /v1/responses/compact'
  })
  assert.equal(genericStrategy.clientProfile, 'generic_openai')
  assert.equal(genericStrategy.requestClientCompatibility, 'openai_standard')
  assert.equal(genericStrategy.codexCompactionExpected, false)

  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses/compact', {
    model: 'gpt-5.5',
    input: [
      { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'compact' }] },
      { type: 'compaction_trigger' }
    ]
  }, {
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_compact' })
  }), {
    ...identity,
    endpoint: 'POST /v1/responses/compact'
  })

  assert.equal(strategy.clientProfile, 'codex')
  assert.equal(strategy.requestClientCompatibility, 'codex_responses')
  assert.equal(strategy.downstreamProtocol, 'json')
  assert.equal(strategy.codexCompactionExpected, true)
  assert.equal(strategy.codexTurn?.turnId, 'turn_compact')
  assert.equal(strategy.retryCoordination.preCommitFailureSignal, 'http_error')
  assert.equal(strategy.retryCoordination.committedFailureSignal, 'disconnect')
  assert.equal(strategy.allowCodexTurnAccountAvoidance, true)
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

function createRequestWithRawBody(
  originalUrl: string,
  rawBody: Buffer,
  headers: Record<string, string> = {}
): GatewayRawBodyRequest {
  const normalizedHeaders = {
    'content-type': 'application/json',
    accept: 'text/event-stream',
    ...headers
  }
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: normalizedHeaders,
    body: { stream: true },
    rawBody,
    header(name: string): string | undefined {
      const lowerName = name.toLowerCase()
      return Object.entries(normalizedHeaders)
        .find(([headerName]) => headerName.toLowerCase() === lowerName)?.[1]
    }
  } as unknown as Request & GatewayRawBodyRequest
}

function createRequestWithoutRawBody(
  originalUrl: string,
  body: Record<string, unknown>,
  headers: Record<string, string> = {}
): GatewayRawBodyRequest {
  const normalizedHeaders = {
    'content-type': 'application/json',
    accept: 'text/event-stream',
    ...headers
  }
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: normalizedHeaders,
    body,
    header(name: string): string | undefined {
      const lowerName = name.toLowerCase()
      return Object.entries(normalizedHeaders)
        .find(([headerName]) => headerName.toLowerCase() === lowerName)?.[1]
    }
  } as unknown as Request & GatewayRawBodyRequest
}

function buildHashTrapBody(): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: 'gpt-5.3-codex',
    stream: true
  }
  for (let index = 0; index < 80; index += 1) {
    body[`field_${index}`] = 'x'.repeat(1024)
  }
  Object.defineProperty(body, 'field_80_trap', {
    enumerable: true,
    get() {
      throw new Error('Codex body hash 不应读取超过字段上限后的属性')
    }
  })
  return body
}

function account(id: string): UpstreamAccount {
  return { id, name: id } as UpstreamAccount
}

main()
