import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const backendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')
const source = (relativePath: string) => fs.readFileSync(path.join(backendRoot, relativePath), 'utf8')
const repositorySource = (relativePath: string) => fs.readFileSync(path.resolve(backendRoot, '..', relativePath), 'utf8')
const exists = (relativePath: string) => fs.existsSync(path.join(backendRoot, relativePath))
const archiveRoot = path.resolve(backendRoot, '..', 'migration-backup/node/j3a-proxy-latency-manual-control-cutover-20260826')

const routeSource = source('src/modules/proxies/proxies.routes.ts')
const systemApiSource = source('src/modules/system-api/system-api-app.ts')
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
const j3aHandoffDocs = [
  repositorySource('docs/migration/J3a-代理延迟检测完整迁移契约.md'),
  repositorySource('docs/migration/J3-候选任务L1语义冻结.md'),
  repositorySource('docs/plans/计划-20260821T182741627Z-J3a代理延迟检测L3-PG smoke.md'),
  repositorySource('docs/plans/计划-20260822T140000000Z-J3a代理延迟检测Node-Go深度对照.md')
]

for (const deletedPath of [
  'src/modules/proxies/proxy-test.service.ts',
  'src/modules/proxies/proxy-test.contract.ts',
  'src/modules/background/proxy-latency-handover.ts',
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
assert.doesNotMatch(routeSource, /:id\/test|testProxyById|update_proxy_test_state|runGoProxyManualExecution|proxyLatency(Node|Go)Owner|resolveProxyLatencyHandoverGate/, 'Node 路由不得保留 J3a 手动执行器、业务写回或 owner/fallback 分支')
assert.doesNotMatch(systemApiSource, /proxyLatencyGoOwnerHealth|JUHE_AI_PROXY_LATENCY_JOBS_HTTP_URL|J3a Go health observer endpoint/, 'Node 不得为 J3a 调用或观测 Go jobs')
assert.doesNotMatch(capacityEnvSource, /JUHE_AI_BACKGROUND_PROXY_LATENCY_REFRESH_/, '容量配置不得保留 Node J3a scheduler 开关')
for (const document of currentOwnershipDocs) {
  assert.match(document, /J3a[\s\S]{0,80}(Go|juhe-ai-jobs)/, '权威文档必须标明 J3a 的 Go owner')
  assert.doesNotMatch(document, /proxy-latency-refresh/, '权威文档不得把 Node scheduler 作为当前 J3a owner')
}
for (const document of j3aHandoffDocs) {
  assert.doesNotMatch(document, /Node\s*(?:→|->)\s*Go\s*(?:→|->)\s*Node/, 'J3a handoff 不得描述为 Go 回调 Node 的跨进程环')
}

const archiveManifest = JSON.parse(fs.readFileSync(path.join(archiveRoot, 'manifest.json'), 'utf8')) as {
  manifestVersion: number
  lifecycleStatus: string
  files: Array<{ archivePath: string; sha256: string; sourceKind: string }>
}
assert.equal(archiveManifest.manifestVersion, 2, 'J3a Node 归档 manifest 必须为当前版本')
assert.equal(archiveManifest.lifecycleStatus, 'node-active-path-removed-runtime-handoff-pending', 'J3a Node 归档必须明确仍待 runtime handoff')
for (const archived of archiveManifest.files) {
  const archivedPath = path.resolve(backendRoot, '..', archived.archivePath)
  assert.equal(fs.existsSync(archivedPath), true, `J3a 归档文件缺失: ${archived.archivePath}`)
  const digest = crypto.createHash('sha256').update(fs.readFileSync(archivedPath)).digest('hex')
  assert.equal(digest, archived.sha256.toLowerCase(), `J3a 归档 SHA-256 不匹配: ${archived.archivePath}`)
}

console.log('proxy-latency-go-only-regression: PASS（Node J3a 控制面和跨进程桥已删除）')
