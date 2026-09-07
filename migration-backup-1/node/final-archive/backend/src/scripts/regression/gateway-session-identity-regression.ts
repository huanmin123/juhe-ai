import assert from 'node:assert/strict'

import {
  deriveGatewaySessionAffinityKey,
  getGatewaySessionIdentity,
  resolveGatewaySessionIdentity,
  type GatewaySessionIdentityRequest,
  type GatewaySessionIdentityScope
} from '../../modules/gateway/session-identity/index.js'

const testSecret = 'gateway-session-identity-regression-secret'

function request(
  path: string,
  headers: Record<string, string | string[] | undefined> = {}
): GatewaySessionIdentityRequest {
  return { method: 'POST', originalUrl: path, path, headers }
}

function scope(clientProfile = 'generic'): GatewaySessionIdentityScope {
  return {
    clientProfile,
    systemAccountId: 'system-account-a',
    apiKeyId: 'api-key-a',
    hmacSecret: testSecret
  }
}

function main(): void {
  const codexRequest = request('/v1/responses', { 'session-id': 'codex-session' })
  const codex = resolveGatewaySessionIdentity(codexRequest, scope('codex'))
  assert.equal(codex.status, 'resolved')
  assert.equal(codex.sessionId, 'codex-session')
  assert.equal(codex.semanticNamespace, 'openai.codex.session')
  assert.match(codex.conversationKey ?? '', /^conv_v1_[A-Za-z0-9_-]+$/)
  assert.equal(getGatewaySessionIdentity(codexRequest), codex)

  const repeatedRequest = request('/responses/compact', { 'session-id': 'codex-session' })
  const repeated = resolveGatewaySessionIdentity(repeatedRequest, scope('codex'))
  assert.equal(repeated.conversationKey, codex.conversationKey)
  assert.equal(getGatewaySessionIdentity(repeatedRequest), repeated)

  const generic = resolveGatewaySessionIdentity(
    request('/chat/completions', { 'session-id': 'generic-session' }),
    scope()
  )
  assert.equal(generic.status, 'missing')
  assert.equal(generic.conversationKey, undefined)

  const claude = resolveGatewaySessionIdentity(
    request('/v1/messages', { 'x-claude-code-session-id': 'claude-session' }),
    scope('claude_code')
  )
  assert.equal(claude.status, 'resolved')
  assert.equal(claude.sessionId, 'claude-session')
  assert.equal(claude.semanticNamespace, 'anthropic.claude_code.session')

  const claudeHeaderOnGenericClient = resolveGatewaySessionIdentity(
    request('/v1/messages', { 'x-claude-code-session-id': 'claude-session' }),
    scope('generic')
  )
  assert.equal(claudeHeaderOnGenericClient.status, 'missing')

  const bodyOnlyRequest = {
    ...request('/responses'),
    body: {
      conversation: 'body-conversation',
      session_id: 'body-session',
      request: { session_id: 'gemini-session' },
      client_metadata: { session_id: 'codex-body-session' }
    }
  } as GatewaySessionIdentityRequest
  const bodyOnly = resolveGatewaySessionIdentity(
    bodyOnlyRequest,
    scope('codex')
  )
  assert.equal(bodyOnly.status, 'missing')
  assert.equal(bodyOnly.conversationKey, undefined)

  const nonSessionHeaders = resolveGatewaySessionIdentity(
    request('/responses', {
      'thread-id': 'thread-only',
      'turn-id': 'turn-only',
      'x-client-request-id': 'request-only',
      'x-openclaw-session-id': 'third-party-session'
    }),
    scope('codex')
  )
  assert.equal(nonSessionHeaders.status, 'missing')

  const duplicateConflictRequest = request('/responses')
  duplicateConflictRequest.headersDistinct = { 'session-id': ['session-a', 'session-b'] }
  const duplicateConflict = resolveGatewaySessionIdentity(duplicateConflictRequest, scope('codex'))
  assert.equal(duplicateConflict.status, 'conflict')
  assert.equal(duplicateConflict.conversationKey, undefined)

  const invalid = resolveGatewaySessionIdentity(
    request('/responses', { 'session-id': 'bad\nvalue' }),
    scope('codex')
  )
  assert.equal(invalid.status, 'invalid')
  assert.equal(invalid.conversationKey, undefined)

  const differentApiKey = resolveGatewaySessionIdentity(
    request('/responses', { 'session-id': 'codex-session' }),
    { ...scope('codex'), apiKeyId: 'api-key-b' }
  )
  assert.notEqual(differentApiKey.conversationKey, codex.conversationKey)

  const affinityA = deriveGatewaySessionAffinityKey(codex, {
    hmacSecret: testSecret,
    systemAccountId: 'system-account-a',
    apiKeyId: 'api-key-a',
    routeStrategyId: 'route-a',
    groupId: 'group-a',
    providerProtocolProfileId: 'profile-a'
  })
  const affinityB = deriveGatewaySessionAffinityKey(codex, {
    hmacSecret: testSecret,
    systemAccountId: 'system-account-a',
    apiKeyId: 'api-key-a',
    routeStrategyId: 'route-a',
    groupId: 'group-a',
    providerProtocolProfileId: 'profile-b'
  })
  assert.match(affinityA ?? '', /^aff_v1_[A-Za-z0-9_-]+$/)
  assert.notEqual(affinityA, affinityB)

  console.log('gateway header-only session identity regression: passed')
}

main()
