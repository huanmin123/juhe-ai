#!/usr/bin/env node

import { lstat, readdir } from 'node:fs/promises'
import { realpathSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const FORBIDDEN_PATH_SEGMENTS = new Set(['data', 'logs', 'node_modules'])
const FORBIDDEN_PERSISTENCE_SUFFIXES = [
  '.sqlite',
  '.sqlite3',
  '.db',
  '.db3',
  '.dump',
  '.rdb',
  '.aof'
]

export class ReleasePackageValidationError extends Error {
  constructor(message) {
    super(message)
    this.name = 'ReleasePackageValidationError'
  }
}

function fail(relativePath, reason) {
  const displayPath = relativePath || '.'
  throw new ReleasePackageValidationError(
    `Release package validation failed at "${displayPath}": ${reason}`
  )
}

function validateReleasePath(relativePath) {
  if (!relativePath) {
    return
  }

  const pathSegments = relativePath.split('/').filter(Boolean)
  const lowerSegments = pathSegments.map((segment) => segment.toLowerCase())
  const forbiddenSegment = lowerSegments.find((segment) => FORBIDDEN_PATH_SEGMENTS.has(segment))

  if (forbiddenSegment) {
    fail(relativePath, `forbidden path segment "${forbiddenSegment}"`)
  }

  const basename = lowerSegments.at(-1) ?? ''

  if (basename.startsWith('.env') && basename !== '.env.example') {
    fail(relativePath, 'real or environment-specific .env files are forbidden')
  }

  if (basename.endsWith('.log')) {
    fail(relativePath, 'log files are forbidden')
  }

  if (FORBIDDEN_PERSISTENCE_SUFFIXES.some((suffix) => basename.endsWith(suffix))) {
    fail(relativePath, 'database or persistence files are forbidden')
  }

  if (/(?:-wal|-shm|-journal)$/u.test(basename)) {
    fail(relativePath, 'database journal files are forbidden')
  }

  if (/^appendonly\.aof(?:\.|$)/u.test(basename)) {
    fail(relativePath, 'Redis append-only persistence files are forbidden')
  }
}

async function visitPath(absolutePath, relativePath, linksOnly) {
  const stats = await lstat(absolutePath)

  if (stats.isSymbolicLink()) {
    fail(relativePath, 'symbolic links and junctions are forbidden')
  }

  if (!linksOnly) {
    validateReleasePath(relativePath)
  }

  if (stats.isFile()) {
    return
  }

  if (!stats.isDirectory()) {
    fail(relativePath, 'only regular files and directories are allowed')
  }

  const entryNames = await readdir(absolutePath)
  entryNames.sort((left, right) => left.localeCompare(right, 'en'))

  for (const entryName of entryNames) {
    const entryAbsolutePath = path.join(absolutePath, entryName)
    const entryRelativePath = relativePath ? `${relativePath}/${entryName}` : entryName
    await visitPath(entryAbsolutePath, entryRelativePath, linksOnly)
  }
}

export async function validateReleasePackagePaths(paths, options = {}) {
  if (!Array.isArray(paths) || paths.length === 0) {
    throw new ReleasePackageValidationError('At least one path is required.')
  }

  const linksOnly = options.linksOnly === true

  for (const inputPath of paths) {
    const absolutePath = path.resolve(inputPath)
    await visitPath(absolutePath, '', linksOnly)
  }
}

async function main() {
  const args = process.argv.slice(2)
  let linksOnly = false
  let quiet = false
  const inputPaths = []

  for (const arg of args) {
    if (arg === '--links-only') {
      linksOnly = true
      continue
    }

    if (arg === '--quiet') {
      quiet = true
      continue
    }

    if (arg.startsWith('--')) {
      throw new ReleasePackageValidationError(`Unknown option: ${arg}`)
    }

    inputPaths.push(arg)
  }

  await validateReleasePackagePaths(inputPaths, { linksOnly })

  if (!quiet) {
    const mode = linksOnly ? 'link safety' : 'release content'
    process.stdout.write(`Validated ${mode}: ${inputPaths.length} path(s)\n`)
  }
}

function resolveEntryPath(entryPath) {
  try {
    return realpathSync(entryPath)
  } catch {
    return path.resolve(entryPath)
  }
}

const isDirectRun = process.argv[1]
  && resolveEntryPath(process.argv[1]) === resolveEntryPath(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch((error) => {
    const message = error instanceof Error ? error.message : String(error)
    process.stderr.write(`${message}\n`)
    process.exitCode = 1
  })
}
