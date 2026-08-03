import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import {
  ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES,
  resolveDefaultHealthCheckEndpointMode,
  resolveHealthCheckEndpointMode
} from '../../domain/account-health-check-endpoint-mode.js'
import { accountManualTestEndpointModes } from '../../modules/accounts/account-test-endpoint-modes.js'
import { accountManualTestEndpointModesForModel } from '../../modules/accounts/account-test-options.service.js'
import type { AccountSummary } from '../../domain/types.js'
import type { ProviderModelCatalogItem } from '../../modules/model-pricing/model-catalog.service.js'
import {
  normalizeGptHealthCheckCredentials,
  resolveLegacyHealthCheckEndpointMode
} from '../maintenance/account-health-check-endpoint-mode-migration.js'
import { applyBusinessSchema } from '../../storage/schema/business-schema.js'

assert.deepEqual(ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES, [
  'images_json',
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse',
  'interactions_json',
  'interactions_sse'
])

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
}), 'responses_sse')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['chat_json', 'chat_sse']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  enabledEndpointModes: ['responses_json', 'chat_json']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  enabledEndpointModes: ['chat_json', 'responses_json']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'glm',
  providerProtocolProfileId: 'profile_glm_coding_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse']
}), 'messages_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse']
}), 'messages_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['generate_content_json', 'generate_content_sse']
}), 'generate_content_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_openai_chat_v1beta',
  enabledEndpointModes: ['chat_json', 'chat_sse']
}), 'chat_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['messages_sse', 'generate_content_json', 'chat_json']
}), 'generate_content_json')

assert.equal(resolveDefaultHealthCheckEndpointMode({
  providerCode: 'openai',
  providerProtocolProfileId: 'profile_openai_openai_v1',
  enabledEndpointModes: ['chat_sse', 'responses_sse']
}), 'chat_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'responses_sse',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['responses_sse']
}), 'responses_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'messages_sse',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['messages_json', 'messages_sse', 'message_token_counting']
}), 'messages_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'generate_content_sse',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['generate_content_json', 'generate_content_sse', 'count_tokens', 'embed_content']
}), 'generate_content_sse')

assert.equal(resolveHealthCheckEndpointMode({
  value: 'images_json',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['responses_json', 'responses_sse'],
  modelSupportsImages: true
}), 'images_json', '模型目录证实图片能力时，健康检查必须接受 Images API 形态')

assert.throws(() => resolveHealthCheckEndpointMode({
  value: 'images_json',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['responses_json', 'responses_sse'],
  modelSupportsImages: false
}), /模型目录证实支持 Images API/, '文本模型或缺失目录证据不得选择 Images API')

assert.throws(() => resolveHealthCheckEndpointMode({
  value: 'responses_json',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  enabledEndpointModes: ['responses_sse']
}), /未启用/)

assert.throws(() => resolveHealthCheckEndpointMode({
  value: 'count_tokens',
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  enabledEndpointModes: ['count_tokens']
}), /请求形态无效/)

assert.throws(() => resolveDefaultHealthCheckEndpointMode({
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  enabledEndpointModes: ['message_token_counting']
}), /至少需要启用一个可用于健康检查的请求形态/)

assert.deepEqual(accountManualTestEndpointModes({
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1',
  type: 'api_key',
  clientCompatibility: 'openai_standard',
  healthCheckEndpointMode: 'messages_sse',
  credentials: {
    supported_endpoint_modes: ['messages_json', 'messages_sse', 'message_token_counting']
  }
}), ['messages_sse', 'messages_json'], 'Anthropic 保存流式检查时仍应返回已启用 JSON，并排除 Count tokens')

assert.deepEqual(accountManualTestEndpointModes({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  type: 'api_key',
  clientCompatibility: 'openai_standard',
  healthCheckEndpointMode: 'generate_content_sse',
  credentials: {
    supported_endpoint_modes: ['generate_content_json', 'generate_content_sse', 'count_tokens', 'embed_content']
  }
}), ['generate_content_sse', 'generate_content_json'], 'Gemini 保存流式检查时仍应返回已启用 JSON，并排除计数和嵌入接口')

assert.deepEqual(accountManualTestEndpointModes({
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  type: 'google_oauth',
  clientCompatibility: 'openai_standard',
  healthCheckEndpointMode: 'interactions_sse',
  credentials: {
    supported_endpoint_modes: ['interactions_json', 'interactions_sse', 'generate_content_json', 'generate_content_sse']
  }
}), ['interactions_sse', 'interactions_json', 'generate_content_json', 'generate_content_sse'], 'Gemini Interactions 检查应优先返回保存的精确 mode，并保留同账户的 Generate Content mode')

assert.deepEqual(accountManualTestEndpointModes({
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'oauth',
  clientCompatibility: 'codex_responses',
  healthCheckEndpointMode: 'responses_sse',
  credentials: {
    supported_endpoint_modes: ['responses_json', 'responses_sse']
  }
}), ['responses_sse', 'responses_json'], 'OAuth 保存流式检查时仍应返回已启用 Responses JSON')

assert.deepEqual(accountManualTestEndpointModes({
  providerCode: 'hybrid',
  providerProtocolProfileId: 'profile_hybrid_openai_chat_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  clientCompatibility: 'openai_standard',
  healthCheckEndpointMode: 'messages_sse',
  credentials: {
    supported_endpoint_modes: ['chat_json', 'messages_sse', 'generate_content_sse', 'count_tokens']
  }
}), ['messages_sse', 'chat_json', 'generate_content_sse'], '混合供应商应返回全部已启用生成 mode，并排除工具接口')

const hybridModelAccount = {
  providerCode: 'hybrid',
  providerProtocolProfileId: 'profile_hybrid_openai_chat_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  clientCompatibility: 'openai_standard',
  healthCheckEndpointMode: 'messages_sse',
  credentials: { supported_endpoint_modes: ['chat_json', 'messages_sse', 'responses_sse'] },
  modelMappings: [{
    sourceModel: 'claude-source',
    sourceEndpointFamily: 'messages',
    upstreamModel: 'chat-upstream',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }]
} as AccountSummary
const modelCatalog = [
  { model: 'claude-source', status: 'active', supportedApiProtocols: ['messages'] },
  { model: 'chat-upstream', status: 'active', supportedApiProtocols: ['chat_completions'] },
  { model: 'responses-only', status: 'active', supportedApiProtocols: ['responses'] }
] as ProviderModelCatalogItem[]
assert.deepEqual(
  accountManualTestEndpointModesForModel(hybridModelAccount, modelCatalog[0]!, modelCatalog),
  ['messages_sse'],
  '混合供应商合法 Messages 到 Chat 映射必须保留 source mode，不能按上游 Chat 协议误杀'
)
assert.deepEqual(
  accountManualTestEndpointModesForModel(hybridModelAccount, modelCatalog[2]!, modelCatalog),
  ['responses_sse'],
  '无映射模型只能展示模型目录和账户能力共同支持的 mode'
)

const geminiInteractionsAccount = {
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta',
  type: 'google_oauth',
  clientCompatibility: 'openai_standard',
  healthCheckEndpointMode: 'interactions_sse',
  credentials: { supported_endpoint_modes: ['interactions_json', 'interactions_sse'] }
} as AccountSummary
const geminiInteractionsModel = {
  model: 'gemini-interactions-only',
  status: 'active',
  supportedApiProtocols: ['interactions']
} as ProviderModelCatalogItem
assert.deepEqual(
  accountManualTestEndpointModesForModel(geminiInteractionsAccount, geminiInteractionsModel, [geminiInteractionsModel]),
  ['interactions_sse', 'interactions_json'],
  'Gemini Interactions-only 模型必须保留 Interactions 人工测试 mode'
)

assert.deepEqual(normalizeGptHealthCheckCredentials({}, 'oauth'), {
  credentials: {
    supported_endpoint_modes: ['responses_json', 'responses_sse']
  },
  changed: true
})

assert.deepEqual(normalizeGptHealthCheckCredentials({}, 'api_key'), {
  credentials: {
    supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
  },
  changed: true
})

assert.deepEqual(normalizeGptHealthCheckCredentials({
  supported_endpoint_modes: ['messages_sse']
}, 'api_key'), {
  credentials: {
    supported_endpoint_modes: ['messages_sse', 'responses_sse']
  },
  changed: true
})

const alreadyNormalizedCredentials = {
  supported_endpoint_modes: ['responses_json', 'responses_sse']
}
assert.deepEqual(normalizeGptHealthCheckCredentials(alreadyNormalizedCredentials, 'oauth'), {
  credentials: alreadyNormalizedCredentials,
  changed: false
})
assert.throws(
  () => normalizeGptHealthCheckCredentials({ supported_endpoint_modes: 'responses_sse' }, 'oauth'),
  /必须是字符串数组/
)
assert.throws(
  () => normalizeGptHealthCheckCredentials({ supported_endpoint_modes: ['responses_json', 1] }, 'oauth'),
  /必须是非空字符串数组/
)

for (const fixture of [
  {
    legacyFamily: 'chat_completions' as const,
    supportedMode: 'chat_sse',
    expectedMode: 'chat_sse'
  },
  {
    legacyFamily: 'messages' as const,
    supportedMode: 'messages_sse',
    expectedMode: 'messages_sse'
  },
  {
    legacyFamily: 'generate_content' as const,
    supportedMode: 'generate_content_sse',
    expectedMode: 'generate_content_sse'
  }
]) {
  assert.equal(resolveLegacyHealthCheckEndpointMode({
    accountId: `account_${fixture.legacyFamily}_sse_only`,
    accountType: 'api_key',
    providerCode: 'hybrid',
    legacyFamily: fixture.legacyFamily,
    credentials: {
      supported_endpoint_modes: [fixture.supportedMode, 'count_tokens', 'embed_content']
    }
  }), fixture.expectedMode, `${fixture.legacyFamily} 仅启用 Streaming 时迁移不得回落到 JSON`)
}

assert.equal(resolveLegacyHealthCheckEndpointMode({
  accountId: 'account_chat_json_preferred',
  accountType: 'api_key',
  providerCode: 'openai',
  legacyFamily: 'chat_completions',
  credentials: {
    supported_endpoint_modes: ['chat_sse', 'chat_json']
  }
}), 'chat_json', '同族 JSON 与 Streaming 都启用时迁移应优先 JSON')

assert.equal(resolveLegacyHealthCheckEndpointMode({
  accountId: 'account_legacy_chat_actual_messages_streaming',
  accountType: 'api_key',
  providerCode: 'hybrid',
  legacyFamily: 'chat_completions',
  credentials: {
    supported_endpoint_modes: ['messages_sse']
  }
}), 'messages_sse', '历史 family 与能力错配时应回退到真实启用的跨 family Streaming 生成 mode')

assert.equal(resolveLegacyHealthCheckEndpointMode({
  accountId: 'account_legacy_chat_actual_cross_family_json_and_streaming',
  accountType: 'api_key',
  providerCode: 'hybrid',
  legacyFamily: 'chat_completions',
  credentials: {
    supported_endpoint_modes: ['generate_content_sse', 'responses_json', 'messages_json']
  }
}), 'responses_json', '跨 family 回退必须先按稳定顺序选择 JSON，再选择 Streaming')

assert.throws(() => resolveLegacyHealthCheckEndpointMode({
  accountId: 'account_tool_only',
  accountType: 'api_key',
  providerCode: 'hybrid',
  legacyFamily: 'generate_content',
  credentials: {
    supported_endpoint_modes: ['count_tokens', 'embed_content']
  }
}), /没有已启用的 JSON 或 Streaming 生成能力/, '工具接口不能被迁移成检查请求形态')

const migrationSource = readFileSync(
  new URL('../maintenance/account-health-check-endpoint-mode-migration.ts', import.meta.url),
  'utf8'
)
const migrationCliSource = readFileSync(
  new URL('../maintenance/migrate-account-health-check-endpoint-mode.ts', import.meta.url),
  'utf8'
)
assert.match(migrationSource, /BEGIN[\s\S]+LOCK TABLE juhe_business\.accounts IN ACCESS EXCLUSIVE MODE/)
assert.match(migrationSource, /WHERE id > \$1[\s\S]+ORDER BY id ASC[\s\S]+LIMIT \$2/, '迁移扫描必须使用 keyset 分批')
assert.match(migrationSource, /decryptJson\(encrypted\)/, '迁移必须通过应用 codec 解密凭据')
assert.match(migrationSource, /encryptJson\(normalized\.credentials\)/, '迁移必须通过应用 codec 重新加密凭据')
assert.match(migrationSource, /RENAME COLUMN health_check_endpoint_family TO health_check_endpoint_mode/)
assert.match(migrationSource, /resolveLegacyHealthCheckEndpointMode/, '迁移必须按账户加密能力选择同族精确请求形态')
assert.match(migrationSource, /不在加密上游生成能力中/, '迁移提交前必须校验全部账户精确 mode 属于加密生成能力')
assert.match(migrationSource, /await verifyExactRows\(client, batchSize\)[\s\S]+await client\.query\('COMMIT'\)/, '正式迁移必须在提交前校验结果')
assert.match(migrationSource, /await client\.query\('ROLLBACK'\)\.catch/, '迁移失败必须回滚整个事务')
assert.match(migrationCliSource, /mode: execute \? 'execute' : verify \? 'verify' : 'dry-run'/, '维护命令默认必须是 dry-run')
assert.match(migrationCliSource, /JUHE_AI_OFFLINE_MAINTENANCE_CONFIRMED/, '正式迁移必须显式确认已停服')
assert.match(migrationCliSource, /args\.includes\('--verify'\)/, '维护命令必须提供独立 verify 模式')

const draftServiceSource = readFileSync(new URL('../../modules/accounts/account-draft-test.service.ts', import.meta.url), 'utf8')
assert.equal(
  draftServiceSource.match(/resolveHealthCheckEndpointMode\(\{/g)?.length,
  3,
  '同步草稿、异步草稿和 Images 目录验证都必须按最终 endpoint mode 解析健康检查请求形态'
)
assert.match(draftServiceSource, /findProviderModelTestCatalogItemAsync/, '草稿 Images API 必须读取模型目录能力')
assert.match(draftServiceSource, /supportedApiProtocols\.includes\('images'\)/, '草稿 Images API 必须拒绝未证实图片能力的模型')

const accountWriteSource = readFileSync(new URL('../../storage/repositories.ts', import.meta.url), 'utf8')
const accountPatchSource = readFileSync(new URL('../../storage/account-management-patch.repository.ts', import.meta.url), 'utf8')
const accountBatchEditSource = readFileSync(new URL('../../modules/accounts/account-batch-edit.service.ts', import.meta.url), 'utf8')
assert.match(accountWriteSource, /accountHealthCheckModelSupportsImages\(modelValidationContext, healthCheckModel\)/, '账户创建必须以模型目录证实 Images API')
assert.match(accountPatchSource, /accountHealthCheckModelSupportsImages\(modelValidationContext, nextHealthCheckModel\)/, '账户更新必须以模型目录证实 Images API')
assert.match(accountBatchEditSource, /accountHealthCheckModelSupportsImages\(modelValidationContext, nextHealthCheckModel\)/, '批量编辑必须以模型目录证实既有 Images API 检查模型')

for (const relativePath of [
  '../../modules/background/account-api-key-cooldown-retest.service.ts',
  '../../modules/background/account-health-check.service.ts',
  '../../modules/background/account-quality-failure-precheck.service.ts',
  '../../modules/background/cooldown-account-retest.service.ts',
  '../../modules/background/normal-route-speed-first-recovery-probe.service.ts',
  '../../modules/gateway/runtime/account-side-effects.service.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.match(source, /testEndpointMode:\s*account\.healthCheckEndpointMode/, `${relativePath} 必须直接使用账户保存的精确 mode`)
  assert.doesNotMatch(source, /healthCheckEndpointMode\s*\(/, `${relativePath} 不得再次从协议族推导请求形态`)
}

for (const relativePath of [
  '../../modules/background/account-health-check.service.ts',
  '../../modules/background/cooldown-account-retest.service.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.match(source, /forceProbeKind:\s*account\.healthCheckEndpointMode === 'images_json' \? 'models_catalog' : undefined/, `${relativePath} 的图片检查必须使用模型目录探针`)
  assert.match(source, /requireCatalogModelEvidence:\s*account\.healthCheckEndpointMode === 'images_json'/, `${relativePath} 的图片检查必须验证模型目录包含检查模型`)
}

for (const relativePath of [
  '../../modules/accounts/account-test.service.ts',
  '../../modules/accounts/account-test-options.service.ts'
]) {
  const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8')
  assert.match(source, /accountManualTestEndpointModes/, `${relativePath} 必须复用共享人工测试请求形态解析器`)
  assert.doesNotMatch(source, /function accountTestEndpointModeOrder/, `${relativePath} 不得保留独立请求形态排序规则`)
}

assertSqliteImagesHealthCheckEndpointModeSchema()

console.log('AI 账户健康检查请求形态领域回归通过')

function assertSqliteImagesHealthCheckEndpointModeSchema(): void {
  const freshDatabase = new DatabaseSync(':memory:')
  try {
    applyBusinessSchema(freshDatabase)
    assertAccountHealthCheckModeConstraint(freshDatabase, '全新 SQLite 库')
  } finally {
    freshDatabase.close()
  }

  const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-account-health-check-endpoint-mode-'))
  const databasePath = join(tempRoot, 'business.sqlite3')
  let legacyDatabase: DatabaseSync | undefined
  try {
    legacyDatabase = new DatabaseSync(databasePath)
    applyBusinessSchema(legacyDatabase)
    seedAccountHealthCheckSchemaReferences(legacyDatabase)
    legacyDatabase.exec('CREATE INDEX accounts_health_check_endpoint_mode_regression_idx ON accounts(name)')
    legacyDatabase.prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      'legacy-account', 'system-account', 'openai', 'profile-openai', 'openai', 'v1',
      'Legacy account', 'api_key', 'encrypted', 'gpt-4.1-mini', 'chat_json',
      '2026-08-03T00:00:00.000Z', '2026-08-03T00:00:00.000Z'
    )
    replaceImagesModeWithLegacyConstraint(legacyDatabase)
    legacyDatabase.close()
    legacyDatabase = new DatabaseSync(databasePath)

    const legacySql = accountTableSql(legacyDatabase)
    assert.doesNotMatch(legacySql, /'images_json'/, '构造的既有 SQLite 库必须仍使用旧的健康检查 mode 约束')
    applyBusinessSchema(legacyDatabase)
    assertAccountHealthCheckModeConstraint(legacyDatabase, '既有 SQLite 库升级后')
    assert.equal(
      legacyDatabase.prepare("SELECT health_check_endpoint_mode FROM accounts WHERE id = 'legacy-account'").get()?.health_check_endpoint_mode,
      'chat_json',
      'SQLite 既有 accounts 记录必须在约束升级后保持不变'
    )
    assert.ok(
      legacyDatabase.prepare("SELECT 1 FROM sqlite_master WHERE type = 'index' AND name = 'accounts_health_check_endpoint_mode_regression_idx'").get(),
      'SQLite 约束升级必须保留 accounts 的显式索引'
    )
    legacyDatabase.prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      'images-account', 'system-account', 'openai', 'profile-openai', 'openai', 'v1',
      'Images account', 'api_key', 'encrypted', 'gpt-image-2', 'images_json',
      '2026-08-03T00:00:00.000Z', '2026-08-03T00:00:00.000Z'
    )
  } finally {
    legacyDatabase?.close()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function assertAccountHealthCheckModeConstraint(database: DatabaseSync, label: string): void {
  assert.match(accountTableSql(database), /health_check_endpoint_mode[\s\S]*?'images_json'/, `${label} 必须允许 images_json`)
}

function accountTableSql(database: DatabaseSync): string {
  const row = database.prepare("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'accounts'").get() as { sql?: unknown } | undefined
  if (typeof row?.sql !== 'string') throw new Error('SQLite accounts 表定义必须存在')
  return row.sql
}

function seedAccountHealthCheckSchemaReferences(database: DatabaseSync): void {
  const timestamp = '2026-08-03T00:00:00.000Z'
  database.prepare(`
    INSERT INTO system_accounts (id, username, display_name, password_hash, created_at, updated_at)
    VALUES ('system-account', 'system-account', 'System account', 'hash', ?, ?)
  `).run(timestamp, timestamp)
  database.prepare(`
    INSERT INTO providers (id, code, name, created_at, updated_at)
    VALUES ('provider-openai', 'openai', 'OpenAI', ?, ?)
  `).run(timestamp, timestamp)
  database.prepare(`
    INSERT INTO protocols (id, code, version, name, created_at, updated_at)
    VALUES ('protocol-openai-v1', 'openai', 'v1', 'OpenAI v1', ?, ?)
  `).run(timestamp, timestamp)
  database.prepare(`
    INSERT INTO provider_protocol_profiles (
      id, provider_code, name, protocol_code, protocol_version, base_url, default_health_check_model,
      account_types_json, capabilities_json, created_at, updated_at
    ) VALUES ('profile-openai', 'openai', 'OpenAI profile', 'openai', 'v1', 'https://example.test', 'gpt-4.1-mini', '[]', '[]', ?, ?)
  `).run(timestamp, timestamp)
}

function replaceImagesModeWithLegacyConstraint(database: DatabaseSync): void {
  const currentSql = accountTableSql(database)
  const legacySql = currentSql.replace("'images_json', ", '')
  assert.notEqual(legacySql, currentSql, '测试库当前 accounts schema 必须包含 images_json')
  const schemaVersion = database.prepare('PRAGMA schema_version').get() as { schema_version?: unknown } | undefined
  const nextSchemaVersion = Number(schemaVersion?.schema_version) + 1
  assert.ok(Number.isInteger(nextSchemaVersion) && nextSchemaVersion > 0, 'SQLite schema_version 必须可递增')
  database.exec('PRAGMA writable_schema = ON')
  try {
    database.prepare("UPDATE sqlite_master SET sql = ? WHERE type = 'table' AND name = 'accounts'").run(legacySql)
    database.exec(`PRAGMA schema_version = ${nextSchemaVersion}`)
  } finally {
    database.exec('PRAGMA writable_schema = OFF')
  }
}
