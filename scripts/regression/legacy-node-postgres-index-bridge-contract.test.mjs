import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const root = resolve(import.meta.dirname, '../..')
const operationsRoot = resolve(root, 'docs/deploy/macos/operations')
const catalogPath = resolve(operationsRoot, 'legacy-node-postgres-index-bridge.catalog.json')
const scriptPath = resolve(operationsRoot, 'legacy-node-postgres-index-bridge.mjs')
const guidePath = resolve(operationsRoot, '遗留NodePostgreSQL索引桥接说明.md')
const migration87Path = resolve(root, 'backend-go/db/migrations/000087_w7_model_quality_health_sync_candidate_indexes.sql')

const [catalogRaw, script, guide, operationsReadme, migration87, packageJson, releasePowerShell, releaseShell] = await Promise.all([
  readFile(catalogPath, 'utf8'),
  readFile(scriptPath, 'utf8'),
  readFile(guidePath, 'utf8'),
  readFile(resolve(operationsRoot, 'README.md'), 'utf8'),
  readFile(migration87Path, 'utf8'),
  readFile(resolve(root, 'package.json'), 'utf8'),
  readFile(resolve(root, 'scripts/package-release.ps1'), 'utf8'),
  readFile(resolve(root, 'scripts/package-release.sh'), 'utf8')
])
const catalog = JSON.parse(catalogRaw)
const catalogFingerprint = createHash('sha256').update(JSON.stringify(catalog)).digest('hex')
const scriptFingerprint = /const catalogFingerprint = '([a-f0-9]{64})'/.exec(script)?.[1]

assert.equal(catalog.catalogVersion, 1)
assert.equal(catalog.owner, 'node-postgres-legacy')
assert.equal(scriptFingerprint, catalogFingerprint, '脚本必须拒绝被改写的桥接目录')
assert.deepEqual(catalog.indexes.map((index) => index.name), [
  'idx_model_check_runs_quality_health_sync_due',
  'idx_model_check_runs_quality_health_sync_invalid_time',
  'idx_accounts_balance_auto_detect_due'
])
assert.equal(catalog.indexes.length, 3, '遗留桥接目录只能包含批准的三条索引')
assert.deepEqual(catalog.indexes[0].notApplicableWhenAllColumnsMissing, [
  'quality_health_sync_next_attempt_at',
  'quality_health_sync_claim_until',
  'quality_health_sync_claim_epoch',
  'quality_health_sync_attempt_count'
], 'due 索引只能在完整的纯 Node 缺列形态下不适用')
assert.deepEqual(catalog.indexes[1].notApplicableWhenAllColumnsMissing, [
  'quality_health_sync_next_attempt_at',
  'quality_health_sync_claim_until',
  'quality_health_sync_claim_epoch',
  'quality_health_sync_attempt_count',
  'quality_health_sync_last_error_class'
], 'invalid-time 索引只能在完整的纯 Node 缺列形态下不适用')
assert.equal(catalog.indexes[2].notApplicableWhenAllColumnsMissing, undefined, 'Node 账户索引不得因缺字段而被跳过')
for (const index of catalog.indexes) {
  assert.equal(index.accessMethod, 'btree', `${index.name} 必须声明 btree 访问方法`)
  assert.match(index.createSql, /^CREATE INDEX CONCURRENTLY /, `${index.name} 必须并发创建`)
  assert.doesNotMatch(index.createSql, /\bIF\s+NOT\s+EXISTS\b|\bDROP\s+INDEX\b/i, `${index.name} 不得掩盖冲突或携带删除`)
  assert.deepEqual(index.requiredIndexProperties, {
    indisunique: false,
    indisexclusion: false,
    indnkeyatts: index.requiredKeyExpressions.length,
    indnatts: index.requiredKeyExpressions.length
  }, `${index.name} 必须锁定非唯一、非 exclusion、无 INCLUDE 列的物理属性`)
}

const migration87Creates = migration87.match(/CREATE INDEX idx_model_check_runs_quality_health_sync_[\s\S]*?;/g) ?? []
assert.equal(migration87Creates.length, 2, '87 的最终形态必须恰有两条索引')
for (const statement of migration87Creates) {
  const expected = normalizeSql(statement.replace(/^CREATE INDEX /, 'CREATE INDEX CONCURRENTLY ').replace(/;$/, ''))
  const name = /CREATE INDEX idx_([a-z0-9_]+)/i.exec(statement)?.[1]
  const catalogIndex = catalog.indexes.find((index) => index.name === `idx_${name}`)
  assert.ok(catalogIndex, `目录缺少 87 索引 ${name}`)
  assert.equal(normalizeSql(catalogIndex.createSql), expected, `目录必须保留 87 ${catalogIndex.name} 的最终 canonical 定义`)
}

const balanceIndex = catalog.indexes.at(-1)
assert.match(balanceIndex.createSql, /schedulable = 1/)
assert.match(balanceIndex.createSql, /balance_query_enabled = 0/)
assert.equal(balanceIndex.requiredColumns.schedulable, 'integer')
assert.equal(balanceIndex.requiredColumns.balance_query_enabled, 'integer')
assert.deepEqual(balanceIndex.requiredColumnDefaults, { schedulable: '1', balance_query_enabled: '0' })
assert.deepEqual(balanceIndex.requiredKeyExpressions, ['balance_query_next_refresh_at', 'id'])
assert.ok(catalog.indexes.every((index) => Array.isArray(index.requiredKeyExpressions) && index.requiredKeyExpressions.length > 0), '目录必须固定索引键顺序')
assert.ok(catalog.indexes[1].requiredDefinitionTokens.includes('quality_health_sync_attempt_count < 9223372036854775807'), 'invalid-time 索引必须保留 attempt count predicate')

for (const contract of [
  "action: 'inspect'",
  "new Set(['inspect', 'apply', 'verify', 'cleanup-invalid'])",
  'pg_try_advisory_lock(hashtextextended($1, 0))',
  'CREATE INDEX CONCURRENTLY',
  'DROP INDEX CONCURRENTLY',
  'current_database()',
  "relname = 'goose_db_version'",
  'format_type(a.atttypid, a.atttypmod)',
  'pg_get_expr(i.indpred, i.indrelid, false)',
  'pg_get_indexdef(i.indexrelid, key_position, false)',
  'a.attnotnull',
  'pg_get_expr(d.adbin, d.adrelid)',
  'pg_get_userbyid(c.relowner)',
  'indisvalid',
  'indislive',
  'i.indisunique',
  'i.indisexclusion',
  'i.indnatts',
  'partial_goose_schema_detected',
  "reason: 'pure_node_schema'",
  'notApplicableWhenAllColumnsMissing',
  'requiredIndexProperties',
  'canonicalBooleanExpression',
  'canonicalKeyExpression',
  'i.indrelid = $3::oid',
  'am.amname AS access_method',
  'SET lock_timeout',
  'SET statement_timeout',
  'catalog_confirmation_required',
  'integer_value_precondition_failed',
  'integer_nullability_precondition_failed',
  'column_default_precondition_failed',
  'cleanup_index_not_allowlisted',
  'catalog_fingerprint_mismatch'
]) {
  assert.ok(script.includes(contract), `索引桥接缺少安全契约：${contract}`)
}
assert.match(script, /\(existing\.indisvalid && existing\.indisready && existing\.indislive\)/, '清理无效索引必须和检查状态使用同一有效性定义')
assert.match(script, /missingOptionalColumns\.length === optionalColumns\.length && optionalColumns\.length > 0/, '只有全部纯 Node 专用字段缺失时才允许 not_applicable')
assert.match(script, /if \(missingOptionalColumns\.length > 0\) throw new BridgeError\('partial_goose_schema_detected'\)/, '半套 Goose 字段不得被当作 not_applicable')
assert.match(script, /existing\.indisunique !== requiredProperties\.indisunique/, '同名索引必须核验唯一属性')
assert.match(script, /existing\.indisexclusion !== requiredProperties\.indisexclusion/, '同名索引必须核验 exclusion 属性')
assert.match(script, /Number\(existing\.indnatts\) !== Number\(existing\.indnkeyatts\)/, '同名索引不得带 INCLUDE 列')
assert.doesNotMatch(script, /INSERT\s+INTO\s+.*goose|UPDATE\s+.*goose|DELETE\s+FROM\s+.*goose/i, '桥接不得写入 Goose ledger')
assert.doesNotMatch(script, /\bIF\s+NOT\s+EXISTS\b/i, '桥接不得用 IF NOT EXISTS 掩盖索引冲突')
assert.doesNotMatch(catalog.indexes.map((index) => index.createSql).join('\n'), /;/, '桥接目录 DDL 不得包含多语句')
assert.match(operationsReadme, /遗留NodePostgreSQL索引桥接说明\.md/, 'macOS 运维目录 README 必须链接桥接说明')
assert.match(guide, /不得存在 `goose_db_version`/, '说明必须拒绝 Goose ledger')
assert.match(guide, /`CREATE\/DROP INDEX CONCURRENTLY`/, '说明必须保留并发索引边界')
assert.match(guide, new RegExp(catalogFingerprint), '说明中的 apply 指纹必须与目录、脚本一致')
assert.match(guide, /not_applicable` \/ `pure_node_schema/, '说明必须明确纯 Node quality 索引是不适用而非已创建')
assert.match(guide, /partial_goose_schema_detected/, '说明必须明确半套 Goose schema 不能跳过')
assert.match(guide, /postgres:init-schema/, '说明必须约束桥接早于 Node schema 初始化')
assert.match(script, /actualKeys\.every\(\(expression, index\) => canonicalKeyExpression\(expression\) === canonicalKeyExpression\(target\.requiredKeyExpressions\[index\]\)\)/, '同名索引必须逐项匹配键表达式，不能只命中若干 token')
assert.match(script, /canonicalBooleanExpression\(existing\.predicate\) === canonicalBooleanExpression\(expectedPredicate\)/, '同名索引必须完整匹配 partial predicate')

const packageScripts = JSON.parse(packageJson).scripts
assert.equal(typeof packageScripts['test:legacy-node-index-bridge'], 'string', '根 package 必须暴露桥接回归命令')
assert.match(packageScripts['test:macos-operations'], /test:legacy-node-index-bridge/, 'macOS 运维门禁必须包含桥接回归')
assert.match(packageScripts['test:release-package'], /validate-release-package\.test\.mjs/, '发布包门禁必须运行基础包校验')
assert.match(packageScripts['test:release-package'], /test:legacy-node-index-bridge/, '发布包门禁必须验证桥接产物')
assert.match(releasePowerShell, /docs\/deploy/, 'Windows 发布包必须复制部署文档')
assert.match(releaseShell, /docs\/deploy/, 'macOS/Linux 发布包必须复制部署文档')

console.log('legacy Node PostgreSQL index bridge contract passed')

function normalizeSql(value) {
  return value.toLowerCase().replace(/\s+/g, ' ').replace(/\s*([(),])\s*/g, '$1').trim()
}
