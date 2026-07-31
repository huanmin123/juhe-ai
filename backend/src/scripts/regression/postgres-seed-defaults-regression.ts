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

const defaultGroupRepairStatements = executedStatements.filter(({ sql }) => (
  /UPDATE\s+"juhe_business"\."groups"\s+AS\s+candidate/i.test(sql)
))
const defaultGroupInsertStatements = executedStatements.filter(({ sql }) => (
  /INSERT INTO\s+"juhe_business"\."groups"/i.test(sql)
))
assert.equal(defaultGroupRepairStatements.length, DEFAULT_BUILT_IN_GROUPS.length, 'PostgreSQL 默认 seed 必须为每个内置供应商恢复缺失默认标记')
assert.equal(defaultGroupInsertStatements.length, DEFAULT_BUILT_IN_GROUPS.length, 'PostgreSQL 默认 seed 必须为每个内置供应商批量补齐默认分组')
for (const group of DEFAULT_BUILT_IN_GROUPS) {
  const repairStatement = defaultGroupRepairStatements.find(({ values }) => (
    values[0] === group.providerCode && values[1] === group.systemAccountId && values[2] === group.id
  ))
  assert.ok(repairStatement, `${group.providerCode} 必须先恢复静态默认分组的 is_default 标记`)
  assert.match(repairStatement.sql, /candidate\.system_account_id\s*=\s*\$2[\s\S]*candidate\.id\s*=\s*\$3/i, '恢复默认标记必须只命中静态默认分组')
  assert.match(repairStatement.sql, /NOT EXISTS[\s\S]*existing_default[\s\S]*is_default\s*=\s*1/i, '恢复默认标记不得覆盖已有默认分组')

  const insertStatement = defaultGroupInsertStatements.find(({ values }) => (
    values[0] === group.systemAccountId
      && values[1] === group.id
      && values[2] === `grp_default_${group.providerCode}_`
      && values[3] === group.providerCode
  ))
  assert.ok(insertStatement, `${group.providerCode} 必须生成对应的批量默认分组 INSERT`)
  assert.match(insertStatement.sql, /FROM\s+"juhe_business"\."system_accounts"\s+AS\s+system_accounts/i, '默认分组必须以 system_accounts 批量补齐，而非仅 seed sys_admin')
  assert.match(insertStatement.sql, /CASE\s+WHEN\s+system_accounts\.id\s*=\s*\$1\s+THEN\s+\$2\s+ELSE\s+\$3\s*\|\|\s*system_accounts\.id/i, '非管理员默认分组 ID 必须无需 PostgreSQL 扩展且与管理员静态 ID 区分')
  assert.match(insertStatement.sql, /same_name[\s\S]*lower\(same_name\.name\)\s*=\s*lower\(\$5\)[\s\S]*THEN\s+\$6\s*\|\|\s*'（系统默认：'\s*\|\|\s*system_accounts\.id/i, '大小写变体的同名自定义分组不得阻断系统默认分组补齐')
  assert.match(insertStatement.sql, /LEFT JOIN LATERAL[\s\S]*generate_series[\s\S]*existing_fallback_name[\s\S]*ORDER BY candidate_suffix\.suffix/i, '回退名称已存在时必须选择未占用的编号变体，而非由冲突静默跳过')
  assert.match(insertStatement.sql, /ON CONFLICT DO NOTHING/i, '默认分组批量补齐遇到冲突时不得覆盖已有分组')
}

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
assert.match(modelInsertStatement.sql, /ON CONFLICT\(provider_code, model\) DO UPDATE SET/i, '内建模型 seed 必须按 provider/model 幂等同步')
assert.match(modelInsertStatement.sql, /source IN \('manual-override', 'manual-visibility-override'\)/i, '模型 seed 必须识别管理员手工覆盖')
assert.match(modelInsertStatement.sql, /catalog_visible[\s\S]*AND excluded\.catalog_visible/i, '模型 seed 不得放宽管理员手工隐藏')
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

const staleBuiltInModelUpdates = executedStatements.filter(({ sql }) => (
  /UPDATE\s+"juhe_business"\."provider_model_catalog"[\s\S]*jsonb_to_recordset\(\$2::jsonb\)/i.test(sql)
))
assert.equal(staleBuiltInModelUpdates.length, 1, 'PostgreSQL seed 必须仅用一条受限 UPDATE 停用已移除的内置模型')
const staleBuiltInModelUpdate = staleBuiltInModelUpdates[0]
assert.ok(staleBuiltInModelUpdate, 'PostgreSQL seed 必须传入当前内置模型键作为停用白名单')
const staleBuiltInModelKeys = (JSON.parse(String(staleBuiltInModelUpdate.values[1])) as Array<{ provider_code?: unknown; model?: unknown }>)
  .map((item) => `${String(item.provider_code)}\u0000${String(item.model)}`)
  .sort()
assert.deepEqual(staleBuiltInModelKeys, expectedModelKeys, '停用白名单必须使用 provider_code/model 并与 Node 权威目录完全一致')

const seededModelIds = modelSeedRows.map((values) => String(values[0]))
assert.equal(seededModelIds.length, expectedModelKeys.length, 'PostgreSQL seed 生成 ID 数量必须等于权威模型键数量')
assert.equal(new Set(seededModelIds).size, expectedModelKeys.length, 'PostgreSQL seed 模型 ID 必须全局唯一')
const slashCollisionPair = [
  'antigravity-claude-opus-4-6-thinking',
  'antigravity/claude-opus-4-6-thinking'
]
assert.notEqual(
  expectedProviderModelId('anthropic', slashCollisionPair[0]),
  expectedProviderModelId('anthropic', slashCollisionPair[1]),
  'slash/hyphen 模型名必须生成不同 ID'
)

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
assert.match(repeatedModelInserts[0]?.sql ?? '', /ON CONFLICT\(provider_code, model\) DO UPDATE SET/i, '二次 seed 必须按模型键同步目录')
assert.match(repeatedModelInserts[0]?.sql ?? '', /manual-override/i, '二次 seed 必须保留管理员手工覆盖')
const repeatedModelKeys = Array.from(
  { length: (repeatedModelInserts[0]?.values.length ?? 0) / modelSeedParameterCount },
  (_item, index) => {
    const values = repeatedModelInserts[0]?.values ?? []
    const offset = index * modelSeedParameterCount
    return `${String(values[offset + 1])}\u0000${String(values[offset + 2])}`
  }
).sort()
assert.deepEqual(repeatedModelKeys, expectedModelKeys, '二次 seed 必须同步相同模型键')

console.log('postgres-seed-defaults-regression passed')
