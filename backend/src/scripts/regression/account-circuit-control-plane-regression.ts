import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient } from '../../storage/database-client.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import {
  acknowledgeAccountCircuitOutboxInClient,
  advanceAccountCircuitDispatchRevisionInClient,
  claimAccountCircuitOutboxInClient,
  cleanupAccountCircuitControlPlaneInClient,
  compareAndSetAccountCircuitIncidentInClient,
  getAccountCircuitIncidentByScopeKeyInClient,
  listAccountCircuitIncidentsByRuntimeKeysInClient,
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
const confirmationEvidenceA = 'a'.repeat(64)
const confirmationEvidenceB = 'b'.repeat(64)
const maximumChildIncidentIds = Array.from({ length: 64 }, (_, index) => `child-${index + 1}`)

insertSchemaFixture()
insertAccount()

try {
  assertCurrentSchema()
  assertLegacySqliteSchemaUpgrade()
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
  assert.equal(created.incident?.confirmationFailuresRequired, 2)
  assert.equal(created.incident?.consecutiveFailures, 1)
  assert.deepEqual(created.incident?.childIncidentIds, maximumChildIncidentIds, 'ledger 必须支持配置允许的 64 个独立 protocol/model child scope')
  assert.deepEqual(created.incident?.confirmationFailureEvidenceKeys, [confirmationEvidenceA, confirmationEvidenceB])

  const suspectRebuild = await listAccountCircuitIncidentsForRebuildInClient(client, {
    nowMs: 1_501,
    limit: 10
  })
  assert.equal(suspectRebuild.items.length, 1)
  assert.equal(suspectRebuild.items[0]?.confirmationFailuresRequired, 2)
  assert.equal(suspectRebuild.items[0]?.consecutiveFailures, 1)
  assert.deepEqual(suspectRebuild.items[0]?.confirmationFailureEvidenceKeys, [confirmationEvidenceA, confirmationEvidenceB])

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
    retainedUntilMs: 20_000,
    consecutiveFailures: 0,
    confirmationFailureEvidenceKeys: []
  }))
  assert.equal(closed.status, 'applied')
  assert.equal(closed.incident?.ledgerRevision, 3)
  assert.equal(closed.incident?.retainedUntilMs, 20_000)

  const delayedOlderGeneration = await compareAndSetAccountCircuitIncidentInClient(client, incidentMutation({
    expectedLedgerRevision: 3,
    state: 'RECOVERING',
    transitionId: 'incident-delayed-older-generation',
    generation: 0,
    dispatchRevision: 3,
    stateUpdatedAtMs: 1_600,
    nextTransitionAtMs: 4_500,
    consecutiveFailures: 0,
    confirmationFailureEvidenceKeys: []
  }))
  assert.equal(delayedOlderGeneration.status, 'cas_conflict', '迟到旧 generation 即使带更新时间也不得覆盖 CLOSED')
  assert.equal(delayedOlderGeneration.incident?.state, 'CLOSED')
  assert.equal(delayedOlderGeneration.incident?.ledgerRevision, 3)

  const rebuild = await listAccountCircuitIncidentsForRebuildInClient(client, {
    nowMs: 10_000,
    limit: 10
  })
  assert.equal(rebuild.items.length, 1, '未过保留期的 CLOSED tombstone 必须参与重建')
  assert.equal(rebuild.items[0]?.state, 'CLOSED')
  assert.deepEqual(
    await listAccountCircuitIncidentsByRuntimeKeysInClient(client, [accountRuntimeKey]),
    [],
    '管理摘要默认不得返回 CLOSED tombstone'
  )
  assert.equal(
    (await listAccountCircuitIncidentsByRuntimeKeysInClient(client, [accountRuntimeKey], {
      includeRetainedClosed: true,
      nowMs: 10_000
    }))[0]?.state,
    'CLOSED',
    '按账户渐进 readiness 必须读取未过保留期的 CLOSED tombstone'
  )
  assert.deepEqual(
    await listAccountCircuitIncidentsByRuntimeKeysInClient(client, [accountRuntimeKey], {
      includeRetainedClosed: true,
      nowMs: 20_001
    }),
    [],
    '按账户渐进 readiness 不得加载已经过保留期的 CLOSED tombstone'
  )
  assert.equal(
    (await cleanupAccountCircuitControlPlaneInClient(client, {
      nowMs: 20_001,
      outboxAcknowledgedBeforeMs: 20_001,
      limit: 100
    })).deletedIncidents,
    0,
    '已过期 CLOSED tombstone 在 outbox ACK 前仍必须保留'
  )
  assert.equal(
    (await getAccountCircuitIncidentByScopeKeyInClient(client, circuitScopeKey))?.state,
    'CLOSED',
    'outbox 投影直查必须读取已过保留期但尚未 ACK 的 CLOSED tombstone'
  )

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
    childIncidentIds: maximumChildIncidentIds,
    causedByTerminalOutcomeId: 'terminal-1',
    failureScope: 'account',
    consecutiveFailures: 1,
    confirmationFailuresRequired: 2,
    confirmationFailureEvidenceKeys: [confirmationEvidenceA, confirmationEvidenceB],
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

  const incidentColumns = database.prepare('PRAGMA table_info(account_circuit_incidents)').all() as Array<{ name: string; dflt_value: string | null }>
  const outboxColumns = database.prepare('PRAGMA table_info(account_circuit_outbox)').all() as Array<{ name: string }>
  assert.equal(incidentColumns.some((column) => /response|payload|body|message/i.test(column.name)), false, 'incident ledger 禁止保存响应内容')
  assert.equal(outboxColumns.some((column) => /response|payload|body|message/i.test(column.name)), false, 'outbox 禁止保存响应内容')
  for (const required of ['incident_id', 'transition_id', 'dispatch_revision', 'ledger_revision', 'projected_ledger_revision', 'retained_until_ms', 'confirmation_failures_required', 'confirmation_failure_evidence_keys_json']) {
    assert.equal(incidentColumns.some((column) => column.name === required), true, `incident ledger 缺少 ${required}`)
  }
  assert.equal(incidentColumns.find((column) => column.name === 'confirmation_failures_required')?.dflt_value, '1', 'SQLite schema 默认 1 必须兼容升级前 active incident')
  assert.equal(incidentColumns.find((column) => column.name === 'confirmation_failure_evidence_keys_json')?.dflt_value, "'[]'")
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
  assert.match(generatedIncident, /confirmation_failures_required integer NOT NULL DEFAULT 1/)
  assert.match(generatedIncident, /jsonb_typeof\(confirmation_failure_evidence_keys_json::jsonb\) = 'array'/)
  assert.match(generatedIncident, /jsonb_array_length\(confirmation_failure_evidence_keys_json::jsonb\) <= confirmation_failures_required \+ 1/)
  assert.doesNotMatch(generatedIncident, /\bjson_array_length\(/, 'PostgreSQL fresh schema 不得残留 SQLite JSON 函数')
  assert.match(generatedOutbox, /dispatch_revision bigint[\s\S]+claim_until_ms bigint[\s\S]+acknowledged_at_ms bigint/)

}

function assertLegacySqliteSchemaUpgrade(): void {
  const legacyDatabase = new DatabaseSync(':memory:')
  try {
    applyBusinessSchema(legacyDatabase)
    const oldColumnNames = (legacyDatabase.prepare('PRAGMA table_info(account_circuit_incidents)').all() as Array<{ name: string }>)
      .map((column) => column.name)
      .filter((name) => !['confirmation_failures_required', 'confirmation_failure_evidence_keys_json'].includes(name))
    const selectedColumns = oldColumnNames.map((name) => `"${name}"`).join(', ')
    legacyDatabase.exec(`
      PRAGMA foreign_keys = OFF;
      CREATE TABLE account_circuit_incidents_legacy AS
        SELECT ${selectedColumns} FROM account_circuit_incidents WHERE 0;
      DROP TABLE account_circuit_incidents;
      ALTER TABLE account_circuit_incidents_legacy RENAME TO account_circuit_incidents;
      INSERT INTO account_circuit_incidents (circuit_scope_key) VALUES ('legacy-active-incident');
    `)

    applyBusinessSchema(legacyDatabase)
    const upgraded = legacyDatabase.prepare(`
      SELECT confirmation_failures_required, confirmation_failure_evidence_keys_json
      FROM account_circuit_incidents
      WHERE circuit_scope_key = 'legacy-active-incident'
    `).get() as { confirmation_failures_required: number; confirmation_failure_evidence_keys_json: string }
    assert.equal(upgraded.confirmation_failures_required, 1, 'SQLite 旧表升级必须用旧行为阈值 1 补齐历史 incident')
    assert.equal(upgraded.confirmation_failure_evidence_keys_json, '[]', 'SQLite 旧表升级必须为空 evidence 补默认值')
  } finally {
    legacyDatabase.close()
  }
}
