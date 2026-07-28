import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import {
  loadAccountModelValidationContextAsync
} from '../../storage/account-model-validation.repository.js'
import {
  postgresDialect,
  sqliteDialect,
  type DatabaseClient,
  type DatabaseClientDriver,
  type ExecuteResult,
  type SqlDialect
} from '../../storage/database-client.js'
import { requireEnabledProviderProtocolProfileInClientAsync } from '../../storage/provider.repository.js'

interface CapturedQuery {
  sql: string
  params: readonly unknown[]
}

interface ValidationRow {
  provider_code: string
  model: string
  mode: null
  supported_api_protocols_json: string
  supported_service_tiers_json: string
  supported_reasoning_efforts_json: string
  scope_priority: number
  priced: number
}

async function main(): Promise<void> {
  await assertTargetedProviderProfileRead()
  await assertSingleModelQueryBudget()
  await assertMultiMappingDeduplicatesModels()
  await assertHybridFamiliesUseOneCatalogQuery()
  await assertPostgresScalarParameterShape()
  await assertManagementPathsReuseValidationContext()
  console.log('account model validation demand regression passed')
}

async function assertTargetedProviderProfileRead(): Promise<void> {
  const captured: CapturedQuery[] = []
  const client = recordingClient('sqlite', captured, (sql) => {
    if (sql.includes('LEFT JOIN') && sql.includes('provider_protocol_profiles')) {
      return [{
        id: 'profile_gpt_openai_v1', provider_code: 'gpt', name: 'GPT OpenAI v1', description: null,
        enabled: 1, protocol_code: 'openai', protocol_version: 'v1', base_url: 'https://api.openai.com/v1',
        default_health_check_model: 'gpt-5.6', account_types_json: '["api_key"]', capabilities_json: '[]',
        provider_enabled: 1, provider_exists: 1
      }]
    }
    return [{ profile_id: 'profile_gpt_openai_v1', family_code: 'responses', name: 'Responses', description: null }]
  })
  const profile = await requireEnabledProviderProtocolProfileInClientAsync(client, 'gpt', 'profile_gpt_openai_v1')
  assert.equal(profile.id, 'profile_gpt_openai_v1')
  assert.deepEqual(profile.endpointFamilies?.map((family) => typeof family === 'string' ? family : family.code), ['responses'])
  assert.equal(captured.length, 2, '单一 provider/profile 校验应只执行定点主记录和 endpoint family 两条窄查询')
  assert.match(captured[0]!.sql, /ppp\.id = \?/) 
  assert.match(captured[0]!.sql, /WHERE p\.code = \?/) 
  assert.doesNotMatch(captured[0]!.sql, /ORDER BY ppp\.provider_code/) 
  assert.deepEqual(captured[0]!.params, ['profile_gpt_openai_v1', 'gpt'])
}

async function assertSingleModelQueryBudget(): Promise<void> {
  const captured: CapturedQuery[] = []
  const client = recordingClient('sqlite', captured, () => [validationRow('gpt', 'gpt-5.6', ['responses'])])
  const context = await loadAccountModelValidationContextAsync(client, {
    providerCode: 'gpt',
    systemAccountId: 'sys_user',
    models: ['gpt-5.6']
  })
  assert.equal(context.accountModel('gpt-5.6')?.model, 'gpt-5.6')
  assert.equal(captured.length, 1, '普通供应商单模型校验必须只发起一次目录查询')
  assertNarrowCatalogProjection(captured[0]!.sql)
  assert.deepEqual(captured[0]!.params, ['gpt', 'gpt-5.6', 'gpt', 'gpt-5.6', 'sys_user'])
}

async function assertMultiMappingDeduplicatesModels(): Promise<void> {
  const captured: CapturedQuery[] = []
  const client = recordingClient('sqlite', captured, () => [
    validationRow('gpt', 'alias-source', ['chat_completions']),
    validationRow('gpt', 'gpt-5.6', ['responses']),
    validationRow('gpt', 'gpt-5.6-mini', ['responses'])
  ])
  const context = await loadAccountModelValidationContextAsync(client, {
    providerCode: 'gpt',
    systemAccountId: 'sys_user',
    models: ['gpt-5.6', 'gpt-5.6'],
    mappings: [
      {
        sourceModel: 'alias-source', sourceEndpointFamily: 'chat_completions',
        upstreamModel: 'gpt-5.6', upstreamEndpointFamily: 'responses', enabled: true
      },
      {
        sourceModel: 'alias-source', sourceEndpointFamily: 'chat_completions',
        upstreamModel: 'gpt-5.6-mini', upstreamEndpointFamily: 'responses', enabled: true
      }
    ]
  })
  assert.equal(context.endpointModel('responses', 'gpt-5.6-mini')?.priced, true)
  assert.equal(captured.length, 1, '多 mapping 不得按 mapping 或 endpoint family 重复查询目录')
  const modelParams = captured[0]!.params.filter((value) => typeof value === 'string' && value.startsWith('gpt-5.6'))
  assert.deepEqual(modelParams, ['gpt-5.6', 'gpt-5.6-mini', 'gpt-5.6', 'gpt-5.6-mini'])
}

async function assertHybridFamiliesUseOneCatalogQuery(): Promise<void> {
  const captured: CapturedQuery[] = []
  const client = recordingClient('sqlite', captured, (sql) => {
    if (sql.includes('SELECT DISTINCT p.code')) {
      return [
        { code: 'gpt', protocol_code: 'openai', protocol_version: 'v1' },
        { code: 'anthropic', protocol_code: 'anthropic', protocol_version: 'v1' },
        { code: 'gemini', protocol_code: 'gemini', protocol_version: 'v1beta' }
      ]
    }
    return [
      validationRow('gpt', 'gpt-5.6', ['responses']),
      validationRow('anthropic', 'claude-sonnet', ['messages']),
      validationRow('gemini', 'gemini-pro', ['generate_content'])
    ]
  })
  const context = await loadAccountModelValidationContextAsync(client, {
    providerCode: 'hybrid',
    systemAccountId: 'sys_user',
    models: [],
    mappings: [
      {
        sourceModel: 'gpt-5.6', sourceEndpointFamily: 'responses',
        upstreamModel: 'claude-sonnet', upstreamEndpointFamily: 'messages', enabled: true
      },
      {
        sourceModel: 'gemini-pro', sourceEndpointFamily: 'stream_generate_content',
        upstreamModel: 'gemini-pro', upstreamEndpointFamily: 'generate_content', enabled: true
      }
    ]
  })
  assert.equal(context.endpointModel('messages', 'claude-sonnet')?.providerCode, 'anthropic')
  assert.equal(context.endpointModel('responses', 'gpt-5.6')?.providerCode, 'gpt')
  assert.equal(captured.length, 2, '混合供应商应一次解析涉及协议、一次批量读取所需模型')
  assert.deepEqual(captured[0]!.params, ['openai', 'v1', 'anthropic', 'v1', 'gemini', 'v1beta'])
  assertNarrowCatalogProjection(captured[1]!.sql)
}

async function assertPostgresScalarParameterShape(): Promise<void> {
  const captured: CapturedQuery[] = []
  const client = recordingClient('postgres', captured, () => [validationRow('gpt', 'gpt-5.6', ['responses'])])
  await loadAccountModelValidationContextAsync(client, {
    providerCode: 'gpt',
    systemAccountId: 'sys_user',
    models: ['gpt-5.6']
  })
  assert.equal(captured.length, 1)
  assert.match(captured[0]!.sql, /IN \(\$1\)/)
  assert.match(captured[0]!.sql, /IN \(\$2\)/)
  assert.match(captured[0]!.sql, /IN \(\$3\)/)
  assert.match(captured[0]!.sql, /IN \(\$4\)/)
  assert.match(captured[0]!.sql, /system_account_id = \$5/)
  assert.equal(captured[0]!.params.some(Array.isArray), false, 'PostgreSQL 参数必须使用标量占位符，不依赖数组隐式转换')
  assert.match(captured[0]!.sql, /"juhe_business"\."provider_model_catalog"/)
}

async function assertManagementPathsReuseValidationContext(): Promise<void> {
  const repositoriesSource = await readFile(new URL('../../storage/repositories.ts', import.meta.url), 'utf8')
  const patchSource = await readFile(new URL('../../storage/account-management-patch.repository.ts', import.meta.url), 'utf8')
  assert.match(repositoriesSource, /requireEnabledProviderProtocolProfileInClientAsync\(client,/)
  assert.match(repositoriesSource, /loadAccountModelValidationContextAsync\(client,/)
  assert.match(repositoriesSource, /validationContext: modelValidationContext/)
  assert.equal((patchSource.match(/loadAccountModelValidationContextAsync\(client,/g) ?? []).length, 1)
  assert.match(patchSource, /validationContext: modelValidationContext/)
  assert.doesNotMatch(patchSource, /client\.driver === 'postgres'[\s\S]{0,200}normalizeAccountSupportedModelsForProvider/)
}

function assertNarrowCatalogProjection(sql: string): void {
  assert.doesNotMatch(sql, /SELECT\s+\*/i)
  for (const field of ['context_window_tokens', 'max_input_tokens', 'pricing_notes', 'capability_notes', 'notes', 'created_at', 'updated_at']) {
    assert.equal(sql.includes(field), false, `模型存在性校验不应读取 ${field}`)
  }
  assert.match(sql, /supported_api_protocols_json/)
  assert.match(sql, /supported_service_tiers_json/)
  assert.match(sql, /supported_reasoning_efforts_json/)
}

function validationRow(providerCode: string, model: string, protocols: string[]): ValidationRow {
  return {
    provider_code: providerCode,
    model,
    mode: null,
    supported_api_protocols_json: JSON.stringify(protocols),
    supported_service_tiers_json: '["priority"]',
    supported_reasoning_efforts_json: '["medium"]',
    scope_priority: 1,
    priced: 1
  }
}

function recordingClient(
  driver: DatabaseClientDriver,
  captured: CapturedQuery[],
  rows: (sql: string, params: readonly unknown[]) => object[]
): DatabaseClient {
  const dialect: SqlDialect = driver === 'postgres' ? postgresDialect : sqliteDialect
  const query = async <T extends object>(sql: string, params: readonly unknown[] = []): Promise<T[]> => {
    const bound = dialect.bind(sql, params)
    captured.push(bound)
    return rows(bound.sql, bound.params) as T[]
  }
  const client: DatabaseClient = {
    driver,
    dialect,
    query,
    async one<T extends object>(sql: string, params: readonly unknown[] = []): Promise<T | undefined> {
      return (await query<T>(sql, params))[0]
    },
    async execute(): Promise<ExecuteResult> {
      throw new Error('execute is not expected')
    },
    async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
      return operation(client)
    }
  }
  return client
}

await main()
