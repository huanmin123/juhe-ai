import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { sanitizeAccountListResponse } from '../../modules/accounts/account-response-sanitizer.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-projection-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-list-projection-secret'
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
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '列表严格投影分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '列表严格投影账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-list-projection',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    modelMappings: [],
    status: 'active',
    groupId: group.id
  }, access)

  seedAccountUsage(account.id)
  seedAccountQuality(account.id)
  const result = repositories.listAccountItemsPageReadOnly(access, { page: 1, pageSize: 20 })
  const response = sanitizeAccountListResponse(result)
  const item = response.items.find((candidate) => candidate.id === account.id)
  assert(item, '账户基础列表应返回新建账户')

  const forbiddenFields = [
    'credentials',
    'supportedModels',
    'modelMappings',
    'apiKeyRuntimeDetails',
    'usage',
    'oauthUsage',
    'authorizationSources',
    'authorizationCount',
    'authorizationTeamCount'
  ] as const
  for (const field of forbiddenFields) {
    assert.equal(Object.prototype.hasOwnProperty.call(item, field), false, `账户基础列表不应返回重量字段 ${field}`)
  }
  assert.equal(Object.prototype.hasOwnProperty.call(item, 'todayUsage'), true, '今日用量必须随列表一次返回')
  assert.equal(Object.prototype.hasOwnProperty.call(item, 'currentConcurrency'), true, '当前并发必须随列表一次返回')
  assert.equal(Object.prototype.hasOwnProperty.call(item, 'lastUsedAt'), true, '最近使用时间必须随列表一次返回')

  assert.equal(item.name, '列表严格投影账户')
  assert.equal(item.providerCode, 'gpt')
  assert.equal(item.qualityScore, 1234, '基础列表必须保留轻量质量快照字段')
  assert.equal(item.permissions?.canEdit, true, '基础列表必须保留行操作权限')

  console.log('AI 账户列表投影回归通过：动态字段与轻量静态字段均在列表一次返回')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedAccountUsage(accountId: string): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id,
        request_count, input_tokens, output_tokens, total_cost_usd,
        last_used_at, updated_at
      ) VALUES (?, 'account', ?, 9, 120, 80, 0.01, ?, ?)
    `)
    .run('sys_admin', accountId, now, now)
}

function seedAccountQuality(accountId: string): void {
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO account_quality_scores (
      account_id, system_account_id, provider_code, quality_score, quality_state,
      recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
      recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
      window_started_at, window_ended_at, updated_at
    ) VALUES (?, 'sys_admin', 'gpt', 1234, 'fresh', 1, 1, 0, 1, 1234, 1234, 1, ?, ?, ?)
  `).run(accountId, now, now, now)
}
