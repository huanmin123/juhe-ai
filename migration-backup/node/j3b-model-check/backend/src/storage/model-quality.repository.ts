import type {
  AccountStatus,
  ModelCheckProfile,
  ModelCheckRunStatus,
  ModelQualityPenaltyAction,
  ModelQualityPolicy,
  ModelQualityPolicyUpdateInput,
  ModelQualitySchedule,
  ModelQualityScheduleListResult
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { getPostgresPool } from './postgres-client.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'
import { isAccountAvailabilityScheduleAllowed } from './account-availability-schedule.js'
import { loadSupportedModelsByAccountIdsAsync } from './account-supported-models.repository.js'
import { loadModelMappingsByAccountIdsAsync } from './account-model-mappings.repository.js'
import { configuredModelCheckModelsForAccount } from '../modules/model-checks/model-checks.profiles.js'
import { passiveScheduleDelayMs } from '../shared/passive-schedule-jitter.js'

const businessSchemaName = 'juhe_business'
const defaultSchedulePageSize = 20
const maximumSchedulePageSize = 100

interface ModelQualityPolicyRow {
  system_account_id: string
  revision: number
  profile: ModelCheckProfile
  manual_enforcement_enabled: number
  penalty_threshold: number
  penalty_action: ModelQualityPenaltyAction
  recovery_interval_minutes: number
  created_at: string
  updated_at: string
}

interface ModelQualityScheduleRow {
  id: string
  system_account_id: string
  account_id: string
  account_name: string | null
  provider_code: string | null
  model: string
  interval_minutes: number
  profile: ModelCheckProfile
  penalty_threshold: number
  penalty_action: ModelQualityPenaltyAction
  recovery_interval_minutes: number
  enabled: number
  revision: number
  next_run_at: string
  last_run_id: string | null
  last_run_at: string | null
  last_run_status: Exclude<ModelCheckRunStatus, 'running'> | null
  enforcement_action: ModelQualityPenaltyAction | null
  enforcement_recovery_due_at: string | null
  created_at: string
  updated_at: string
}

interface AccountQualityMutationRow {
  id: string
  system_account_id: string
  status: AccountStatus
  config_revision: number
  fallback_enabled: number
  super_priority_enabled: number
  deleted_at: string | null
  authorization_instance_authorization_id: string | null
}

export interface ModelQualityScheduleMutationInput {
  accountId: string
  model: string
  intervalMinutes: number
  profile: ModelCheckProfile
  penaltyThreshold: number
  penaltyAction: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
  enabled?: boolean
  expectedRevision?: number
}

export type ModelQualitySchedulePatchInput = {
  expectedRevision: number
} & Partial<Pick<ModelQualityScheduleMutationInput,
  'model' | 'intervalMinutes' | 'profile' | 'penaltyThreshold' | 'penaltyAction' | 'recoveryIntervalMinutes' | 'enabled'>>

export interface ModelQualityEnforcementInput {
  systemAccountId: string
  accountId: string
  runId: string
  action: ModelQualityPenaltyAction
  policyRevision: number
  scheduleId?: string
  profile: ModelCheckProfile
  penaltyThreshold: number
  model: string
  accountConfigRevision: number
  recoveryIntervalMinutes: number
  message: string
  decidedAt?: string
}

export interface ModelQualityEnforcementWriteResult {
  result: 'applied' | 'already_effective' | 'skipped' | 'stale'
  beforeStatus?: AccountStatus
  afterStatus?: AccountStatus
  recoveryDueAt?: string
  enforcementId?: string
  generation?: number
  message: string
}

export interface ModelQualityScheduledRunCandidate {
  scheduleId: string
  scheduleRevision: number
  systemAccountId: string
  accountId: string
  model: string
  intervalMinutes: number
  policy: ModelQualityPolicy
}

export interface ModelQualityRecoveryCandidate {
  accountId: string
  systemAccountId: string
  model: string
  accountConfigRevision: number
  enforcementId: string
  generation: number
  scheduleId?: string
  policy: ModelQualityPolicy
}

export interface ModelQualityRecoveryCompletionResult {
  result: 'recovered' | 'kept_isolated' | 'stale'
  beforeStatus?: AccountStatus
  afterStatus?: AccountStatus
  nextRecoveryAt?: string
  message: string
}

export function defaultModelQualityPolicy(systemAccountId: string): ModelQualityPolicy {
  return {
    systemAccountId,
    revision: 0,
    profile: 'quick',
    manualEnforcementEnabled: true,
    penaltyThreshold: 70,
    penaltyAction: 'fallback',
    recoveryIntervalMinutes: 10
  }
}

export async function getModelQualityPolicyAsync(systemAccountId: string): Promise<ModelQualityPolicy> {
  const client = await businessClient()
  const row = await loadModelQualityPolicyRow(client, systemAccountId)
  return row ? policyFromRow(row) : defaultModelQualityPolicy(systemAccountId)
}

async function loadModelQualityPolicyRow(client: DatabaseClient, systemAccountId: string): Promise<ModelQualityPolicyRow | undefined> {
  return await client.one<ModelQualityPolicyRow>(`
    SELECT system_account_id, revision, profile, manual_enforcement_enabled, penalty_threshold,
           penalty_action, recovery_interval_minutes, created_at, updated_at
    FROM ${table(client, 'model_quality_policies')}
    WHERE system_account_id = ?
    LIMIT 1
  `, [systemAccountId])
}

export async function saveModelQualityPolicyAsync(systemAccountId: string, input: ModelQualityPolicyUpdateInput): Promise<ModelQualityPolicy> {
  assertPolicyInput(input)
  const client = await businessClient()
  const existing = await loadModelQualityPolicyRow(client, systemAccountId)
  if (existing && existing.revision !== input.expectedRevision) {
    throw new Error('模型质量检测配置已被其他操作修改，请刷新后重试')
  }
  if (!existing && input.expectedRevision !== 0) {
    throw new Error('模型质量检测配置已被其他操作修改，请刷新后重试')
  }
  const now = nowIso()
  if (!existing) {
    const defaults = defaultModelQualityPolicy(systemAccountId)
    const next = {
      profile: input.profile ?? defaults.profile,
      manualEnforcementEnabled: input.manualEnforcementEnabled ?? defaults.manualEnforcementEnabled,
      penaltyThreshold: input.penaltyThreshold ?? defaults.penaltyThreshold,
      penaltyAction: input.penaltyAction ?? defaults.penaltyAction,
      recoveryIntervalMinutes: input.recoveryIntervalMinutes ?? defaults.recoveryIntervalMinutes
    }
    if (next.profile === defaults.profile
      && next.manualEnforcementEnabled === defaults.manualEnforcementEnabled
      && next.penaltyThreshold === defaults.penaltyThreshold
      && next.penaltyAction === defaults.penaltyAction
      && next.recoveryIntervalMinutes === defaults.recoveryIntervalMinutes) return defaults
    const result = await client.execute(`
      INSERT INTO ${table(client, 'model_quality_policies')} (
        system_account_id, revision, profile, manual_enforcement_enabled, penalty_threshold,
        penalty_action, recovery_interval_minutes, created_at, updated_at
      ) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id) DO NOTHING
    `, [
      systemAccountId,
      next.profile,
      next.manualEnforcementEnabled ? 1 : 0,
      next.penaltyThreshold,
      next.penaltyAction,
      next.recoveryIntervalMinutes,
      now,
      now
    ])
    if (result.changes <= 0) throw new Error('模型质量检测配置已被其他操作修改，请刷新后重试')
  } else {
    const assignments: string[] = []
    const values: unknown[] = []
    if (input.profile !== undefined && input.profile !== existing.profile) {
      assignments.push('profile = ?')
      values.push(input.profile)
    }
    if (input.manualEnforcementEnabled !== undefined && input.manualEnforcementEnabled !== (existing.manual_enforcement_enabled === 1)) {
      assignments.push('manual_enforcement_enabled = ?')
      values.push(input.manualEnforcementEnabled ? 1 : 0)
    }
    if (input.penaltyThreshold !== undefined && input.penaltyThreshold !== Number(existing.penalty_threshold)) {
      assignments.push('penalty_threshold = ?')
      values.push(input.penaltyThreshold)
    }
    if (input.penaltyAction !== undefined && input.penaltyAction !== existing.penalty_action) {
      assignments.push('penalty_action = ?')
      values.push(input.penaltyAction)
    }
    if (input.recoveryIntervalMinutes !== undefined && input.recoveryIntervalMinutes !== Number(existing.recovery_interval_minutes)) {
      assignments.push('recovery_interval_minutes = ?')
      values.push(input.recoveryIntervalMinutes)
    }
    if (!assignments.length) return policyFromRow(existing)
    const result = await client.execute(`
      UPDATE ${table(client, 'model_quality_policies')}
      SET ${assignments.join(', ')}, revision = revision + 1, updated_at = ?
      WHERE system_account_id = ? AND revision = ?
    `, [
      ...values,
      now,
      systemAccountId,
      input.expectedRevision
    ])
    if (result.changes <= 0) throw new Error('模型质量检测配置已被其他操作修改，请刷新后重试')
  }
  const saved = await loadModelQualityPolicyRow(client, systemAccountId)
  if (!saved) throw new Error('模型质量检测配置保存失败')
  return policyFromRow(saved)
}

export async function listModelQualitySchedulesAsync(
  systemAccountId: string,
  options: { page?: number; pageSize?: number } = {}
): Promise<ModelQualityScheduleListResult> {
  const page = boundedInteger(options.page, 1, 1, Number.MAX_SAFE_INTEGER)
  const pageSize = boundedInteger(options.pageSize, defaultSchedulePageSize, 1, maximumSchedulePageSize)
  const client = await businessClient()
  const schedules = table(client, 'model_quality_schedules')
  const accounts = table(client, 'accounts')
  const enforcements = table(client, 'account_quality_enforcements')
  const totalRow = await client.one<{ count: number }>(`
    SELECT COUNT(*) AS count FROM ${schedules} WHERE system_account_id = ?
  `, [systemAccountId])
  const rows = await client.query<ModelQualityScheduleRow>(`
    SELECT ${modelQualityScheduleSelectColumns()},
           accounts.name AS account_name,
           accounts.provider_code,
           aqe.action AS enforcement_action,
           aqe.recovery_due_at AS enforcement_recovery_due_at
    FROM ${schedules} mqs
    JOIN ${accounts} accounts ON accounts.id = mqs.account_id AND accounts.deleted_at IS NULL
    LEFT JOIN ${enforcements} aqe ON aqe.account_id = mqs.account_id AND aqe.state = 'active'
    WHERE mqs.system_account_id = ?
    ORDER BY mqs.created_at DESC, mqs.id DESC
    LIMIT ? OFFSET ?
  `, [systemAccountId, pageSize + 1, (page - 1) * pageSize])
  const hasMore = rows.length > pageSize
  return {
    items: rows.slice(0, pageSize).map(scheduleFromRow),
    total: Number(totalRow?.count ?? 0),
    hasMore,
    page,
    pageSize
  }
}

export async function createModelQualityScheduleAsync(
  systemAccountId: string,
  input: ModelQualityScheduleMutationInput
): Promise<ModelQualitySchedule> {
  if (input.expectedRevision !== undefined) throw new Error('创建定时检查配置不接受 revision，请使用字段级更新')
  const accountId = requiredText(input.accountId, '定时检查账户不能为空')
  const model = requiredText(input.model, '定时检查模型不能为空')
  const intervalMinutes = strictInteger(input.intervalMinutes, 10, 10080, '定时检查间隔必须是 10 到 10080 的整数分钟')
  assertSchedulePolicyInput(input)
  const client = await businessClient()
  const schedules = table(client, 'model_quality_schedules')
  const existing = await client.one<{ id: string; revision: number }>(`
    SELECT id, revision FROM ${schedules}
    WHERE system_account_id = ? AND account_id = ?
    LIMIT 1
  `, [systemAccountId, accountId])
  const now = nowIso()
  const enabled = input.enabled !== false
  if (existing) throw new Error('该账户已存在定时检查配置，请使用字段级更新')
  await assertModelQualityScheduleModelAllowed(client, systemAccountId, accountId, model)
  const id = newId('mqs')
  const inserted = await client.execute(`
    INSERT INTO ${schedules} (
      id, system_account_id, account_id, model, interval_minutes, profile, penalty_threshold,
      penalty_action, recovery_interval_minutes, enabled, revision,
      next_run_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
    ON CONFLICT(system_account_id, account_id) DO NOTHING
  `, [id, systemAccountId, accountId, model, intervalMinutes, input.profile, input.penaltyThreshold, input.penaltyAction, input.recoveryIntervalMinutes, enabled ? 1 : 0, addPassiveMinutes(now, intervalMinutes), now, now])
  if (inserted.changes <= 0) throw new Error('该账户已存在定时检查配置，请使用字段级更新')
  const row = await findScheduleRow(client, systemAccountId, accountId)
  if (!row) throw new Error('定时检查配置保存失败')
  return scheduleFromRow(row)
}

export async function patchModelQualityScheduleAsync(
  systemAccountId: string,
  scheduleId: string,
  input: ModelQualitySchedulePatchInput
): Promise<ModelQualitySchedule> {
  assertSchedulePatchInput(input)
  const client = await businessClient()
  const schedules = table(client, 'model_quality_schedules')
  const existing = await client.one<{
    id: string
    account_id: string
    model: string
    interval_minutes: number
    profile: ModelCheckProfile
    penalty_threshold: number
    penalty_action: ModelQualityPenaltyAction
    recovery_interval_minutes: number
    enabled: number
    revision: number
  }>(`
    SELECT id, account_id, model, interval_minutes, profile, penalty_threshold,
           penalty_action, recovery_interval_minutes, enabled, revision
    FROM ${schedules}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `, [scheduleId, systemAccountId])
  if (!existing) throw new Error('定时检查配置不存在')
  if (existing.revision !== input.expectedRevision) throw new Error('定时检查配置已变化，请刷新后重试')

  if (input.model !== undefined && input.model !== existing.model) {
    await assertModelQualityScheduleModelAllowed(client, systemAccountId, existing.account_id, input.model)
  }

  const assignments: string[] = []
  const values: unknown[] = []
  const append = (column: string, value: unknown): void => {
    assignments.push(`${column} = ?`)
    values.push(value)
  }
  if (input.model !== undefined && input.model !== existing.model) append('model', input.model)
  if (input.intervalMinutes !== undefined && input.intervalMinutes !== Number(existing.interval_minutes)) {
    append('interval_minutes', input.intervalMinutes)
    append('next_run_at', addPassiveMinutes(nowIso(), input.intervalMinutes))
  }
  if (input.profile !== undefined && input.profile !== existing.profile) append('profile', input.profile)
  if (input.penaltyThreshold !== undefined && input.penaltyThreshold !== Number(existing.penalty_threshold)) append('penalty_threshold', input.penaltyThreshold)
  if (input.penaltyAction !== undefined && input.penaltyAction !== existing.penalty_action) append('penalty_action', input.penaltyAction)
  if (input.recoveryIntervalMinutes !== undefined && input.recoveryIntervalMinutes !== Number(existing.recovery_interval_minutes)) append('recovery_interval_minutes', input.recoveryIntervalMinutes)
  if (input.enabled !== undefined && input.enabled !== (existing.enabled === 1)) append('enabled', input.enabled ? 1 : 0)

  if (assignments.length) {
    const now = nowIso()
    const result = await client.execute(`
      UPDATE ${schedules}
      SET ${assignments.join(', ')}, revision = revision + 1, updated_at = ?
      WHERE id = ? AND system_account_id = ? AND revision = ?
    `, [...values, now, scheduleId, systemAccountId, input.expectedRevision])
    if (result.changes <= 0) throw new Error('定时检查配置已变化，请刷新后重试')
  }

  const row = await findScheduleRow(client, systemAccountId, existing.account_id)
  if (!row) throw new Error('定时检查配置保存失败')
  return scheduleFromRow(row)
}

async function assertModelQualityScheduleModelAllowed(
  client: DatabaseClient,
  systemAccountId: string,
  accountId: string,
  model: string
): Promise<void> {
  const account = await client.one<{
    id: string
    provider_code: string
    provider_protocol_profile_id: string
    protocol_code: string
    protocol_version: string
  }>(`
    SELECT id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version
    FROM ${table(client, 'accounts')}
    WHERE id = ? AND system_account_id = ? AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
    LIMIT 1
  `, [accountId, systemAccountId])
  if (!account) throw new Error('账户不存在、不是当前系统账户的自有账户或无权配置定时检查')
  const [supportedModelsByAccountId, modelMappingsByAccountId] = await Promise.all([
    loadSupportedModelsByAccountIdsAsync([account.id]),
    loadModelMappingsByAccountIdsAsync([account.id])
  ])
  const allowedModels = configuredModelCheckModelsForAccount({
    providerCode: account.provider_code,
    providerProtocolProfileId: account.provider_protocol_profile_id,
    protocolCode: account.protocol_code,
    protocolVersion: account.protocol_version,
    supportedModels: supportedModelsByAccountId.get(account.id) ?? [],
    modelMappings: modelMappingsByAccountId.get(account.id) ?? []
  })
  if (!allowedModels.includes(model)) {
    const allowedModelText = allowedModels.length ? `；可选模型：${allowedModels.join('、')}` : ''
    throw new Error(`账户模型限制或供应商协议不支持定时检查模型 ${model}${allowedModelText}`)
  }
}

export async function deleteModelQualityScheduleAsync(systemAccountId: string, scheduleId: string): Promise<boolean> {
  const client = await businessClient()
  const result = await client.execute(`
    DELETE FROM ${table(client, 'model_quality_schedules')}
    WHERE id = ? AND system_account_id = ?
  `, [scheduleId, systemAccountId])
  return result.changes > 0
}

export async function claimDueModelQualitySchedulesAsync(
  ownerId: string,
  options: { now?: string; limit?: number; leaseMinutes?: number } = {}
): Promise<ModelQualityScheduledRunCandidate[]> {
  const client = await businessClient()
  const now = normalizedIso(options.now)
  const limit = boundedInteger(options.limit, 3, 1, 20)
  const leaseUntil = addMinutes(now, boundedInteger(options.leaseMinutes, 5, 1, 30))
  const schedules = table(client, 'model_quality_schedules')
  const accounts = table(client, 'accounts')
  const claimed: ModelQualityScheduledRunCandidate[] = []
  await client.transaction(async (tx) => {
    const rows = await tx.query<{
      id: string
      revision: number
      system_account_id: string
      account_id: string
      model: string
      interval_minutes: number
      profile: ModelCheckProfile
      penalty_threshold: number
      penalty_action: ModelQualityPenaltyAction
      recovery_interval_minutes: number
    }>(`
      SELECT mqs.id, mqs.revision, mqs.system_account_id, mqs.account_id, mqs.model, mqs.interval_minutes,
             mqs.profile, mqs.penalty_threshold, mqs.penalty_action, mqs.recovery_interval_minutes
      FROM ${schedules} mqs
      JOIN ${accounts} accounts ON accounts.id = mqs.account_id
      WHERE mqs.enabled = 1
        AND mqs.next_run_at <= ?
        AND (mqs.lease_until IS NULL OR mqs.lease_until <= ?)
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL
        AND accounts.status = 'active'
      ORDER BY mqs.next_run_at ASC, mqs.id ASC
      LIMIT ?
      ${tx.driver === 'postgres' ? 'FOR UPDATE OF mqs SKIP LOCKED' : ''}
    `, [now, now, limit])
    for (const row of rows) {
      const result = await tx.execute(`
        UPDATE ${schedules}
        SET lease_owner = ?, lease_until = ?, updated_at = ?
        WHERE id = ? AND revision = ? AND enabled = 1
          AND (lease_until IS NULL OR lease_until <= ?)
      `, [ownerId, leaseUntil, now, row.id, row.revision, now])
      if (result.changes <= 0) continue
      claimed.push({
        scheduleId: row.id,
        scheduleRevision: row.revision,
        systemAccountId: row.system_account_id,
        accountId: row.account_id,
        model: row.model,
        intervalMinutes: Number(row.interval_minutes),
        policy: schedulePolicy(row)
      })
    }
  })
  return claimed
}

export async function completeModelQualityScheduleRunAsync(input: {
  ownerId: string
  scheduleId: string
  scheduleRevision: number
  intervalMinutes: number
  runId?: string
  status: Exclude<ModelCheckRunStatus, 'running'>
  completedAt?: string
}): Promise<boolean> {
  const client = await businessClient()
  const completedAt = normalizedIso(input.completedAt)
  const result = await client.execute(`
    UPDATE ${table(client, 'model_quality_schedules')}
    SET last_run_id = ?, last_run_at = ?, last_run_status = ?,
        next_run_at = ?,
        lease_owner = NULL, lease_until = NULL, updated_at = ?
    WHERE id = ? AND revision = ? AND lease_owner = ?
  `, [input.runId ?? null, completedAt, input.status, addPassiveMinutes(completedAt, strictInteger(input.intervalMinutes, 10, 10080, '定时检查间隔无效')), completedAt, input.scheduleId, input.scheduleRevision, input.ownerId])
  return result.changes > 0
}

export async function claimDueModelQualityRecoveriesAsync(
  ownerId: string,
  options: { now?: string; limit?: number; leaseMinutes?: number } = {}
): Promise<ModelQualityRecoveryCandidate[]> {
  const client = await businessClient()
  const now = normalizedIso(options.now)
  const limit = boundedInteger(options.limit, 2, 1, 10)
  const leaseUntil = addMinutes(now, boundedInteger(options.leaseMinutes, 6, 1, 30))
  const enforcements = table(client, 'account_quality_enforcements')
  const accounts = table(client, 'accounts')
  const candidates: ModelQualityRecoveryCandidate[] = []
  await client.transaction(async (tx) => {
    const rows = await tx.query<{
      account_id: string
      system_account_id: string
      enforcement_id: string
      generation: number
      recovery_model: string
      config_revision: number
      policy_revision: number
      config_source_id: string | null
      profile: ModelCheckProfile
      penalty_threshold: number
      recovery_interval_minutes: number
    }>(`
      SELECT aqe.account_id, aqe.system_account_id, aqe.enforcement_id, aqe.generation,
             COALESCE(NULLIF(aqe.recovery_model, ''), accounts.health_check_model) AS recovery_model,
             accounts.config_revision, aqe.policy_revision, aqe.config_source_id, aqe.profile,
             aqe.penalty_threshold, aqe.recovery_interval_minutes
      FROM ${enforcements} aqe
      JOIN ${accounts} accounts ON accounts.id = aqe.account_id
      WHERE aqe.state = 'active' AND aqe.action = 'quality_isolate'
        AND aqe.recovery_due_at IS NOT NULL AND aqe.recovery_due_at <= ?
        AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until <= ?)
        AND accounts.deleted_at IS NULL AND accounts.status = 'quality_isolated'
      ORDER BY aqe.recovery_due_at ASC, aqe.account_id ASC
      LIMIT ?
      ${tx.driver === 'postgres' ? 'FOR UPDATE OF aqe SKIP LOCKED' : ''}
    `, [now, now, limit])
    for (const row of rows) {
      const claimed = await tx.execute(`
        UPDATE ${enforcements}
        SET recovery_lease_owner = ?, recovery_lease_until = ?, account_config_revision = ?, updated_at = ?
        WHERE account_id = ? AND enforcement_id = ? AND generation = ?
          AND state = 'active' AND action = 'quality_isolate'
          AND (recovery_lease_until IS NULL OR recovery_lease_until <= ?)
      `, [ownerId, leaseUntil, row.config_revision, now, row.account_id, row.enforcement_id, row.generation, now])
      if (claimed.changes <= 0) continue
      candidates.push({
        accountId: row.account_id,
        systemAccountId: row.system_account_id,
        model: row.recovery_model,
        accountConfigRevision: Number(row.config_revision),
        enforcementId: row.enforcement_id,
        generation: Number(row.generation),
        scheduleId: row.config_source_id ?? undefined,
        policy: {
          systemAccountId: row.system_account_id,
          revision: Number(row.policy_revision),
          profile: row.profile,
          manualEnforcementEnabled: true,
          penaltyThreshold: Number(row.penalty_threshold),
          penaltyAction: 'quality_isolate',
          recoveryIntervalMinutes: Number(row.recovery_interval_minutes)
        }
      })
    }
  })
  return candidates
}

export async function completeModelQualityRecoveryAsync(input: {
  ownerId: string
  accountId: string
  enforcementId: string
  generation: number
  policyRevision: number
  runId: string
  passed: boolean
  recoveryIntervalMinutes: number
  completedAt?: string
}): Promise<ModelQualityRecoveryCompletionResult> {
  const client = await businessClient()
  const completedAt = normalizedIso(input.completedAt)
  const recoveryIntervalMinutes = strictInteger(input.recoveryIntervalMinutes, 10, 10080, '质量隔离恢复周期无效')
  let accountChanged = false
  const result = await client.transaction(async (tx): Promise<ModelQualityRecoveryCompletionResult> => {
    const enforcement = await tx.one<{ state: string; action: string; system_account_id: string; account_config_revision: number; policy_revision: number }>(`
      SELECT state, action, system_account_id, account_config_revision, policy_revision FROM ${table(tx, 'account_quality_enforcements')}
      WHERE account_id = ? AND enforcement_id = ? AND generation = ? AND recovery_lease_owner = ?
      LIMIT 1
    `, [input.accountId, input.enforcementId, input.generation, input.ownerId])
    if (!enforcement || enforcement.state !== 'active' || enforcement.action !== 'quality_isolate') {
      return { result: 'stale', message: '质量隔离处罚代次或恢复租约已变化，本次恢复结果已忽略' }
    }
    if (Number(enforcement.policy_revision) !== input.policyRevision) {
      const nextRecoveryAt = addPassiveMinutes(completedAt, recoveryIntervalMinutes)
      await rescheduleRecovery(tx, input, nextRecoveryAt, completedAt)
      return { result: 'stale', nextRecoveryAt, message: '处罚配置快照已变化，本次不解除隔离并等待复检' }
    }
    if (!input.passed) {
      const nextRecoveryAt = addPassiveMinutes(completedAt, recoveryIntervalMinutes)
      await rescheduleRecovery(tx, input, nextRecoveryAt, completedAt)
      return { result: 'kept_isolated', beforeStatus: 'quality_isolated', afterStatus: 'quality_isolated', nextRecoveryAt, message: `质量恢复检查未达标，账户继续隔离；下次检查时间 ${nextRecoveryAt}` }
    }
    const account = await tx.one<{ status: AccountStatus; config_revision: number; availability_schedule_json: string | null }>(`
      SELECT status, config_revision, availability_schedule_json FROM ${table(tx, 'accounts')}
      WHERE id = ? AND system_account_id = ? AND deleted_at IS NULL
      LIMIT 1
    `, [input.accountId, enforcement.system_account_id])
    if (!account || account.status !== 'quality_isolated') {
      return { result: 'stale', beforeStatus: account?.status, afterStatus: account?.status, message: '账户已被用户或其他任务修改，本次恢复结果已忽略' }
    }
    if (Number(account.config_revision) !== Number(enforcement.account_config_revision)) {
      const nextRecoveryAt = addPassiveMinutes(completedAt, recoveryIntervalMinutes)
      await rescheduleRecovery(tx, input, nextRecoveryAt, completedAt)
      return { result: 'stale', beforeStatus: account.status, afterStatus: account.status, nextRecoveryAt, message: '账户配置在恢复检查期间发生变化，本次不解除隔离并等待重新复检' }
    }
    const afterStatus: AccountStatus = isAccountAvailabilityScheduleAllowed(account.availability_schedule_json, new Date(completedAt)) ? 'active' : 'disabled'
    const updated = await tx.execute(`
      UPDATE ${table(tx, 'accounts')}
      SET status = ?, schedulable = ?, last_error_code = NULL, last_error_message = NULL,
          config_revision = config_revision + 1, updated_at = ?
      WHERE id = ? AND status = 'quality_isolated'
    `, [afterStatus, afterStatus === 'active' ? 1 : 0, completedAt, input.accountId])
    if (updated.changes <= 0) return { result: 'stale', beforeStatus: account.status, afterStatus: account.status, message: '账户状态在恢复提交前已变化' }
    accountChanged = true
    await tx.execute(`
      UPDATE ${table(tx, 'account_quality_enforcements')}
      SET state = 'cleared', last_recovery_run_id = ?, cleared_at = ?, recovery_due_at = NULL,
          recovery_lease_owner = NULL, recovery_lease_until = NULL, updated_at = ?
      WHERE account_id = ? AND enforcement_id = ? AND generation = ? AND recovery_lease_owner = ?
    `, [input.runId, completedAt, completedAt, input.accountId, input.enforcementId, input.generation, input.ownerId])
    return { result: 'recovered', beforeStatus: account.status, afterStatus, message: afterStatus === 'active' ? '质量恢复检查达标，账户已恢复可调度' : '质量恢复检查达标，但当前不在可用时段，账户保持停用' }
  })
  if (accountChanged) invalidateQualityAccount(input.accountId)
  return result
}

async function rescheduleRecovery(
  client: DatabaseClient,
  input: { ownerId: string; accountId: string; enforcementId: string; generation: number; runId: string },
  nextRecoveryAt: string,
  completedAt: string
): Promise<void> {
  await client.execute(`
    UPDATE ${table(client, 'account_quality_enforcements')}
    SET last_recovery_run_id = ?, recovery_due_at = ?, recovery_lease_owner = NULL,
        recovery_lease_until = NULL, updated_at = ?
    WHERE account_id = ? AND enforcement_id = ? AND generation = ? AND recovery_lease_owner = ?
  `, [input.runId, nextRecoveryAt, completedAt, input.accountId, input.enforcementId, input.generation, input.ownerId])
}

export async function applyModelQualityEnforcementAsync(input: ModelQualityEnforcementInput): Promise<ModelQualityEnforcementWriteResult> {
  const client = await businessClient()
  const decidedAt = input.decidedAt ?? nowIso()
  let changed = false
  const result = await client.transaction(async (tx): Promise<ModelQualityEnforcementWriteResult> => {
    const configurationMatches = input.scheduleId
      ? await scheduledEnforcementConfigurationMatches(tx, input)
      : await manualEnforcementConfigurationMatches(tx, input)
    if (!configurationMatches) {
      return { result: 'stale', message: '检测配置已变化，本次仅保留质量事实，不修改账户' }
    }
    const account = await tx.one<AccountQualityMutationRow>(`
      SELECT id, system_account_id, status, config_revision, fallback_enabled,
             super_priority_enabled, deleted_at, authorization_instance_authorization_id
      FROM ${table(tx, 'accounts')}
      WHERE id = ?
      LIMIT 1
    `, [input.accountId])
    if (!account || account.deleted_at || account.system_account_id !== input.systemAccountId || account.authorization_instance_authorization_id) {
      return { result: 'skipped', message: '账户不存在、已删除或不是可处罚的自有账户' }
    }
    if (account.config_revision !== input.accountConfigRevision) {
      return { result: 'stale', beforeStatus: account.status, afterStatus: account.status, message: '账户配置已变化，本次处罚已跳过' }
    }
    if (!allowedEnforcementSourceStatus(account.status, input.action)) {
      return { result: 'skipped', beforeStatus: account.status, afterStatus: account.status, message: `账户当前状态为 ${account.status}，不在自动处罚允许来源状态中` }
    }
    const prior = await tx.one<{ enforcement_id: string; generation: number; state: string; action: ModelQualityPenaltyAction; trigger_run_id: string }>(`
      SELECT enforcement_id, generation, state, action, trigger_run_id
      FROM ${table(tx, 'account_quality_enforcements')}
      WHERE account_id = ?
      LIMIT 1
    `, [account.id])
    if (prior?.trigger_run_id === input.runId && prior.state === 'active') {
      return {
        result: 'already_effective',
        beforeStatus: account.status,
        afterStatus: account.status,
        enforcementId: prior.enforcement_id,
        generation: prior.generation,
        message: '本次处罚已生效，无需重复执行'
      }
    }
    const generation = (prior?.generation ?? 0) + 1
    const enforcementId = newId('mqe')
    const recoveryDueAt = input.action === 'quality_isolate'
      ? addPassiveMinutes(decidedAt, input.recoveryIntervalMinutes)
      : undefined
    const targetStatus: AccountStatus = input.action === 'quality_isolate'
      ? 'quality_isolated'
      : input.action === 'disable' ? 'disabled' : account.status
    const fallbackAlreadyEnabled = input.action === 'fallback' && account.fallback_enabled === 1
    const statusAlreadyEffective = input.action !== 'fallback' && account.status === targetStatus
    if (!fallbackAlreadyEnabled && !statusAlreadyEffective) {
      const update = await tx.execute(`
        UPDATE ${table(tx, 'accounts')}
        SET status = ?,
            schedulable = CASE WHEN ? IN ('disable', 'quality_isolate') THEN 0 ELSE schedulable END,
            fallback_enabled = CASE WHEN ? = 'fallback' THEN 1 ELSE fallback_enabled END,
            super_priority_enabled = CASE WHEN ? = 'fallback' THEN 0 ELSE super_priority_enabled END,
            last_error_code = 'model_quality_failed',
            last_error_message = ?,
            config_revision = config_revision + 1,
            updated_at = ?
        WHERE id = ? AND config_revision = ?
      `, [targetStatus, input.action, input.action, input.action, input.message.slice(0, 1000), decidedAt, account.id, account.config_revision])
      if (update.changes <= 0) {
        return { result: 'stale', beforeStatus: account.status, afterStatus: account.status, message: '账户在处罚提交前已变化，本次处罚已跳过' }
      }
      changed = true
    }
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_quality_enforcements')} (
        account_id, system_account_id, enforcement_id, generation, state, action, trigger_run_id,
        config_source, config_source_id, policy_revision, profile, penalty_threshold,
        recovery_interval_minutes, recovery_model, account_config_revision, before_status, after_status,
        fallback_was_enabled, super_priority_was_enabled, started_at, recovery_due_at,
        created_at, updated_at
      ) VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(account_id) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        enforcement_id = excluded.enforcement_id,
        generation = excluded.generation,
        state = 'active',
        action = excluded.action,
        trigger_run_id = excluded.trigger_run_id,
        config_source = excluded.config_source,
        config_source_id = excluded.config_source_id,
        policy_revision = excluded.policy_revision,
        profile = excluded.profile,
        penalty_threshold = excluded.penalty_threshold,
        recovery_interval_minutes = excluded.recovery_interval_minutes,
        recovery_model = excluded.recovery_model,
        account_config_revision = excluded.account_config_revision,
        before_status = excluded.before_status,
        after_status = excluded.after_status,
        fallback_was_enabled = excluded.fallback_was_enabled,
        super_priority_was_enabled = excluded.super_priority_was_enabled,
        started_at = excluded.started_at,
        recovery_due_at = excluded.recovery_due_at,
        last_recovery_run_id = NULL,
        cleared_at = NULL,
        updated_at = excluded.updated_at
    `, [
      account.id,
      input.systemAccountId,
      enforcementId,
      generation,
      input.action,
      input.runId,
      input.scheduleId ? 'schedule' : 'manual',
      input.scheduleId ?? null,
      input.policyRevision,
      input.profile,
      input.penaltyThreshold,
      input.recoveryIntervalMinutes,
      input.model,
      input.accountConfigRevision,
      account.status,
      targetStatus,
      account.fallback_enabled,
      account.super_priority_enabled,
      decidedAt,
      recoveryDueAt ?? null,
      decidedAt,
      decidedAt
    ])
    return {
      result: fallbackAlreadyEnabled || statusAlreadyEffective ? 'already_effective' : 'applied',
      beforeStatus: account.status,
      afterStatus: targetStatus,
      recoveryDueAt,
      enforcementId,
      generation,
      message: enforcementMessage(input.action, fallbackAlreadyEnabled || statusAlreadyEffective, recoveryDueAt)
    }
  })
  if (changed) invalidateQualityAccount(input.accountId)
  return result
}

async function findScheduleRow(client: DatabaseClient, systemAccountId: string, accountId: string): Promise<ModelQualityScheduleRow | undefined> {
  return await client.one<ModelQualityScheduleRow>(`
    SELECT ${modelQualityScheduleSelectColumns()},
           accounts.name AS account_name,
           accounts.provider_code,
           aqe.action AS enforcement_action,
           aqe.recovery_due_at AS enforcement_recovery_due_at
    FROM ${table(client, 'model_quality_schedules')} mqs
    JOIN ${table(client, 'accounts')} accounts ON accounts.id = mqs.account_id AND accounts.deleted_at IS NULL
    LEFT JOIN ${table(client, 'account_quality_enforcements')} aqe ON aqe.account_id = mqs.account_id AND aqe.state = 'active'
    WHERE mqs.system_account_id = ? AND mqs.account_id = ?
    LIMIT 1
  `, [systemAccountId, accountId])
}

function policyFromRow(row: ModelQualityPolicyRow): ModelQualityPolicy {
  return {
    systemAccountId: row.system_account_id,
    revision: Number(row.revision),
    profile: row.profile,
    manualEnforcementEnabled: row.manual_enforcement_enabled === 1,
    penaltyThreshold: Number(row.penalty_threshold),
    penaltyAction: row.penalty_action,
    recoveryIntervalMinutes: Number(row.recovery_interval_minutes),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function scheduleFromRow(row: ModelQualityScheduleRow): ModelQualitySchedule {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    accountId: row.account_id,
    accountName: row.account_name ?? undefined,
    providerCode: row.provider_code ?? undefined,
    model: row.model,
    intervalMinutes: Number(row.interval_minutes),
    profile: row.profile,
    penaltyThreshold: Number(row.penalty_threshold),
    penaltyAction: row.penalty_action,
    recoveryIntervalMinutes: Number(row.recovery_interval_minutes),
    enabled: row.enabled === 1,
    revision: Number(row.revision),
    nextRunAt: row.next_run_at,
    lastRunId: row.last_run_id ?? undefined,
    lastRunAt: row.last_run_at ?? undefined,
    lastRunStatus: row.last_run_status ?? undefined,
    currentEnforcementAction: row.enforcement_action ?? undefined,
    currentEnforcementRecoveryDueAt: row.enforcement_recovery_due_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function allowedEnforcementSourceStatus(status: AccountStatus, action: ModelQualityPenaltyAction): boolean {
  if (action === 'fallback') return status === 'active'
  if (action === 'disable') return status === 'active' || status === 'quality_isolated'
  return status === 'active'
}

function enforcementMessage(action: ModelQualityPenaltyAction, alreadyEffective: boolean, recoveryDueAt?: string): string {
  if (alreadyEffective) {
    if (action === 'fallback') return '账户原本已经是降级备用，本次处罚无需重复修改'
    if (action === 'disable') return '账户原本已经停用，本次处罚无需重复修改'
    return '账户已经处于质量隔离，本次处罚无需重复修改'
  }
  if (action === 'fallback') return '处罚已执行：账户已设为降级备用，超级优先已按现有互斥规则关闭'
  if (action === 'disable') return '处罚已执行：账户已停用'
  return `处罚已执行：账户已进入质量隔离${recoveryDueAt ? `，下次质量恢复检查时间 ${recoveryDueAt}` : ''}`
}

function invalidateQualityAccount(accountId: string): void {
  invalidateAccountLookupCache(accountId)
  invalidateGroupAccountIdsCache()
  notifyGatewayRuntimeCacheInvalidation('model_quality_enforcement')
}

function assertPolicyInput(input: ModelQualityPolicyUpdateInput): void {
  if (!Number.isInteger(input.expectedRevision) || input.expectedRevision < 0) throw new Error('检测配置 revision 无效')
  if (input.profile !== undefined && input.profile !== 'quick' && input.profile !== 'full') throw new Error('检测 profile 仅支持 quick 或 full')
  if (input.manualEnforcementEnabled !== undefined && typeof input.manualEnforcementEnabled !== 'boolean') throw new Error('手动检测处罚开关无效')
  if (input.penaltyThreshold !== undefined) strictInteger(input.penaltyThreshold, 40, 100, '处罚阈值必须是 40 到 100 的整数')
  if (input.penaltyAction !== undefined && input.penaltyAction !== 'disable' && input.penaltyAction !== 'fallback' && input.penaltyAction !== 'quality_isolate') {
    throw new Error('处罚方式无效')
  }
  if (input.recoveryIntervalMinutes !== undefined) strictInteger(input.recoveryIntervalMinutes, 10, 10080, '质量隔离恢复周期必须是 10 到 10080 的整数分钟')
}

function assertSchedulePolicyInput(input: ModelQualityScheduleMutationInput): void {
  if (input.profile !== 'quick' && input.profile !== 'full') throw new Error('定时检测 profile 仅支持 quick 或 full')
  strictInteger(input.penaltyThreshold, 40, 100, '定时处罚阈值必须是 40 到 100 的整数')
  if (input.penaltyAction !== 'disable' && input.penaltyAction !== 'fallback' && input.penaltyAction !== 'quality_isolate') throw new Error('定时处罚方式无效')
  strictInteger(input.recoveryIntervalMinutes, 10, 10080, '定时质量隔离恢复周期必须是 10 到 10080 的整数分钟')
}

function assertSchedulePatchInput(input: ModelQualitySchedulePatchInput): void {
  if (!Number.isInteger(input.expectedRevision) || input.expectedRevision < 1) throw new Error('定时检查配置 revision 无效')
  if (input.model !== undefined) requiredText(input.model, '定时检查模型不能为空')
  if (input.intervalMinutes !== undefined) strictInteger(input.intervalMinutes, 10, 10080, '定时检查间隔必须是 10 到 10080 的整数分钟')
  if (input.profile !== undefined && input.profile !== 'quick' && input.profile !== 'full') throw new Error('定时检测 profile 仅支持 quick 或 full')
  if (input.penaltyThreshold !== undefined) strictInteger(input.penaltyThreshold, 40, 100, '定时处罚阈值必须是 40 到 100 的整数')
  if (input.penaltyAction !== undefined && input.penaltyAction !== 'disable' && input.penaltyAction !== 'fallback' && input.penaltyAction !== 'quality_isolate') throw new Error('定时处罚方式无效')
  if (input.recoveryIntervalMinutes !== undefined) strictInteger(input.recoveryIntervalMinutes, 10, 10080, '定时质量隔离恢复周期必须是 10 到 10080 的整数分钟')
  if (input.enabled !== undefined && typeof input.enabled !== 'boolean') throw new Error('定时检查启用状态无效')
}

function schedulePolicy(row: {
  system_account_id: string
  revision: number
  profile: ModelCheckProfile
  penalty_threshold: number
  penalty_action: ModelQualityPenaltyAction
  recovery_interval_minutes: number
}): ModelQualityPolicy {
  return {
    systemAccountId: row.system_account_id,
    revision: Number(row.revision),
    profile: row.profile,
    manualEnforcementEnabled: true,
    penaltyThreshold: Number(row.penalty_threshold),
    penaltyAction: row.penalty_action,
    recoveryIntervalMinutes: Number(row.recovery_interval_minutes)
  }
}

async function scheduledEnforcementConfigurationMatches(client: DatabaseClient, input: ModelQualityEnforcementInput): Promise<boolean> {
  const schedule = await client.one<{
    revision: number
    profile: ModelCheckProfile
    penalty_threshold: number
    penalty_action: ModelQualityPenaltyAction
    recovery_interval_minutes: number
    model: string
  }>(`
    SELECT revision, profile, penalty_threshold, penalty_action, recovery_interval_minutes, model
    FROM ${table(client, 'model_quality_schedules')}
    WHERE id = ? AND system_account_id = ? AND account_id = ?
    LIMIT 1
  `, [input.scheduleId, input.systemAccountId, input.accountId])
  return Boolean(schedule
    && Number(schedule.revision) === input.policyRevision
    && schedule.profile === input.profile
    && Number(schedule.penalty_threshold) === input.penaltyThreshold
    && schedule.penalty_action === input.action
    && Number(schedule.recovery_interval_minutes) === input.recoveryIntervalMinutes
    && schedule.model === input.model)
}

async function manualEnforcementConfigurationMatches(client: DatabaseClient, input: ModelQualityEnforcementInput): Promise<boolean> {
  const policy = await client.one<ModelQualityPolicyRow>(`
    SELECT system_account_id, revision, profile, manual_enforcement_enabled, penalty_threshold,
           penalty_action, recovery_interval_minutes, created_at, updated_at
    FROM ${table(client, 'model_quality_policies')}
    WHERE system_account_id = ?
    LIMIT 1
  `, [input.systemAccountId])
  if (!policy) {
    return input.policyRevision === 0
      && input.profile === 'quick'
      && input.penaltyThreshold === 70
      && input.action === 'fallback'
      && input.recoveryIntervalMinutes === 10
  }
  return policy.revision === input.policyRevision
    && policy.profile === input.profile
    && Number(policy.penalty_threshold) === input.penaltyThreshold
    && policy.penalty_action === input.action
    && Number(policy.recovery_interval_minutes) === input.recoveryIntervalMinutes
}

function modelQualityScheduleSelectColumns(): string {
  return [
    'mqs.id',
    'mqs.system_account_id',
    'mqs.account_id',
    'mqs.model',
    'mqs.interval_minutes',
    'mqs.profile',
    'mqs.penalty_threshold',
    'mqs.penalty_action',
    'mqs.recovery_interval_minutes',
    'mqs.enabled',
    'mqs.revision',
    'mqs.next_run_at',
    'mqs.last_run_id',
    'mqs.last_run_at',
    'mqs.last_run_status',
    'mqs.created_at',
    'mqs.updated_at'
  ].join(', ')
}

async function businessClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function table(client: DatabaseClient, name: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, name)
    : client.dialect.quoteIdentifier(name)
}

function addMinutes(value: string, minutes: number): string {
  const timestamp = rfc3339InstantMilliseconds(value)
  if (timestamp === undefined) throw new Error('模型质量调度时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  return new Date(timestamp + minutes * 60_000).toISOString()
}

function addPassiveMinutes(value: string, minutes: number): string {
  const timestamp = rfc3339InstantMilliseconds(value)
  if (timestamp === undefined) throw new Error('模型质量被动调度时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  const intervalMs = Math.max(1, Math.trunc(Number(minutes) || 1)) * 60_000
  return new Date(timestamp + passiveScheduleDelayMs(intervalMs)).toISOString()
}

function normalizedIso(value?: string): string {
  return value === undefined ? nowIso() : requiredRfc3339Instant(value, '模型质量调度时间')
}

function requiredText(value: unknown, message: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(message)
  return value.trim()
}

function strictInteger(value: unknown, min: number, max: number, message: string): number {
  if (!Number.isInteger(value) || Number(value) < min || Number(value) > max) throw new Error(message)
  return Number(value)
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric)) return fallback
  return Math.min(max, Math.max(min, Math.trunc(numeric)))
}
