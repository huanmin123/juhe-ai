import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import {
  DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS,
  DEFAULT_EXTERNAL_INTEGRATION_SELECTED_SCOPES,
  buildSourcePatch,
  createSourceFormFromRecord
} from '../../views/external-integration-sources/externalSourceFormModel'

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `未找到片段起点：${start}`)
  assert.notEqual(endIndex, -1, `未找到片段终点：${end}`)
  return source.slice(startIndex, endIndex)
}

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
const groupModalSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupEditModal.vue', import.meta.url)), 'utf8')
const externalSourcesViewSource = readFileSync(fileURLToPath(new URL('../../views/external-integration-sources/ExternalIntegrationSourcesView.vue', import.meta.url)), 'utf8')
const externalSourceModalSource = readFileSync(fileURLToPath(new URL('../../views/external-integration-sources/ExternalSourceFormModal.vue', import.meta.url)), 'utf8')
const externalSourceFormSource = readFileSync(fileURLToPath(new URL('../../views/external-integration-sources/externalSourceFormModel.ts', import.meta.url)), 'utf8')

const groupCreateSource = sourceBetween(groupsViewSource, 'function openCreate()', 'function openEdit')
assert.doesNotMatch(groupCreateSource, /loadGroupOptions|groupsApi\.(?:detail|options)/, '打开新建分组弹窗不得预取供应商候选')

const groupEditSource = sourceBetween(groupsViewSource, 'function openEdit', 'const saveGroup')
assert.match(groupEditSource, /if \(group\.groupType !== 'high_concurrency'\)[\s\S]*applyGroupToForm\(\{\s*\.\.\.group,\s*accessType: target\.accessType\s*\}\)[\s\S]*modalOpen\.value = true[\s\S]*return/, '普通分组编辑必须直接使用列表行并保留访问类型，不得读取详情')
assert.match(groupEditSource, /groupsApi\.editBasicDetail\(group\.id/, '仅高并发分组读取专用编辑投影，不得读取完整分组摘要')
assert.doesNotMatch(groupEditSource, /groupsApi\.detail\(group\.id/, '编辑分组不得读取包含用量、账户 ID 和授权详情的完整摘要')
assert.match(groupEditSource, /applyGroupToForm\(\{\s*\.\.\.detail,\s*accessType: target\.accessType\s*\}\)/, '编辑分组必须使用必要详情并保留访问类型填充表单')
assert.doesNotMatch(groupEditSource, /loadGroupOptions/, '打开编辑分组弹窗不得预取供应商候选')
assert.match(groupEditSource, /requestId !== groupEditRequestId/, '分组编辑详情迟到响应不得打开旧弹窗')
assert.match(groupsViewSource, /useResponsivePagedList<GroupListItem/, '分组列表必须使用 Node 轻量列表 DTO，而不是伪装成完整详情')
assert.doesNotMatch(groupsViewSource, /updateGroupItems[\s\S]{0,250}\.\.\.updated/, '编辑响应不得把详情字段临时灌回轻量列表，避免授权限额刷新前后闪变')

assert.match(groupModalSource, /@dropdown-visible-change="emit\('provider-dropdown-visible-change', \$event\)"/, '供应商下拉必须暴露展开事件')
assert.match(groupModalSource, /:loading="providerOptionsLoading"/, '供应商下拉必须显示按需请求的加载状态')
assert.match(groupsViewSource, /@provider-dropdown-visible-change="handleProviderOptionsDropdown"/, '分组页面必须接收供应商下拉展开事件')
assert.match(groupsViewSource, /function handleProviderOptionsDropdown\(open: boolean\): void \{\s*if \(open\) void loadGroupOptions\(\)\s*\}/, '仅展开供应商下拉时才加载远程候选')
assert.match(groupsViewSource, /selectedCode[\s\S]*options\.some[\s\S]*value: selectedCode/, '供应商候选必须补齐列表行中的已选值')
const groupOptionLoadSource = sourceBetween(groupsViewSource, 'async function loadGroupOptions', 'function invalidateGroupOptions')
assert.match(groupOptionLoadSource, /groupOptionsLoadingPromise/, '同一分组弹窗的供应商候选请求必须 singleflight')
assert.match(groupOptionLoadSource, /requestId !== groupOptionsRequestId/, '迟到供应商候选不得覆盖当前身份状态')
assert.equal((groupOptionLoadSource.match(/message\.error\(/g) ?? []).length, 1, '同一供应商候选失败只能提示一次')

const externalSourceEditSource = sourceBetween(externalSourcesViewSource, 'function openEditSource', 'async function saveSource')
assert.match(externalSourceEditSource, /createSourceFormFromRecord\(record\)/, '编辑外部来源必须直接使用列表行填充表单')
assert.doesNotMatch(externalSourceEditSource, /externalIntegrationSources\.detail|\.detail\(/, '打开外部来源编辑弹窗不得重复读取详情')
assert.match(externalSourceFormSource, /createSourceFormFromRecord\(record: ExternalIntegrationSourceListItem\)/, '外部来源表单映射必须显式接受列表 DTO')
const externalSourceMounted = sourceBetween(externalSourcesViewSource, 'onMounted(() =>', 'function loadScopes')
assert.doesNotMatch(externalSourceMounted, /loadScopes\(/, '外部来源页面首屏不得预取 scope 常量')
const externalScopeLoadSource = sourceBetween(externalSourcesViewSource, 'function loadScopes', 'function handleScopeOptionsDropdown')
assert.match(externalScopeLoadSource, /activeScopeOptionsRequest/, 'scope 候选请求必须 singleflight')
assert.match(externalScopeLoadSource, /requestId !== scopeOptionsRequestId/, 'scope 候选迟到响应不得覆盖当前页面')
assert.match(externalSourcesViewSource, /function handleScopeOptionsDropdown\(open: boolean\): void \{\s*if \(open\) void loadScopes\(\)\s*\}/, 'scope 候选只能由真实下拉展开触发')
assert.match(externalSourceModalSource, /@dropdown-visible-change="emit\('scope-options-dropdown-visible-change', \$event\)"/, 'scope 下拉必须暴露真实展开事件')
assert.match(externalSourceModalSource, /:loading="scopeOptionsLoading"/, 'scope 下拉必须显示按需请求状态')
assert.match(externalSourcesViewSource, /availableScopeOptions[\s\S]*sourceForm\.scopes[\s\S]*label: value/, '编辑外部来源必须在远程 scope 候选未加载时保留已选值')
assert.match(externalSourceFormSource, /DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS/, '公开接口 scope 常量必须提供本地默认值')
assert.equal(DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS.length, 16, '本地 scope 标签字典必须覆盖 Node 的 16 个公开接口 scope')
assert.equal(new Set(DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS.map((item) => item.value)).size, 16, '本地 scope 标签字典不得包含重复值')
assert.deepEqual(DEFAULT_EXTERNAL_INTEGRATION_SELECTED_SCOPES, ['juhe_ai_public:group_list:read'], '新增来源默认 scope 必须与完整标签字典独立维护')
const externalSourceRecord = {
  id: 'extsrc_1',
  name: '外部来源',
  status: 'active' as const,
  scopes: ['juhe_ai_public:group_list:read'],
  rateLimits: [{ windowSeconds: 60, maxRequests: 10 }],
  notes: '原备注',
  isBuiltIn: false
}
assert.deepEqual(buildSourcePatch(createSourceFormFromRecord(externalSourceRecord), externalSourceRecord), {}, '未修改表单不得生成 PATCH 字段')
const changedExternalSourceForm = createSourceFormFromRecord(externalSourceRecord)
changedExternalSourceForm.notes = '新备注'
assert.deepEqual(buildSourcePatch(changedExternalSourceForm, externalSourceRecord), { notes: '新备注' }, '编辑保存只应发送实际修改字段')
const externalSourceSaveSource = sourceBetween(externalSourcesViewSource, 'async function saveSource', 'function addRateLimit')
assert.match(externalSourceSaveSource, /buildSourcePatch\(sourceForm, original\)/, '外部来源编辑保存必须按列表快照构造差异 PATCH')
assert.match(externalSourceSaveSource, /if \(!Object\.keys\(payload\)\.length\)[\s\S]*return/, '未修改外部来源不得发送 PATCH')
const externalListLoadSource = sourceBetween(externalSourcesViewSource, 'async function loadData', 'function isCurrentListRequest')
assert.match(externalListLoadSource, /requestId = \+\+listRequestId[\s\S]*requestSignature = JSON\.stringify\(params\)/, '外部来源列表必须为每次筛选、翻页和刷新绑定请求代次与参数签名')
assert.match(externalListLoadSource, /isCurrentListRequest\(requestId, requestSignature\)/, '外部来源列表写回和错误提示必须拒绝迟到响应')
assert.match(externalSourcesViewSource, /onBeforeUnmount\([\s\S]*listRequestId \+= 1/, '卸载外部来源页面必须作废列表请求')

console.log('Group and external source modal lazy-loading regression passed')
