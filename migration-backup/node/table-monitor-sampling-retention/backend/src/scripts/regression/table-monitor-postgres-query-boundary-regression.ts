import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../storage/table-monitor.repository.ts', import.meta.url), 'utf8')
const catalog = sourceBetween(source, 'async function listPostgresSchemaTableCatalog', 'async function loadPostgresTableSizes')
const exactSizes = sourceBetween(source, 'async function loadPostgresTableSizes', 'async function postgresBlockSize')
const sqliteCollect = sourceBetween(source, 'export function collectTableStorageSnapshot', 'export async function collectTableStorageSnapshotAsync')
const postgresCollect = sourceBetween(source, 'export async function collectTableStorageSnapshotAsync', 'export function getTableStorageOverview')

assert.doesNotMatch(catalog, /pg_(?:relation|indexes|total_relation)_size/i, 'PG catalog 阶段不得在 relation 预算选择前读取全部精确大小')
assert.match(catalog, /c\.relpages[\s\S]*index_pages/, 'PG catalog 阶段应使用 relpages 形成低成本 schema 容量估算')
assert.match(exactSizes, /pg_relation_size\(c\.oid\)/, '选中 relation 后应读取精确 table size')
assert.match(exactSizes, /pg_indexes_size\(c\.oid\)/, '选中 relation 后应读取精确 index size')
assert.match(exactSizes, /pg_total_relation_size\(c\.oid\)/, '选中 relation 后应读取精确 total size')
assert.match(exactSizes, /c\.relname\s*=\s*ANY\(\?::text\[\]\)/, 'PG 精确大小查询必须只接受本轮选中 relation 数组')
assert(
  postgresCollect.indexOf('selectPostgresTableScan') < postgresCollect.indexOf('collectPostgresTargetTableRows'),
  'PG 常驻采样必须先按 cursor/budget 选择 relation，再进入 exact size 阶段'
)
assert.doesNotMatch(sqliteCollect, /cleanupOldTableStorageSnapshots|cleanupTableStorageSnapshotsBefore/, 'SQLite 采样不得混入 retention')
assert.doesNotMatch(postgresCollect, /cleanupOldPostgresTableStorageSnapshots|cleanupTableStorageSnapshotsBeforeAsync/, 'PG 采样不得混入 retention')

console.log('表监控 PostgreSQL 查询边界回归通过：先选 relation 后 exact size，采样不承担 retention')

function sourceBetween(fullSource: string, startMarker: string, endMarker: string): string {
  const start = fullSource.indexOf(startMarker)
  const end = fullSource.indexOf(endMarker, start + startMarker.length)
  assert(start >= 0, `缺少源码起始标记：${startMarker}`)
  assert(end > start, `缺少源码结束标记：${endMarker}`)
  return fullSource.slice(start, end)
}
