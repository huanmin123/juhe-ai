import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../router/routeLoadRecovery.ts', import.meta.url), 'utf8')

assert.match(source, /addEventListener\(['"]vite:preloadError['"]/, '异步组件 chunk 加载失败必须监听 Vite preload error')
assert.match(source, /recoverRouteAssetLoadError\(/, 'Vite preload error 必须复用前端资源恢复流程')
assert.match(source, /preventDefault\(\)/, '命中旧 chunk 后必须阻止 Vite 默认错误继续冒泡')

console.log('前端资源加载恢复回归通过：异步组件旧 chunk 失败会进入统一刷新恢复流程')
