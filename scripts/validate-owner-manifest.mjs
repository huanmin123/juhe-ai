#!/usr/bin/env node

import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REQUIRED_ROUTE_OWNERS = ['management', 'public', 'gateway', 'worker']
const ALLOWED_OWNERS = new Set(['node', 'go'])

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

export function validateOwnerManifest(manifest) {
  if (!isRecord(manifest)) fail('manifest must be an object')
  if (manifest.schemaVersion !== 1) fail('schemaVersion must equal 1')
  requireNonEmptyString(manifest.deploymentEpoch, 'deploymentEpoch')

  if (!isRecord(manifest.release)) fail('release must be an object')
  requireNonEmptyString(manifest.release.nodeVersion, 'release.nodeVersion')
  requireNonEmptyString(manifest.release.goVersion, 'release.goVersion')
  if (!Number.isInteger(manifest.release.schemaVersion) || manifest.release.schemaVersion < 1) {
    fail('release.schemaVersion must be a positive integer')
  }

  if (!isRecord(manifest.routeOwners)) fail('routeOwners must be an object')
  const actualRoutes = Object.keys(manifest.routeOwners).sort()
  const expectedRoutes = [...REQUIRED_ROUTE_OWNERS].sort()
  if (JSON.stringify(actualRoutes) !== JSON.stringify(expectedRoutes)) {
    fail(`routeOwners must contain exactly ${REQUIRED_ROUTE_OWNERS.join(', ')}`)
  }
  for (const route of REQUIRED_ROUTE_OWNERS) {
    if (!ALLOWED_OWNERS.has(manifest.routeOwners[route])) {
      fail(`routeOwners.${route} must be node or go`)
    }
  }

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
    if (manifest.routeOwners[route] !== owner) {
      fail(`routeOwners.${route} is ${manifest.routeOwners[route]}, expected ${owner}`)
    }
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

async function main() {
  const args = process.argv.slice(2)
  let requiredOwners
  const requiredRelease = {}
  const positional = []
  for (const arg of args) {
    if (arg.startsWith('--require-owners=')) {
      requiredOwners = parseRequiredOwners(arg.slice('--require-owners='.length))
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
    throw new OwnerManifestValidationError('usage: node scripts/validate-owner-manifest.mjs [--require-owners=route=owner,...] <manifest.json>')
  }
  const manifest = await readAndValidateOwnerManifest(path.resolve(positional[0]))
  assertRequiredOwners(manifest, requiredOwners)
  assertRequiredRelease(manifest, requiredRelease)
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
