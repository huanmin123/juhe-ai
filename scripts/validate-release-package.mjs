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
const REQUIRED_RELEASE_FILES = [
  'start.sh',
  'start.ps1',
  'backend/dist/server.js',
  'frontend/dist/index.html'
]
const REQUIRED_GO_PROJECTS = ['jobs', 'gateway', 'maintenance']

// 部署模式校验分支（G20/X03 双轨准备）：
// - hybrid（默认，与历史行为完全一致）：要求 Node server + 前端 + 三 Go 二进制。
// - go：go-only 发布物不携带 backend/dist（Node server），但三 Go 二进制必填。
// - node：回滚兜底校验，不要求 Go 二进制。
const DEPLOY_MODES = new Set(['hybrid', 'go', 'node'])
const REQUIRED_RELEASE_FILES_BY_MODE = new Map([
  ['hybrid', REQUIRED_RELEASE_FILES],
  ['go', ['start.sh', 'start.ps1', 'frontend/dist/index.html']],
  ['node', ['start.sh', 'start.ps1', 'backend/dist/server.js', 'frontend/dist/index.html']]
])
const REQUIRED_GO_PROJECTS_BY_MODE = new Map([
  ['hybrid', REQUIRED_GO_PROJECTS],
  ['go', REQUIRED_GO_PROJECTS],
  ['node', []]
])

export function resolveDeployMode(value) {
  const mode = value === undefined || value === null || value === '' ? 'hybrid' : value
  if (!DEPLOY_MODES.has(mode)) {
    throw new ReleasePackageValidationError(
      `Unknown deploy mode: ${mode} (expected go, hybrid, node, or omitted for hybrid)`
    )
  }
  return mode
}

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
  const deployMode = resolveDeployMode(options.deployMode)

  for (const inputPath of paths) {
    const absolutePath = path.resolve(inputPath)
    await visitPath(absolutePath, '', linksOnly)
    if (!linksOnly) {
      await validateRequiredReleaseFiles(absolutePath, deployMode)
    }
  }
}

async function validateRequiredReleaseFiles(releaseRoot, deployMode) {
  for (const relativePath of REQUIRED_RELEASE_FILES_BY_MODE.get(deployMode)) {
    const absolutePath = path.join(releaseRoot, ...relativePath.split('/'))
    let stats
    try {
      stats = await lstat(absolutePath)
    } catch {
      fail(relativePath, 'required release file is missing')
    }
    if (!stats.isFile() || stats.isSymbolicLink()) {
      fail(relativePath, 'required release entry must be a regular file')
    }
  }
  for (const project of REQUIRED_GO_PROJECTS_BY_MODE.get(deployMode)) {
    const candidates = [
      `backend-go/juhe-ai-${project}`,
      `backend-go/juhe-ai-${project}.exe`
    ]
    let regularFileFound = false
    for (const relativePath of candidates) {
      try {
        const stats = await lstat(path.join(releaseRoot, ...relativePath.split('/')))
        if (stats.isFile() && !stats.isSymbolicLink()) {
          regularFileFound = true
          break
        }
      } catch {
      }
    }
    if (!regularFileFound) {
      fail(candidates[0], 'required Go project release binary is missing or not a regular file')
    }
  }
}

async function main() {
  const args = process.argv.slice(2)
  let linksOnly = false
  let quiet = false
  let deployMode = 'hybrid'
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

    if (arg.startsWith('--deploy-mode=')) {
      deployMode = resolveDeployMode(arg.slice('--deploy-mode='.length))
      continue
    }

    if (arg.startsWith('--')) {
      throw new ReleasePackageValidationError(`Unknown option: ${arg}`)
    }

    inputPaths.push(arg)
  }

  await validateReleasePackagePaths(inputPaths, { linksOnly, deployMode })

  if (!quiet) {
    const mode = linksOnly ? 'link safety' : 'release content'
    process.stdout.write(`Validated ${mode} (deploy mode: ${deployMode}): ${inputPaths.length} path(s)\n`)
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
