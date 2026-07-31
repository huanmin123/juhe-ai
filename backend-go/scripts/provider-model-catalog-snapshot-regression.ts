import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'

import {
  listProviderModelPricingAsOf
} from '../../backend/src/modules/model-pricing/model-pricing.service.js'
import {
  PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE,
  providerModelCatalogSnapshotSQL
} from './generate-provider-model-catalog.js'

const lfSnapshotFixture = 'catalog-model-a\ncatalog-model-b\n'
const crlfSnapshotFixture = lfSnapshotFixture.replace(/\n/g, '\r\n')
assert.equal(
  normalizeSnapshotLineEndings(crlfSnapshotFixture),
  normalizeSnapshotLineEndings(lfSnapshotFixture),
  'snapshot comparison must treat LF and CRLF as equivalent'
)
assert.notEqual(
  normalizeSnapshotLineEndings(lfSnapshotFixture.replace('catalog-model-b', 'catalog-model-c')),
  normalizeSnapshotLineEndings(lfSnapshotFixture),
  'snapshot comparison must retain real character differences'
)
assert.notEqual(
  normalizeSnapshotLineEndings(lfSnapshotFixture.replace('catalog-model-b\n', 'catalog-model-b \n')),
  normalizeSnapshotLineEndings(lfSnapshotFixture),
  'snapshot comparison must retain horizontal whitespace differences'
)
assert.notEqual(
  normalizeSnapshotLineEndings(lfSnapshotFixture.slice(0, -1)),
  normalizeSnapshotLineEndings(lfSnapshotFixture),
  'snapshot comparison must retain the trailing newline contract'
)

assert.equal(
  PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE,
  '2026-07-30',
  'W2 provider model catalog snapshot as-of date must remain explicit and fixed'
)

const gpt54Mini = modelAtSnapshot('gpt', 'gpt-5.4-mini')
assert.equal(gpt54Mini.contextWindowTokens, 400_000, 'GPT-5.4 mini must keep total context separate from max input')
assert.equal(gpt54Mini.maxInputTokens, 272_000, 'GPT-5.4 mini max input must match the official model page')
assert.equal(gpt54Mini.maxOutputTokens, 128_000, 'GPT-5.4 mini max output must match the official model page')

const gpt56Sol = modelAtSnapshot('gpt', 'gpt-5.6-sol')
const gpt56Terra = modelAtSnapshot('gpt', 'gpt-5.6-terra')
const gpt56Luna = modelAtSnapshot('gpt', 'gpt-5.6-luna')
assert.equal(gpt56Sol.inputUsdPer1M, 5, 'GPT-5.6 Sol standard input price must remain unchanged')
assert.equal(gpt56Sol.cachedInputUsdPer1M, 0.5, 'GPT-5.6 Sol standard cached input price must remain unchanged')
assert.equal(gpt56Sol.outputUsdPer1M, 30, 'GPT-5.6 Sol standard output price must remain unchanged')
assert.equal(gpt56Sol.cacheWriteUsdPer1M, 6.25, 'GPT-5.6 Sol standard cache write price must remain unchanged')
assert.equal(gpt56Terra.inputUsdPer1M, 2, 'GPT-5.6 Terra standard input price must match the 2026-07-30 official price')
assert.equal(gpt56Terra.cachedInputUsdPer1M, 0.2, 'GPT-5.6 Terra standard cached input price must match the 2026-07-30 official price')
assert.equal(gpt56Terra.outputUsdPer1M, 12, 'GPT-5.6 Terra standard output price must match the 2026-07-30 official price')
assert.equal(gpt56Terra.cacheWriteUsdPer1M, 2.5, 'GPT-5.6 Terra standard cache write price must match the 2026-07-30 official price')
assert.equal(gpt56Luna.inputUsdPer1M, 0.2, 'GPT-5.6 Luna standard input price must match the 2026-07-30 official price')
assert.equal(gpt56Luna.cachedInputUsdPer1M, 0.02, 'GPT-5.6 Luna standard cached input price must match the 2026-07-30 official price')
assert.equal(gpt56Luna.outputUsdPer1M, 1.2, 'GPT-5.6 Luna standard output price must match the 2026-07-30 official price')
assert.equal(gpt56Luna.cacheWriteUsdPer1M, 0.25, 'GPT-5.6 Luna standard cache write price must match the 2026-07-30 official price')
for (const [model, input, cachedInput, output, cacheWrite] of [
  [gpt56Terra, 4, 0.4, 24, 5],
  [gpt56Luna, 0.4, 0.04, 2.4, 0.5]
] as const) {
  assert.equal(model.serviceTierPrices.priority?.inputUsdPer1M, input, `${model.model} priority input must remain Fast-compatible 2x pricing`)
  assert.equal(model.serviceTierPrices.priority?.cachedInputUsdPer1M, cachedInput, `${model.model} priority cached input must remain Fast-compatible 2x pricing`)
  assert.equal(model.serviceTierPrices.priority?.outputUsdPer1M, output, `${model.model} priority output must remain Fast-compatible 2x pricing`)
  assert.equal(model.serviceTierPrices.priority?.cacheWriteUsdPer1M, cacheWrite, `${model.model} priority cache write must remain Fast-compatible 2x pricing`)
  assert.equal(model.serviceTierPrices.flex?.inputUsdPer1M, input / 4, `${model.model} flex input must remain half of standard pricing`)
  assert.equal(model.serviceTierPrices.flex?.cachedInputUsdPer1M, cachedInput / 4, `${model.model} flex cached input must remain half of standard pricing`)
  assert.equal(model.serviceTierPrices.flex?.outputUsdPer1M, output / 4, `${model.model} flex output must remain half of standard pricing`)
  assert.equal(model.serviceTierPrices.flex?.cacheWriteUsdPer1M, cacheWrite / 4, `${model.model} flex cache write must remain half of standard pricing`)
  assert.equal(model.longContextInputTokenThreshold, 272_000, `${model.model} long-context threshold must remain unchanged`)
  assert.equal(model.longContextInputCostMultiplier, 2, `${model.model} long-context input multiplier must remain unchanged`)
  assert.equal(model.longContextOutputCostMultiplier, 1.5, `${model.model} long-context output multiplier must remain unchanged`)
}

const deepSeekModels = new Set(listProviderModelPricingAsOf('deepseek', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE).map((item) => item.model))
assert.equal(deepSeekModels.has('deepseek-ai-v4-flash'), false, 'non-official DeepSeek V4 alias must not be public')
assert.equal(deepSeekModels.has('deepseek-ai-v4-pro'), false, 'non-official DeepSeek V4 alias must not be public')
const deepSeekV4 = modelAtSnapshot('deepseek', 'deepseek-v4-flash')
assert.equal(deepSeekV4.contextWindowTokens, 1_000_000, 'DeepSeek V4 1M is total context')
assert.equal(deepSeekV4.maxInputTokens, undefined, 'DeepSeek V4 max input must remain unknown when the vendor does not publish it')
assert.equal(deepSeekV4.maxOutputTokens, 384_000)
for (const item of listProviderModelPricingAsOf('deepseek', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE)) {
  assert(item.supportedApiProtocols.includes('messages'), `${item.model} must declare the archived DeepSeek Anthropic Messages surface`)
}

const geminiModels = new Set(listProviderModelPricingAsOf('gemini', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE).map((item) => item.model))
assert.equal(geminiModels.has('gemini-embedding-001'), false, 'Gemini embedding 001 shut down on 2026-07-14')
assert.equal(modelAtSnapshot('gemini', 'gemini-embedding-2').inputUsdPer1M, 0.2, 'Gemini Embedding 2 text price must match official pricing')
assert(modelAtSnapshot('gemini', 'gemini-3.5-flash').supportedApiProtocols.includes('interactions'), 'Gemini 3.5 Flash must expose the official Interactions protocol')
assert(modelAtSnapshot('gemini', 'gemini-2.5-pro').supportedApiProtocols.includes('interactions'), 'Gemini 2.5 Pro must expose the official Interactions protocol')
assert.equal(modelAtSnapshot('gemini', 'gemini-3.1-pro-preview').cacheStorageUsdPer1MPerHour, 4.5, 'Gemini Pro cache storage must use the official per-hour price')
assert.equal(modelAtSnapshot('gemini', 'gemini-3.5-flash').cacheStorageUsdPer1MPerHour, 1, 'Gemini Flash cache storage must use the official per-hour price')

const grok420 = modelAtSnapshot('xai', 'grok-4.20-0309-reasoning')
assert.equal(grok420.contextWindowTokens, 1_000_000, 'Grok 4.20 context window must match official xAI models')
assert.equal(grok420.inputUsdPer1M, 1.25, 'Grok 4.20 input price must match official xAI pricing')
assert.equal(grok420.cachedInputUsdPer1M, 0.2, 'Grok 4.20 cached input price must match official xAI pricing')
assert.equal(grok420.outputUsdPer1M, 2.5, 'Grok 4.20 output price must match official xAI pricing')
assert.deepEqual(grok420.supportedApiProtocols, ['chat_completions', 'responses'])
assert.deepEqual(grok420.supportedServiceTiers, ['priority'])
assert.equal(grok420.serviceTierPrices.priority?.inputUsdPer1M, 2.5, 'xAI priority pricing is 2x standard input')
assert.equal(grok420.longContextInputTokenThreshold, 200_000, 'xAI long-context threshold must be explicit')
assert.equal(grok420.longContextInputTokenThresholdInclusive, true, 'xAI long-context pricing starts at the 200k boundary')
assert.equal(grok420.longContextInputCostMultiplier, 2, 'xAI long-context input multiplier must be explicit')
assert.equal(grok420.longContextOutputCostMultiplier, 2, 'xAI long-context output multiplier must be explicit')
assert.equal(modelAtSnapshot('xai', 'grok-imagine-image').outputUsdPerImage, 0.02, 'xAI standard image price must match official pricing')
for (const model of [
  'grok-4.5',
  'grok-4.20-0309-reasoning',
  'grok-4.20-0309-non-reasoning',
  'grok-build-0.1',
  'grok-4.20-multi-agent-0309'
]) {
  const item = modelAtSnapshot('xai', model)
  assert.deepEqual(item.inputModalities, ['text', 'image'], `${model} must retain official text and image input capability`)
  assert.deepEqual(item.supportedTools, ['function_calling'], `${model} must expose official function calling support`)
}
assert.deepEqual(modelAtSnapshot('xai', 'grok-4.5').supportedReasoningEfforts, ['low', 'medium', 'high', 'xhigh'])
assert.equal(modelAtSnapshot('xai', 'grok-4.5').defaultReasoningEffort, 'high')
for (const model of ['grok-4.20-0309-reasoning', 'grok-4.20-0309-non-reasoning', 'grok-build-0.1', 'grok-4.20-multi-agent-0309']) {
  const item = modelAtSnapshot('xai', model)
  assert.deepEqual(item.supportedReasoningEfforts, [], `${model} must not invent unverified reasoning_effort values`)
  assert.equal(item.defaultReasoningEffort, undefined, `${model} must not invent a default reasoning effort`)
}
assert.equal(
  modelAtSnapshot('gemini', 'gemini-3.1-pro-preview').longContextInputTokenThresholdInclusive,
  false,
  'Gemini long-context pricing remains strictly above 200k'
)

for (const [model, context, output] of [
  ['glm-5.1', 200_000, 128_000],
  ['glm-5', 200_000, 128_000],
  ['glm-5-turbo', 200_000, 128_000]
] as const) {
  const item = modelAtSnapshot('glm', model)
  assert.equal(item.contextWindowTokens, context, `${model} total context must match the official GLM overview`)
  assert.equal(item.maxOutputTokens, output, `${model} max output must match the official GLM overview`)
}
assert.equal(listProviderModelPricingAsOf('glm', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE).some((item) => item.model === 'glm-4-flashx-250414'), false, 'GLM model without an official USD price must stay out of the built-in snapshot')
assert.equal(listProviderModelPricingAsOf('glm', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE).some((item) => item.model === 'glm-4-flash-250414'), false, 'GLM model removed from the current official catalog must stay out of the built-in snapshot')
assert.equal(modelAtSnapshot('glm', 'glm-4.7-flash').cachedInputUsdPer1M, 0, 'GLM 4.7 Flash cached input must retain its official free price')
assert.equal(modelAtSnapshot('glm', 'glm-4.7-flash').supportsPromptCaching, true, 'GLM 4.7 Flash must declare automatic prompt caching')

for (const [model, context] of [
  ['glm-4.7', 200_000],
  ['glm-4.7-flashx', 200_000],
  ['glm-4.7-flash', 200_000],
  ['glm-4.6', 200_000],
  ['glm-4.5', 128_000],
  ['glm-4.5-x', 128_000],
  ['glm-4.5-air', 128_000],
  ['glm-4.5-airx', 128_000],
  ['glm-4.5-flash', 128_000]
] as const) {
  const item = modelAtSnapshot('glm', model)
  assert.equal(item.contextWindowTokens, context, `${model} published window is total context`)
  assert.equal(item.maxInputTokens, undefined, `${model} max input must remain unknown without a published input-only limit`)
}

const anthropicModels = listProviderModelPricingAsOf('anthropic', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE)
assert(anthropicModels.every((item) => item.supportedServiceTiers.length === 0), 'Anthropic supports_service_tier boolean must not invent an OpenAI service tier token')
const claudeSonnet5 = modelAtSnapshot('anthropic', 'claude-sonnet-5')
assert(claudeSonnet5.catalogVisible !== false, 'Claude Sonnet 5 must be public')
assert.equal(claudeSonnet5.contextWindowTokens, 1_000_000, 'Claude Sonnet 5 context window must match the current official model overview')
assert.equal(claudeSonnet5.defaultReasoningEffort, 'high', 'Claude Sonnet 5 effort default must match the official effort documentation')
for (const alias of ['best', 'fable', 'opus', 'opus[1m]', 'opusplan', 'sonnet', 'sonnet[1m]', 'haiku']) {
  assert.equal(anthropicModels.some((item) => item.model === alias), false, `Claude Code alias ${alias} must not remain in the official Anthropic catalog`)
}

const deepSeekAtSnapshot = new Set(
  listProviderModelPricingAsOf('deepseek', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE)
    .map((item) => item.model)
)
assert.equal(deepSeekAtSnapshot.has('deepseek-chat'), false, 'current snapshot must exclude DeepSeek alias after its shutdown date')
assert.equal(deepSeekAtSnapshot.has('deepseek-reasoner'), false, 'current snapshot must exclude DeepSeek reasoning alias after its shutdown date')

const deepSeekAtShutdown = new Set(
  listProviderModelPricingAsOf('deepseek', '2026-07-24')
    .map((item) => item.model)
)
assert.equal(deepSeekAtShutdown.has('deepseek-chat'), false, 'as-of listing must exclude a model on its shutdown date')
assert.equal(deepSeekAtShutdown.has('deepseek-reasoner'), false, 'as-of listing must apply shutdown filtering to every model')

assert(
  providerModelCatalogSnapshotSQL.includes(`-- Snapshot as-of date: ${PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE}`),
  'generated SQL must record the fixed snapshot as-of date'
)
assert(providerModelCatalogSnapshotSQL.includes('service_tier_prices_json'), 'generated catalog must use unified service tier prices JSON')
assert(
  providerModelCatalogSnapshotSQL.includes("status = CASE WHEN provider_model_catalog.source IN ('manual-override', 'manual-visibility-override')"),
  'catalog sync must distinguish explicit administrator visibility from legacy snapshot state'
)
assert(
  providerModelCatalogSnapshotSQL.includes("catalog_visible = CASE WHEN provider_model_catalog.source IN ('manual-override', 'manual-visibility-override')"),
  'catalog sync must reopen repaired legacy rows without reopening administrator-hidden rows'
)
for (const legacy of ['pricing_model', 'priority_input_usd_per_1m', 'flex_input_usd_per_1m']) {
  assert.equal(providerModelCatalogSnapshotSQL.includes(legacy), false, `generated catalog must not use ${legacy}`)
}
for (const pricingAssignment of [
  "input_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.input_usd_per_1m ELSE EXCLUDED.input_usd_per_1m END",
  "output_usd_per_1m = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.output_usd_per_1m ELSE EXCLUDED.output_usd_per_1m END",
  "service_tier_prices_json = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.service_tier_prices_json ELSE EXCLUDED.service_tier_prices_json END",
  "output_usd_per_image = CASE WHEN provider_model_catalog.source = 'manual-override' THEN provider_model_catalog.output_usd_per_image ELSE EXCLUDED.output_usd_per_image END"
]) {
  assert(providerModelCatalogSnapshotSQL.includes(pricingAssignment), `catalog sync must refresh snapshot pricing while preserving explicit manual overrides: ${pricingAssignment}`)
}
const insertIndex = providerModelCatalogSnapshotSQL.indexOf('INSERT INTO juhe_business.provider_model_catalog')
const staleDisableIndex = providerModelCatalogSnapshotSQL.indexOf('UPDATE juhe_business.provider_model_catalog AS existing')
assert(insertIndex >= 0 && staleDisableIndex > insertIndex, 'stale generated rows must be disabled only after current snapshot upsert preserves administrator overrides')
assert(providerModelCatalogSnapshotSQL.includes('AND NOT EXISTS ('), 'stale generated row cleanup must exclude every model still present in the current snapshot')
assert(providerModelCatalogSnapshotSQL.includes('AS snapshot(provider_code, model)'), 'stale generated row cleanup must compare provider/model snapshot identity')
assert.doesNotMatch(providerModelCatalogSnapshotSQL, /\n[ \t]+\n/, 'generated catalog SQL must not contain whitespace-only value rows')
assert.doesNotMatch(providerModelCatalogSnapshotSQL, /,\n\s*\n\s*\)/, 'generated catalog SQL must not leave a trailing comma before a tuple closes')
assert.equal(
  normalizeSnapshotLineEndings(
    readFileSync(resolve(process.cwd(), '../backend-go/db/migrations/000094_w2_sync_provider_model_catalog_openai_pricing_20260730.sql'), 'utf8')
  ),
  normalizeSnapshotLineEndings(providerModelCatalogSnapshotSQL),
  'unified provider catalog seed migration must match the generated current-schema snapshot'
)

const generatorCheckDir = mkdtempSync(join(tmpdir(), 'provider-model-catalog-check-'))
try {
  const crlfSnapshotPath = join(generatorCheckDir, 'snapshot.sql')
  writeFileSync(crlfSnapshotPath, providerModelCatalogSnapshotSQL.replace(/\n/g, '\r\n'), 'utf8')
  const check = spawnSync(process.execPath, [
    '--import', 'tsx',
    resolve(process.cwd(), '../backend-go/scripts/generate-provider-model-catalog.ts'),
    '--check',
    '--output', crlfSnapshotPath
  ], { cwd: process.cwd(), encoding: 'utf8' })
  assert.equal(check.status, 0, `generator --check must accept CRLF snapshots: ${check.stderr || check.stdout}`)
} finally {
  rmSync(generatorCheckDir, { recursive: true, force: true })
}

console.log('provider model catalog snapshot regression passed')

function normalizeSnapshotLineEndings(value: string): string {
  return value.replace(/\r\n/g, '\n')
}

function modelAtSnapshot(providerCode: string, model: string) {
  const item = listProviderModelPricingAsOf(providerCode, PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE).find((candidate) => candidate.model === model)
  assert(item, `${providerCode}/${model} must exist at snapshot`)
  return item
}
