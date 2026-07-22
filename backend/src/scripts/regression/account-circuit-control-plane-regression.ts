import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import {
  acknowledgeAccountCircuitOutboxInClient,
  advanceAccountCircuitDispatchRevisionInClient,
  claimAccountCircuitOutboxInClient,
  cleanupAccountCircuitControlPlaneInClient,
  compareAndSetAccountCircuitIncidentInClient,
  listAccountCircuitIncidentsForRebuildInClient,
  listAccountCircuitProjectionGapsInClient,
  releaseAccountCircuitOutboxForReplayInClient
} from '../../storage/account-circuit-control-plane.repository.js'
import { applyBusinessSchema } from '../../storage/schema/business-schema.js'

const database = new DatabaseSync(':memory:')
applyBusinessSchema(database)
const client = createSqliteDatabaseClient(database)
const accountId = 'account-circuit-control-plane-regression'
const accountRuntimeKey = `${accountId}:sys_admin:group:grant`
const circuitScopeKey = `7:account|${Buffer.byteLength(accountRuntimeKey, 'utf8')}:${accountRuntimeKey}`

insertSchemaFixture()
insertAccount()

try {
  assertCurrentSchema()
  assertPostgresSchemaParity()

  const firstRevision = await advanceAccountCircuitDispatchRevisionInClient(client, {
    accountId,
    accountRuntimeKey,
    transitionId: 'dispatch-transition-1',
    nowMs: 1_000
  })
  assert.deepEqual(firstRevision, {
    status: 'applied',
    accountId,
    accountRuntimeKey,
    dispatchRevision: 2,
    transitionId: 'dispatch-transition-1'
  })

  const replayedRevision = await advanceAccountCircuitDispatchRevisionInClient(client, {
    accountId,
    accountRuntimeKey,
    transitionId: 'dispatch-transition-1',
    nowMs: 1_001
  })
  assert.equal(replayedRevision.status, 'idempotent', '同一 dispatch transition 重放不能重复递增 revision')
  assert.equal(replayedRevision.dispatchRevision, 2)

  const secondRevision = await advanceAccountCircuitDispatchRevisionInClient(client, {
    accountId,
    accountRuntimeKey,
    transitionId: 'dispatch-transition-2',
    nowMs: 1_100
  })
  assert.equal(secondRevision.dispatchRevision, 3)

  const created = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: null,
    state: 'SUSPECT',
    transitionId: 'incident-transition-1',
    generation: 1,
    dispatchRevision: 3,
    nextTransitionAtMs: 2_000,
    lastFailureClass: 'timeout_before_complete'
  }))
  assert.equal(created.status, 'applied')
  assert.equal(created.incident?.ledgerRevision, 1)
  assert.equal(created.incident?.projectedLedgerRevision, 0)

  const idempotentCreate = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: null,
    state: 'SUSPECT',
    transitionId: 'incident-transition-1',
    generation: 1,
    dispatchRevision: 3,
    nextTransitionAtMs: 2_000,
    lastFailureClass: 'timeout_before_complete'
  }))
  assert.equal(idempotentCreate.status, 'idempotent')
  assert.equal(idempotentCreate.incident?.ledgerRevision, 1)

  const staleRevision = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: 1,
    state: 'OPEN',
    transitionId: 'incident-stale-dispatch',
    generation: 1,
    dispatchRevision: 2,
    openUntilMs: 4_000,
    nextTransitionAtMs: 4_000,
    lastFailureClass: 'timeout_before_complete'
  }))
  assert.equal(staleRevision.status, 'stale_dispatch_revision')

  const staleLedger = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: 99,
    state: 'OPEN',
    transitionId: 'incident-stale-ledger',
    generation: 1,
    dispatchRevision: 3,
    openUntilMs: 4_000,
    nextTransitionAtMs: 4_000,
    lastFailureClass: 'timeout_before_complete'
  }))
  assert.equal(staleLedger.status, 'cas_conflict')

  const opened = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: 1,
    state: 'OPEN',
    transitionId: 'incident-transition-2',
    generation: 1,
    dispatchRevision: 3,
    openUntilMs: 4_000,
    nextTransitionAtMs: 4_000,
    lastFailureClass: 'timeout_before_complete'
  }))
  assert.equal(opened.status, 'applied')
  assert.equal(opened.incident?.ledgerRevision, 2)

  const closed = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: 2,
    state: 'CLOSED',
    transitionId: 'incident-transition-3',
    generation: 1,
    dispatchRevision: 3,
    retainedUntilMs: 20_000
  }))
  assert.equal(closed.status, 'applied')
  assert.equal(closed.incident?.ledgerRevision, 3)
  assert.equal(closed.incident?.retainedUntilMs, 20_000)

  const rebuild = await listAccountCircuitIncidentsForRebuildInClient(client, {
    nowMs: 10_000,
    limit: 10
  })
  assert.equal(rebuild.items.length, 1, '未过保留期的 CLOSED tombstone 必须参与重建')
  assert.equal(rebuild.items[0]?.state, 'CLOSED')

  const gapsBeforeAck = await listAccountCircuitProjectionGapsInClient(client, {
    afterAccountId: '',
    afterUpdatedAtMs: -1,
    afterCircuitScopeKey: '',
    limit: 10
  })
  assert.equal(gapsBeforeAck.dispatchRevisionGaps.length, 1)
  assert.equal(gapsBeforeAck.incidentGaps.length, 1)

  const firstClaim = await claimAccountCircuitOutboxInClient(client, {
    ownerId: 'projector-a',
    nowMs: 11_000,
    leaseMs: 5_000,
    limit: 2
  })
  assert.equal(firstClaim.length, 2)
  assert.equal(firstClaim.every((item) => item.status === 'processing' && item.claimToken), true)
  assert.equal(new Set(firstClaim.map((item) => item.claimToken)).size, 2, '每个 claim 必须有独立 fencing token')

  const replayTarget = firstClaim[0]!
  const released = await releaseAccountCircuitOutboxForReplayInClient(client, {
    eventId: replayTarget.eventId,
    claimToken: replayTarget.claimToken!,
    errorClass: 'redis_unavailable',
    nowMs: 11_100,
    retryDelayMs: 500
  })
  assert.equal(released, true)

  const tooEarly = await claimAccountCircuitOutboxInClient(client, {
    ownerId: 'projector-b',
    nowMs: 11_599,
    leaseMs: 5_000,
    limit: 10
  })
  assert.equal(tooEarly.some((item) => item.eventId === replayTarget.eventId), false, '重放延迟前不能重新 claim')

  const replayed = await claimAccountCircuitOutboxInClient(client, {
    ownerId: 'projector-b',
    nowMs: 11_600,
    leaseMs: 5_000,
    limit: 10
  })
  assert.equal(replayed.some((item) => item.eventId === replayTarget.eventId), true)

  const allClaims = [...firstClaim.slice(1), ...tooEarly, ...replayed]
  const uniqueClaims = new Map(allClaims.map((item) => [item.eventId, item]))
  for (const item of uniqueClaims.values()) {
    const acked = await acknowledgeAccountCircuitOutboxInClient(client, {
      eventId: item.eventId,
      projectionKey: item.projectionKey,
      claimToken: item.claimToken!,
      acknowledgedAtMs: 12_000
    })
    assert.equal(acked, true, `outbox ${item.eventId} 必须 ACK 成功`)
    assert.equal(await acknowledgeAccountCircuitOutboxInClient(client, {
      eventId: item.eventId,
      projectionKey: item.projectionKey,
      claimToken: item.claimToken!,
      acknowledgedAtMs: 12_001
    }), true, '重复 ACK 必须幂等成功')
  }

  for (;;) {
    const claims = await claimAccountCircuitOutboxInClient(client, {
      ownerId: 'projector-drain',
      nowMs: 12_001,
      leaseMs: 5_000,
      limit: 50
    })
    if (claims.length === 0) break
    for (const item of claims) {
      assert.equal(await acknowledgeAccountCircuitOutboxInClient(client, {
        eventId: item.eventId,
        projectionKey: item.projectionKey,
        claimToken: item.claimToken!,
        acknowledgedAtMs: 12_100
      }), true)
    }
  }

  const gapsAfterAck = await listAccountCircuitProjectionGapsInClient(client, {
    afterAccountId: '',
    afterUpdatedAtMs: -1,
    afterCircuitScopeKey: '',
    limit: 10
  })
  assert.deepEqual(gapsAfterAck.dispatchRevisionGaps, [])
  assert.deepEqual(gapsAfterAck.incidentGaps, [])

  const beforeRetention = await cleanupAccountCircuitControlPlaneInClient(client, {
    nowMs: 19_999,
    outboxAcknowledgedBeforeMs: 12_050,
    limit: 100
  })
  assert.equal(beforeRetention.deletedIncidents, 0, 'CLOSED tombstone 未到 retainedUntilMs 不能删除')

  const afterRetention = await cleanupAccountCircuitControlPlaneInClient(client, {
    nowMs: 20_000,
    outboxAcknowledgedBeforeMs: 20_000,
    limit: 100
  })
  assert.equal(afterRetention.deletedIncidents, 1)
  assert.ok(beforeRetention.deletedOutbox + afterRetention.deletedOutbox > 0)

  const finalRebuild = await listAccountCircuitIncidentsForRebuildInClient(client, {
    nowMs: 20_001,
    limit: 10
  })
  assert.deepEqual(finalRebuild.items, [])

  console.log(JSON.stringify({
    message: '账户 circuit 控制面账本回归通过',
    dispatchRevision: 3,
    incidentLedgerRevision: 3,
    deletedOutbox: beforeRetention.deletedOutbox + afterRetention.deletedOutbox
  }))
} finally {
  database.close()
}

function incidentMutation(overrides: Record<string, unknown>): Parameters<typeof compareAndSetAccountCircuitIncidentInClient>[1] {
  return {
    accountId,
    accountRuntimeKey,
    circuitScopeKey,
    scopeKind: 'account',
    incidentId: 'incident-1',
    childIncidentIds: ['child-1', 'child-2'],
    causedByTerminalOutcomeId: 'terminal-1',
    failureScope: 'account',
    consecutiveFailures: 1,
    backoffLevel: 0,
    recoveringSuccesses: 0,
    upstreamAttemptObserved: true,
    cooldownObservationGeneration: 0,
    nowMs: 1_500,
    ...overrides
  } as Parameters<typeof compareAndSetAccountCircuitIncidentInClient>[1]
}

function insertAccount(): void {
  const now = new Date(0).toISOString()
  database.prepare(`
    INSERT INTO accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, credentials_encrypted,
      health_check_model, health_check_endpoint_mode, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    accountId,
    'sys_admin',
    'gpt',
    'profile_gpt_openai_v1',
    'openai',
    'v1',
    'Circuit control plane regression',
    'api_key',
    'active',
    '{}',
    'gpt-5.6-sol',
    'responses_sse',
    now,
    now
  )
}

function insertSchemaFixture(): void {
  const now = new Date(0).toISOString()
  database.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `).run('sys_admin', 'admin', 'Admin', 'super_admin', 'active', 'test-only', now, now)
  database.prepare(`
    INSERT INTO providers (id, code, name, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?)
  `).run('gpt', 'gpt', 'GPT', now, now)
  database.prepare(`
    INSERT INTO protocols (id, code, version, name, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?)
  `).run('openai_v1', 'openai', 'v1', 'OpenAI v1', now, now)
  database.prepare(`
    INSERT INTO provider_protocol_profiles (
      id, provider_code, name, protocol_code, protocol_version, base_url,
      default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    'profile_gpt_openai_v1',
    'gpt',
    'GPT / OpenAI v1',
    'openai',
    'v1',
    'https://example.invalid/v1',
    'gpt-5.6-sol',
    '["api_key"]',
    '["responses"]',
    now,
    now
  )
}

function assertCurrentSchema(): void {
  const accountColumns = database.prepare('PRAGMA table_info(accounts)').all() as Array<{ name: string; dflt_value: string | null }>
  assert.equal(accountColumns.find((column) => column.name === 'dispatch_revision')?.dflt_value, '1')
  assert.equal(accountColumns.find((column) => column.name === 'circuit_projection_revision')?.dflt_value, '0')

  const incidentColumns = database.prepare('PRAGMA table_info(account_circuit_incidents)').all() as Array<{ name: string }>
  const outboxColumns = database.prepare('PRAGMA table_info(account_circuit_outbox)').all() as Array<{ name: string }>
  assert.equal(incidentColumns.some((column) => /response|payload|body|message/i.test(column.name)), false, 'incident ledger 禁止保存响应内容')
  assert.equal(outboxColumns.some((column) => /response|payload|body|message/i.test(column.name)), false, 'outbox 禁止保存响应内容')
  for (const required of ['incident_id', 'transition_id', 'dispatch_revision', 'ledger_revision', 'projected_ledger_revision', 'retained_until_ms']) {
    assert.equal(incidentColumns.some((column) => column.name === required), true, `incident ledger 缺少 ${required}`)
  }
  for (const required of ['projection_key', 'dedupe_key', 'claim_token', 'claim_until_ms', 'acknowledged_at_ms']) {
    assert.equal(outboxColumns.some((column) => column.name === required), true, `outbox 缺少 ${required}`)
  }
}

function assertPostgresSchemaParity(): void {
  const statements = collectPostgresSchemaStatements()
  const generatedIncident = statements.find((statement) => /^CREATE TABLE IF NOT EXISTS account_circuit_incidents\b/.test(statement.sql))?.sql
  const generatedOutbox = statements.find((statement) => /^CREATE TABLE IF NOT EXISTS account_circuit_outbox\b/.test(statement.sql))?.sql
  assert.ok(generatedIncident)
  assert.ok(generatedOutbox)
  assert.match(generatedIncident, /dispatch_revision bigint[\s\S]+ledger_revision bigint[\s\S]+retained_until_ms bigint/)
  assert.match(generatedOutbox, /dispatch_revision bigint[\s\S]+claim_until_ms bigint[\s\S]+acknowledged_at_ms bigint/)

  const migration = readFileSync(
    new URL('../../../../backend-go/db/migrations/000071_w1_account_circuit_control_plane.sql', import.meta.url),
    'utf8'
  )
  assert.match(migration, /ADD COLUMN IF NOT EXISTS dispatch_revision bigint NOT NULL DEFAULT 1/)
  assert.match(migration, /CREATE TABLE IF NOT EXISTS juhe_business\.account_circuit_incidents/)
  assert.match(migration, /CREATE TABLE IF NOT EXISTS juhe_business\.account_circuit_outbox/)
  assert.match(migration, /idx_account_circuit_outbox_claim[\s\S]+status, available_at_ms, claim_until_ms, created_at_ms, event_id/)
  assert.deepEqual(
    tableColumnNames(generatedIncident),
    tableColumnNames(extractMigrationTable(migration, 'account_circuit_incidents')),
    'SQLite 生成的 PostgreSQL incident 当前 schema 必须与 Goose 71 列集合一致'
  )
  assert.deepEqual(
    tableColumnNames(generatedOutbox),
    tableColumnNames(extractMigrationTable(migration, 'account_circuit_outbox')),
    'SQLite 生成的 PostgreSQL outbox 当前 schema 必须与 Goose 71 列集合一致'
  )
}

function extractMigrationTable(source: string, tableName: string): string {
  const start = source.indexOf(`CREATE TABLE IF NOT EXISTS juhe_business.${tableName} (`)
  assert.ok(start >= 0, `Goose migration 缺少 ${tableName}`)
  const end = source.indexOf('\n);', start)
  assert.ok(end > start, `Goose migration 无法解析 ${tableName}`)
  return source.slice(start, end + 3)
}

function tableColumnNames(source: string): string[] {
  return source
    .split(/\r?\n/)
    .map((line) => /^\s{2,}([a-z][a-z0-9_]*)\s+(?:text|integer|bigint)\b/i.exec(line)?.[1])
    .filter((value): value is string => Boolean(value))
}
