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
  assert(
    routerSource.includes(`path: '${path}'`),
    `必须保留模型检测路由定义：${path}`
  )
}

console.log('model-check route availability regression passed')
