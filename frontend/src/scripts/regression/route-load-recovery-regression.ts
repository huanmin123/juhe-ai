import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../router/routeLoadRecovery.ts', import.meta.url), 'utf8')

assert.match(source, /addEventListener\(['"]vite:preloadError['"]/, '异步组件 chunk 加载失败必须监听 Vite preload error')
assert.match(source, /recoverRouteAssetLoadError\(/, 'Vite preload error 必须复用前端资源恢复流程')
assert.match(source, /preventDefault\(\)/, '命中旧 chunk 后必须阻止 Vite 默认错误继续冒泡')
assert.match(source, /classifyFrontendBuild/, '首次资源失败必须读取清单并比较真实 Build ID')
assert.match(source, /loadRemoteFrontendBuildId/, '首次资源失败必须读取静态 Build ID 清单')
assert.match(source, /页面资源加载失败/, '同版本或未知版本必须使用中性标题')
assert.match(source, /正在重新加载页面，请稍候。/, '首次普通资源失败必须说明正在自动恢复')
assert.match(source, /自动恢复未成功，请手动刷新页面后重试。/, '短时间重复失败必须停止自动恢复')
assert.doesNotMatch(source, /message\.warning\(['"]检测到系统前端已更新/, '资源失败不得在版本确认前直接声称系统更新')
assert.match(source, /couldn.*resolve component/i, 'Vue Router 的组件解析包装错误必须归入同一次资源恢复')

console.log('前端资源加载恢复回归通过：异步组件旧 chunk 失败会进入统一刷新恢复流程')
