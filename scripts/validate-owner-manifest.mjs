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

async function main() {
  const args = process.argv.slice(2)
  if (args.length !== 1 || args[0].startsWith('--')) {
    throw new OwnerManifestValidationError('usage: node scripts/validate-owner-manifest.mjs <manifest.json>')
  }
  await readAndValidateOwnerManifest(path.resolve(args[0]))
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
