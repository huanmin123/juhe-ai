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
  assertAccountHealthCheckModelRuntimeBoundary()
  assertDefaultHealthCheckModelRoleLookupBoundary()
  assertModelCatalogPostgresSyncBoundary()
  assertProviderEnabledPredicateBoundary()
  assertOpenAIAccountSelectorPostgresAuthorizationBoundary()

  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

  const databaseModule = await import('../../storage/database.js')
  databaseModule.getBusinessDatabase()
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

function assertProviderEnabledPredicateBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const source = readFileSync(join(srcRoot, 'storage/provider.repository.ts'), 'utf8')
  assert.match(
    source,
    /function providerEnabledPredicate\(client: DatabaseClient, column: string\): string \{[\s\S]*?client\.driver === 'postgres' \? 'TRUE' : '1'/,
    '供应商 enabled 谓词必须匹配 SQLite INTEGER 与 PostgreSQL boolean 的实际列类型'
  )
  assert.doesNotMatch(
    source,
    /function providerEnabledPredicate\(_client:[\s\S]{0,200}return `\$\{column\} = 1`/,
    'PostgreSQL boolean enabled 字段不得与 integer 1 比较'
  )
  assert.equal(
    source.match(/providerEnabledPredicate\(client, '[^']+'\)/g)?.length,
    11,
    '全部供应商异步 enabled 查询都必须复用方言谓词'
  )

  const migrationRoot = join(srcRoot, '..', '..', 'backend-go', 'db', 'migrations')
  const providerSchema = readFileSync(join(migrationRoot, '000004_w1b_public_groups.sql'), 'utf8')
  const protocolSchema = readFileSync(join(migrationRoot, '000005_w1b_public_accounts.sql'), 'utf8')
  const endpointFamilySchema = readFileSync(join(migrationRoot, '000008_w2_management_provider_options.sql'), 'utf8')
  for (const [table, migration] of [
    ['providers', providerSchema],
    ['provider_protocol_profiles', protocolSchema],
    ['protocol_endpoint_families', endpointFamilySchema],
    ['provider_protocol_profile_families', endpointFamilySchema]
  ] as const) {
    assert.match(
      migration,
      new RegExp(`CREATE TABLE IF NOT EXISTS juhe_business\\.${table} \\([\\s\\S]*?enabled boolean NOT NULL DEFAULT true`),
      `Goose PostgreSQL ${table}.enabled 必须保持 boolean`
    )
  }
}

function assertModelCatalogPostgresSyncBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const source = readFileSync(join(srcRoot, 'modules/model-pricing/model-catalog.service.ts'), 'utf8')
  assert.ok(
    source.includes('postgresSyncOpenAIProtocolProviderCodes')
      && source.includes("runtimeConfig.databaseDriver === 'postgres'"),
    '模型目录同步路径在 PG 模式下必须使用内置 provider code 列表'
  )
  const postgresProviderCodes = source.match(/const postgresSyncOpenAIProtocolProviderCodes = \[([\s\S]*?)\] as const/)?.[1] ?? ''
  assert.match(
    postgresProviderCodes,
    /XAI_PROVIDER_CODE/,
    'PostgreSQL OpenAI 协议聚合必须包含 xAI 模型目录'
  )
  assert.doesNotMatch(
    source,
    /function modelCatalogSourceProviderCodes\([\s\S]*?listOpenAIProtocolProviderCodes\(\)[\s\S]*?runtimeConfig\.databaseDriver === 'postgres'/,
    '模型目录同步路径在 PG 模式下不得先调用 provider.repository'
  )
}

function assertDefaultHealthCheckModelRoleLookupBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const source = readFileSync(join(srcRoot, 'storage/provider.repository.ts'), 'utf8')
  const roleLookupQueries = source.match(/SELECT role\s+FROM [\s\S]*?WHERE id = \?\s+LIMIT 1/g) ?? []
  assert.equal(
    roleLookupQueries.length,
    2,
    '供应商默认检查模型的同步与异步角色判定都必须按 system_accounts.id 有界读取并 LIMIT 1'
  )
  assert.match(
    source,
    /async function shouldReadProviderDefaultHealthCheckModelPreferenceAsync[\s\S]*?const client = await getProviderDatabaseClient\(\)[\s\S]*?FROM \$\{providerTable\(client, 'system_accounts'\)\}[\s\S]*?WHERE id = \?[\s\S]*?LIMIT 1/,
    'PostgreSQL 角色判定必须使用统一 DatabaseClient 和 schema-aware system_accounts 表名'
  )
}

function assertAccountHealthCheckModelRuntimeBoundary(): void {
  const srcRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')
  const accountTestServiceSource = readFileSync(join(srcRoot, 'modules/accounts/account-test.service.ts'), 'utf8')
  assert.ok(
    accountTestServiceSource.includes('export async function resolveAccountTestModelAsync')
      && accountTestServiceSource.includes('const healthCheckModel = stringValue(account.healthCheckModel)')
      && accountTestServiceSource.includes('if (!supportedModels.includes(healthCheckModel))')
      && accountTestServiceSource.includes('return healthCheckModel'),
    '系统复测必须严格读取账户检查模型并验证其属于支持模型'
  )
  assert.ok(
    accountTestServiceSource.includes('const explicitModel = stringValue(input.explicitModel)')
      && accountTestServiceSource.includes('if (explicitModel) return explicitModel'),
    '人工测试显式模型应只作为本次诊断输入'
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
    /\bfindProviderDefaultHealthCheckModel\b(?!Async)/,
    '账号测试服务不得在运行时回退同步供应商默认检查模型读取入口'
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
    assert.doesNotMatch(source, /preferredSystemAccountTestModelAsync/, `${relativePath} 应把模型优先级交给通用账号测试服务`)
    assert.ok(
      source.includes('testOpenAIAccount') || source.includes('resolveAccountTestModelAsync'),
      `${relativePath} 应复用通用账号测试模型解析入口`
    )
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
    const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
    await readWorkerPool.closeSqliteReadWorkerPool()
  } catch {
    // The regression may fail before the SQLite read worker pool is imported.
  }
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
  assert.ok((await repositories.findProviderDefaultSupportedModelsAsync(GPT_VENDOR_CODE)).includes('gpt-5.6-sol'), '统一 repository 出口应暴露默认支持模型读取')
}

async function assertProviderRepositoryAsync(repository: typeof import('../../storage/provider.repository.js')): Promise<void> {
  const providers = await repository.listProvidersAsync()
  assert.ok(providers.length >= 6, '应读取默认内置供应商')

  const gpt = providers.find((provider) => provider.code === GPT_VENDOR_CODE)
  assert.ok(gpt, '应包含 GPT 供应商')
  assert.equal(gpt.enabled, true, 'GPT 供应商应启用')
  assert.equal(gpt.protocolCode, OPENAI_PROTOCOL_CODE, 'GPT 默认协议应为 OpenAI')
  assert.equal(gpt.protocolVersion, OPENAI_PROTOCOL_VERSION, 'GPT 默认协议版本应为 OpenAI v1')
  assert.ok(gpt.defaultSupportedModels.includes('gpt-5.6-sol'), 'GPT 应返回默认支持模型')
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

  const defaultModel = await repository.findProviderDefaultHealthCheckModelAsync(GPT_VENDOR_CODE)
  assert.ok(defaultModel, '应能读取供应商默认检查模型')
  await assertDefaultHealthCheckModelRolePriorityAsync(repository, defaultModel)
  assert.ok((await repository.findProviderDefaultSupportedModelsAsync(GPT_VENDOR_CODE)).includes('gpt-5.6-sol'), '应能读取供应商默认支持模型')
  assert.equal(await repository.findProviderDefaultHealthCheckModelAsync(ANTHROPIC_PROVIDER_CODE), 'claude-opus-4-8', 'Anthropic 默认检查模型应使用 Opus 4.8')

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

async function assertDefaultHealthCheckModelRolePriorityAsync(
  repository: typeof import('../../storage/provider.repository.js'),
  fallbackModel: string
): Promise<void> {
  const marker = `provider_health_role_${process.pid}_${Date.now()}`
  const fixtures = {
    user: {
      id: `${marker}_user`,
      username: `${marker}_user`,
      displayName: `${marker} 普通用户`,
      role: 'user',
      model: `${marker}_user_model`
    },
    admin: {
      id: `${marker}_admin`,
      username: `${marker}_admin`,
      displayName: `${marker} 管理员`,
      role: 'admin',
      model: `${marker}_admin_legacy_model`
    },
    superAdmin: {
      id: `${marker}_super_admin`,
      username: `${marker}_super_admin`,
      displayName: `${marker} 超级管理员`,
      role: 'super_admin',
      model: `${marker}_super_admin_legacy_model`
    }
  } as const

  try {
    await seedDefaultHealthCheckModelRoleFixturesAsync(fixtures)
    const sqliteReadWorkerPool = process.env.JUHE_AI_DATABASE_DRIVER === 'postgres'
      ? undefined
      : await import('../../storage/sqlite-read-worker-pool.js')
    const handledJobsBefore = sqliteReadWorkerPool?.getSqliteReadWorkerPoolRuntime().handledJobs
    assert.equal(
      await repository.findProviderDefaultHealthCheckModelAsync(GPT_VENDOR_CODE, fixtures.user.id),
      fixtures.user.model,
      '普通用户应保持个人偏好优先于系统默认和协议档案'
    )
    assert.equal(
      await repository.findProviderDefaultHealthCheckModelAsync(GPT_VENDOR_CODE, fixtures.admin.id),
      fallbackModel,
      '管理员应忽略遗留个人偏好并回退系统默认或协议档案'
    )
    assert.equal(
      await repository.findProviderDefaultHealthCheckModelAsync(GPT_VENDOR_CODE, fixtures.superAdmin.id),
      fallbackModel,
      '超级管理员应忽略遗留个人偏好并回退系统默认或协议档案'
    )

    if (sqliteReadWorkerPool && handledJobsBefore !== undefined) {
      assert.equal(sqliteReadWorkerPool.sqliteReadWorkerPoolEnabled(), true, 'SQLite provider repository 回归必须启用 read worker')
      assert.ok(
        sqliteReadWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= handledJobsBefore + 3,
        'SQLite 默认检查模型 async 角色优先级读取必须由 read worker 执行'
      )
      assert.equal(
        repository.findProviderDefaultHealthCheckModel(GPT_VENDOR_CODE, fixtures.user.id),
        fixtures.user.model,
        'SQLite 同步路径应保持普通用户个人偏好优先'
      )
      assert.equal(
        repository.findProviderDefaultHealthCheckModel(GPT_VENDOR_CODE, fixtures.admin.id),
        fallbackModel,
        'SQLite 同步路径应忽略管理员遗留个人偏好'
      )
      assert.equal(
        repository.findProviderDefaultHealthCheckModel(GPT_VENDOR_CODE, fixtures.superAdmin.id),
        fallbackModel,
        'SQLite 同步路径应忽略超级管理员遗留个人偏好'
      )
    }
  } finally {
    await cleanupDefaultHealthCheckModelRoleFixturesAsync(Object.values(fixtures).map((fixture) => fixture.id))
  }
}

async function seedDefaultHealthCheckModelRoleFixturesAsync(fixtures: Record<string, {
  id: string
  username: string
  displayName: string
  role: 'user' | 'admin' | 'super_admin'
  model: string
}>): Promise<void> {
  const now = new Date().toISOString()
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const { getPostgresPool } = await import('../../storage/postgres-client.js')
    const pool = await getPostgresPool()
    for (const fixture of Object.values(fixtures)) {
      await pool.query(`
        INSERT INTO juhe_business.system_accounts (
          id, username, display_name, role, status, password_hash,
          must_change_password, image_generation_enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, 'active', 'provider-repository-regression', 0, 0, $5, $5)
      `, [fixture.id, fixture.username, fixture.displayName, fixture.role, now])
      await pool.query(`
        INSERT INTO juhe_business.provider_default_health_check_models (
          system_account_id, provider_code, model, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $4)
      `, [fixture.id, GPT_VENDOR_CODE, fixture.model, now])
    }
    return
  }

  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  const insertSystemAccount = database.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash,
      must_change_password, image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, 'active', 'provider-repository-regression', 0, 0, ?, ?)
  `)
  const insertPreference = database.prepare(`
    INSERT INTO provider_default_health_check_models (
      system_account_id, provider_code, model, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?)
  `)
  for (const fixture of Object.values(fixtures)) {
    insertSystemAccount.run(fixture.id, fixture.username, fixture.displayName, fixture.role, now, now)
    insertPreference.run(fixture.id, GPT_VENDOR_CODE, fixture.model, now, now)
  }
}

async function cleanupDefaultHealthCheckModelRoleFixturesAsync(systemAccountIds: string[]): Promise<void> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const { getPostgresPool } = await import('../../storage/postgres-client.js')
    const pool = await getPostgresPool()
    await pool.query(
      'DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])',
      [systemAccountIds]
    )
    return
  }

  const { getBusinessDatabase } = await import('../../storage/database.js')
  const statement = getBusinessDatabase().prepare('DELETE FROM system_accounts WHERE id = ?')
  for (const systemAccountId of systemAccountIds) {
    statement.run(systemAccountId)
  }
}
