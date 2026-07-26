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

const [databaseModule, repositories, qualityRepository, healthRepository, healthMonitorRepository, modelCheckRepository, scheduledQualityService, accountOptionsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/model-quality.repository.js'),
  import('../../storage/model-quality-health.repository.js'),
  import('../../storage/account-health-monitor.repository.js'),
  import('../../storage/model-checks.repository.js'),
  import('../../modules/background/model-quality-scheduled-check.service.js'),
  import('../../storage/account-options.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const stoppedSignal = AbortSignal.abort(new Error('scheduler stopping'))
  assert.deepEqual(
    await scheduledQualityService.runDueModelQualityScheduledChecks(stoppedSignal),
    { claimed: 0, completed: 0, failed: 0 },
    '父任务已取消时不得 claim 新的模型质量定时检查'
  )
  assert.deepEqual(
    await scheduledQualityService.runDueModelQualityRecoveries(stoppedSignal),
    { claimed: 0, completed: 0, failed: 0 },
    '父任务已取消时不得 claim 新的模型质量恢复检查'
  )
  assert.deepEqual(
    await scheduledQualityService.retryFailedModelQualityHealthSyncs(stoppedSignal),
    { selected: 0, completed: 0, failed: 0 },
    '父任务已取消时不得领取新的模型质量健康同步补偿'
  )

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
  const accountWithoutRunnableModel = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '模型质量无可运行模型账户',
    type: 'api_key',
    credentials: { api_key: 'sk-model-quality-no-runnable-model', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-image-2'],
    groupId: group.id,
    status: 'active'
  }, access)
  const business = databaseModule.getBusinessDatabase()
  business.prepare("UPDATE accounts SET status = 'active', schedulable = 1 WHERE id = ?").run(account.id)
  business.prepare("UPDATE accounts SET status = 'active', schedulable = 1, priority = 0 WHERE id = ?").run(accountWithoutRunnableModel.id)

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
  const scheduleInput = {
    accountId: account.id,
    model: 'gpt-5.5',
    intervalMinutes: 60,
    profile: 'full' as const,
    penaltyThreshold: 58,
    penaltyAction: 'quality_isolate' as const,
    recoveryIntervalMinutes: 45
  }
  await assert.rejects(() => qualityRepository.upsertModelQualityScheduleAsync('sys_admin', { ...scheduleInput, intervalMinutes: 9 }), /10 到 10080/)
  await assert.rejects(() => qualityRepository.upsertModelQualityScheduleAsync('sys_admin', { ...scheduleInput, model: 'gpt-5.4' }), /账户模型限制或供应商协议不支持/)
  const schedule = await qualityRepository.upsertModelQualityScheduleAsync('sys_admin', scheduleInput)
  assert.equal(schedule.intervalMinutes, 60)
  assert.equal(schedule.profile, 'full')
  assert.equal(schedule.penaltyThreshold, 58)
  assert.equal(schedule.penaltyAction, 'quality_isolate')
  assert.equal(schedule.recoveryIntervalMinutes, 45)
  business.prepare("UPDATE model_quality_schedules SET next_run_at = '2020-01-01T00:00:00.000Z' WHERE id = ?").run(schedule.id)
  const claimedSchedules = await qualityRepository.claimDueModelQualitySchedulesAsync('regression-schedule-policy', { now: '2026-07-26T00:00:00.000Z' })
  assert.equal(claimedSchedules.length, 1)
  assert.equal(claimedSchedules[0].policy.profile, 'full')
  assert.equal(claimedSchedules[0].policy.penaltyThreshold, 58)
  assert.equal(claimedSchedules[0].policy.penaltyAction, 'quality_isolate')
  assert.equal(claimedSchedules[0].policy.recoveryIntervalMinutes, 45)
  await qualityRepository.completeModelQualityScheduleRunAsync({
    ownerId: 'regression-schedule-policy',
    scheduleId: schedule.id,
    scheduleRevision: schedule.revision,
    intervalMinutes: schedule.intervalMinutes,
    status: 'completed',
    completedAt: '2026-07-26T00:00:30.000Z'
  })
  const scheduledAccountRevision = Number((business.prepare('SELECT config_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number }).config_revision)
  const scheduledEnforcement = await qualityRepository.applyModelQualityEnforcementAsync({
    systemAccountId: 'sys_admin',
    accountId: account.id,
    runId: 'mcr_scheduled_quality_penalty',
    action: schedule.penaltyAction,
    policyRevision: schedule.revision,
    scheduleId: schedule.id,
    profile: schedule.profile,
    penaltyThreshold: schedule.penaltyThreshold,
    model: schedule.model,
    accountConfigRevision: scheduledAccountRevision,
    recoveryIntervalMinutes: schedule.recoveryIntervalMinutes,
    message: '定时计划独立策略回归'
  })
  assert.equal(scheduledEnforcement.result, 'applied')
  const scheduledEnforcementRow = business.prepare(`
    SELECT config_source, config_source_id, profile, penalty_threshold, recovery_interval_minutes, recovery_model
    FROM account_quality_enforcements WHERE account_id = ?
  `).get(account.id) as Record<string, string | number>
  assert.equal(scheduledEnforcementRow.config_source, 'schedule')
  assert.equal(scheduledEnforcementRow.config_source_id, schedule.id)
  assert.equal(scheduledEnforcementRow.profile, 'full')
  assert.equal(Number(scheduledEnforcementRow.penalty_threshold), 58)
  assert.equal(Number(scheduledEnforcementRow.recovery_interval_minutes), 45)
  assert.equal(scheduledEnforcementRow.recovery_model, 'gpt-5.5')
  assert.throws(
    () => business.prepare('UPDATE account_quality_enforcements SET config_source_id = NULL WHERE account_id = ?').run(account.id),
    /CHECK constraint failed/,
    'schedule enforcement 必须保留非空 config_source_id'
  )
  assert.throws(
    () => business.prepare("UPDATE account_quality_enforcements SET config_source = 'manual' WHERE account_id = ?").run(account.id),
    /CHECK constraint failed/,
    'manual enforcement 不得保留 schedule config_source_id'
  )
  business.prepare("UPDATE account_quality_enforcements SET recovery_due_at = '2020-01-01T00:00:00.000Z' WHERE account_id = ?").run(account.id)
  const scheduledRecoveries = await qualityRepository.claimDueModelQualityRecoveriesAsync('regression-scheduled-recovery', { now: '2026-07-26T00:01:00.000Z' })
  assert.equal(scheduledRecoveries.length, 1)
  assert.equal(scheduledRecoveries[0].scheduleId, schedule.id)
  assert.equal(scheduledRecoveries[0].policy.profile, 'full')
  assert.equal(scheduledRecoveries[0].policy.penaltyThreshold, 58)
  assert.equal(scheduledRecoveries[0].policy.recoveryIntervalMinutes, 45)
  const scheduledRecovered = await qualityRepository.completeModelQualityRecoveryAsync({
    ownerId: 'regression-scheduled-recovery', accountId: account.id, enforcementId: scheduledRecoveries[0].enforcementId,
    generation: scheduledRecoveries[0].generation, policyRevision: scheduledRecoveries[0].policy.revision,
    runId: 'mcr_scheduled_quality_recovery', passed: true, recoveryIntervalMinutes: scheduledRecoveries[0].policy.recoveryIntervalMinutes,
    completedAt: '2026-07-26T00:01:30.000Z'
  })
  assert.equal(scheduledRecovered.result, 'recovered')
  const updatedSchedule = await qualityRepository.upsertModelQualityScheduleAsync('sys_admin', {
    ...scheduleInput,
    expectedRevision: schedule.revision,
    penaltyThreshold: 61
  })
  assert.equal(updatedSchedule.revision, schedule.revision + 1)
  const staleScheduledAccountRevision = Number((business.prepare('SELECT config_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number }).config_revision)
  const staleScheduledEnforcement = await qualityRepository.applyModelQualityEnforcementAsync({
    systemAccountId: 'sys_admin', accountId: account.id, runId: 'mcr_stale_scheduled_quality_penalty',
    action: schedule.penaltyAction, policyRevision: schedule.revision, scheduleId: schedule.id,
    profile: schedule.profile, penaltyThreshold: schedule.penaltyThreshold, model: schedule.model,
    accountConfigRevision: staleScheduledAccountRevision, recoveryIntervalMinutes: schedule.recoveryIntervalMinutes,
    message: '旧计划策略不得处罚'
  })
  assert.equal(staleScheduledEnforcement.result, 'stale')
  const modelCheckAccountOptions = accountOptionsRepository.listModelCheckAccountOptions(access, { purpose: 'run', limit: 1 })
  assert.deepEqual(modelCheckAccountOptions.find((item) => item.id === account.id)?.modelCheckModels, ['gpt-5.5'])
  assert.equal(modelCheckAccountOptions.some((item) => item.id === accountWithoutRunnableModel.id), false, '运行账户选项不得返回没有任何可检测模型的账户')
  const modelCheckHistoryAccountOptions = accountOptionsRepository.listModelCheckAccountOptions(access, { purpose: 'history', limit: 20 })
  assert.deepEqual(modelCheckHistoryAccountOptions.find((item) => item.id === accountWithoutRunnableModel.id)?.modelCheckModels, [], '历史筛选仍应保留没有当前可运行模型的旧账户')

  const row = business.prepare('SELECT config_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number }
  const enforcement = await qualityRepository.applyModelQualityEnforcementAsync({
    systemAccountId: 'sys_admin',
    accountId: account.id,
    runId: 'mcr_quality_penalty',
    action: 'quality_isolate',
    policyRevision: policy.revision,
    profile: policy.profile,
    penaltyThreshold: policy.penaltyThreshold,
    model: 'gpt-5.5',
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

  const secondRevision = Number((business.prepare('SELECT config_revision FROM accounts WHERE id = ?').get(account.id) as { config_revision: number }).config_revision)
  const secondEnforcement = await qualityRepository.applyModelQualityEnforcementAsync({
    systemAccountId: 'sys_admin', accountId: account.id, runId: 'mcr_quality_penalty_second',
    action: 'quality_isolate', policyRevision: policy.revision, accountConfigRevision: secondRevision,
    profile: policy.profile, penaltyThreshold: policy.penaltyThreshold, model: 'gpt-5.5',
    recoveryIntervalMinutes: 10, message: '恢复竞态回归质量不达标'
  })
  assert.equal(secondEnforcement.result, 'applied')
  business.prepare("UPDATE account_quality_enforcements SET recovery_due_at = '2020-01-01T00:00:00.000Z' WHERE account_id = ?").run(account.id)
  const secondRecoveries = await qualityRepository.claimDueModelQualityRecoveriesAsync('regression-worker-stale', { now: '2026-07-26T00:02:00.000Z' })
  assert.equal(secondRecoveries.length, 1)
  business.prepare('UPDATE accounts SET config_revision = config_revision + 1 WHERE id = ?').run(account.id)
  const staleRecovery = await qualityRepository.completeModelQualityRecoveryAsync({
    ownerId: 'regression-worker-stale', accountId: account.id, enforcementId: secondRecoveries[0].enforcementId,
    generation: secondRecoveries[0].generation, policyRevision: secondRecoveries[0].policy.revision,
    runId: 'mcr_quality_recovery_stale', passed: true, recoveryIntervalMinutes: 10,
    completedAt: '2026-07-26T00:03:00.000Z'
  })
  assert.equal(staleRecovery.result, 'stale')
  assert.equal((business.prepare('SELECT status FROM accounts WHERE id = ?').get(account.id) as { status: string }).status, 'quality_isolated')

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

  const retryRun = repositories.createModelCheckRun({
    id: 'mcr_quality_health_retry',
    systemAccountId: 'sys_admin', actorSystemAccountId: 'sys_admin', providerCode: 'gpt',
    targetType: 'account', targetId: account.id, targetOwnerSystemAccountId: 'sys_admin',
    accountId: account.id, model: 'gpt-5.5', profile: 'quick', trustedComparison: false,
    trustedComparisonAvailable: false, probeSetVersion: 'quality-health-retry-regression',
    startedAt: '2026-07-26T02:00:00.000Z'
  })
  repositories.finishModelCheckRun(retryRun.id, {
    status: 'completed', level: 'suspicious', score: 40, message: '质量健康同步补偿回归',
    finishedAt: '2026-07-26T02:01:00.000Z'
  })
  modelCheckRepository.updateModelCheckQualityDecision(retryRun.id, {
    triggerKind: 'scheduled', triggered: true, hardFailure: true, threshold: 70, score: 40,
    configuredAction: 'fallback', result: 'applied', reasonCodes: ['hard_failure'],
    healthSyncResult: 'failed', message: '质量健康同步补偿回归', decidedAt: '2026-07-26T02:01:00.000Z'
  })
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  const retryResult = await scheduledQualityService.retryFailedModelQualityHealthSyncs()
  assert.equal(retryResult.completed, 1)
  const retriedDetail = modelCheckRepository.getModelCheckRunDetail(retryRun.id)
  assert.equal(retriedDetail?.qualityDecision?.healthSyncResult, 'applied')
  const retriedHour = databaseModule.getStatsDatabase().prepare(`
    SELECT model_check_run_id FROM account_quality_health_hourly WHERE account_id = ? AND model_check_run_id = ?
  `).get(account.id, retryRun.id) as { model_check_run_id: string } | undefined
  assert.equal(retriedHour?.model_check_run_id, retryRun.id)
  runtimeConfig.processRole = 'db-service'

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
