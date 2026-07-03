import { createHash } from 'node:crypto'

import type { AccountSummary } from '../../../../domain/types.js'
import {
  cancelAccountTestSession,
  completeAccountTestTask,
  createAccountTestSession,
  createAccountTestTask,
  failAccountTestTask,
  markAccountTestTaskRunning
} from '../../../../storage/account-test-tasks.repository.js'
import { hashSecret } from '../../../../storage/crypto.js'
import {
  codexContextStateShardIndexes,
  getBusinessDatabase,
  getCodexContextStateShardDatabase,
  getDatasetDatabase,
  getStatsDatabase,
  nowIso
} from '../../../../storage/database.js'
import { createOpenAICompatibleFile } from '../../../../storage/openai-compatible-files.repository.js'
import {
  createOpenAICompatibleVectorStore,
  createOpenAICompatibleVectorStoreFile
} from '../../../../storage/openai-compatible-vector-stores.repository.js'
import {
  dayMs,
  idPrefix,
  minuteMs,
  namePrefix,
  providerCode,
  tracePrefix,
  type CreatedMockdata,
  type UsageRecordSeed
} from '../shared.js'

type BusinessDatabase = ReturnType<typeof getBusinessDatabase>
type DatasetDatabase = ReturnType<typeof getDatasetDatabase>
type StatsDatabase = ReturnType<typeof getStatsDatabase>

export function createBusinessTableCoverageMockdata(created: CreatedMockdata): void {
  createSystemSessionCoverage(created)
  createProviderDefaultTestModelCoverage(created)
  createAccountTestCoverage(created)
  createGroupAuthorizationSettingsCoverage(created)
  createOpenAICompatibleStorageCoverage(created)
  createAvailabilityScheduleEventCoverage(created)
  createCodexContextStateCoverage(created)
}

export function createDatasetTableCoverageMockdata(): void {
  const database = getDatasetDatabase()
  const now = nowIso()
  const latestRuntimeLog = database.prepare(`
    SELECT log_file, log_offset, line_number, time
    FROM runtime_logs
    WHERE id LIKE ?
    ORDER BY time DESC, id DESC
    LIMIT 1
  `).get(`${idPrefix}%`) as { log_file?: string | null; log_offset?: number | null; line_number?: number | null; time?: string | null } | undefined
  const lineNumber = Number(latestRuntimeLog?.line_number ?? 240)
  const offset = Number(latestRuntimeLog?.log_offset ?? 48_000)
  database.prepare(`
    INSERT INTO runtime_log_file_cursors (
      log_file, file_identity, cursor_offset, line_number, file_size, file_mtime_ms,
      last_read_at, last_error_message, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
    ON CONFLICT(log_file) DO UPDATE SET
      file_identity = excluded.file_identity,
      cursor_offset = excluded.cursor_offset,
      line_number = excluded.line_number,
      file_size = excluded.file_size,
      file_mtime_ms = excluded.file_mtime_ms,
      last_read_at = excluded.last_read_at,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
  `).run(
    `${idPrefix}runtime.log`,
    `${tracePrefix}runtime-log-file`,
    Math.max(1, offset + 512),
    Math.max(1, lineNumber),
    Math.max(1, offset + 512),
    Date.parse(latestRuntimeLog?.time ?? now),
    now,
    now,
    now
  )
}

export function createStatsTableCoverageMockdata(created: CreatedMockdata, usageRecords: UsageRecordSeed[]): void {
  const database = getStatsDatabase()
  const now = nowIso()
  createBackgroundJobCoverage(database, now)
  createStatsDirtyQueueCoverage(database, created, now)
  createUsageRecordCleanupDeductionCoverage(database, created, usageRecords, now)
}

function createSystemSessionCoverage(created: CreatedMockdata): void {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    INSERT INTO system_sessions (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
    VALUES (?, ?, ?, ?, ?, ?)
  `).run(
    `${idPrefix}system_session_manager`,
    created.users.manager.id,
    hashSecret(`${idPrefix}manager-session-token`),
    new Date(Date.now() + dayMs).toISOString(),
    now,
    now
  )
}

function createProviderDefaultTestModelCoverage(created: CreatedMockdata): void {
  const now = nowIso()
  const database = getBusinessDatabase()
  const rows = [
    [created.users.manager.id, 'gpt', 'mockdata-personal-codex'],
    [created.users.tester.id, 'gpt', 'gpt-5.4-mini']
  ] as const
  const statement = database.prepare(`
    INSERT INTO provider_default_test_models (system_account_id, provider_code, model, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, provider_code) DO UPDATE SET
      model = excluded.model,
      updated_at = excluded.updated_at
  `)
  for (const [systemAccountId, providerCode, model] of rows) {
    statement.run(systemAccountId, providerCode, model, now, now)
  }
}

function createAccountTestCoverage(created: CreatedMockdata): void {
  const access = { systemAccountId: created.users.admin.id, role: created.users.admin.role, systemAccountFilterId: created.users.admin.id }
  const completedSession = createAccountTestSession(access)
  const successTask = createAccountTestTask({
    account: created.accounts.primary,
    access,
    sessionId: completedSession.id,
    diagnostics: 'full',
    model: 'gpt-5.4-mini'
  })
  markAccountTestTaskRunning(successTask.id)
  completeAccountTestTask(successTask.id, accountTestResult(created.accounts.primary, true, 'Mockdata 账户测试通过'))
  markAccountTestSessionCompleted(completedSession.id)

  const failedTask = createAccountTestTask({
    account: created.accounts.error,
    access,
    diagnostics: 'limited',
    model: 'gpt-5.4-mini'
  })
  failAccountTestTask(
    failedTask.id,
    'Mockdata 模拟上游账号测试失败',
    accountTestResult(created.accounts.error, false, 'Mockdata 模拟上游账号测试失败', 503, 'upstream_unavailable')
  )

  const canceledSession = createAccountTestSession(access)
  createAccountTestTask({
    account: created.accounts.pendingTest,
    access,
    sessionId: canceledSession.id,
    diagnostics: 'full',
    model: 'gpt-5.4-mini'
  })
  cancelAccountTestSession(canceledSession.id, access, 'Mockdata 模拟前端关闭测试窗口')
}

function createGroupAuthorizationSettingsCoverage(created: CreatedMockdata): void {
  const authorization = getBusinessDatabase().prepare(`
    SELECT id
    FROM resource_authorizations
    WHERE resource_type = 'group'
      AND resource_id = ?
      AND grantee_system_account_id = ?
      AND status = 'active'
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `).get(created.groups.main.id, created.users.dev.id) as { id?: string } | undefined
  if (!authorization?.id) return
  const now = nowIso()
  getBusinessDatabase().prepare(`
    INSERT INTO group_authorization_settings (
      authorization_id, system_account_id, group_id, enabled, group_type,
      scheduling_policy_json, created_at, updated_at
    ) VALUES (?, ?, ?, 1, 'high_concurrency', ?, ?, ?)
    ON CONFLICT(authorization_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      group_id = excluded.group_id,
      enabled = excluded.enabled,
      group_type = excluded.group_type,
      scheduling_policy_json = excluded.scheduling_policy_json,
      updated_at = excluded.updated_at
  `).run(
    authorization.id,
    created.users.dev.id,
    created.groups.main.id,
    JSON.stringify({
      defaultSoftConcurrency: 4,
      maxQueueWaitMs: 20_000,
      clientIpConcurrencyLimit: 3,
      clientIpConcurrencyOverflowMode: 'queue'
    }),
    now,
    now
  )
}

function createOpenAICompatibleStorageCoverage(created: CreatedMockdata): void {
  const fileId = `${idPrefix}openai_file_policy`
  const vectorStoreId = `${idPrefix}vector_store_knowledge`
  const systemAccountId = created.users.admin.id
  const apiKeyId = created.apiKeys.adminMain.id
  createOpenAICompatibleFile({
    id: fileId,
    systemAccountId,
    apiKeyId,
    purpose: 'assistants',
    containerId: vectorStoreId,
    filename: `${namePrefix}知识库片段.txt`,
    bytes: 1920,
    mediaType: 'text/plain',
    storageKey: `${idPrefix}openai-compatible/files/policy.txt`,
    sha256: sha256(`${namePrefix}知识库片段`),
    expiresAt: new Date(Date.now() + 30 * dayMs).toISOString()
  })
  createOpenAICompatibleVectorStore({
    id: vectorStoreId,
    systemAccountId,
    apiKeyId,
    name: `${namePrefix}OpenAI 兼容知识库`,
    description: 'Mockdata OpenAI-compatible Vector Store，用于 Files / Vector Stores API 数据展示',
    metadata: {
      source: 'mockdata',
      owner: created.users.admin.username
    },
    expiresAfterAnchor: 'last_active_at',
    expiresAfterDays: 30
  })
  createOpenAICompatibleVectorStoreFile({
    vectorStoreId,
    fileId,
    systemAccountId,
    apiKeyId,
    attributes: {
      topic: 'mockdata',
      provider: 'openai-compatible'
    },
    chunkingStrategy: {
      type: 'static',
      static: { max_chunk_size_tokens: 800, chunk_overlap_tokens: 120 }
    },
    status: 'completed',
    chunks: [
      {
        contentText: 'Mockdata OpenAI-compatible 文件用于验证本地 Files API 元数据、向量库绑定和分块检索。',
        contentPreview: 'Mockdata OpenAI-compatible 文件用于验证本地 Files API...',
        tokenEstimate: 38,
        keywordIndexText: 'mockdata openai-compatible files vector stores'
      },
      {
        contentText: '该向量库绑定到造数主力网关 Key，和 API Key、系统账号、文件元数据形成完整业务关联。',
        contentPreview: '该向量库绑定到造数主力网关 Key...',
        tokenEstimate: 42,
        keywordIndexText: 'api key system account vector store binding'
      }
    ]
  })
}

function createAvailabilityScheduleEventCoverage(created: CreatedMockdata): void {
  const now = nowIso()
  const database = getBusinessDatabase()
  database.prepare(`
    INSERT INTO account_schedule_status_events (event_key, account_id, status, executed_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(event_key) DO UPDATE SET
      account_id = excluded.account_id,
      status = excluded.status,
      executed_at = excluded.executed_at
  `).run(
    `${idPrefix}account_schedule_${created.accounts.scheduledInactive.id}`,
    created.accounts.scheduledInactive.id,
    created.accounts.scheduledInactive.status,
    now
  )
  database.prepare(`
    INSERT INTO api_key_schedule_status_events (event_key, api_key_id, status, executed_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(event_key) DO UPDATE SET
      api_key_id = excluded.api_key_id,
      status = excluded.status,
      executed_at = excluded.executed_at
  `).run(
    `${idPrefix}api_key_schedule_${created.apiKeys.adminScheduled.id}`,
    created.apiKeys.adminScheduled.id,
    created.apiKeys.adminScheduled.status,
    now
  )
}

function createCodexContextStateCoverage(created: CreatedMockdata): void {
  const now = nowIso()
  const expiresAt = new Date(Date.now() + 7 * dayMs).toISOString()
  for (const shardIndex of codexContextStateShardIndexes()) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    const shardKey = String(shardIndex).padStart(3, '0')
    const sessionId = `${idPrefix}codex_context_session_${shardKey}`
    const responseId = `${idPrefix}codex_context_response_${shardKey}`
    const compactId = `${idPrefix}codex_context_compact_${shardKey}`
    const responseStorageKey = `${idPrefix}codex-context/state-${shardKey}/response.json.gz`
    const compactStorageKey = `${idPrefix}codex-context/state-${shardKey}/compact.json.gz`
    const rawResponseBytes = 6400 + shardIndex * 137
    const compressedResponseBytes = 1900 + shardIndex * 41
    const rawCompactBytes = 1800 + shardIndex * 53
    const compressedCompactBytes = 720 + shardIndex * 19
    database.exec('BEGIN')
    try {
      database.prepare(`
        INSERT INTO codex_context_sessions (
          id, system_account_id, api_key_id, group_id, provider_code,
          source_response_id, latest_response_id, latest_compact_id,
          created_at, updated_at, last_used_at, expires_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
          system_account_id = excluded.system_account_id,
          api_key_id = excluded.api_key_id,
          group_id = excluded.group_id,
          provider_code = excluded.provider_code,
          source_response_id = excluded.source_response_id,
          latest_response_id = excluded.latest_response_id,
          latest_compact_id = excluded.latest_compact_id,
          updated_at = excluded.updated_at,
          last_used_at = excluded.last_used_at,
          expires_at = excluded.expires_at
      `).run(
        sessionId,
        created.users.admin.id,
        created.apiKeys.adminMain.id,
        created.groups.main.id,
        providerCode,
        responseId,
        responseId,
        compactId,
        now,
        now,
        now,
        expiresAt
      )
      database.prepare(`
        INSERT INTO codex_context_responses (
          response_id, session_id, previous_response_id, system_account_id, api_key_id,
          group_id, provider_code, upstream_account_id, model, upstream_model,
          storage_key, storage_offset_bytes, sha256, raw_size_bytes, compressed_size_bytes,
          compression, schema_version, created_at, updated_at, last_used_at, expires_at
        ) VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'gzip', 1, ?, ?, ?, ?)
        ON CONFLICT(response_id) DO UPDATE SET
          session_id = excluded.session_id,
          previous_response_id = excluded.previous_response_id,
          system_account_id = excluded.system_account_id,
          api_key_id = excluded.api_key_id,
          group_id = excluded.group_id,
          provider_code = excluded.provider_code,
          upstream_account_id = excluded.upstream_account_id,
          model = excluded.model,
          upstream_model = excluded.upstream_model,
          storage_key = excluded.storage_key,
          storage_offset_bytes = excluded.storage_offset_bytes,
          sha256 = excluded.sha256,
          raw_size_bytes = excluded.raw_size_bytes,
          compressed_size_bytes = excluded.compressed_size_bytes,
          compression = excluded.compression,
          schema_version = excluded.schema_version,
          updated_at = excluded.updated_at,
          last_used_at = excluded.last_used_at,
          expires_at = excluded.expires_at
      `).run(
        responseId,
        sessionId,
        created.users.admin.id,
        created.apiKeys.adminMain.id,
        created.groups.main.id,
        providerCode,
        created.accounts.primary.id,
        'gpt-5.4-mini',
        'gpt-5.4-mini',
        responseStorageKey,
        0,
        sha256(`${responseId}:${responseStorageKey}`),
        rawResponseBytes,
        compressedResponseBytes,
        now,
        now,
        now,
        expiresAt
      )
      database.prepare(`
        INSERT INTO codex_context_compacts (
          compact_id, session_id, source_response_id, summary_digest, system_account_id, api_key_id,
          group_id, provider_code, upstream_account_id, model, upstream_model,
          storage_key, storage_offset_bytes, sha256, raw_size_bytes, compressed_size_bytes,
          compression, schema_version, created_at, updated_at, last_used_at, expires_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'gzip', 1, ?, ?, ?, ?)
        ON CONFLICT(compact_id) DO UPDATE SET
          session_id = excluded.session_id,
          source_response_id = excluded.source_response_id,
          summary_digest = excluded.summary_digest,
          system_account_id = excluded.system_account_id,
          api_key_id = excluded.api_key_id,
          group_id = excluded.group_id,
          provider_code = excluded.provider_code,
          upstream_account_id = excluded.upstream_account_id,
          model = excluded.model,
          upstream_model = excluded.upstream_model,
          storage_key = excluded.storage_key,
          storage_offset_bytes = excluded.storage_offset_bytes,
          sha256 = excluded.sha256,
          raw_size_bytes = excluded.raw_size_bytes,
          compressed_size_bytes = excluded.compressed_size_bytes,
          compression = excluded.compression,
          schema_version = excluded.schema_version,
          updated_at = excluded.updated_at,
          last_used_at = excluded.last_used_at,
          expires_at = excluded.expires_at
      `).run(
        compactId,
        sessionId,
        responseId,
        `Mockdata Responses 状态分片 ${shardKey} 摘要，关联主 API Key、主分组和主 AI 账户。`,
        created.users.admin.id,
        created.apiKeys.adminMain.id,
        created.groups.main.id,
        providerCode,
        created.accounts.primary.id,
        'gpt-5.4-mini',
        'gpt-5.4-mini',
        compactStorageKey,
        compressedResponseBytes,
        sha256(`${compactId}:${compactStorageKey}`),
        rawCompactBytes,
        compressedCompactBytes,
        now,
        now,
        now,
        expiresAt
      )
      database.exec('COMMIT')
    } catch (error) {
      database.exec('ROLLBACK')
      throw error
    }
  }
}

function createBackgroundJobCoverage(database: StatsDatabase, now: string): void {
  const runningStartedAt = new Date(Date.now() - 8 * minuteMs).toISOString()
  const completedStartedAt = new Date(Date.now() - 3 * 60 * minuteMs).toISOString()
  const completedFinishedAt = new Date(Date.now() - 2 * 60 * minuteMs).toISOString()
  database.prepare(`
    INSERT INTO background_task_runs (
      run_id, job_name, job_type, worker_role, status, lease_key, owner_id,
      params_json, result_json, error_message, submitted_at, started_at, heartbeat_at,
      finished_at, duration_ms, exit_code, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    `${idPrefix}background_task_usage_stats`,
    'usage_stats_aggregation',
    'scheduled',
    'stats-worker',
    'completed',
    `${idPrefix}lease_usage_stats`,
    `${idPrefix}worker_stats_01`,
    JSON.stringify({ shard: 'all', source: 'mockdata' }),
    JSON.stringify({ processed: 3729, status: 'ok' }),
    null,
    completedStartedAt,
    completedStartedAt,
    completedFinishedAt,
    completedFinishedAt,
    Date.parse(completedFinishedAt) - Date.parse(completedStartedAt),
    0,
    completedStartedAt,
    completedFinishedAt
  )
  database.prepare(`
    INSERT INTO background_task_runs (
      run_id, job_name, job_type, worker_role, status, lease_key, owner_id,
      params_json, result_json, error_message, submitted_at, started_at, heartbeat_at,
      finished_at, duration_ms, exit_code, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?)
  `).run(
    `${idPrefix}background_task_runtime_log_cursor`,
    'runtime_log_ingest',
    'scheduled',
    'ops-worker',
    'running',
    `${idPrefix}lease_runtime_log_cursor`,
    `${idPrefix}worker_ops_01`,
    JSON.stringify({ logFile: `${idPrefix}runtime.log` }),
    JSON.stringify({}),
    null,
    runningStartedAt,
    runningStartedAt,
    now,
    runningStartedAt,
    now
  )
  database.prepare(`
    INSERT INTO background_job_leases (
      lease_key, job_name, shard_key, owner_id, run_id,
      lease_until, heartbeat_at, started_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(lease_key) DO UPDATE SET
      job_name = excluded.job_name,
      shard_key = excluded.shard_key,
      owner_id = excluded.owner_id,
      run_id = excluded.run_id,
      lease_until = excluded.lease_until,
      heartbeat_at = excluded.heartbeat_at,
      started_at = excluded.started_at,
      updated_at = excluded.updated_at
  `).run(
    `${idPrefix}lease_runtime_log_cursor`,
    'runtime_log_ingest',
    `${idPrefix}runtime.log`,
    `${idPrefix}worker_ops_01`,
    `${idPrefix}background_task_runtime_log_cursor`,
    new Date(Date.now() + 5 * minuteMs).toISOString(),
    now,
    runningStartedAt,
    now
  )
}

function createStatsDirtyQueueCoverage(database: StatsDatabase, created: CreatedMockdata, now: string): void {
  const clientIpRows = database.prepare(`
    SELECT ip_hash
    FROM client_ip_registry
    WHERE client_ip LIKE '10.10.%'
       OR client_ip LIKE '10.20.%'
    ORDER BY last_seen_at DESC, ip_hash ASC
    LIMIT 2
  `).all() as Array<{ ip_hash?: string }>
  const firstDirtyAt = new Date(Date.now() - 15 * minuteMs).toISOString()
  database.prepare(`
    INSERT INTO account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      first_dirty_at = excluded.first_dirty_at,
      updated_at = excluded.updated_at
  `).run(created.accounts.temporary.id, firstDirtyAt, now)

  const ipHash = clientIpRows[0]?.ip_hash ?? `${idPrefix}client_ip_hash`
  database.prepare(`
    INSERT INTO client_ip_range_window_dirty_ips (ip_hash, updated_at)
    VALUES (?, ?)
    ON CONFLICT(ip_hash) DO UPDATE SET updated_at = excluded.updated_at
  `).run(ipHash, now)
  database.prepare(`
    INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, updated_at)
    VALUES (?, ?)
    ON CONFLICT(ip_hash) DO UPDATE SET updated_at = excluded.updated_at
  `).run(clientIpRows[1]?.ip_hash ?? ipHash, now)
}

function createUsageRecordCleanupDeductionCoverage(
  database: StatsDatabase,
  created: CreatedMockdata,
  usageRecords: UsageRecordSeed[],
  now: string
): void {
  const primaryRecord = usageRecords.find((record) => record.apiKeyId === created.apiKeys.adminMain.id && record.accountId === created.accounts.primary.id)
    ?? usageRecords[0]
  if (!primaryRecord) return
  database.prepare(`
    INSERT INTO usage_record_cleanup_deductions (
      usage_id, api_key_id, account_id, system_account_id, source_shard_key,
      record_json, stats_subtracted_at, shard_deleted_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
      api_key_id = excluded.api_key_id,
      account_id = excluded.account_id,
      system_account_id = excluded.system_account_id,
      record_json = excluded.record_json,
      stats_subtracted_at = excluded.stats_subtracted_at,
      shard_deleted_at = excluded.shard_deleted_at,
      updated_at = excluded.updated_at
  `).run(
    `${idPrefix}cleanup_deduction_${primaryRecord.id}`,
    primaryRecord.apiKeyId ?? created.apiKeys.adminMain.id,
    primaryRecord.accountId ?? null,
    primaryRecord.systemAccountId ?? created.users.admin.id,
    shardKeyForCreatedAt(primaryRecord.createdAt),
    JSON.stringify({
      id: primaryRecord.id,
      traceId: primaryRecord.traceId,
      model: primaryRecord.model,
      endpoint: primaryRecord.endpoint,
      reason: `${namePrefix}API Key 删除后等待统计扣减样本`
    }),
    now,
    null,
    now,
    now
  )
}

function markAccountTestSessionCompleted(sessionId: string): void {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_sessions
    SET status = 'completed',
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
  `).run(now, now, sessionId)
}

function accountTestResult(
  account: AccountSummary,
  success: boolean,
  message: string,
  statusCode = success ? 200 : 503,
  errorCode?: string
) {
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    traceId: `${tracePrefix}account-test-${account.id}`,
    success,
    statusCode,
    errorCode,
    message,
    model: 'gpt-5.4-mini',
    requestUrl: 'https://api.openai.com/v1/responses',
    responseBody: success ? { id: `${idPrefix}account_test_response`, status: 'completed' } : { error: { code: errorCode, message } },
    durationMs: success ? 860 : 2400,
    firstTokenMs: success ? 320 : undefined,
    clientCompatibility: account.clientCompatibility,
    testClientCompatibility: account.clientCompatibility
  }
}

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function shardKeyForCreatedAt(createdAt: string): string {
  const date = new Date(createdAt)
  const dateKey = Number.isNaN(date.getTime())
    ? new Date().toISOString().slice(0, 10).replaceAll('-', '')
    : date.toISOString().slice(0, 10).replaceAll('-', '')
  return `${dateKey}:s00`
}
