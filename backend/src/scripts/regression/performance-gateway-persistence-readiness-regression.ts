import { existsSync, mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import {
  createAuditLogsBatchAsync,
  createOperationLogsBatchAsync,
  createPublicApiLogsBatchAsync,
  createRuntimeLogsBatchAsync,
  createUsageRecordsBatchAsync,
  getAuditLogDetailAsync,
  getAuditLogPayload,
  listAuditErrorGroupEventsAsync,
  listAuditErrorGroupsAsync,
  listAuditLogsAsync,
  listAuditLogsByIdsAsync,
  getOperationLogDetailAsync,
  getOperationLogDetailForViewerAsync,
  getPublicApiLogDetailAsync,
  getRuntimeLogDetailAsync,
  listOperationLogsAsync,
  listOperationLogsForViewerAsync,
  listPublicApiLogsAsync,
  listRuntimeLogsAsync,
  runtimeLogIndexRetentionDays
} from '../../storage/repositories.js'
import { closeStorageDatabases, nowIso } from '../../storage/database.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { buildGatewayQuotaSnapshotAsync } from '../../storage/gateway-quota-snapshot.repository.js'
import { aggregateUsageStatsBatchAsync, refreshUsageQuotaHourlyWindowsCacheAsync } from '../../storage/usage-stats.repository.js'

interface ReadinessCheck {
  name: string
  ok: boolean
  latencyMs: number
  category?: string
  message?: string
}

interface ReadinessReport {
  mode: {
    runtimeMode: string
    databaseDriver: string
    cacheDriver: string
    runtimeStateDriver: string
    queueDriver: string
  }
  runId: string
  startedAt: string
  finishedAt: string
  durationMs: number
  checks: ReadinessCheck[]
  pass: boolean
  missingAdapterChecks: string[]
}

const reportPath = process.env.JUHE_PERFORMANCE_GATEWAY_READINESS_REPORT
  || resolve(backendRoot, '..', 'reports', `performance-gateway-persistence-readiness-${timestampForFile(new Date())}.json`)
const runId = `perf_gateway_readiness_${Date.now()}_${Math.random().toString(16).slice(2)}`
const tracePrefix = `trace-${runId}`
const readinessApiKeyId = `${runId}_api_key`
const readinessRouteStrategyId = `${runId}_route_strategy`
const readinessGroupId = `${runId}_group`
const readinessAccountId = `${runId}_account`
const readinessModel = `${runId}_model`
const readinessErrorCode = `${runId}_error`
const readinessOwnerSystemAccountId = `${runId}_owner`
const readinessAccountAuthorizationId = `${runId}_account_authorization`
const readinessGroupAuthorizationId = `${runId}_group_authorization`
const readinessTeamId = `${runId}_team`
const readinessTeamSourceAccountId = `${runId}_team_source_account`
const readinessTeamInstanceAccountId = `${runId}_team_instance_account`
const readinessTeamAccountAuthorizationId = `${runId}_team_account_authorization`
const readinessTeamGroupId = `${runId}_team_group`
const readinessTeamGroupAuthorizationId = `${runId}_team_group_authorization`
const readinessTeamAccountGrantId = `${runId}_team_account_grant`
const readinessTeamGroupGrantId = `${runId}_team_group_grant`

let exitCode = 0

try {
  assertPerformanceRuntime()
  const report = await runReadiness()
  writeReport(report)
  printReport(report)
  if (!report.pass) {
    exitCode = 1
  }
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  closeStorageDatabases()
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function runReadiness(): Promise<ReadinessReport> {
  const startedAt = new Date()
  const startedAtMs = performance.now()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await cleanupReadinessRows(client).catch(() => undefined)
  await seedReadinessQuotaData(client)

  const checks: ReadinessCheck[] = []
  try {
    checks.push(await runCheck('usage_records_write', async () => {
      const createdAt = nowIso()
      const baseRecord = {
        trafficSource: 'gateway' as const,
        systemAccountId: 'sys_admin',
        apiKeyId: readinessApiKeyId,
        groupId: readinessGroupId,
        accountId: readinessAccountId,
        accountOwnerSystemAccountId: readinessOwnerSystemAccountId,
        groupOwnerSystemAccountId: readinessOwnerSystemAccountId,
        accountAccessType: 'account_authorized' as const,
        groupAccessType: 'authorized' as const,
        accountAuthorizationId: readinessAccountAuthorizationId,
        groupAuthorizationId: readinessGroupAuthorizationId,
        endpoint: '/v1/chat/completions',
        providerCode: 'gpt',
        usageSemantic: 'chat',
        model: readinessModel,
        upstreamModel: readinessModel,
        stream: false,
        createdAt
      }
      await createUsageRecordsBatchAsync([
        {
          ...baseRecord,
          id: `${runId}_usage_success`,
          traceId: `${tracePrefix}-usage-success`,
          statusCode: 200,
          success: true,
          firstTokenMs: 20,
          durationMs: 35,
          inputTokens: 8,
          outputTokens: 12,
          cacheReadTokens: 3,
          cacheReadCostUsd: 0.000001,
          costUsd: 6
        },
        {
          ...baseRecord,
          id: `${runId}_usage_error`,
          traceId: `${tracePrefix}-usage-error`,
          statusCode: 429,
          success: false,
          failureAttribution: 'account_upstream',
          firstTokenMs: 0,
          durationMs: 55,
          inputTokens: 4,
          outputTokens: 0,
          costUsd: 0,
          errorCode: readinessErrorCode,
          errorMessage: 'performance readiness injected upstream error'
        },
        {
          ...baseRecord,
          id: `${runId}_usage_team_account`,
          traceId: `${tracePrefix}-usage-team-account`,
          groupId: undefined,
          groupOwnerSystemAccountId: undefined,
          groupAccessType: undefined,
          groupAuthorizationId: undefined,
          accountId: readinessTeamInstanceAccountId,
          accountAuthorizationId: readinessTeamAccountAuthorizationId,
          accountAuthorizationSourceTeamId: readinessTeamId,
          statusCode: 200,
          success: true,
          firstTokenMs: 18,
          durationMs: 42,
          inputTokens: 5,
          outputTokens: 9,
          costUsd: 6
        },
        {
          ...baseRecord,
          id: `${runId}_usage_team_group`,
          traceId: `${tracePrefix}-usage-team-group`,
          accountId: undefined,
          accountOwnerSystemAccountId: undefined,
          accountAccessType: undefined,
          accountAuthorizationId: undefined,
          groupId: readinessTeamGroupId,
          groupAuthorizationId: readinessTeamGroupAuthorizationId,
          groupAuthorizationSourceTeamId: readinessTeamId,
          statusCode: 200,
          success: true,
          firstTokenMs: 16,
          durationMs: 40,
          inputTokens: 6,
          outputTokens: 10,
          costUsd: 6
        }
      ])
    }))

    checks.push(await runCheck('audit_logs_write', async () => {
      const startedAtIso = nowIso()
      await createAuditLogsBatchAsync([
        {
          id: `${runId}_audit`,
          traceId: `${tracePrefix}-audit`,
          trafficSource: 'gateway',
          systemAccountId: 'sys_admin',
          method: 'POST',
          path: '/v1/chat/completions',
          model: 'gpt-5-mini',
          stream: false,
          auditOutcome: 'success',
          success: true,
          finalStatusCode: 200,
          sampleBucket: 1,
          sampleReason: 'performance_gateway_readiness',
          startedAt: startedAtIso,
          endedAt: startedAtIso,
          durationMs: 35,
          firstTokenMs: 20,
          attempts: [{
            id: `${runId}_audatt`,
            tempId: `${runId}_attempt_0`,
            attemptIndex: 0,
            upstreamMethod: 'POST',
            upstreamUrl: 'http://127.0.0.1:1/v1/chat/completions',
            upstreamStatusCode: 200,
            success: true,
            startedAt: startedAtIso,
            endedAt: startedAtIso,
            durationMs: 35
          }],
          payloads: [
            {
              id: `${runId}_audpay_req`,
              attemptTempId: `${runId}_attempt_0`,
              partType: 'client_request',
              sequenceIndex: 0,
              contentType: 'application/json',
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ runId, direction: 'request' }),
              createdAt: startedAtIso
            },
            {
              id: `${runId}_audpay_res`,
              attemptTempId: `${runId}_attempt_0`,
              partType: 'upstream_response',
              sequenceIndex: 1,
              contentType: 'application/json',
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({ runId, direction: 'response' }),
              createdAt: startedAtIso
            }
          ],
          createdAt: startedAtIso
        },
        {
          id: `${runId}_audit_error`,
          traceId: `${tracePrefix}-audit-error`,
          trafficSource: 'gateway',
          systemAccountId: 'sys_admin',
          method: 'POST',
          path: '/v1/chat/completions',
          model: 'gpt-5-mini',
          stream: false,
          auditOutcome: 'upstream_failed',
          success: false,
          finalStatusCode: 502,
          errorPhase: 'upstream',
          errorCode: 'performance_readiness_error',
          errorMessage: 'performance readiness injected audit error',
          sampleBucket: 1,
          sampleReason: 'performance_gateway_readiness',
          startedAt: startedAtIso,
          endedAt: startedAtIso,
          durationMs: 42,
          attempts: [{
            id: `${runId}_audatt_error`,
            tempId: `${runId}_attempt_error_0`,
            attemptIndex: 0,
            upstreamMethod: 'POST',
            upstreamUrl: 'http://127.0.0.1:1/v1/chat/completions',
            upstreamStatusCode: 502,
            success: false,
            errorPhase: 'upstream',
            errorCode: 'performance_readiness_error',
            errorMessage: 'performance readiness injected audit error',
            startedAt: startedAtIso,
            endedAt: startedAtIso,
            durationMs: 42
          }],
          payloads: [{
            id: `${runId}_audpay_error_req`,
            attemptTempId: `${runId}_attempt_error_0`,
            partType: 'client_request',
            sequenceIndex: 0,
            contentType: 'application/json',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ runId, direction: 'request', failure: true }),
            createdAt: startedAtIso
          }],
          createdAt: startedAtIso
        }
      ])
      const errorGroupRow = await client.one<{ error_group_id?: string; count?: number }>(`
        SELECT al.error_group_id, aeg.count
        FROM juhe_dataset.audit_logs al
        LEFT JOIN juhe_dataset.audit_error_groups aeg ON aeg.id = al.error_group_id
        WHERE al.id = ?
      `, [`${runId}_audit_error`])
      if (!errorGroupRow?.error_group_id || Number(errorGroupRow.count ?? 0) < 1) {
        throw new Error('audit_logs_write 未写入 PG 审计错误分组')
      }
      const auditList = await listAuditLogsAsync({ traceId: `${tracePrefix}-audit-error`, pageSize: 10 })
      if (!auditList.items.some((item) => item.id === `${runId}_audit_error`)) {
        throw new Error('audit_logs_write PG 列表未读到 readiness 失败审计')
      }
      const auditByIds = await listAuditLogsByIdsAsync([`${runId}_audit_error`, `${runId}_audit`])
      if (auditByIds.length !== 2 || auditByIds[0]?.id !== `${runId}_audit_error`) {
        throw new Error('audit_logs_write PG 按 ID 列表读取异常')
      }
      const errorGroups = await listAuditErrorGroupsAsync({ path: '/v1/chat/completions', model: 'gpt-5-mini', statusCode: 502, pageSize: 10 })
      if (!errorGroups.items.some((item) => item.id === errorGroupRow.error_group_id)) {
        throw new Error('audit_logs_write PG 错误分组列表未读到 readiness 错误组')
      }
      const groupEvents = await listAuditErrorGroupEventsAsync(errorGroupRow.error_group_id, { pageSize: 10 })
      if (!groupEvents.items.some((item) => item.id === `${runId}_audit_error`)) {
        throw new Error('audit_logs_write PG 错误分组事件未读到 readiness 失败审计')
      }
      const detail = await getAuditLogDetailAsync(`${runId}_audit_error`)
      if (!detail?.errorGroup || detail.payloads.length !== 1 || detail.attempts.length !== 1) {
        throw new Error('audit_logs_write PG 审计详情未包含错误组、payload 或 attempt')
      }
      const payload = await getAuditLogPayload(`${runId}_audit_error`, `${runId}_audpay_error_req`, { offset: 0, limit: 4096 })
      if (!payload?.bodyText?.includes(runId)) {
        throw new Error('audit_logs_write PG payload 窗口读取异常')
      }
    }))

    checks.push(await runCheck('operation_logs_write', async () => {
      const createdAtIso = nowIso()
      await createOperationLogsBatchAsync([{
        id: `${runId}_oplog`,
        traceId: `${tracePrefix}-operation`,
        actorSystemAccountId: 'sys_admin',
        actorUsername: 'admin',
        actorDisplayName: 'admin',
        actorRole: 'admin',
        operationScopeSystemAccountId: 'sys_admin',
        mode: 'admin',
        module: 'performance_gateway_readiness',
        action: 'probe',
        operationKey: `${runId}.probe`,
        resourceType: 'system',
        resourceId: runId,
        resourceName: 'performance gateway readiness',
        summary: 'performance readiness operation keywordneedle',
        method: 'POST',
        path: '/internal/performance-readiness',
        statusCode: 200,
        changes: [{ field: 'readiness', label: 'readiness', before: 'pending', after: 'ok' }],
        metadata: { runId },
        targets: [{
          targetType: 'system',
          targetId: runId,
          targetName: 'performance gateway readiness',
          targetOwnerSystemAccountId: 'sys_admin',
          relation: 'primary'
        }],
        viewers: [{
          systemAccountId: 'sys_admin',
          visibilityReason: 'actor_self',
          detailLevel: 'full'
        }],
        visibilityScope: 'targeted',
        detailLevel: 'full',
        createdAt: createdAtIso
      }])
      const operationList = await listOperationLogsAsync({ traceId: `${tracePrefix}-operation`, pageSize: 10 })
      if (!operationList.items.some((item) => item.id === `${runId}_oplog`)) {
        throw new Error('operation_logs_write PG 列表未读到 readiness 操作日志')
      }
      const operationSearchList = await listOperationLogsAsync({ summaryKeyword: 'keywordneedle', pageSize: 10 })
      if (!operationSearchList.items.some((item) => item.id === `${runId}_oplog`)) {
        throw new Error('operation_logs_write PG summary 搜索未读到 readiness 操作日志')
      }
      const viewerSearchList = await listOperationLogsForViewerAsync('sys_admin', { summaryKeyword: 'keywordneedle', pageSize: 10 })
      if (!viewerSearchList.items.some((item) => item.id === `${runId}_oplog`)) {
        throw new Error('operation_logs_write PG viewer 列表未读到 readiness 操作日志')
      }
      const detail = await getOperationLogDetailAsync(`${runId}_oplog`)
      if (!detail || detail.targets.length === 0 || detail.viewers.length === 0 || detail.metadata.runId !== runId) {
        throw new Error('operation_logs_write PG 操作日志详情未包含 targets、viewers 或 metadata')
      }
      const viewerDetail = await getOperationLogDetailForViewerAsync(`${runId}_oplog`, 'sys_admin')
      if (!viewerDetail || viewerDetail.targets.length === 0 || viewerDetail.method !== 'POST' || viewerDetail.metadata.runId !== runId) {
        throw new Error('operation_logs_write PG viewer 操作日志详情未保留 full 详情')
      }
    }))

    checks.push(await runCheck('public_api_logs_write', async () => {
      const startedAtIso = nowIso()
      await createPublicApiLogsBatchAsync([{
        id: `${runId}_publog`,
        traceId: `${tracePrefix}-public-api`,
        method: 'GET',
          path: '/__aipublic__/health',
          statusCode: 200,
          success: true,
          requestData: { runId, direction: 'request' },
          responseData: { runId, direction: 'response' },
          startedAt: startedAtIso,
          endedAt: startedAtIso,
          createdAt: startedAtIso
      }])
      const publicApiList = await listPublicApiLogsAsync({ traceId: `${tracePrefix}-public-api`, path: '/__aipublic__/health', result: 'success', pageSize: 10 })
      if (!publicApiList.items.some((item) => item.id === `${runId}_publog`)) {
        throw new Error('public_api_logs_write PG 列表未读到 readiness 公开接口日志')
      }
      const detail = await getPublicApiLogDetailAsync(`${runId}_publog`)
      if (!detail?.requestData || detail.requestData.runId !== runId || detail.responseData.runId !== runId) {
        throw new Error('public_api_logs_write PG 公开接口日志详情 request/response 读取异常')
      }
    }))

    checks.push(await runCheck('runtime_logs_read', async () => {
      const createdAtIso = nowIso()
      await createRuntimeLogsBatchAsync([{
        id: `${runId}_rtlog`,
        logFile: 'performance-readiness.log',
        logOffset: 0,
        lineNumber: 1,
        time: createdAtIso,
        level: 'info',
        traceId: `${tracePrefix}-runtime`,
        event: 'performance_readiness_runtime',
        message: 'runtime readiness keywordneedle',
        rawJson: JSON.stringify({ runId, event: 'performance_readiness_runtime' }),
        createdAt: createdAtIso
      }])
      const runtimeList = await listRuntimeLogsAsync({ traceId: `${tracePrefix}-runtime`, pageSize: 10 })
      if (!runtimeList.items.some((item) => item.id === `${runId}_rtlog`)) {
        throw new Error('runtime_logs_read PG trace 列表未读到 readiness 运行日志')
      }
      const keywordList = await listRuntimeLogsAsync({ keyword: 'keywordneedle', level: 'info', pageSize: 10 })
      if (!keywordList.items.some((item) => item.id === `${runId}_rtlog`)) {
        throw new Error('runtime_logs_read PG keyword 搜索未读到 readiness 运行日志')
      }
      const detail = await getRuntimeLogDetailAsync(`${runId}_rtlog`)
      if (!detail?.rawJson.includes(runId)) {
        throw new Error('runtime_logs_read PG 运行日志详情 raw_json 读取异常')
      }
    }))

    checks.push(await runCheck('usage_stats_aggregate', async () => {
      const safeCreatedBefore = new Date(Date.now() + 60_000).toISOString()
      const processed = await aggregateUsageStatsBatchAsync(10, safeCreatedBefore)
      if (processed < 4) {
        throw new Error(`usage_stats_aggregate 未消费 readiness 用量记录，processed=${processed}`)
      }
      await assertUsageStatsDerivedRows(client)
    }))

    checks.push(await runCheck('gateway_quota_snapshot_pg', async () => {
      await refreshUsageQuotaHourlyWindowsCacheAsync()
      const snapshot = await buildGatewayQuotaSnapshotAsync()
      const costEntry = snapshot.costEntries.find((entry) =>
        entry.systemAccountId === 'sys_admin'
        && entry.scopeType === 'api_key'
        && entry.scopeId === readinessApiKeyId
      )
      if (!costEntry) {
        throw new Error('gateway quota snapshot 缺少 readiness API Key 成本快照')
      }
      assertMinimumCount('gateway_quota_snapshot.costs.hourly', costEntry.costs.hourly, 6)
      assertMinimumCount('gateway_quota_snapshot.costs.daily', costEntry.costs.daily, 6)
      assertMinimumCount('gateway_quota_snapshot.costs.monthly', costEntry.costs.monthly, 6)
      assertMinimumCount('gateway_quota_snapshot.costs.total', costEntry.costs.total, 6)
      const accountAuthorizationEntry = snapshot.authorizationEntries.find((entry) =>
        entry.scopeType === 'account_authorization'
        && entry.authorizationId === readinessAccountAuthorizationId
      )
      if (!accountAuthorizationEntry) {
        throw new Error('gateway quota snapshot 缺少 readiness 账户授权额度快照')
      }
      if (accountAuthorizationEntry.decision.allowed) {
        throw new Error('gateway quota snapshot 未拦截 readiness 账户授权额度')
      }
      const groupAuthorizationEntry = snapshot.authorizationEntries.find((entry) =>
        entry.scopeType === 'group_authorization'
        && entry.authorizationId === readinessGroupAuthorizationId
      )
      if (!groupAuthorizationEntry) {
        throw new Error('gateway quota snapshot 缺少 readiness 分组授权额度快照')
      }
      if (groupAuthorizationEntry.decision.allowed) {
        throw new Error('gateway quota snapshot 未拦截 readiness 分组授权额度')
      }
      const teamAccountAuthorizationEntry = snapshot.authorizationEntries.find((entry) =>
        entry.scopeType === 'account_authorization'
        && entry.authorizationId === readinessTeamAccountAuthorizationId
      )
      if (!teamAccountAuthorizationEntry) {
        throw new Error('gateway quota snapshot 缺少 readiness 团队账户授权额度快照')
      }
      if (teamAccountAuthorizationEntry.decision.allowed) {
        throw new Error('gateway quota snapshot 未拦截 readiness 团队账户授权额度')
      }
      const teamGroupAuthorizationEntry = snapshot.authorizationEntries.find((entry) =>
        entry.scopeType === 'group_authorization'
        && entry.authorizationId === readinessTeamGroupAuthorizationId
      )
      if (!teamGroupAuthorizationEntry) {
        throw new Error('gateway quota snapshot 缺少 readiness 团队分组授权额度快照')
      }
      if (teamGroupAuthorizationEntry.decision.allowed) {
        throw new Error('gateway quota snapshot 未拦截 readiness 团队分组授权额度')
      }
    }))
  } finally {
    await cleanupReadinessRows(client).catch(() => undefined)
  }

  const finishedAt = new Date()
  const missingAdapterChecks = checks
    .filter((check) => check.category === 'postgres_adapter_missing')
    .map((check) => check.name)
  return {
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    runId,
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: performance.now() - startedAtMs,
    checks,
    pass: checks.every((check) => check.ok),
    missingAdapterChecks
  }
}

async function runCheck(name: string, operation: () => void | Promise<void>): Promise<ReadinessCheck> {
  const startedAt = performance.now()
  try {
    await operation()
    return {
      name,
      ok: true,
      latencyMs: performance.now() - startedAt
    }
  } catch (error) {
    return {
      name,
      ok: false,
      latencyMs: performance.now() - startedAt,
      category: classifyReadinessError(error),
      message: error instanceof Error ? error.message : String(error)
    }
  }
}

function classifyReadinessError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error)
  if (
    message.includes('尚未接入 PostgreSQL')
    || message.includes('JUHE_AI_DATABASE_DRIVER=postgres 不能回退写入 SQLite')
    || message.includes('PostgreSQL 模式') && message.includes('暂不支持')
  ) {
    return 'postgres_adapter_missing'
  }
  if (message.includes('relation') && message.includes('does not exist')) {
    return 'postgres_schema_missing'
  }
  return 'unexpected_error'
}

async function seedReadinessQuotaData(client: DatabaseClient): Promise<void> {
  const now = nowIso()
  const quotaLimitsJson = JSON.stringify({
    hourly: { enabled: true, hours: 3, limit: 5 },
    daily: { enabled: true, limit: 5 },
    monthly: { enabled: true, limit: 5 },
    total: { enabled: true, limit: 5 }
  })
  await client.execute(`
    INSERT INTO juhe_business.route_strategies (
      id, system_account_id, name, description, mode, status, config_json, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, 'performance readiness quota snapshot route strategy probe',
      'normal', 'active', NULL, ?, ?)
  `, [
    readinessRouteStrategyId,
    `performance readiness route ${runId}`,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.api_keys (
      id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix,
      key_suffix, key_secret_encrypted, status, quota_limits_json, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, 'performance readiness quota snapshot probe', ?,
      'sk-readiness', 'probe', ?, 'active', ?, ?, ?)
  `, [
    readinessApiKeyId,
    readinessRouteStrategyId,
    `performance readiness ${runId}`,
    `hash_${runId}`,
    `encrypted_${runId}`,
    quotaLimitsJson,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.system_teams (
      id, name, description, status, created_by, created_at, updated_at
    )
    VALUES (?, ?, 'performance readiness team authorization quota snapshot probe', 'active', ?, ?, ?)
  `, [
    readinessTeamId,
    `performance readiness ${runId}`,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.resource_authorizations (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
      remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
    )
    VALUES (?, 'account', ?, ?, 'sys_admin',
      'use', 'active', 'manual', NULL, ?, ?,
      'performance readiness account authorization quota snapshot probe', NULL, ?, ?, ?, NULL, NULL, NULL, ?)
  `, [
    readinessAccountAuthorizationId,
    readinessAccountId,
    readinessOwnerSystemAccountId,
    now,
    now,
    quotaLimitsJson,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.resource_authorizations (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
      remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
    )
    VALUES (?, 'group', ?, ?, 'sys_admin',
      'use', 'active', 'manual', NULL, ?, ?,
      'performance readiness group authorization quota snapshot probe', NULL, ?, ?, ?, NULL, NULL, NULL, ?)
  `, [
    readinessGroupAuthorizationId,
    readinessGroupId,
    readinessOwnerSystemAccountId,
    now,
    now,
    quotaLimitsJson,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, concurrency_limit, schedulable,
      health_check_model, health_check_endpoint_mode,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, 'api_key', 'active', '{}', 'sk-***readiness-source', 20, 1,
        'gpt-5.4-mini', 'responses_sse', ?, ?)
  `, [
    readinessTeamSourceAccountId,
    readinessOwnerSystemAccountId,
    GPT_VENDOR_CODE,
    GPT_OPENAI_V1_PROFILE_ID,
    OPENAI_PROTOCOL_CODE,
    OPENAI_PROTOCOL_VERSION,
    `performance readiness source ${runId}`,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.resource_authorizations (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
      remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
    )
    VALUES (?, 'account', ?, ?, 'sys_admin',
      'use', 'active', 'team', ?, ?, ?,
      'performance readiness team account authorization quota snapshot probe', NULL, NULL, ?, ?, NULL, NULL, NULL, ?)
  `, [
    readinessTeamAccountAuthorizationId,
    readinessTeamSourceAccountId,
    readinessOwnerSystemAccountId,
    readinessTeamId,
    now,
    now,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.resource_authorizations (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
      remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
    )
    VALUES (?, 'group', ?, ?, 'sys_admin',
      'use', 'active', 'team', ?, ?, ?,
      'performance readiness team group authorization quota snapshot probe', NULL, NULL, ?, ?, NULL, NULL, NULL, ?)
  `, [
    readinessTeamGroupAuthorizationId,
    readinessTeamGroupId,
    readinessOwnerSystemAccountId,
    readinessTeamId,
    now,
    now,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.resource_authorization_grants (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
      grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
      limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
    )
    VALUES (?, 'account', ?, ?, 'team', NULL, ?, 'use', 'active',
      'performance readiness team account grant quota snapshot probe', NULL, ?, ?, ?, NULL, NULL, ?)
  `, [
    readinessTeamAccountGrantId,
    readinessTeamSourceAccountId,
    readinessOwnerSystemAccountId,
    readinessTeamId,
    quotaLimitsJson,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.resource_authorization_grants (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
      grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
      limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
    )
    VALUES (?, 'group', ?, ?, 'team', NULL, ?, 'use', 'active',
      'performance readiness team group grant quota snapshot probe', NULL, ?, ?, ?, NULL, NULL, ?)
  `, [
    readinessTeamGroupGrantId,
    readinessTeamGroupId,
    readinessOwnerSystemAccountId,
    readinessTeamId,
    quotaLimitsJson,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
  await client.execute(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, concurrency_limit, schedulable,
      authorization_instance_source_account_id, authorization_instance_authorization_id,
      authorization_instance_owner_system_account_id, health_check_model, health_check_endpoint_mode, created_at, updated_at
    )
    VALUES (?, 'sys_admin', ?, ?, ?, ?, ?, 'api_key', 'active', '{}', 'sk-***readiness-instance', 20, 1,
        ?, ?, ?, 'gpt-5.4-mini', 'responses_sse', ?, ?)
  `, [
    readinessTeamInstanceAccountId,
    GPT_VENDOR_CODE,
    GPT_OPENAI_V1_PROFILE_ID,
    OPENAI_PROTOCOL_CODE,
    OPENAI_PROTOCOL_VERSION,
    `performance readiness instance ${runId}`,
    readinessTeamSourceAccountId,
    readinessTeamAccountAuthorizationId,
    readinessOwnerSystemAccountId,
    now,
    now
  ])
}

async function assertUsageStatsDerivedRows(client: DatabaseClient): Promise<void> {
  const modelRow = await client.one<{ request_count?: number | string | null }>(`
    SELECT COALESCE(SUM(request_count), 0)::int AS request_count
    FROM juhe_stats.usage_model_daily
    WHERE system_account_id = 'sys_admin'
      AND provider_code = 'gpt'
      AND model = ?
  `, [readinessModel])
  assertMinimumCount('usage_model_daily.request_count', modelRow?.request_count, 2)

  const errorRow = await client.one<{ error_count?: number | string | null }>(`
    SELECT COALESCE(SUM(error_count), 0)::int AS error_count
    FROM juhe_stats.usage_error_daily
    WHERE system_account_id = 'sys_admin'
      AND provider_code = 'gpt'
      AND error_code = ?
      AND status_code = 429
  `, [readinessErrorCode])
  assertMinimumCount('usage_error_daily.error_count', errorRow?.error_count, 1)

  const latencyRow = await client.one<{ sample_count?: number | string | null }>(`
    SELECT COALESCE(SUM(sample_count), 0)::int AS sample_count
    FROM juhe_stats.usage_latency_daily
    WHERE system_account_id = 'sys_admin'
      AND scope_type = 'api_key'
      AND scope_id = ?
      AND metric_type = 'duration_ms'
  `, [readinessApiKeyId])
  assertMinimumCount('usage_latency_daily.sample_count', latencyRow?.sample_count, 2)

  const accountQualityRow = await client.one<{
    request_count?: number | string | null
    success_count?: number | string | null
    error_count?: number | string | null
  }>(`
    SELECT
      COALESCE(SUM(request_count), 0)::int AS request_count,
      COALESCE(SUM(success_count), 0)::int AS success_count,
      COALESCE(SUM(error_count), 0)::int AS error_count
    FROM juhe_stats.account_quality_minute_stats
    WHERE account_id = ?
  `, [readinessAccountId])
  assertMinimumCount('account_quality_minute_stats.request_count', accountQualityRow?.request_count, 2)
  assertMinimumCount('account_quality_minute_stats.success_count', accountQualityRow?.success_count, 1)
  assertMinimumCount('account_quality_minute_stats.error_count', accountQualityRow?.error_count, 1)
}

function assertMinimumCount(name: string, value: number | string | null | undefined, minimum: number): void {
  const count = Number(value ?? 0)
  if (!Number.isFinite(count) || count < minimum) {
    throw new Error(`${name} 低于预期，actual=${value ?? 0}, expected>=${minimum}`)
  }
}

async function cleanupReadinessRows(client: DatabaseClient): Promise<void> {
  const lower = tracePrefix
  const upper = `${tracePrefix}\uffff`
  const statsScopeIds = [
    'sys_admin',
    'global',
    'gpt',
    '/v1/chat/completions',
    readinessApiKeyId,
    readinessGroupId,
    readinessAccountId,
    readinessModel,
    readinessAccountAuthorizationId,
    readinessGroupAuthorizationId,
    readinessTeamInstanceAccountId,
    readinessTeamSourceAccountId,
    readinessTeamAccountAuthorizationId,
    readinessTeamGroupId,
    readinessTeamGroupAuthorizationId,
    `${readinessTeamInstanceAccountId}:${readinessTeamId}`,
    `${readinessTeamGroupId}:${readinessTeamId}`
  ]
  const auditBlobStorageKeys = (await client.query<{ storage_key?: string }>(`
    SELECT DISTINCT b.storage_key
    FROM juhe_dataset.audit_payload_blobs b
    WHERE b.id IN (
      SELECT headers_blob_id
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
      UNION
      SELECT body_blob_id
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
    )
  `, [lower, upper, lower, upper]))
    .map((row) => row.storage_key)
    .filter((value): value is string => Boolean(value))

  await client.execute(
    'DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)',
    [lower, upper]
  )
  await client.execute(
    'DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)',
    [lower, upper]
  )
  await client.execute(`
    DELETE FROM juhe_dataset.audit_error_groups
    WHERE first_event_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
       OR last_event_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
       OR sample_event_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
  `, [lower, upper, lower, upper, lower, upper])
  await client.execute('DELETE FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  if (auditBlobStorageKeys.length > 0) {
    await client.execute(
      `DELETE FROM juhe_dataset.audit_payload_blobs WHERE storage_key IN (${auditBlobStorageKeys.map(() => '?').join(', ')})`,
      auditBlobStorageKeys
    )
    deleteAuditBlobFiles(auditBlobStorageKeys)
  }
  await client.execute('DELETE FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = ?', [`${runId}_oplog`])
  await client.execute('DELETE FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = ?', [`${runId}_oplog`])
  await client.execute('DELETE FROM juhe_dataset.operation_log_targets WHERE operation_log_id = ?', [`${runId}_oplog`])
  await client.execute('DELETE FROM juhe_dataset.operation_logs WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_dataset.public_api_logs WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  const deletedRuntimeLogRows = await client.query<{ time: string; level: string; event: string | null }>(
    'DELETE FROM juhe_dataset.runtime_logs WHERE trace_id >= ? AND trace_id < ? RETURNING time, level, event',
    [lower, upper]
  )
  await decrementRuntimeLogFacetsForCleanup(client, deletedRuntimeLogRows)
  await client.execute('DELETE FROM juhe_business.accounts WHERE id = ?', [readinessTeamInstanceAccountId])
  await client.execute('DELETE FROM juhe_business.resource_authorization_grants WHERE id = ANY(?::text[])', [[readinessTeamAccountGrantId, readinessTeamGroupGrantId]])
  await client.execute('DELETE FROM juhe_business.resource_authorizations WHERE id = ANY(?::text[])', [[
    readinessAccountAuthorizationId,
    readinessGroupAuthorizationId,
    readinessTeamAccountAuthorizationId,
    readinessTeamGroupAuthorizationId
  ]])
  await client.execute('DELETE FROM juhe_business.accounts WHERE id = ?', [readinessTeamSourceAccountId])
  await client.execute('DELETE FROM juhe_business.system_teams WHERE id = ?', [readinessTeamId])
  await client.execute('DELETE FROM juhe_business.api_keys WHERE id = ?', [readinessApiKeyId])
  await client.execute('DELETE FROM juhe_business.route_strategies WHERE id = ?', [readinessRouteStrategyId])
  await client.execute('DELETE FROM juhe_usage.usage_record_shard_entries WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_usage.usage_records WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_stats.account_quality_minute_stats WHERE account_id = ?', [readinessAccountId])
  await client.execute('DELETE FROM juhe_stats.account_quality_minute_stats WHERE account_id = ?', [readinessTeamInstanceAccountId])
  await client.execute('DELETE FROM juhe_stats.account_quality_dirty_accounts WHERE account_id = ?', [readinessAccountId])
  await client.execute('DELETE FROM juhe_stats.account_quality_dirty_accounts WHERE account_id = ?', [readinessTeamInstanceAccountId])
  for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_id = ANY(?::text[])`, [statsScopeIds])
  }
  for (const tableName of ['usage_model_minute', 'usage_model_hourly', 'usage_model_daily', 'usage_model_weekly', 'usage_model_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE model = ?`, [readinessModel])
  }
  for (const tableName of ['usage_error_minute', 'usage_error_hourly', 'usage_error_daily', 'usage_error_weekly', 'usage_error_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE error_code = ?`, [readinessErrorCode])
  }
  for (const tableName of ['usage_latency_minute', 'usage_latency_hourly', 'usage_latency_daily', 'usage_latency_weekly', 'usage_latency_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_id = ANY(?::text[])`, [statsScopeIds])
  }
  await client.execute('DELETE FROM juhe_stats.usage_quota_hourly_windows WHERE scope_id = ANY(?::text[])', [statsScopeIds])
}

async function decrementRuntimeLogFacetsForCleanup(
  client: DatabaseClient,
  rows: Array<{ time: string; level: string; event: string | null }>
): Promise<void> {
  const cutoffIso = new Date(Date.now() - runtimeLogIndexRetentionDays * 24 * 60 * 60 * 1000).toISOString()
  const retainedRows = rows.filter((row) => row.time >= cutoffIso)
  if (retainedRows.length === 0) return

  const bucketKey = 'current'
  const updatedAt = nowIso()
  await client.execute(`
    UPDATE juhe_dataset.runtime_log_facet_summary
    SET total_count = GREATEST(0, total_count - ?),
        earliest_time = (SELECT MIN(time) FROM juhe_dataset.runtime_logs WHERE time >= ?),
        latest_time = (SELECT MAX(time) FROM juhe_dataset.runtime_logs WHERE time >= ?),
        updated_at = ?
    WHERE bucket_key = ?
  `, [retainedRows.length, cutoffIso, cutoffIso, updatedAt, bucketKey])
  await client.execute('DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = ? AND total_count <= 0', [bucketKey])

  const levels = new Map<string, number>()
  const events = new Map<string, number>()
  for (const row of retainedRows) {
    levels.set(row.level, (levels.get(row.level) ?? 0) + 1)
    const event = row.event?.trim()
    if (event) {
      events.set(event, (events.get(event) ?? 0) + 1)
    }
  }

  for (const [level, count] of levels) {
    await client.execute(`
      UPDATE juhe_dataset.runtime_log_level_facets
      SET count = GREATEST(0, count - ?),
          updated_at = ?
      WHERE bucket_key = ? AND level = ?
    `, [count, updatedAt, bucketKey, level])
  }
  await client.execute('DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = ? AND count <= 0', [bucketKey])

  for (const [event, count] of events) {
    await client.execute(`
      UPDATE juhe_dataset.runtime_log_event_facets
      SET count = GREATEST(0, count - ?),
          latest_time = (SELECT MAX(time) FROM juhe_dataset.runtime_logs WHERE event = ? AND time >= ?),
          updated_at = ?
      WHERE bucket_key = ? AND event = ?
    `, [count, event, cutoffIso, updatedAt, bucketKey, event])
  }
  await client.execute('DELETE FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = ? AND count <= 0', [bucketKey])
}

function deleteAuditBlobFiles(storageKeys: string[]): void {
  const auditBlobRoot = resolve(backendRoot, 'data', 'audit', 'blobs')
  for (const storageKey of storageKeys) {
    const target = resolve(auditBlobRoot, storageKey)
    const relativePath = relative(auditBlobRoot, target)
    if (relativePath.startsWith('..') || isAbsolute(relativePath)) continue
    if (existsSync(target)) {
      unlinkSync(target)
    }
  }
}

function assertPerformanceRuntime(): void {
  if (runtimeConfig.runtimeMode !== 'performance' || runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('performance gateway persistence readiness 必须在 JUHE_AI_RUNTIME_MODE=performance / JUHE_AI_DATABASE_DRIVER=postgres 下运行')
  }
  if (
    runtimeConfig.cacheDriver !== 'redis'
    || runtimeConfig.runtimeStateDriver !== 'redis'
    || runtimeConfig.queueDriver !== 'redis_stream'
    || !runtimeConfig.postgres.url
    || !runtimeConfig.redis.cacheUrl
    || !runtimeConfig.redis.stateUrl
    || !runtimeConfig.redis.queueUrl
  ) {
    throw new Error('performance gateway persistence readiness 必须完整配置 PostgreSQL + Redis cache/state/queue')
  }
}

function writeReport(report: ReadinessReport): void {
  mkdirSync(dirname(reportPath), { recursive: true })
  writeFileSync(reportPath, JSON.stringify(report, null, 2), 'utf8')
}

function printReport(report: ReadinessReport): void {
  console.log('高性能网关事实表 readiness 结果')
  for (const check of report.checks) {
    console.log(`- ${check.name}: ${check.ok ? 'ok' : `failed (${check.category})`} ${check.latencyMs.toFixed(2)}ms`)
  }
  if (report.missingAdapterChecks.length > 0) {
    console.log(`缺失 PG adapter: ${report.missingAdapterChecks.join(', ')}`)
  }
  console.log(`报告已写入：${reportPath}`)
}

function timestampForFile(value: Date): string {
  return value.toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
}
