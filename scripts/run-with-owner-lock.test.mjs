#!/usr/bin/env node

import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readFile, stat, symlink, writeFile, rm } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { runWithOwnerLock } from './run-with-owner-lock.mjs'

const cliPath = fileURLToPath(new URL('./run-with-owner-lock.mjs', import.meta.url))
const projectRoot = fileURLToPath(new URL('..', import.meta.url))

function resolveNodeExecutable() {
  if (existsSync(process.execPath)) return process.execPath
  const lookup = spawnSync('where.exe', ['node'], { encoding: 'utf8' })
  const candidate = lookup.stdout
    .split(/\r?\n/)
    .map(value => value.trim())
    .find(value => value && existsSync(value))
  return candidate ?? 'node'
}

const nodeExecutable = resolveNodeExecutable()

function cliArgs(lockPath, extra = [], releaseRoot = projectRoot) {
  return [
    cliPath,
    '--lock-path', lockPath,
    '--release-root', releaseRoot,
    '--deployment-epoch', 'owner-lock-test-epoch',
    '--role', 'management',
    '--version', '0.1.0-test',
    ...extra
  ]
}

function runCli(args, options = {}) {
  const child = spawn(nodeExecutable, args, {
    cwd: projectRoot,
    env: { ...process.env, ...options.env },
    stdio: ['ignore', 'pipe', 'pipe']
  })

  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8')
  child.stderr.setEncoding('utf8')
  child.stdout.on('data', chunk => { stdout += chunk })
  child.stderr.on('data', chunk => { stderr += chunk })

  const result = new Promise(resolve => {
    child.once('error', error => resolve({ child, code: null, signal: null, stdout, stderr, error }))
    child.once('close', (code, signal) => resolve({ child, code, signal, stdout, stderr }))
  })
  return { child, result }
}

async function waitForMetadata(lockPath, timeoutMs = 3000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const metadata = JSON.parse(await readFile(path.join(lockPath, 'metadata.json'), 'utf8'))
      return metadata
    } catch {
      await new Promise(resolve => setTimeout(resolve, 25))
    }
  }
  throw new Error(`owner lock metadata was not created: ${lockPath}`)
}

async function commandArgs(durationMs = 1000) {
  return [
    '--',
    nodeExecutable,
    '-e',
    `setTimeout(() => process.exit(0), ${durationMs})`
  ]
}

async function createTempDirectory() {
  return mkdtemp(path.join(os.tmpdir(), 'juhe-owner-lock-test-'))
}

test('first owner holds the lock, writes metadata, and rejects a second owner', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'owner.lock')
  const first = runCli(cliArgs(lockPath, await commandArgs(1500)))

  try {
    const metadata = await waitForMetadata(lockPath)
    assert.equal(metadata.deploymentEpoch, 'owner-lock-test-epoch')
    assert.equal(metadata.role, 'management')
    assert.equal(metadata.version, '0.1.0-test')
    assert.equal(metadata.pid, first.child.pid)

    const second = runCli(cliArgs(lockPath, await commandArgs(50)))
    const secondResult = await second.result
    assert.notEqual(secondResult.code, 0)
    assert.match(`${secondResult.stdout}${secondResult.stderr}`, /lock|owner|already|held|busy/i)

    first.child.kill('SIGTERM')
    await first.result
  } finally {
    if (!first.child.killed) first.child.kill('SIGTERM')
    await first.result
    await rm(directory, { recursive: true, force: true })
  }
})

test('a normally exited owner releases the path for a subsequent owner', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'owner.lock')

  try {
    const first = runCli(cliArgs(lockPath, await commandArgs(50)))
    const firstResult = await first.result
    assert.equal(firstResult.code, 0, firstResult.stderr)

    const second = runCli(cliArgs(lockPath, await commandArgs(50)))
    const secondResult = await second.result
    assert.equal(secondResult.code, 0, secondResult.stderr)
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('invalid arguments fail closed without creating a lock', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'owner.lock')

  try {
    for (const args of [
      [cliPath],
      [cliPath, '--lock-path', lockPath],
      [cliPath, '--unknown-option'],
      [cliPath, '--lock-path', lockPath, '--epoch', 'e', '--role', 'management', '--version', 'v']
    ]) {
      const result = await runCli(args).result
      assert.notEqual(result.code, 0)
      await assert.rejects(stat(lockPath), error => error?.code === 'ENOENT')
    }
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('a stale lock is reclaimed only when both wrapper and child processes are gone', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'stale-owner.lock')
  const metadataPath = path.join(lockPath, 'metadata.json')

  try {
    await mkdir(lockPath)
    await writeFile(metadataPath, JSON.stringify({
      ownerId: 'stale-owner',
      deploymentEpoch: 'owner-lock-test-epoch',
      role: 'management',
      version: '0.1.0-test',
      pid: 2147483000,
      childPid: 2147483001,
      startedAt: '2026-07-19T00:00:00.000Z'
    }), 'utf8')

    const result = await runCli(cliArgs(lockPath, await commandArgs(50))).result
    assert.equal(result.code, 0, result.stderr)
    await assert.rejects(stat(lockPath), error => error?.code === 'ENOENT')
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('an unverifiable existing lock is rejected and never removed', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'unverifiable-owner.lock')
  const markerPath = path.join(lockPath, 'unrelated.json')

  try {
    await mkdir(lockPath)
    await writeFile(markerPath, '{"keep":true}\n', 'utf8')

    const result = await runCli(cliArgs(lockPath, await commandArgs(50))).result
    assert.notEqual(result.code, 0)
    assert.match(`${result.stdout}${result.stderr}`, /lock|metadata|verify|owner/i)
    assert.deepEqual(JSON.parse(await readFile(markerPath, 'utf8')), { keep: true })
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('a stale wrapper lock is preserved while its recorded child is still alive', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'live-child-owner.lock')
  const metadataPath = path.join(lockPath, 'metadata.json')

  try {
    await mkdir(lockPath)
    await writeFile(metadataPath, JSON.stringify({
      ownerId: 'live-child-owner',
      deploymentEpoch: 'owner-lock-test-epoch',
      role: 'management',
      version: '0.1.0-test',
      pid: 2147483000,
      childPid: process.pid,
      startedAt: '2026-07-19T00:00:00.000Z'
    }), 'utf8')

    const result = await runCli(cliArgs(lockPath, await commandArgs(50))).result
    assert.notEqual(result.code, 0)
    assert.match(`${result.stdout}${result.stderr}`, /already held|owner lock/i)
    assert.equal((await stat(lockPath)).isDirectory(), true)
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('a relative lock path is rejected before starting the command', async () => {
  const result = await runCli(cliArgs('runtime/owner.lock', await commandArgs(50))).result
  assert.notEqual(result.code, 0)
  assert.match(`${result.stdout}${result.stderr}`, /absolute|lock path/i)
})

test('a lock path inside the release root is rejected before starting the command', async () => {
  const lockPath = path.join(projectRoot, 'runtime', 'owner.lock')
  const result = await runCli(cliArgs(lockPath, await commandArgs(50))).result
  assert.notEqual(result.code, 0)
  assert.match(`${result.stdout}${result.stderr}`, /outside|release root|lock path/i)
  await assert.rejects(stat(lockPath), error => error?.code === 'ENOENT')
})

test('a child spawn failure releases the acquired lock', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'spawn-failure.lock')

  try {
    const result = await runCli(cliArgs(lockPath, ['--', path.join(directory, 'missing-command')])).result
    assert.notEqual(result.code, 0)
    await assert.rejects(stat(lockPath), error => error?.code === 'ENOENT')
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('a physical lock path inside a symlinked release root is rejected', async () => {
  const directory = await createTempDirectory()
  const physicalRelease = path.join(directory, 'releases', 'candidate')
  const currentRelease = path.join(directory, 'current')
  const lockPath = path.join(physicalRelease, 'runtime', 'owner.lock')

  try {
    await mkdir(physicalRelease, { recursive: true })
    await symlink(physicalRelease, currentRelease, process.platform === 'win32' ? 'junction' : 'dir')
    const result = await runCli(cliArgs(lockPath, await commandArgs(50), currentRelease)).result
    assert.notEqual(result.code, 0)
    assert.match(`${result.stdout}${result.stderr}`, /outside|release root|lock path/i)
    await assert.rejects(stat(lockPath), error => error?.code === 'ENOENT')
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('a post-spawn metadata failure stops the child before releasing the lock', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'metadata-failure.lock')
  const pidPath = path.join(directory, 'child.pid')
  let metadataWrites = 0

  try {
    await assert.rejects(runWithOwnerLock({
      lockPath,
      deploymentEpoch: 'owner-lock-test-epoch',
      role: 'management',
      version: '0.1.0-test',
      command: nodeExecutable,
      commandArgs: ['-e', `require('node:fs').writeFileSync(${JSON.stringify(pidPath)}, String(process.pid)); setInterval(() => {}, 1000)`],
      writeMetadata: async (targetLockPath, metadata) => {
        metadataWrites += 1
        if (metadataWrites === 2) {
          await new Promise(resolve => setTimeout(resolve, 200))
          throw new Error('injected metadata failure')
        }
        await writeFile(path.join(targetLockPath, 'metadata.json'), JSON.stringify(metadata), 'utf8')
      }
    }), /injected metadata failure/)

    const childPid = Number(await readFile(pidPath, 'utf8'))
    assert.throws(() => process.kill(childPid, 0), error => error?.code === 'ESRCH')
    await assert.rejects(stat(lockPath), error => error?.code === 'ENOENT')
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

process.stdout.write('run-with-owner-lock regression tests loaded.\n')
