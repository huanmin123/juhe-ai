import { strict as assert } from 'node:assert'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-quality-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-quality-enforcement-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, qualityRepository, healthRepository, healthMonitorRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/model-quality.repository.js'),
  import('../../storage/model-quality-health.repository.js'),
  import('../../storage/account-health-monitor.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({ name: '模型质量回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '模型质量回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-model-quality-regression', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.5'],
    groupId: group.id,
    status: 'active'
  }, access)
  const business = databaseModule.getBusinessDatabase()
  business.prepare("UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?").run(account.id)

  const defaultPolicy = await qualityRepository.getModelQualityPolicyAsync('sys_admin')
  assert.equal(defaultPolicy.profile, 'quick')
  assert.equal(defaultPolicy.manualEnforcementEnabled, true)
  assert.equal(defaultPolicy.penaltyThreshold, 70)
  assert.equal(defaultPolicy.penaltyAction, 'fallback')
  assert.equal(defaultPolicy.recoveryIntervalMinutes, 10)
  await assert.rejects(() => qualityRepository.saveModelQualityPolicyAsync('sys_admin', { ...defaultPolicy, expectedRevision: 0, penaltyThreshold: 39 }), /40 到 100/)

  const policy = await qualityRepository.saveModelQualityPolicyAsync('sys_admin', {
    expectedRevision: 0,
    profile: 'quick',
    manualEnforcementEnabled: true,
    penaltyThreshold: 70,
    penaltyAction: 'quality_isolate',
    recoveryIntervalMinutes: 10
  })
  assert.equal(policy.revision, 1)
  await assert.rejects(() => qualityRepository.upsertModelQualityScheduleAsync('sys_admin', { accountId: account.id, model: 'gpt-5.5', intervalMinutes: 9 }), /10 到 10080/)
  const schedule = await qualityRepository.upsertModelQualityScheduleAsync('sys_admin', { accountId: account.id, model: 'gpt-5.5', intervalMinutes: 60 })
  assert.equal(schedule.intervalMinutes, 60)

  const row = business.prepare('SELECT config_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number }
  const enforcement = await qualityRepository.applyModelQualityEnforcementAsync({
    systemAccountId: 'sys_admin',
    accountId: account.id,
    runId: 'mcr_quality_penalty',
    action: 'quality_isolate',
    policyRevision: policy.revision,
    accountConfigRevision: Number(row.config_revision),
    recoveryIntervalMinutes: 10,
    message: '回归质量不达标'
  })
  assert.equal(enforcement.result, 'applied')
  assert.equal((business.prepare('SELECT status FROM accounts WHERE id = ?').get(account.id) as { status: string }).status, 'quality_isolated')
  business.prepare("UPDATE account_quality_enforcements SET recovery_due_at = '2020-01-01T00:00:00.000Z' WHERE account_id = ?").run(account.id)
  const recoveries = await qualityRepository.claimDueModelQualityRecoveriesAsync('regression-worker', { now: '2026-07-26T00:00:00.000Z' })
  assert.equal(recoveries.length, 1)
  assert.equal(recoveries[0].model, 'gpt-5.5')
  const recovered = await qualityRepository.completeModelQualityRecoveryAsync({
    ownerId: 'regression-worker', accountId: account.id, enforcementId: recoveries[0].enforcementId,
    generation: recoveries[0].generation, policyRevision: recoveries[0].policy.revision,
    runId: 'mcr_quality_recovery', passed: true, recoveryIntervalMinutes: 10,
    completedAt: '2026-07-26T00:01:00.000Z'
  })
  assert.equal(recovered.result, 'recovered')
  assert.equal((business.prepare('SELECT status FROM accounts WHERE id = ?').get(account.id) as { status: string }).status, 'active')

  const qualityHealth = await healthRepository.recordModelQualityHealthFailureAsync({
    accountId: account.id,
    systemAccountId: 'sys_admin',
    providerCode: 'gpt',
    observedAt: new Date().toISOString(),
    runId: 'mcr_quality_health',
    model: 'gpt-5.5',
    profile: 'quick',
    score: 69,
    threshold: 70,
    level: 'suspicious',
    errorCode: 'model_quality_failed',
    errorMessage: '模型质量回归失败'
  })
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO account_health_hourly (
      account_id, system_account_id, provider_code, stat_hour, status, last_observed_at,
      last_record_id, status_code, error_code, error_message, updated_at
    ) VALUES (?, 'sys_admin', 'gpt', ?, 'success', ?, 'ordinary-success', 200, NULL, NULL, ?)
  `).run(account.id, qualityHealth.statHour, new Date().toISOString(), new Date().toISOString())
  const healthPage = healthMonitorRepository.getAiHealthList(access, { hours: 2, page: 1, pageSize: 10 })
  const healthAccount = healthPage.items.find((item) => item.id === account.id)
  assert(healthAccount)
  assert.equal(healthAccount.hours.find((item) => item.statHour === qualityHealth.statHour)?.status, 'failure')

  const runIds: string[] = []
  for (let index = 0; index < 1000; index += 1) {
    const run = repositories.createModelCheckRun({
      id: `mcr_retention_${String(index).padStart(4, '0')}`,
      systemAccountId: 'sys_admin', actorSystemAccountId: 'sys_admin', providerCode: 'gpt',
      targetType: 'account', targetId: 'acc_retention', targetOwnerSystemAccountId: 'sys_admin',
      accountId: 'acc_retention', model: 'gpt-5.5', profile: 'quick', trustedComparison: false,
      trustedComparisonAvailable: false, probeSetVersion: 'retention-regression',
      startedAt: `2026-01-${String(1 + Math.floor(index / 40)).padStart(2, '0')}T00:${String(index % 40).padStart(2, '0')}:00.000Z`
    })
    repositories.finishModelCheckRun(run.id, { status: 'completed', level: 'likely', score: 80, message: 'done' })
    runIds.push(run.id)
  }
  databaseModule.getDatasetDatabase().prepare(`
    INSERT INTO model_check_observations (
      id, run_id, system_account_id, account_id, provider_code, provider_protocol_profile_id,
      endpoint_family, requested_model, mapped_upstream_model, mapping_applied,
      upstream_bucket_hmac, cohort_key_hmac, population_key_hmac, probe_key_hmac,
      probe_family, probe_set_version, tokenizer_version, feature_version, round_index,
      padding_tokens, local_input_tokens, observation_status, identity_status, mapping_status,
      protocol_status, evidence_coverage, created_at
    ) VALUES ('mco_retention', ?, 'sys_admin', 'acc_retention', 'gpt', ?, 'responses',
      'gpt-5.5', 'gpt-5.5', 0, 'upstream', 'cohort', 'population', 'probe', 'behavior',
      'retention-regression', 'none', 'none', 0, 0, 1, 'valid', 'consistent', 'direct', 'consistent', 100, ?)
  `).run(runIds[0], GPT_OPENAI_V1_PROFILE_ID, '2026-01-01T00:00:00.000Z')
  assert.throws(() => repositories.createModelCheckRun({
    systemAccountId: 'sys_admin', actorSystemAccountId: 'sys_admin', providerCode: 'gpt',
    targetType: 'account', targetId: 'acc_retention', accountId: 'acc_retention', model: 'gpt-5.5',
    profile: 'quick', trustedComparison: false, trustedComparisonAvailable: false, probeSetVersion: 'retention-regression'
  }), /尚未被统计聚合消费/)
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
    VALUES ('global', '', 'model-trust-observation-aggregation', '2026-12-31T23:59:59.999Z', 'zzzz', ?)
  `).run(new Date().toISOString())
  repositories.createModelCheckRun({
    systemAccountId: 'sys_admin', actorSystemAccountId: 'sys_admin', providerCode: 'gpt',
    targetType: 'account', targetId: 'acc_retention', accountId: 'acc_retention', model: 'gpt-5.5',
    profile: 'quick', trustedComparison: false, trustedComparisonAvailable: false, probeSetVersion: 'retention-regression'
  })
  const retainedCount = Number((databaseModule.getDatasetDatabase().prepare("SELECT COUNT(*) AS count FROM model_check_runs WHERE account_id = 'acc_retention'").get() as { count: number }).count)
  assert.equal(retainedCount, 901)

  console.log('model quality enforcement regression passed')
} finally {
  databaseModule.closeStorageDatabases()
}

process.exit(0)
