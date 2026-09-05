import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { classifyFrontendBuild, loadRemoteFrontendBuildId } from '../../router/frontendBuildInfo'
import { normalizeFrontendBuildId } from '../../shared/frontendBuildId'

const currentBuildId = '0123456789abcdef0123456789abcdef01234567'
const changedBuildId = '89abcdef0123456789abcdef0123456789abcdef'

assert.equal(
  normalizeFrontendBuildId(currentBuildId.toUpperCase()),
  currentBuildId,
  '完整 Build ID 必须规范化为小写'
)
assert.equal(normalizeFrontendBuildId('invalid'), undefined, '非法 Build ID 必须拒绝')
assert.equal(normalizeFrontendBuildId('a'.repeat(39)), undefined, '短 SHA 必须拒绝')

assert.equal(
  await classifyFrontendBuild(currentBuildId, async () => changedBuildId),
  'changed',
  '远端 Build ID 变化时必须确认系统更新'
)
assert.equal(
  await classifyFrontendBuild(currentBuildId, async () => currentBuildId),
  'same',
  '远端 Build ID 相同时必须识别为同版本资源失败'
)
assert.equal(
  await classifyFrontendBuild(currentBuildId, async () => undefined),
  'unknown',
  '远端 Build ID 缺失时不得猜测系统更新'
)
assert.equal(
  await classifyFrontendBuild('invalid', async () => changedBuildId),
  'unknown',
  '当前 Build ID 非法时不得猜测系统更新'
)
assert.equal(
  await classifyFrontendBuild(currentBuildId, async () => {
    throw new Error('network unavailable')
  }),
  'unknown',
  '版本清单读取失败时必须受控回落为未知'
)

const viteConfigSource = readFileSync(new URL('../../../vite.config.ts', import.meta.url), 'utf8')
const powerShellReleaseSource = readFileSync(new URL('../../../../scripts/package-release.ps1', import.meta.url), 'utf8')
const shellReleaseSource = readFileSync(new URL('../../../../scripts/package-release.sh', import.meta.url), 'utf8')
const builderDockerfileSource = readFileSync(new URL('../../../../docker/Dockerfile.builder', import.meta.url), 'utf8')
const jenkinsSource = readFileSync(new URL('../../../../Jenkinsfile', import.meta.url), 'utf8')
const serverSource = readFileSync(new URL('../../../../migration-backup/node/final-archive/backend/src/server.ts', import.meta.url), 'utf8')

assert.match(viteConfigSource, /__JUHE_AI_FRONTEND_BUILD_ID__/, 'Vite 必须注入当前页面 Build ID')
assert.match(viteConfigSource, /fileName:\s*['"]build-info\.json['"]/, 'Vite 必须输出静态 Build ID 清单')
assert.match(powerShellReleaseSource, /VITE_JUHE_AI_BUILD_ID\s*=\s*\$releaseSourceCommit/, 'PowerShell 发布必须注入冻结提交')
assert.match(shellReleaseSource, /VITE_JUHE_AI_BUILD_ID=["']?\$RELEASE_SOURCE_COMMIT/, 'POSIX 发布必须注入冻结提交')
assert.match(powerShellReleaseSource, /VITE_JUHE_AI_J3B_ENABLED\s*=\s*'false'/, 'PowerShell 正式包必须显式关闭 J3b UI')
assert.match(shellReleaseSource, /VITE_JUHE_AI_J3B_ENABLED=["']false["']/, 'POSIX 正式包必须显式关闭 J3b UI')
assert.match(builderDockerfileSource, /ARG VITE_JUHE_AI_J3B_ENABLED=false[\s\S]*ENV VITE_JUHE_AI_J3B_ENABLED=\$\{VITE_JUHE_AI_J3B_ENABLED\}/, '容器构建器不得继承宿主机的 J3b 环境')
assert.match(jenkinsSource, /--build-arg VITE_JUHE_AI_J3B_ENABLED=false/, 'Jenkins 正式构建必须显式关闭 J3b UI')
assert.match(serverSource, /build-info\.json/, '后端静态服务必须显式设置 Build ID 清单缓存规则')

let requestedUrl = ''
let requestedCache: RequestCache | undefined
const loadedBuildId = await loadRemoteFrontendBuildId({
  baseUrl: '/__aisys__/',
  now: () => 123456,
  timeoutMs: 1500,
  fetcher: async (input, init) => {
    requestedUrl = String(input)
    requestedCache = init?.cache
    return new Response(JSON.stringify({ buildId: changedBuildId }), { status: 200 })
  }
})
assert.equal(loadedBuildId, changedBuildId, '合法静态清单必须返回规范化 Build ID')
assert.equal(requestedUrl, '/__aisys__/build-info.json?t=123456', '静态清单必须带时间参数绕过中间缓存')
assert.equal(requestedCache, 'no-store', '静态清单请求必须禁用浏览器缓存')

assert.equal(
  await loadRemoteFrontendBuildId({
    baseUrl: '/__aisys__/',
    fetcher: async () => new Response(JSON.stringify({ buildId: 'invalid' }), { status: 200 })
  }),
  undefined,
  '非法静态清单必须受控回落'
)
assert.equal(
  await loadRemoteFrontendBuildId({
    baseUrl: '/__aisys__/',
    fetcher: async () => {
      throw new Error('connection refused')
    }
  }),
  undefined,
  '静态清单请求失败必须受控回落'
)
assert.equal(
  await loadRemoteFrontendBuildId({
    baseUrl: '/__aisys__/',
    fetcher: async () => new Response('', { status: 503 })
  }),
  undefined,
  '静态清单非成功响应必须受控回落'
)

let timeoutSignalObserved = false
let timeoutAbortObserved = false
assert.equal(
  await loadRemoteFrontendBuildId({
    baseUrl: '/__aisys__/',
    timeoutMs: 5,
    fetcher: async (_input, init) => new Promise<Response>((resolve) => {
      const signal = init?.signal
      timeoutSignalObserved = signal instanceof AbortSignal
      const keepAliveTimer = setTimeout(() => {
        resolve(new Response(JSON.stringify({ buildId: changedBuildId }), { status: 200 }))
      }, 100)
      if (!signal) {
        return
      }
      signal.addEventListener('abort', () => {
        clearTimeout(keepAliveTimer)
        timeoutAbortObserved = true
        resolve(new Response('', { status: 503 }))
      }, { once: true })
    })
  }),
  undefined,
  '静态清单超时必须受控回落'
)
assert.equal(timeoutSignalObserved, true, '静态清单请求必须携带超时 signal')
assert.equal(timeoutAbortObserved, true, '静态清单请求必须在超时后中止')

console.log('前端 Build ID 分类回归通过：变化、相同、非法和未知状态符合契约')
