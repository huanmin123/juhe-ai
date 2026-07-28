import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { authorizationCreateTargetGroupTip } from '../../views/authorizations/authorizationCreateState'
import { createAuthorizationOptionSingleflight } from '../../views/authorizations/authorizationOptionResource'

const viewSource = readFileSync(
  fileURLToPath(new URL('../../views/authorizations/AuthorizationsView.vue', import.meta.url)),
  'utf8'
)
const optionStateSource = readFileSync(
  fileURLToPath(new URL('../../views/authorizations/useAuthorizationOptionState.ts', import.meta.url)),
  'utf8'
)
const actionSource = readFileSync(
  fileURLToPath(new URL('../../views/authorizations/useAuthorizationActions.ts', import.meta.url)),
  'utf8'
)

const openCreateSource = sourceBetween(viewSource, 'function openCreateModal', 'function handleCreateOwnerChange')
assert.match(openCreateSource, /createModalOpen\.value = true/, '新增授权操作必须直接打开弹窗')
assert.doesNotMatch(openCreateSource, /loadCreate\w+Options\(/, '打开新增授权弹窗不得预取任何候选项')

const granteeTypeWatchSource = sourceBetween(
  viewSource,
  'watch(() => createForm.granteeType',
  'watch(() => createForm.resourceType'
)
assert.match(granteeTypeWatchSource, /resetCreateGranteeOptions\(\)/, '切换被授权人类型必须清除旧候选')
assert.doesNotMatch(granteeTypeWatchSource, /loadCreateGranteeOptions\(/, '切换被授权人类型不得预取新候选')

const resourceTypeWatchSource = sourceBetween(
  viewSource,
  'watch(() => createForm.resourceType',
  'watch(\n  () => [createForm.resourceType'
)
assert.match(resourceTypeWatchSource, /resetCreateResourceOptions\(\)/, '切换资源类型必须清除旧候选')
assert.doesNotMatch(resourceTypeWatchSource, /loadCreateResourceOptions\(/, '切换资源类型不得预取新候选')

const targetGroupWatchSource = sourceBetween(
  viewSource,
  'watch(\n  () => [createForm.resourceType',
  'async function loadMetaData'
)
assert.match(targetGroupWatchSource, /resetCreateTargetGroupState\(\)/, '目标分组上下文变化必须清除旧候选')
assert.doesNotMatch(targetGroupWatchSource, /loadCreateTargetGroupOptions\(/, '目标分组上下文满足条件时也不得自动请求')

const ownerChangeSource = sourceBetween(viewSource, 'function handleCreateOwnerChange', 'function handleCreateOwnerDropdown')
assert.match(ownerChangeSource, /resetCreateResourceOptions\(\)/, '切换授权人必须清除旧资源候选')
assert.match(ownerChangeSource, /resetCreateGranteeOptions\(\)/, '切换授权人必须清除旧被授权人候选')
assert.doesNotMatch(ownerChangeSource, /loadCreate\w+Options\(/, '切换授权人不得预取候选项')

const filterOwnerChangeSource = sourceBetween(viewSource, 'function handleFilterOwnerChange', 'function handleResourceTypeChange')
assert.match(filterOwnerChangeSource, /resetFilterResource\(\)/, '切换筛选归属用户必须清除旧资源选择')
assert.match(filterOwnerChangeSource, /resetFilterResourceOptions\(\)/, '切换筛选归属用户必须清除旧候选')
assert.doesNotMatch(filterOwnerChangeSource, /loadFilterResourceOptions\(/, '切换筛选归属用户不得预取资源候选')

const filterResourceTypeChangeSource = sourceBetween(viewSource, 'function handleResourceTypeChange', 'function resetFilters')
assert.match(filterResourceTypeChangeSource, /resetFilterResourceOptions\(\)/, '切换筛选资源类型必须清除旧候选')
assert.doesNotMatch(filterResourceTypeChangeSource, /loadFilterResourceOptions\(/, '切换筛选资源类型不得预取资源候选')

assertDropdownLoads('handleCreateOwnerDropdown', 'handleCreateOwnerSearch', 'loadCreateOwnerOptions')
assertDropdownLoads('handleCreateResourceDropdown', 'handleCreateResourceSearch', 'loadCreateResourceOptions')
assertDropdownLoads('handleCreateGranteeDropdown', 'handleCreateGranteeSearch', 'loadCreateGranteeOptions')
assertDropdownLoads('handleCreateTargetGroupDropdown', 'handleCreateTargetGroupSearch', 'loadCreateTargetGroupOptions')

for (const [resetName, requestIdName, optionsName] of [
  ['resetCreateOwnerOptions', 'createOwnerUserRequestId', 'createOwnerUsers'],
  ['resetCreateResourceOptions', 'createOwnerResourceRequestId', 'createAccounts'],
  ['resetCreateGranteeOptions', 'createGranteeRequestId', 'createUsers'],
  ['resetCreateTargetGroupState', 'createTargetGroupRequestId', 'createTargetGroups']
] as const) {
  const resetSource = functionSource(optionStateSource, `function ${resetName}`)
  assert.match(resetSource, new RegExp(`${requestIdName} \\+= 1`), `${resetName} 必须使旧请求失效`)
  assert.match(resetSource, new RegExp(`${optionsName}\\.value = \\[\\]`), `${resetName} 必须清空旧候选`)
  assert.match(resetSource, /Loading\.value = false/, `${resetName} 必须结束旧请求的加载状态`)
}

const resetResourceOptionsSource = functionSource(optionStateSource, 'function resetCreateResourceOptions')
assert.match(resetResourceOptionsSource, /createGroups\.value = \[\]/, '切换资源上下文必须同时清空账户和分组候选')
const resetGranteeOptionsSource = functionSource(optionStateSource, 'function resetCreateGranteeOptions')
assert.match(resetGranteeOptionsSource, /createTeams\.value = \[\]/, '切换被授权人上下文必须同时清空用户和团队候选')
assert.match(resetGranteeOptionsSource, /createGranteeOptionsLoaded\.value = false/, '重置被授权人上下文必须恢复未加载状态')

const resetFilterResourceOptionsSource = functionSource(optionStateSource, 'function resetFilterResourceOptions')
assert.match(resetFilterResourceOptionsSource, /filterResourceRequestId \+= 1/, '切换筛选资源上下文必须使旧请求失效')
assert.match(resetFilterResourceOptionsSource, /filterResourceOptionsLoading\.value = false/, '切换筛选资源上下文必须结束旧加载状态')
const filterResourceLoadSource = functionSource(optionStateSource, 'async function loadFilterResourceOptions')
assert.match(filterResourceLoadSource, /selectedFilterOwnerSystemAccountId\.value === systemAccountId/, '筛选资源迟到响应必须校验请求发起时的归属用户')
assert.match(filterResourceLoadSource, /!filterResourceDisabled\.value/, '筛选资源迟到响应不得写回已禁用的下拉')
assert.match(filterResourceLoadSource, /isManagementView\.value/, '筛选资源迟到响应不得跨页面权限上下文写回')

const targetGroupLoadSource = functionSource(optionStateSource, 'async function loadCreateTargetGroupOptions')
assert.match(targetGroupLoadSource, /selectDefaultCreateTargetGroup\(nextGroups\)/, '目标分组下拉按需加载后必须沿用默认分组选择')
assert.match(targetGroupLoadSource, /createTargetGroupOptionsLoaded\.value = true/, '目标分组首次成功加载后必须标记真实空态可见')
assert.doesNotMatch(authorizationCreateTargetGroupTip(0, false), /暂无可选/, '目标分组尚未加载时不得误报为空')
assert.match(authorizationCreateTargetGroupTip(0, true), /暂无可选/, '目标分组成功加载为空后必须显示真实空态')

assert.match(viewSource, /:grantee-options-loaded="createGranteeOptionsLoaded"/, '被授权对象下拉必须区分未加载和已加载为空')

assert.match(viewSource, /@owner-dropdown="handleCreateOwnerDropdown"/, '授权人下拉必须保留展开事件')
assert.match(viewSource, /@resource-dropdown="handleCreateResourceDropdown"/, '资源下拉必须保留展开事件')
assert.match(viewSource, /@grantee-dropdown="handleCreateGranteeDropdown"/, '被授权人下拉必须保留展开事件')
assert.match(viewSource, /@target-group-dropdown="handleCreateTargetGroupDropdown"/, '目标分组下拉必须保留展开事件')
assert.match(viewSource, /@owner-search="handleCreateOwnerSearch"/, '授权人搜索必须保留输入加载入口')
assert.match(viewSource, /@resource-search="handleCreateResourceSearch"/, '资源搜索必须保留输入加载入口')
assert.match(viewSource, /@grantee-search="handleCreateGranteeSearch"/, '被授权人搜索必须保留输入加载入口')
assert.match(viewSource, /@target-group-search="handleCreateTargetGroupSearch"/, '目标分组搜索必须保留输入加载入口')

const createModalLifecycleSource = sourceBetween(
  viewSource,
  'watch(createModalOpen',
  'watch(expireModalOpen'
)
assert.match(createModalLifecycleSource, /if \(!open\) resetCreateOptionSearchState\(\)/, '关闭新增授权弹窗必须使全部候选请求和搜索定时器失效')

const resetCreateOptionSearchSource = sourceBetween(
  optionStateSource,
  'function resetCreateOptionSearchState',
  'function createOptionRequestKey'
)
assert.match(resetCreateOptionSearchSource, /createOptionSingleflight\.invalidate\(\)/, '重置新增授权候选时必须断开旧 singleflight 生命周期')
assert.match(resetCreateOptionSearchSource, /clearCreateOwnerSearchTimer\(\)/, '关闭新增授权弹窗必须清除授权人搜索定时器')
assert.match(resetCreateOptionSearchSource, /clearCreateResourceSearchTimer\(\)/, '关闭新增授权弹窗必须清除资源搜索定时器')
assert.match(resetCreateOptionSearchSource, /clearCreateGranteeSearchTimer\(\)/, '关闭新增授权弹窗必须清除被授权人搜索定时器')
assert.match(resetCreateOptionSearchSource, /clearCreateTargetGroupSearchTimer\(\)/, '关闭新增授权弹窗必须清除目标分组搜索定时器')

for (const [loaderName, nextLoaderName, selectedField] of [
  ['loadCreateOwnerOptions', 'loadCreateResourceOptions', 'ownerSystemAccountId'],
  ['loadCreateResourceOptions', 'loadCreateGranteeOptions', 'resourceId'],
  ['loadCreateGranteeOptions', 'loadCreateTargetGroupOptions', 'granteeId'],
  ['loadCreateTargetGroupOptions', 'loadFilterResourceOptions', 'targetGroupId']
] as const) {
  const loaderSource = sourceBetween(optionStateSource, `async function ${loaderName}`, `async function ${nextLoaderName}`)
  assert.match(loaderSource, /if \(!createModalOpen\.value\) return/, `${loaderName} 在弹窗关闭后不得发出请求`)
  assert.match(loaderSource, /createOptionSingleflight\.run\(/, `${loaderName} 的同作用域并发请求必须 singleflight`)
  assert.match(loaderSource, /authorizationRequestContext\.value === requestContext/, `${loaderName} 的迟到响应不得跨登录身份或页面写回`)
  assert.match(loaderSource, new RegExp(`createForm\\.${selectedField} === selectedId`), `${loaderName} 的迟到响应不得覆盖请求后发生的用户选择`)
  assert.match(loaderSource, /if \(!isCurrent\(\)\) return/, `${loaderName} 的迟到错误不得提示`)
}

const expireOpenSource = sourceBetween(
  actionSource,
  'async function openExpireModal',
  'function invalidateExpireDetailRequest'
)
assert.match(expireOpenSource, /requestToken = \+\+expireDetailRequestToken/, '到期编辑详情请求必须使用独立请求代次')
assert.match(expireOpenSource, /requestContext/, '到期编辑详情请求必须绑定登录身份和页面上下文')
assert.match(expireOpenSource, /resourceOwnerSystemAccountId/, '到期编辑详情请求必须绑定资源 owner')
assert.match(expireOpenSource, /item\.id/, '到期编辑详情请求必须绑定授权记录')
assert.match(expireOpenSource, /activeExpireDetailRequestSignature === requestSignature/, '到期编辑详情响应必须校验完整请求签名')
assert.ok((expireOpenSource.match(/if \(!isCurrent\(\)\) return/g) ?? []).length >= 3, '到期编辑的迟到响应、网络错误和结构错误都必须静默丢弃')

const expireModalLifecycleSource = sourceBetween(
  viewSource,
  'watch(expireModalOpen',
  'watch(authorizationRequestContext'
)
assert.match(expireModalLifecycleSource, /invalidateExpireDetailRequest\(\)/, '关闭到期编辑弹窗必须使详情请求失效')
const requestContextLifecycleSource = sourceBetween(
  viewSource,
  'watch(authorizationRequestContext',
  'async function loadMetaData'
)
assert.match(requestContextLifecycleSource, /invalidateExpireDetailRequest\(\)/, '登录身份或页面上下文切换必须使到期编辑详情请求失效')

await verifySingleflightLifecycle()

console.log('新增授权候选项按需加载回归通过')

async function verifySingleflightLifecycle(): Promise<void> {
  const singleflight = createAuthorizationOptionSingleflight()
  const firstGate = deferred<string>()
  let networkCalls = 0
  const first = singleflight.run('same-scope', async () => {
    networkCalls += 1
    return await firstGate.promise
  })
  const duplicate = singleflight.run('same-scope', async () => {
    networkCalls += 1
    return 'duplicate'
  })
  assert.equal(first, duplicate, '同一作用域和关键词的进行中请求必须复用同一个 Promise')
  assert.equal(networkCalls, 0, 'singleflight 网络任务应在微任务中统一启动')
  await Promise.resolve()
  assert.equal(networkCalls, 1, '同一作用域并发只能启动一次网络请求')

  singleflight.invalidate()
  const secondGate = deferred<string>()
  const reopened = singleflight.run('same-scope', async () => {
    networkCalls += 1
    return await secondGate.promise
  })
  assert.notEqual(reopened, first, '弹窗生命周期失效后不得复用关闭前的进行中请求')
  await Promise.resolve()
  assert.equal(networkCalls, 2, '新弹窗生命周期必须能重新发起请求')

  firstGate.resolve('old')
  await first
  assert.equal(singleflight.run('same-scope', async () => 'unexpected'), reopened, '旧请求结束不得删除新生命周期中的同键请求')
  secondGate.resolve('new')
  assert.equal(await reopened, 'new')
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve
  })
  return { promise, resolve }
}

function assertDropdownLoads(handlerName: string, nextHandlerName: string, loaderName: string): void {
  const source = sourceBetween(viewSource, `function ${handlerName}`, `function ${nextHandlerName}`)
  assert.match(source, /if \(open\)/, `${handlerName} 只能在下拉展开时加载`)
  assert.match(source, new RegExp(`void ${loaderName}\\(\\)`), `${handlerName} 必须触发对应候选加载`)
}

function functionSource(source: string, signature: string): string {
  const startIndex = source.indexOf(signature)
  assert.notEqual(startIndex, -1, `缺少源码函数：${signature}`)
  const nextFunctionIndex = source.slice(startIndex + signature.length).search(/\n(?:async\s+)?function\s+/)
  return source.slice(startIndex, nextFunctionIndex < 0 ? undefined : startIndex + signature.length + nextFunctionIndex)
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
