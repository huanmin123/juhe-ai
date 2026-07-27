import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
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
import { markGatewayUpstreamModelsProbe } from '../../modules/gateway/request/upstream-models-probe.js'
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

const emptySupportedModels = account('empty-supported-models', [])
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
const sameNameResponsesBridge = account('same-name-responses-bridge', ['gpt-5.5'], [
  { sourceModel: 'gpt-5.5', sourceEndpointFamily: 'responses', upstreamModel: 'gpt-5.5', upstreamEndpointFamily: 'chat_completions', enabled: true }
])
const sameNameInvalidBridge = account('same-name-invalid-bridge', ['gpt-5.5'], [
  { sourceModel: 'gpt-5.5', sourceEndpointFamily: 'responses', upstreamModel: 'gpt-5.5-private', upstreamEndpointFamily: 'chat_completions', enabled: true }
])
const disabledSameNameBridge = account('disabled-same-name-bridge', ['gpt-5.5'], [
  { sourceModel: 'gpt-5.5', sourceEndpointFamily: 'responses', upstreamModel: 'gpt-5.5', upstreamEndpointFamily: 'chat_completions', enabled: false }
])

const matched = filterGatewayAccountsByRequestedModel([gpt55Only, emptySupportedModels, gpt54Only], 'gpt-5.5')
assert.deepEqual(matched.accounts.map((item) => item.id), ['gpt55-only'])
assert.equal(matched.skippedCount, 2)
assert.equal(matched.invalidModelConstraintCount, 1)
assert.equal(matched.directMatchedCount, 1)
assert.equal(matched.mappingMatchedCount, 0)
assert.equal(matched.reason, undefined)

const prioritized = filterGatewayAccountsByRequestedModel([emptySupportedModels, mappedByUpstream, gpt55Only], 'gpt-5.5', 'chat_completions')
assert.deepEqual(
  prioritized.accounts.map((item) => item.id),
  ['gpt55-only', 'mapped-by-upstream'],
  '模型过滤应优先保留直接命中和映射命中账户，并跳过没有支持模型的异常账户'
)
assert.equal(prioritized.skippedCount, 1)
assert.equal(prioritized.directMatchedCount, 1)
assert.equal(prioritized.mappingMatchedCount, 1)
assert.equal(prioritized.invalidModelConstraintCount, 1)
assert.equal(prioritized.modelPriority.rankByAccountId.get('gpt55-only'), 0)
assert.equal(prioritized.modelPriority.rankByAccountId.get('mapped-by-upstream'), 1)
assert.equal(prioritized.modelPriority.rankByAccountId.get('empty-supported-models'), 2)

const sameNameBridgePrioritized = filterGatewayAccountsByRequestedModel([
  sameNameResponsesBridge,
  gpt55Only
], 'gpt-5.5', 'responses')
assert.deepEqual(
  sameNameBridgePrioritized.accounts.map((item) => item.id),
  ['gpt55-only', 'same-name-responses-bridge'],
  '跨账户仍应由真正直连账户优先，但同账户精确协议映射不能被 supportedModels 直连命中遮蔽'
)
assert.equal(sameNameBridgePrioritized.directMatchedCount, 1)
assert.equal(sameNameBridgePrioritized.mappingMatchedCount, 1)
assert.equal(sameNameBridgePrioritized.modelPriority.rankByAccountId.get('same-name-responses-bridge'), 1)

const invalidSameNameBridge = filterGatewayAccountsByRequestedModel([
  sameNameInvalidBridge
], 'gpt-5.5', 'responses')
assert.deepEqual(invalidSameNameBridge.accounts, [], '精确映射目标失效时不得回退为同名模型直连')
assert.equal(invalidSameNameBridge.directMatchedCount, 0)
assert.equal(invalidSameNameBridge.mappingMatchedCount, 0)
assert.equal(invalidSameNameBridge.reason, 'unsupported_model')

const disabledSameNameBridgeResult = filterGatewayAccountsByRequestedModel([
  disabledSameNameBridge
], 'gpt-5.5', 'responses')
assert.deepEqual(disabledSameNameBridgeResult.accounts.map((item) => item.id), ['disabled-same-name-bridge'])
assert.equal(disabledSameNameBridgeResult.directMatchedCount, 1, '停用映射不应遮蔽账户真实直连支持')
assert.equal(disabledSameNameBridgeResult.mappingMatchedCount, 0)

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

const missingModel = filterGatewayAccountsByRequestedModel([gpt55Only, emptySupportedModels], undefined)
assert.deepEqual(missingModel.accounts, [])
assert.equal(missingModel.skippedCount, 2)
assert.equal(missingModel.directMatchedCount, 0)
assert.equal(missingModel.mappingMatchedCount, 0)
assert.equal(missingModel.invalidModelConstraintCount, 1)
assert.equal(missingModel.reason, 'missing_model')

const allRestrictedMissingModel = filterGatewayAccountsByRequestedModel([gpt55Only, gpt54Only], undefined)
assert.deepEqual(allRestrictedMissingModel.accounts, [])
assert.equal(allRestrictedMissingModel.reason, 'missing_model')
assert.match(gatewayModelFilterFailureMessage(allRestrictedMissingModel), /缺少 model/)

const unsupported = filterGatewayAccountsByRequestedModel([gpt55Only, gpt54Only], 'claude-opus-4-6')
assert.deepEqual(unsupported.accounts, [])
assert.equal(unsupported.reason, 'unsupported_model')
assert.match(gatewayModelFilterFailureMessage(unsupported), /claude-opus-4-6/)

await assertCandidateFilterLoadsModelAwareCandidates()
await assertCandidateFilterBypassesModelRestrictionsForUpstreamCatalog()
assert.match(
  readFileSync(resolve('src/modules/gateway/request/preflight.ts'), 'utf8'),
  /bypassModelFilter: interactionResourceAffinity !== undefined \|\| options\.forwardModelsRequestToUpstream/,
  '直连上游模型目录时，网关预检必须绕过账户支持模型筛选'
)
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
  databaseModule.getBusinessDatabase()
  const accountImport = await import('../../modules/accounts/account-import.service.js')
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

  try {
    const group = repositories.createGroup({
      name: '账户模型限制回归分组',
      providerCode: GPT_VENDOR_CODE
    }, access)
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '账户模型限制回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-filter-create',
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      supportedModels: ['gpt-5.5', 'gpt-5.4'],
      healthCheckModel: 'gpt-5.4',
      groupId: group.id
    }, access)
    assert.deepEqual(sorted(account.supportedModels), ['gpt-5.4', 'gpt-5.5'], '创建账户应保存资料完整的内置模型')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4', 'gpt-5.5'], '创建账户应写入模型关系')

    assert.equal(repositories.recordAccountHealthCheckSuccess(account.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), true, '网关候选验证前应由后台健康检查激活账户')

    const runtimeAccount = repositories.listOpenAIAccountsForGroup(account.boundGroupId ?? '', access.systemAccountId)
      .find((item) => item.id === account.id)
    assert.deepEqual(sorted(runtimeAccount?.supportedModels), ['gpt-5.4', 'gpt-5.5'], '网关候选账号快照应带上模型限制')

    const updated = repositories.updateAccount(account.id, { supportedModels: ['gpt-5.4'] }, access)
    assert.deepEqual(sorted(updated?.supportedModels), ['gpt-5.4'], '更新账户应返回新的模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4'], '更新账户应替换模型限制关系表')

    const updatedWithTwoModels = repositories.updateAccount(account.id, {
      supportedModels: ['gpt-5.4', 'gpt-5.5']
    }, access)
    assert.deepEqual(sorted(updatedWithTwoModels?.supportedModels), ['gpt-5.4', 'gpt-5.5'], '更新账户应允许恢复资料完整的内置模型')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4', 'gpt-5.5'], '更新账户应持久化模型关系')

    const renamed = repositories.updateAccount(account.id, { name: '账户模型限制回归-仅改名' }, access)
    assert.deepEqual(sorted(renamed?.supportedModels), ['gpt-5.4', 'gpt-5.5'], '未提交 supportedModels 时不应清空已有模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4', 'gpt-5.5'], '未提交 supportedModels 时关系表应保持不变')

    assert.throws(
      () => repositories.updateAccount(account.id, { supportedModels: [] }, access),
      /账户检查模型必须属于账户支持模型/,
      '提交空数组不应清空模型限制'
    )
    assert.deepEqual(sorted(repositories.findAccountSummary(account.id, access)?.supportedModels), ['gpt-5.4', 'gpt-5.5'], '提交空数组失败后应保留原模型限制')
    assert.deepEqual(loadStoredModels(databaseModule.getBusinessDatabase(), account.id), ['gpt-5.4', 'gpt-5.5'], '提交空数组失败后关系表应保持不变')

    const importResult = accountImport.executeAccountImport({
      type: accountImport.accountImportProtocolType,
      version: accountImport.accountImportProtocolVersion,
      accounts: [{
        name: '内置模型导入账户',
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'active',
        groupId: group.id,
        supportedModels: ['gpt-5.4', 'gpt-5.5'],
        healthCheckModel: 'gpt-5.4',
        credentials: {
          api_key: 'sk-account-model-filter-import-built-in',
          base_url: 'https://api.openai.com/v1'
        }
      }]
    }, {}, access)
    assert.equal(importResult.summary.accounts.create, 1, '账户导入应允许资料完整的内置模型')
    const importedAccountId = importResult.accounts[0]?.accountId
    assert(importedAccountId, '内置模型导入应返回账户 ID')
    assert.deepEqual(
      sorted(repositories.findAccountSummary(importedAccountId, access)?.supportedModels),
      ['gpt-5.4', 'gpt-5.5'],
      '账户导入应真实持久化内置模型'
    )

    assert.throws(() => repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '账户模型限制必填回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-filter-required',
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      groupId: group.id
    }, access), /账户支持模型不能为空/, '创建账户必须显式选择至少一个支持模型')

    const openAICompatibleGroup = repositories.createGroup({
      name: '账户模型限制 OpenAI 兼容分组',
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE
    }, access)
    assert.equal(openAICompatibleGroup.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE)
    const openAICompatibleAccount = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: '账户模型限制 OpenAI 兼容账号回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-account-model-filter-openai-compatible',
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      groupId: openAICompatibleGroup.id
    }, access)
    assert.equal(openAICompatibleAccount.providerCode, OPENAI_COMPATIBLE_PROVIDER_CODE, 'openai 通用供应商应允许创建 API Key 账户')
    assert.throws(() => {
      repositories.createAccount({
        providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
        providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
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
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
    rawCandidateAccounts: [emptySupportedModels],
    clientStrategy: {
      requestClientCompatibility: 'openai_standard'
    } as OpenAIGatewayClientStrategyContext,
    systemAccountId: 'sys_model_filter',
    groupId: 'group_model_filter',
    endpoint: 'POST /v1/chat/completions',
    routeCoordinator: {
      requestFallback: async () => ({ attempted: false }),
      completeFailure: async (failure) => {
        throw new Error(failure.message)
      }
    },
    loadModelAwareCandidateAccounts: async (requestedModel) => {
      loaderCalls += 1
      assert.equal(requestedModel, 'gpt-5.5')
      return [gpt55Only, gpt54Only]
    }
  })

  assert.equal(loaderCalls, 1, '候选窗口内只有缺失支持模型的异常账号时，应按请求模型补读模型感知候选')
  assert.equal(result.outcome, 'accounts')
  if (result.outcome === 'accounts') {
    assert.deepEqual(result.accounts.map((item) => item.id), ['gpt55-only'])
  }
}

async function assertCandidateFilterBypassesModelRestrictionsForUpstreamCatalog(): Promise<void> {
  const result = await filterOpenAIGatewayRequestCandidateAccounts({
    req: markGatewayUpstreamModelsProbe({
      method: 'GET',
      path: '/v1/models',
      originalUrl: '/v1/models'
    } as Request),
    res: {} as Response,
    auditCapture: {
      addGatewayMetadata() {}
    } as unknown as AuditCaptureContext,
    usageContext: {} as never,
    startedAt: Date.now(),
    rawCandidateAccounts: [gpt55Only],
    clientStrategy: {
      requestClientCompatibility: 'openai_standard'
    } as OpenAIGatewayClientStrategyContext,
    systemAccountId: 'sys_model_filter',
    groupId: 'group_model_filter',
    endpoint: 'GET /v1/models',
    bypassModelFilter: true,
    routeCoordinator: {
      requestFallback: async () => ({ attempted: false }),
      completeFailure: async (failure) => {
        throw new Error(failure.message)
      }
    }
  })

  assert.equal(result.outcome, 'accounts')
  if (result.outcome === 'accounts') {
    assert.deepEqual(
      result.accounts.map((item) => item.id),
      ['gpt55-only'],
      '上游模型目录没有 model 参数时，仍必须使用当前编辑账户，而不是因支持模型限制被筛空'
    )
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
