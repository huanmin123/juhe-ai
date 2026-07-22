import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  assertRequiredOwners,
  assertRequiredRelease,
  CURRENT_SCHEMA_VERSION,
  OwnerManifestValidationError,
  readMigrationCatalogSchemaVersion,
  validateOwnerManifest
} from './validate-owner-manifest.mjs'

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const currentMigrationVersion = await readMigrationCatalogSchemaVersion(path.join(repoRoot, 'backend-go/db/migrations'))
const currentManifest = JSON.parse(await readFile(path.join(repoRoot, 'deploy/owner-manifest.json'), 'utf8'))
const rootPackage = JSON.parse(await readFile(path.join(repoRoot, 'package.json'), 'utf8'))
const startupScripts = await Promise.all(
  ['deploy/start.ps1', 'deploy/start.sh'].map(async relativePath => ({
    relativePath,
    source: await readFile(path.join(repoRoot, relativePath), 'utf8')
  }))
)

const valid = {
  schemaVersion: 1,
  deploymentEpoch: 'node-production-test',
  release: { nodeVersion: '0.1.0', goVersion: '0.1.0-w0', schemaVersion: CURRENT_SCHEMA_VERSION },
  routeOwners: { management: 'node', public: 'node', gateway: 'node', worker: 'node' }
}

validateOwnerManifest(valid)
assert.equal(CURRENT_SCHEMA_VERSION, currentMigrationVersion)
assert.throws(
  () => validateOwnerManifest({
    ...valid,
    release: { ...valid.release, schemaVersion: currentMigrationVersion - 1 }
  }),
  OwnerManifestValidationError
)
assert.equal(currentManifest.release.schemaVersion, currentMigrationVersion)
assert.equal(
  rootPackage.scripts['validate:owner-manifest:node'].includes('--require-migration-catalog='),
  false,
  'release package validation must not depend on source-only migration catalog files'
)
for (const { relativePath, source } of startupScripts) {
  assert.equal(
    /--require-schema-version(?:=|\s)/u.test(source),
    false,
    `${relativePath} must use the validator current schema version instead of a duplicated literal`
  )
}
assertRequiredOwners(valid, { management: 'node', gateway: 'node' })
assert.throws(() => assertRequiredOwners(valid, { management: 'go' }), OwnerManifestValidationError)
assertRequiredRelease(valid, {
  deploymentEpoch: 'node-production-test',
  nodeVersion: '0.1.0',
  schemaVersion: CURRENT_SCHEMA_VERSION
})
assert.throws(() => assertRequiredRelease(valid, { nodeVersion: '0.2.0' }), OwnerManifestValidationError)

for (const invalid of [
  { ...valid, schemaVersion: 2 },
  { ...valid, deploymentEpoch: '' },
  { ...valid, release: { ...valid.release, schemaVersion: 0 } },
  { ...valid, routeOwners: { ...valid.routeOwners, management: 'python' } },
  { ...valid, routeOwners: { management: 'node' } }
]) {
  assert.throws(() => validateOwnerManifest(invalid), OwnerManifestValidationError)
}

process.stdout.write('Owner manifest validator tests passed.\n')
