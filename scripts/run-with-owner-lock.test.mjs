#!/usr/bin/env node

import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readFile, stat, writeFile, rm } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

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

function cliArgs(lockPath, extra = []) {
  return [
    cliPath,
    '--lock-path', lockPath,
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

test('an existing lock directory is rejected and never removed', async () => {
  const directory = await createTempDirectory()
  const lockPath = path.join(directory, 'stale-owner.lock')
  const markerPath = path.join(lockPath, 'stale-metadata.json')

  try {
    await mkdir(lockPath)
    await writeFile(markerPath, '{"pid":1,"epoch":"stale"}\n', 'utf8')

    const result = await runCli(cliArgs(lockPath, await commandArgs(50))).result
    assert.notEqual(result.code, 0)
    assert.match(`${result.stdout}${result.stderr}`, /directory|regular file|lock|owner/i)
    assert.deepEqual(JSON.parse(await readFile(markerPath, 'utf8')), { pid: 1, epoch: 'stale' })
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

process.stdout.write('run-with-owner-lock regression tests loaded.\n')
