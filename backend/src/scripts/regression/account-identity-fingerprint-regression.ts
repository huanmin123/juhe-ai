import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-identity-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-identity-fingerprint-secret'
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
    name: '账户身份指纹回归分组',
    providerCode: 'openai'
  }, access)

  const first = repositories.createAccount({
    providerCode: 'openai',
    name: '固定 Key 站点 A',
    type: 'api_key',
    credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://site-a.example.com/v1' },
    groupId: group.id
  }, access)

  const second = repositories.createAccount({
    providerCode: 'openai',
    name: '固定 Key 站点 B',
    type: 'api_key',
    credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://site-b.example.com/openai/v1/' },
    groupId: group.id
  }, access)

  assert.notEqual(first.id, second.id, '相同 API Key 在不同上游域名下应允许创建两个 AI 账户')

  const preview = accountImport.previewAccountImport({
    type: accountImport.accountImportProtocolType,
    version: accountImport.accountImportProtocolVersion,
    accounts: [
      {
        name: '导入固定 Key 站点 C',
        providerCode: 'openai',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-fixed-key', base_url: 'https://site-c.example.com/v1' }
      },
      {
        name: '导入固定 Key 站点 D',
        providerCode: 'openai',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-fixed-key', base_url: 'https://site-d.example.com/openai/v1/' }
      },
      {
        name: '导入固定 Key 站点 C 路径变体',
        providerCode: 'openai',
        type: 'api_key',
        status: 'active',
        groupName: group.name,
        credentials: { api_key: 'sk-import-fixed-key', base_url: 'https://SITE-C.example.com/compat/v1/' }
      }
    ]
  }, { skipDuplicates: true }, access)
  assert.equal(preview.accounts[0]?.action, 'create', '导入预览中相同 Key 不同域名第一条应允许创建')
  assert.equal(preview.accounts[1]?.action, 'create', '导入预览中相同 Key 不同域名第二条应允许创建')
  assert.equal(preview.accounts[2]?.action, 'skip', '导入预览中同域名路径变体应按重复账户跳过')

  const rows = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT credential_fingerprint, account_identity_fingerprint
      FROM accounts
      WHERE id IN (?, ?)
      ORDER BY name ASC
    `)
    .all(first.id, second.id) as Array<{ credential_fingerprint: string; account_identity_fingerprint: string }>
  assert.equal(rows.length, 2, '回归账户应写入账户表')
  assert.equal(rows[0]?.credential_fingerprint, rows[1]?.credential_fingerprint, '相同 API Key 应保留相同凭据指纹，便于排查重复凭据')
  assert.notEqual(rows[0]?.account_identity_fingerprint, rows[1]?.account_identity_fingerprint, '不同域名应生成不同账户身份指纹')

  assert.throws(
    () => repositories.createAccount({
      providerCode: 'openai',
      name: '固定 Key 站点 A 路径变体',
      type: 'api_key',
      credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://SITE-A.example.com/openai/v1/' },
      groupId: group.id
    }, access),
    repositories.DuplicateAccountCredentialError,
    '同一域名下不同路径或大小写写法仍应按同一上游账户判重'
  )

  assert.throws(
    () => repositories.updateAccount(second.id, {
      credentials: { api_key: 'sk-fixed-upstream-key', base_url: 'https://site-a.example.com' }
    }, access),
    repositories.DuplicateAccountCredentialError,
    '编辑账户时也应阻止改成已有的同域名同凭据身份'
  )

  console.log('AI 账户身份指纹回归通过：同 Key 不同域名可共存，同域名路径变体仍判重')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
