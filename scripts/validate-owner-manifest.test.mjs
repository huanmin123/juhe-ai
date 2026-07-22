import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  assertAllRoutesOwnedBy,
  assertRequiredOwners,
  assertRequiredRelease,
  createRollbackManifest,
  CURRENT_SCHEMA_VERSION,
  OwnerManifestValidationError,
  readMigrationCatalogSchemaVersion,
  resolveRouteOwner,
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

const legacy = {
  schemaVersion: 1,
  deploymentEpoch: 'node-production-test',
  release: { nodeVersion: '0.1.0', goVersion: '0.1.0-w0', schemaVersion: CURRENT_SCHEMA_VERSION },
  routeOwners: { management: 'node', public: 'node', gateway: 'node', worker: 'node' }
}

const valid = {
  ...legacy,
  schemaVersion: 2,
  rollbackRouteOwners: { management: 'node', public: 'node', gateway: 'node', worker: 'node' },
  routeAllowlist: [
    {
      surface: 'management',
      method: 'GET',
      path: '/__aisys__/api/accounts/{accountId}',
      owner: 'go',
      rollbackOwner: 'node'
    },
    {
      surface: 'public',
      method: 'GET',
      path: '/__aipublic__/announcements',
      owner: 'go',
      rollbackOwner: 'node'
    },
    {
      surface: 'gateway',
      method: 'POST',
      path: '/v1/responses',
      owner: 'go',
      rollbackOwner: 'node'
    }
  ]
}

// Schema v1 remains structurally valid during the manifest format transition.
validateOwnerManifest(legacy)
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
assert.equal(
  rootPackage.scripts['validate:owner-manifest:node'].includes('--require-schema-version='),
  false,
  'release package validation must use the validator current schema version instead of a duplicated literal'
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

assert.equal(resolveRouteOwner(legacy, { surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/a-1' }), 'node')
assert.equal(resolveRouteOwner(valid, { surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/a-1' }), 'go')
assert.equal(resolveRouteOwner(valid, { surface: 'management', method: 'PATCH', path: '/__aisys__/api/accounts/a-1' }), 'node')
assert.equal(resolveRouteOwner(valid, { surface: 'gateway', method: 'POST', path: '/v1/responses' }), 'go')
assert.equal(resolveRouteOwner(valid, { surface: 'gateway', method: 'POST', path: '/v1/responses/' }), 'node')

const headOnly = {
  ...valid,
  routeAllowlist: [
    { surface: 'gateway', method: 'HEAD', path: '/v1/models', owner: 'go', rollbackOwner: 'node' }
  ]
}
assert.equal(resolveRouteOwner(headOnly, { surface: 'gateway', method: 'HEAD', path: '/v1/models' }), 'go')
assert.equal(resolveRouteOwner(headOnly, { surface: 'gateway', method: 'GET', path: '/v1/models' }), 'node')

const getOnly = {
  ...valid,
  routeAllowlist: [
    { surface: 'gateway', method: 'GET', path: '/v1/models', owner: 'go', rollbackOwner: 'node' }
  ]
}
assert.equal(resolveRouteOwner(getOnly, { surface: 'gateway', method: 'GET', path: '/v1/models' }), 'go')
assert.equal(resolveRouteOwner(getOnly, { surface: 'gateway', method: 'HEAD', path: '/v1/models' }), 'node')

const geminiActions = {
  ...valid,
  routeAllowlist: [
    {
      surface: 'gateway',
      method: 'POST',
      path: '/v1beta/models/{model}:generateContent',
      owner: 'go',
      rollbackOwner: 'node'
    },
    {
      surface: 'gateway',
      method: 'POST',
      path: '/v1beta/models/{model}:streamGenerateContent',
      owner: 'node',
      rollbackOwner: 'go'
    }
  ]
}
assert.equal(
  resolveRouteOwner(geminiActions, {
    surface: 'gateway',
    method: 'POST',
    path: '/v1beta/models/gemini-3.5-flash:generateContent'
  }),
  'go'
)
assert.equal(
  resolveRouteOwner(geminiActions, {
    surface: 'gateway',
    method: 'POST',
    path: '/v1beta/models/gemini-3.5-flash:streamGenerateContent'
  }),
  'node'
)
assert.equal(
  resolveRouteOwner(geminiActions, {
    surface: 'gateway',
    method: 'POST',
    path: '/v1beta/models/gemini-3.5-flash:countTokens'
  }),
  'node'
)

assert.throws(() => assertAllRoutesOwnedBy(valid, 'node'), /routeAllowlist\[0\].*go/)
assertAllRoutesOwnedBy({
  ...valid,
  routeAllowlist: valid.routeAllowlist.map(route => ({ ...route, owner: 'node', rollbackOwner: 'go' }))
}, 'node')

const rollback = createRollbackManifest(valid, 'rollback-2026-07-22-001')
assert.equal(rollback.deploymentEpoch, 'rollback-2026-07-22-001')
assert.deepEqual(rollback.routeOwners, valid.rollbackRouteOwners)
assert.deepEqual(rollback.rollbackRouteOwners, valid.routeOwners)
assert.equal(rollback.routeAllowlist[0].owner, 'node')
assert.equal(rollback.routeAllowlist[0].rollbackOwner, 'go')
assert.equal(resolveRouteOwner(rollback, { surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/a-1' }), 'node')
assert.throws(() => createRollbackManifest(legacy, 'rollback-legacy'), /schemaVersion 2/)
assert.throws(() => createRollbackManifest(valid, valid.deploymentEpoch), /different/)

for (const invalid of [
  { ...legacy, schemaVersion: 3 },
  { ...legacy, deploymentEpoch: '' },
  { ...legacy, release: { ...legacy.release, schemaVersion: 0 } },
  { ...legacy, routeOwners: { ...legacy.routeOwners, management: 'python' } },
  { ...legacy, routeOwners: { management: 'node' } },
  { ...legacy, routeAllowlist: [] },
  { ...valid, rollbackRouteOwners: { management: 'node' } },
  { ...valid, routeAllowlist: 'not-an-array' }
]) {
  assert.throws(() => validateOwnerManifest(invalid), OwnerManifestValidationError)
}

const invalidRouteCases = [
  [{ surface: 'worker', method: 'GET', path: '/jobs/run', owner: 'go', rollbackOwner: 'node' }, /surface/],
  [{ surface: 'management', method: '*', path: '/__aisys__/api/accounts', owner: 'go', rollbackOwner: 'node' }, /method/],
  [{ surface: 'management', method: 'get', path: '/__aisys__/api/accounts', owner: 'go', rollbackOwner: 'node' }, /method/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/*', owner: 'go', rollbackOwner: 'node' }, /wildcards/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/:id', owner: 'go', rollbackOwner: 'node' }, /wildcards/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/{id}.json', owner: 'go', rollbackOwner: 'node' }, /template/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/{id}/{id}', owner: 'go', rollbackOwner: 'node' }, /parameter names/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/', owner: 'go', rollbackOwner: 'node' }, /canonical/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api//accounts', owner: 'go', rollbackOwner: 'node' }, /canonical/],
  [{ surface: 'management', method: 'GET', path: '/__aisys__/api/accounts%2Fsecret', owner: 'go', rollbackOwner: 'node' }, /encoded/],
  [{ surface: 'management', method: 'GET', path: '/__aipublic__/accounts', owner: 'go', rollbackOwner: 'node' }, /surface/],
  [{ surface: 'gateway', method: 'POST', path: '/__aisys__/api/accounts', owner: 'go', rollbackOwner: 'node' }, /reserved/],
  [{ surface: 'gateway', method: 'POST', path: '/{surface}/api/accounts', owner: 'go', rollbackOwner: 'node' }, /first segment/],
  [{ surface: 'gateway', method: 'POST', path: '/{prefix}/{rest}', owner: 'go', rollbackOwner: 'node' }, /first segment/],
  [{ surface: 'gateway', method: 'POST', path: '/v1/responses', owner: 'go', rollbackOwner: 'go' }, /must differ/],
  [{ surface: 'gateway', method: 'POST', path: '/v1/responses', owner: 'rust', rollbackOwner: 'node' }, /owner/]
]

for (const [route, expectedError] of invalidRouteCases) {
  assert.throws(
    () => validateOwnerManifest({ ...valid, routeAllowlist: [route] }),
    expectedError
  )
}

for (const overlappingRoutes of [
  [
    { surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/{id}', owner: 'go', rollbackOwner: 'node' },
    { surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/current', owner: 'go', rollbackOwner: 'node' }
  ],
  [
    { surface: 'gateway', method: 'POST', path: '/v1/{resource}/{id}', owner: 'go', rollbackOwner: 'node' },
    { surface: 'gateway', method: 'POST', path: '/v1/responses/{responseId}', owner: 'go', rollbackOwner: 'node' }
  ],
  [
    { surface: 'gateway', method: 'POST', path: '/v1beta/models/{model}', owner: 'go', rollbackOwner: 'node' },
    { surface: 'gateway', method: 'POST', path: '/v1beta/models/{model}:generateContent', owner: 'go', rollbackOwner: 'node' }
  ],
  [
    { surface: 'gateway', method: 'POST', path: '/v1beta/models/{model}:generateContent', owner: 'go', rollbackOwner: 'node' },
    { surface: 'gateway', method: 'POST', path: '/v1beta/models/gemini-3.5-flash:generateContent', owner: 'go', rollbackOwner: 'node' }
  ]
]) {
  assert.throws(
    () => validateOwnerManifest({ ...valid, routeAllowlist: overlappingRoutes }),
    /overlaps/
  )
}

validateOwnerManifest({
  ...valid,
  routeAllowlist: [
    { surface: 'gateway', method: 'GET', path: '/v1/models', owner: 'go', rollbackOwner: 'node' },
    { surface: 'gateway', method: 'HEAD', path: '/v1/models', owner: 'node', rollbackOwner: 'go' }
  ]
})

validateOwnerManifest({
  ...valid,
  routeAllowlist: [
    { surface: 'management', method: 'GET', path: '/__aisys__/api/accounts/{id}', owner: 'go', rollbackOwner: 'node' },
    { surface: 'management', method: 'PATCH', path: '/__aisys__/api/accounts/current', owner: 'go', rollbackOwner: 'node' }
  ]
})

process.stdout.write('Owner manifest validator tests passed.\n')
