import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'

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
assert.ok(modelInsertStatements.length > 0, 'PostgreSQL 默认 seed 必须写入 provider_model_catalog')
assert.ok(
  modelInsertStatements.every(({ sql }) => /ON CONFLICT DO NOTHING/i.test(sql)),
  '内建模型 seed 冲突时不得覆盖管理员已有配置'
)

const seededModelKeys = modelInsertStatements
  .map(({ values }) => `${String(values[1])}\u0000${String(values[2])}`)
  .sort()
const expectedModelKeys = DEFAULT_PROVIDER_SEEDS
  .filter((provider) => provider.code !== HYBRID_PROVIDER_CODE && provider.code !== 'openai')
  .flatMap((provider) => listProviderModelPricing(provider.code).map((model) => `${provider.code}\u0000${model.model}`))
  .sort()
assert.deepEqual(seededModelKeys, expectedModelKeys, 'PostgreSQL seed 模型键集合必须与 Node 权威价格目录一致')

const seededModelIds = modelInsertStatements.map(({ values }) => String(values[0]))
assert.equal(seededModelIds.length, expectedModelKeys.length, 'PostgreSQL seed 生成 ID 数量必须等于权威模型键数量')
assert.equal(new Set(seededModelIds).size, expectedModelKeys.length, 'PostgreSQL seed 模型 ID 必须全局唯一')
const slashCollisionPair = [
  'antigravity-claude-opus-4-6-thinking',
  'antigravity/claude-opus-4-6-thinking'
].map((model) => modelInsertStatements.find(({ values }) => values[1] === 'anthropic' && values[2] === model))
assert.ok(slashCollisionPair.every(Boolean), 'slash/hyphen 碰撞样本必须存在于权威模型目录')
assert.notEqual(slashCollisionPair[0]?.values[0], slashCollisionPair[1]?.values[0], 'slash/hyphen 模型名必须生成不同 ID')

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
        JSON.stringify(model.serviceTierPrices ?? {}),
        model.longContextInputTokenThreshold ?? null,
        model.longContextInputCostMultiplier ?? null,
        model.longContextOutputCostMultiplier ?? null,
        model.imageInputUsdPer1M ?? null,
        model.imageOutputUsdPer1M ?? null,
        model.audioInputUsdPer1M ?? null,
        model.audioOutputUsdPer1M ?? null,
        model.outputUsdPerImage ?? null,
        model.supportsPromptCaching ? 1 : 0,
        model.catalogVisible === false ? 0 : 1,
        model.source
      ] as readonly unknown[]
    ] as const))
)
for (const { values } of modelInsertStatements) {
  const key = `${String(values[1])}\u0000${String(values[2])}`
  assert.deepEqual(values.slice(0, 35), expectedModelValues.get(key), `${key} 的 PostgreSQL seed 字段映射必须完整`)
  assert.equal(values.length, 37, `${key} 的 PostgreSQL seed 参数数量必须与 schema 一致`)
  assert.equal(typeof values[35], 'string', `${key} 必须写入 created_at`)
  assert.equal(values[36], values[35], `${key} 首次 seed 的 created_at / updated_at 必须一致`)
}

const gpt5MiniInsert = modelInsertStatements.find(({ values }) => values[1] === 'gpt' && values[2] === 'gpt-5-mini')
assert.ok(gpt5MiniInsert, 'fresh PostgreSQL 必须包含 gpt/gpt-5-mini')
assert.match(gpt5MiniInsert.sql, /model, status, mode/i, '模型 INSERT 必须显式写入 status')
assert.match(gpt5MiniInsert.sql, /VALUES\s*\(\s*\$1,\s*\$2,\s*\$3,\s*'active'/i, '内建模型 status 必须为 active')
assert.equal(gpt5MiniInsert.values[7], JSON.stringify(['chat_completions', 'responses']), 'gpt-5-mini API 能力必须完整写入')
assert.equal(gpt5MiniInsert.values[8], JSON.stringify(['priority', 'flex']), 'gpt-5-mini service tiers 必须完整写入')
assert.equal(gpt5MiniInsert.values[18], 0.25, 'gpt-5-mini direct input price 必须写入')
assert.equal(gpt5MiniInsert.values[19], 2, 'gpt-5-mini direct output price 必须写入')
assert.equal(gpt5MiniInsert.values[20], 0.025, 'gpt-5-mini direct cached input price 必须写入')
assert.deepEqual(JSON.parse(String(gpt5MiniInsert.values[23])), {
  priority: {
    inputUsdPer1M: 0.45,
    outputUsdPer1M: 3.6,
    cachedInputUsdPer1M: 0.045
  }
}, 'gpt-5-mini service tier prices 必须完整写入')
assert.equal(gpt5MiniInsert.values[33], 1, 'gpt-5-mini catalog_visible 必须为 true')

console.log('postgres-seed-defaults-regression passed')
