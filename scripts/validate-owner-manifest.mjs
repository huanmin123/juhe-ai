#!/usr/bin/env node

import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REQUIRED_ROUTE_OWNERS = ['management', 'public', 'gateway', 'worker']
const HTTP_ROUTE_SURFACES = new Set(['management', 'public', 'gateway'])
const ALLOWED_OWNERS = new Set(['node', 'go'])
const MIGRATION_FILENAME_PATTERN = /^([0-9]{6})_[a-z0-9_]+\.sql$/

// Release packages omit the source migration catalog; the source-tree regression derives and verifies this value.
export const CURRENT_SCHEMA_VERSION = 93
const ALLOWED_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'])
const MAX_EXACT_ROUTES = 2048
const MAX_WORKER_JOBS = 256
const LEGACY_FIELDS = ['deploymentEpoch', 'release', 'routeOwners', 'schemaVersion']
const V2_FIELDS = [...LEGACY_FIELDS, 'rollbackRouteOwners', 'routeAllowlist']
const CURRENT_FIELDS = [...V2_FIELDS, 'workerAllowlist']
const RELEASE_FIELDS = ['goVersion', 'nodeVersion', 'schemaVersion']
const ROUTE_FIELDS = ['method', 'owner', 'path', 'rollbackOwner', 'surface']
const WORKER_JOB_FIELDS = ['job', 'owner', 'rollbackOwner']
const WORKER_JOB_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/

export class OwnerManifestValidationError extends Error {
  constructor(message) {
    super(message)
    this.name = 'OwnerManifestValidationError'
  }
}

function fail(reason) {
  throw new OwnerManifestValidationError(`Owner manifest validation failed: ${reason}`)
}

function isRecord(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function requireNonEmptyString(value, label) {
  if (typeof value !== 'string' || value.trim() === '') fail(`${label} must be a non-empty string`)
  return value
}

function requireExactFields(value, expectedFields, label) {
  const actual = Object.keys(value).sort()
  const expected = [...expectedFields].sort()
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    fail(`${label} must contain exactly ${expectedFields.join(', ')}`)
  }
}

function validateOwnerMap(ownerMap, label) {
  if (!isRecord(ownerMap)) fail(`${label} must be an object`)
  requireExactFields(ownerMap, REQUIRED_ROUTE_OWNERS, label)
  for (const route of REQUIRED_ROUTE_OWNERS) {
    if (!ALLOWED_OWNERS.has(ownerMap[route])) fail(`${label}.${route} must be node or go`)
  }
}

function isParameterSegment(segment) {
  return /^\{[A-Za-z][A-Za-z0-9_]*\}$/.test(segment)
}

function parseActionParameterSegment(segment) {
  const match = /^\{([A-Za-z][A-Za-z0-9_]*)\}(:[A-Za-z][A-Za-z0-9._~-]*)$/.exec(segment)
  return match === null ? undefined : { parameterName: match[1], suffix: match[2] }
}

function segmentParameterName(segment) {
  if (isParameterSegment(segment)) return segment.slice(1, -1)
  return parseActionParameterSegment(segment)?.parameterName
}

function parseCanonicalPath(routePath, label, { allowTemplates }) {
  requireNonEmptyString(routePath, label)
  if (!routePath.startsWith('/')) fail(`${label} must be an absolute path`)
  if (routePath.includes('?') || routePath.includes('#') || routePath.includes('\\')) {
    fail(`${label} must not contain a query, fragment, or backslash`)
  }
  if (routePath.includes('%')) fail(`${label} must not contain encoded path bytes`)
  if (routePath.includes('*')) fail(`${label} must not contain unsafe wildcards`)
  if (routePath !== '/' && (routePath.endsWith('/') || routePath.includes('//'))) {
    fail(`${label} must use a canonical path without empty or trailing segments`)
  }

  const segments = routePath === '/' ? [] : routePath.slice(1).split('/')
  const parameterNames = new Set()
  for (const segment of segments) {
    if (segment === '.' || segment === '..') fail(`${label} must use a canonical path without dot segments`)
    if (segment.startsWith(':')) fail(`${label} must not contain unsafe wildcards`)
    const parameterName = segmentParameterName(segment)
    if (parameterName !== undefined) {
      if (!allowTemplates) return undefined
      if (parameterNames.has(parameterName)) fail(`${label} must use unique parameter names`)
      parameterNames.add(parameterName)
      continue
    }
    if (segment.includes('{') || segment.includes('}')) {
      fail(`${label} template parameters must occupy a complete segment`)
    }
    if (!/^[A-Za-z0-9._~:-]+$/.test(segment)) {
      fail(`${label} contains unsupported path characters`)
    }
  }
  return segments
}

function validateSurfacePath(surface, routePath, label) {
  if (surface === 'management') {
    if (routePath !== '/__aisys__/api' && !routePath.startsWith('/__aisys__/api/')) {
      fail(`${label} is outside the management surface`)
    }
    return
  }
  if (surface === 'public') {
    if (routePath !== '/__aipublic__' && !routePath.startsWith('/__aipublic__/')) {
      fail(`${label} is outside the public surface`)
    }
    return
  }
  if (routePath === '/__aisys__' || routePath.startsWith('/__aisys__/')
    || routePath === '/__aipublic__' || routePath.startsWith('/__aipublic__/')) {
    fail(`${label} uses a management or public reserved prefix on the gateway surface`)
  }
  const firstSegment = routePath === '/' ? undefined : routePath.slice(1).split('/', 1)[0]
  if (firstSegment !== undefined && isParameterSegment(firstSegment)) {
    fail(`${label} gateway first segment must be literal to protect reserved surfaces`)
  }
}

function methodsOverlap(left, right) {
  return left === right
}

function methodMatchesRequest(routeMethod, requestMethod) {
  return routeMethod === requestMethod
}

function pathTemplatesOverlap(leftSegments, rightSegments) {
  if (leftSegments.length !== rightSegments.length) return false
  return leftSegments.every((left, index) => {
    const right = rightSegments[index]
    return segmentTemplatesOverlap(left, right)
  })
}

function actionSuffix(segment) {
  return parseActionParameterSegment(segment)?.suffix
}

function literalMatchesActionTemplate(literal, template) {
  const suffix = actionSuffix(template)
  return suffix !== undefined && literal.length > suffix.length && literal.endsWith(suffix)
}

function segmentTemplatesOverlap(left, right) {
  if (isParameterSegment(left) || isParameterSegment(right)) return true
  const leftSuffix = actionSuffix(left)
  const rightSuffix = actionSuffix(right)
  if (leftSuffix !== undefined && rightSuffix !== undefined) return leftSuffix === rightSuffix
  if (leftSuffix !== undefined) return literalMatchesActionTemplate(right, left)
  if (rightSuffix !== undefined) return literalMatchesActionTemplate(left, right)
  return left === right
}

function validateRouteAllowlist(routeAllowlist) {
  if (!Array.isArray(routeAllowlist)) fail('routeAllowlist must be an array')
  if (routeAllowlist.length > MAX_EXACT_ROUTES) {
    fail(`routeAllowlist must contain at most ${MAX_EXACT_ROUTES} routes`)
  }

  const parsedRoutes = routeAllowlist.map((route, index) => {
    const label = `routeAllowlist[${index}]`
    if (!isRecord(route)) fail(`${label} must be an object`)
    requireExactFields(route, ROUTE_FIELDS, label)
    if (!HTTP_ROUTE_SURFACES.has(route.surface)) {
      fail(`${label}.surface must be management, public, or gateway`)
    }
    if (!ALLOWED_METHODS.has(route.method)) {
      fail(`${label}.method must be an explicit uppercase HTTP method`)
    }
    if (!ALLOWED_OWNERS.has(route.owner)) fail(`${label}.owner must be node or go`)
    if (!ALLOWED_OWNERS.has(route.rollbackOwner)) fail(`${label}.rollbackOwner must be node or go`)
    if (route.owner === route.rollbackOwner) fail(`${label}.owner and rollbackOwner must differ`)
    const segments = parseCanonicalPath(route.path, `${label}.path`, { allowTemplates: true })
    validateSurfacePath(route.surface, route.path, `${label}.path`)
    return { ...route, segments, index }
  })

  for (let leftIndex = 0; leftIndex < parsedRoutes.length; leftIndex += 1) {
    const left = parsedRoutes[leftIndex]
    for (let rightIndex = leftIndex + 1; rightIndex < parsedRoutes.length; rightIndex += 1) {
      const right = parsedRoutes[rightIndex]
      if (methodsOverlap(left.method, right.method) && pathTemplatesOverlap(left.segments, right.segments)) {
        fail(`routeAllowlist[${left.index}] overlaps routeAllowlist[${right.index}]`)
      }
    }
  }
}

function validateWorkerAllowlist(workerAllowlist) {
  if (!Array.isArray(workerAllowlist)) fail('workerAllowlist must be an array')
  if (workerAllowlist.length > MAX_WORKER_JOBS) {
    fail(`workerAllowlist must contain at most ${MAX_WORKER_JOBS} jobs`)
  }

  const seenJobs = new Set()
  for (const [index, workerJob] of workerAllowlist.entries()) {
    const label = `workerAllowlist[${index}]`
    if (!isRecord(workerJob)) fail(`${label} must be an object`)
    requireExactFields(workerJob, WORKER_JOB_FIELDS, label)
    if (typeof workerJob.job !== 'string' || !WORKER_JOB_PATTERN.test(workerJob.job)) {
      fail(`${label}.job must be a lowercase kebab-case job name`)
    }
    if (seenJobs.has(workerJob.job)) fail(`workerAllowlist contains duplicate job ${JSON.stringify(workerJob.job)}`)
    seenJobs.add(workerJob.job)
    if (!ALLOWED_OWNERS.has(workerJob.owner)) fail(`${label}.owner must be node or go`)
    if (!ALLOWED_OWNERS.has(workerJob.rollbackOwner)) fail(`${label}.rollbackOwner must be node or go`)
    if (workerJob.owner === workerJob.rollbackOwner) fail(`${label}.owner and rollbackOwner must differ`)
  }
}

export function validateOwnerManifest(manifest) {
  if (!isRecord(manifest)) fail('manifest must be an object')
  if (manifest.schemaVersion !== 1 && manifest.schemaVersion !== 2 && manifest.schemaVersion !== 3) {
    fail('schemaVersion must equal 1, 2, or 3')
  }
  const expectedFields = manifest.schemaVersion === 1
    ? LEGACY_FIELDS
    : manifest.schemaVersion === 2 ? V2_FIELDS : CURRENT_FIELDS
  requireExactFields(manifest, expectedFields, 'manifest')
  requireNonEmptyString(manifest.deploymentEpoch, 'deploymentEpoch')

  if (!isRecord(manifest.release)) fail('release must be an object')
  requireExactFields(manifest.release, RELEASE_FIELDS, 'release')
  requireNonEmptyString(manifest.release.nodeVersion, 'release.nodeVersion')
  requireNonEmptyString(manifest.release.goVersion, 'release.goVersion')
  if (manifest.release.schemaVersion !== CURRENT_SCHEMA_VERSION) {
    fail(`release.schemaVersion is ${manifest.release.schemaVersion}, expected current schema version ${CURRENT_SCHEMA_VERSION}`)
  }

  validateOwnerMap(manifest.routeOwners, 'routeOwners')
  if (manifest.schemaVersion >= 2) {
    validateOwnerMap(manifest.rollbackRouteOwners, 'rollbackRouteOwners')
    validateRouteAllowlist(manifest.routeAllowlist)
  }
  if (manifest.schemaVersion === 3) validateWorkerAllowlist(manifest.workerAllowlist)
  return manifest
}

export async function readAndValidateOwnerManifest(manifestPath) {
  let parsed
  try {
    parsed = JSON.parse(await readFile(manifestPath, 'utf8'))
  } catch (error) {
    const reason = error instanceof SyntaxError ? 'manifest must contain valid JSON' : 'manifest could not be read'
    fail(reason)
  }
  return validateOwnerManifest(parsed)
}

export async function readMigrationCatalogSchemaVersion(catalogPath) {
  let entries
  try {
    entries = await readdir(catalogPath, { withFileTypes: true })
  } catch {
    fail('migration catalog could not be read')
  }

  const versions = []
  const namesByVersion = new Map()
  for (const entry of entries) {
    if (!entry.isFile()) fail(`migration catalog contains non-file entry ${JSON.stringify(entry.name)}`)
    const match = MIGRATION_FILENAME_PATTERN.exec(entry.name)
    if (!match) fail(`invalid migration filename ${JSON.stringify(entry.name)}`)
    const version = Number(match[1])
    if (version < 1) fail(`migration version must be positive in ${JSON.stringify(entry.name)}`)
    const previous = namesByVersion.get(version)
    if (previous !== undefined) {
      fail(`migration version ${version} is duplicated by ${JSON.stringify(previous)} and ${JSON.stringify(entry.name)}`)
    }
    namesByVersion.set(version, entry.name)
    versions.push(version)
  }

  versions.sort((left, right) => left - right)
  if (versions.length === 0) fail('migration catalog is empty')
  for (const [index, version] of versions.entries()) {
    const expected = index + 1
    if (version !== expected) fail(`migration catalog version ${version} is not contiguous; expected ${expected}`)
  }
  return versions.at(-1)
}

function parseRequiredOwners(value) {
  if (value === undefined) return undefined
  const result = {}
  for (const entry of value.split(',')) {
    const [route, owner, ...extra] = entry.split('=')
    if (!route || !owner || extra.length > 0 || !REQUIRED_ROUTE_OWNERS.includes(route)) {
      throw new OwnerManifestValidationError(`invalid required owner expression: ${entry}`)
    }
    if (!ALLOWED_OWNERS.has(owner)) {
      throw new OwnerManifestValidationError(`required owner for ${route} must be node or go`)
    }
    result[route] = owner
  }
  return result
}

export function assertRequiredOwners(manifest, requiredOwners) {
  validateOwnerManifest(manifest)
  for (const [route, owner] of Object.entries(requiredOwners ?? {})) {
    if (!REQUIRED_ROUTE_OWNERS.includes(route) || !ALLOWED_OWNERS.has(owner)) {
      fail(`invalid required owner ${route}=${owner}`)
    }
    if (manifest.routeOwners[route] !== owner) {
      fail(`routeOwners.${route} is ${manifest.routeOwners[route]}, expected ${owner}`)
    }
  }
  return manifest
}

export function assertAllRoutesOwnedBy(manifest, owner) {
  validateOwnerManifest(manifest)
  if (!ALLOWED_OWNERS.has(owner)) fail('required all-routes owner must be node or go')
  for (const route of REQUIRED_ROUTE_OWNERS) {
    if (manifest.routeOwners[route] !== owner) {
      fail(`routeOwners.${route} is ${manifest.routeOwners[route]}, expected ${owner}`)
    }
  }
  for (const [index, route] of (manifest.routeAllowlist ?? []).entries()) {
    if (route.owner !== owner) fail(`routeAllowlist[${index}] is ${route.owner}, expected ${owner}`)
  }
  for (const [index, workerJob] of (manifest.workerAllowlist ?? []).entries()) {
    if (workerJob.owner !== owner) fail(`workerAllowlist[${index}] is ${workerJob.owner}, expected ${owner}`)
  }
  return manifest
}

export function assertRequiredRelease(manifest, requirements = {}) {
  validateOwnerManifest(manifest)
  if (requirements.deploymentEpoch !== undefined && manifest.deploymentEpoch !== requirements.deploymentEpoch) {
    fail(`deploymentEpoch is ${manifest.deploymentEpoch}, expected ${requirements.deploymentEpoch}`)
  }
  if (requirements.nodeVersion !== undefined && manifest.release.nodeVersion !== requirements.nodeVersion) {
    fail(`release.nodeVersion is ${manifest.release.nodeVersion}, expected ${requirements.nodeVersion}`)
  }
  if (requirements.schemaVersion !== undefined && manifest.release.schemaVersion !== requirements.schemaVersion) {
    fail(`release.schemaVersion is ${manifest.release.schemaVersion}, expected ${requirements.schemaVersion}`)
  }
  return manifest
}

function requestPathMatchesTemplate(requestPath, templatePath) {
  let requestSegments
  try {
    requestSegments = parseCanonicalPath(requestPath, 'request path', { allowTemplates: false })
  } catch {
    return false
  }
  if (requestSegments === undefined) return false
  const templateSegments = templatePath === '/' ? [] : templatePath.slice(1).split('/')
  if (requestSegments.length !== templateSegments.length) return false
  return templateSegments.every((segment, index) => {
    const requestSegment = requestSegments[index]
    return isParameterSegment(segment)
      || literalMatchesActionTemplate(requestSegment, segment)
      || segment === requestSegment
  })
}

export function resolveRouteOwner(manifest, request) {
  validateOwnerManifest(manifest)
  if (!isRecord(request) || !HTTP_ROUTE_SURFACES.has(request.surface)) {
    fail('route owner resolution requires a management, public, or gateway surface')
  }
  const fallbackOwner = manifest.routeOwners[request.surface]
  if (!ALLOWED_METHODS.has(request.method) || typeof request.path !== 'string') return fallbackOwner
  for (const route of manifest.routeAllowlist ?? []) {
    if (route.surface === request.surface
      && methodMatchesRequest(route.method, request.method)
      && requestPathMatchesTemplate(request.path, route.path)) {
      return route.owner
    }
  }
  return fallbackOwner
}

export function resolveWorkerOwner(manifest, job) {
  validateOwnerManifest(manifest)
  if (typeof job !== 'string' || !WORKER_JOB_PATTERN.test(job)) {
    fail('worker owner resolution requires a lowercase kebab-case job name')
  }
  for (const workerJob of manifest.workerAllowlist ?? []) {
    if (workerJob.job === job) return workerJob.owner
  }
  return manifest.routeOwners.worker
}

export function createRollbackManifest(manifest, deploymentEpoch) {
  validateOwnerManifest(manifest)
  if (manifest.schemaVersion !== 2 && manifest.schemaVersion !== 3) {
    fail('automatic rollback requires schemaVersion 2 or 3')
  }
  requireNonEmptyString(deploymentEpoch, 'rollback deploymentEpoch')
  if (deploymentEpoch === manifest.deploymentEpoch) {
    fail('rollback deploymentEpoch must be different from the active deploymentEpoch')
  }
  const rollback = {
    ...manifest,
    deploymentEpoch,
    routeOwners: { ...manifest.rollbackRouteOwners },
    rollbackRouteOwners: { ...manifest.routeOwners },
    routeAllowlist: manifest.routeAllowlist.map(route => ({
      ...route,
      owner: route.rollbackOwner,
      rollbackOwner: route.owner
    }))
  }
  if (manifest.schemaVersion === 3) {
    rollback.workerAllowlist = manifest.workerAllowlist.map(workerJob => ({
      ...workerJob,
      owner: workerJob.rollbackOwner,
      rollbackOwner: workerJob.owner
    }))
  }
  return validateOwnerManifest(rollback)
}

async function main() {
  const args = process.argv.slice(2)
  let requiredOwners
  let requiredAllRoutesOwner
  let rollbackDeploymentEpoch
  const requiredRelease = {}
  const positional = []
  for (const arg of args) {
    if (arg.startsWith('--require-owners=')) {
      requiredOwners = parseRequiredOwners(arg.slice('--require-owners='.length))
      continue
    }
    if (arg.startsWith('--require-all-routes-owner=')) {
      requiredAllRoutesOwner = arg.slice('--require-all-routes-owner='.length)
      continue
    }
    if (arg.startsWith('--print-rollback=')) {
      rollbackDeploymentEpoch = arg.slice('--print-rollback='.length)
      continue
    }
    if (arg.startsWith('--require-deployment-epoch=')) {
      requiredRelease.deploymentEpoch = arg.slice('--require-deployment-epoch='.length)
      continue
    }
    if (arg.startsWith('--require-node-version=')) {
      requiredRelease.nodeVersion = arg.slice('--require-node-version='.length)
      continue
    }
    if (arg.startsWith('--require-schema-version=')) {
      const value = Number(arg.slice('--require-schema-version='.length))
      if (!Number.isInteger(value) || value < 1) throw new OwnerManifestValidationError('required schema version must be a positive integer')
      requiredRelease.schemaVersion = value
      continue
    }
    if (arg.startsWith('--')) throw new OwnerManifestValidationError(`unknown option: ${arg}`)
    positional.push(arg)
  }
  if (positional.length !== 1) {
    throw new OwnerManifestValidationError('usage: node scripts/validate-owner-manifest.mjs [--require-owners=route=owner,...] [--require-all-routes-owner=node|go] [--print-rollback=deployment-epoch] <manifest.json>')
  }
  const manifest = await readAndValidateOwnerManifest(path.resolve(positional[0]))
  assertRequiredOwners(manifest, requiredOwners)
  if (requiredAllRoutesOwner !== undefined) assertAllRoutesOwnedBy(manifest, requiredAllRoutesOwner)
  assertRequiredRelease(manifest, requiredRelease)
  if (rollbackDeploymentEpoch !== undefined) {
    process.stdout.write(`${JSON.stringify(createRollbackManifest(manifest, rollbackDeploymentEpoch), null, 2)}\n`)
    return
  }
  process.stdout.write('Validated owner manifest: 1 file\n')
}

const isDirectRun = process.argv[1]
  && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch(error => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`)
    process.exitCode = 1
  })
}
