import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import {
  filterGatewayAccountsByRequestedModel,
  gatewayModelFilterFailureMessage
} from '../../modules/gateway/openai-gateway-model-filter.js'
import { logger } from '../../shared/logger.js'
import type { UpstreamAccount } from '../../modules/gateway/openai-gateway-route-helpers.js'

function account(id: string, supportedModels?: string[]): UpstreamAccount {
  return {
    id,
    name: id,
    supportedModels,
    type: 'api_key',
    status: 'active',
    apiKey: 'sk-model-filter',
    baseUrl: 'https://api.openai.com/v1',
    credentials: {},
    systemAccountId: 'sys_model_filter',
    accountOwnerSystemAccountId: 'sys_model_filter',
    groupOwnerSystemAccountId: 'sys_model_filter',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    passthroughEnabled: true,
    streamFailureCount: 0
  } as UpstreamAccount
}

const unrestricted = account('unrestricted', [])
const gpt55Only = account('gpt55-only', ['gpt-5.5'])
const gpt54Only = account('gpt54-only', ['gpt-5.4'])

const matched = filterGatewayAccountsByRequestedModel([gpt55Only, unrestricted, gpt54Only], 'gpt-5.5')
assert.deepEqual(matched.accounts.map((item) => item.id), ['gpt55-only', 'unrestricted'])
assert.equal(matched.skippedCount, 1)
assert.equal(matched.reason, undefined)

const missingModel = filterGatewayAccountsByRequestedModel([gpt55Only, unrestricted], undefined)
assert.deepEqual(missingModel.accounts.map((item) => item.id), ['unrestricted'])
assert.equal(missingModel.skippedCount, 1)
assert.equal(missingModel.reason, undefined)

const allRestrictedMissingModel = filterGatewayAccountsByRequestedModel([gpt55Only, gpt54Only], undefined)
assert.deepEqual(allRestrictedMissingModel.accounts, [])
assert.equal(allRestrictedMissingModel.reason, 'missing_model')
assert.match(gatewayModelFilterFailureMessage(allRestrictedMissingModel), /缺少 model/)

const unsupported = filterGatewayAccountsByRequestedModel([gpt55Only, gpt54Only], 'claude-opus-4-6')
assert.deepEqual(unsupported.accounts, [])
assert.equal(unsupported.reason, 'unsupported_model')
assert.match(gatewayModelFilterFailureMessage(unsupported), /claude-opus-4-6/)

await assertStorageRoundTrip()

console.log('account model filter regression passed')

async function assertStorageRoundTrip(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-account-model-filter-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
  runtimeConfig.secret = 'account-model-filter-secret'
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

  try {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: '账户模型限制回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-filter-create',
        base_url: 'http://127.0.0.1:9/v1'
      },
      supportedModels: ['gpt-5.5', 'gpt-5.4']
    }, access)
    assert.deepEqual(sorted(account.supportedModels), ['gpt-5.4', 'gpt-5.5'], '创建账户应返回模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getDatabase(), account.id), ['gpt-5.4', 'gpt-5.5'], '创建账户应写入模型限制关系表')

    const runtimeAccount = repositories.listOpenAIAccountsForGroup(account.boundGroupId ?? '', access.systemAccountId)
      .find((item) => item.id === account.id)
    assert.deepEqual(sorted(runtimeAccount?.supportedModels), ['gpt-5.4', 'gpt-5.5'], '网关候选账号快照应带上模型限制')

    const updated = repositories.updateAccount(account.id, { supportedModels: ['gpt-5.4'] }, access)
    assert.deepEqual(sorted(updated?.supportedModels), ['gpt-5.4'], '更新账户应返回新的模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getDatabase(), account.id), ['gpt-5.4'], '更新账户应替换模型限制关系表')

    const renamed = repositories.updateAccount(account.id, { name: '账户模型限制回归-仅改名' }, access)
    assert.deepEqual(sorted(renamed?.supportedModels), ['gpt-5.4'], '未提交 supportedModels 时不应清空已有模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getDatabase(), account.id), ['gpt-5.4'], '未提交 supportedModels 时关系表应保持不变')

    const cleared = repositories.updateAccount(account.id, { supportedModels: [] }, access)
    assert.deepEqual(cleared?.supportedModels, [], '提交空数组应清空模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getDatabase(), account.id), [], '提交空数组应清空关系表')

    assert.throws(() => {
      repositories.createAccount({
        providerCode: 'openai',
        name: '账户模型限制非法模型回归',
        type: 'api_key',
        credentials: {
          api_key: 'sk-account-model-filter-invalid',
          base_url: 'http://127.0.0.1:9/v1'
        },
        supportedModels: ['claude-opus-4-6']
      }, access)
    }, /供应商模型目录/, '账户模型限制必须来自供应商模型目录')
  } finally {
    try {
      databaseModule.getDatabase().close()
      databaseModule.getRecordDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function loadStoredModels(database: DatabaseSync, accountId: string): string[] {
  return (database
    .prepare('SELECT model FROM account_supported_models WHERE account_id = ? ORDER BY model ASC')
    .all(accountId) as unknown as Array<{ model: string }>)
    .map((row) => row.model)
}

function sorted(values: string[] | undefined): string[] {
  return [...(values ?? [])].sort()
}
