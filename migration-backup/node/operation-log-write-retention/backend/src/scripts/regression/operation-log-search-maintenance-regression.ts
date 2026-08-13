import { strict as assert } from 'node:assert'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { createSqliteDatabaseClient, postgresDialect, type DatabaseClient } from '../../storage/database-client.js'
import { rebuildOperationLogSearchTerms } from '../../storage/operation-log-search-maintenance.js'

const maintenancePath = resolve('src/scripts/maintenance/rebuild-operation-log-search-terms.ts')
const repositoryPath = resolve('src/storage/operation-log-search-maintenance.ts')

assert(existsSync(maintenancePath), '发布包必须包含操作日志摘要搜索词离线重建入口')
assert(existsSync(repositoryPath), '操作日志摘要搜索词离线重建应由独立存储模块承接')

const maintenanceSource = readFileSync(maintenancePath, 'utf8')
const repositorySource = readFileSync(repositoryPath, 'utf8')
assert.match(maintenanceSource, /runtimeConfig\.databaseDriver === 'postgres'/, '维护入口必须支持当前 SQLite 和 PostgreSQL 模式')
assert.match(repositorySource, /created_at[\s\S]*id[\s\S]*(?:LIMIT|limit)/, '离线重建必须按 created_at、id 游标分批读取')
assert.doesNotMatch(repositorySource, /SELECT[\s\S]*FROM[\s\S]*operation_logs(?![\s\S]*LIMIT)/, '离线重建不能无界读取整个 operation_logs 表')
assert.match(repositorySource, /buildOperationLogSearchTerms/, '离线重建必须复用在线写入的搜索词生成规则')

const database = new DatabaseSync(':memory:')
database.exec(`
  CREATE TABLE operation_logs (id TEXT PRIMARY KEY, summary TEXT NOT NULL, created_at TEXT NOT NULL);
  CREATE TABLE operation_log_summary_search_terms (
    operation_log_id TEXT NOT NULL,
    term TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (term, operation_log_id)
  );
  INSERT INTO operation_logs (id, summary, created_at) VALUES
    ('op_2', '更新 B2', '2026-07-13T00:00:00.000Z'),
    ('op_1', '更新 A1', '2026-07-13T00:00:00.000Z'),
    ('op_3', '删除 C3', '2026-07-13T00:00:01.000Z');
  INSERT INTO operation_log_summary_search_terms (operation_log_id, term, created_at)
  VALUES ('op_1', 'stale', '2026-07-13T00:00:00.000Z');
`)

try {
  const client = createSqliteDatabaseClient(database)
  const first = await rebuildOperationLogSearchTerms(client, 2)
  assert.equal(first.logCount, 3, '首次重建应处理全部日志')
  assert.equal(first.batchCount, 2, '批大小为 2 时三条日志应通过两个批次处理')
  assert(first.termCount > 0, '首次重建应写入摘要词项')
  assert.equal(database.prepare(`SELECT COUNT(*) AS count FROM operation_log_summary_search_terms WHERE term = 'stale'`).get()?.count, 0, '重建应删除当前批次旧词项')
  for (const term of ['更', 'a', '1', '删', 'c', '3']) {
    assert(Number(database.prepare('SELECT COUNT(*) AS count FROM operation_log_summary_search_terms WHERE term = ?').get(term)?.count ?? 0) >= 1, `重建后应包含单字符词项 ${term}`)
  }
  const totalTerms = Number(database.prepare('SELECT COUNT(*) AS count FROM operation_log_summary_search_terms').get()?.count ?? 0)
  const second = await rebuildOperationLogSearchTerms(client, 2)
  assert.equal(second.batchCount, 2, '重复执行仍应按游标分为两个批次')
  assert.equal(Number(database.prepare('SELECT COUNT(*) AS count FROM operation_log_summary_search_terms').get()?.count ?? 0), totalTerms, '重复执行不应产生重复词项')
} finally {
  database.close()
}

let postgresReadCount = 0
const postgresSql: string[] = []
const postgresClient: DatabaseClient = {
  driver: 'postgres',
  dialect: postgresDialect,
  async query<T extends object>(): Promise<T[]> {
    postgresReadCount += 1
    return postgresReadCount === 1
      ? [{ id: 'op_pg', summary: '更新 PG 9', created_at: '2026-07-13T00:00:00.000Z' } as T]
      : []
  },
  async one<T extends object>(): Promise<T | undefined> {
    return undefined
  },
  async execute(sql): Promise<{ changes: number }> {
    postgresSql.push(sql)
    return { changes: sql.includes('INSERT INTO') ? 1 : 0 }
  },
  async transaction<T>(operation: (tx: DatabaseClient) => Promise<T>): Promise<T> {
    return operation(postgresClient)
  }
}
const postgresResult = await rebuildOperationLogSearchTerms(postgresClient, 10)
assert.equal(postgresResult.logCount, 1, 'PostgreSQL 模式应处理游标批次')
assert(postgresSql.some((sql) => sql.includes('juhe_dataset.operation_log_summary_search_terms')), 'PostgreSQL 模式应写入 juhe_dataset schema')
assert(postgresSql.some((sql) => sql.includes('ON CONFLICT(term, operation_log_id) DO NOTHING')), 'PostgreSQL 模式应保持词项写入幂等')

console.log('操作日志摘要搜索词离线重建回归通过：发布入口存在，SQLite/PG 双模式按 created_at、id 游标分批处理')
