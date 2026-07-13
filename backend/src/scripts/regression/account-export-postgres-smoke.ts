import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  createProxyAsync,
  deleteAccountAsync,
  deleteGroupAsync,
  deleteProxyAsync
} from '../../storage/repositories.js'
import { exportAccountsForRequestAsync } from '../../modules/accounts/account-export-request.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户导出 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
const createdProxyIds: string[] = []
const tagName = `export-pg-${marker}`

try {
  const group = await createGroupAsync({
    name: `账户导出 PG smoke 分组 ${marker}`,
    providerCode: 'gpt'
  }, access)
  createdGroupIds.push(group.id)

  const proxy = await createProxyAsync({
    name: `账户导出 PG smoke 代理 ${marker}`,
    type: 'http',
    host: '127.0.0.1',
    port: 18_081,
    username: 'export-user',
    password: 'export-pass',
    enabled: true
  }, access)
  createdProxyIds.push(proxy.id)

  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `账户导出 PG smoke 账户 ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-export-pg-smoke-${marker}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    proxyProfileId: proxy.id,
    tags: [tagName],
    supportedModels: ['gpt-4o-mini'],
    status: 'disabled'
  }, access)
  createdAccountIds.push(account.id)

  const byId = await exportAccountsForRequestAsync({ accountIds: [account.id] }, access)
  const exported = byId.document.accounts[0]
  assert(exported, 'PG 按 ID 导出应返回账号')
  assert.equal(exported.ref, account.id, 'PG 按 ID 导出应保留账号 ID ref')
  assert.equal(exported.credentials.api_key, `sk-account-export-pg-smoke-${marker}`, 'PG 导出应读取账号密钥凭据')
  assert.deepEqual(exported.credentials.supported_endpoint_modes, ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'], 'PG 导出应保留上游接口能力')
  assert.deepEqual(exported.tags, [tagName], 'PG 导出应保留标签')
  assert.deepEqual(exported.supportedModels, ['gpt-4o-mini'], 'PG 导出应保留支持模型')
  assert.equal(exported.groupName, group.name, 'PG 导出应优先输出分组名称')
  assert.equal(exported.proxyRef, `proxy-${proxy.id}`, 'PG 导出应输出代理引用')
  assert.equal(byId.document.proxies?.[0]?.password, 'export-pass', 'PG 导出应通过 async 代理读取保留代理密码')
  assert.equal(byId.summary.accounts, 1, 'PG 按 ID 导出 summary 应统计导出账号数')

  const byFilter = await exportAccountsForRequestAsync({
    filters: {
      keyword: `账户导出 PG smoke 账户 ${marker}`
    }
  }, access)
  assert.equal(byFilter.summary.accounts, 1, 'PG 按筛选导出应返回匹配账号')
  assert.equal(byFilter.summary.matchedAccounts, 1, 'PG 按筛选导出应回填匹配总数')
  assert.equal(byFilter.document.accounts[0]?.ref, account.id, 'PG 按筛选导出应走 async 列表后回填账号凭据')

  console.log(JSON.stringify({
    message: '账户导出 PG smoke 通过',
    accountId: account.id,
    proxyExported: Boolean(byId.document.proxies?.length)
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  for (const accountId of createdAccountIds) {
    await deleteAccountAsync(accountId, access).catch(() => false)
  }
  for (const proxyId of createdProxyIds) {
    await deleteProxyAsync(proxyId).catch(() => false)
  }
  for (const groupId of createdGroupIds) {
    await deleteGroupAsync(groupId, access).catch(() => undefined)
  }

  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.proxy_profiles WHERE id = ANY($1::text[])', [createdProxyIds])
  await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  await pool.query('DELETE FROM juhe_business.account_tags WHERE system_account_id = $1 AND name = $2', [access.systemAccountId, tagName])
}
