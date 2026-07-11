import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-priority-contract-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-priority-contract.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-priority-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

function createRegressionAccount(
  input: Record<string, unknown>,
  scope = access
) {
  const supportedModels = Array.isArray(input.supportedModels)
    ? input.supportedModels
    : ['gpt-5.5']
  const normalized = input.providerCode === 'gpt'
    ? {
        ...input,
        providerProtocolProfileId: input.providerProtocolProfileId ?? GPT_OPENAI_V1_PROFILE_ID,
        supportedModels,
        healthCheckModel: input.healthCheckModel ?? supportedModels[0]
      }
    : input
  return repositories.createAccount(
    normalized as Parameters<typeof repositories.createAccount>[0],
    scope
  )
}

try {
  const accountGroup = repositories.createGroup({
    providerCode: 'gpt',
    name: '账户字段契约回归分组'
  }, access)
  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: '优先级拼写残留创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-create',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id,
    prioritiy: 99
  }, access), /账户创建参数包含未知字段：prioritiy/, '拼错字段 prioritiy 不应在创建账户时被静默忽略')

  const account = createRegressionAccount({
    providerCode: 'gpt',
    name: '优先级拼写残留更新检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-update',
      base_url: 'https://api.openai.com/v1'
    },
    priority: 7,
    groupId: accountGroup.id
  }, access)
  assert.equal(account.priority, 7, '当前 priority 字段应正常生效')

  assert.throws(() => repositories.updateAccount(account.id, { prioritiy: 66 }, access), /账户更新参数包含未知字段：prioritiy/, '拼错字段 prioritiy 不应在更新账户时被静默忽略')

  const updated = repositories.updateAccount(account.id, { priority: 8 }, access)
  assert.equal(updated?.priority, 8, '当前 priority 字段应仍可更新优先级')

  assert.throws(() => createRegressionAccount({
    name: '缺失供应商创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-missing-provider',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access), /供应商不能为空/, '创建账户必须显式提供当前供应商')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-missing-name',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access), /账户名称不能为空/, '创建账户必须显式提供当前账户名称')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: '缺失类型创建检查',
    credentials: {
      api_key: 'sk-priority-contract-missing-type',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access), /账户类型不能为空/, '创建账户必须显式提供当前账户类型')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: '字符串并发创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-string-concurrency',
      base_url: 'https://api.openai.com/v1'
    },
    concurrencyLimit: '20',
    groupId: accountGroup.id
  }, access), /并发限制必须是大于 0 的整数/, '创建账户不应接收数字字符串形式的并发限制')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: '缺失 Base URL 创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-missing-base-url'
    },
    groupId: accountGroup.id
  }, access), /Base URL不能为空/, '创建账户不应为 API Key 账户补默认 Base URL')

  assert.throws(() => repositories.updateAccount(account.id, {
    credentials: {
      api_key: 'sk-priority-contract-update-missing-base-url'
    }
  }, access), /Base URL不能为空/, '更新账户不应为凭据补默认 Base URL')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: '凭据旧字段创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-unknown-credential',
      base_url: 'https://api.openai.com/v1',
      apiKey: 'legacy-field'
    },
    groupId: accountGroup.id
  }, access), /账户凭据包含不支持的字段：apiKey/, '创建账户不应静默保留 credentials 内的旧字段')

  assert.throws(() => repositories.updateAccount(account.id, {
    credentials: {
      api_key: 'sk-priority-contract-update-unknown-credential',
      base_url: 'https://api.openai.com/v1',
      legacyToken: 'old-token'
    }
  }, access), /账户凭据包含不支持的字段：legacyToken/, '更新账户不应静默保留 credentials 内的旧字段')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: '超长 API Key 创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'x'.repeat(16 * 1024 + 1),
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access), /API Key不能超过 16384 字节/, '创建账户不应接收超长 API Key 凭据')

  const oauthAccount = createRegressionAccount({
    providerCode: 'gpt',
    name: 'OAuth 凭据当前字段检查',
    type: 'oauth',
    credentials: {
      access_token: 'access-priority-contract-oauth',
      refresh_token: 'refresh-priority-contract-oauth',
      expires_at: '2099-01-01T00:00:00.000Z',
      client_id: 'client-priority-contract',
      id_token: 'id-token-priority-contract',
      email: 'priority-contract@example.com',
      account_id: 'acct_priority_contract',
      chatgpt_user_id: 'user_priority_contract',
      plan_type: 'plus',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access)
  assert.equal(oauthAccount.credentials.client_id, 'client-priority-contract', '当前 OAuth 元数据字段应正常保留')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: 'OAuth 凭据旧字段检查',
    type: 'oauth',
    credentials: {
      refresh_token: 'refresh-priority-contract-legacy-oauth',
      base_url: 'https://api.openai.com/v1',
      accountId: 'legacy-account-id'
    },
    groupId: accountGroup.id
  }, access), /账户凭据包含不支持的字段：accountId/, 'OAuth 凭据不应接收 camelCase 旧字段')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: 'OAuth 凭据非法时间检查',
    type: 'oauth',
    credentials: {
      refresh_token: 'refresh-priority-contract-invalid-expiry',
      expires_at: 'not-a-date',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access), /Access Token 到期时间必须是有效时间字符串/, 'OAuth 凭据时间字段不应吞掉非法字符串')

  assert.throws(() => createRegressionAccount({
    providerCode: 'gpt',
    name: 'OAuth 凭据整体大小检查',
    type: 'oauth',
    credentials: {
      access_token: 'a'.repeat(16 * 1024),
      refresh_token: 'r'.repeat(16 * 1024),
      id_token: 'i'.repeat(16 * 1024),
      base_url: 'https://api.openai.com/v1'
    },
    groupId: accountGroup.id
  }, access), /账户凭据整体大小不能超过 32768 字节/, '创建账户不应接收整体过大的 OAuth 凭据')

  assert.throws(() => repositories.createGroup({
    name: '缺失供应商分组检查'
  }, access), /供应商不能为空/, '创建分组必须显式提供当前供应商')

  assert.throws(() => repositories.createGroup({
    providerCode: 'gpt'
  }, access), /分组名称不能为空/, '创建分组必须显式提供当前分组名称')

  assert.throws(() => repositories.updateAccount(account.id, { status: 'archived' }, access), /账户状态无效/, '显式传入非法状态应被拒绝')
  assert.throws(() => repositories.updateAccount(account.id, { concurrencyLimit: '20' }, access), /并发限制必须是大于 0 的整数/, '更新账户不应接收数字字符串形式的并发限制')
  assert.throws(() => repositories.updateAccount(account.id, { concurrencyLimit: 20.5 }, access), /并发限制必须是大于 0 的整数/, '更新账户不应截断小数形式的并发限制')
  assert.throws(() => repositories.updateAccount(account.id, { priority: '8' }, access), /优先级必须是大于等于 0 的整数/, '更新账户不应接收数字字符串形式的优先级')
  assert.throws(() => repositories.updateAccount(account.id, { priority: 8.5 }, access), /优先级必须是大于等于 0 的整数/, '更新账户不应截断小数形式的优先级')
  assert.throws(() => repositories.updateAccount(account.id, { superPriorityEnabled: '1' }, access), /超级优先必须是布尔值/, '更新账户不应接收 1 字符串形式的布尔调度字段')
  assert.throws(() => repositories.createGroup({
    providerCode: 'gpt',
    name: '分组未知字段检查',
    legacyName: '旧字段'
  }, access), /分组创建参数包含未知字段：legacyName/, '分组创建不应静默忽略未知字段')
  const groupForUnknownField = repositories.createGroup({
    providerCode: 'gpt',
    name: '分组未知字段更新检查'
  }, access)
  assert.throws(() => repositories.updateGroup(groupForUnknownField.id, { legacyDescription: '旧字段' }, access), /分组更新参数包含未知字段：legacyDescription/, '分组更新不应静默忽略未知字段')

  console.log('账户字段契约回归通过：拼错字段、历史宽松输入和超长凭据被直接拒绝')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
