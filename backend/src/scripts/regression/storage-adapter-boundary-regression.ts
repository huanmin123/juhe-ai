import assert from 'node:assert/strict'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

interface InventoryPattern {
  id: string
  description: string
  pattern: RegExp
}

interface InventoryHit {
  file: string
  line: number
  patternId: string
  domain: string
  area: string
  text: string
}

const scriptDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(scriptDir, '../../..')
const repoRoot = resolve(backendRoot, '..')
const srcRoot = resolve(backendRoot, 'src')

const inventoryPatterns: InventoryPattern[] = [
  {
    id: 'sqlite_handle',
    description: 'SQLite database handle access',
    pattern: /\bget(?:Business|Dataset|Stats|UsageCatalog|UsageRecordShard|CodexContextStateShard)Database\s*\(/
  },
  {
    id: 'sqlite_client',
    description: 'SQLite DatabaseClient adapter creation',
    pattern: /\bcreateSqliteDatabaseClient\s*\(/
  },
  {
    id: 'sqlite_transaction',
    description: 'SQLite synchronous transaction helper',
    pattern: /\brunInDatabaseTransaction\s*\(/
  },
  {
    id: 'postgres_pool_or_client',
    description: 'PostgreSQL pool/client adapter access',
    pattern: /\b(?:getPostgresPool|createPostgresDatabaseClient)\s*\(/
  },
  {
    id: 'redis_client',
    description: 'Raw Redis client access',
    pattern: /\b(?:getRedisClient|createDedicatedRedisClient)\s*\(/
  },
  {
    id: 'redis_stream_queue',
    description: 'Redis Stream queue construction or typing',
    pattern: /\bRedisStreamQueue\b/
  },
  {
    id: 'shared_cache',
    description: 'Shared JSON cache creation',
    pattern: /\bcreateSharedJsonCache(?:<[^\n]*>)?\s*\(/
  },
  {
    id: 'app_cache',
    description: 'Process-local app cache creation',
    pattern: /\bcreateAppCache(?:<[^\n]*>)?\s*\(/
  },
  {
    id: 'runtime_state_store',
    description: 'Runtime state store creation',
    pattern: /\bcreateRuntimeStateStore\s*\(/
  },
  {
    id: 'driver_branch',
    description: 'Runtime driver branch',
    pattern: /\bruntimeConfig\.(?:databaseDriver|cacheDriver|runtimeStateDriver|queueDriver)\b/
  }
]

const requiredFiles = [
  'docs/functions/存储适配接口设计.md',
  'docs/plans/计划-20260629T035152001Z-存储适配接口收敛.md',
  'backend/src/storage/runtime/storage-runtime.ts',
  'backend/src/storage/runtime/sqlite-memory-runtime.ts',
  'backend/src/storage/runtime/postgres-redis-runtime.ts',
  'backend/src/storage/runtime/index.ts'
]

const sourceFiles = listSourceFiles(srcRoot)
const hits = collectInventoryHits(sourceFiles)
const unclassifiedHits = hits.filter((hit) => hit.domain === 'unknown' || hit.area === 'unknown')

for (const requiredFile of requiredFiles) {
  assert.ok(existsSync(resolve(repoRoot, requiredFile)), `缺少存储适配边界文件：${requiredFile}`)
}

assert.equal(unclassifiedHits.length, 0, `存在未分类的存储直接调用点：${JSON.stringify(unclassifiedHits.slice(0, 10), null, 2)}`)
assertNoRuntimeNodeSqliteValueImports(sourceFiles)
assertNoUnexpectedRawDriverImports(sourceFiles)
assertNoUnexpectedRuntimeSqliteDirectAccess(hits)
assertNoHttpRouteSqliteSyncImports(sourceFiles)
assertStorageRuntimeSkeletonBoundary()
assertGatewayRuntimeCachePostgresWorkerBoundary()
assertGroupSummaryAsyncTimezoneBoundary()
assertSqliteOnlyAsyncHelperGuards()

const summary: Record<string, unknown> = {
  message: 'storage-adapter-boundary-regression passed',
  scannedFiles: sourceFiles.length,
  totalHits: hits.length,
  runtimeHits: hits.filter((hit) => hit.area !== 'scripts').length,
  byPattern: countBy(hits, (hit) => hit.patternId),
  byArea: countBy(hits, (hit) => hit.area),
  byDomain: countBy(hits, (hit) => hit.domain),
  highRiskDomains: topEntries(countBy(hits.filter((hit) => hit.area !== 'scripts'), (hit) => hit.domain), 12)
}

if (process.env.JUHE_STORAGE_ADAPTER_BOUNDARY_PRINT_HITS === '1') {
  summary.hits = hits
}

console.log(JSON.stringify(summary, null, 2))

function listSourceFiles(root: string): string[] {
  const output: string[] = []
  const walk = (directory: string): void => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const fullPath = resolve(directory, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'dist' || entry.name === 'node_modules') continue
        walk(fullPath)
        continue
      }
      if (entry.isFile() && entry.name.endsWith('.ts')) {
        output.push(fullPath)
      }
    }
  }
  walk(root)
  return output.sort()
}

function collectInventoryHits(files: string[]): InventoryHit[] {
  const output: InventoryHit[] = []
  for (const filePath of files) {
    const relativePath = slash(relative(srcRoot, filePath))
    const content = readFileSync(filePath, 'utf8')
    const lines = content.split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      const text = lines[index]
      for (const item of inventoryPatterns) {
        if (!item.pattern.test(text)) continue
        output.push({
          file: relativePath,
          line: index + 1,
          patternId: item.id,
          domain: classifyDomain(relativePath),
          area: classifyArea(relativePath),
          text: text.trim()
        })
      }
    }
  }
  return output
}

function classifyArea(filePath: string): string {
  if (filePath.startsWith('scripts/')) return 'scripts'
  if (filePath.startsWith('modules/db-service/')) return 'db-service'
  if (filePath.startsWith('modules/gateway/')) return 'gateway'
  if (filePath.startsWith('modules/background/')) return 'background'
  if (filePath.startsWith('modules/')) return 'modules'
  if (filePath.startsWith('storage/runtime/')) return 'storage-runtime'
  if (filePath.startsWith('storage/schema/')) return 'storage-schema'
  if (filePath.startsWith('storage/')) return 'storage'
  if (filePath.startsWith('shared/')) return 'shared'
  if (filePath.startsWith('config/')) return 'config'
  if (filePath === 'server.ts' || filePath === 'worker.ts' || filePath === 'db-service.ts') return 'entrypoint'
  return 'unknown'
}

function classifyDomain(filePath: string): string {
  const path = filePath.toLowerCase()
  const domainRules: Array<[string, RegExp]> = [
    ['regression-or-performance-script', /^scripts\//],
    ['db-service', /db-service/],
    ['gateway', /gateway|quota|dispatch|runtime-cache|hybrid|session-affinity|client-ip-account-avoidance|client-ip-error-circuit/],
    ['background', /background|worker|dataset-writer|stats-writer|maintenance-jobs|background-jobs/],
    ['system-api', /system-api/],
    ['rate-limit', /rate-limit/],
    ['system-account', /system-account|auth|login|captcha|session/],
    ['account', /account(?!-authorization)|openai-oauth|cooldown|health|quality|model-mapping|model-filter|probe/],
    ['api-key', /api-key|apikey/],
    ['announcement', /announcement/],
    ['group', /group/],
    ['route-strategy', /route-strategy/],
    ['authorization', /authorization|resource-authorization|system-team/],
    ['provider-model', /provider|model-catalog|custom-provider-models|pricing/],
    ['proxy', /proxy/],
    ['response-inspection', /response-inspection/],
    ['settings', /settings|global-settings/],
    ['usage-records', /usage-record|usage-shard|usage-catalog/],
    ['usage-stats', /usage-stats|usage-rank|usage-overview|usage-scope|usage-summary|usage-window|ai-performance|quota-hourly|range-window/],
    ['client-ip-stats', /client-ip/],
    ['audit-log', /audit/],
    ['operation-log', /operation-log/],
    ['public-api-log', /public-api-log/],
    ['runtime-log', /runtime-log/],
    ['record-maintenance', /record-maintenance|record-cleanup|data-retention|deleted-record|cleanup/],
    ['external-integration', /external/],
    ['model-check', /model-check/],
    ['model-trust', /model-trust/],
    ['chat', /chat/],
    ['openai-compatible-artifacts', /openai-compatible-(?:files|vector-stores)/],
    ['system-metrics', /system-metrics|table-monitor|metrics/],
    ['codex-context', /codex-context/],
    ['storage-repository-facade', /repositories|repository-lookups/],
    ['storage-infra', /database|postgres|redis|cache|runtime-state|query-utils|sqlite|schema/],
    ['entrypoint', /server\.ts|worker\.ts|db-service\.ts/]
  ]
  for (const [domain, pattern] of domainRules) {
    if (pattern.test(path)) return domain
  }
  return 'unknown'
}

function assertNoRuntimeNodeSqliteValueImports(files: string[]): void {
  const offenders: Array<{ file: string; line: number; text: string }> = []
  const allowedLazyFiles = new Set(['storage/database.ts', 'storage/usage-record-shards.ts'])
  for (const filePath of files) {
    const relativePath = slash(relative(srcRoot, filePath))
    if (relativePath.startsWith('scripts/')) continue
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      const text = lines[index].trim()
      if (/^import\s+(?!type\b).*\s+from ['"]node:sqlite['"]/.test(text)) {
        offenders.push({ file: relativePath, line: index + 1, text })
      }
      if (/\b(?:require|import)\(['"]node:sqlite['"]\)/.test(text) && !allowedLazyFiles.has(relativePath)) {
        offenders.push({ file: relativePath, line: index + 1, text })
      }
    }
  }
  assert.deepEqual(offenders, [], '运行态源码不能值导入 node:sqlite；SQLite 必须由 standalone adapter / SQLite 基础设施懒加载')
}

function assertNoUnexpectedRawDriverImports(files: string[]): void {
  const allowedDriverFiles = new Set([
    'shared/redis-client.ts',
    'storage/postgres-client.ts',
    'storage/postgres-goose-schema-gate.ts'
  ])
  const offenders: Array<{ file: string; line: number; text: string }> = []
  for (const filePath of files) {
    const relativePath = slash(relative(srcRoot, filePath))
    if (relativePath.startsWith('scripts/')) continue
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      const text = lines[index].trim()
      if (!/(?:from ['"](?:pg|redis)['"]|\b(?:require|import)\(['"](?:pg|redis)['"]\))/.test(text)) continue
      if (allowedDriverFiles.has(relativePath)) continue
      offenders.push({ file: relativePath, line: index + 1, text })
    }
  }
  assert.deepEqual(offenders, [], '运行态源码不能绕过 shared/redis-client.ts 或 storage/postgres-client.ts 直接加载 pg / redis 驱动')
}

function assertNoUnexpectedRuntimeSqliteDirectAccess(inventoryHits: InventoryHit[]): void {
  const allowed: Record<string, RegExp[]> = {
    'db-service.ts': [/\bgetBusinessDatabase\(\)/],
    'worker.ts': [/\bgetDatasetDatabase\(\)/, /\bgetUsageCatalogDatabase\(\)/],
    'modules/external-integrations/external-public-account-push.service.ts': [/\bgetBusinessDatabase\(\)/, /\brunInDatabaseTransaction\s*\(/],
    'modules/operation-logs/operation-log.service.ts': [/\brunInDatabaseTransaction\s*\(/],
    'modules/background/background-stats-writer.ts': [/\bgetStatsDatabase\(\)/],
    'modules/background/data-retention-cleanup.service.ts': [/\bgetDatasetDatabase\(\)/],
    'modules/stats/mock-background-runtime.ts': [/\bget(?:Business|Dataset|Stats)Database\(\)/],
    'modules/gateway/quota/api-key-quota.service.ts': [/\bgetStatsDatabase\(\)/],
    'modules/gateway/quota/authorization-quota.service.ts': [/\bget(?:Business|Stats)Database\(\)/]
  }
  const runtimeAreas = new Set(['entrypoint', 'db-service', 'modules', 'gateway', 'background'])
  const offenders = inventoryHits
    .filter((hit) => runtimeAreas.has(hit.area))
    .filter((hit) => hit.patternId === 'sqlite_handle' || hit.patternId === 'sqlite_client' || hit.patternId === 'sqlite_transaction')
    .filter((hit) => !(allowed[hit.file] ?? []).some((pattern) => pattern.test(hit.text)))
    .map(({ file, line, patternId, text }) => ({ file, line, patternId, text }))
  assert.deepEqual(
    offenders,
    [],
    '运行态非 storage 基础设施不得新增 SQLite getter/client 直连；确需 SQLite-only 分支必须在此回归中显式分类'
  )
}

function assertNoHttpRouteSqliteSyncImports(files: string[]): void {
  const forbiddenImportNames = [
    'getBusinessDatabase',
    'getDatasetDatabase',
    'getUsageCatalogDatabase',
    'getStatsDatabase',
    'getCodexContextStateShardDatabase',
    'runInDatabaseTransaction'
  ]
  const offenders: Array<{ file: string; line: number; text: string }> = []
  for (const filePath of files) {
    const relativePath = slash(relative(srcRoot, filePath))
    if (!relativePath.startsWith('modules/') || !relativePath.endsWith('.routes.ts')) continue
    const source = readFileSync(filePath, 'utf8')
    for (const match of source.matchAll(/import\s*\{([\s\S]*?)\}\s*from ['"][^'"]*storage\/database\.js['"]/g)) {
      const importedNames = match[1]
      const forbidden = forbiddenImportNames.filter((name) => new RegExp(`\\b${name}\\b`).test(importedNames))
      if (!forbidden.length) continue
      offenders.push({
        file: relativePath,
        line: lineNumberAt(source, match.index ?? 0),
        text: forbidden.join(', ')
      })
    }
  }
  assert.deepEqual(offenders, [], 'HTTP routes 不得直接导入 SQLite getter 或同步事务 helper；PG 路径必须走 async repository / DB service')
}

function assertStorageRuntimeSkeletonBoundary(): void {
  const runtimeFiles = requiredFiles
    .filter((filePath) => filePath.startsWith('backend/src/storage/runtime/'))
    .map((filePath) => resolve(repoRoot, filePath))
  const forbiddenPatterns = [
    /from ['"].*\.\.\/database\.js['"]/,
    /from ['"].*\.\.\/usage-record-shards\.js['"]/,
    /from ['"].*\.\.\/postgres-client\.js['"]/,
    /from ['"].*\.\.\/\.\.\/shared\/redis-client\.js['"]/,
    /from ['"].*\.\.\/database-client\.js['"]/,
    /from ['"].*\.\.\/\.\.\/shared\/cache\.js['"]/,
    /from ['"].*\.\.\/\.\.\/shared\/runtime-state-store\.js['"]/,
    /from ['"].*\.\.\/\.\.\/shared\/redis-stream-queue\.js['"]/,
    /from ['"]node:sqlite['"]/,
    /\bget(?:Business|Dataset|Stats|UsageCatalog|UsageRecordShard|CodexContextStateShard)Database\s*\(/,
    /\bgetPostgresPool\s*\(/,
    /\bgetRedisClient\s*\(/,
    /\bcreateSqliteDatabaseClient\s*\(/,
    /\bcreatePostgresDatabaseClient\s*\(/,
    /\bcreateDedicatedRedisClient\s*\(/,
    /\bcreateSharedJsonCache(?:<[^\n]*>)?\s*\(/,
    /\bcreateAppCache(?:<[^\n]*>)?\s*\(/,
    /\bcreateRuntimeStateStore\s*\(/,
    /\bRedisStreamQueue\b/
  ]
  const offenders: Array<{ file: string; line: number; text: string }> = []
  for (const filePath of runtimeFiles) {
    const lines = readFileSync(filePath, 'utf8').split(/\r?\n/)
    for (let index = 0; index < lines.length; index += 1) {
      const text = lines[index]
      if (!forbiddenPatterns.some((pattern) => pattern.test(text))) continue
      offenders.push({
        file: slash(relative(repoRoot, filePath)),
        line: index + 1,
        text: text.trim()
      })
    }
  }
  assert.deepEqual(offenders, [], 'StorageRuntime 骨架只能做装配描述，不能直接打开 SQLite / PostgreSQL / Redis 基础设施')
}

function assertGatewayRuntimeCachePostgresWorkerBoundary(): void {
  const source = readFileSync(resolve(srcRoot, 'modules/gateway/runtime/runtime-cache.service.ts'), 'utf8')
  const gatewayDbServiceRequestSource = readFileSync(resolve(srcRoot, 'modules/gateway/runtime/gateway-db-service-request.ts'), 'utf8')
  assert.ok(
    source.includes('function shouldUseGatewayRuntimeDbService()')
      && source.includes("runtimeConfig.databaseDriver === 'postgres'")
      && source.includes("runtimeConfig.processRole !== 'db-service'"),
    'gateway runtime cache 应让 PG worker 通过 DB service 读取运行态缓存底座'
  )
  assert.doesNotMatch(
    source,
    /runtimeConfig\.processRole !== 'server'[\s\S]{0,120}readCachedGatewaySettings\(\)/,
    'PG worker 不得在 async 网关设置读取中回退同步 SQLite'
  )
  assert.doesNotMatch(
    source,
    /runtimeConfig\.processRole !== 'server'[\s\S]{0,160}resolveCachedGroupUsageAccessMetadata\(/,
    'PG worker 不得在 async 分组访问读取中回退同步 SQLite'
  )
  assert.ok(
    gatewayDbServiceRequestSource.includes('function requestGatewayDbService')
      && gatewayDbServiceRequestSource.includes("runtimeConfig.processRole === 'worker'")
      && gatewayDbServiceRequestSource.includes("import('../../background/background-ipc.js')")
      && gatewayDbServiceRequestSource.includes('requestBackgroundWorkerDbService(operation, options)'),
    'gateway 运行态 DB service 副作用在 PG worker 中必须通过 background IPC 转发'
  )
  for (const relativePath of [
    'modules/gateway/runtime/account-api-key-effects.service.ts',
    'modules/gateway/runtime/account-effects.ts',
    'modules/gateway/runtime/account-side-effects.service.ts',
    'modules/gateway/codex-responses/chat-bridge-state.ts'
  ]) {
    const sideEffectSource = readFileSync(resolve(srcRoot, relativePath), 'utf8')
    assert.ok(sideEffectSource.includes('requestGatewayDbService'), `${relativePath} 应使用 worker-safe DB service 请求包装`)
    assert.doesNotMatch(sideEffectSource, /\brequestDbService\(/, `${relativePath} 不得直接调用 requestDbService`)
  }
}

function assertGroupSummaryAsyncTimezoneBoundary(): void {
  const source = readFileSync(resolve(srcRoot, 'storage/group-summary.repository.ts'), 'utf8')
  const start = source.indexOf('async function buildGroupSummariesAsync')
  const end = source.indexOf('function canBindAuthorizedGroupRowToApiKey', start)
  assert.ok(start >= 0 && end > start, '分组汇总必须保留 buildGroupSummariesAsync async 入口')
  const asyncFunction = source.slice(start, end)
  assert.match(asyncFunction, /\busageStatsTimezoneAsync\(\)/, 'PG 分组汇总 async 路径必须使用 async 时区配置')
  assert.doesNotMatch(asyncFunction, /\busageStatsTimezone\(\)/, 'PG 分组汇总 async 路径不能调用同步时区配置，避免回退 SQLite')
}

function assertSqliteOnlyAsyncHelperGuards(): void {
  const guardedHelpers: Array<[string, string, string]> = [
    ['storage/group-account-stats-cache.repository.ts', 'refreshDirtyGroupAccountStatsCacheWithWriter', 'refreshDirtyGroupAccountStatsCacheAsync'],
    ['storage/external-integration-source-auth.repository.ts', 'flushExternalIntegrationSourceLastUsedTouchesForTest', 'PostgreSQL 测试必须使用 async driver']
  ]
  for (const [relativePath, functionName, messageToken] of guardedHelpers) {
    const source = readFileSync(resolve(srcRoot, relativePath), 'utf8')
    const body = functionBody(source, functionName)
    assert.ok(
      /runtimeConfig\.databaseDriver !== 'sqlite'/.test(body) || /\bassert\w*SqliteOnly\(/.test(body),
      `${relativePath}:${functionName} 必须显式拒绝 PG 误用`
    )
    assert.match(source, /runtimeConfig\.databaseDriver !== 'sqlite'/, `${relativePath}:${functionName} 必须保留 SQLite-only driver guard`)
    assert.ok(source.includes(messageToken), `${relativePath}:${functionName} PG guard 错误信息必须指出正确 async 路径`)
  }
}

function functionBody(sourceText: string, functionName: string): string {
  const start = sourceText.indexOf(`function ${functionName}`)
  assert.ok(start >= 0, `缺少函数 ${functionName}`)
  const openBrace = functionBodyOpenBrace(sourceText, start)
  assert.ok(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) return sourceText.slice(openBrace, index + 1)
    }
  }
  throw new Error(`函数 ${functionName} 函数体解析失败`)
}

function functionBodyOpenBrace(sourceText: string, start: number): number {
  let parenDepth = 0
  for (let index = start; index < sourceText.length; index += 1) {
    const char = sourceText[index]
    if (char === '(') parenDepth += 1
    if (char === ')') parenDepth = Math.max(0, parenDepth - 1)
    if (char === '{' && parenDepth === 0) return index
  }
  return -1
}

function countBy<T>(items: T[], key: (item: T) => string): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const item of items) {
    const value = key(item)
    counts[value] = (counts[value] ?? 0) + 1
  }
  return Object.fromEntries(Object.entries(counts).sort(([left], [right]) => left.localeCompare(right)))
}

function topEntries(counts: Record<string, number>, limit: number): Record<string, number> {
  return Object.fromEntries(
    Object.entries(counts)
      .sort((left, right) => right[1] - left[1] || left[0].localeCompare(right[0]))
      .slice(0, limit)
  )
}

function slash(path: string): string {
  return path.replace(/\\/g, '/')
}

function lineNumberAt(sourceText: string, offset: number): number {
  return sourceText.slice(0, Math.max(0, offset)).split(/\r?\n/).length
}
