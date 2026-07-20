import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { classifyFrontendBuild } from '../../router/frontendBuildInfo'
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
const serverSource = readFileSync(new URL('../../../../backend/src/server.ts', import.meta.url), 'utf8')

assert.match(viteConfigSource, /__JUHE_AI_FRONTEND_BUILD_ID__/, 'Vite 必须注入当前页面 Build ID')
assert.match(viteConfigSource, /fileName:\s*['"]build-info\.json['"]/, 'Vite 必须输出静态 Build ID 清单')
assert.match(powerShellReleaseSource, /VITE_JUHE_AI_BUILD_ID\s*=\s*\$releaseSourceCommit/, 'PowerShell 发布必须注入冻结提交')
assert.match(shellReleaseSource, /VITE_JUHE_AI_BUILD_ID=["']?\$RELEASE_SOURCE_COMMIT/, 'POSIX 发布必须注入冻结提交')
assert.match(serverSource, /build-info\.json/, '后端静态服务必须显式设置 Build ID 清单缓存规则')

console.log('前端 Build ID 分类回归通过：变化、相同、非法和未知状态符合契约')
