import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { AccountSummary } from '../../domain/types.js'
import { readPublishedAccountHealthJobsInput } from '../../modules/background/account-health-jobs-input.protocol.js'
import { publishAccountHealthJobsInputFromAccount, publishAccountHealthJobsProbeRequest } from '../../modules/background/account-health-jobs-input.service.js'
import { isJ1AccountHealthEndpointModeEligible } from '../../storage/account-health-jobs-input.repository.js'

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
  assert.equal((payload.eligibility as Record<string, unknown>).bound_group, true)
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
  assert.equal(isJ1AccountHealthEndpointModeEligible('oauth', 'responses_sse'), false)
  assert.throws(() => publishAccountHealthJobsInputFromAccount({
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
  }), /账户类型 oauth 不支持探活 endpoint mode：responses_sse/u)

  const cooldownAccount = {
    ...account,
    status: 'temporary_unavailable',
    cooldownUntil: '2030-08-16T08:00:00.000+08:00',
    cooldownRetestObservationStartedAt: '2030-08-16T08:01:00.000+08:00',
    cooldownRetestGeneration: 'generation-1',
    cooldownRetestDispatchRevision: 3
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
