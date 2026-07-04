import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  ANTHROPIC_PROVIDER_CODE,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  GEMINI_PROVIDER_CODE,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GPT_VENDOR_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION
} from '../../domain/provider-protocol.js'

if (process.env.JUHE_PROVIDER_REPOSITORY_DRIVER_CHILD === 'postgres') {
  const repository = await import('../../storage/provider.repository.js')
  await assertProviderRepositoryAsync(repository)
  await assertRepositoryBarrelAsync()
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-provider-driver-'))
try {
  assertAccountTestDefaultModelRuntimeBoundary()
  assertModelCatalogPostgresSyncBoundary()
  assertOpenAIAccountSelectorPostgresAuthorizationBoundary()

  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

  const repository = await import('../../storage/provider.repository.js')
  await assertProviderRepositoryAsync(repository)
  await assertRepositoryBarrelAsync()

  if (process.env.JUHE_PROVIDER_REPOSITORY_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_PROVIDER_REPOSITORY_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_PROVIDER_REPOSITORY_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_PROVIDER_REPOSITORY_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_PROVIDER_REPOSITORY_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_PROVIDER_REPOSITORY_REDIS_QUEUE_URL ?? process.env.JUHE_PROVIDER_REPOSITORY_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('provider-repository-driver-regression passed')
} finally {
  await closeSqliteStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertModelCatalogPostgresSyncBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const source = readFileSync(join(srcRoot, 'modules/model-pricing/model-catalog.service.ts'), 'utf8')
  assert.ok(
    source.includes('postgresSyncOpenAIProtocolProviderCodes')
      && source.includes("runtimeConfig.databaseDriver === 'postgres'"),
    '模型目录同步路径在 PG 模式下必须使用内置 provider code 列表'
  )
  assert.doesNotMatch(
    source,
    /function modelCatalogSourceProviderCodes\([\s\S]*?listOpenAIProtocolProviderCodes\(\)[\s\S]*?runtimeConfig\.databaseDriver === 'postgres'/,
    '模型目录同步路径在 PG 模式下不得先调用 provider.repository'
  )
}

function assertAccountTestDefaultModelRuntimeBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const accountTestServiceSource = readFileSync(join(srcRoot, 'modules/accounts/account-test.service.ts'), 'utf8')
  assert.ok(
    accountTestServiceSource.includes('findProviderDefaultTestModelAsync')
      && accountTestServiceSource.includes('await defaultAccountTestModelAsync'),
    '账号测试默认模型应提供 async 读取路径，避免 PG 模式回退 SQLite'
  )
  assert.ok(
    accountTestServiceSource.includes('findAccountForTestAsync')
      && accountTestServiceSource.includes('input.findAccountForTest ?? findAccountForTestAsync'),
    '账号测试状态回读默认路径必须使用 async 账户读取，避免 PG 模式回退 SQLite'
  )
  const accountTestRepositoryImport = accountTestServiceSource.match(/import\s*\{[\s\S]*?\}\s*from '\.\.\/\.\.\/storage\/repositories\.js'/)?.[0] ?? ''
  assert.doesNotMatch(
    accountTestRepositoryImport,
    /\bfindAccountForTest\b(?!Async)/,
    '账号测试服务不得从 repository 导入同步 findAccountForTest'
  )
  assert.doesNotMatch(
    accountTestServiceSource,
    /\bfindProviderDefaultTestModel\b(?!Async)/,
    '账号测试服务不得导入或调用同步供应商默认测试模型读取入口'
  )
  assert.doesNotMatch(
    accountTestServiceSource,
    /\bexport function preferredSystemAccountTestModel\b/,
    '账号测试服务不得继续暴露同步默认测试模型入口'
  )
  assert.doesNotMatch(
    accountTestServiceSource,
    /\bfunction defaultAccountTestModel\b(?!Async)/,
    '账号测试服务不得保留同步默认测试模型 fallback'
  )

  for (const relativePath of [
    'modules/background/cooldown-account-retest.service.ts',
    'modules/background/account-health-check.service.ts',
    'modules/background/account-quality-failure-precheck.service.ts',
    'modules/background/account-api-key-cooldown-retest.service.ts',
    'modules/accounts/account-test-task-queue.service.ts',
    'modules/gateway/client-profiles/codex-switch-probe.ts',
    'modules/gateway/runtime/account-side-effects.service.ts'
  ]) {
    const source = readFileSync(join(srcRoot, relativePath), 'utf8')
    assert.ok(source.includes('preferredSystemAccountTestModelAsync'), `${relativePath} 应使用 async 默认测试模型读取入口`)
    assert.doesNotMatch(source, /\bpreferredSystemAccountTestModel\(/, `${relativePath} 不得在运行路径调用同步默认测试模型读取入口`)
  }
}

function assertOpenAIAccountSelectorPostgresAuthorizationBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const source = readFileSync(join(srcRoot, 'storage/openai-account-selector.repository.ts'), 'utf8')
  assert.match(
    source,
    /export async function findOpenAIAccountForGroupAsync[\s\S]*?const accountAuthorizationsByIdOrResourceId = await loadAccountAuthorizationsForSelectionAsync\(client, \[selectionRow\], groupAccess, systemAccountId\) \?\? new Map\(\)/,
    'PG 单账号候选读取必须把空授权预加载结果规范成 Map，避免回落同步 SQLite 授权查询'
  )
  assert.match(
    source,
    /export async function listOpenAIAccountsForGroupResultAsync[\s\S]*?const accountAuthorizationsByIdOrResourceId = await loadAccountAuthorizationsForSelectionAsync\(client, groupAccountRows, groupAccess, systemAccountId\) \?\? new Map\(\)/,
    'PG 分组候选读取必须把空授权预加载结果规范成 Map，避免回落同步 SQLite 授权查询'
  )
  assert.match(
    source,
    /function resolveOpenAIAccountAccess[\s\S]*?accountAuthorizationsByIdOrResourceId\s*\?[\s\S]*?accountAuthorizationsByIdOrResourceId\.get\([\s\S]*?:\s*activeResourceAuthorizationById/,
    '授权解析仍应保留 standalone 同步兜底，但 PG async 调用方必须传入 Map 阻断该兜底'
  )
}

async function closeSqliteStorageDatabases(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}

async function assertRepositoryBarrelAsync(): Promise<void> {
  const repositories = await import('../../storage/repositories.js')
  const providers = await repositories.listProvidersAsync()
  assert.ok(providers.some((provider) => provider.code === GPT_VENDOR_CODE), '统一 repository 出口应暴露异步供应商读取')
  assert.ok((await repositories.findProviderDefaultSupportedModelsAsync(GPT_VENDOR_CODE)).includes('gpt-5.5'), '统一 repository 出口应暴露默认支持模型读取')
}

async function assertProviderRepositoryAsync(repository: typeof import('../../storage/provider.repository.js')): Promise<void> {
  const providers = await repository.listProvidersAsync()
  assert.ok(providers.length >= 6, '应读取默认内置供应商')

  const gpt = providers.find((provider) => provider.code === GPT_VENDOR_CODE)
  assert.ok(gpt, '应包含 GPT 供应商')
  assert.equal(gpt.enabled, true, 'GPT 供应商应启用')
  assert.equal(gpt.protocolCode, OPENAI_PROTOCOL_CODE, 'GPT 默认协议应为 OpenAI')
  assert.equal(gpt.protocolVersion, OPENAI_PROTOCOL_VERSION, 'GPT 默认协议版本应为 OpenAI v1')
  assert.ok(gpt.defaultSupportedModels.includes('gpt-5.5'), 'GPT 应返回默认支持模型')
  assert.ok(gpt.protocolProfiles.length >= 1, 'GPT 应包含协议档案')
  assert.ok(gpt.protocolProfiles.some((profile) => profile.endpointFamilies.some((family) => family.code === 'responses')), 'GPT 协议档案应包含 Responses endpoint family')

  const openAIProviderCodes = await repository.listOpenAIProtocolProviderCodesAsync()
  assert.ok(openAIProviderCodes.includes(GPT_VENDOR_CODE), 'OpenAI 协议供应商列表应包含 GPT')

  const anthropicProviderCodes = await repository.listAnthropicProtocolProviderCodesAsync()
  assert.ok(anthropicProviderCodes.length >= 1, 'Anthropic 协议供应商列表不应为空')

  const geminiProviderCodes = await repository.listGeminiProtocolProviderCodesAsync()
  assert.ok(geminiProviderCodes.includes(GEMINI_PROVIDER_CODE), 'Gemini 协议供应商列表应包含 Gemini')

  const openAIProfileIds = await repository.listOpenAIProtocolProfileIdsAsync()
  assert.ok(openAIProfileIds.some((profileId) => profileId.includes('openai')), 'OpenAI 协议档案 ID 列表应包含 OpenAI 相关档案')

  assert.equal(await repository.isOpenAIProtocolProviderCodeAsync(GPT_VENDOR_CODE), true, 'GPT 应支持 OpenAI 协议')
  assert.equal(await repository.isProtocolProviderCodeAsync(GEMINI_PROVIDER_CODE, GEMINI_PROTOCOL_CODE, GEMINI_PROTOCOL_VERSION), true, 'Gemini 应支持 Gemini 原生协议')
  assert.equal(await repository.isProtocolProviderCodeAsync(GPT_VENDOR_CODE, ANTHROPIC_PROTOCOL_CODE, ANTHROPIC_PROTOCOL_VERSION), false, 'GPT 不应被误判为 Anthropic 协议供应商')

  const defaultModel = await repository.findProviderDefaultTestModelAsync(GPT_VENDOR_CODE)
  assert.ok(defaultModel, '应能读取供应商默认测试模型')
  assert.ok((await repository.findProviderDefaultSupportedModelsAsync(GPT_VENDOR_CODE)).includes('gpt-5.5'), '应能读取供应商默认支持模型')
  assert.equal(await repository.findProviderDefaultTestModelAsync(ANTHROPIC_PROVIDER_CODE), 'claude-opus-4-8', 'Anthropic 默认测试模型应使用 Opus 4.8')

  const defaultProfile = await repository.defaultProviderProtocolProfileAsync(GPT_VENDOR_CODE)
  assert.ok(defaultProfile, '应能读取默认协议档案')
  assert.equal(defaultProfile.providerCode, GPT_VENDOR_CODE)

  const foundProfile = await repository.findProviderProtocolProfileAsync(defaultProfile.id)
  assert.equal(foundProfile?.id, defaultProfile.id, '应能按 ID 读取协议档案')

  const requiredProfile = await repository.requireEnabledProviderProtocolProfileAsync(GPT_VENDOR_CODE, defaultProfile.id)
  assert.equal(requiredProfile.id, defaultProfile.id, '应能校验启用协议档案')

  await assert.rejects(
    repository.requireEnabledProviderProtocolProfileAsync('missing-provider', undefined),
    /不支持的供应商/
  )
}
