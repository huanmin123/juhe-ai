import assert from 'node:assert/strict'

import {
  assertRequiredOwners,
  assertRequiredRelease,
  OwnerManifestValidationError,
  validateOwnerManifest
} from './validate-owner-manifest.mjs'

const valid = {
  schemaVersion: 1,
  deploymentEpoch: 'node-production-test',
  release: { nodeVersion: '0.1.0', goVersion: '0.1.0-w0', schemaVersion: 55 },
  routeOwners: { management: 'node', public: 'node', gateway: 'node', worker: 'node' }
}

validateOwnerManifest(valid)
assertRequiredOwners(valid, { management: 'node', gateway: 'node' })
assert.throws(() => assertRequiredOwners(valid, { management: 'go' }), OwnerManifestValidationError)
assertRequiredRelease(valid, { deploymentEpoch: 'node-production-test', nodeVersion: '0.1.0', schemaVersion: 55 })
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
