import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function backendSource(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

function rootSource(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../../../${relativePath}`, import.meta.url)), 'utf8')
}

function assertIncludes(text: string, fragment: string, label: string): void {
  assert(text.includes(fragment), `${label} 缺少源码证据：${fragment}`)
}

const capture = backendSource('modules/gateway/audit/capture.service.ts')
const directInput = backendSource('modules/audit-logs/audit-log-go-input.service.ts')
const routes = backendSource('modules/audit-logs/audit-logs.routes.ts')
const f3ReadAdapter = backendSource('storage/audit-log-f3-query.repository.ts')
const goStore = rootSource('backend-go/internal/auditlog/store.go')
const goInputServer = rootSource('backend-go/internal/auditlog/input_server.go')
const goRetention = rootSource('backend-go/internal/auditlog/retention.go')

// Gateway capture keeps the stable record ID across streaming in_progress and finalization,
// but sends both records directly to Go. Node no longer owns their delivery or persistence.
assert.match(capture, /id:\s*this\.auditLogId[\s\S]{0,500}lifecycleStatus:\s*'finalized'/, '网关终态 capture')
assert.match(capture, /id:\s*this\.auditLogId[\s\S]{0,500}lifecycleStatus:\s*'in_progress'/, '网关流式进行中 capture')
assert((capture.match(/dispatchAuditLogToGo\(/g) ?? []).length >= 2, 'in_progress/finalized 均必须直接投递 Go')
assert.doesNotMatch(capture, /enqueueAuditLog|recordDroppedAuditCapture/, 'capture 不得回接 Node audit queue')
assertIncludes(directInput, "export const auditLogGoInputPath = '/__aiinternal__/v1/audit-captures'", 'Node->Go 输入协议')
assert.doesNotMatch(directInput, /enqueueAuditLog|Redis|retry/i, 'Node->Go 输入不得引入 queue、Redis 或 retry fallback')

// F3 active Node path has no audit writer, queue, transport, retention scheduler or schema owner.
for (const retiredNodeFile of [
  'modules/audit-logs/audit-log-queue.service.ts',
  'modules/audit-logs/audit-log-transport.service.ts',
  'modules/audit-logs/audit-log-transport-worker.ts',
  'modules/audit-logs/audit-log-stream-codec.ts',
  'modules/audit-logs/audit-log-capacity-fallback.ts',
  'storage/audit-logs.repository.ts',
  'storage/audit-log-retention.repository.ts',
  'storage/audit-log-hot-search-files.ts'
]) {
  const path = fileURLToPath(new URL(`../../${retiredNodeFile}`, import.meta.url))
  assert.equal(existsSync(path), false, `F3 活跃 Node 路径不得保留：${retiredNodeFile}`)
}

assert.match(routes, /createAuditLogF3QueryRepository/, '管理路由必须构造 F3 read-only adapter')
assert.doesNotMatch(routes, /repositories\.js|audit-log-queue|audit-log-hot-search-files/, '管理路由不得回读 Node dataset/queue owner')
assert.match(f3ReadAdapter, /PRAGMA query_only = ON/, 'SQLite F3 读取必须 query_only')
assert.match(f3ReadAdapter, /BEGIN READ ONLY/, 'PostgreSQL F3 读取必须显式只读事务')

// Go owns persistence and lifecycle maintenance in both SQLite and PostgreSQL modes.
assert.match(goStore, /func \(s \*sqlStore\) Persist/, 'Go owner 必须持久化审计记录')
assert.match(goInputServer, /func RunInputServer/, 'Go owner 必须提供 loopback input server')
assert.match(goInputServer, /store\.Persist/, '输入确认前必须完成 Go 持久化')
assert.match(goRetention, /func \(s \*sqlStore\) CleanupRetention/, 'Go owner 必须执行 F3 retention')

console.log('F3 审计接管契约通过：Node 仅生产一次性直接输入和只读查询；Go 唯一拥有持久化、热搜索与保留。')
