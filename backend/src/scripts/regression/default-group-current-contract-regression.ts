import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
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
    .run('grp_name_only_default', userId, '默认 GPT 分组', GPT_VENDOR_CODE, '', now, now)

  assert.equal(
    defaultGroupRepository.defaultGptGroupIdForSystemAccount(userId),
    undefined,
    '默认 GPT 分组名称不能作为默认分组判定依据'
  )
  assert.equal(
    defaultGroupRepository.defaultGroupIdForProviderCode(GPT_VENDOR_CODE, userId),
    undefined,
    'GPT 默认分组必须只认供应商和 is_default'
  )

  database
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)')
    .run('grp_marked_default', userId, '显式默认分组', GPT_VENDOR_CODE, '', now, now)
  assert.equal(defaultGroupRepository.defaultGptGroupIdForSystemAccount(userId), 'grp_marked_default')
  assert.equal(defaultGroupRepository.defaultGroupIdForProviderCode(GPT_VENDOR_CODE, userId), 'grp_marked_default')

  const missingDefaultUserId = 'sys_default_contract_missing'
  insertSystemAccount(database, missingDefaultUserId, 'default_contract_missing', now)
  defaultGroupRepository.ensureDefaultBuiltInGroupsForSystemAccount(missingDefaultUserId, now)
  const createdDefault = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 LIMIT 1')
    .get(missingDefaultUserId, GPT_VENDOR_CODE) as { id?: string } | undefined
  const createdOpenAICompatibleDefault = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 LIMIT 1')
    .get(missingDefaultUserId, OPENAI_COMPATIBLE_PROVIDER_CODE) as { id?: string } | undefined
  const createdDeepSeekDefault = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 LIMIT 1')
    .get(missingDefaultUserId, DEEPSEEK_PROVIDER_CODE) as { id?: string } | undefined
  const createdGlmDefault = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 LIMIT 1')
    .get(missingDefaultUserId, GLM_PROVIDER_CODE) as { id?: string } | undefined
  assert(createdDefault?.id, '缺失默认 GPT 分组时应创建 is_default = 1 的当前默认分组')
  assert(createdOpenAICompatibleDefault?.id, '缺失默认 OpenAI 兼容分组时应创建 is_default = 1 的当前默认分组')
  assert(createdDeepSeekDefault?.id, '缺失默认 DeepSeek 分组时应创建 is_default = 1 的当前默认分组')
  assert(createdGlmDefault?.id, '缺失默认 GLM 分组时应创建 is_default = 1 的当前默认分组')
  assert.equal(defaultGroupRepository.defaultGptGroupIdForSystemAccount(missingDefaultUserId), createdDefault.id)
  assert.equal(defaultGroupRepository.defaultGroupIdForProviderCode(DEEPSEEK_PROVIDER_CODE, missingDefaultUserId), createdDeepSeekDefault.id, 'DeepSeek 默认分组按供应商复用')
  assert.equal(defaultGroupRepository.defaultGroupIdForProviderCode(GLM_PROVIDER_CODE, missingDefaultUserId), createdGlmDefault.id, 'GLM 默认分组按供应商复用')

  await assertGroupProviderCodeUsesProviderLayer()

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

async function assertGroupProviderCodeUsesProviderLayer(): Promise<void> {
  const repositories = await import('../../storage/repositories.js')
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const openAICompatibleGroup = repositories.createGroup({
    name: 'OpenAI 兼容供应商分组回归',
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE
  }, access)
  assert.equal(openAICompatibleGroup.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, 'openai 可以作为通用 OpenAI-compatible 供应商编码')
  assert.equal('providerProtocolProfileId' in openAICompatibleGroup, false, '分组摘要不应返回协议档案字段')

  const group = repositories.createGroup({
    name: 'GPT 供应商分组回归',
    providerCode: GPT_VENDOR_CODE
  }, access)
  assert.equal(group.providerCode, GPT_VENDOR_CODE, 'GPT 分组创建应落在 GPT 子供应商层')
  assert.equal('protocolCode' in group, false, '分组摘要不应返回协议代码')

  const deepSeekGroup = repositories.createGroup({
    name: 'DeepSeek 供应商分组回归',
    providerCode: DEEPSEEK_PROVIDER_CODE
  }, access)
  assert.equal(deepSeekGroup.providerCode, DEEPSEEK_PROVIDER_CODE, 'DeepSeek 分组创建应落在 DeepSeek 独立供应商层')

  const glmGroup = repositories.createGroup({
    name: 'GLM 供应商分组回归',
    providerCode: GLM_PROVIDER_CODE
  }, access)
  assert.equal(glmGroup.providerCode, GLM_PROVIDER_CODE, 'GLM 分组创建应落在 GLM 独立供应商层')
  assert.throws(
    () => repositories.createGroup({
      name: '分组协议档案禁用回归',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID
    }, access),
    /分组创建参数包含未知字段/,
    '分组创建不应接受协议档案字段'
  )

  const moved = repositories.updateGroup(group.id, {
    providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE
  }, access)
  assert.equal(moved?.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, '无账号分组可以改到 openai 通用供应商')
  assert.equal(moved && 'providerProtocolProfileId' in moved, false, '分组更新后不应返回协议档案字段')
}
