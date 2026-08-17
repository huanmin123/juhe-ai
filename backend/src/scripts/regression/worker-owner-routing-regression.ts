import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { resolveNodeWorkerOwnership, runIfNodeOwnsWorkerJob } from '../../shared/worker-owner.js'
import { CURRENT_RELEASE_SCHEMA_VERSION } from '../../shared/release-schema-version.js'

const job = 'migration-fixture-job'
const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-worker-owner-routing-'))

try {
  const v2NodePath = writeManifest('v2-node.json', manifest(2, 'epoch-v2-node', 'node'))
  assert.equal(decide(v2NodePath, 'epoch-v2-node').nodeOwns, true, 'v2 global Node owner should schedule')

  const v2GoPath = writeManifest('v2-go.json', manifest(2, 'epoch-v2-go', 'go'))
  assert.equal(decide(v2GoPath, 'epoch-v2-go').nodeOwns, false, 'v2 global Go owner should drain Node')

  const exactGoPath = writeManifest('v3-exact-go.json', manifest(3, 'epoch-exact-go', 'node', [
    { job, owner: 'go', rollbackOwner: 'node' }
  ]))
  const exactGo = decide(exactGoPath, 'epoch-exact-go')
  assert.equal(exactGo.nodeOwns, false, 'v3 exact Go owner should override global Node owner')
  assert.equal(exactGo.resolvedOwner, 'go')

  const exactNodePath = writeManifest('v3-exact-node.json', manifest(3, 'epoch-exact-node', 'go', [
    { job, owner: 'node', rollbackOwner: 'go' }
  ]))
  assert.equal(decide(exactNodePath, 'epoch-exact-node').nodeOwns, true, 'v3 exact Node owner should override global Go owner')

  const fallbackNodePath = writeManifest('v3-fallback-node.json', manifest(3, 'epoch-fallback-node', 'node', []))
  assert.equal(decide(fallbackNodePath, 'epoch-fallback-node').nodeOwns, true, 'unlisted v3 job should use global Node owner')

  const fallbackGoPath = writeManifest('v3-fallback-go.json', manifest(3, 'epoch-fallback-go', 'go', []))
  assert.equal(decide(fallbackGoPath, 'epoch-fallback-go').nodeOwns, false, 'unlisted v3 job should use global Go owner')

  assert.equal(
    resolveNodeWorkerOwnership({ enabled: false, manifestPath: 'relative.json' }, job).nodeOwns,
    true,
    'disabled owner lock should preserve legacy Node ownership'
  )
  assertFailClosed(decide(exactGoPath, 'wrong-epoch'), 'deployment_epoch_mismatch')
  assertFailClosed(resolveNodeWorkerOwnership({ enabled: true, manifestPath: 'relative.json', deploymentEpoch: 'epoch' }, job), 'invalid_config')
  assertFailClosed(decide(join(tempRoot, 'missing.json'), 'epoch'), 'manifest_unreadable')

  const invalidJsonPath = join(tempRoot, 'invalid.json')
  writeFileSync(invalidJsonPath, '{', 'utf8')
  assertFailClosed(decide(invalidJsonPath, 'epoch'), 'manifest_invalid')

  const malformedPath = writeManifest('malformed.json', {
    ...manifest(3, 'epoch-malformed', 'node', []),
    unexpected: true
  })
  assertFailClosed(decide(malformedPath, 'epoch-malformed'), 'manifest_invalid')

  const invalidWorkerPath = writeManifest('invalid-worker.json', manifest(3, 'epoch-invalid-worker', 'node', [
    { job, owner: 'go', rollbackOwner: 'go' }
  ]))
  assertFailClosed(decide(invalidWorkerPath, 'epoch-invalid-worker'), 'manifest_invalid')

  let scheduled = 0
  runIfNodeOwnsWorkerJob({ enabled: true, manifestPath: fallbackNodePath, deploymentEpoch: 'epoch-fallback-node' }, job, () => { scheduled += 1 })
  assert.equal(scheduled, 1, 'Node owner must execute scheduler registration once')
  runIfNodeOwnsWorkerJob({ enabled: true, manifestPath: exactGoPath, deploymentEpoch: 'epoch-exact-go' }, job, () => { scheduled += 1 })
  assert.equal(scheduled, 1, 'Go owner must not execute Node scheduler registration')
  runIfNodeOwnsWorkerJob({ enabled: true, manifestPath: 'relative.json', deploymentEpoch: 'epoch' }, job, () => { scheduled += 1 })
  assert.equal(scheduled, 1, 'invalid owner configuration must not execute Node scheduler registration')

  const wrongReleaseSchemaPath = writeManifest('wrong-release-schema.json', {
    ...manifest(3, 'epoch-wrong-schema', 'node', []),
    release: { nodeVersion: '0.1.0', goVersion: '0.1.0-w0', schemaVersion: CURRENT_RELEASE_SCHEMA_VERSION - 1 }
  })
  assertFailClosed(decide(wrongReleaseSchemaPath, 'epoch-wrong-schema'), 'manifest_invalid')

  const nonCanonicalRoutePath = writeManifest('non-canonical-route.json', {
    ...manifest(3, 'epoch-bad-route', 'node', []),
    routeAllowlist: [{
      surface: 'management',
      method: 'POST',
      path: '/__aisys__/api/accounts/',
      owner: 'go',
      rollbackOwner: 'node'
    }]
  })
  assertFailClosed(decide(nonCanonicalRoutePath, 'epoch-bad-route'), 'manifest_invalid')

  process.env.NODE_ENV = 'test'
  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_QUEUE_DRIVER = 'memory'
  process.env.JUHE_AI_OWNER_LOCK_ENABLED = 'true'
  process.env.JUHE_AI_OWNER_MANIFEST_PATH = fallbackNodePath
  process.env.JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH = 'epoch-fallback-node'
  const { runtimeConfig } = await import('../../config/runtime.js')
  assert.deepEqual(runtimeConfig.ownerLock, {
    enabled: true,
    manifestPath: fallbackNodePath,
    deploymentEpoch: 'epoch-fallback-node'
  }, 'runtime owner lock config must read the deployment manifest path and epoch environment variables')

  console.log('worker owner routing regression passed')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

function decide(manifestPath: string, deploymentEpoch: string) {
  return resolveNodeWorkerOwnership({ enabled: true, manifestPath, deploymentEpoch }, job)
}

function assertFailClosed(
  decision: ReturnType<typeof resolveNodeWorkerOwnership>,
  reason: ReturnType<typeof resolveNodeWorkerOwnership>['reason']
): void {
  assert.equal(decision.nodeOwns, false, `${reason} must fail closed`)
  assert.equal(decision.reason, reason)
}

function writeManifest(name: string, value: unknown): string {
  const path = join(tempRoot, name)
  writeFileSync(path, JSON.stringify(value), 'utf8')
  return path
}

function manifest(
  schemaVersion: 2 | 3,
  deploymentEpoch: string,
  worker: 'node' | 'go',
  workerAllowlist: Array<{ job: string; owner: 'node' | 'go'; rollbackOwner: 'node' | 'go' }> = []
): Record<string, unknown> {
  const ownerMap = { management: 'node', public: 'node', gateway: 'node', worker }
  const result: Record<string, unknown> = {
    schemaVersion,
    deploymentEpoch,
    release: { nodeVersion: '0.1.0', goVersion: '0.1.0-w0', schemaVersion: CURRENT_RELEASE_SCHEMA_VERSION },
    routeOwners: ownerMap,
    rollbackRouteOwners: { ...ownerMap },
    routeAllowlist: []
  }
  if (schemaVersion === 3) result.workerAllowlist = workerAllowlist
  return result
}
