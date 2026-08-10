import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function backendSource(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

function rootSource(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../../../${relativePath}`, import.meta.url)), 'utf8')
}

const goHotSearch = rootSource('backend-go/internal/auditlog/hot_search.go')
const goRetention = rootSource('backend-go/internal/auditlog/retention.go')
const inputServer = rootSource('backend-go/internal/auditlog/input_server.go')
const routes = backendSource('modules/audit-logs/audit-logs.routes.ts')

for (const retiredNodeFile of [
  'modules/audit-logs/audit-log-queue.service.ts',
  'modules/audit-logs/audit-log-transport.service.ts',
  'storage/audit-log-hot-search-files.ts',
  'storage/audit-log-retention.repository.ts'
]) {
  const path = fileURLToPath(new URL(`../../${retiredNodeFile}`, import.meta.url))
  assert.equal(existsSync(path), false, `F3 active Node 路径不得保留旧热搜索/保留 owner：${retiredNodeFile}`)
}

assert.match(goHotSearch, /func \(s \*sqlStore\) AppendHotSearch/, 'Go owner 必须写入 F3 hot-search')
assert.match(goHotSearch, /func \(s \*sqlStore\) SearchHotSearch/, 'Go owner 必须查询 F3 hot-search')
assert.match(goRetention, /cleanupHotSearchFilesBefore/, 'Go retention 必须清理过期 F3 hot-search bucket')
assert.match(inputServer, /store\.AppendHotSearch/, 'Go input owner 仅在持久化后追加 hot-search')
assert.match(routes, /\.searchHot\(/, 'Node 管理端必须通过 F3 read-only adapter 查询 hot-search')

console.log('F3 审计热搜索边界回归通过：Go 是写入/保留 owner，Node 仅保留只读管理查询；旧 Node 基线已归档。')
