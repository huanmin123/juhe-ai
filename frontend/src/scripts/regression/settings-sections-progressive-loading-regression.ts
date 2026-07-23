import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { buildSettingsSectionRequestSignature, createSettingsSectionRequestGate } from '../../views/settings/settingsSectionRequestGate'

const sourceRoot = fileURLToPath(new URL('../..', import.meta.url))
const view = readFileSync(resolve(sourceRoot, 'views/settings/SettingsView.vue'), 'utf8')
const api = readFileSync(resolve(sourceRoot, 'api/domains/settings.ts'), 'utf8')

assert(!view.includes('api.settings.get()'), '设置页不得再请求完整 system settings')
assert(!view.includes('api.settings.global()'), '设置页品牌应走统一 section 契约')
assert(view.includes("Promise.all([loadSection('brand'), loadSection('gateway-core')])"), '首屏必须只主动加载品牌和高频网关 section')
assert(view.includes('IntersectionObserver'), '低频 section 必须由视口渐进触发')
assert(view.includes('hasActivated'), '设置页首次激活不得与 onMounted 重复请求')
assert(view.includes('changedPayload(sectionKey)'), '系统设置保存必须从 section 基线生成差异 payload')
assert(view.includes("if (!sectionReady[sectionKey]) continue"), '未加载 section 不得参与 reset 或 PATCH')
assert(view.includes('sectionBaselines[sectionKey]'), '每个 section 必须维护独立保存基线')
assert(view.includes('onDeactivated(() =>'), 'KeepAlive 停用时必须作废 section 请求')
assert(view.includes('onActivated(() =>'), 'KeepAlive 恢复时必须按需重载 section')
assert(view.includes('authState.revision.value'), 'section 请求签名必须包含 auth revision')
assert(view.includes('viewerId: viewer?.id'), 'section 请求签名必须包含 viewer id')
assert(view.includes('sectionRequestGate.isCurrent'), '迟到成功、失败和 finally 都必须经过 generation/signature 门禁')
assert(view.includes('for (const key of Object.keys(dirty)) responseValues[key] = current[key]'), 'ready section 重载不得覆盖 dirty 编辑')
assert(api.includes("http.get(`/settings/sections/${sectionKey}`)"), '前端 API 必须使用 section GET')
assert(api.includes("http.patch(`/settings/sections/${sectionKey}`, payload)"), '前端 API 必须使用 section PATCH')

const baseSignature = buildSettingsSectionRequestSignature({ sectionKey: 'brand', authRevision: 1, viewerId: 'admin-a', viewerRole: 'admin' })
assert.notEqual(baseSignature, buildSettingsSectionRequestSignature({ sectionKey: 'gateway-core', authRevision: 1, viewerId: 'admin-a', viewerRole: 'admin' }))
assert.notEqual(baseSignature, buildSettingsSectionRequestSignature({ sectionKey: 'brand', authRevision: 2, viewerId: 'admin-a', viewerRole: 'admin' }))
assert.notEqual(baseSignature, buildSettingsSectionRequestSignature({ sectionKey: 'brand', authRevision: 1, viewerId: 'admin-b', viewerRole: 'admin' }))

const gate = createSettingsSectionRequestGate()
const first = gate.begin('brand', baseSignature)
assert(gate.isCurrent(first, baseSignature))
const second = gate.begin('brand', baseSignature)
assert(!gate.isCurrent(first, baseSignature), '同 section 新 generation 必须淘汰迟到响应')
assert(gate.isCurrent(second, baseSignature))
gate.deactivate()
assert(!gate.isCurrent(second, baseSignature), 'deactivated 页面不得提交在途响应')
gate.activate()
const third = gate.begin('brand', baseSignature)
assert(gate.isCurrent(third, baseSignature), 'activated 页面应允许新 generation')
assert(!gate.isCurrent(third, buildSettingsSectionRequestSignature({ sectionKey: 'brand', authRevision: 2, viewerId: 'admin-a', viewerRole: 'admin' })), '旧 auth revision 响应不得提交')

console.log('frontend settings sections progressive loading regression passed')
