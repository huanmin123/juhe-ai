import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const routerSource = readFileSync(fileURLToPath(new URL('../../router/index.ts', import.meta.url)), 'utf8')
const layoutSource = readFileSync(fileURLToPath(new URL('../../layouts/AppLayout.vue', import.meta.url)), 'utf8')

// 模型检测是常驻功能（2026-09-21 起前后端均无总开关）：菜单与路由必须
// 无条件存在，且不得恢复任何构建期 J3b 门禁。
assert.doesNotMatch(routerSource, /VITE_JUHE_AI_J3B_ENABLED/, '路由不得再读取 J3b 构建开关')
assert.doesNotMatch(routerSource, /requiresJ3b/, '路由不得再声明 J3b 依赖标注')
assert.doesNotMatch(layoutSource, /isJ3bUiEnabled|requiresJ3b/, '导航不得再按 J3b 开关过滤菜单')

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
