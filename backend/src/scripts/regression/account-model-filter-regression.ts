import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'
import type { Request, Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../../domain/provider-protocol.js'
import {
  filterGatewayAccountsByRequestedModel,
  gatewayModelFilterFailureMessage
} from '../../modules/gateway/dispatch/model-filter.js'
import { filterOpenAIGatewayRequestCandidateAccounts } from '../../modules/gateway/dispatch/candidate-filter.js'
import { logger } from '../../shared/logger.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import type { AccountModelMapping } from '../../domain/types.js'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import type { OpenAIGatewayClientStrategyContext } from '../../modules/gateway/client-profiles/strategy.js'

function account(id: string, supportedModels?: string[], modelMappings?: AccountModelMapping[]): UpstreamAccount {
  return {
    id,
    name: id,
    supportedModels,
    modelMappings,
    type: 'api_key',
    status: 'active',
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    clientCompatibility: 'openai_standard',
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
    streamFailureCount: 0
  } as UpstreamAccount
}

const unrestricted = account('unrestricted', [])
const gpt55Only = account('gpt55-only', ['gpt-5.5'])
const gpt54Only = account('gpt54-only', ['gpt-5.4'])
const mappedByUpstream = account('mapped-by-upstream', ['gpt-5.5-private'], [
  { sourceModel: 'gpt-5.5', sourceEndpointFamily: 'chat_completions', upstreamModel: 'gpt-5.5-private', upstreamEndpointFamily: 'chat_completions', enabled: true }
])
const disabledMappedByUpstream = account('disabled-mapped-by-upstream', ['gpt-5.5-private'], [
  { sourceModel: 'gpt-5.5', sourceEndpointFamily: 'chat_completions', upstreamModel: 'gpt-5.5-private', upstreamEndpointFamily: 'chat_completions', enabled: false }
])
const mappedToUnsupportedUpstream = account('mapped-to-unsupported-upstream', ['gpt-5.4'], [
  { sourceModel: 'gpt-5.5', sourceEndpointFamily: 'chat_completions', upstreamModel: 'gpt-5.5-private', upstreamEndpointFamily: 'chat_completions', enabled: true }
])

const matched = filterGatewayAccountsByRequestedModel([gpt55Only, unrestricted, gpt54Only], 'gpt-5.5')
assert.deepEqual(matched.accounts.map((item) => item.id), ['gpt55-only', 'unrestricted'])
assert.equal(matched.skippedCount, 1)
assert.equal(matched.unrestrictedAccountCount, 1)
assert.equal(matched.directMatchedCount, 1)
assert.equal(matched.mappingMatchedCount, 0)
assert.equal(matched.reason, undefined)

const prioritized = filterGatewayAccountsByRequestedModel([unrestricted, mappedByUpstream, gpt55Only], 'gpt-5.5', 'chat_completions')
assert.deepEqual(
  prioritized.accounts.map((item) => item.id),
  ['gpt55-only', 'mapped-by-upstream', 'unrestricted'],
  '模型过滤应保留直接命中账户、映射命中账户和不限制模型账户'
)
assert.equal(prioritized.skippedCount, 0)
assert.equal(prioritized.directMatchedCount, 1)
assert.equal(prioritized.mappingMatchedCount, 1)
assert.equal(prioritized.unrestrictedAccountCount, 1)
assert.equal(prioritized.modelPriority.rankByAccountId.get('gpt55-only'), 0)
assert.equal(prioritized.modelPriority.rankByAccountId.get('mapped-by-upstream'), 1)
assert.equal(prioritized.modelPriority.rankByAccountId.get('unrestricted'), 2)

const mapped = filterGatewayAccountsByRequestedModel([
  mappedByUpstream,
  disabledMappedByUpstream,
  mappedToUnsupportedUpstream,
  gpt54Only
], 'gpt-5.5', 'chat_completions')
assert.deepEqual(mapped.accounts.map((item) => item.id), ['mapped-by-upstream'])
assert.equal(mapped.skippedCount, 3)
assert.equal(mapped.directMatchedCount, 0)
assert.equal(mapped.mappingMatchedCount, 1)
assert.equal(mapped.reason, undefined)

const missingModel = filterGatewayAccountsByRequestedModel([gpt55Only, unrestricted], undefined)
assert.deepEqual(missingModel.accounts.map((item) => item.id), ['unrestricted'])
assert.equal(missingModel.skippedCount, 1)
assert.equal(missingModel.directMatchedCount, 0)
assert.equal(missingModel.mappingMatchedCount, 0)
assert.equal(missingModel.reason, undefined)

const allRestrictedMissingModel = filterGatewayAccountsByRequestedModel([gpt55Only, gpt54Only], undefined)
assert.deepEqual(allRestrictedMissingModel.accounts, [])
assert.equal(allRestrictedMissingModel.reason, 'missing_model')
assert.match(gatewayModelFilterFailureMessage(allRestrictedMissingModel), /缺少 model/)

const unsupported = filterGatewayAccountsByRequestedModel([gpt55Only, gpt54Only], 'claude-opus-4-6')
assert.deepEqual(unsupported.accounts, [])
assert.equal(unsupported.reason, 'unsupported_model')
assert.match(gatewayModelFilterFailureMessage(unsupported), /claude-opus-4-6/)

await assertCandidateFilterLoadsModelAwareCandidates()
await assertStorageRoundTrip()

console.log('account model filter regression passed')

async function assertStorageRoundTrip(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-account-model-filter-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
    const group = repositories.createGroup({
      name: '账户模型限制回归分组',
      providerCode: GPT_VENDOR_CODE
    }, access)
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      name: '账户模型限制回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-filter-create',
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      groupId: group.id
    }, access)
    assert.deepEqual(sorted(account.supportedModels), ['gpt-5.4', 'gpt-5.5'], '创建账户应返回模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4', 'gpt-5.5'], '创建账户应写入模型限制关系表')

    const runtimeAccount = repositories.listOpenAIAccountsForGroup(account.boundGroupId ?? '', access.systemAccountId)
      .find((item) => item.id === account.id)
    assert.deepEqual(sorted(runtimeAccount?.supportedModels), ['gpt-5.4', 'gpt-5.5'], '网关候选账号快照应带上模型限制')

    const updated = repositories.updateAccount(account.id, { supportedModels: ['gpt-5.4'] }, access)
    assert.deepEqual(sorted(updated?.supportedModels), ['gpt-5.4'], '更新账户应返回新的模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4'], '更新账户应替换模型限制关系表')

    const renamed = repositories.updateAccount(account.id, { name: '账户模型限制回归-仅改名' }, access)
    assert.deepEqual(sorted(renamed?.supportedModels), ['gpt-5.4'], '未提交 supportedModels 时不应清空已有模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4'], '未提交 supportedModels 时关系表应保持不变')

    const cleared = repositories.updateAccount(account.id, { supportedModels: [] }, access)
    assert.deepEqual(cleared?.supportedModels, [], '提交空数组应清空模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), [], '提交空数组应清空关系表')

    const openAICompatibleGroup = repositories.createGroup({
      name: '账户模型限制 OpenAI 兼容分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE
    }, access)
    assert.equal(openAICompatibleGroup.providerProtocolProfileId, OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID)
    const openAICompatibleAccount = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      name: '账户模型限制 OpenAI 兼容账号回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-filter-openai-compatible',
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      supportedModels: ['gpt-5.5'],
      groupId: openAICompatibleGroup.id
    }, access)
    assert.equal(openAICompatibleAccount.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, 'openai 通用供应商应允许创建 API Key 账户')
    assert.throws(() => {
      repositories.createAccount({
        providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
        name: '账户模型限制 OpenAI 兼容 OAuth 回归',
        type: 'oauth',
        credentials: {
          refresh_token: 'refresh-account-model-filter-openai-compatible',
          base_url: 'https://api.openai.com/v1'
        },
        status: 'active',
        groupId: openAICompatibleGroup.id
      }, access)
    }, /不支持账户类型 oauth/, 'openai 通用供应商不应继承 GPT OAuth 能力')

    assert.throws(() => {
      repositories.createAccount({
        providerCode: GPT_VENDOR_CODE,
        name: '账户模型限制非法模型回归',
        type: 'api_key',
        credentials: {
          api_key: 'sk-account-model-filter-invalid',
          base_url: 'https://api.openai.com/v1'
        },
        status: 'active',
        supportedModels: ['claude-opus-4-6'],
        groupId: group.id
      }, access)
    }, /供应商模型目录/, '账户模型限制必须来自供应商模型目录')
  } finally {
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function assertCandidateFilterLoadsModelAwareCandidates(): Promise<void> {
  let loaderCalls = 0
  const result = await filterOpenAIGatewayRequestCandidateAccounts({
    req: {
      method: 'POST',
      path: '/v1/chat/completions',
      originalUrl: '/v1/chat/completions',
      body: { model: 'gpt-5.5' }
    } as Request,
    res: {} as Response,
    auditCapture: {
      addGatewayMetadata() {}
    } as unknown as AuditCaptureContext,
    usageContext: {} as never,
    startedAt: Date.now(),
    rawCandidateAccounts: [gpt54Only],
    clientStrategy: {
      requestClientCompatibility: 'openai_standard'
    } as OpenAIGatewayClientStrategyContext,
    systemAccountId: 'sys_model_filter',
    groupId: 'group_model_filter',
    endpoint: 'POST /v1/chat/completions',
    attemptFallback: async () => ({ attempted: false }),
    loadModelAwareCandidateAccounts: async (requestedModel) => {
      loaderCalls += 1
      assert.equal(requestedModel, 'gpt-5.5')
      return [gpt55Only, gpt54Only]
    }
  })

  assert.equal(loaderCalls, 1, '候选窗口内只有不匹配限制账号时，应按请求模型补读模型感知候选')
  assert.equal(result.outcome, 'accounts')
  if (result.outcome === 'accounts') {
    assert.deepEqual(result.accounts.map((item) => item.id), ['gpt55-only'])
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
