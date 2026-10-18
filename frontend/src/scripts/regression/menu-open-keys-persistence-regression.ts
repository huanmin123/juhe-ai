import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url), 'utf8')

const menuMode = source('composables/useMenuMode.ts')
assert.match(menuMode, /const menuOpenKeysStoragePrefix = 'juhe-ai:menu-open-keys:'/, '展开状态必须使用独立 localStorage 命名空间')
assert.match(
  menuMode,
  /export function readMenuOpenKeysPreference[\s\S]*?menuOpenKeysStorageKey\(user, mode\)[\s\S]*?JSON\.parse[\s\S]*?typeof key === 'string' && key\.startsWith\('group:'\)/,
  '读取必须按「用户 + 菜单模式」命名空间取值，且只接受 group: 前缀字符串键'
)
assert.match(
  menuMode,
  /export function saveMenuOpenKeysPreference[\s\S]*?if \(!user\) return[\s\S]*?try \{[\s\S]*?setItem[\s\S]*?\} catch \{/,
  '保存必须容忍未登录与禁用本地存储（try/catch 静默降级为会话内生效）'
)

const sidebar = source('layouts/AppSidebar.vue')
assert.doesNotMatch(sidebar, /menuOpenKeys = ref/, '侧栏不得自持展开状态副本，唯一状态源必须在布局层')
assert.match(sidebar, /:open-keys="openKeys"/, '侧栏展开键必须直接受控于 props.openKeys（桌面侧栏与移动抽屉同源）')
assert.match(sidebar, /emit\('open-change', keys\.map\(\(key\) => String\(key\)\)\)/, '侧栏必须把用户手动开合回传给布局层')

const layout = source('layouts/AppLayout.vue')
assert.match(layout, /:open-keys="menuOpenKeys"/, '布局层必须把状态源传给侧栏')
assert.match(layout, /@open-change="handleMenuOpenChange"/, '布局层必须接收侧栏开合回传')
assert.match(
  layout,
  /function restoreMenuOpenKeys[\s\S]*?readMenuOpenKeysPreference\(currentUser\.value, appMenuMode\.value\)[\s\S]*?knownGroupKeys\.has\(key\)/,
  '恢复必须按当前用户与菜单模式读取，并过滤菜单重组后已不存在的分组键'
)
assert.match(
  layout,
  /const activeKey = activeMenuGroupKey\(\)[\s\S]*?menuOpenKeys\.value = activeKey \? \[\.\.\.new Set\(\[\.\.\.persisted, activeKey\]\)\] : persisted/,
  '恢复必须并入当前路由所在组，保证选中项始终可见'
)
assert.match(layout, /watch\(menuOpenKeys, \(keys\) => \{[\s\S]*?saveMenuOpenKeysPreference\(currentUser\.value, appMenuMode\.value, keys\)/, '展开状态变化必须即时持久化')
assert.match(
  layout,
  /watch\(activeMenuGroupKey, \(activeKey\) => \{[\s\S]*?menuOpenKeys\.value = \[\.\.\.menuOpenKeys\.value, activeKey\]/,
  '路由切换必须把新所在组并入展开集且不得收起用户手动展开的其他组'
)

console.log('菜单组展开状态持久化回归通过：用户+模式命名空间读写、布局层唯一状态源、失效组键过滤、路由组并集与选中项可见')
