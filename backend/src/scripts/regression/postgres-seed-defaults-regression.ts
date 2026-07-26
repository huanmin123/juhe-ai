import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'

import { HYBRID_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'
import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'
import {
  DEFAULT_BUILT_IN_GROUPS,
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS
} from '../../storage/schema-defaults.js'

const providerCodes = new Set(DEFAULT_PROVIDER_SEEDS.map((provider) => provider.code))
const profileIds = new Set(DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.map((profile) => profile.id))
const defaultGroupProviderCodes = new Set(DEFAULT_BUILT_IN_GROUPS.map((group) => group.providerCode))
const defaultRouteProviderCodes: string[] = DEFAULT_BUILT_IN_GROUPS
  .map((group) => group.providerCode)
  .filter((providerCode) => providerCode !== HYBRID_PROVIDER_CODE)

assert.equal(providerCodes.size, DEFAULT_PROVIDER_SEEDS.length, '默认 provider seed code 不能重复')
assert.equal(profileIds.size, DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.length, '默认 provider protocol profile seed id 不能重复')
assert.equal(defaultGroupProviderCodes.size, DEFAULT_BUILT_IN_GROUPS.length, '默认内置分组必须按供应商唯一')
assert.equal(defaultRouteProviderCodes.includes(HYBRID_PROVIDER_CODE), false, '默认策略路由 / 默认 API Key seed 不应覆盖混合供应商默认分组')
assert.equal(defaultRouteProviderCodes.length, DEFAULT_BUILT_IN_GROUPS.length - 1, '默认策略路由 / 默认 API Key seed 应覆盖除混合供应商外的每个默认分组')

for (const profile of DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS) {
  assert.ok(
    providerCodes.has(profile.providerCode),
    `默认 provider protocol profile ${profile.id} 引用的 provider ${profile.providerCode} 必须存在于默认 provider seed`
  )
}

for (const group of DEFAULT_BUILT_IN_GROUPS) {
  assert.ok(
    providerCodes.has(group.providerCode),
    `默认分组 ${group.id} 引用的 provider ${group.providerCode} 必须存在于默认 provider seed`
  )
}

interface ExecutedStatement {
  sql: string
  values: readonly unknown[]
}

const executedStatements: ExecutedStatement[] = []
await seedPostgresDefaults({
  async execute(sql, values = []) {
    executedStatements.push({ sql, values })
    return { changes: 1 }
  },
  async one<T extends object>() {
    return undefined as T | undefined
  }
})

const modelInsertStatements = executedStatements.filter(({ sql }) => (
  /INSERT INTO\s+"juhe_business"\."provider_model_catalog"/i.test(sql)
))
const expectedModelKeys = DEFAULT_PROVIDER_SEEDS
  .filter((provider) => provider.code !== HYBRID_PROVIDER_CODE && provider.code !== 'openai')
  .flatMap((provider) => listProviderModelPricing(provider.code).map((model) => `${provider.code}\u0000${model.model}`))
  .sort()
assert.equal(modelInsertStatements.length, 1, 'PostgreSQL 模型目录必须通过单条有界 INSERT 保证批次原子')
const modelInsertStatement = modelInsertStatements[0]
assert.ok(modelInsertStatement, 'PostgreSQL 默认 seed 必须写入 provider_model_catalog')
assert.match(modelInsertStatement.sql, /ON CONFLICT DO NOTHING/i, '内建模型 seed 冲突时不得覆盖管理员已有配置')
assert.doesNotMatch(modelInsertStatement.sql, /DO UPDATE/i, '二次 seed 不得更新管理员已有模型配置')
const modelSeedParameterCount = 39
assert.equal(modelInsertStatement.values.length % modelSeedParameterCount, 0, 'PostgreSQL 模型批次参数必须按完整行对齐')
assert.equal(modelInsertStatement.values.length, expectedModelKeys.length * modelSeedParameterCount, 'PostgreSQL 模型批次参数数量必须覆盖全部行和字段')
assert.ok(modelInsertStatement.values.length < 65_535, 'PostgreSQL 模型批次参数数量必须低于协议上限')
const modelSeedRows = Array.from(
  { length: modelInsertStatement.values.length / modelSeedParameterCount },
  (_item, index) => modelInsertStatement.values.slice(index * modelSeedParameterCount, (index + 1) * modelSeedParameterCount)
)
const seededModelKeys = modelSeedRows
  .map((values) => `${String(values[1])}\u0000${String(values[2])}`)
  .sort()
assert.deepEqual(seededModelKeys, expectedModelKeys, 'PostgreSQL seed 模型键集合必须与 Node 权威价格目录一致')

const seededModelIds = modelSeedRows.map((values) => String(values[0]))
assert.equal(seededModelIds.length, expectedModelKeys.length, 'PostgreSQL seed 生成 ID 数量必须等于权威模型键数量')
assert.equal(new Set(seededModelIds).size, expectedModelKeys.length, 'PostgreSQL seed 模型 ID 必须全局唯一')
function expectedProviderModelId(providerCode: string, model: string): string {
  const slug = `${providerCode}_${model}`
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 72)
  const hash = createHash('sha256')
    .update(`${providerCode}\u0000${model}`)
    .digest('hex')
    .slice(0, 12)
  return `provider_model_${slug}_${hash}`
}

assert.notEqual(
  expectedProviderModelId('anthropic', 'antigravity-claude-opus-4-6-thinking'),
  expectedProviderModelId('anthropic', 'antigravity/claude-opus-4-6-thinking'),
  'slash/hyphen 模型名即使 slug 碰撞也必须生成不同 ID'
)

const expectedModelValues = new Map<string, readonly unknown[]>(
  DEFAULT_PROVIDER_SEEDS
    .filter((provider) => provider.code !== HYBRID_PROVIDER_CODE && provider.code !== 'openai')
    .flatMap((provider) => listProviderModelPricing(provider.code).map((model) => [
      `${provider.code}\u0000${model.model}`,
      [
        expectedProviderModelId(provider.code, model.model),
        provider.code,
        model.model,
        model.mode ?? null,
        model.catalogOrder ?? null,
        model.releaseDate ?? null,
        model.shutdownDate ?? null,
        JSON.stringify(model.supportedApiProtocols),
        JSON.stringify(model.supportedServiceTiers),
        JSON.stringify(model.supportedReasoningEfforts),
        model.defaultReasoningEffort ?? null,
        JSON.stringify(model.codexSupportedReasoningLevels),
        model.codexDefaultReasoningLevel ?? null,
        model.codexMultiAgentVersion ?? null,
        model.contextWindowTokens ?? null,
        model.maxInputTokens ?? null,
        model.maxOutputTokens ?? null,
        model.maxTokens ?? null,
        model.inputUsdPer1M ?? null,
        model.outputUsdPer1M ?? null,
        model.cachedInputUsdPer1M ?? null,
        model.cacheWriteUsdPer1M ?? null,
        model.cacheWrite1hUsdPer1M ?? null,
        model.cacheStorageUsdPer1MPerHour ?? null,
        JSON.stringify(model.serviceTierPrices ?? {}),
        model.longContextInputTokenThreshold ?? null,
        model.longContextInputTokenThresholdInclusive === true,
        model.longContextInputCostMultiplier ?? null,
        model.longContextOutputCostMultiplier ?? null,
        model.imageInputUsdPer1M ?? null,
        model.imageOutputUsdPer1M ?? null,
        model.audioInputUsdPer1M ?? null,
        model.audioOutputUsdPer1M ?? null,
        model.outputUsdPerImage ?? null,
        model.supportsPromptCaching === true,
        model.catalogVisible !== false,
        model.source
      ] as readonly unknown[]
    ] as const))
)
for (const values of modelSeedRows) {
  const key = `${String(values[1])}\u0000${String(values[2])}`
  assert.deepEqual(values.slice(0, 37), expectedModelValues.get(key), `${key} 的 PostgreSQL seed 字段映射必须完整`)
  assert.equal(values.length, 39, `${key} 的 PostgreSQL seed 参数数量必须与 schema 一致`)
  assert.equal(typeof values[37], 'string', `${key} 必须写入 created_at`)
  assert.equal(values[38], values[37], `${key} 首次 seed 的 created_at / updated_at 必须一致`)
}

const gpt5MiniValues = modelSeedRows.find((values) => values[1] === 'gpt' && values[2] === 'gpt-5-mini')
assert.ok(gpt5MiniValues, 'fresh PostgreSQL 必须包含 gpt/gpt-5-mini')
assert.match(modelInsertStatement.sql, /model, status, mode/i, '模型 INSERT 必须显式写入 status')
assert.match(modelInsertStatement.sql, /VALUES\s*\(\s*\$1,\s*\$2,\s*\$3,\s*'active'/i, '内建模型 status 必须为 active')
assert.equal((modelInsertStatement.sql.match(/'active'/g) ?? []).length, expectedModelKeys.length, '批量 INSERT 每个内建模型 status 必须为 active')
assert.equal(gpt5MiniValues[7], JSON.stringify(['chat_completions', 'responses']), 'gpt-5-mini API 能力必须完整写入')
assert.equal(gpt5MiniValues[8], JSON.stringify(['priority', 'flex']), 'gpt-5-mini service tiers 必须完整写入')
assert.equal(gpt5MiniValues[18], 0.25, 'gpt-5-mini direct input price 必须写入')
assert.equal(gpt5MiniValues[19], 2, 'gpt-5-mini direct output price 必须写入')
assert.equal(gpt5MiniValues[20], 0.025, 'gpt-5-mini direct cached input price 必须写入')
assert.deepEqual(JSON.parse(String(gpt5MiniValues[24])), {
  priority: {
    inputUsdPer1M: 0.45,
    outputUsdPer1M: 3.6,
    cachedInputUsdPer1M: 0.045
  }
}, 'gpt-5-mini service tier prices 必须完整写入')
assert.equal(gpt5MiniValues[35], true, 'gpt-5-mini catalog_visible 必须为 boolean true')
const grok45Values = modelSeedRows.find((values) => values[1] === 'xai' && values[2] === 'grok-4.5')
assert.ok(grok45Values, 'fresh PostgreSQL 必须包含 xai/grok-4.5')
assert.equal(grok45Values[25], 200_000, 'grok-4.5 长上下文价格阈值必须写入')
assert.equal(grok45Values[26], true, 'grok-4.5 长上下文价格阈值必须按 inclusive boolean 写入')

const repeatedSeedStatements: ExecutedStatement[] = []
await seedPostgresDefaults({
  async execute(sql, values = []) {
    repeatedSeedStatements.push({ sql, values })
    return { changes: 0 }
  },
  async one<T extends object>() {
    return undefined as T | undefined
  }
})
const repeatedModelInserts = repeatedSeedStatements.filter(({ sql }) => (
  /INSERT INTO\s+"juhe_business"\."provider_model_catalog"/i.test(sql)
))
assert.equal(repeatedModelInserts.length, 1, '二次 seed 仍必须使用单条模型目录 INSERT')
assert.match(repeatedModelInserts[0]?.sql ?? '', /ON CONFLICT DO NOTHING/i, '二次 seed 必须保留已有管理员模型配置')
assert.doesNotMatch(repeatedModelInserts[0]?.sql ?? '', /DO UPDATE/i, '二次 seed 不得覆盖已有管理员模型配置')
const repeatedModelKeys = Array.from(
  { length: (repeatedModelInserts[0]?.values.length ?? 0) / modelSeedParameterCount },
  (_item, index) => {
    const values = repeatedModelInserts[0]?.values ?? []
    const offset = index * modelSeedParameterCount
    return `${String(values[offset + 1])}\u0000${String(values[offset + 2])}`
  }
).sort()
assert.deepEqual(repeatedModelKeys, expectedModelKeys, '二次 seed 必须尝试相同模型键并由冲突策略跳过已有配置')

const builtInExternalSourceUpdate = executedStatements.find(({ sql }) => (
  /UPDATE\s+"juhe_business"\."external_integration_sources"/i.test(sql)
))?.sql ?? ''
const postgresSeedDefaultsSource = readFileSync('src/storage/postgres-seed-defaults.ts', 'utf8')
assert.match(
  builtInExternalSourceUpdate,
  /WHERE id = \$6[\s\S]+name IS DISTINCT FROM \$1[\s\S]+notes IS DISTINCT FROM \$4/,
  '内建外部来源重复 seed 仅允许在实际字段变化时推进 updated_at'
)
assert.match(
  postgresSeedDefaultsSource,
  /UPDATE \$\{businessTable\('external_integration_source_tokens'\)\}[\s\S]+WHERE id = \$5[\s\S]+source_ref_id IS DISTINCT FROM \$1[\s\S]+expires_at IS NOT NULL/,
  '内建外部 token 重复 seed 仅允许在实际字段变化时推进 updated_at'
)

console.log('postgres-seed-defaults-regression passed')
