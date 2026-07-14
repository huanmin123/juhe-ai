import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { AccountModelMapping } from '../../domain/types.js'
import { previewAccountImportAsync } from '../../modules/accounts/account-import.service.js'
import { prepareAccountDraftTestSnapshotAsync } from '../../modules/accounts/account-draft-test.service.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import {
  createAccountAsync,
  deleteAccountAsync,
  findAccountForTestAsync,
  listAccountGroupOptionsAsync,
  updateAccountAsync
} from '../../storage/repositories.js'
import { closePostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账号模型映射 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(
  process.env.JUHE_AI_ALLOW_ACCOUNT_MODEL_MAPPING_POSTGRES_SMOKE,
  '1',
  '账号模型映射 PG smoke 需要显式设置 JUHE_AI_ALLOW_ACCOUNT_MODEL_MAPPING_POSTGRES_SMOKE=1'
)

const access = {
  systemAccountId: 'sys_admin',
  role: 'admin' as const,
  systemAccountFilterId: 'sys_admin'
}
const marker = `account_model_mapping_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const model = 'gpt-5.5'
const createdAccountIds: string[] = []

const enabledMapping: AccountModelMapping = {
  sourceModel: model,
  sourceEndpointFamily: 'responses',
  upstreamModel: model,
  upstreamEndpointFamily: 'chat_completions',
  enabled: true
}
const disabledMapping: AccountModelMapping = {
  ...enabledMapping,
  enabled: false
}

try {
  const group = (await listAccountGroupOptionsAsync(access, { providerCode: GPT_VENDOR_CODE }))
    .find((item) => item.providerCode === GPT_VENDOR_CODE && item.permissions?.canManageAccounts !== false)
  assert(group, '账号模型映射 PG smoke 需要当前 schema seed 提供可管理的 GPT 分组')

  const account = await createAccountAsync({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `模型映射PG烟测-${marker}`,
    type: 'api_key',
    status: 'temporary_unavailable',
    credentials: {
      api_key: `sk-${marker}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'responses_json', 'responses_sse']
    },
    supportedModels: [model],
    healthCheckModel: model,
    healthCheckEndpointMode: 'responses_sse' as const,
    modelMappings: [enabledMapping],
    groupId: group.id
  }, access)
  createdAccountIds.push(account.id)

  await assert.rejects(
    updateAccountAsync(account.id, {
      credentials: {
        api_key: `sk-${marker}`,
        base_url: 'https://api.openai.com/v1',
        supported_endpoint_modes: ['responses_json', 'responses_sse']
      }
    }, access),
    /Chat Completions.*上游接口能力/,
    'PG 异步账户更新必须拒绝右侧目标族能力缺失的启用映射'
  )
  const preserved = await findAccountForTestAsync(account.id, access)
  assert.deepEqual(
    preserved?.credentials.supported_endpoint_modes,
    ['chat_json', 'responses_json', 'responses_sse'],
    'PG 异步账户更新被拒绝后必须保留原上游接口能力'
  )
  assert.deepEqual(
    preserved?.modelMappings,
    [enabledMapping],
    'PG 异步账户更新被拒绝后必须保留原模型映射'
  )

  const importAccount = (mapping: AccountModelMapping) => ({
    name: `模型映射PG导入-${mapping.enabled ? '启用' : '停用'}-${marker}`,
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key' as const,
    status: 'active' as const,
    groupId: group.id,
    credentials: {
      api_key: `sk-import-${marker}-${mapping.enabled ? 'enabled' : 'disabled'}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    supportedModels: [model],
    healthCheckModel: model,
    healthCheckEndpointMode: 'responses_sse' as const,
    modelMappings: [mapping]
  })
  const rejectedImport = await previewAccountImportAsync({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [importAccount(enabledMapping)]
  }, {}, access)
  assert.equal(rejectedImport.canImport, false, 'PG 异步导入预览必须拒绝右侧目标族能力缺失的启用映射')
  assert(
    rejectedImport.accounts[0]?.messages.some((message) => /Chat Completions.*上游接口能力/.test(message)),
    'PG 异步导入预览应返回右侧目标族能力错误'
  )
  const acceptedImport = await previewAccountImportAsync({
    type: 'juhe-ai-account-import',
    version: 1,
    accounts: [importAccount(disabledMapping)]
  }, {}, access)
  assert.equal(acceptedImport.canImport, true, 'PG 异步导入预览应保留并接受目标族能力缺失的停用映射')

  await assert.rejects(
    prepareAccountDraftTestSnapshotAsync({
      accountInput: {
        providerCode: GPT_VENDOR_CODE,
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: `健康检查请求形态PG异步草稿-${marker}`,
        type: 'api_key',
        credentials: {
          api_key: `sk-draft-mode-${marker}`,
          base_url: 'https://api.openai.com/v1',
          supported_endpoint_modes: ['chat_json', 'responses_json']
        },
        supportedModels: [model],
        healthCheckModel: model,
        healthCheckEndpointMode: 'responses_sse',
        groupId: group.id
      },
      requestAccess: access
    }),
    /账户健康检查请求形态 responses_sse 未启用/,
    'PG 异步草稿必须按最终 endpoint modes 拒绝缺少 responses_sse 的健康检查请求形态'
  )

  const draftInput = (mapping: AccountModelMapping) => ({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `模型映射PG草稿-${mapping.enabled ? '启用' : '停用'}-${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-draft-${marker}-${mapping.enabled ? 'enabled' : 'disabled'}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    supportedModels: [model],
    healthCheckModel: model,
    healthCheckEndpointMode: 'responses_sse' as const,
    modelMappings: [mapping],
    groupId: group.id
  })
  await assert.rejects(
    prepareAccountDraftTestSnapshotAsync({
      accountInput: draftInput(enabledMapping),
      requestAccess: access
    }),
    /Chat Completions.*上游接口能力/,
    'PG 异步草稿测试必须拒绝右侧目标族能力缺失的启用映射'
  )
  const acceptedDraft = await prepareAccountDraftTestSnapshotAsync({
    accountInput: draftInput(disabledMapping),
    requestAccess: access
  })
  assert.deepEqual(
    acceptedDraft.draftAccount.modelMappings,
    [disabledMapping],
    'PG 异步草稿测试应保留目标族能力缺失的停用映射'
  )

  console.log(JSON.stringify({
    message: '账号模型映射 PostgreSQL async smoke 通过',
    accountId: account.id,
    updateRollbackPreserved: true,
    enabledImportRejected: true,
    disabledImportAccepted: true,
    enabledDraftRejected: true,
    disabledDraftAccepted: true
  }))
} finally {
  try {
    for (const accountId of createdAccountIds.reverse()) {
      assert.equal(await deleteAccountAsync(accountId, access), true, `PG smoke 临时账户清理失败：${accountId}`)
      assert.equal(await findAccountForTestAsync(accountId, access), undefined, `PG smoke 临时账户清理后仍可查询：${accountId}`)
    }
  } finally {
    await closeRedisClients()
    await closePostgresPool()
  }
}
