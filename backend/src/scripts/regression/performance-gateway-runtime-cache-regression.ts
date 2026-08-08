import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(scriptDir, '../../..')
const runtimeCacheSource = readFileSync(resolve(
  backendRoot,
  'src/modules/gateway/runtime/runtime-cache.service.ts'
), 'utf8')

assert.match(
  runtimeCacheSource,
  /gatewayRuntimeCache = createProcessLocalResourceCache<string, GatewayRuntimeCacheEntry>/,
  '网关敏感运行时必须使用不受 Redis fact-source 开关影响的进程内资源缓存'
)
assert.match(
  runtimeCacheSource,
  /ttlMs: gatewayRuntimeRetainTtlMs,[\s\S]*updateAgeOnGet: runtimeConfig\.runtimeMode === 'standalone'/,
  '高性能模式最后可用快照必须按装载时间硬过期，不能被持续请求无限续期'
)
assert.match(
  functionBody(runtimeCacheSource, 'shouldAllowStaleGatewayRuntimeFallback'),
  /return true/,
  'DB service 短暂不可用时必须允许使用保留窗口内的最后可用快照'
)
assert.match(
  functionBody(runtimeCacheSource, 'syncGatewayCacheInvalidationsBestEffort'),
  /try[\s\S]*syncGatewayCacheInvalidationsFromRuntimeState\(\)[\s\S]*catch/,
  'Redis 失效协调必须是旁路，不能直接成为 AI 请求失败门禁'
)
assert.match(
  functionBody(runtimeCacheSource, 'readGatewaySharedCacheBestEffort'),
  /try[\s\S]*return await operation\(\)[\s\S]*catch[\s\S]*return undefined/,
  'Redis shared cache 读取失败必须退化为 cache miss，不能覆盖事实源结果'
)
assert.match(
  functionBody(runtimeCacheSource, 'writeGatewaySharedCacheBestEffort'),
  /try[\s\S]*await operation\(\)[\s\S]*catch/,
  'Redis shared cache 写入失败必须是 best-effort 旁路'
)
assert.match(
  functionBody(runtimeCacheSource, 'routeCachedDynamicGatewayRuntimeForDispatch'),
  /try[\s\S]*routeCachedDynamicGatewayRuntimeWithFreshSelection\(runtime\)[\s\S]*catch[\s\S]*selected_group_id[\s\S]*groupAccess[\s\S]*accounts\.length[\s\S]*cloneGatewayRuntimeForDispatchAsync\(runtime\)/,
  '动态路由重新选组异常时必须复用有界快照中的上次有效分组，正常空结果不得触发回退'
)
const settingsCacheWriter = functionBody(runtimeCacheSource, 'setGatewaySettingsCacheEntryAsync')
assert(
  settingsCacheWriter.indexOf("gatewaySettingsCache.set('current', cached)")
    < settingsCacheWriter.indexOf('writeGatewaySharedCacheBestEffort('),
  '网关已经取得的设置事实必须先进入本地缓存，再尝试写 Redis shared cache'
)

const cacheProbe = spawnSync(process.execPath, [
  '--import',
  'tsx',
  '--input-type=module',
  '-e',
  [
    "const { createAppCache, createProcessLocalResourceCache } = await import('./src/shared/cache.ts')",
    "const facts = createAppCache({ name: 'probe:facts', max: 2, ttlMs: 1000 })",
    "const resources = createProcessLocalResourceCache({ name: 'probe:resources', max: 2, ttlMs: 1000 })",
    "facts.set('key', { value: 1 })",
    "resources.set('key', { value: 1 })",
    "if (facts.get('key') !== undefined) throw new Error('Redis 模式普通 AppCache 不得成为本地事实源')",
    "if (resources.get('key')?.value !== 1) throw new Error('进程内敏感运行时快照必须可用')"
  ].join(';')
], {
  cwd: backendRoot,
  encoding: 'utf8',
  env: {
    ...process.env,
    NODE_ENV: 'test',
    JUHE_AI_RUNTIME_MODE: 'performance',
    JUHE_AI_PERFORMANCE_NODE_ROLE: 'gateway',
    JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL: 'http://127.0.0.1:65535',
    JUHE_AI_DATABASE_DRIVER: 'postgres',
    JUHE_AI_CACHE_DRIVER: 'redis',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
    JUHE_AI_QUEUE_DRIVER: 'redis_stream',
    JUHE_AI_POSTGRES_URL: 'postgres://test:test@127.0.0.1:5432/test',
    JUHE_AI_REDIS_CACHE_URL: 'redis://127.0.0.1:6379/0',
    JUHE_AI_REDIS_STATE_URL: 'redis://127.0.0.1:6380/0',
    JUHE_AI_REDIS_QUEUE_URL: 'redis://127.0.0.1:6381/0',
    JUHE_AI_LOG_FILE_ENABLED: 'false'
  }
})
assert.equal(cacheProbe.status, 0, cacheProbe.stderr)

console.log('高性能网关最后可用运行时缓存回归通过')

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  const openBrace = source.indexOf('{', start)
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    if (source[index] === '}') {
      depth -= 1
      if (depth === 0) return source.slice(openBrace, index + 1)
    }
  }
  throw new Error(`函数 ${functionName} 未闭合`)
}
