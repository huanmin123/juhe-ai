import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { validateOwnerManifest } from './validate-owner-manifest.mjs'

const scriptsDirectory = path.dirname(fileURLToPath(import.meta.url))
const fixturePath = path.join(scriptsDirectory, 'fixtures', 'owner-cutover-matrix.v1.json')
const fixture = JSON.parse(await readFile(fixturePath, 'utf8'))
const manifestPath = path.resolve(scriptsDirectory, fixture.manifestPath)
const manifest = validateOwnerManifest(JSON.parse(await readFile(manifestPath, 'utf8')))

const expectedStages = ['default', 'canary', 'cutover', 'rollback']
const expectedLanes = ['regular', 'canary']
const expectedHTTPRouteOwnerClasses = ['management', 'public']
const allowedOwners = new Set(['node', 'go'])

function routeKey(route) {
  return `${route.method} ${route.path}`
}

function sorted(values) {
  return [...values].sort()
}

function resolvedOwners(ownerReferences, route, ownerManifest) {
  return ownerReferences.map(owner => owner === 'manifest' ? ownerManifest.routeOwners[route.ownerClass] : owner)
}

function ownersAt(candidate, stageName, laneName, route, ownerManifest) {
  return resolvedOwners(candidate.stages[stageName][laneName][routeKey(route)], route, ownerManifest)
}

function assertOwnerCutoverMatrix(candidate, ownerManifest) {
  assert.equal(candidate.schemaVersion, 1, 'fixture schemaVersion must equal 1')
  assert.ok(allowedOwners.has(candidate.baselineOwner), 'fixture baselineOwner must be node or go')
  assert.equal(candidate.takeoverEvidence, false, 'fixture must not claim production takeover evidence')
  assert.equal(candidate.runtimeRoutingEvaluated, false, 'fixture must not claim runtime routing evaluation')
  assert.deepEqual(sorted(Object.keys(candidate.stages)), sorted(expectedStages), 'fixture must define exactly the four cutover stages')
  assert.ok(Array.isArray(candidate.routes) && candidate.routes.length > 0, 'fixture must define at least one route')

  const routeIDs = candidate.routes.map(route => route.id)
  assert.ok(routeIDs.every(id => typeof id === 'string' && id.length > 0), 'route ids must be non-empty strings')
  assert.equal(new Set(routeIDs).size, routeIDs.length, 'route ids must be unique')
  const routeKeys = candidate.routes.map(routeKey)
  assert.equal(new Set(routeKeys).size, routeKeys.length, 'method/path pairs must be unique')
  assert.deepEqual(
    sorted(new Set(candidate.routes.map(route => route.ownerClass))),
    sorted(expectedHTTPRouteOwnerClasses),
    'fixture must cover exactly management and public HTTP owners'
  )

  for (const route of candidate.routes) {
    assert.match(route.method, /^[A-Z]+$/, `${routeKey(route)} method must be uppercase`)
    assert.match(route.path, /^\/(?!\/)(?!.*[?#])\S*$/, `${routeKey(route)} path must be an exact path without query or fragment`)
    assert.ok(Object.hasOwn(ownerManifest.routeOwners, route.ownerClass), `${routeKey(route)} ownerClass must exist in owner manifest`)
    assert.equal(ownerManifest.routeOwners[route.ownerClass], candidate.baselineOwner, `${routeKey(route)} manifest owner must match fixture baselineOwner`)
    assert.ok(typeof route.goContractFile === 'string' && route.goContractFile.length > 0, `${routeKey(route)} must name Go contract evidence`)
    assert.ok(Array.isArray(route.nodeContractEvidence) && route.nodeContractEvidence.length > 0, `${routeKey(route)} must name Node contract evidence`)
  }

  for (const stageName of expectedStages) {
    const stage = candidate.stages[stageName]
    assert.deepEqual(sorted(Object.keys(stage)), sorted(expectedLanes), `${stageName} must define exactly regular and canary lanes`)

    for (const laneName of expectedLanes) {
      const ownershipByRoute = stage[laneName]
      assert.deepEqual(
        sorted(Object.keys(ownershipByRoute)),
        sorted(routeKeys),
        `${stageName}/${laneName} must cover each method/path exactly once`
      )
      for (const route of candidate.routes) {
        const ownerReferences = ownershipByRoute[routeKey(route)]
        assert.ok(Array.isArray(ownerReferences), `${stageName}/${laneName}/${routeKey(route)} must define owners`)
        const owners = resolvedOwners(ownerReferences, route, ownerManifest)
        assert.equal(owners.length, 1, `${stageName}/${laneName}/${routeKey(route)} must have exactly one owner`)
        assert.ok(allowedOwners.has(owners[0]), `${stageName}/${laneName}/${routeKey(route)} owner must be node or go`)
      }
    }
  }

  return candidate
}

assertOwnerCutoverMatrix(fixture, manifest)

for (const route of fixture.routes) {
  const goContract = await readFile(path.resolve(scriptsDirectory, '..', route.goContractFile), 'utf8')
  const goMethodToken = `http.Method${route.method[0]}${route.method.slice(1).toLowerCase()}`
  assert.ok(goContract.includes(goMethodToken), `${routeKey(route)} Go contract evidence must contain ${goMethodToken}`)
  assert.ok(goContract.includes(route.path), `${routeKey(route)} Go contract evidence must contain the exact path`)
  for (const evidence of route.nodeContractEvidence) {
    assert.ok(typeof evidence.file === 'string' && evidence.file.length > 0, `${routeKey(route)} Node evidence file must be named`)
    assert.ok(Array.isArray(evidence.tokens) && evidence.tokens.length > 0, `${routeKey(route)} Node evidence tokens must be non-empty`)
    const nodeContract = await readFile(path.resolve(scriptsDirectory, '..', evidence.file), 'utf8')
    for (const token of evidence.tokens) {
      assert.ok(nodeContract.includes(token), `${routeKey(route)} Node contract evidence ${evidence.file} must contain ${token}`)
    }
  }

  const baseline = manifest.routeOwners[route.ownerClass]
  assert.equal(ownersAt(fixture, 'default', 'regular', route, manifest)[0], baseline)
  assert.equal(ownersAt(fixture, 'default', 'canary', route, manifest)[0], baseline)
  assert.equal(ownersAt(fixture, 'canary', 'regular', route, manifest)[0], baseline)
  assert.equal(ownersAt(fixture, 'canary', 'canary', route, manifest)[0], 'go')
  assert.equal(ownersAt(fixture, 'cutover', 'regular', route, manifest)[0], 'go')
  assert.equal(ownersAt(fixture, 'cutover', 'canary', route, manifest)[0], 'go')
  assert.equal(ownersAt(fixture, 'rollback', 'regular', route, manifest)[0], 'node')
  assert.equal(ownersAt(fixture, 'rollback', 'canary', route, manifest)[0], 'node')
}

for (const invalid of [
  (() => {
    const value = structuredClone(fixture)
    value.stages.canary.canary[routeKey(value.routes[0])] = []
    return value
  })(),
  (() => {
    const value = structuredClone(fixture)
    value.stages.canary.canary[routeKey(value.routes[0])] = ['node', 'go']
    return value
  })(),
  (() => {
    const value = structuredClone(fixture)
    value.routes.push({ ...value.routes[0], id: 'duplicate-method-path' })
    return value
  })(),
  (() => {
    const value = structuredClone(fixture)
    value.routes = []
    return value
  })(),
  (() => {
    const value = structuredClone(fixture)
    delete value.stages.cutover.regular[routeKey(value.routes[1])]
    return value
  })(),
  (() => {
    const value = structuredClone(fixture)
    value.baselineOwner = 'go'
    return value
  })()
]) {
  assert.throws(() => assertOwnerCutoverMatrix(invalid, manifest), assert.AssertionError)
}

process.stdout.write(`Owner cutover matrix fixture contract passed: ${fixture.routes.length} routes x ${expectedStages.length} stages x ${expectedLanes.length} lanes; takeoverEvidence=false.\n`)
