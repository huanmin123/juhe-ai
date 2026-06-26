import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
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
        JUHE_AI_POSTGRES_URL: process.env.JUHE_PROVIDER_REPOSITORY_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_PROVIDER_REPOSITORY_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_PROVIDER_REPOSITORY_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
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
}

async function assertProviderRepositoryAsync(repository: typeof import('../../storage/provider.repository.js')): Promise<void> {
  const providers = await repository.listProvidersAsync()
  assert.ok(providers.length >= 6, '应读取默认内置供应商')

  const gpt = providers.find((provider) => provider.code === GPT_VENDOR_CODE)
  assert.ok(gpt, '应包含 GPT 供应商')
  assert.equal(gpt.enabled, true, 'GPT 供应商应启用')
  assert.equal(gpt.protocolCode, OPENAI_PROTOCOL_CODE, 'GPT 默认协议应为 OpenAI')
  assert.equal(gpt.protocolVersion, OPENAI_PROTOCOL_VERSION, 'GPT 默认协议版本应为 OpenAI v1')
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
