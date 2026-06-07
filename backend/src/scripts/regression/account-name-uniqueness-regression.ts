import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-name-uniqueness-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-name-uniqueness-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, accountImport] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-import.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const otherProviderCode = 'account-name-unique-provider'
const otherProviderProfileId = 'profile_account_name_unique_openai_v1'
const duplicateName = '用户维度唯一账户'
const renameConflictName = '授权同步冲突名'

try {
  seedTestProvider()

  const openaiGroup = repositories.createGroup({
    name: '账户名称唯一 GPT 分组',
    providerCode: 'gpt'
  }, access)
  const otherProviderGroup = repositories.createGroup({
    name: '账户名称唯一测试供应商分组',
    providerCode: otherProviderCode
  }, access)

  const primary = repositories.createAccount({
    providerCode: 'gpt',
    name: duplicateName,
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-unique-openai', base_url: 'https://api.openai.com/v1' },
    groupId: openaiGroup.id
  }, access)

  assert.throws(
    () => repositories.createAccount({
      providerCode: otherProviderCode,
      name: duplicateName,
      type: 'api_key',
      credentials: { api_key: 'sk-account-name-unique-other', base_url: 'https://other-provider.example.com/v1' },
      groupId: otherProviderGroup.id
    }, access),
    /同一用户下账户名称已存在/,
    '同一用户下跨供应商同名账户应拒绝创建'
  )

  const otherProviderAccount = repositories.createAccount({
    providerCode: otherProviderCode,
    name: '测试供应商可改名账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-unique-update', base_url: 'https://other-provider.example.com/v1' },
    groupId: otherProviderGroup.id
  }, access)
  assert.throws(
    () => repositories.updateAccount(otherProviderAccount.id, { name: duplicateName }, access),
    /同一用户下账户名称已存在/,
    '同一用户下跨供应商同名账户应拒绝编辑'
  )

  const grantee = repositories.createSystemAccount({
    username: 'account_name_unique_grantee',
    displayName: '账户名称唯一被授权人',
    password: 'Password-123456',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeOpenAIGroup = repositories.createGroup({
    name: '账户名称唯一被授权 GPT 分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const granteeOtherProviderGroup = repositories.createGroup({
    name: '账户名称唯一被授权测试供应商分组',
    providerCode: otherProviderCode
  }, granteeAccess)

  const sameNameForOtherUser = repositories.createAccount({
    providerCode: otherProviderCode,
    name: duplicateName,
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-unique-grantee-other', base_url: 'https://other-provider.example.com/v1' },
    groupId: granteeOtherProviderGroup.id
  }, granteeAccess)
  assert.equal(sameNameForOtherUser.name, duplicateName, '不同用户之间仍允许使用相同账户名称')

  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: primary.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeOpenAIGroup.id,
    remark: '账户名称唯一授权实例回归'
  }, access)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === primary.id)
  assert(authorizedInstance?.id, '账户授权应创建授权实例')
  assert.notEqual(authorizedInstance.name, duplicateName, '授权实例应避让被授权用户跨供应商已有同名账户')
  assert(authorizedInstance.name.startsWith(`${duplicateName}-`), '授权实例冲突名称应追加授权 ID 后缀')

  repositories.createAccount({
    providerCode: otherProviderCode,
    name: renameConflictName,
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-unique-grantee-rename', base_url: 'https://other-provider.example.com/v1' },
    groupId: granteeOtherProviderGroup.id
  }, granteeAccess)
  repositories.updateAccount(primary.id, { name: renameConflictName }, access)
  const renamedAuthorizedInstance = repositories.listAccounts(granteeAccess)
    .find((account) => account.id === authorizedInstance.id)
  assert(renamedAuthorizedInstance?.name.startsWith(`${renameConflictName}-`), '来源账户改名时授权实例应继续避让被授权用户跨供应商同名账户')
  assert.notEqual(renamedAuthorizedInstance?.name, renameConflictName, '授权实例同步名称不能覆盖被授权用户已有账户名称')

  const preview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入用户维度重复账户',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName: openaiGroup.name,
        credentials: { api_key: 'sk-import-account-name-unique-openai', base_url: 'https://api.openai.com/v1' }
      },
      {
        name: '导入用户维度重复账户',
        providerCode: otherProviderCode,
        type: 'api_key',
        status: 'active',
        groupName: otherProviderGroup.name,
        credentials: { api_key: 'sk-import-account-name-unique-other', base_url: 'https://other-provider.example.com/v1' }
      }
    ]
  }, { skipDuplicates: true }, access)
  assert.equal(preview.accounts[0]?.action, 'create', '导入批内第一条同名账户应允许创建')
  assert.equal(preview.accounts[1]?.action, 'skip', '导入批内跨供应商同名账户应按用户维度跳过')

  const existingDuplicateImport = accountImport.executeAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: renameConflictName,
        providerCode: otherProviderCode,
        type: 'api_key',
        status: 'active',
        groupName: otherProviderGroup.name,
        credentials: { api_key: 'sk-import-existing-account-name-unique-other', base_url: 'https://other-provider.example.com/v1' }
      }
    ]
  }, { skipDuplicates: true }, access)
  assert.equal(existingDuplicateImport.summary.accounts.skip, 1, '导入执行遇到已存在的跨供应商同名账户应按用户维度跳过')
  assert.match(existingDuplicateImport.accounts[0]?.messages[0] ?? '', /同一用户下账户名称已存在/, '导入执行跳过时应返回用户维度重复名称提示')

  const failedPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入用户维度失败账户',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName: openaiGroup.name,
        credentials: { api_key: 'sk-import-account-name-failed-openai', base_url: 'https://api.openai.com/v1' }
      },
      {
        name: '导入用户维度失败账户',
        providerCode: otherProviderCode,
        type: 'api_key',
        status: 'active',
        groupName: otherProviderGroup.name,
        credentials: { api_key: 'sk-import-account-name-failed-other', base_url: 'https://other-provider.example.com/v1' }
      }
    ]
  }, { skipDuplicates: false }, access)
  assert.equal(failedPreview.accounts[1]?.action, 'failed', '关闭跳过重复后，导入批内跨供应商同名账户应失败')

  const accountIndexes = databaseModule.getBusinessDatabase()
    .prepare("PRAGMA index_list('accounts')")
    .all() as Array<{ name: string; unique: number }>
  assert(accountIndexes.some((item) => item.name === 'idx_accounts_owner_name_unique_lower' && item.unique === 1), 'accounts 表应创建用户维度账户名称唯一索引')
  assert.equal(accountIndexes.some((item) => item.name === 'idx_accounts_owner_provider_name_unique_lower'), false, '新 schema 不应继续创建供应商维度账户名称唯一索引')

  console.log('AI 账户名称唯一回归通过：账户名称按用户维度全局唯一')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedTestProvider(): void {
  const now = new Date().toISOString()
  const database = databaseModule.getBusinessDatabase()
  database
    .prepare(`
      INSERT INTO providers (
        id, code, name, description, enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 1, ?, ?)
    `)
    .run(
      'provider_account_name_unique',
      otherProviderCode,
      '账户名称唯一测试供应商',
      '仅用于账号名称唯一回归',
      now,
      now
    )
  database
    .prepare(`
      INSERT INTO provider_protocol_profiles (
        id, provider_code, name, description, enabled, protocol_code, protocol_version,
        base_url, default_test_model, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 1, 'openai', 'v1', ?, ?, ?, ?, ?, ?)
    `)
    .run(
      otherProviderProfileId,
      otherProviderCode,
      '账户名称唯一测试供应商 / OpenAI v1',
      '仅用于账号名称唯一回归的 OpenAI v1 协议档案',
      'https://other-provider.example.com/v1',
      'test-model',
      JSON.stringify(['api_key', 'oauth']),
      JSON.stringify(['chat']),
      now,
      now
    )
  const familyStatement = database.prepare(`
    INSERT INTO provider_protocol_profile_families (
      profile_id, family_code, enabled, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, 1, '[]', ?, ?)
  `)
  familyStatement.run(otherProviderProfileId, 'chat_completions', now, now)
  familyStatement.run(otherProviderProfileId, 'responses', now, now)
}
