#!/usr/bin/env node

import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { spawn } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SIGNAL_EXIT_CODES = new Map([
  ['SIGINT', 130],
  ['SIGTERM', 143],
])

function usage() {
  return 'usage: node scripts/run-with-owner-lock.mjs --lock-path <path> --deployment-epoch <epoch> --role <role> --version <version> -- <command> [args...]'
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
    if (!['--lock-path', '--deployment-epoch', '--role', '--version'].includes(option)) {
      throw new Error(`unknown option: ${option}\n${usage()}`)
    }
    options[option.slice(2).replaceAll('-', '')] = requireValue(argv, index, option)
    index += 1
  }

  if (commandIndex < 0 || commandIndex === argv.length - 1) {
    throw new Error(`a command is required after --\n${usage()}`)
  }
  for (const option of ['lockpath', 'deploymentepoch', 'role', 'version']) {
    if (!options[option]) throw new Error(`--${option.replaceAll(/([A-Z])/g, '-$1').toLowerCase()} is required\n${usage()}`)
  }

  return {
    lockPath: path.resolve(options.lockpath),
    deploymentEpoch: options.deploymentepoch,
    role: options.role,
    version: options.version,
    command: argv[commandIndex + 1],
    commandArgs: argv.slice(commandIndex + 2),
  }
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
  await mkdir(path.dirname(lockPath), { recursive: true })
  try {
    await mkdir(lockPath)
  } catch (error) {
    if (error?.code === 'EEXIST') throw new Error(`owner lock is already held: ${lockPath}`)
    throw error
  }

  let cleaned = false
  const cleanup = async () => {
    if (cleaned) return
    cleaned = true
    await rm(lockPath, { recursive: true, force: true })
  }

  try {
    await writeMetadata(lockPath, {
      deploymentEpoch: config.deploymentEpoch,
      role: config.role,
      version: config.version,
      pid: process.pid,
      startedAt: new Date().toISOString(),
      command: config.command,
      args: config.commandArgs,
    })

    const child = spawn(config.command, config.commandArgs, { stdio: 'inherit' })
    const signalHandlers = new Map()
    for (const signal of ['SIGINT', 'SIGTERM']) {
      const handler = () => forwardSignal(child, signal)
      signalHandlers.set(signal, handler)
      process.on(signal, handler)
    }

    let result
    try {
      result = await new Promise((resolve, reject) => {
        child.once('error', reject)
        child.once('close', (code, signal) => resolve({ code, signal }))
      })
    } finally {
      for (const [signal, handler] of signalHandlers) process.off(signal, handler)
    }

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
