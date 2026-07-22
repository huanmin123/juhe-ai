import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import {
  acknowledgeAccountCircuitOutboxInClient,
  advanceAccountCircuitDispatchRevisionInClient,
  claimAccountCircuitOutboxInClient,
  cleanupAccountCircuitControlPlaneInClient,
  compareAndSetAccountCircuitIncidentInClient,
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
const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  const fixture = await pool.query(
    `SELECT provider_code, provider_protocol_profile_id
     FROM juhe_business.accounts
     WHERE deleted_at IS NULL
     ORDER BY id
     LIMIT 1`
  )
  const source = fixture.rows[0] as { provider_code?: string; provider_protocol_profile_id?: string } | undefined
  assert.ok(source?.provider_code && source.provider_protocol_profile_id, 'PG smoke 需要至少一个现有账户作为 provider/profile fixture')
  const now = Date.now()
  const iso = new Date(now).toISOString()
  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, credentials_encrypted,
      health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES ($1, 'sys_admin', $2, $3, 'openai', 'v1', $4, 'api_key', 'active', '{}', 'gpt-5.6-sol', 'responses_sse', $5, $5)
  `, [marker, source.provider_code, source.provider_protocol_profile_id, marker, iso])

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
    state: 'OPEN',
    failureScope: 'account',
    generation: 1,
    dispatchRevision: 2,
    expectedLedgerRevision: null,
    transitionId: `${marker}:incident`,
    openUntilMs: now + 3_000,
    nextTransitionAtMs: now + 3_000,
    lastFailureClass: 'timeout_before_complete',
    nowMs: now
  })
  assert.equal(incident.status, 'applied')

  const claims = await claimAccountCircuitOutboxInClient(client, {
    ownerId: `${marker}:projector`,
    nowMs: now,
    leaseMs: 5_000,
    limit: 20
  })
  assert.equal(claims.length, 2, 'PG smoke 应 claim revision 与 incident 两条 outbox')
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
  await closePostgresPool()
}
