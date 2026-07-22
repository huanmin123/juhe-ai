import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  createModelCheckItemsAsync,
  createModelCheckRunAsync,
  finishModelCheckRunAsync,
  getModelCheckRunDetailAsync,
  listModelCheckRunsAsync
} from '../../storage/model-checks.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  aggregateModelTrustObservationsAsync,
  createModelCheckObservationsAsync,
  findModelAccountTrustResultAsync
} from '../../storage/model-trust.repository.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '模型检测 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `model_check_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const accountId = `acc_${marker}`
const runIds: string[] = []

const access: AccessScope = {
  systemAccountId: `admin_${marker}`,
  role: 'admin',
  systemAccountFilterId: systemAccountId
}

try {
  const run = await createModelCheckRunAsync({
    id: `mcr_${marker}`,
    systemAccountId,
    actorSystemAccountId: access.systemAccountId,
    providerCode: 'gpt',
    targetType: 'account',
    targetId: accountId,
    targetName: `模型检测 PG smoke ${marker}`,
    targetOwnerSystemAccountId: systemAccountId,
    accountId,
    groupId: `grp_${marker}`,
    model: 'gpt-5.5',
    profile: 'full',
    trustedComparison: true,
    trustedComparisonAvailable: false,
    traceId: `trace_${marker}`,
    probeSetVersion: 'postgres-smoke',
    startedAt: new Date().toISOString(),
    requestSummary: {
      marker,
      note: 'postgres smoke'
    }
  })
  runIds.push(run.id)
  assert.equal(run.id, `mcr_${marker}`, 'PG model check run should return the created id')
  assert.equal(run.status, 'running', 'PG model check run should start as running')

  const items = await createModelCheckItemsAsync(run.id, [
    {
      id: `mci_${marker}_1`,
      itemKey: 'target.responses_basic',
      itemType: 'responses',
      status: 'passed',
      score: 50,
      maxScore: 50,
      durationMs: 12,
      traceId: `trace_${marker}_1`,
      evidenceSummary: { message: 'ok' }
    },
    {
      id: `mci_${marker}_2`,
      itemKey: 'target.usage_shape',
      itemType: 'usage',
      status: 'warning',
      score: 30,
      maxScore: 50,
      durationMs: 8,
      traceId: `trace_${marker}_2`,
      evidenceSummary: { message: 'shape warning' }
    }
  ])
  assert.equal(items.length, 2, 'PG model check items should be inserted')
  assert.equal(await createModelCheckObservationsAsync([{
    id: `mco_${marker}`,
    runId: run.id,
    systemAccountId,
    accountId,
    providerCode: 'gpt',
    providerProtocolProfileId: 'gpt-openai-v1',
    endpointFamily: 'responses',
    requestedModel: 'gpt-5.5',
    mappedUpstreamModel: 'gpt-5.5',
    observedModel: 'gpt-5.5',
    mappingApplied: false,
    upstreamBucketHmac: `hmac-sha256-v1:${'a'.repeat(64)}`,
    cohortKeyHmac: `hmac-sha256-v1:${'b'.repeat(64)}`,
    populationKeyHmac: `hmac-sha256-v1:${'b'.repeat(64)}`,
    probeKeyHmac: `hmac-sha256-v1:${'c'.repeat(64)}`,
    probeFamily: 'token_input_differential',
    probeSetVersion: 'postgres-smoke',
    tokenizerVersion: 'js-tiktoken@1.0.21:o200k_base',
    featureVersion: 'none',
    roundIndex: 0,
    paddingTokens: 0,
    localInputTokens: 100,
    reportedInputTokens: 110,
    observationStatus: 'observed',
    identityStatus: 'consistent',
    mappingStatus: 'direct',
    protocolStatus: 'consistent',
    evidenceCoverage: 10
  }]), 1, 'PG model check observation should be inserted through current schema repository')
  for (let index = 0; index < 20; index += 1) {
    await aggregateModelTrustObservationsAsync(500)
    if (await findModelAccountTrustResultAsync(systemAccountId, accountId, 'gpt-5.5')) break
  }
  const trustResult = await findModelAccountTrustResultAsync(systemAccountId, accountId, 'gpt-5.5')
  assert.ok(trustResult, 'PG smoke 必须真实执行模型可信聚合并写入 latest')
  const activationIndex = await (await getPostgresPool()).query(`
    SELECT indexname FROM pg_indexes
    WHERE schemaname = 'juhe_stats' AND indexname = 'idx_model_token_integrity_windows_activation'
  `)
  assert.equal(activationIndex.rowCount, 1, 'PG smoke 必须存在固定截距激活查询索引')

  const finished = await finishModelCheckRunAsync(run.id, {
    level: 'likely',
    score: 80,
    maxScore: 100,
    status: 'completed',
    message: 'PG smoke completed',
    finishedAt: new Date().toISOString(),
    durationMs: 50,
    resultSummary: {
      itemCount: items.length,
      marker
    }
  })
  assert.equal(finished?.status, 'completed', 'PG model check run should finish')
  assert.equal(finished?.level, 'likely', 'PG model check run level should update')

  const detail = await getModelCheckRunDetailAsync(run.id, access)
  assert.ok(detail, 'PG model check run detail should be readable')
  assert.equal(detail.status, 'completed', 'PG model check detail should expose final status')
  assert.equal(detail.checks.length, 2, 'PG model check detail should include checks')
  assert.equal(detail.systemAccountId, systemAccountId, 'admin filtered access should include system account fields')

  const list = await listModelCheckRunsAsync(access, {
    page: 1,
    pageSize: 10,
    targetType: 'account',
    targetId: accountId,
    model: 'gpt-5.5',
    status: 'completed'
  })
  assert.ok(list.items.some((item) => item.id === run.id), 'PG model check list should include the smoke run')

  const hidden = await getModelCheckRunDetailAsync(run.id, {
    systemAccountId: `other_${marker}`,
    role: 'user'
  })
  assert.equal(hidden, undefined, 'PG model check detail should honor system account scope')

  console.log(JSON.stringify({
    message: '模型检测 PG smoke 通过',
    runCount: runIds.length,
    itemCount: items.length
  }))
} finally {
  await cleanupSmokeRows(runIds)
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(ids: string[]): Promise<void> {
  if (!ids.length) return
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.model_token_integrity_rounds WHERE account_id = $1', [accountId])
  await pool.query('DELETE FROM juhe_stats.model_token_integrity_windows WHERE account_id = $1', [accountId])
  await pool.query('DELETE FROM juhe_stats.model_trust_window_sources WHERE account_id = $1', [accountId])
  await pool.query('DELETE FROM juhe_stats.model_account_trust_results WHERE account_id = $1', [accountId])
  await pool.query('DELETE FROM juhe_stats.model_trust_latest_dirty_accounts WHERE account_id = $1', [accountId])
  await pool.query('DELETE FROM juhe_stats.model_token_intercept_baseline_versions WHERE probe_set_version = $1', ['postgres-smoke'])
  await pool.query('DELETE FROM juhe_dataset.model_check_items WHERE run_id = ANY($1::text[])', [ids])
  await pool.query('DELETE FROM juhe_dataset.model_check_observations WHERE run_id = ANY($1::text[])', [ids])
  await pool.query('DELETE FROM juhe_dataset.model_check_runs WHERE id = ANY($1::text[])', [ids])
}
