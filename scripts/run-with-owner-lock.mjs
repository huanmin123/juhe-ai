#!/usr/bin/env node

import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SIGNAL_EXIT_CODES = new Map([
  ['SIGINT', 130],
  ['SIGTERM', 143],
])

function usage() {
  return 'usage: node scripts/run-with-owner-lock.mjs --lock-path <path> --release-root <path> --deployment-epoch <epoch> --role <role> --version <version> -- <command> [args...]'
}

function requireValue(args, index, option) {
  const value = args[index + 1]
  if (value === undefined || value === '' || value.startsWith('--')) {
    throw new Error(`${option} requires a value\n${usage()}`)
  }
  return value
}

export function parseArguments(argv) {
  const options = {}
  let commandIndex = -1

  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === '--') {
      commandIndex = index
      break
    }

    const option = argv[index]
    if (!['--lock-path', '--release-root', '--deployment-epoch', '--role', '--version'].includes(option)) {
      throw new Error(`unknown option: ${option}\n${usage()}`)
    }
    options[option.slice(2).replaceAll('-', '')] = requireValue(argv, index, option)
    index += 1
  }

  if (commandIndex < 0 || commandIndex === argv.length - 1) {
    throw new Error(`a command is required after --\n${usage()}`)
  }
  for (const option of ['lockpath', 'releaseroot', 'deploymentepoch', 'role', 'version']) {
    if (!options[option]) throw new Error(`--${option.replaceAll(/([A-Z])/g, '-$1').toLowerCase()} is required\n${usage()}`)
  }

  if (!path.isAbsolute(options.lockpath)) {
    throw new Error(`owner lock path must be absolute: ${options.lockpath}`)
  }
  if (!path.isAbsolute(options.releaseroot)) {
    throw new Error(`release root must be absolute: ${options.releaseroot}`)
  }

  const lockPath = path.normalize(options.lockpath)
  const releaseRoot = path.normalize(options.releaseroot)
  const relativeToRelease = path.relative(releaseRoot, lockPath)
  const insideRelease = relativeToRelease === ''
    || (relativeToRelease !== '..' && !relativeToRelease.startsWith(`..${path.sep}`) && !path.isAbsolute(relativeToRelease))
  if (insideRelease) {
    throw new Error(`owner lock path must be outside the release root: ${lockPath}`)
  }

  return {
    lockPath,
    releaseRoot,
    deploymentEpoch: options.deploymentepoch,
    role: options.role,
    version: options.version,
    command: argv[commandIndex + 1],
    commandArgs: argv.slice(commandIndex + 2),
  }
}

function processIsAlive(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 0) return null
  try {
    process.kill(pid, 0)
    return true
  } catch (error) {
    if (error?.code === 'ESRCH') return false
    if (error?.code === 'EPERM') return true
    throw error
  }
}

async function reclaimStaleLock(lockPath) {
  let metadata
  try {
    metadata = JSON.parse(await readFile(path.join(lockPath, 'metadata.json'), 'utf8'))
  } catch (error) {
    throw new Error(`owner lock metadata cannot be verified: ${lockPath}; ${error instanceof Error ? error.message : String(error)}`)
  }

  const ownerAlive = processIsAlive(metadata.pid)
  const childAlive = processIsAlive(metadata.childPid)
  if (ownerAlive === null || childAlive === null || typeof metadata.ownerId !== 'string' || metadata.ownerId === '') {
    throw new Error(`owner lock metadata cannot be verified: ${lockPath}`)
  }
  if (ownerAlive || childAlive) {
    throw new Error(`owner lock is already held: ${lockPath}`)
  }

  const stalePath = `${lockPath}.stale.${randomUUID()}`
  try {
    await rename(lockPath, stalePath)
  } catch (error) {
    if (error?.code === 'ENOENT') return false
    throw new Error(`failed to quarantine stale owner lock: ${lockPath}; ${error instanceof Error ? error.message : String(error)}`)
  }
  await rm(stalePath, { recursive: true, force: true })
  return true
}

async function acquireLockDirectory(lockPath) {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      await mkdir(lockPath)
      return
    } catch (error) {
      if (error?.code !== 'EEXIST') throw error
      const reclaimed = await reclaimStaleLock(lockPath)
      if (!reclaimed) continue
    }
  }
  throw new Error(`owner lock changed while acquiring: ${lockPath}`)
}

async function writeMetadata(lockPath, metadata) {
  const temporaryPath = path.join(lockPath, `.metadata.${process.pid}.${Date.now()}.tmp`)
  const content = `${JSON.stringify(metadata, null, 2)}\n`
  try {
    await writeFile(temporaryPath, content, { encoding: 'utf8', flag: 'wx' })
    await rename(temporaryPath, path.join(lockPath, 'metadata.json'))
  } catch (error) {
    await rm(temporaryPath, { force: true }).catch(() => {})
    throw error
  }
}

function forwardSignal(child, signal) {
  if (child.exitCode !== null || child.signalCode !== null) return
  try {
    child.kill(signal)
  } catch (error) {
    if (error?.code !== 'ESRCH') throw error
  }
}

export async function runWithOwnerLock(config) {
  const lockPath = config.lockPath
  const ownerId = randomUUID()
  await mkdir(path.dirname(lockPath), { recursive: true })
  await acquireLockDirectory(lockPath)

  let cleaned = false
  const cleanup = async () => {
    if (cleaned) return
    cleaned = true
    await rm(lockPath, { recursive: true, force: true })
  }

  try {
    const startedAt = new Date().toISOString()
    await writeMetadata(lockPath, {
      ownerId,
      deploymentEpoch: config.deploymentEpoch,
      role: config.role,
      version: config.version,
      pid: process.pid,
      startedAt,
      command: config.command,
      args: config.commandArgs,
    })

    const child = spawn(config.command, config.commandArgs, { stdio: 'inherit' })
    const childResult = new Promise(resolve => {
      child.once('error', error => resolve({ error }))
      child.once('close', (code, signal) => resolve({ code, signal }))
    })
    await writeMetadata(lockPath, {
      ownerId,
      deploymentEpoch: config.deploymentEpoch,
      role: config.role,
      version: config.version,
      pid: process.pid,
      childPid: child.pid,
      startedAt,
      command: config.command,
      args: config.commandArgs,
    })
    const signalHandlers = new Map()
    for (const signal of ['SIGINT', 'SIGTERM']) {
      const handler = () => forwardSignal(child, signal)
      signalHandlers.set(signal, handler)
      process.on(signal, handler)
    }

    let result
    try {
      result = await childResult
    } finally {
      for (const [signal, handler] of signalHandlers) process.off(signal, handler)
    }

    if (result.error) throw result.error
    await cleanup()
    if (result.signal) return SIGNAL_EXIT_CODES.get(result.signal) ?? 1
    return result.code ?? 1
  } catch (error) {
    await cleanup().catch(cleanupError => {
      throw new Error(`${error instanceof Error ? error.message : String(error)}; failed to release owner lock: ${cleanupError.message}`)
    })
    throw error
  }
}

async function main() {
  const config = parseArguments(process.argv.slice(2))
  process.exitCode = await runWithOwnerLock(config)
}

const isDirectRun = process.argv[1]
  && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch(error => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`)
    process.exitCode = 1
  })
}
