import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

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
