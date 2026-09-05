import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const backendRoot = resolve(import.meta.dirname, '../../..')
const inputDirectory = mkdtempSync(join(tmpdir(), 'juhe-ai-j1-owner-handoff-'))

try {
  const result = spawnSync(process.execPath, [
    '--import',
    'tsx',
    '--input-type=module',
    '-e',
    `
      import { startBackgroundJobs, getBackgroundJobRuntimeSnapshots, stopBackgroundJobs } from './src/modules/background/background-jobs.js'
      startBackgroundJobs()
      const names = getBackgroundJobRuntimeSnapshots().map((item) => item.name).sort()
      const drain = await stopBackgroundJobs()
      process.stdout.write(JSON.stringify({ names, drain }) + '\\n')
    `
  ], {
    cwd: backendRoot,
    encoding: 'utf8',
    env: {
      ...process.env,
      JUHE_AI_DISABLE_BASE_ENV: 'true',
      NODE_ENV: 'test',
      JUHE_AI_RUNTIME_MODE: 'standalone',
      JUHE_AI_DATABASE_DRIVER: 'sqlite',
      JUHE_AI_CACHE_DRIVER: 'memory',
      JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
      JUHE_AI_QUEUE_DRIVER: 'memory',
      JUHE_AI_PROCESS_ROLE: 'worker',
      JUHE_AI_WORKER_ROLE: 'ops-worker',
      JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
      JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: inputDirectory,
      JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: 'j1-owner-handoff-scheduler-signing-key'
    }
  })
  const output = `${result.stdout ?? ''}${result.stderr ?? ''}`
  assert.equal(result.status, 0, output)
  const snapshotLine = output.split(/\r?\n/u).find((line) => line.startsWith('{"names":'))
  assert(snapshotLine, output)
  const snapshot = JSON.parse(snapshotLine) as {
    names: string[]
    drain: { drained: boolean, activeCount: number }
  }

  assert.equal(snapshot.names.includes('account-health-check'), false, 'Go owner 下 Node 不得注册 J1 周期健康检查')
  assert.equal(snapshot.names.includes('cooldown-account-retest'), false, 'Go owner 下 Node 不得注册 J1 账户冷却复测')
  assert.equal(snapshot.names.includes('account-api-key-cooldown-retest'), true, 'API Key runtime cooldown 不是 J1，必须保持 Node owner')
  assert.deepEqual(snapshot.drain, { drained: true, activeCount: 0 }, 'Node scheduler 必须在没有 J1 任务的情况下完整排空')
} finally {
  rmSync(inputDirectory, { recursive: true, force: true })
}

console.log('account-health-jobs-owner-handoff-scheduler-regression passed')
