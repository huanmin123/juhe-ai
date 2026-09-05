import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { getAccountStatusSnapshot, hydrateAccountListPage } from '../../modules/accounts/account-status-snapshot.service.js'
import { logger } from '../../shared/logger.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import { ensureAccountDerivedStatusSqlFunctions } from '../../storage/account-derived-status-sql.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-runtime-status-sql-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-runtime-status-sql-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [database, repositories, sqliteReadWorkerPool] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: 'Key 运行态 SQL 回归分组', providerCode: 'gpt' }, access)
  const allUnverified = createMultiKeyAccount('全未验证 Key 池', ['sk-unverified-a', 'sk-unverified-b'])
  const missingRuntimeState = createMultiKeyAccount('缺失 Key 运行态', ['sk-missing-a', 'sk-missing-b'])
  const diagnosticsAccount = createMultiKeyAccount('运行态诊断隔离 Key 池', ['sk-diagnostic-a', 'sk-diagnostic-b'])
  const summaryProbeAccount = createMultiKeyAccount('探测汇总候选 Key 池', ['sk-summary-active', 'sk-summary-error', 'sk-summary-rate-limited'])
  const db = database.getBusinessDatabase()
  db.prepare("UPDATE accounts SET status = 'active', schedulable = 1 WHERE id IN (?, ?, ?, ?)")
    .run(allUnverified.id, missingRuntimeState.id, diagnosticsAccount.id, summaryProbeAccount.id)
  ensureAccountDerivedStatusSqlFunctions(db)
  const now = new Date().toISOString()
  const earliestProbe = new Date(Date.now() + 30_000).toISOString()
  const laterProbe = new Date(Date.now() + 60_000).toISOString()
  const insert = db.prepare(`
    INSERT INTO account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index, status, failure_count, consecutive_failures,
      success_count, next_probe_at, probe_claimed_until, last_failure_at, last_error_code, last_error_message,
      last_trace_id, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, NULL, ?, ?, ?, ?, ?, ?)
  `)
  const unverifiedEntries = accountApiKeyEntries({ api_keys: ['sk-unverified-a', 'sk-unverified-b'] })
  for (const entry of unverifiedEntries) {
    insert.run(
      `state-unverified-${entry.index}`,
      access.systemAccountId,
      allUnverified.id,
      entry.fingerprint,
      entry.index,
      'unverified',
      entry.index === 0 ? laterProbe : earliestProbe,
      null,
      null,
      null,
      null,
      now,
      now
    )
  }
  const diagnosticEntries = accountApiKeyEntries({ api_keys: ['sk-diagnostic-a', 'sk-diagnostic-b'] })
  insert.run(
    'state-diagnostic-error',
    access.systemAccountId,
    diagnosticsAccount.id,
    diagnosticEntries[0]!.fingerprint,
    diagnosticEntries[0]!.index,
    'error',
    laterProbe,
    now,
    'upstream_rejected',
    '来源 Key 失败诊断不得进入公共列表',
    'trace-api-key-runtime-private',
    now,
    now
  )
  insert.run(
    'state-diagnostic-unverified',
    access.systemAccountId,
    diagnosticsAccount.id,
    diagnosticEntries[1]!.fingerprint,
    diagnosticEntries[1]!.index,
    'unverified',
    earliestProbe,
    null,
    null,
    null,
    null,
    now,
    now
  )
  const summaryProbeEntries = accountApiKeyEntries({ api_keys: ['sk-summary-active', 'sk-summary-error', 'sk-summary-rate-limited'] })
  const staleActiveProbe = new Date(Date.now() - 60_000).toISOString()
  insert.run(
    'state-summary-active',
    access.systemAccountId,
    summaryProbeAccount.id,
    summaryProbeEntries[0]!.fingerprint,
    summaryProbeEntries[0]!.index,
    'active',
    staleActiveProbe,
    null,
    null,
    null,
    null,
    now,
    now
  )
  insert.run(
    'state-summary-error',
    access.systemAccountId,
    summaryProbeAccount.id,
    summaryProbeEntries[1]!.fingerprint,
    summaryProbeEntries[1]!.index,
    'error',
    laterProbe,
    now,
    'upstream_rejected',
    'active Key 的陈旧探测时间不得污染汇总',
    'trace-summary-runtime-private',
    now,
    now
  )
  insert.run(
    'state-summary-rate-limited',
    access.systemAccountId,
    summaryProbeAccount.id,
    summaryProbeEntries[2]!.fingerprint,
    summaryProbeEntries[2]!.index,
    'rate_limited',
    laterProbe,
    now,
    'api_key_quota_insufficient',
    '周期额度不足，等待下次探测',
    'trace-summary-runtime-rate-limited',
    now,
    now
  )

  const derivedStatus = db.prepare(`
    SELECT account_api_key_pool_all_unavailable(provider_code, protocol_code, protocol_version, type, credentials_encrypted, (
      SELECT group_concat(key_fingerprint || char(30) || status, char(31))
      FROM account_api_key_runtime_states
      WHERE account_id = accounts.id
    )) AS all_unavailable
    FROM accounts
    WHERE id = ?
  `).get(allUnverified.id) as { all_unavailable: number }
  assert.equal(derivedStatus.all_unavailable, 1, '两个 Key 都处于 unverified 时 SQLite 派生必须判定全不可用')

  const activeRows = repositories.listAccounts(access, { status: 'active' })
  assert.equal(activeRows.some((item) => item.id === allUnverified.id), false, '全 unverified Key 池不得仍被 owner 账户读取筛为 active')
  assert.equal(activeRows.some((item) => item.id === missingRuntimeState.id), true, '缺失 Key 运行态必须继续兼容为 active')
  const activeOptions = repositories.listAccountOptions(access, { status: 'active' })
  assert.equal(activeOptions.some((item) => item.id === allUnverified.id), false, '全 unverified Key 池不得仍出现在 active 账户选项')
  assert.equal(activeOptions.some((item) => item.id === missingRuntimeState.id), true, '缺失 Key 运行态必须继续出现在 active 账户选项')

  const ownerSnapshot = await getAccountStatusSnapshot(access, [allUnverified.id])
  assert.equal(ownerSnapshot.items[0]?.effectiveAvailability.status, 'api_key_pool_unavailable', '运行态快照必须将全 unverified Key 池视为不可调度')
  assert.deepEqual(ownerSnapshot.items[0]?.apiKeyRuntime, {
    total: 2,
    active: 0,
    temporaryUnavailable: 0,
    rateLimited: 0,
    error: 0,
    disabled: 0,
    unavailable: 2,
    allUnavailable: true,
    nextProbeAt: earliestProbe
  }, 'owner 快照必须返回无明细的 Key 运行态汇总')
  assertPublicRuntimeSummary(ownerSnapshot.items[0]?.apiKeyRuntime, 'owner 状态快照')
  assertKeyFailureDiagnosticsAbsent(ownerSnapshot, 'owner 状态快照')

  const basePage = await repositories.listAccountManagementItemsPageReadOnly(access, {
    ids: [allUnverified.id], page: 1, pageSize: 20
  })
  const hydratedPage = await hydrateAccountListPage(access, basePage)
  assert.deepEqual(hydratedPage.items[0]?.apiKeyRuntime, ownerSnapshot.items[0]?.apiKeyRuntime, '普通 owner 列表水合必须保留 Key 运行态汇总')
  assertPublicRuntimeSummary(hydratedPage.items[0]?.apiKeyRuntime, '普通 owner 列表')
  assert.equal(hydratedPage.items[0]?.effectiveAvailability.status, 'api_key_pool_unavailable', '普通 owner 列表水合必须保留 Key 池不可用状态')
  assertKeyFailureDiagnosticsAbsent(hydratedPage, '普通 owner 列表')

  const diagnosticsSnapshot = await getAccountStatusSnapshot(access, [diagnosticsAccount.id])
  assert.deepEqual(diagnosticsSnapshot.items[0]?.apiKeyRuntime, {
    total: 2,
    active: 0,
    temporaryUnavailable: 0,
    rateLimited: 0,
    error: 1,
    disabled: 0,
    unavailable: 2,
    allUnavailable: true,
    nextProbeAt: earliestProbe
  }, '独立诊断账户的 error + unverified Key 必须派生为全不可用')
  assertDiagnosticAccountPublicResponse(diagnosticsSnapshot, '诊断账户 owner 状态快照')
  const diagnosticsBasePage = await repositories.listAccountManagementItemsPageReadOnly(access, {
    ids: [diagnosticsAccount.id], page: 1, pageSize: 20
  })
  const diagnosticsPage = await hydrateAccountListPage(access, diagnosticsBasePage)
  assertDiagnosticAccountPublicResponse(diagnosticsPage, '诊断账户 owner 普通列表')

  const summaryProbeSnapshot = await getAccountStatusSnapshot(access, [summaryProbeAccount.id])
  assert.equal(
    summaryProbeSnapshot.items[0]?.apiKeyRuntime?.nextProbeAt,
    laterProbe,
    '聚合 nextProbeAt 只能取实际可到期探测的非 active Key，必须忽略更早的 active Key 陈旧时间'
  )

  const grantee = repositories.createSystemAccount({
    username: 'api_key_runtime_status_grantee',
    displayName: 'key_runtime_status_grantee',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({ name: 'Key 运行态 SQL 被授权分组', providerCode: 'gpt' }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: diagnosticsAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '诊断 Key 汇总不泄露明细回归'
  }, access)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === diagnosticsAccount.id)
  assert(authorizedInstance, '回归夹具必须创建授权账户实例')
  const authorizedSnapshot = await getAccountStatusSnapshot(granteeAccess, [authorizedInstance.id])
  assert.deepEqual(authorizedSnapshot.items[0]?.apiKeyRuntime, diagnosticsSnapshot.items[0]?.apiKeyRuntime, '授权实例可沿用来源诊断 Key 聚合可用性')
  assertDiagnosticAccountPublicResponse(authorizedSnapshot, '诊断账户授权实例状态快照')
  assert.equal(authorizedSnapshot.items[0]?.effectiveAvailability.status, 'api_key_pool_unavailable', '授权实例状态快照必须保留来源 Key 池不可用状态')
  const authorizedBasePage = await repositories.listAccountManagementItemsPageReadOnly(granteeAccess, {
    ids: [authorizedInstance.id], page: 1, pageSize: 20
  })
  const authorizedPage = await hydrateAccountListPage(granteeAccess, authorizedBasePage)
  assert.deepEqual(authorizedPage.items[0]?.apiKeyRuntime, diagnosticsSnapshot.items[0]?.apiKeyRuntime, '授权实例普通列表可返回来源诊断 Key 公共汇总')
  assertDiagnosticAccountPublicResponse(authorizedPage, '诊断账户授权实例普通列表')
  assert.equal(authorizedPage.items[0]?.effectiveAvailability.status, 'api_key_pool_unavailable', '授权实例普通列表水合必须保留来源 Key 池不可用状态')

  console.log('account-api-key-runtime-status-sql 回归通过')

  function createMultiKeyAccount(name: string, apiKeys: string[]) {
    return repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name,
      type: 'api_key',
      status: 'active',
      schedulable: true,
      credentials: { api_key: apiKeys[0], api_keys: apiKeys, base_url: 'https://api.openai.com/v1' },
      supportedModels: ['gpt-5.5'],
      groupId: group.id
    }, access)
  }

  function assertPublicRuntimeSummary(value: unknown, label: string): void {
    assert(value && typeof value === 'object', `${label}必须返回 Key 池公共汇总`)
    const summary = value as Record<string, unknown>
    assert.deepEqual(
      Object.keys(summary).sort(),
      ['active', 'allUnavailable', 'disabled', 'error', 'nextProbeAt', 'rateLimited', 'temporaryUnavailable', 'total', 'unavailable'],
      `${label}只能返回允许的 Key 池公共字段`
    )
    for (const privateField of ['lastFailureAt', 'lastErrorCode', 'lastErrorMessage', 'lastTraceId', 'apiKeyRuntimeDetails']) {
      assert.equal(privateField in summary, false, `${label}不得泄露 ${privateField}`)
    }
  }

  function assertDiagnosticAccountPublicResponse(value: unknown, label: string): void {
    assert(value && typeof value === 'object', `${label}必须返回账户响应对象`)
    const items = (value as { items?: unknown }).items
    assert(Array.isArray(items) && items.length === 1, `${label}必须只返回诊断账户`)
    const item = items[0] as Record<string, unknown>
    assertPublicRuntimeSummary(item.apiKeyRuntime, label)
    assert.equal((item.effectiveAvailability as { status?: unknown } | undefined)?.status, 'api_key_pool_unavailable', `${label}必须派生为 Key 池不可用`)
    assert.equal((item.availabilityPresentation as { status?: unknown } | undefined)?.status, 'key_pool_unavailable', `${label}必须保留嵌套可用性展示`)
    assertKeyFailureDiagnosticsAbsent(value, label)
  }

  function assertKeyFailureDiagnosticsAbsent(value: unknown, label: string, path: string[] = []): void {
    if (Array.isArray(value)) {
      value.forEach((item, index) => assertKeyFailureDiagnosticsAbsent(item, label, [...path, String(index)]))
      return
    }
    if (!value || typeof value !== 'object') return

    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      const genericPresentationReason = key === 'reason'
        && (path.at(-1) === 'effectiveAvailability' || path.at(-1) === 'availabilityPresentation')
      if (!genericPresentationReason) {
        assert.equal(
          [
            'lastFailureAt', 'lastErrorCode', 'lastErrorMessage', 'lastTraceId',
            'attemptedAt', 'errorCode', 'reason', 'traceId',
            'apiKeyRuntimeDetails', 'keyIndex', 'keyFingerprintPrefix', 'keySuffix',
            'weight', 'failureCount', 'consecutiveFailures', 'successCount', 'cooldownUntil', 'lastAttemptAt', 'lastSuccessAt'
          ].includes(key),
          false,
          `${label}不得在 ${[...path, key].join('.')} 返回来源 Key 失败诊断`
        )
      }
      assertKeyFailureDiagnosticsAbsent(child, label, [...path, key])
    }
  }
} finally {
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}
