import { readFileSync } from 'node:fs'
import { isAbsolute } from 'node:path'

export type WorkerOwner = 'node' | 'go'

export interface WorkerOwnerLockConfig {
  enabled: boolean
  manifestPath?: string
  deploymentEpoch?: string
}

export interface NodeWorkerOwnershipDecision {
  nodeOwns: boolean
  reason:
    | 'owner_lock_disabled'
    | 'manifest_owner_node'
    | 'manifest_owner_go'
    | 'invalid_config'
    | 'manifest_unreadable'
    | 'manifest_invalid'
    | 'deployment_epoch_mismatch'
  resolvedOwner?: WorkerOwner
  schemaVersion?: 2 | 3
  detail?: string
}

interface OwnerMap {
  management: WorkerOwner
  public: WorkerOwner
  gateway: WorkerOwner
  worker: WorkerOwner
}

interface WorkerJobOwner {
  job: string
  owner: WorkerOwner
  rollbackOwner: WorkerOwner
}

interface WorkerOwnerManifest {
  schemaVersion: 2 | 3
  deploymentEpoch: string
  routeOwners: OwnerMap
  workerAllowlist: WorkerJobOwner[]
}

interface ParsedRoute {
  index: number
  method: string
  segments: string[]
}

const ownerMapFields = ['gateway', 'management', 'public', 'worker']
const releaseFields = ['goVersion', 'nodeVersion', 'schemaVersion']
const v2ManifestFields = [
  'deploymentEpoch',
  'release',
  'rollbackRouteOwners',
  'routeAllowlist',
  'routeOwners',
  'schemaVersion'
]
const v3ManifestFields = [...v2ManifestFields, 'workerAllowlist']
const routeFields = ['method', 'owner', 'path', 'rollbackOwner', 'surface']
const workerJobFields = ['job', 'owner', 'rollbackOwner']
const allowedMethods = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'])
const allowedSurfaces = new Set(['management', 'public', 'gateway'])
const workerJobPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const currentReleaseSchemaVersion = 92

export function runIfNodeOwnsWorkerJob(
  config: WorkerOwnerLockConfig,
  job: string,
  run: () => void
): NodeWorkerOwnershipDecision {
  const decision = resolveNodeWorkerOwnership(config, job)
  if (decision.nodeOwns) run()
  return decision
}

export function resolveNodeWorkerOwnership(
  config: WorkerOwnerLockConfig,
  job: string
): NodeWorkerOwnershipDecision {
  if (!config.enabled) return { nodeOwns: true, reason: 'owner_lock_disabled', resolvedOwner: 'node' }
  if (!workerJobPattern.test(job)) {
    return failClosed('invalid_config', 'job must be a lowercase kebab-case name')
  }
  if (!isNonEmptyString(config.manifestPath) || !isAbsolute(config.manifestPath)) {
    return failClosed('invalid_config', 'owner manifest path must be absolute')
  }
  if (!isNonEmptyString(config.deploymentEpoch)) {
    return failClosed('invalid_config', 'owner lock deployment epoch must be configured')
  }

  let rawManifest: string
  try {
    rawManifest = readFileSync(config.manifestPath, 'utf8')
  } catch {
    return failClosed('manifest_unreadable', 'owner manifest could not be read')
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(rawManifest)
  } catch {
    return failClosed('manifest_invalid', 'owner manifest must contain valid JSON')
  }

  let manifest: WorkerOwnerManifest
  try {
    manifest = validateManifest(parsed)
  } catch (error) {
    return failClosed('manifest_invalid', error instanceof Error ? error.message : 'owner manifest is invalid')
  }
  if (manifest.deploymentEpoch !== config.deploymentEpoch) {
    return {
      ...failClosed('deployment_epoch_mismatch', 'owner manifest deployment epoch does not match the process configuration'),
      schemaVersion: manifest.schemaVersion
    }
  }

  const resolvedOwner = manifest.workerAllowlist.find(entry => entry.job === job)?.owner
    ?? manifest.routeOwners.worker
  return {
    nodeOwns: resolvedOwner === 'node',
    reason: resolvedOwner === 'node' ? 'manifest_owner_node' : 'manifest_owner_go',
    resolvedOwner,
    schemaVersion: manifest.schemaVersion
  }
}

function validateManifest(value: unknown): WorkerOwnerManifest {
  requireRecord(value, 'manifest')
  if (value.schemaVersion !== 2 && value.schemaVersion !== 3) {
    throw new Error('schemaVersion must equal 2 or 3')
  }
  requireExactFields(value, value.schemaVersion === 2 ? v2ManifestFields : v3ManifestFields, 'manifest')
  requireNonEmptyString(value.deploymentEpoch, 'deploymentEpoch')
  validateRelease(value.release)
  const routeOwners = validateOwnerMap(value.routeOwners, 'routeOwners')
  validateOwnerMap(value.rollbackRouteOwners, 'rollbackRouteOwners')
  validateRouteAllowlist(value.routeAllowlist)
  const workerAllowlist = value.schemaVersion === 3
    ? validateWorkerAllowlist(value.workerAllowlist)
    : []
  return {
    schemaVersion: value.schemaVersion,
    deploymentEpoch: value.deploymentEpoch,
    routeOwners,
    workerAllowlist
  }
}

function validateRelease(value: unknown): void {
  requireRecord(value, 'release')
  requireExactFields(value, releaseFields, 'release')
  requireNonEmptyString(value.nodeVersion, 'release.nodeVersion')
  requireNonEmptyString(value.goVersion, 'release.goVersion')
  if (value.schemaVersion !== currentReleaseSchemaVersion) {
    throw new Error(`release.schemaVersion must equal ${currentReleaseSchemaVersion}`)
  }
}

function validateOwnerMap(value: unknown, label: string): OwnerMap {
  requireRecord(value, label)
  requireExactFields(value, ownerMapFields, label)
  for (const field of ownerMapFields) requireOwner(value[field], `${label}.${field}`)
  return value as unknown as OwnerMap
}

function validateRouteAllowlist(value: unknown): void {
  if (!Array.isArray(value) || value.length > 2048) {
    throw new Error('routeAllowlist must be an array with at most 2048 entries')
  }
  const parsedRoutes = value.map((route, index): ParsedRoute => {
    const label = `routeAllowlist[${index}]`
    requireRecord(route, label)
    requireExactFields(route, routeFields, label)
    if (!allowedSurfaces.has(route.surface as string)) throw new Error(`${label}.surface is invalid`)
    if (!allowedMethods.has(route.method as string)) throw new Error(`${label}.method is invalid`)
    const owner = requireOwner(route.owner, `${label}.owner`)
    const rollbackOwner = requireOwner(route.rollbackOwner, `${label}.rollbackOwner`)
    if (owner === rollbackOwner) throw new Error(`${label}.owner and rollbackOwner must differ`)
    const segments = parseCanonicalPath(route.path, `${label}.path`)
    validateSurfacePath(route.surface as string, route.path as string, `${label}.path`)
    return { index, method: route.method as string, segments }
  })
  for (let leftIndex = 0; leftIndex < parsedRoutes.length; leftIndex += 1) {
    const left = parsedRoutes[leftIndex]
    for (let rightIndex = leftIndex + 1; rightIndex < parsedRoutes.length; rightIndex += 1) {
      const right = parsedRoutes[rightIndex]
      if (left.method === right.method && pathTemplatesOverlap(left.segments, right.segments)) {
        throw new Error(`routeAllowlist[${left.index}] overlaps routeAllowlist[${right.index}]`)
      }
    }
  }
}

function parseCanonicalPath(value: unknown, label: string): string[] {
  requireNonEmptyString(value, label)
  if (!value.startsWith('/')) throw new Error(`${label} must be an absolute path`)
  if (value.includes('?') || value.includes('#') || value.includes('\\')) {
    throw new Error(`${label} must not contain a query, fragment, or backslash`)
  }
  if (value.includes('%')) throw new Error(`${label} must not contain encoded path bytes`)
  if (value.includes('*')) throw new Error(`${label} must not contain unsafe wildcards`)
  if (value !== '/' && (value.endsWith('/') || value.includes('//'))) {
    throw new Error(`${label} must not contain empty or trailing segments`)
  }
  const segments = value === '/' ? [] : value.slice(1).split('/')
  const parameterNames = new Set<string>()
  for (const segment of segments) {
    if (segment === '.' || segment === '..') throw new Error(`${label} must not contain dot segments`)
    if (segment.startsWith(':')) throw new Error(`${label} must not contain unsafe wildcards`)
    const parameterName = segmentParameterName(segment)
    if (parameterName !== undefined) {
      if (parameterNames.has(parameterName)) throw new Error(`${label} must use unique parameter names`)
      parameterNames.add(parameterName)
      continue
    }
    if (segment.includes('{') || segment.includes('}')) {
      throw new Error(`${label} template parameters must occupy a complete segment`)
    }
    if (!/^[A-Za-z0-9._~:-]+$/.test(segment)) throw new Error(`${label} contains unsupported path characters`)
  }
  return segments
}

function validateSurfacePath(surface: string, path: string, label: string): void {
  if (surface === 'management') {
    if (path !== '/__aisys__/api' && !path.startsWith('/__aisys__/api/')) {
      throw new Error(`${label} is outside the management surface`)
    }
    return
  }
  if (surface === 'public') {
    if (path !== '/__aipublic__' && !path.startsWith('/__aipublic__/')) {
      throw new Error(`${label} is outside the public surface`)
    }
    return
  }
  if (path === '/__aisys__' || path.startsWith('/__aisys__/')
    || path === '/__aipublic__' || path.startsWith('/__aipublic__/')) {
    throw new Error(`${label} uses a reserved surface prefix`)
  }
  const firstSegment = path === '/' ? undefined : path.slice(1).split('/', 1)[0]
  if (firstSegment !== undefined && isParameterSegment(firstSegment)) {
    throw new Error(`${label} gateway first segment must be literal`)
  }
}

function pathTemplatesOverlap(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((segment, index) => segmentTemplatesOverlap(segment, right[index]))
}

function segmentTemplatesOverlap(left: string, right: string): boolean {
  if (isParameterSegment(left) || isParameterSegment(right)) return true
  const leftSuffix = actionSuffix(left)
  const rightSuffix = actionSuffix(right)
  if (leftSuffix !== undefined && rightSuffix !== undefined) return leftSuffix === rightSuffix
  if (leftSuffix !== undefined) return literalMatchesActionTemplate(right, leftSuffix)
  if (rightSuffix !== undefined) return literalMatchesActionTemplate(left, rightSuffix)
  return left === right
}

function literalMatchesActionTemplate(literal: string, suffix: string): boolean {
  return literal.length > suffix.length && literal.endsWith(suffix)
}

function actionSuffix(segment: string): string | undefined {
  return /^\{[A-Za-z][A-Za-z0-9_]*\}(:[A-Za-z][A-Za-z0-9._~-]*)$/.exec(segment)?.[1]
}

function segmentParameterName(segment: string): string | undefined {
  const parameter = /^\{([A-Za-z][A-Za-z0-9_]*)\}$/.exec(segment)?.[1]
  if (parameter !== undefined) return parameter
  return /^\{([A-Za-z][A-Za-z0-9_]*)\}:[A-Za-z][A-Za-z0-9._~-]*$/.exec(segment)?.[1]
}

function isParameterSegment(segment: string): boolean {
  return /^\{[A-Za-z][A-Za-z0-9_]*\}$/.test(segment)
}

function validateWorkerAllowlist(value: unknown): WorkerJobOwner[] {
  if (!Array.isArray(value) || value.length > 256) {
    throw new Error('workerAllowlist must be an array with at most 256 entries')
  }
  const seenJobs = new Set<string>()
  return value.map((entry, index) => {
    const label = `workerAllowlist[${index}]`
    requireRecord(entry, label)
    requireExactFields(entry, workerJobFields, label)
    if (typeof entry.job !== 'string' || !workerJobPattern.test(entry.job)) {
      throw new Error(`${label}.job must be a lowercase kebab-case name`)
    }
    if (seenJobs.has(entry.job)) throw new Error(`workerAllowlist contains duplicate job ${entry.job}`)
    seenJobs.add(entry.job)
    const owner = requireOwner(entry.owner, `${label}.owner`)
    const rollbackOwner = requireOwner(entry.rollbackOwner, `${label}.rollbackOwner`)
    if (owner === rollbackOwner) throw new Error(`${label}.owner and rollbackOwner must differ`)
    return { job: entry.job, owner, rollbackOwner }
  })
}

function requireOwner(value: unknown, label: string): WorkerOwner {
  if (value !== 'node' && value !== 'go') throw new Error(`${label} must be node or go`)
  return value
}

function requireRecord(value: unknown, label: string): asserts value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new Error(`${label} must be an object`)
}

function requireExactFields(value: Record<string, unknown>, fields: string[], label: string): void {
  const actual = Object.keys(value).sort()
  const expected = [...fields].sort()
  if (actual.length !== expected.length || actual.some((field, index) => field !== expected[index])) {
    throw new Error(`${label} contains unexpected or missing fields`)
  }
}

function requireNonEmptyString(value: unknown, label: string): asserts value is string {
  if (!isNonEmptyString(value)) throw new Error(`${label} must be a non-empty string`)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function failClosed(
  reason: Extract<NodeWorkerOwnershipDecision['reason'], 'invalid_config' | 'manifest_unreadable' | 'manifest_invalid' | 'deployment_epoch_mismatch'>,
  detail: string
): NodeWorkerOwnershipDecision {
  return { nodeOwns: false, reason, detail }
}
