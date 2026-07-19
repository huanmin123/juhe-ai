import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { getAccountStatusSnapshot, parseAccountStatusSnapshotAccountIds } from '../../modules/accounts/account-status-snapshot.service.js'
import { logger } from '../../shared/logger.js'
import { todayDateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.deepEqual(
  parseAccountStatusSnapshotAccountIds(' account_b,account_a,account_b '),
  ['account_b', 'account_a'],
  '状态快照 ID 应去空白、去重并保持首次出现顺序'
)
assert.throws(() => parseAccountStatusSnapshotAccountIds(''), /至少选择 1 个账户/)
assert.throws(
  () => parseAccountStatusSnapshotAccountIds(Array.from({ length: 101 }, (_, index) => `account_${index}`).join(',')),
  /最多查询 100 个账户/
)
const fullLengthAccountIds = Array.from({ length: 100 }, (_, index) => `acc_${String(index).padStart(4, '0')}_${'x'.repeat(31)}`)
assert.equal(
  parseAccountStatusSnapshotAccountIds(fullLengthAccountIds.join(',')).length,
  100,
  '状态快照必须接受 100 个真实长度账户 ID'
)
assert.throws(() => parseAccountStatusSnapshotAccountIds(`account_${'x'.repeat(8190)}`), /查询参数过长/)

const repositorySource = readFileSync(resolve('src/storage/account-status-snapshot.repository.ts'), 'utf8')
assert.doesNotMatch(repositorySource, /usage_records/, '状态快照不得扫描使用记录明细')
assert.doesNotMatch(repositorySource, /credentials_encrypted|credential_mask/, '状态快照不得读取凭据或凭据摘要')
assert.match(repositorySource, /loadAccountUsageSummariesForScopesAsync/, '今日用量必须来自预聚合统计读取器')
assert.match(repositorySource, /authorization_effective_source_team_id/, '状态快照必须保留团队授权额度来源字段')
assert.match(repositorySource, /list_account_status_snapshots_read_only/, 'SQLite 状态投影必须投递到 read worker')
const readWorkerSource = readFileSync(resolve('src/storage/sqlite-read-worker.ts'), 'utf8')
assert.match(readWorkerSource, /case 'list_account_status_snapshots_read_only'/, 'SQLite read worker 必须实现状态投影 operation')

const tempRoot = resolve(tmpdir(), `juhe-ai-account-status-snapshot-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-status-snapshot-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const user = repositories.createSystemAccount({
    username: 'account_status_snapshot_user',
    displayName: '账户状态快照用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userAccess = { systemAccountId: user.id, role: 'user' as const }
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '账户快照回归分组', providerCode: 'gpt' }, userAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户快照回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-status-snapshot', base_url: 'https://api.openai.com/v1' },
    status: 'active',
    groupId: group.id
  }, userAccess)
  const foreignGroup = repositories.createGroup({ name: '账户快照管理员分组', providerCode: 'gpt' }, adminAccess)
  const foreignAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户快照管理员账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-status-snapshot-admin', base_url: 'https://api.openai.com/v1' },
    status: 'active',
    groupId: foreignGroup.id
  }, adminAccess)
  const lastUsedAt = '2026-07-16T02:03:04.000Z'
  databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET status = 'active', schedulable = 1, last_used_at = CASE WHEN id = ? THEN ? ELSE last_used_at END WHERE id IN (?, ?)")
    .run(account.id, lastUsedAt, account.id, foreignAccount.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET last_health_check_at = ?, next_health_check_at = ?, last_health_check_status_code = ?,
        last_health_check_error_code = ?, last_health_check_error_message = ?, last_health_check_trace_id = ?
    WHERE id = ?
  `).run(
    '2026-07-20T00:30:00.000Z',
    '2026-07-20T12:30:00.000Z',
    503,
    'model_not_found',
    '测试探针失败',
    'trace-snapshot-latest',
    account.id
  )
  const today = todayDateKey(await usageStatsTimezoneAsync())
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'account', ?, ?, 7, 6, 1, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
  `).run(user.id, account.id, today, lastUsedAt, lastUsedAt)
  const result = await getAccountStatusSnapshot(userAccess, [foreignAccount.id, account.id])
  assert.deepEqual(result.items.map((item) => item.id), [account.id], '用户快照必须省略无权查看的账户 ID')
  assert.equal(result.items[0]?.effectiveAvailability.label, '可调度')
  assert.equal(result.items[0]?.todayUsage.requestCount, 7, '状态快照必须读取今日账户预聚合用量')
  assert.equal(result.items[0]?.lastUsedAt, lastUsedAt, '状态快照必须返回账户最近使用时间')
  assert.equal(result.items[0]?.availabilityPresentation?.probe?.lastObservation?.traceId, 'trace-snapshot-latest', '状态快照必须返回最近检查 traceId')
  assert.equal(result.items[0]?.availabilityPresentation?.probe?.schedule.nextAttemptAt, '2026-07-20T12:30:00.000Z', '状态快照必须返回下次检查时间')
  assert.equal('credentials' in (result.items[0] ?? {}), false, '状态快照响应不得包含凭据')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('账户状态快照契约回归通过：ID 有界、权限裁剪、预聚合用量和可调度状态均正确')
