import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

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
assert.doesNotMatch(providerModelCatalogSnapshotSQL, /\n[ \t]+\n/, 'generated catalog SQL must not contain whitespace-only value rows')
assert.doesNotMatch(providerModelCatalogSnapshotSQL, /,\n\s*\n\s*\)/, 'generated catalog SQL must not leave a trailing comma before a tuple closes')
assert.equal(
  normalizeSnapshotLineEndings(
    readFileSync(resolve(process.cwd(), '../backend-go/db/migrations/000047_w2_sync_provider_model_catalog_unified_pricing.sql'), 'utf8')
  ),
  normalizeSnapshotLineEndings(providerModelCatalogSnapshotSQL),
  'unified provider catalog seed migration must match the generated current-schema snapshot'
)

console.log('provider model catalog snapshot regression passed')

function normalizeSnapshotLineEndings(value: string): string {
  return value.replace(/\r\n/g, '\n')
}
