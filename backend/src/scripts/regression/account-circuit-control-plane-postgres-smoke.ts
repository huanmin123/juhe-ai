import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import {
  acknowledgeAccountCircuitOutboxInClient,
  advanceAccountCircuitDispatchRevisionInClient,
  claimAccountCircuitOutboxInClient,
  cleanupAccountCircuitControlPlaneInClient,
  compareAndSetAccountCircuitIncidentInClient,
  listAccountCircuitIncidentsForRebuildInClient,
  listAccountCircuitProjectionGapsInClient
} from '../../storage/account-circuit-control-plane.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

if (runtimeConfig.databaseDriver !== 'postgres') {
  console.log('SKIP: account circuit control plane PostgreSQL smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
  process.exit(0)
}

const marker = `account_circuit_control_plane_pg_${Date.now()}_${Math.random().toString(16).slice(2)}`
const accountRuntimeKey = `${marker}:runtime`
const scopeKey = `7:account|${Buffer.byteLength(accountRuntimeKey, 'utf8')}:${accountRuntimeKey}`
const confirmationEvidenceKeys = ['a'.repeat(64), 'b'.repeat(64)]
const childIncidentIds = Array.from({ length: 64 }, (_, index) => `${marker}:child:${index + 1}`)
const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  const fixture = await pool.query(
    `SELECT provider_code, provider_protocol_profile_id, protocol_code, protocol_version
     FROM juhe_business.accounts
     WHERE deleted_at IS NULL
     UNION ALL
     SELECT profile.provider_code, profile.id AS provider_protocol_profile_id, profile.protocol_code, profile.protocol_version
     FROM juhe_business.provider_protocol_profiles profile
     WHERE NOT EXISTS (SELECT 1 FROM juhe_business.accounts WHERE deleted_at IS NULL)
     ORDER BY provider_code, provider_protocol_profile_id
     LIMIT 1`
  )
  const source = fixture.rows[0] as {
    provider_code?: string
    provider_protocol_profile_id?: string
    protocol_code?: string
    protocol_version?: string
  } | undefined
  assert.ok(source?.provider_code && source.provider_protocol_profile_id && source.protocol_code && source.protocol_version, 'PG smoke 需要 provider/profile/protocol fixture')
  const now = Date.now()
  const iso = new Date(now).toISOString()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, created_at, updated_at
    ) VALUES ($1, $1, $1, 'user', 'active', 'test-only', $2, $2)
  `, [marker, iso])
  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, credentials_encrypted,
      health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES ($1, $1, $2, $3, $4, $5, $6, 'api_key', 'active', '{}', 'gpt-5.6-sol', 'responses_sse', $7, $7)
  `, [marker, source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, marker, iso])

  const revision = await advanceAccountCircuitDispatchRevisionInClient(client, {
    accountId: marker,
    accountRuntimeKey,
    transitionId: `${marker}:revision`,
    nowMs: now
  })
  assert.equal(revision.dispatchRevision, 2)

  const incident = await compareAndSetAccountCircuitIncidentInClient(client, {
    accountId: marker,
    accountRuntimeKey,
    circuitScopeKey: scopeKey,
    scopeKind: 'account',
    incidentId: `${marker}:incident`,
    childIncidentIds,
    state: 'OPEN',
    failureScope: 'account',
    generation: 1,
    dispatchRevision: 2,
    expectedLedgerRevision: null,
    transitionId: `${marker}:incident`,
    openUntilMs: now + 3_000,
    nextTransitionAtMs: now + 3_000,
    lastFailureClass: 'timeout_before_complete',
    consecutiveFailures: 1,
    confirmationFailuresRequired: 2,
    confirmationFailureEvidenceKeys: confirmationEvidenceKeys,
    nowMs: now
  })
  assert.equal(incident.status, 'applied')
  assert.equal(incident.incident?.confirmationFailuresRequired, 2)
  assert.equal(incident.incident?.consecutiveFailures, 1)
  assert.deepEqual(incident.incident?.confirmationFailureEvidenceKeys, confirmationEvidenceKeys)
  assert.deepEqual(incident.incident?.childIncidentIds, childIncidentIds)

  const keyModelScopeKey = `${scopeKey}:key_model`
  const keyModelHash = 'c'.repeat(64)
  const keyModel = await compareAndSetAccountCircuitIncidentInClient(client, {
    accountId: marker,
    accountRuntimeKey,
    circuitScopeKey: keyModelScopeKey,
    scopeKind: 'key_model',
    keyFingerprint: 'a'.repeat(64),
    clientModel: 'gpt-5.6-sol',
    capabilityHash: keyModelHash,
    credentialSourceAccountId: `${marker}:source`,
    clientEndpointFamily: 'responses',
    finalUpstreamModel: 'gpt-5.6-sol',
    upstreamEndpointMode: 'responses_sse',
    incidentId: `${marker}:key-model-incident`,
    state: 'OPEN',
    failureScope: 'key_model',
    generation: 1,
    dispatchRevision: 2,
    expectedLedgerRevision: null,
    transitionId: `${marker}:key-model-incident`,
    openUntilMs: now + 3_000,
    nextTransitionAtMs: now + 3_000,
    lastFailureClass: 'timeout_before_complete',
    consecutiveFailures: 1,
    confirmationFailuresRequired: 1,
    confirmationFailureEvidenceKeys: [],
    nowMs: now
  })
  assert.equal(keyModel.status, 'applied')
  assert.equal(keyModel.incident?.scopeKind, 'key_model')
  assert.equal(keyModel.incident?.capabilityHash, keyModelHash)
  assert.equal(keyModel.incident?.clientModel, 'gpt-5.6-sol')
  assert.equal(keyModel.incident?.credentialSourceAccountId, `${marker}:source`)
  assert.equal(keyModel.incident?.clientEndpointFamily, 'responses')
  assert.equal(keyModel.incident?.finalUpstreamModel, 'gpt-5.6-sol')
  assert.equal(keyModel.incident?.upstreamEndpointMode, 'responses_sse')

  const delayedOlderGeneration = await compareAndSetAccountCircuitIncidentInClient(client, {
    accountId: marker,
    accountRuntimeKey,
    circuitScopeKey: scopeKey,
    scopeKind: 'account',
    incidentId: `${marker}:incident`,
    state: 'RECOVERING',
    generation: 0,
    dispatchRevision: 2,
    expectedLedgerRevision: 1,
    transitionId: `${marker}:delayed-older-generation`,
    consecutiveFailures: 0,
    confirmationFailuresRequired: 2,
    confirmationFailureEvidenceKeys: [],
    stateUpdatedAtMs: now + 10,
    nowMs: now + 1
  })
  assert.equal(delayedOlderGeneration.status, 'cas_conflict', 'PG 必须拒绝迟到旧 generation')
  assert.equal(delayedOlderGeneration.incident?.state, 'OPEN')
  assert.equal(delayedOlderGeneration.incident?.ledgerRevision, 1)

  const rebuild = await listAccountCircuitIncidentsForRebuildInClient(client, {
    nowMs: now,
    limit: 500
  })
  const rebuiltIncident = rebuild.items.find((item) => item.circuitScopeKey === scopeKey)
  assert.equal(rebuiltIncident?.confirmationFailuresRequired, 2)
  assert.equal(rebuiltIncident?.consecutiveFailures, 1)
  assert.deepEqual(rebuiltIncident?.confirmationFailureEvidenceKeys, confirmationEvidenceKeys)
  assert.deepEqual(rebuiltIncident?.childIncidentIds, childIncidentIds)

  const constraints = await pool.query(`
    SELECT pg_get_constraintdef(oid) AS definition
    FROM pg_constraint
    WHERE conrelid = 'juhe_business.account_circuit_incidents'::regclass
  `)
  const definitions = constraints.rows.map((row) => String((row as { definition?: string }).definition ?? ''))
  assert.ok(definitions.some((definition) => /confirmation_failures_required\s+>=\s+1[\s\S]+confirmation_failures_required\s+<=\s+5/i.test(definition)), 'PG smoke 必须加载确认阈值约束')
  assert.ok(definitions.some((definition) => /consecutive_failures\s*<=\s*confirmation_failures_required/i.test(definition)), 'PG smoke 必须加载确认计数约束')
  assert.ok(definitions.some((definition) => /jsonb_array_length[\s\S]*confirmation_failures_required/i.test(definition)), 'PG evidence 约束必须使用 jsonb_array_length 并限制 N+1')

  const claims = await claimAccountCircuitOutboxInClient(client, {
    ownerId: `${marker}:projector`,
    nowMs: now,
    leaseMs: 5_000,
    limit: 20
  })
  assert.ok(claims.length >= 2, 'PG smoke 应 claim 当前 fixture 的 revision 与 incident outbox')
  assert.ok(claims.some((item) => item.accountId === marker && item.eventType === 'dispatch_revision_changed'), 'PG smoke 必须 claim 当前 fixture revision outbox')
  assert.ok(claims.some((item) => item.accountId === marker && item.eventType === 'incident_changed'), 'PG smoke 必须 claim 当前 fixture incident outbox')
  for (const item of claims) {
    assert.equal(await acknowledgeAccountCircuitOutboxInClient(client, {
      eventId: item.eventId,
      projectionKey: item.projectionKey,
      claimToken: item.claimToken!,
      acknowledgedAtMs: now + 1
    }), true)
  }
  const gaps = await listAccountCircuitProjectionGapsInClient(client, {
    afterAccountId: '',
    afterUpdatedAtMs: -1,
    afterCircuitScopeKey: '',
    limit: 20
  })
  assert.equal(gaps.dispatchRevisionGaps.some((item) => item.accountId === marker), false)
  assert.equal(gaps.incidentGaps.some((item) => item.circuitScopeKey === scopeKey), false)

  await cleanupAccountCircuitControlPlaneInClient(client, {
    nowMs: now + 10_000,
    outboxAcknowledgedBeforeMs: now + 10_000,
    limit: 100
  })
  console.log(JSON.stringify({ message: '账户 circuit 控制面 PostgreSQL smoke 通过', accountId: marker }))
} finally {
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [marker])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = $1', [marker])
  await closePostgresPool()
}
