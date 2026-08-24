import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { AccountSummary } from '../../domain/types.js'
import { readPublishedAccountHealthJobsInput } from '../../modules/background/account-health-jobs-input.protocol.js'
import { publishAccountHealthJobsInputFromAccount, publishAccountHealthJobsProbeRequest } from '../../modules/background/account-health-jobs-input.service.js'
import { isJ1AccountHealthEndpointModeEligible, resolveJ1AccountHealthProbeProtocol } from '../../storage/account-health-jobs-input.repository.js'

const testRoot = resolve(process.env.JUHE_AI_TEST_TEMP_ROOT?.trim() || tmpdir())
const root = mkdtempSync(join(testRoot, 'juhe-ai-account-health-service-'))
try {
  const signingKey = Buffer.alloc(32, 9).toString('base64url')
  const account = {
    id: 'account-1',
    configRevision: 7,
    dispatchRevision: 3,
    providerCode: 'gpt',
    type: 'api_key',
    credentials: { api_key: 'sk-test' },
    status: 'active',
    schedulable: true,
    boundGroupId: 'group-1',
    healthCheckModel: 'gpt-test',
    healthCheckEndpointMode: 'chat_json'
  } as AccountSummary
  const path = publishAccountHealthJobsInputFromAccount({
    account,
    dispatchRevision: 3,
    inputVersion: 11,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const envelope = readPublishedAccountHealthJobsInput(path)
  const payload = JSON.parse(Buffer.from(envelope.payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(payload.account_id, 'account-1')
  assert.equal(payload.config_revision, 7)
  assert.equal(payload.dispatch_revision, 3)
  assert.equal(payload.provider, 'openai', 'GPT 物理 provider 必须规范化为冻结的 OpenAI v1 jobs provider')
  assert.equal(payload.type, 'api_key')
  assert.equal(payload.endpoint_mode, 'chat_json')
  assert.equal((payload.schedule as Record<string, unknown>).health_interval_ms, 3_600_000)
  assert.equal((payload.schedule as Record<string, unknown>).cooldown_failure_backoff_ms, 3_000)
  assert.equal((payload.schedule as Record<string, unknown>).max_pause_minutes, 2)
  assert.equal((payload.schedule as Record<string, unknown>).max_recovery_hours, 12)
  assert.equal((payload.eligibility as Record<string, unknown>).bound_group, true)
  assert.equal((payload.eligibility as Record<string, unknown>).temporary_unavailable_continuous_probe_enabled, true)
  assert.equal(Array.isArray(payload.api_keys), true)
  assert.equal(typeof (payload.api_keys as Array<Record<string, unknown>>)[0]?.credential, 'object')

  const responsesSsePath = publishAccountHealthJobsInputFromAccount({
    account: { ...account, healthCheckEndpointMode: 'responses_sse' } as AccountSummary,
    dispatchRevision: 3,
    inputVersion: 17,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const responsesSsePayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(responsesSsePath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(responsesSsePayload.endpoint_mode, 'responses_sse', 'GPT Responses SSE 必须可进入 Go J1 输入协议')
  assert.equal(isJ1AccountHealthEndpointModeEligible('api_key', 'responses_sse'), true)
  assert.equal(isJ1AccountHealthEndpointModeEligible('oauth', 'responses_sse'), true)
  const oauthSsePath = publishAccountHealthJobsInputFromAccount({
    account: {
      ...account,
      type: 'oauth',
      healthCheckEndpointMode: 'responses_sse',
      credentials: { access_token: 'oauth-access-token', expires_at: new Date(Date.now() + 60_000).toISOString() }
    } as AccountSummary,
    dispatchRevision: 3,
    inputVersion: 18,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const oauthSsePayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(oauthSsePath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(oauthSsePayload.endpoint_mode, 'responses_sse')

  const xaiOAuthPath = publishAccountHealthJobsInputFromAccount({
    account: {
      ...account,
      providerCode: 'xai',
      providerProtocolProfileId: 'profile_xai_openai_v1',
      type: 'oauth',
      healthCheckEndpointMode: 'responses_json',
      credentials: { access_token: 'xai-oauth-token', expires_at: new Date(Date.now() + 60_000).toISOString() }
    } as AccountSummary,
    dispatchRevision: 3,
    inputVersion: 181,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const xaiOAuthPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(xaiOAuthPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(xaiOAuthPayload.base_url, 'https://cli-chat-proxy.grok.com/v1')

  const anthropicAccount = {
    ...account,
    providerCode: 'anthropic',
    providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
    protocolCode: 'anthropic',
    protocolVersion: 'v1',
    healthCheckEndpointMode: 'messages_sse'
  } as AccountSummary
  assert.equal(resolveJ1AccountHealthProbeProtocol(anthropicAccount), 'anthropic')
  const anthropicPath = publishAccountHealthJobsInputFromAccount({
    account: anthropicAccount,
    dispatchRevision: 3,
    inputVersion: 19,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const anthropicPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(anthropicPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(anthropicPayload.provider, 'anthropic')
  assert.equal(anthropicPayload.provider_protocol_profile_id, 'profile_anthropic_anthropic_v1')
  assert.equal(anthropicPayload.endpoint_mode, 'messages_sse')

  const geminiAccount = {
    ...account,
    providerCode: 'gemini',
    providerProtocolProfileId: 'profile_gemini_native_v1beta',
    protocolCode: 'gemini',
    protocolVersion: 'v1beta',
    healthCheckEndpointMode: 'generate_content_json'
  } as AccountSummary
  assert.equal(resolveJ1AccountHealthProbeProtocol(geminiAccount), 'gemini')
  const geminiPath = publishAccountHealthJobsInputFromAccount({
    account: geminiAccount,
    dispatchRevision: 3,
    inputVersion: 20,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const geminiPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(geminiPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(geminiPayload.provider, 'gemini')
  assert.equal(geminiPayload.endpoint_mode, 'generate_content_json')

  for (const [label, candidate, expected] of [
    ['xai oauth responses', { providerCode: 'xai', providerProtocolProfileId: 'profile_xai_openai_v1', type: 'oauth', healthCheckEndpointMode: 'responses_sse' }, 'openai'],
    ['OpenAI-compatible OAuth is rejected by its API-key-only profile', { providerCode: 'openai', providerProtocolProfileId: 'profile_openai_openai_v1', type: 'oauth', healthCheckEndpointMode: 'responses_json' }, undefined],
    ['deepseek anthropic', { providerCode: 'deepseek', providerProtocolProfileId: 'profile_deepseek_anthropic_v1', type: 'api_key', healthCheckEndpointMode: 'messages_json' }, 'anthropic'],
    ['glm coding anthropic', { providerCode: 'glm', providerProtocolProfileId: 'profile_glm_coding_anthropic_v1', type: 'api_key', healthCheckEndpointMode: 'messages_sse' }, 'anthropic'],
    ['gemini OpenAI chat', { providerCode: 'gemini', providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta', type: 'api_key', healthCheckEndpointMode: 'chat_sse' }, 'openai'],
    ['hybrid without mapping is rejected', { providerCode: 'hybrid', providerProtocolProfileId: 'profile_hybrid_openai_chat_v1', type: 'api_key', healthCheckEndpointMode: 'messages_json' }, undefined],
    ['invalid provider profile pair', { providerCode: 'gemini', providerProtocolProfileId: 'profile_anthropic_anthropic_v1', type: 'api_key', healthCheckEndpointMode: 'messages_json' }, undefined],
    ['protocol metadata mismatch is rejected', { providerCode: 'gpt', providerProtocolProfileId: 'profile_gpt_openai_v1', protocolCode: 'anthropic', protocolVersion: 'v1', type: 'api_key', healthCheckEndpointMode: 'chat_json' }, undefined]
  ] as const) {
    assert.equal(resolveJ1AccountHealthProbeProtocol({ ...account, ...candidate } as AccountSummary), expected, label)
  }

  const hybridAccount = {
    ...account,
    providerCode: 'hybrid',
    providerProtocolProfileId: 'profile_hybrid_openai_chat_v1',
    healthCheckEndpointMode: 'messages_json',
    credentials: { api_key: 'sk-hybrid', base_url: 'https://hybrid.example.com' },
    modelMappings: [{
      sourceModel: 'gpt-test',
      sourceEndpointFamily: 'messages',
      upstreamModel: 'claude-test',
      upstreamEndpointFamily: 'messages',
      enabled: true
    }]
  } as AccountSummary
  assert.equal(resolveJ1AccountHealthProbeProtocol(hybridAccount), 'anthropic')
  const hybridPath = publishAccountHealthJobsInputFromAccount({
    account: hybridAccount,
    dispatchRevision: 3,
    inputVersion: 21,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const hybridPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(hybridPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(hybridPayload.provider, 'anthropic')
  assert.equal(hybridPayload.endpoint_mode, 'messages_json')
  assert.equal(hybridPayload.health_model, 'claude-test')

  const hybridGeminiSseAccount = {
    ...hybridAccount,
    healthCheckEndpointMode: 'generate_content_sse',
    modelMappings: [{
      sourceModel: 'gpt-test',
      sourceEndpointFamily: 'stream_generate_content',
      upstreamModel: 'gemini-test',
      upstreamEndpointFamily: 'generate_content',
      enabled: true
    }]
  } as AccountSummary
  assert.equal(resolveJ1AccountHealthProbeProtocol(hybridGeminiSseAccount), 'gemini')
  const hybridGeminiSsePath = publishAccountHealthJobsInputFromAccount({
    account: hybridGeminiSseAccount,
    dispatchRevision: 3,
    inputVersion: 22,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const hybridGeminiSsePayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(hybridGeminiSsePath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(hybridGeminiSsePayload.provider, 'gemini')
  assert.equal(hybridGeminiSsePayload.endpoint_mode, 'generate_content_sse')
  assert.equal(hybridGeminiSsePayload.health_model, 'gemini-test')

  const cooldownAccount = {
    ...account,
    status: 'temporary_unavailable',
    cooldownUntil: '2030-08-16T08:00:00.000+08:00',
    cooldownRetestObservationStartedAt: '2030-08-16T08:01:00.000+08:00',
    cooldownRetestGeneration: 'generation-1',
    cooldownRetestDispatchRevision: 3,
    temporaryUnavailableContinuousProbeEnabled: true
  } as AccountSummary
  const cooldownPath = publishAccountHealthJobsInputFromAccount({
    account: cooldownAccount,
    dispatchRevision: 3,
    inputVersion: 12,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const cooldownPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(cooldownPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal((cooldownPayload.eligibility as Record<string, unknown>).cooldown_until, '2030-08-16T00:00:00.000Z')
  assert.deepEqual(cooldownPayload.cooldown_fence, {
    observation_started_at: '2030-08-16T00:01:00.000Z',
    generation: 'generation-1'
  })
  assert.equal((cooldownPayload.eligibility as Record<string, unknown>).temporary_unavailable_continuous_probe_enabled, true)

  const oauthAccount = {
    ...account,
    type: 'oauth',
    healthCheckEndpointMode: 'responses_json',
    credentials: { access_token: 'oauth-access-token', expires_at: '2030-08-16T08:00:00.000+08:00' }
  } as AccountSummary
  const oauthPath = publishAccountHealthJobsInputFromAccount({
    account: oauthAccount,
    dispatchRevision: 3,
    inputVersion: 13,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const oauthPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(oauthPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(oauthPayload.endpoint_mode, 'responses_json')
  assert.equal(oauthPayload.oauth_expires_at, '2030-08-16T00:00:00.000Z')

  const profiledOAuthPath = publishAccountHealthJobsInputFromAccount({
    account: {
      ...oauthAccount,
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      credentials: {
        access_token: 'oauth-access-token',
        expires_at: '2030-08-16T08:00:00.000+08:00',
        base_url: 'https://api.openai.com/v1'
      }
    } as AccountSummary,
    dispatchRevision: 3,
    inputVersion: 130,
    signingKey,
    root,
    settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
    expiresAt: new Date(Date.now() + 60_000)
  })
  const profiledOAuthPayload = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(profiledOAuthPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(profiledOAuthPayload.base_url, 'https://chatgpt.com/backend-api/codex')

  for (const invalidTime of ['2030-08-16T08:00:00.000', '2030-08-16 08:00:00+08:00', 'not-a-time']) {
    assert.throws(() => publishAccountHealthJobsInputFromAccount({
      account: { ...oauthAccount, credentials: { access_token: 'oauth-access-token', expires_at: invalidTime } } as AccountSummary,
      dispatchRevision: 3,
      inputVersion: 14,
      signingKey,
      root,
      settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
      expiresAt: new Date(Date.now() + 60_000)
    }), /J1 OAuth credentials\.expires_at必须是带 Z 或数值 offset 的 RFC3339 时间/u, `OAuth expires_at 必须拒绝：${invalidTime}`)
    assert.throws(() => publishAccountHealthJobsInputFromAccount({
      account: { ...cooldownAccount, cooldownUntil: invalidTime } as AccountSummary,
      dispatchRevision: 3,
      inputVersion: 15,
      signingKey,
      root,
      settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
      expiresAt: new Date(Date.now() + 60_000)
    }), /J1 cooldownUntil必须是带 Z 或数值 offset 的 RFC3339 时间/u, `cooldownUntil 必须拒绝：${invalidTime}`)
    assert.throws(() => publishAccountHealthJobsInputFromAccount({
      account: { ...cooldownAccount, cooldownRetestObservationStartedAt: invalidTime } as AccountSummary,
      dispatchRevision: 3,
      inputVersion: 16,
      signingKey,
      root,
      settings: { intervalHours: 1, jitterMinutes: 10, failureThreshold: 2 },
      expiresAt: new Date(Date.now() + 60_000)
    }), /J1 cooldownRetestObservationStartedAt必须是带 Z 或数值 offset 的 RFC3339 时间/u, `cooldown observation 必须拒绝：${invalidTime}`)
  }

  const requestPath = publishAccountHealthJobsProbeRequest({
    account,
    inputVersion: 12,
    root,
    signingKey,
    requestId: 'request-1',
    reason: 'activation',
    deadline: new Date(Date.now() + 60_000)
  })
  const request = JSON.parse(Buffer.from(readPublishedAccountHealthJobsInput(requestPath).payload, 'base64url').toString('utf8')) as Record<string, unknown>
  assert.equal(request.request_id, 'request-1')
  assert.equal(request.mutate_account, true)
  assert.equal(request.input_version, 12)

  const maintenanceSource = readFileSync(resolve(process.cwd(), 'src/scripts/maintenance/publish-account-health-jobs-input.ts'), 'utf8')
  assert.match(maintenanceSource, /findAccountHealthJobsInputRevisionsAsync/u, 'J1 maintenance 发布必须读取内部 dispatch fence')
  assert.match(maintenanceSource, /dispatchRevision:\s*revisions\.dispatchRevision/u, 'J1 maintenance 发布必须使用存储层 dispatchRevision')
  assert.doesNotMatch(maintenanceSource, /account\.dispatchRevision\s*\?\?\s*0/u, '公开 AccountSummary 缺少 dispatchRevision 时不得静默降为 0')
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-input-service-regression passed')
