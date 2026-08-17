import { strict as assert } from 'node:assert'
import { DatabaseSync } from 'node:sqlite'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const tempRoot = mkdtempSync(join(process.env.TEMP ?? process.cwd(), 'juhe-ai-f3-query-'))
const sqlitePath = join(tempRoot, 'audit.sqlite3')
const emptyPath = join(tempRoot, 'empty.sqlite3')
const blobRoot = join(tempRoot, 'blobs')
const hotSearchRoot = join(tempRoot, 'hot-search')

const adapterSource = readFileSync(fileURLToPath(new URL('../../storage/audit-log-f3-query.repository.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(adapterSource, /\b(?:INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|CREATE\s+TABLE|DROP\s+TABLE)\b/i, 'F3 Node query adapter 不得包含写 SQL')
assert.match(adapterSource, /readOnly:\s*true/, 'SQLite adapter 必须以 readOnly 打开')
assert.match(adapterSource, /PRAGMA\s+query_only\s*=\s*ON/i, 'SQLite adapter 必须设置 query_only')
assert.match(adapterSource, /BEGIN\s+READ\s+ONLY/i, 'PostgreSQL adapter 必须使用 READ ONLY 事务')
assert.match(adapterSource, /postgresTransactionLocalSettingsSql/, 'PostgreSQL adapter 必须接收运行时提供的事务本地设置')
assert.match(adapterSource, /BEGIN READ ONLY[\s\S]{0,240}transactionLocalSettingsSql[\s\S]{0,240}connection\.query\(convertQuestionPlaceholdersToPostgres/, 'F3 PostgreSQL 读取必须在查询前应用事务本地设置')
assert.doesNotMatch(adapterSource, /runtimeConfig/, 'F3 query adapter 不得依赖全局 runtimeConfig')
const searchHotStart = adapterSource.indexOf('async searchHot(options: AuditLogF3HotSearchOptions)')
const searchHotEnd = adapterSource.indexOf('\n  getRuntime(): AuditLogF3Runtime', searchHotStart)
assert.ok(searchHotStart >= 0 && searchHotEnd > searchHotStart, '必须能定位 F3 hot-search 实现')
const searchHotSource = adapterSource.slice(searchHotStart, searchHotEnd)
assert.doesNotMatch(searchHotSource, /\breadFile\s*\(/, 'F3 hot-search 不得完整 readFile 小时 NDJSON')
assert.doesNotMatch(searchHotSource, /Date\.parse\(/, 'F3 hot-search supplied 时间不得按本机时区解析')
assert.match(searchHotSource, /f3TimestampMilliseconds\(options\.endAt, 'F3 热搜索 endAt'\)/, 'F3 hot-search endAt 必须严格解析')
assert.match(searchHotSource, /f3TimestampMilliseconds\(options\.startAt, 'F3 热搜索 startAt'\)/, 'F3 hot-search startAt 必须严格解析')
assert.match(searchHotSource, /scanF3HotSearchFile/, 'F3 hot-search 必须通过有界文件扫描器读取 NDJSON')
assert.match(adapterSource, /const f3HotSearchMaxFiles = 2/, 'F3 hot-search 必须限制扫描文件数')
assert.match(adapterSource, /const f3HotSearchMaxScanBytes = 4 \* 1024 \* 1024/, 'F3 hot-search 必须限制总扫描字节数')
assert.match(adapterSource, /const f3HotSearchMaxScanLines = 10_000/, 'F3 hot-search 必须限制扫描行数')
assert.match(adapterSource, /const f3HotSearchMaxLineBytes = 256 \* 1024/, 'F3 hot-search 必须限制单行大小')
assert.match(adapterSource, /const f3AbsoluteTimestampColumns = new Set/, 'F3 DB 时间字段必须在读取边界统一 canonical')
assert.match(adapterSource, /f3TimestampMilliseconds\(row\.createdAt, 'F3 热搜索 createdAt'\)/, 'F3 hot-search 文件中的 createdAt 必须严格解析')

const { createAuditLogF3QueryRepository, AuditLogF3SchemaError } = await import('../../storage/audit-log-f3-query.repository.js')

try {
  const database = new DatabaseSync(sqlitePath)
  database.exec(`
    CREATE TABLE audit_logs (
      id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, traffic_source TEXT NOT NULL,
      system_account_id TEXT, api_key_id TEXT, conversation_key TEXT, session_id TEXT,
      session_client_type TEXT, group_id TEXT, account_id TEXT, provider_code TEXT,
      method TEXT NOT NULL, path TEXT NOT NULL, query_string TEXT, model TEXT,
      upstream_model TEXT, pricing_model TEXT, model_mapping_applied INTEGER NOT NULL DEFAULT 0,
      model_mapping_source TEXT, source_endpoint_family TEXT, upstream_endpoint_family TEXT,
      stream INTEGER NOT NULL DEFAULT 0, client_ip TEXT, user_agent TEXT,
      audit_outcome TEXT NOT NULL, success INTEGER NOT NULL DEFAULT 0, final_status_code INTEGER,
      error_phase TEXT, error_code TEXT, error_message TEXT, sample_bucket INTEGER NOT NULL,
      sample_reason TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
      payload_count INTEGER NOT NULL DEFAULT 0, raw_payload_bytes INTEGER NOT NULL DEFAULT 0,
      compressed_payload_bytes INTEGER NOT NULL DEFAULT 0, compression_saved_bytes INTEGER NOT NULL DEFAULT 0,
      error_group_id TEXT, capture_status TEXT NOT NULL, lifecycle_status TEXT NOT NULL,
      started_at TEXT NOT NULL, ended_at TEXT NOT NULL, duration_ms INTEGER,
      http_completed_at TEXT, http_duration_ms INTEGER, first_token_ms INTEGER, created_at TEXT NOT NULL
    );
    CREATE TABLE audit_log_attempts (
      id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL, attempt_index INTEGER NOT NULL,
      account_id TEXT, account_owner_system_account_id TEXT, group_id TEXT, proxy_url TEXT,
      provider_code TEXT, attempt_model TEXT, attempt_upstream_model TEXT, attempt_pricing_model TEXT,
      attempt_model_mapping_applied INTEGER NOT NULL DEFAULT 0, attempt_model_mapping_source TEXT,
      attempt_source_endpoint_family TEXT, attempt_upstream_endpoint_family TEXT,
      upstream_method TEXT NOT NULL, upstream_url TEXT NOT NULL, upstream_status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0, error_phase TEXT, error_code TEXT, error_message TEXT,
      started_at TEXT NOT NULL, ended_at TEXT, duration_ms INTEGER
    );
    CREATE TABLE audit_payload_blobs (
      id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, raw_size_bytes INTEGER NOT NULL,
      compressed_size_bytes INTEGER NOT NULL, content_type TEXT NOT NULL, content_encoding TEXT,
      compression TEXT NOT NULL DEFAULT 'none', storage_key TEXT NOT NULL, ref_count INTEGER NOT NULL DEFAULT 0,
      first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, created_at TEXT NOT NULL
    );
    CREATE TABLE audit_payload_refs (
      id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL, attempt_id TEXT, part_type TEXT NOT NULL,
      sequence_index INTEGER NOT NULL, content_type TEXT, content_encoding TEXT,
      headers_blob_id TEXT, body_blob_id TEXT, headers_sha256 TEXT, body_sha256 TEXT,
      raw_size_bytes INTEGER NOT NULL DEFAULT 0, compressed_size_bytes INTEGER NOT NULL DEFAULT 0,
      capture_status TEXT NOT NULL, drop_reason TEXT, created_at TEXT NOT NULL
    );
    CREATE TABLE audit_error_groups (
      id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, window_started_at TEXT NOT NULL,
      window_ended_at TEXT NOT NULL, system_account_id TEXT, api_key_id TEXT, group_id TEXT,
      account_id TEXT, provider_code TEXT, path TEXT, model TEXT, status_code INTEGER,
      error_phase TEXT, error_code TEXT, error_type TEXT, request_fingerprint TEXT,
      error_fingerprint TEXT, count INTEGER NOT NULL DEFAULT 0, first_event_id TEXT,
      last_event_id TEXT, sample_event_id TEXT, last_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
    );
  `)
  database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, method, path, model, model_mapping_applied, stream,
      audit_outcome, success, final_status_code, sample_bucket, sample_reason,
      attempt_count, payload_count, raw_payload_bytes, compressed_payload_bytes, compression_saved_bytes,
      error_group_id, capture_status, lifecycle_status, started_at, ended_at, duration_ms, created_at
    ) VALUES (?, ?, 'gateway', 'GET', '/v1/models', 'gpt-test', 0, 0, 'gateway_succeeded', 1, 200,
      1, 'sampled', 1, 1, 11, 11, 0, 'eg1', 'complete', 'finalized', ?, ?, 4, ?)
  `).run('audit-1', 'trace-1', '2026-08-09T09:00:00.000+09:00', '2026-08-09T09:00:00.004+09:00', '2026-08-09T00:00:00.004Z')
  database.prepare(`
    INSERT INTO audit_log_attempts (
      id, audit_log_id, attempt_index, account_id, group_id, provider_code, attempt_model,
      upstream_method, upstream_url, upstream_status_code, success, started_at, ended_at, duration_ms
    ) VALUES ('attempt-1', 'audit-1', 0, 'account-1', 'group-1', 'openai', 'gpt-test',
      'GET', 'https://example.test/v1/models', 200, 1, ?, ?, 3)
  `).run('2026-08-09T09:00:00.001+09:00', '2026-08-09T09:00:00.004+09:00')
  database.prepare(`
    INSERT INTO audit_payload_blobs (
      id, sha256, raw_size_bytes, compressed_size_bytes, content_type, compression, storage_key,
      first_seen_at, last_seen_at, created_at
    ) VALUES ('blob-headers', 'headers-sha', 15, 15, 'application/json', 'none', 'headers.json', ?, ?, ?),
             ('blob-body', 'body-sha', 11, 11, 'text/plain', 'none', 'body.txt', ?, ?, ?)
  `).run(
    '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z',
    '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z'
  )
  database.prepare(`
    INSERT INTO audit_payload_refs (
      id, audit_log_id, attempt_id, part_type, sequence_index, content_type,
      headers_blob_id, body_blob_id, headers_sha256, body_sha256, raw_size_bytes,
      compressed_size_bytes, capture_status, created_at
    ) VALUES ('payload-1', 'audit-1', 'attempt-1', 'gateway_response', 0, 'text/plain',
      'blob-headers', 'blob-body', 'headers-sha', 'body-sha', 11, 11, 'complete', ?)
  `).run('2026-08-09T09:00:00.004+09:00')
  database.prepare(`
    INSERT INTO audit_error_groups (
      id, fingerprint, window_started_at, window_ended_at, path, model, status_code,
      error_phase, error_code, error_type, request_fingerprint, error_fingerprint,
      count, first_event_id, last_event_id, sample_event_id, last_message, created_at, updated_at
    ) VALUES ('eg1', 'fingerprint-1', ?, ?, '/v1/models', 'gpt-test', 500,
      'upstream', 'E_TEST', 'Error', 'request-1', 'error-1', 1, 'audit-1', 'audit-1', 'audit-1', 'failed', ?, ?)
  `).run(
    '2026-08-09T09:00:00.000+09:00', '2026-08-09T10:00:00.000+09:00',
    '2026-08-09T09:00:00.000+09:00', '2026-08-09T09:00:00.000+09:00'
  )
  database.close()

  // The adapter keeps blob roots explicit and never creates them.
  mkdirSync(blobRoot, { recursive: true })
  mkdirSync(hotSearchRoot, { recursive: true })
  writeFileSync(join(blobRoot, 'headers.json'), '{"x-test":"ok"}')
  writeFileSync(join(blobRoot, 'body.txt'), 'hello world')

  const hotSearchEndMs = Date.now() - 1_000
  const hotSearchStartMs = hotSearchEndMs - 10 * 60 * 1_000
  const hotSearchBucket = new Date(hotSearchEndMs)
  const hotSearchBucketName = [
    hotSearchBucket.getUTCFullYear(),
    String(hotSearchBucket.getUTCMonth() + 1).padStart(2, '0'),
    String(hotSearchBucket.getUTCDate()).padStart(2, '0'),
    String(hotSearchBucket.getUTCHours()).padStart(2, '0')
  ].join('')
  const hotSearchRecord = (id: string, createdAtMs: number, text: string): string => JSON.stringify({
    auditLogId: id,
    createdAt: new Date(createdAtMs).toISOString(),
    text
  })
  const hotSearchRows = [
    hotSearchRecord('audit-hot-old', hotSearchEndMs - 20_000, 'needle old match'),
    hotSearchRecord('audit-hot-latest', hotSearchEndMs - 10_000, 'needle latest match')
  ]
  const hotSearchFiller = hotSearchRecord('audit-hot-filler', hotSearchEndMs - 5_000, `filler ${'x'.repeat(64 * 1024)}`)
  while (Buffer.byteLength(`${hotSearchRows.join('\n')}\n`) <= 4 * 1024 * 1024) {
    hotSearchRows.push(hotSearchFiller)
  }
  hotSearchRows.push(hotSearchRecord('audit-hot-out-of-budget', hotSearchEndMs - 2_000, 'needle after scan budget'))
  writeFileSync(join(hotSearchRoot, `audit-hot-${hotSearchBucketName}.ndjson`), hotSearchRows.join('\n'))

  const repository = await createAuditLogF3QueryRepository({ sqlitePath, payloadBlobDirectory: blobRoot, hotSearchDirectory: hotSearchRoot })
  try {
    assert.deepEqual(repository.getRuntime(), { mode: 'sqlite', readOnly: true, queryOnly: true, schemaReady: true })
    const page = await repository.listAuditLogs({ pageSize: 1 })
    assert.equal(page.items.length, 1)
    assert.equal(page.items[0]?.id, 'audit-1')
    assert.equal(page.items[0]?.createdAt, '2026-08-09T00:00:00.004Z')
    assert.equal(page.items[0]?.lifecycleStatus, 'finalized')
    assert.equal((await repository.listAuditLogs({ startAt: '2026-08-09T09:00:00.004+09:00' })).items[0]?.id, 'audit-1', 'F3 list 时间筛选必须 canonical numeric offset')
    await assert.rejects(
      () => repository.listAuditLogs({ startAt: '2026-08-09T00:00:00.004' }),
      /F3 审计 startAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
      'F3 list supplied bare startAt 必须显式失败'
    )
    const detail = await repository.getAuditLogDetail('audit-1')
    assert.equal(detail?.startedAt, '2026-08-09T00:00:00.000Z')
    assert.equal(detail?.endedAt, '2026-08-09T00:00:00.004Z')
    assert.equal(detail?.attempts[0]?.startedAt, '2026-08-09T00:00:00.001Z')
    assert.equal(detail?.attempts[0]?.endedAt, '2026-08-09T00:00:00.004Z')
    assert.equal(detail?.payloads[0]?.createdAt, '2026-08-09T00:00:00.004Z')
    assert.equal(detail?.errorGroup?.updatedAt, '2026-08-09T00:00:00.000Z')
    assert.equal(detail?.attempts[0]?.upstreamUrl, 'https://example.test/v1/models')
    assert.equal(detail?.payloads[0]?.id, 'payload-1')
    assert.equal(detail?.errorGroup?.id, 'eg1')
    const payload = await repository.getAuditLogPayload('audit-1', 'payload-1', { full: true, includeHeaders: true })
    assert.equal(payload?.bodyText, 'hello world')
    assert.deepEqual(payload?.headers, { 'x-test': 'ok' })
    assert.equal((await repository.listAuditErrorGroups()).items[0]?.id, 'eg1')
    assert.equal((await repository.listAuditErrorGroupEvents('eg1')).items[0]?.id, 'audit-1')

    const boundedHotSearch = await repository.searchHot({
      keywords: ['needle'],
      startAt: new Date(hotSearchStartMs).toISOString(),
      endAt: new Date(hotSearchEndMs).toISOString(),
      limit: 100
    })
    assert.deepEqual(boundedHotSearch.auditLogIds, ['audit-hot-latest', 'audit-hot-old'])
    assert.equal(boundedHotSearch.scannedFileCount, 1)
    assert.equal(boundedHotSearch.truncated, true)
    assert.match(boundedHotSearch.message ?? '', /读取上限/)

    const limitedHotSearch = await repository.searchHot({
      keywords: ['needle'],
      startAt: new Date(hotSearchStartMs).toISOString(),
      endAt: new Date(hotSearchEndMs).toISOString(),
      limit: 1
    })
    assert.deepEqual(limitedHotSearch.auditLogIds, ['audit-hot-latest'])
    assert.equal(limitedHotSearch.truncated, true)
    await assert.rejects(
      () => repository.searchHot({ keywords: ['needle'], endAt: '2026-08-09T00:00:00.000' }),
      /F3 热搜索 endAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
      'F3 hot-search supplied bare endAt 必须显式失败'
    )
    writeFileSync(join(hotSearchRoot, `audit-hot-${hotSearchBucketName}.ndjson`), JSON.stringify({
      auditLogId: 'audit-hot-invalid-created-at',
      createdAt: '2026-08-09T00:00:00.000',
      text: 'needle'
    }))
    await assert.rejects(
      () => repository.searchHot({
        keywords: ['needle'],
        startAt: new Date(hotSearchStartMs).toISOString(),
        endAt: new Date(hotSearchEndMs).toISOString()
      }),
      /F3 热搜索 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
      'F3 hot-search NDJSON bare createdAt 必须显式失败'
    )
  } finally {
    await repository.close()
  }

  const bareTimestampDatabase = new DatabaseSync(sqlitePath)
  bareTimestampDatabase.prepare('UPDATE audit_logs SET created_at = ? WHERE id = ?').run('2026-08-09T00:00:00.004', 'audit-1')
  bareTimestampDatabase.close()
  const bareTimestampRepository = await createAuditLogF3QueryRepository({ sqlitePath, payloadBlobDirectory: blobRoot, hotSearchDirectory: hotSearchRoot })
  try {
    await assert.rejects(
      () => bareTimestampRepository.listAuditLogs(),
      /F3 审计 created_at必须是带 Z 或数值 offset 的 RFC3339 时间/,
      'F3 DB bare created_at 不得继续兼容读取'
    )
  } finally {
    await bareTimestampRepository.close()
  }

  const emptyDatabase = new DatabaseSync(emptyPath)
  emptyDatabase.close()
  await assert.rejects(
    () => createAuditLogF3QueryRepository({ sqlitePath: emptyPath }),
    (error: unknown) => error instanceof AuditLogF3SchemaError && error.mode === 'sqlite' && error.missingTables.includes('audit_logs')
  )
} finally {
  // node:sqlite can release a closed Windows file handle on the next turn.
  await new Promise<void>((resolveCleanup) => setTimeout(resolveCleanup, 500))
  try {
    rmSync(tempRoot, { recursive: true, force: true, maxRetries: 8, retryDelay: 25 })
  } catch (error) {
    if (!(typeof error === 'object' && error !== null && 'code' in error && String((error as { code?: unknown }).code) === 'EBUSY')) {
      throw error
    }
    console.warn(`F3 query adapter regression cleanup retained a temporary SQLite handle: ${tempRoot}`)
  }
}

console.log('F3 Node audit query adapter regression passed: SQLite readOnly/query_only, list/detail/attempt/payload/error-group/runtime/schema errors and bounded hot-search reads are visible.')
