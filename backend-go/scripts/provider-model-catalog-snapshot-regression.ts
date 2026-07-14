import assert from 'node:assert/strict'

import {
  listProviderModelPricingAsOf
} from '../../backend/src/modules/model-pricing/model-pricing.service.js'
import {
  PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE,
  providerModelCatalogSnapshotSQL
} from './generate-provider-model-catalog.js'

assert.equal(
  PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE,
  '2026-07-12',
  'W2 provider model catalog snapshot as-of date must remain explicit and fixed'
)

const deepSeekAtSnapshot = new Set(
  listProviderModelPricingAsOf('deepseek', PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE)
    .map((item) => item.model)
)
assert(deepSeekAtSnapshot.has('deepseek-chat'), 'fixed snapshot must retain DeepSeek alias before its shutdown date')
assert(deepSeekAtSnapshot.has('deepseek-reasoner'), 'fixed snapshot must retain DeepSeek reasoning alias before its shutdown date')

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
for (const legacy of ['pricing_model', 'priority_input_usd_per_1m', 'flex_input_usd_per_1m']) {
  assert.equal(providerModelCatalogSnapshotSQL.includes(legacy), false, `generated catalog must not use ${legacy}`)
}
assert.equal(providerModelCatalogSnapshotSQL.includes('input_usd_per_1m = EXCLUDED.input_usd_per_1m'), false, 'catalog sync must not overwrite administrator prices')

console.log('provider model catalog snapshot regression passed')
