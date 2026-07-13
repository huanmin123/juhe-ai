import { strict as assert } from 'node:assert'
import { fork } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const serverSource = readSource('../../server.ts')
const auditQueueSource = readSource('../../modules/audit-logs/audit-log-queue.service.ts')
const dbSupervisorSource = readSource('../../modules/db-service/db-service-supervisor.ts')
const workerSupervisorSource = readSource('../../modules/background/background-worker-supervisor.ts')

assert(serverSource.includes("process.once('SIGTERM', () => void shutdownServer(httpServer, 0))"), 'server 必须集中接管 SIGTERM 优雅退出')
assert(serverSource.includes("process.once('SIGINT', () => void shutdownServer(httpServer, 0))"), 'server 必须集中接管 SIGINT 优雅退出')
assert(!dbSupervisorSource.includes("process.once('SIGTERM'"), 'DB service supervisor 不得抢先 process.exit 绕过审计排空')
assert(!workerSupervisorSource.includes("process.once('SIGTERM'"), 'background worker supervisor 不得抢先 process.exit 绕过审计排空')

assertOrdered(serverSource, [
  'closeHttpServer(httpServer, httpShutdownGraceMs)',
  'waitForGatewayFailureUsageFinalizationsIdle(8_000)',
  'waitForActiveAuditCapturesIdle(8_000)',
  'waitForAuditLogServerDispatchIdle(8_000)',
  'waitForIngestAuditDrain(5_000)',
  'stopAuditLogTransportWorker()',
  'stopBackgroundWorkerSupervisor()',
  'stopDbServiceSupervisor()'
])
assert(auditQueueSource.includes('pendingAuditLogServerDispatches.add(dispatch)'), 'server 审计异步投递必须登记 pending')
assert(auditQueueSource.includes('.finally(() => pendingAuditLogServerDispatches.delete(dispatch))'), 'server 审计异步投递结束后必须清 pending')
assert(auditQueueSource.includes('waitForAuditLogServerDispatchIdle'), 'server 关闭流程必须能有限等待审计投递排空')
assert(serverSource.includes('pendingFailureUsageFinalizationCount'), 'server 关闭未排空告警必须暴露失败 usage pending 数')

await assertImmediateSigtermDrainsFailureUsage()

console.log('服务审计优雅退出回归通过：停止接流量、活动捕获、transport、IPC/worker 与子进程关闭顺序受控')

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8').replace(/\r\n/g, '\n')
}

function assertOrdered(source: string, markers: string[]): void {
  let previousIndex = -1
  for (const marker of markers) {
    const index = source.indexOf(marker)
    assert(index > previousIndex, `优雅退出顺序缺失或错误：${marker}`)
    previousIndex = index
  }
}

async function assertImmediateSigtermDrainsFailureUsage(): Promise<void> {
  const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
  const fixturePath = resolve(backendRoot, 'src/scripts/regression/server-failure-usage-shutdown-fixture.ts')
  const child = fork(fixturePath, [], {
    cwd: backendRoot,
    execArgv: process.execArgv,
    silent: true
  })
  let stdout = ''
  let stderr = ''
  let signaled = false
  child.stdout?.setEncoding('utf8')
  child.stderr?.setEncoding('utf8')
  child.stdout?.on('data', (chunk: string) => {
    stdout += chunk
    if (!signaled && stdout.includes('READY pending=1')) {
      signaled = true
      if (process.platform === 'win32') {
        child.send('simulate-sigterm')
      } else {
        child.kill('SIGTERM')
      }
    }
  })
  child.stderr?.on('data', (chunk: string) => {
    stderr += chunk
  })

  const exit = await new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      rejectPromise(new Error(`立即 SIGTERM 回归超时；stdout=${stdout}; stderr=${stderr}`))
    }, 5_000)
    child.once('error', (error) => {
      clearTimeout(timeout)
      rejectPromise(error)
    })
    child.once('exit', (code, signal) => {
      clearTimeout(timeout)
      resolvePromise({ code, signal })
    })
  })

  assert.equal(signaled, true, `fixture 未进入 pending 状态；stdout=${stdout}; stderr=${stderr}`)
  assert.equal(exit.code, 0, `立即 SIGTERM 后应成功排空失败 usage；signal=${exit.signal}; stdout=${stdout}; stderr=${stderr}`)
  assert.match(stdout, /DRAINED idle=true pending=0/, '立即 SIGTERM 必须等待已登记的失败 usage 收尾完成')
}
