import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  captureWindowsProcessTree,
  assertTrackedProcessIdentitiesStopped,
  extendTrackedProcessTree,
  isTcpPortListening,
  killWindowsProcess,
  listWindowsProcessIdentities,
  mergeTrackedProcesses,
  sameProcessIdentity,
  selectTrackedProcessTree,
  startWindowsProcessTreeTracker,
  stopTrackedWindowsProcessTree,
  waitForChildProcessClose,
  type TrackedProcessIdentity
} from './chat-long-session-process-tree.js'

if (process.platform !== 'win32') {
  console.log('非 Windows 平台跳过长会话 Windows 进程树回归')
} else if (process.argv.includes('--cleanup-harness')) {
  await runCleanupHarness()
} else {
  await runOuterRegression()
}

async function runOuterRegression(): Promise<void> {
  const child = spawn(process.execPath, [
    ...process.execArgv,
    fileURLToPath(import.meta.url),
    '--cleanup-harness'
  ], { shell: false, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] })
  let output = ''
  const collect = (chunk: Buffer): void => { output = `${output}${chunk.toString('utf8')}`.slice(-64 * 1024) }
  child.stdout.on('data', collect)
  child.stderr.on('data', collect)
  const exitCode = await new Promise<number>((resolveExit, rejectExit) => {
    const timeout = setTimeout(() => { child.kill('SIGKILL'); resolveExit(-2) }, 30_000)
    child.once('error', (error) => { clearTimeout(timeout); rejectExit(error) })
    child.once('exit', (code) => { clearTimeout(timeout); resolveExit(code ?? -1) })
  })
  assert.equal(exitCode, 0, `cleanup harness exit=${exitCode}: ${output}`)
  assert.match(output, /HARNESS_SURVIVED_AFTER_CLEANUP/)
  console.log('长会话 Windows 完整进程树停止回归通过')
}

async function runCleanupHarness(): Promise<void> {
  const fakeChild = new EventEmitter() as EventEmitter & { exitCode: number | null; stdout: { destroyed: boolean }; stderr: { destroyed: boolean } }
  fakeChild.exitCode = 0
  fakeChild.stdout = { destroyed: false }
  fakeChild.stderr = { destroyed: false }
  let closeWaitResolved = false
  const closeWait = waitForChildProcessClose(fakeChild, 1_000).then(() => { closeWaitResolved = true })
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 10))
  assert.equal(closeWaitResolved, false, '仅 exit 不足以释放 Windows stdio 句柄，必须继续等待 close')
  fakeChild.stdout.destroyed = true
  fakeChild.stderr.destroyed = true
  fakeChild.emit('close')
  await closeWait

  const rootIdentity = identity(100, 1, '2026-07-16T00:18:37.000Z', 'run server')
  const dbServiceIdentity = identity(101, 100, '2026-07-16T00:18:38.000Z', 'run db-service')
  const initialTracked = extendTrackedProcessTree(100, [], [rootIdentity, dbServiceIdentity])
  const runWriter = identity(102, 101, '2026-07-16T00:23:48.000Z', 'codex-context-state-writer-worker.ts')
  const devService = identity(200, 2, '2026-07-15T18:52:30.000Z', 'dev server')
  const devWriter = identity(201, 200, '2026-07-16T00:23:48.000Z', 'codex-context-state-writer-worker.ts')
  const extendedTracked = extendTrackedProcessTree(100, initialTracked, [dbServiceIdentity, runWriter, devService, devWriter])
  assert.deepEqual(extendedTracked.map((item) => item.pid).sort((left, right) => left - right), [100, 101, 102], '持续快照只能沿已记录 run 祖先吸收晚生/重父化后代，不能按命令或创建时间误收开发 worker')
  const reusedDbServicePid = identity(101, 200, '2026-07-16T01:00:00.000Z', 'reused dev db-service')
  const reusedPidDevWriter = identity(202, 101, '2026-07-16T01:00:01.000Z', 'codex-context-state-writer-worker.ts')
  const afterPidReuse = extendTrackedProcessTree(100, initialTracked, [reusedDbServicePid, reusedPidDevWriter])
  assert.deepEqual(afterPidReuse.map((item) => item.pid).sort((left, right) => left - right), [100, 101], 'tracked 父 PID 被复用后不得把新身份的开发 child 吸入 run')
  let trackerSnapshot = [rootIdentity]
  const tracker = startWindowsProcessTreeTracker({ rootPid: rootIdentity.pid, initial: trackerSnapshot, listProcesses: async () => trackerSnapshot, pollIntervalMs: 5 })
  trackerSnapshot = [rootIdentity, dbServiceIdentity]
  await tracker.sample()
  trackerSnapshot = [rootIdentity, dbServiceIdentity, runWriter, devService, devWriter]
  await tracker.sample()
  const trackerFinal = await tracker.stop()
  assert.deepEqual(trackerFinal.map((item) => item.pid).sort((left, right) => left - right), [100, 101, 102], '定时 tracker 必须记录本 run 晚生多级 children，且不能吸收无关开发树')
  let localWorkers = [identity(301, process.pid, 'runner-worker-v1', 'node sqlite-read-worker.ts')]
  const localWorkerStop = assertTrackedProcessIdentitiesStopped(localWorkers, {
    listCurrentProcesses: async () => localWorkers,
    timeoutMs: 1_000,
    pollIntervalMs: 5
  })
  setTimeout(() => { localWorkers = [] }, 10)
  await localWorkerStop

  const port = await freePort()
  const childCode = "require('node:net').createServer().listen(Number(process.argv[1]), '127.0.0.1'); setInterval(() => {}, 1000)"
  const rootCode = [
    "const { spawn } = require('node:child_process')",
    'const child = spawn(process.execPath, [\'-e\', process.argv[1], process.argv[2]], { detached: true, stdio: \'ignore\', windowsHide: true })',
    'child.unref()',
    'process.stdout.write(String(child.pid))'
  ].join('; ')
  const root = spawn(process.execPath, ['-e', rootCode, childCode, String(port)], {
    shell: false,
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe']
  })
  let stdout = ''
  root.stdout.on('data', (chunk: Buffer) => { stdout += chunk.toString('utf8') })
  await new Promise<void>((resolveExit, rejectExit) => {
    root.once('error', rejectExit)
    root.once('exit', (code) => code === 0 ? resolveExit() : rejectExit(new Error(`process_tree_fixture_root_${code}`)))
  })
  const childPid = Number(stdout.trim())
  assert(Number.isInteger(childPid) && childPid > 0, '根进程必须报告后端子 PID')
  let tracked: TrackedProcessIdentity[] = []
  try {
    await waitForPort(port, true, 5_000)
    assert.equal(root.exitCode, 0, '根 shell 必须已经退出')
    tracked = await captureWindowsProcessTree(root.pid!)
    assert(tracked.some((identity) => identity.pid === childPid), '即使根进程退出，也必须通过 ParentProcessId 捕获后端子进程')
    assert(!tracked.some((identity) => identity.pid === process.pid || identity.pid === process.ppid), '快照不得包含 harness 或其父进程')
    await stopTrackedWindowsProcessTree({
      rootPid: root.pid!,
      tracked,
      servicePort: port,
      captureTree: captureWindowsProcessTree,
      taskkillTree: async () => { throw new Error('taskkill 不应对已退出/复用根 PID 执行') },
      listCurrentProcesses: listWindowsProcessIdentities,
      killPid: killWindowsProcess,
      isPortListening: isTcpPortListening,
      timeoutMs: 5_000
    })
    assert.equal(await isTcpPortListening(port), false, '停止完成后服务端口必须关闭')
    assert(!await processIdentityStillExists(childPid, tracked), '停止完成后记录的后端 PID 身份必须消失')
  } finally {
    const expected = tracked.find((identity) => identity.pid === childPid)
    const current = (await listWindowsProcessIdentities()).find((identity) => identity.pid === childPid)
    if (expected && current && sameProcessIdentity(expected, current)) await killWindowsProcess(childPid)
  }
  await runLiveShellTaskkillCase()
  await runRunnerLocalReadWorkerCleanupCase()
  console.log('HARNESS_SURVIVED_AFTER_CLEANUP')
}

async function runRunnerLocalReadWorkerCleanupCase(): Promise<void> {
  const root = mkdtempSync(join(tmpdir(), 'juhe-chat-runner-pool-'))
  Object.assign(process.env, {
    JUHE_AI_DISABLE_BASE_ENV: 'true',
    JUHE_AI_RUNTIME_MODE: 'standalone',
    JUHE_AI_PROCESS_ROLE: 'db-service',
    JUHE_AI_WORKER_ROLE: 'worker',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_CACHE_DRIVER: 'memory',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
    JUHE_AI_QUEUE_DRIVER: 'memory',
    JUHE_AI_DATABASE_PATH: join(root, 'business.sqlite3'),
    JUHE_AI_CHAT_DATABASE_PATH: join(root, 'chat.sqlite3'),
    JUHE_AI_DATASET_DATABASE_PATH: join(root, 'dataset.sqlite3'),
    JUHE_AI_USAGE_CATALOG_DATABASE_PATH: join(root, 'usage-catalog.sqlite3'),
    JUHE_AI_STATS_DATABASE_PATH: join(root, 'stats.sqlite3'),
    JUHE_AI_USAGE_SHARD_ROOT: join(root, 'usage-shards'),
    JUHE_AI_CODEX_CONTEXT_ROOT: join(root, 'codex-context'),
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(root, 'codex-context', 'state-shards'),
    JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE: '1',
    JUHE_AI_CODEX_CONTEXT_STATE_WRITER_POOL_ENABLED: 'false',
    JUHE_AI_USAGE_RECORD_WRITER_POOL_ENABLED: 'false',
    JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
    JUHE_AI_LOG_FILE_ENABLED: 'false',
    JUHE_AI_SECRET: 'runner-pool-cleanup-regression'
  })
  const databaseModule = await import('../../storage/database.js')
  const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
  try {
    databaseModule.getBusinessDatabase()
    await readWorkerPool.requestSqliteReadWorker({ type: 'list_providers_read_only' })
    const before = selectTrackedProcessTree(process.pid, await listWindowsProcessIdentities())
      .filter((identity) => identity.pid !== process.pid && identity.commandLine.toLowerCase().includes('sqlite-read-worker'))
    assert.equal(before.length, 1, 'runner-local read path 必须真实懒启动 1 个 sqlite read worker')
    await readWorkerPool.closeSqliteReadWorkerPool()
    await assertTrackedProcessIdentitiesStopped(before)
    databaseModule.closeStorageDatabases()
    rmSync(root, { recursive: true, force: true })
  } finally {
    await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
    databaseModule.closeStorageDatabases()
    rmSync(root, { recursive: true, force: true })
  }
}

function identity(pid: number, parentPid: number, creationTime: string, commandLine: string): TrackedProcessIdentity {
  return { pid, parentPid, creationTime, commandLine }
}

async function runLiveShellTaskkillCase(): Promise<void> {
  const port = await freePort()
  const listenerPath = fileURLToPath(new URL('./chat-long-session-listener-child.cjs', import.meta.url))
  const root = spawn(process.execPath, [listenerPath, String(port)], {
    shell: true,
    windowsHide: true,
    stdio: 'ignore'
  })
  assert(root.pid)
  let tracked = await captureWindowsProcessTree(root.pid)
  try {
    await waitForPort(port, true, 5_000)
    tracked = mergeTrackedProcesses(tracked, await captureWindowsProcessTree(root.pid))
    assert(!tracked.some((identity) => identity.pid === process.pid || identity.pid === process.ppid), 'live shell 快照不得包含 harness 或其父进程')
    const expectedRoot = tracked.find((identity) => identity.pid === root.pid)
    const currentRoot = (await listWindowsProcessIdentities()).find((identity) => identity.pid === root.pid)
    assert(expectedRoot && currentRoot && sameProcessIdentity(expectedRoot, currentRoot), 'taskkill 前必须确认 live shell root identity 仍匹配')
    await stopTrackedWindowsProcessTree({
      rootPid: root.pid,
      tracked,
      servicePort: port,
      captureTree: captureWindowsProcessTree,
      taskkillTree: runTaskkillTree,
      listCurrentProcesses: listWindowsProcessIdentities,
      killPid: killWindowsProcess,
      isPortListening: isTcpPortListening,
      timeoutMs: 5_000
    })
    assert.equal(await isTcpPortListening(port), false, 'taskkill 场景停止完成后服务端口必须关闭')
  } finally {
    const currentByPid = new Map((await listWindowsProcessIdentities()).map((identity) => [identity.pid, identity]))
    for (const expected of [...tracked].reverse()) {
      const current = currentByPid.get(expected.pid)
      if (current && sameProcessIdentity(expected, current)) await killWindowsProcess(expected.pid)
    }
  }
}

function runTaskkillTree(rootPid: number): Promise<number> {
  return new Promise<number>((resolveKill, rejectKill) => {
    const killer = spawn('taskkill', ['/pid', String(rootPid), '/t', '/f'], { stdio: 'ignore', windowsHide: true })
    killer.once('error', rejectKill)
    killer.once('exit', (code) => resolveKill(code ?? -1))
  })
}

async function processIdentityStillExists(pid: number, tracked: readonly TrackedProcessIdentity[]): Promise<boolean> {
  const expected = tracked.find((identity) => identity.pid === pid)
  if (!expected) return false
  const current = (await listWindowsProcessIdentities()).find((identity) => identity.pid === pid)
  return Boolean(current && sameProcessIdentity(expected, current))
}

async function waitForPort(port: number, expected: boolean, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await isTcpPortListening(port) === expected) return
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, 25))
  }
  throw new Error(`process_tree_fixture_port_${expected ? 'not_open' : 'not_closed'}`)
}

function freePort(): Promise<number> {
  return new Promise<number>((resolvePort, rejectPort) => {
    const server = net.createServer()
    server.once('error', rejectPort)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') { rejectPort(new Error('process_tree_fixture_port_missing')); return }
      server.close((error) => error ? rejectPort(error) : resolvePort(address.port))
    })
  })
}
