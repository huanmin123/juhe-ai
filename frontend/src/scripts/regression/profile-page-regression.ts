import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import {
  normalizeProfileRedirectPath,
  PROFILE_PATH,
  requiredPasswordProfileLocation
} from '../../views/profile/profileNavigation'

const source = (relativePath: string) => readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), 'utf8')

const authApiSource = source('../../api/domains/auth.ts')
const identitySource = source('../../types/domain/identity.ts')
const headerSource = source('../../layouts/AppHeader.vue')
const layoutSource = source('../../layouts/AppLayout.vue')
const routerSource = source('../../router/index.ts')
const profileSource = source('../../views/profile/ProfileView.vue')

assert.match(authApiSource, /profile:\s*\(\)\s*=>[\s\S]*http\.get\('\/auth\/profile'\)/, 'Auth API 必须按需读取 /auth/profile')
assert.match(identitySource, /export interface CurrentUserProfile extends SystemAccountSummary/, '个人详情类型应扩展系统账户安全摘要字段')
assert.match(identitySource, /effectiveRequestLimits: EffectiveUserRequestLimits/, '个人详情必须包含最终生效的请求限制')

assert.match(headerSource, /key="profile">个人信息/, '头像菜单必须提供个人信息入口')
assert.doesNotMatch(headerSource, /key="display-name"|key="password"/, '头像菜单不得保留名称 / 密码双弹窗入口')
assert.match(layoutSource, /key="profile">个人信息/, '沉浸式移动菜单必须提供个人信息入口')
assert.doesNotMatch(layoutSource, /DisplayNameModal|ChangePasswordModal|openPasswordModal/, '应用壳不得继续挂载资料或密码弹窗')

assert.match(routerSource, /path: PROFILE_PATH[\s\S]*ProfileView\.vue/, '路由必须注册独立 /profile 页面')
assert.match(routerSource, /user\.mustChangePassword && to\.path !== PROFILE_PATH/, '强制改密用户必须由路由守卫收敛到个人信息页')
assert.match(profileSource, /api\.auth\.profile\(\)/, '页面必须从 self-only 详情接口读取资料')
assert.match(profileSource, /updateProfile\(\{ displayName \}\)/, '页面必须复用用户名称修改接口')
assert.match(profileSource, /changePassword\(/, '页面必须复用改密接口')
assert.match(profileSource, /@click="saveDisplayName"/, '用户名称保存必须使用可验证的显式点击提交')
assert.match(profileSource, /@click="savePassword"/, '密码修改必须使用可验证的显式点击提交')
for (const marker of ['系统角色', '图像生成', '账户状态', '请求限制', '每分钟', '每日', '每周', '每月', '最近登录', '账户创建', '资料更新']) {
  assert(profileSource.includes(marker), `个人信息页面缺少展示项：${marker}`)
}

const layoutDirectory = fileURLToPath(new URL('../../layouts/', import.meta.url))
assert.equal(existsSync(`${layoutDirectory}DisplayNameModal.vue`), false, '旧名称弹窗组件必须删除')
assert.equal(existsSync(`${layoutDirectory}ChangePasswordModal.vue`), false, '旧密码弹窗组件必须删除')

assert.equal(PROFILE_PATH, '/profile')
assert.equal(normalizeProfileRedirectPath('/my-stats?range=7d'), '/my-stats?range=7d')
assert.equal(normalizeProfileRedirectPath('https://example.com'), undefined)
assert.equal(normalizeProfileRedirectPath('//example.com/path'), undefined)
assert.equal(normalizeProfileRedirectPath('/\\example.com/path'), undefined)
assert.equal(normalizeProfileRedirectPath('/profile'), undefined)
assert.equal(normalizeProfileRedirectPath('/login'), undefined)
assert.deepEqual(requiredPasswordProfileLocation('/my-chat'), {
  path: '/profile',
  query: { section: 'security', required: '1', redirect: '/my-chat' }
})

console.log('PROFILE_PAGE_REGRESSION_OK')
