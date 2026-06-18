import { strict as assert } from 'node:assert'
import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, rmSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import net from 'node:net'
import { DatabaseSync } from 'node:sqlite'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const serverEntryMode = process.env.JUHE_AI_WORKER_TOPOLOGY_ENTRY === 'dist' ? 'dist' : 'source'
const tempRoot = mkdtempSync(resolve(tmpdir(), 'juhe-ai-worker-topology-'))
const dataRoot = resolve(tempRoot, 'data')
const logRoot = resolve(tempRoot, 'logs')
const statsDatabasePath = resolve(dataRoot, 'stats.sqlite3')

mkdirSync(dataRoot, { recursive: true })
mkdirSync(logRoot, { recursive: true })

const port = await freePort()
const child = startBackendServer(port)
let childStdout = ''
let childStderr = ''
child.stdout?.on('data', (chunk: Buffer) => {
  childStdout = tailText(childStdout + chunk.toString('utf8'))
})
child.stderr?.on('data', (chunk: Buffer) => {
  childStderr = tailText(childStderr + chunk.toString('utf8'))
})

try {
  await waitForHealth(`http://127.0.0.1:${port}/__aisys__/health`, child)
  await waitForHealth(`http://127.0.0.1:${port}/__aisys__/api/health`, child)

  const { workerChildren, dbServiceChildren } = await waitForChildProcessTopology(child, 7, 1)
  assert.equal(workerChildren.length, 7, `server 必须拉起默认 worker、metrics-worker、ingest-worker、stats-worker、snapshot-worker、probe-worker 和 maintenance-worker 七个子进程，实际 worker 子进程数：${workerChildren.length}`)
  assert.equal(dbServiceChildren.length, 1, `server 必须拉起一个 db-service 子进程，实际 db-service 子进程数：${dbServiceChildren.length}`)

  const rows = await waitForProcessEventLoopRoles(statsDatabasePath, ['server', 'worker', 'ingest-worker', 'stats-worker', 'snapshot-worker', 'probe-worker', 'maintenance-worker', 'db-service'])
  console.log(`后台 worker 拓扑 smoke 通过：entry=${serverEntryMode} serverPid=${child.pid} workerChildren=${workerChildren.length} dbServiceChildren=${dbServiceChildren.length} roles=${rows.map((row) => `${row.processRole}:${row.count}`).join(',')}`)
} finally {
  await stopProcessTree(child)
  removeTempRoot(tempRoot)
}

function startBackendServer(port: number): ChildProcess {
  const args = serverEntryMode === 'dist'
    ? ['dist/server.js']
    : ['--import', 'tsx', 'src/server.ts']
  return spawn(process.execPath, args, {
    cwd: backendRoot,
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_HOST: '127.0.0.1',
      JUHE_AI_PORT: String(port),
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
      JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_DATABASE_PATH: resolve(dataRoot, 'business.sqlite3'),
      JUHE_AI_DATASET_DATABASE_PATH: resolve(dataRoot, 'dataset.sqlite3'),
      JUHE_AI_STATS_DATABASE_PATH: statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: resolve(dataRoot, 'usage-shards'),
      JUHE_AI_SECRET: 'juhe-ai-worker-topology-smoke-secret',
      JUHE_AI_LOG_LEVEL: 'warn',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    stdio: ['ignore', 'pipe', 'pipe']
  })
}

async function waitForChildProcessTopology(
  child: ChildProcess,
  expectedWorkerCount: number,
  expectedDbServiceCount: number
): Promise<{
  workerChildren: Array<{ processId: number; parentProcessId: number; commandLine: string }>
  dbServiceChildren: Array<{ processId: number; parentProcessId: number; commandLine: string }>
}> {
  const startedAt = Date.now()
  let lastWorkerChildren: Array<{ processId: number; parentProcessId: number; commandLine: string }> = []
  let lastDbServiceChildren: Array<{ processId: number; parentProcessId: number; commandLine: string }> = []
  while (Date.now() - startedAt < 30_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出，exitCode=${child.exitCode}\nstdout=${childStdout}\nstderr=${childStderr}`)
    }
    const children = listChildProcesses(child.pid)
    lastWorkerChildren = children.filter((processInfo) => /(?:^|\b)worker\.(?:js|ts)\b/i.test(processInfo.commandLine))
    lastDbServiceChildren = children.filter((processInfo) => /(?:^|\b)db-service\.(?:js|ts)\b/i.test(processInfo.commandLine))
    if (lastWorkerChildren.length === expectedWorkerCount && lastDbServiceChildren.length === expectedDbServiceCount) {
      return {
        workerChildren: lastWorkerChildren,
        dbServiceChildren: lastDbServiceChildren
      }
    }
    await sleep(250)
  }
  return {
    workerChildren: lastWorkerChildren,
    dbServiceChildren: lastDbServiceChildren
  }
}

async function waitForHealth(url: string, child: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  let lastError: unknown
  while (Date.now() - startedAt < 20_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出，exitCode=${child.exitCode}\nstdout=${childStdout}\nstderr=${childStderr}`)
    }
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(1000) })
      if (response.ok) return
      lastError = new Error(`${url} 返回 ${response.status}`)
    } catch (error) {
      lastError = error
    }
    await sleep(250)
  }
  throw new Error(`临时后端健康检查等待超时：${lastError instanceof Error ? lastError.message : String(lastError)}\nstdout=${childStdout}\nstderr=${childStderr}`)
}

async function waitForProcessEventLoopRoles(databasePath: string, expectedRoles: string[]): Promise<Array<{ processRole: string; count: number }>> {
  const startedAt = Date.now()
  let lastRoles: string[] = []
  while (Date.now() - startedAt < 30_000) {
    if (existsSync(databasePath)) {
      try {
        const database = new DatabaseSync(databasePath, { readOnly: true })
        const rows = database.prepare(`
          SELECT process_role AS processRole, COUNT(*) AS count
          FROM process_event_loop_samples
          GROUP BY process_role
          ORDER BY process_role
        `).all() as Array<{ processRole: string; count: number }>
        database.close()
        lastRoles = rows.map((row) => String(row.processRole))
        if (expectedRoles.every((role) => lastRoles.includes(role))) {
          return rows
        }
      } catch {
      }
    }
    await sleep(500)
  }
  throw new Error(`等待进程事件循环采样角色超时，最后角色：${lastRoles.join(',') || 'none'}\nstdout=${childStdout}\nstderr=${childStderr}`)
}

function listChildProcesses(parentPid: number | undefined): Array<{ processId: number; parentProcessId: number; commandLine: string }> {
  if (!parentPid) return []
  return process.platform === 'win32'
    ? listWindowsChildProcesses(parentPid)
    : listPosixChildProcesses(parentPid)
}

function listWindowsChildProcesses(parentPid: number): Array<{ processId: number; parentProcessId: number; commandLine: string }> {
  const command = `Get-CimInstance Win32_Process -Filter "ParentProcessId = ${parentPid}" | Select-Object ProcessId,ParentProcessId,CommandLine | ConvertTo-Json -Compress`
  const output = execFileSync('powershell', ['-NoProfile', '-Command', command], { encoding: 'utf8' }).trim()
  if (!output) return []
  const parsed = JSON.parse(output) as unknown
  const items = Array.isArray(parsed) ? parsed : [parsed]
  return items.map((item) => {
    const record = item as Record<string, unknown>
    return {
      processId: Number(record.ProcessId),
      parentProcessId: Number(record.ParentProcessId),
      commandLine: typeof record.CommandLine === 'string' ? record.CommandLine : ''
    }
  })
}

function listPosixChildProcesses(parentPid: number): Array<{ processId: number; parentProcessId: number; commandLine: string }> {
  const output = execFileSync('ps', ['-eo', 'pid=,ppid=,command='], { encoding: 'utf8' })
  const rows: Array<{ processId: number; parentProcessId: number; commandLine: string }> = []
  for (const line of output.split('\n')) {
    const match = line.match(/^\s*(\d+)\s+(\d+)\s+(.*)$/)
    if (!match) continue
    const processId = Number(match[1])
    const parentProcessId = Number(match[2])
    if (parentProcessId !== parentPid) continue
    rows.push({ processId, parentProcessId, commandLine: match[3] })
  }
  return rows
}

async function stopProcessTree(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) return
  if (process.platform === 'win32' && child.pid) {
    await new Promise<void>((resolvePromise) => {
      const killer = spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore' })
      killer.once('error', () => resolvePromise())
      killer.once('exit', () => resolvePromise())
    })
  } else {
    child.kill('SIGTERM')
  }
  await new Promise<void>((resolvePromise) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      resolvePromise()
    }, 3000)
    child.once('exit', () => {
      clearTimeout(timeout)
      resolvePromise()
    })
  })
}

function removeTempRoot(path: string): void {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch {
    }
  }
}

function freePort(): Promise<number> {
  return new Promise((resolvePromise, rejectPromise) => {
    const server = net.createServer()
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      const port = typeof address === 'object' && address ? address.port : undefined
      server.close(() => {
        if (typeof port === 'number') {
          resolvePromise(port)
        } else {
          rejectPromise(new Error('无法分配临时端口'))
        }
      })
    })
  })
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

function tailText(text: string): string {
  return text.length > 12_000 ? text.slice(text.length - 12_000) : text
}
