import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-credential-duplicates-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-credential-duplicates-secret'
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

try {
  const group = repositories.createGroup({
    name: '账户凭据重复回归分组',
    providerCode: 'gpt'
  }, access)

  const apiKeyPrimary = repositories.createAccount({
    providerCode: 'gpt',
    name: '固定 Key 站点 A 主账户',
    type: 'api_key',
    credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://site-a.example.com/v1' },
    groupId: group.id
  }, access)

  const apiKeyDuplicate = repositories.createAccount({
    providerCode: 'gpt',
    name: '固定 Key 站点 A 重复账户',
    type: 'api_key',
    credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://SITE-A.example.com/openai/v1/' },
    groupId: group.id
  }, access)

  const apiKeyDifferentHost = repositories.createAccount({
    providerCode: 'gpt',
    name: '固定 Key 站点 B',
    type: 'api_key',
    credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://site-b.example.com/openai/v1/' },
    groupId: group.id
  }, access)

  const oauthPrimary = repositories.createAccount({
    providerCode: 'gpt',
    name: 'OAuth Refresh Token 主账户',
    type: 'oauth',
    credentials: { refresh_token: 'refresh-duplicate-oauth-token', access_token: 'access-duplicate-oauth-token-1', base_url: 'https://chatgpt.com/backend-api/codex' },
    groupId: group.id
  }, access)

  const oauthDuplicate = repositories.createAccount({
    providerCode: 'gpt',
    name: 'OAuth Refresh Token 重复账户',
    type: 'oauth',
    credentials: { refresh_token: 'refresh-duplicate-oauth-token', access_token: 'access-duplicate-oauth-token-2', base_url: 'https://chatgpt.com/backend-api/codex' },
    groupId: group.id
  }, access)

  assert.notEqual(apiKeyPrimary.id, apiKeyDuplicate.id, '相同 API Key 和同一上游域名应允许创建两个 AI 账户')
  assert.notEqual(apiKeyPrimary.id, apiKeyDifferentHost.id, '相同 API Key 在不同上游域名下也应允许创建两个 AI 账户')
  assert.notEqual(oauthPrimary.id, oauthDuplicate.id, '相同 OAuth Refresh Token 和同一上游域名应允许创建两个 AI 账户')

  const preview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入固定 Key 站点 C',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-fixed-key', base_url: 'https://site-c.example.com/v1' }
      },
      {
        name: '导入固定 Key 站点 D',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-fixed-key', base_url: 'https://site-d.example.com/openai/v1/' }
      },
      {
        name: '导入固定 Key 站点 C 重复凭据',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-fixed-key', base_url: 'https://SITE-C.example.com/compat/v1/' }
      },
      {
        name: '导入固定 Key 站点 C',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-other-key', base_url: 'https://site-e.example.com/v1' }
      }
    ]
  }, { skipDuplicates: true }, access)
  assert.equal(preview.accounts[0]?.action, 'create', '导入预览中相同 Key 不同域名第一条应允许创建')
  assert.equal(preview.accounts[1]?.action, 'create', '导入预览中相同 Key 不同域名第二条应允许创建')
  assert.equal(preview.accounts[2]?.action, 'create', '导入预览中同域名同凭据也应允许创建')
  assert.equal(preview.accounts[3]?.action, 'skip', '导入预览仍应按同一用户下账户名称重复跳过')

  const updatedDifferentHost = repositories.updateAccount(apiKeyDifferentHost.id, {
    credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://site-a.example.com' }
  }, access)
  assert.equal(updatedDifferentHost?.id, apiKeyDifferentHost.id, '编辑账户时应允许改成已有的同域名同凭据身份')

  const rows = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, type, credential_fingerprint
      FROM accounts
      WHERE id IN (?, ?, ?, ?, ?)
      ORDER BY name ASC
    `)
    .all(apiKeyPrimary.id, apiKeyDuplicate.id, apiKeyDifferentHost.id, oauthPrimary.id, oauthDuplicate.id) as Array<{ id: string; type: string; credential_fingerprint: string }>
  assert.equal(rows.length, 5, '回归账户应写入账户表')
  const apiKeyRows = rows.filter((row) => row.type === 'api_key')
  const oauthRows = rows.filter((row) => row.type === 'oauth')
  assert.equal(new Set(apiKeyRows.map((row) => row.credential_fingerprint)).size, 1, '相同 API Key 应保留相同凭据指纹，便于排查重复凭据')
  assert.equal(new Set(oauthRows.map((row) => row.credential_fingerprint)).size, 1, '相同 OAuth Refresh Token 应保留相同凭据指纹')

  const columns = databaseModule.getBusinessDatabase()
    .prepare("PRAGMA table_info('accounts')")
    .all() as Array<{ name: string }>
  assert.deepEqual(
    columns.map((item) => item.name).filter((name) => name.includes('fingerprint')),
    ['credential_fingerprint'],
    'accounts 表只应保留凭据指纹字段'
  )

  const indexes = databaseModule.getBusinessDatabase()
    .prepare("PRAGMA index_list('accounts')")
    .all() as Array<{ name: string; unique: number }>
  assert.deepEqual(
    indexes.map((item) => item.name).filter((name) => name.includes('fingerprint')),
    ['idx_accounts_credential_fingerprint'],
    'accounts 表只应保留凭据指纹索引'
  )

  console.log('AI 账户凭据重复回归通过：API Key/OAuth 凭据允许重复，只保留凭据指纹排查索引')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
