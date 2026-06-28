import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const source = readFileSync(resolve('src/scripts/maintenance/migrate-sqlite-to-postgres.ts'), 'utf8')
const packageJson = readFileSync(resolve('package.json'), 'utf8')

assert.match(packageJson, /"postgres:migrate-sqlite"\s*:\s*"tsx src\/scripts\/maintenance\/migrate-sqlite-to-postgres\.ts"/, 'package.json 必须暴露 SQLite -> PostgreSQL 离线迁移命令')
assert.match(packageJson, /"test:sqlite-to-postgres-migration-script"\s*:\s*"tsx src\/scripts\/regression\/sqlite-to-postgres-migration-script-regression\.ts"/, 'package.json 必须暴露迁移脚本源码门禁')

assert.match(source, /--confirm-offline/, '迁移脚本必须要求 --confirm-offline')
assert.match(source, /JUHE_AI_CONFIRM_SQLITE_TO_POSTGRES_MIGRATION/, '迁移脚本必须支持环境变量显式离线确认')
assert.match(source, /--dry-run/, '迁移脚本必须支持 dry run 预检查')
assert.match(source, /allowNonEmptyTarget/, '迁移脚本默认必须检查目标表是否为空')
assert.match(source, /assertTargetTablesEmpty/, '迁移脚本必须在导入前拒绝非空目标表')
assert.match(source, /applyPostgresSchema/, '迁移脚本必须在正式导入前确认 PostgreSQL schema')

assert.match(source, /new DatabaseSync\(path,\s*\{\s*readOnly:\s*true\s*\}\)/, '迁移脚本必须只读打开源 SQLite 文件')
assert.doesNotMatch(source, /getBusinessDatabase|getDatasetDatabase|getUsageCatalogDatabase|getStatsDatabase/, '迁移脚本不能调用运行时 SQLite getter，避免 postgres 模式回退 SQLite')
assert.match(source, /rowid > \?/, '迁移脚本必须按 rowid 游标分批读取')
assert.match(source, /ORDER BY rowid/, '迁移脚本必须稳定排序读取 SQLite 行')
assert.match(source, /LIMIT \?/, '迁移脚本每批读取必须有限制')
assert.doesNotMatch(source, /\bOFFSET\b/i, '迁移脚本不能使用 OFFSET 扫描大表')
assert.match(source, /await yieldToEventLoop\(\)/, '迁移脚本每批导入后必须让出事件循环')
assert.match(source, /usage_record_shards/, '迁移脚本必须从 usage catalog 读取 usage shard 文件清单')
assert.match(source, /tableNames:\s*\['usage_records'\]/, '迁移脚本必须把 usage shard 明细导入 PostgreSQL usage_records')
assert.match(source, /state-\$\{String\(index\)\.padStart\(3,\s*'0'\)\}\.sqlite3/, '迁移脚本必须覆盖 Codex context state 分片')
assert.doesNotMatch(source, /console\.log\([^)]*postgresUrl/, '迁移脚本日志不能输出 PostgreSQL 连接串')

console.log('SQLite 到 PostgreSQL 离线迁移脚本门禁通过：确认、只读源库、分批游标、非空目标保护和分片迁移边界已固定')
