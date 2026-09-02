import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const routerSource = readFileSync(fileURLToPath(new URL('../../router/index.ts', import.meta.url)), 'utf8')
const layoutSource = readFileSync(fileURLToPath(new URL('../../layouts/AppLayout.vue', import.meta.url)), 'utf8')

assert.match(
  routerSource,
  /export const isJ3bUiEnabled = import\.meta\.env\.VITE_JUHE_AI_J3B_ENABLED === 'true'/,
  'J3b 页面能力必须默认关闭并只能由显式构建变量打开'
)

for (const path of ['/my-model-checks', '/model-checks']) {
  const routeStart = routerSource.indexOf(`path: '${path}'`)
  assert(routeStart >= 0, `必须保留模型检测路由定义：${path}`)
  const routeEnd = routerSource.indexOf('\n  },', routeStart)
  const routeSource = routerSource.slice(routeStart, routeEnd >= 0 ? routeEnd : undefined)
  assert.match(routeSource, /requiresJ3b:\s*true/, `${path} 必须声明依赖 Go J3b`)
}

assert.match(
  routerSource,
  /if \(to\.meta\.requiresJ3b && !isJ3bUiEnabled\) \{[\s\S]*return getPreferredEntryPath\(user\)/,
  'J3b 未启用时直接访问模型检测必须安全回到可用入口'
)
assert.match(
  layoutSource,
  /!item\.meta\?\.requiresJ3b \|\| isJ3bUiEnabled/,
  'J3b 未启用时导航不得展示模型检测入口'
)

console.log('model-check route availability regression passed')
