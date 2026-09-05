// One-shot regeneration tool for model_catalog_data.go (migration period).
//
// It runs the exact Node functions that seed-defaults.ts /
// postgres-seed-defaults.ts use (listProviderModelPricing in the
// DEFAULT_PROVIDER_SEEDS order plus providerModelCatalogId) and emits the Go
// seed table next to this file. Run it with the backend tsx:
//
//   cd backend
//   ./node_modules/.bin/tsx ../backend-go/projects/maintenance/internal/schema/dump_model_catalog.mts
//
// The row count is data-driven: it changes whenever the Node pricing data
// changes (the generated file header records the dumped row count). After
// regenerating run gofmt and the schema tests.
import { writeFileSync } from 'node:fs'
import { DEFAULT_PROVIDER_SEEDS } from '../../../../../backend/src/storage/schema-defaults.js'
import { listProviderModelPricing } from '../../../../../backend/src/modules/model-pricing/model-pricing.service.js'
import { providerModelCatalogId } from '../../../../../backend/src/storage/provider-model-catalog-id.js'

interface SeedRow {
  [field: string]: unknown
}

const rows: SeedRow[] = []
for (const provider of DEFAULT_PROVIDER_SEEDS) {
  if (provider.code === 'hybrid' || provider.code === 'openai') continue
  for (const model of listProviderModelPricing(provider.code)) {
    rows.push({
      id: providerModelCatalogId(provider.code, model.model),
      provider_code: provider.code,
      model: model.model,
      mode: model.mode ?? null,
      catalog_order: model.catalogOrder ?? null,
      release_date: model.releaseDate ?? null,
      shutdown_date: model.shutdownDate ?? null,
      supported_api_protocols_json: JSON.stringify(model.supportedApiProtocols),
      supported_service_tiers_json: JSON.stringify(model.supportedServiceTiers),
      supported_reasoning_efforts_json: JSON.stringify(model.supportedReasoningEfforts),
      default_reasoning_effort: model.defaultReasoningEffort ?? null,
      codex_supported_reasoning_levels_json: JSON.stringify(model.codexSupportedReasoningLevels),
      codex_default_reasoning_level: model.codexDefaultReasoningLevel ?? null,
      codex_multi_agent_version: model.codexMultiAgentVersion ?? null,
      context_window_tokens: model.contextWindowTokens ?? null,
      max_input_tokens: model.maxInputTokens ?? null,
      max_output_tokens: model.maxOutputTokens ?? null,
      max_tokens: model.maxTokens ?? null,
      input_usd_per_1m: model.inputUsdPer1M ?? null,
      output_usd_per_1m: model.outputUsdPer1M ?? null,
      cached_input_usd_per_1m: model.cachedInputUsdPer1M ?? null,
      cache_write_usd_per_1m: model.cacheWriteUsdPer1M ?? null,
      cache_write_1h_usd_per_1m: model.cacheWrite1hUsdPer1M ?? null,
      cache_storage_usd_per_1m_per_hour: model.cacheStorageUsdPer1MPerHour ?? null,
      service_tier_prices_json: JSON.stringify(model.serviceTierPrices ?? {}),
      long_context_input_token_threshold: model.longContextInputTokenThreshold ?? null,
      long_context_input_token_threshold_inclusive: model.longContextInputTokenThresholdInclusive === true,
      long_context_input_cost_multiplier: model.longContextInputCostMultiplier ?? null,
      long_context_output_cost_multiplier: model.longContextOutputCostMultiplier ?? null,
      image_input_usd_per_1m: model.imageInputUsdPer1M ?? null,
      image_output_usd_per_1m: model.imageOutputUsdPer1M ?? null,
      audio_input_usd_per_1m: model.audioInputUsdPer1M ?? null,
      audio_output_usd_per_1m: model.audioOutputUsdPer1M ?? null,
      output_usd_per_image: model.outputUsdPerImage ?? null,
      supports_prompt_caching: model.supportsPromptCaching === true,
      catalog_visible: model.catalogVisible !== false,
      source: model.source
    })
  }
}

const intFields = ['catalog_order', 'context_window_tokens', 'max_input_tokens', 'max_output_tokens', 'max_tokens', 'long_context_input_token_threshold']
const floatFields = ['input_usd_per_1m', 'output_usd_per_1m', 'cached_input_usd_per_1m', 'cache_write_usd_per_1m', 'cache_write_1h_usd_per_1m', 'cache_storage_usd_per_1m_per_hour', 'long_context_input_cost_multiplier', 'long_context_output_cost_multiplier', 'image_input_usd_per_1m', 'image_output_usd_per_1m', 'audio_input_usd_per_1m', 'audio_output_usd_per_1m', 'output_usd_per_image']
const strPtrFields = ['mode', 'release_date', 'shutdown_date', 'default_reasoning_effort', 'codex_default_reasoning_level', 'codex_multi_agent_version']
const strFields = ['id', 'provider_code', 'model', 'supported_api_protocols_json', 'supported_service_tiers_json', 'supported_reasoning_efforts_json', 'codex_supported_reasoning_levels_json', 'service_tier_prices_json', 'source']
const boolFields = ['long_context_input_token_threshold_inclusive', 'supports_prompt_caching', 'catalog_visible']

for (const row of rows) {
  for (const field of intFields) {
    if (row[field] !== null && !Number.isSafeInteger(row[field])) throw new Error(`field ${field} not integer: ${String(row[field])}`)
  }
  for (const field of floatFields) {
    if (row[field] !== null && (typeof row[field] !== 'number' || !Number.isFinite(row[field]))) throw new Error(`field ${field} not finite number: ${String(row[field])}`)
  }
}

const goName = (field: string): string => field
  .split('_')
  .map((part) => (part.length <= 2 ? part.toUpperCase() : part[0].toUpperCase() + part.slice(1)))
  .join('')
  .replace(/Json$/, 'JSON')
  .replace(/Api/, 'API')
const goValue = (field: string, value: unknown): string => {
  if (value === null) return 'nil'
  if (strFields.includes(field)) return JSON.stringify(value as string)
  if (strPtrFields.includes(field)) return `seedStrPtr(${JSON.stringify(value as string)})`
  if (intFields.includes(field)) return `seedInt64Ptr(${String(value as number)})`
  if (floatFields.includes(field)) return `seedFloat64Ptr(${String(value as number)})`
  if (boolFields.includes(field)) return value ? 'true' : 'false'
  throw new Error('unknown field ' + field)
}
const fieldOrder = ['id', 'provider_code', 'model', 'mode', 'catalog_order', 'release_date', 'shutdown_date', 'supported_api_protocols_json', 'supported_service_tiers_json', 'supported_reasoning_efforts_json', 'default_reasoning_effort', 'codex_supported_reasoning_levels_json', 'codex_default_reasoning_level', 'codex_multi_agent_version', 'context_window_tokens', 'max_input_tokens', 'max_output_tokens', 'max_tokens', 'input_usd_per_1m', 'output_usd_per_1m', 'cached_input_usd_per_1m', 'cache_write_usd_per_1m', 'cache_write_1h_usd_per_1m', 'cache_storage_usd_per_1m_per_hour', 'service_tier_prices_json', 'long_context_input_token_threshold', 'long_context_input_token_threshold_inclusive', 'long_context_input_cost_multiplier', 'long_context_output_cost_multiplier', 'image_input_usd_per_1m', 'image_output_usd_per_1m', 'audio_input_usd_per_1m', 'audio_output_usd_per_1m', 'output_usd_per_image', 'supports_prompt_caching', 'catalog_visible', 'source']

const dumpedDate = new Date().toISOString().slice(0, 10)
const body = rows
  .map((row) => '\t{' + fieldOrder.map((field) => goName(field) + ': ' + goValue(field, row[field])).join(', ') + '},')
  .join('\n')
const generated = [
  '// Code generated from the Node pricing catalog (seed-defaults.ts model upsert',
  '// rows) by dump_model_catalog.mts next to this file. DO NOT hand-edit:',
  '// regenerate with the backend tsx (see the dump script header) and re-verify',
  '// the row count against the fresh Node dump. The rows are the ' + String(rows.length),
  '// static rows (39 seed parameters minus created_at/updated_at) that Node',
  '// seedDefaults/seedPostgresDefaults insert for the built-in pricing providers',
  '// (gpt, xai, deepseek, anthropic, gemini, glm; hybrid and openai are skipped',
  '// exactly like Node). Row order is the Node listProviderModelPricing order',
  '// (catalog order, release date desc, model asc) and is asserted sorted by',
  '// model_catalog_seed_test.go. Dumped ' + dumpedDate + '.',
  '',
  'package schema',
  '',
  '// seedStrPtr/seedInt64Ptr/seedFloat64Ptr build the optional seed parameters',
  '// (the Node "... ?? null" columns).',
  'func seedStrPtr(value string) *string       { return &value }',
  'func seedInt64Ptr(value int64) *int64       { return &value }',
  'func seedFloat64Ptr(value float64) *float64 { return &value }',
  '',
  '// modelCatalogSeedRow mirrors one provider_model_catalog seed row with the',
  '// static columns of Node seedDefaults (seed-defaults.ts modelStatement) and',
  '// seedPostgresDefaults (postgres-seed-defaults.ts modelSeedValues). JSON',
  '// columns keep the exact Node JSON.stringify output.',
  'type modelCatalogSeedRow struct {',
  '\tID                                      string',
  '\tProviderCode                            string',
  '\tModel                                   string',
  '\tMode                                    *string',
  '\tCatalogOrder                            *int64',
  '\tReleaseDate                             *string',
  '\tShutdownDate                            *string',
  '\tSupportedAPIProtocolsJSON               string',
  '\tSupportedServiceTiersJSON               string',
  '\tSupportedReasoningEffortsJSON           string',
  '\tDefaultReasoningEffort                  *string',
  '\tCodexSupportedReasoningLevelsJSON       string',
  '\tCodexDefaultReasoningLevel              *string',
  '\tCodexMultiAgentVersion                  *string',
  '\tContextWindowTokens                     *int64',
  '\tMaxInputTokens                          *int64',
  '\tMaxOutputTokens                         *int64',
  '\tMaxTokens                               *int64',
  '\tInputUsdPer1M                           *float64',
  '\tOutputUsdPer1M                          *float64',
  '\tCachedInputUsdPer1M                     *float64',
  '\tCacheWriteUsdPer1M                      *float64',
  '\tCacheWrite1HUsdPer1M                    *float64',
  '\tCacheStorageUsdPer1MPerHour             *float64',
  '\tServiceTierPricesJSON                   string',
  '\tLongContextInputTokenThreshold          *int64',
  '\tLongContextInputTokenThresholdInclusive bool',
  '\tLongContextInputCostMultiplier          *float64',
  '\tLongContextOutputCostMultiplier         *float64',
  '\tImageInputUsdPer1M                      *float64',
  '\tImageOutputUsdPer1M                     *float64',
  '\tAudioInputUsdPer1M                      *float64',
  '\tAudioOutputUsdPer1M                     *float64',
  '\tOutputUsdPerImage                       *float64',
  '\tSupportsPromptCaching                   bool',
  '\tCatalogVisible                          bool',
  '\tSource                                  string',
  '}',
  '',
  '// modelCatalogSeedRows is the frozen Node pricing snapshot. Shutdown',
  '// filtering stays runtime-side: seed code re-applies the Node shutdown_date',
  '// <= current-UTC-date rule before inserting.',
  'var modelCatalogSeedRows = []modelCatalogSeedRow{',
  body,
  '}',
  ''
].join('\n')
writeFileSync(new URL('./model_catalog_data.go', import.meta.url), generated)
console.log('generated model_catalog_data.go rows=' + String(rows.length))
