import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  listProviderModelPricingAsOf
} from '../../backend/src/modules/model-pricing/model-pricing.service.js'
import {
  PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE,
  providerModelCatalogSnapshotSQL
} from './generate-provider-model-catalog.js'

const regressionDirectory = dirname(fileURLToPath(import.meta.url))
const migrationPath = resolve(
  regressionDirectory,
  '../db/migrations/000039_w2_sync_provider_model_catalog_tier_pricing.sql'
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

const migrationSQL = readFileSync(migrationPath, 'utf8')
assert.equal(
  migrationSQL,
  providerModelCatalogSnapshotSQL,
  '000039 must match the complete Node built-in catalog generated at the fixed snapshot date'
)
assert(
  migrationSQL.includes(`-- Snapshot as-of date: ${PROVIDER_MODEL_CATALOG_SNAPSHOT_AS_OF_DATE}`),
  'generated SQL must record the fixed snapshot as-of date'
)

console.log('provider model catalog snapshot regression passed')
