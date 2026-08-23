import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')
const source = (relativePath: string) => fs.readFileSync(path.join(backendRoot, relativePath), 'utf8')
const repositorySource = (relativePath: string) => fs.readFileSync(path.resolve(backendRoot, '..', relativePath), 'utf8')
const exists = (relativePath: string) => fs.existsSync(path.join(backendRoot, relativePath))

const routeSource = source('src/modules/proxies/proxies.routes.ts')
const handoverSource = source('src/modules/background/proxy-latency-handover.ts')
const dbServiceSource = source('src/db-service.ts')
const backgroundSource = source('src/modules/background/background-jobs.ts')
const registrySource = source('src/modules/background/background-job-registry.entries.ts')
const capacityEnvSource = source('.env.capacity.example')
const currentOwnershipDocs = [
  repositorySource('docs/architecture/架构总览.md'),
  repositorySource('docs/architecture/backend/README.md'),
  repositorySource('docs/functions/SQLite存储说明.md'),
  repositorySource('docs/functions/PostgreSQL与Redis高性能模式设计.md'),
  repositorySource('docs/migration/Go后端架构基线.md'),
  repositorySource('docs/migration/Go三项目架构基线.md')
]

for (const deletedPath of [
  'src/modules/proxies/proxy-test.service.ts',
  'src/modules/background/proxy-latency-jobs-projector.service.ts',
  'src/modules/background/proxy-latency-jobs-outcome-projection-runtime.service.ts',
  'src/storage/proxy-latency-jobs-outcome.repository.ts',
  'src/storage/proxy-latency-projection-cursor.repository.ts',
]) {
  assert.equal(exists(deletedPath), false, `J3a Node 文件必须删除: ${deletedPath}`)
}

assert.doesNotMatch(backgroundSource, /proxy-latency-refresh|refreshProxyLatencyBatch/, 'Node 不得注册 J3a scheduler')
assert.doesNotMatch(registrySource, /proxy-latency-refresh/, 'Node background registry 不得包含 J3a scheduler')
assert.doesNotMatch(dbServiceSource, /proxy-latency-jobs-outcome|startProxyLatency|stopProxyLatency/, 'Node db-service 不得启动/停止 J3a projector')
assert.doesNotMatch(routeSource, /testProxyById|update_proxy_test_state|proxyLatency(Node|Go)Owner|resolveProxyLatencyHandoverGate/, 'Node 路由不得保留旧执行器、业务写回或 owner/fallback 分支')
assert.match(routeSource, /runGoProxyManualExecution/, '管理面必须保留 Go manual adapter')
assert.match(routeSource, /const execution = await runGoProxyManualExecution[\s\S]*?const after = await findProxyAsync[\s\S]*?if \(!after\)/, 'Go 手动执行完成后必须再次确认代理仍存在，删除竞态必须返回 404')
assert.match(handoverSource, /response\.status === 404[\s\S]*J3a manual proxy missing or deleted[\s\S]*代理不存在/, '只有 Go 的明确 missing/deleted 语义才能映射为代理不存在')
assert.match(handoverSource, /class GoManualBridgeHttpError[\s\S]*retryAfter/, 'Go manual 非 2xx 必须保留 HTTP status 与 Retry-After')
assert.match(handoverSource, /JUHE_AI_PROXY_LATENCY_JOBS_OWNER/, 'Go manual adapter 必须要求显式 Go owner 配置')
assert.match(handoverSource, /endpointURL\.protocol !== 'http:'/, 'Go manual adapter 必须限制为本机 HTTP loopback')
assert.match(handoverSource, /127\.0\.0\.1.*::1/, 'Go manual adapter 必须拒绝非 loopback endpoint')
assert.match(handoverSource, /readBoundedGoManualResponse/, 'Go manual adapter 必须有界读取 Go 响应')
assert.doesNotMatch(handoverSource, /proxyLatency(Node|Go)OwnerEnabled|resolveProxyLatencyHandoverGate|fallback/i, 'Go adapter 不得包含 Node fallback/双 owner')
assert.match(handoverSource, /assertProxyLatencyReportMatchesProxy/, 'Go report 必须在 Node 写回前做 proxy identity 校验')
assert.match(routeSource, /error instanceof GoManualBridgeHttpError[\s\S]*error\.status === 503[\s\S]*Retry-After/, 'Node 管理路由必须透传 Go lease busy 的 503/Retry-After')
assert.doesNotMatch(capacityEnvSource, /JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_/, '容量配置不得保留 Node J3a scheduler 开关')
for (const document of currentOwnershipDocs) {
  assert.match(document, /J3a[\s\S]{0,80}(Go|juhe-ai-jobs)/, '权威文档必须标明 J3a 的 Go owner')
  assert.doesNotMatch(document, /proxy-latency-refresh/, '权威文档不得把 Node scheduler 作为当前 J3a owner')
}

console.log('proxy-latency-go-only-regression: PASS')
