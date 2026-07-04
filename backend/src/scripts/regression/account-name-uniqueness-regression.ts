import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
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
const otherProviderCode = OPENAI_COMPATIBLE_PROVIDER_CODE
const otherProviderProfileId = OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
const duplicateName = '用户维度唯一账户'
const renameConflictName = '授权同步冲突名'

try {
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
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: duplicateName,
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-unique-openai', base_url: 'https://api.openai.com/v1' },
    groupId: openaiGroup.id
  }, access)

  const caseVariantUpper = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'TorchAI',
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-case-upper', base_url: 'https://api.openai.com/v1' },
    groupId: openaiGroup.id
  }, access)
  const caseVariantLower = repositories.createAccount({
    providerCode: otherProviderCode,
    providerProtocolProfileId: otherProviderProfileId,
    name: 'torchai',
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-case-lower', base_url: 'https://other-provider.example.com/v1' },
    groupId: otherProviderGroup.id
  }, access)
  assert.equal(caseVariantUpper.name, 'TorchAI', '同一用户下账户名称应区分大小写保留 TorchAI')
  assert.equal(caseVariantLower.name, 'torchai', '同一用户下账户名称应区分大小写允许 torchai')
  assert.throws(
    () => repositories.createAccount({
      providerCode: otherProviderCode,
      providerProtocolProfileId: otherProviderProfileId,
      name: 'TorchAI',
      type: 'api_key',
      credentials: { api_key: 'sk-account-name-case-exact-duplicate', base_url: 'https://other-provider.example.com/v1' },
      groupId: otherProviderGroup.id
    }, access),
    /同一用户下账户名称已存在/,
    '同一用户下完全相同的账户名称仍应由唯一索引拒绝'
  )

  assert.throws(
    () => repositories.createAccount({
      providerCode: otherProviderCode,
      providerProtocolProfileId: otherProviderProfileId,
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
    providerProtocolProfileId: otherProviderProfileId,
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

  repositories.createAccount({
    providerCode: otherProviderCode,
    providerProtocolProfileId: otherProviderProfileId,
    name: 'torchai',
    type: 'api_key',
    credentials: { api_key: 'sk-account-name-unique-grantee-lower-case', base_url: 'https://other-provider.example.com/v1' },
    groupId: granteeOtherProviderGroup.id
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: caseVariantUpper.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeOpenAIGroup.id,
    remark: '账户名称大小写授权实例回归'
  }, access)
  const caseVariantAuthorizedInstance = repositories.listAccounts(granteeAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === caseVariantUpper.id)
  assert.equal(caseVariantAuthorizedInstance?.name, 'TorchAI', '授权实例名称避让应区分大小写，不应被 grantee 的 torchai 触发后缀')

  const sameNameForOtherUser = repositories.createAccount({
    providerCode: otherProviderCode,
    providerProtocolProfileId: otherProviderProfileId,
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
    providerProtocolProfileId: otherProviderProfileId,
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
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupName: openaiGroup.name,
        credentials: { api_key: 'sk-import-account-name-unique-openai', base_url: 'https://api.openai.com/v1' }
      },
      {
        name: '导入用户维度重复账户',
        providerCode: otherProviderCode,
        providerProtocolProfileId: otherProviderProfileId,
        type: 'api_key',
        status: 'active',
        groupName: otherProviderGroup.name,
        credentials: { api_key: 'sk-import-account-name-unique-other', base_url: 'https://other-provider.example.com/v1' }
      }
    ]
  }, { skipDuplicates: true }, access)
  assert.equal(preview.accounts[0]?.action, 'create', '导入批内第一条同名账户应允许创建')
  assert.equal(preview.accounts[1]?.action, 'skip', '导入批内跨供应商同名账户应按用户维度跳过')

  const caseVariantPreview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入CaseSensitive',
        providerCode: 'gpt',
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupName: openaiGroup.name,
        credentials: { api_key: 'sk-import-account-name-case-upper', base_url: 'https://api.openai.com/v1' }
      },
      {
        name: '导入casesensitive',
        providerCode: otherProviderCode,
        providerProtocolProfileId: otherProviderProfileId,
        type: 'api_key',
        status: 'active',
        groupName: otherProviderGroup.name,
        credentials: { api_key: 'sk-import-account-name-case-lower', base_url: 'https://other-provider.example.com/v1' }
      }
    ]
  }, { skipDuplicates: true }, access)
  assert.equal(caseVariantPreview.accounts[0]?.action, 'create', '导入批内大小写不同的第一条账户应允许创建')
  assert.equal(caseVariantPreview.accounts[1]?.action, 'create', '导入批内大小写不同的第二条账户也应允许创建')

  const existingDuplicateImport = accountImport.executeAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: renameConflictName,
        providerCode: otherProviderCode,
        providerProtocolProfileId: otherProviderProfileId,
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
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupName: openaiGroup.name,
        credentials: { api_key: 'sk-import-account-name-failed-openai', base_url: 'https://api.openai.com/v1' }
      },
      {
        name: '导入用户维度失败账户',
        providerCode: otherProviderCode,
        providerProtocolProfileId: otherProviderProfileId,
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
  assert(accountIndexes.some((item) => item.name === 'idx_accounts_owner_name_unique' && item.unique === 1), 'accounts 表应创建用户维度大小写敏感账户名称唯一索引')
  assert.equal(accountIndexes.some((item) => item.name === 'idx_accounts_owner_name_unique_lower'), false, '新 schema 不应继续创建 lower(name) 账户名称唯一索引')
  assert.equal(accountIndexes.some((item) => item.name === 'idx_accounts_owner_provider_name_unique_lower'), false, '新 schema 不应继续创建供应商维度账户名称唯一索引')

  console.log('AI 账户名称唯一回归通过：账户名称按用户维度大小写敏感全局唯一')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

