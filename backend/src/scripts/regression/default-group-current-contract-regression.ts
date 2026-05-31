import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-default-group-current-contract-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'default-group.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'default-group-current-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  defaultGroupRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/default-group.repository.js')
])

try {
  const database = databaseModule.getBusinessDatabase()
  const now = new Date().toISOString()
  const userId = 'sys_default_contract_user'
  insertSystemAccount(database, userId, 'default_contract_user', now)
  database
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 0, ?, ?)')
    .run('grp_name_only_default', userId, '默认 OpenAI 分组', 'openai', '', now, now)

  assert.equal(
    defaultGroupRepository.defaultOpenAIGroupIdForSystemAccount(userId),
    undefined,
    '默认 OpenAI 分组名称不能作为默认分组判定依据'
  )
  assert.equal(
    defaultGroupRepository.defaultGroupIdForSystemAccount('openai', userId),
    undefined,
    'OpenAI 默认分组必须只认 is_default'
  )

  database
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)')
    .run('grp_marked_default', userId, '显式默认分组', 'openai', '', now, now)
  assert.equal(defaultGroupRepository.defaultOpenAIGroupIdForSystemAccount(userId), 'grp_marked_default')
  assert.equal(defaultGroupRepository.defaultGroupIdForSystemAccount('openai', userId), 'grp_marked_default')

  const missingDefaultUserId = 'sys_default_contract_missing'
  insertSystemAccount(database, missingDefaultUserId, 'default_contract_missing', now)
  defaultGroupRepository.ensureDefaultOpenAIGroupForSystemAccount(missingDefaultUserId, now)
  const createdDefault = database
    .prepare("SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'openai' AND is_default = 1 LIMIT 1")
    .get(missingDefaultUserId) as { id?: string } | undefined
  assert(createdDefault?.id, '缺失默认 OpenAI 分组时应创建 is_default = 1 的当前默认分组')
  assert.equal(defaultGroupRepository.defaultOpenAIGroupIdForSystemAccount(missingDefaultUserId), createdDefault.id)

  console.log('默认分组当前契约回归通过：默认分组只认 is_default，不按名称或最新分组推断')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function insertSystemAccount(database: ReturnType<typeof databaseModule.getBusinessDatabase>, id: string, username: string, now: string): void {
  database
    .prepare(`
      INSERT INTO system_accounts (
        id, username, display_name, description, role, status,
        password_hash, must_change_password, image_generation_enabled,
        created_at, updated_at
      )
      VALUES (?, ?, ?, '', 'user', 'active', 'hash', 0, 0, ?, ?)
    `)
    .run(id, username, username, now, now)
}
