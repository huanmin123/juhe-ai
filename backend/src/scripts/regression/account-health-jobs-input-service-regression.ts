import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import type { AccountSummary } from '../../domain/types.js'
import { readPublishedAccountHealthJobsInput } from '../../modules/background/account-health-jobs-input.protocol.js'
import { publishAccountHealthJobsInputFromAccount, publishAccountHealthJobsProbeRequest } from '../../modules/background/account-health-jobs-input.service.js'

const testRoot = resolve(process.env.JUHE_AI_TEST_TEMP_ROOT?.trim() || tmpdir())
const root = mkdtempSync(join(testRoot, 'juhe-ai-account-health-service-'))
try {
  const signingKey = Buffer.alloc(32, 9).toString('base64url')
  const account = {
    id: 'account-1',
    configRevision: 7,
    dispatchRevision: 3,
    providerCode: 'openai',
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
  assert.equal(payload.provider, 'openai')
  assert.equal(payload.type, 'api_key')
  assert.equal(payload.endpoint_mode, 'chat_json')
  assert.equal((payload.schedule as Record<string, unknown>).health_interval_ms, 3_600_000)
  assert.equal((payload.eligibility as Record<string, unknown>).bound_group, true)
  assert.equal(Array.isArray(payload.api_keys), true)
  assert.equal(typeof (payload.api_keys as Array<Record<string, unknown>>)[0]?.credential, 'object')

  const cooldownAccount = {
    ...account,
    status: 'temporary_unavailable',
    cooldownUntil: new Date(Date.now() + 30_000).toISOString(),
    cooldownRetestObservationStartedAt: new Date(Date.now() - 60_000).toISOString(),
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
  assert.equal((cooldownPayload.eligibility as Record<string, unknown>).cooldown_until, cooldownAccount.cooldownUntil)
  assert.deepEqual(cooldownPayload.cooldown_fence, {
    observation_started_at: cooldownAccount.cooldownRetestObservationStartedAt,
    generation: 'generation-1'
  })

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
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('account-health-jobs-input-service-regression passed')
