import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyListResult,
  ResponseInspectionPolicyProviderOption
} from '@/types/domain'
import { createResponseInspectionPolicyLoadCoordinator } from '@/views/response-inspection-policies/responseInspectionPolicyLoadCoordinator'
import {
  defaultResponseInspectionProviderCode,
  responseInspectionProviderSelectOptions
} from '@/views/response-inspection-policies/responseInspectionProviderOptions'
import {
  buildResponseInspectionPolicyPatch,
  hasResponseInspectionPolicyChanges,
  responseInspectionPolicyPayloadFromDetail
} from '@/views/response-inspection-policies/responseInspectionPolicyMutation'

const emptyList: ResponseInspectionPolicyListResult = { defaultRules: [], policies: [] }
const options: ResponseInspectionPolicyProviderOption[] = [
  { code: 'openai', name: 'OpenAI 兼容' },
  { code: 'anthropic', name: 'Anthropic' },
  { code: 'gemini', name: 'Gemini' }
]

const openAiProviderQuery = { protocolCode: 'openai' as const, scopeType: 'provider' as const }
const anthropicProviderQuery = { protocolCode: 'anthropic' as const, scopeType: 'provider' as const }

await verifyRequestMatrix()
await verifyListRaceIsolation()
await verifyModalIntentAndDetailRaceIsolation()
await verifyProviderOptionsIntentIsolationAndRetry()
verifyLocalProviderDefaultsAndSelectedOptions()
verifyFieldLevelMutationPayloads()
verifyViewUsesCoordinator()

console.log('响应检查策略前端渐进加载回归通过：请求矩阵、按需详情/options、缓存重试和竞态隔离均符合预期')

async function verifyRequestMatrix(): Promise<void> {
  const calls = { list: 0, detail: [] as string[], options: 0 }
  const coordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async () => {
      calls.list += 1
      return emptyList
    },
    detail: async (id) => {
      calls.detail.push(id)
      return detail(id)
    },
    providerOptions: async () => {
      calls.options += 1
      return options
    }
  })

  assert.deepEqual(calls, { list: 0, detail: [], options: 0 }, '创建页面协调器时不得预取任何资源')
  await coordinator.loadList()
  assert.deepEqual(calls, { list: 1, detail: [], options: 0 }, '首屏只能请求一次 overview 列表')

  const viewIntent = coordinator.beginModalIntent('policy_view')
  assert.equal((await coordinator.loadDetail(viewIntent, 'policy_view'))?.id, 'policy_view')
  assert.deepEqual(calls, { list: 1, detail: ['policy_view'], options: 0 }, '查看只能按需请求 detail，不得请求 provider options')

  const createIntent = coordinator.beginModalIntent()
  assert.deepEqual(calls, { list: 1, detail: ['policy_view'], options: 0 }, '打开创建弹窗不得请求 provider options')
  assert.deepEqual(await coordinator.loadProviderOptions(createIntent, openAiProviderQuery), options)
  assert.deepEqual(calls, { list: 1, detail: ['policy_view'], options: 1 }, '创建时只有展开供应商下拉才请求 provider options')

  coordinator.dispose()

  const editCalls = { list: 0, detail: 0, options: 0 }
  const editCoordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async () => {
      editCalls.list += 1
      return emptyList
    },
    detail: async (id) => {
      editCalls.detail += 1
      return detail(id)
    },
    providerOptions: async () => {
      editCalls.options += 1
      return options
    }
  })
  const editIntent = editCoordinator.beginModalIntent('policy_edit')
  const editDetail = await editCoordinator.loadDetail(editIntent, 'policy_edit')
  assert.equal(editDetail?.id, 'policy_edit')
  assert.deepEqual(editCalls, { list: 0, detail: 1, options: 0 }, '打开编辑弹窗只能请求 detail，不得预取 provider options')
  assert.deepEqual(await editCoordinator.loadProviderOptions(editIntent, openAiProviderQuery), options)
  assert.deepEqual(editCalls, { list: 0, detail: 1, options: 1 }, '编辑时只有展开供应商下拉才请求 provider options')
  assert.deepEqual(await editCoordinator.loadProviderOptions(editIntent, openAiProviderQuery), options)
  assert.deepEqual(editCalls, { list: 0, detail: 1, options: 1 }, '同一编辑 intent 重复展开不得重复请求 provider options')
  editCoordinator.dispose()
}

async function verifyListRaceIsolation(): Promise<void> {
  const requests: Array<Deferred<ResponseInspectionPolicyListResult>> = []
  const signals: AbortSignal[] = []
  const coordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async (signal) => {
      signals.push(signal)
      const request = deferred<ResponseInspectionPolicyListResult>()
      requests.push(request)
      return request.promise
    },
    detail: async (id) => detail(id),
    providerOptions: async () => options
  })

  const oldRequest = coordinator.loadList()
  const newRequest = coordinator.loadList()
  assert.equal(signals[0]?.aborted, true, '新 overview 请求必须中止旧请求')
  requests[1]?.resolve({ defaultRules: [], policies: [overview('new')] })
  assert.equal((await newRequest)?.policies[0]?.id, 'new')
  requests[0]?.resolve({ defaultRules: [], policies: [overview('old')] })
  assert.equal(await oldRequest, undefined, '迟到 overview 响应不得返回给页面覆盖新列表')
  coordinator.dispose()
}

async function verifyModalIntentAndDetailRaceIsolation(): Promise<void> {
  const requests = new Map<string, Deferred<ResponseInspectionPolicyDetail>>()
  const signals = new Map<string, AbortSignal>()
  const coordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async () => emptyList,
    detail: async (id, signal) => {
      signals.set(id, signal)
      const request = deferred<ResponseInspectionPolicyDetail>()
      requests.set(id, request)
      return request.promise
    },
    providerOptions: async () => options
  })

  const alphaIntent = coordinator.beginModalIntent('alpha')
  const alphaRequest = coordinator.loadDetail(alphaIntent, 'alpha')
  const betaIntent = coordinator.beginModalIntent('beta')
  const betaRequest = coordinator.loadDetail(betaIntent, 'beta')
  assert.equal(signals.get('alpha')?.aborted, true, '切换查看目标必须中止旧 detail 请求')
  requests.get('beta')?.resolve(detail('beta'))
  assert.equal((await betaRequest)?.id, 'beta')
  requests.get('alpha')?.resolve(detail('alpha'))
  assert.equal(await alphaRequest, undefined, '旧目标 detail 迟到后不得覆盖当前弹窗')

  const closeIntent = coordinator.beginModalIntent('close-me')
  const closeRequest = coordinator.loadDetail(closeIntent, 'close-me')
  coordinator.cancelModalIntent()
  assert.equal(signals.get('close-me')?.aborted, true, '关闭弹窗必须中止 detail 请求')
  requests.get('close-me')?.resolve(detail('close-me'))
  assert.equal(await closeRequest, undefined, '弹窗关闭后的 detail 迟到响应必须丢弃')
  coordinator.dispose()
}

async function verifyProviderOptionsIntentIsolationAndRetry(): Promise<void> {
  let calls = 0
  const queries: Array<{ protocolCode: string; scopeType: string; keyword?: string }> = []
  const requests: Array<Deferred<ResponseInspectionPolicyProviderOption[]>> = []
  const coordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async () => emptyList,
    detail: async (id) => detail(id),
    providerOptions: async (query) => {
      calls += 1
      queries.push(query)
      const request = deferred<ResponseInspectionPolicyProviderOption[]>()
      requests.push(request)
      return request.promise
    }
  })

  const firstIntent = coordinator.beginModalIntent()
  const first = coordinator.loadProviderOptions(firstIntent, openAiProviderQuery)
  const sameFlight = coordinator.loadProviderOptions(firstIntent, openAiProviderQuery)
  assert.equal(calls, 1, '同一页面的并发 options 请求必须 singleflight')
  requests[0]?.resolve(options)
  assert.deepEqual(await first, options)
  assert.deepEqual(await sameFlight, options)
  assert.deepEqual(await coordinator.loadProviderOptions(firstIntent, openAiProviderQuery), options)
  assert.equal(calls, 1, '同一 modal intent 成功后可以复用本次 options 结果')

  const secondIntent = coordinator.beginModalIntent('edit-after-create')
  const second = coordinator.loadProviderOptions(secondIntent, anthropicProviderQuery)
  assert.equal(calls, 2, '不同协议必须使用独立 provider options 缓存')
  requests[1]?.resolve(options)
  assert.deepEqual(await second, options)
  const cachedIntent = coordinator.beginModalIntent('cached-openai')
  assert.deepEqual(await coordinator.loadProviderOptions(cachedIntent, openAiProviderQuery), options)
  assert.equal(calls, 2, '同 protocol/scope/keyword 的成功结果必须跨 modal intent 复用')
  const searched = coordinator.loadProviderOptions(cachedIntent, { ...openAiProviderQuery, keyword: 'open' })
  assert.equal(calls, 3, '不同 keyword 必须使用独立 provider options 缓存键')
  requests[2]?.resolve([{ code: 'openai', name: 'OpenAI 兼容' }])
  assert.deepEqual(await searched, [{ code: 'openai', name: 'OpenAI 兼容' }])
  assert.deepEqual(queries, [openAiProviderQuery, anthropicProviderQuery, { ...openAiProviderQuery, keyword: 'open' }], 'loader 必须收到完整 protocol/scope/keyword 查询矩阵')
  coordinator.dispose()

  const raceRequests: Array<Deferred<ResponseInspectionPolicyProviderOption[]>> = []
  const raceSignals: AbortSignal[] = []
  const raceCoordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async () => emptyList,
    detail: async (id) => detail(id),
    providerOptions: async (_query, signal) => {
      raceSignals.push(signal)
      const request = deferred<ResponseInspectionPolicyProviderOption[]>()
      raceRequests.push(request)
      return request.promise
    }
  })
  const staleIntent = raceCoordinator.beginModalIntent()
  const stale = raceCoordinator.loadProviderOptions(staleIntent, openAiProviderQuery)
  const fresh = raceCoordinator.loadProviderOptions(staleIntent, anthropicProviderQuery)
  assert.equal(raceSignals[0]?.aborted, true, '同一弹窗切换协议必须中止旧 options 请求')
  assert.equal(raceRequests.length, 2, '不同协议不得复用旧协议的在途 options')
  raceRequests[0]?.resolve([{ code: 'stale', name: 'Stale' }])
  assert.equal(await stale, undefined, '旧 intent 的 options 迟到响应必须丢弃')
  raceRequests[1]?.resolve(options)
  assert.deepEqual(await fresh, options, '当前协议必须接收自己的 options 响应')
  raceCoordinator.dispose()

  let retryCalls = 0
  const retryCoordinator = createResponseInspectionPolicyLoadCoordinator({
    list: async () => emptyList,
    detail: async (id) => detail(id),
    providerOptions: async () => {
      retryCalls += 1
      if (retryCalls === 1) throw new Error('expected options failure')
      return options
    }
  })
  const retryIntent = retryCoordinator.beginModalIntent()
  await assert.rejects(retryCoordinator.loadProviderOptions(retryIntent, openAiProviderQuery), /expected options failure/)
  assert.deepEqual(await retryCoordinator.loadProviderOptions(retryIntent, openAiProviderQuery), options)
  assert.equal(retryCalls, 2, 'provider options 失败不得被缓存，当前操作必须可重试')
  retryCoordinator.dispose()
}

function verifyLocalProviderDefaultsAndSelectedOptions(): void {
  assert.equal(defaultResponseInspectionProviderCode([], 'openai'), 'gpt', 'OpenAI 协议在 options 未加载时必须使用本地 GPT 默认值')
  assert.equal(defaultResponseInspectionProviderCode([], 'anthropic'), 'anthropic', 'Anthropic 协议在 options 未加载时必须使用本地默认值')
  assert.equal(defaultResponseInspectionProviderCode([], 'gemini'), 'gemini', 'Gemini 协议在 options 未加载时必须使用本地默认值')
  assert.equal(defaultResponseInspectionProviderCode([], 'openai', true), '', '远程 options 已确认当前协议没有可用供应商时不得继续提交本地默认代码')
  assert.equal(
    defaultResponseInspectionProviderCode([{ code: 'custom-openai', name: 'Custom' }], 'openai'),
    'custom-openai',
    '远程 options 不含本地默认值时必须回落到当前协议首个可用供应商'
  )
  assert.deepEqual(
    responseInspectionProviderSelectOptions([], { code: 'anthropic', name: 'Anthropic' }),
    [{ label: 'Anthropic', value: 'anthropic' }],
    'options 未加载时必须保留创建或编辑表单的已选供应商回显'
  )
  assert.deepEqual(
    responseInspectionProviderSelectOptions(options, { code: 'custom-selected', name: '已选自定义供应商' }).at(-1),
    { label: '已选自定义供应商', value: 'custom-selected' },
    '远程候选不含当前值时不得清空用户已选供应商'
  )
}

function verifyFieldLevelMutationPayloads(): void {
  const baseline = responseInspectionPolicyPayloadFromDetail(detail('mutation'))
  assert.deepEqual(buildResponseInspectionPolicyPatch(baseline, structuredClone(baseline)), {}, '同值表单必须生成空 patch')
  assert.equal(hasResponseInspectionPolicyChanges({}), false, '空 patch 必须被识别为 no-op')
  assert.deepEqual(
    buildResponseInspectionPolicyPatch(baseline, { ...structuredClone(baseline), priority: 9 }),
    { priority: 9 },
    '单字段修改只能提交目标字段'
  )
  assert.deepEqual(
    buildResponseInspectionPolicyPatch(baseline, { ...structuredClone(baseline), notes: undefined }),
    { notes: null },
    '清空备注必须显式提交 null'
  )
  assert.deepEqual(
    buildResponseInspectionPolicyPatch(
      { ...structuredClone(baseline), scopeType: 'provider', providerCode: 'gpt' },
      { ...structuredClone(baseline), scopeType: 'protocol', providerCode: undefined }
    ),
    { scopeType: 'protocol', providerCode: null },
    '切换到协议层必须显式清空旧供应商'
  )
}

function verifyViewUsesCoordinator(): void {
  const source = readFileSync(resolve('../frontend/src/views/response-inspection-policies/ResponseInspectionPoliciesView.vue'), 'utf8')
  const modalSource = readFileSync(resolve('../frontend/src/views/response-inspection-policies/ResponseInspectionPolicyFormModal.vue'), 'utf8')
  const apiSource = readFileSync(resolve('../frontend/src/api/domains/responseInspectionPolicies.ts'), 'utf8')
  assert.match(source, /onMounted\(loadPolicies\)/, '页面首屏必须从 overview loader 启动')
  assert.match(source, /list:\s*\(signal\)[\s\S]{0,180}responseInspectionPolicies\.list\(\{ signal \}\)/, 'overview 请求必须受协调器 AbortSignal 管理')
  const createFunction = functionSource(source, 'function openCreate')
  assert.match(createFunction, /modalOpen\.value = true/, '创建操作必须直接打开弹窗')
  assert.doesNotMatch(createFunction, /providerOptions|loadProviderOptions/, '打开创建弹窗不得加载 provider options')
  const viewFunction = functionSource(source, 'async function openView')
  assert.match(viewFunction, /loadPolicyDetailForIntent\(intent, policy\.id\)/, '查看操作必须按需加载 detail')
  assert.doesNotMatch(viewFunction, /providerOptions|loadProviderOptions/, '查看操作不得加载 provider options')
  const editFunction = functionSource(source, 'async function openEdit')
  assert.match(editFunction, /loadPolicyDetailForIntent\(intent, policy\.id\)/, '编辑操作必须加载必要 detail')
  assert.doesNotMatch(editFunction, /Promise\.all|providerOptions|loadProviderOptions/, '打开编辑弹窗不得并行或提前加载 provider options')
  const saveFunction = functionSource(source, 'async function savePolicy')
  assert.match(saveFunction, /buildResponseInspectionPolicyPatch\(editingBaseline\.value, payload\)/, '编辑保存必须按详情基线生成字段差量')
  assert.match(saveFunction, /expectedUpdatedAt: editingExpectedUpdatedAt\.value/, '编辑 PATCH 必须提交 CAS 版本')
  assert.match(saveFunction, /hasResponseInspectionPolicyChanges\(patch\)/, '编辑无变化必须在前端阻止请求')
  assert.doesNotMatch(saveFunction, /loadPolicies\(/, '保存成功后必须本地合并，不得重拉列表')
  assert.match(source, /responseInspectionPolicies\.update\(targetId, \{/, '编辑保存必须走字段级 update API')
  assert.match(apiSource, /update:[\s\S]{0,220}http\.patch\(`/, '响应检查策略更新 API 必须使用 PATCH')
  assert.doesNotMatch(apiSource, /update:[\s\S]{0,220}http\.put\(`/, '响应检查策略更新 API 不得保留 PUT 全量替换')
  const removeFunction = functionSource(source, 'async function removePolicy')
  assert.doesNotMatch(removeFunction, /loadPolicies\(/, '删除成功后必须本地移除，不得重拉列表')
  const dropdownFunction = functionSource(source, 'async function loadProviderOptionsOnDropdown')
  assert.match(dropdownFunction, /if \(!open\)[\s\S]*cancelActiveProviderOptionsRequest\(\)/, '关闭下拉必须取消在途 provider options 请求')
  assert.match(dropdownFunction, /await loadProviderOptions\(query\)/, '只有供应商下拉展开后才加载当前上下文 provider options')
  const optionsFunction = functionSource(source, 'async function loadProviderOptions')
  assert.match(optionsFunction, /query\.scopeType !== 'provider'/, '协议层不得发起 provider options 请求')
  assert.match(optionsFunction, /loadCoordinator\.loadProviderOptions\(intent, query\)/, 'provider options 必须携带 protocol/scope/keyword 查询')
  assert.equal((optionsFunction.match(/message\.error\(/g) ?? []).length, 1, '同一 provider options 请求只能在一个 UI 边界提示一次错误')
  const contextFunction = functionSource(source, 'function handleProviderOptionsContextChange')
  assert.match(contextFunction, /cancelActiveProviderOptionsRequest\(\)/, '切换协议或层级必须取消旧 provider options 请求')
  assert.match(contextFunction, /clearProviderOptionsPresentation\(\)/, '切换协议或层级必须清空旧协议候选')
  const searchFunction = functionSource(source, 'function scheduleProviderOptionsSearch')
  assert.match(searchFunction, /setTimeout\([\s\S]*loadProviderOptions\(query\)/, '供应商搜索必须防抖后按需请求')
  assert.match(source, /:provider-options-loading="providerOptionsLoading"/, '父层必须向供应商下拉传递 loading 状态')
  assert.match(source, /:provider-options-ready="providerOptionsReady"/, '父层必须区分 options 未加载与远程已确认为空')
  assert.match(source, /@provider-options-dropdown-visible-change="loadProviderOptionsOnDropdown"/, '父层必须监听供应商下拉展开事件')
  assert.match(source, /@provider-options-search="scheduleProviderOptionsSearch"/, '父层必须监听远程供应商搜索')
  assert.doesNotMatch(source, /default-provider-code/, '父层不得把 OpenAI 默认供应商硬编码传给所有协议')
  assert.match(modalSource, /:loading="providerOptionsLoading"/, '供应商下拉必须展示按需加载状态')
  assert.match(modalSource, /:filter-option="false"/, '供应商下拉必须使用服务端关键词过滤')
  assert.match(modalSource, /@dropdown-visible-change="handleProviderDropdownVisibleChange"/, '供应商下拉必须在展开时通知父层')
  assert.match(modalSource, /@search="handleProviderSearch"/, '供应商下拉搜索必须通知父层按需查询')
  assert.match(modalSource, /@change="handleProviderChange"/, '用户手动选择供应商后必须保护该选择')
  assert.match(modalSource, /responseInspectionProviderSelectOptions\([\s\S]{0,300}code: form\.providerCode/, '编辑已选供应商在 options 未加载时必须仍可显示名称')
  assert.match(modalSource, /watch\(\[\(\) => props\.providerOptions, \(\) => props\.providerOptionsReady\][\s\S]{0,600}form\.providerCode = defaultProviderCodeForProtocol\(\)/, 'options 返回后必须按当前协议补默认供应商或清空不可用本地默认值')
  assert.match(modalSource, /defaultResponseInspectionProviderCode\(props\.providerOptions, form\.protocolCode, props\.providerOptionsReady\)/, '协议切换必须区分本地默认与远程已确认为空')
  assert.match(apiSource, /providerOptions:[\s\S]{0,320}params, signal: options\?\.signal/, 'provider options API 必须携带 protocol/scope/keyword 与 AbortSignal')
  const mountedFunction = functionSource(source, 'async function loadPolicies')
  assert.doesNotMatch(mountedFunction, /\.detail\(|providerOptions|loadProviderOptions/, '首屏 overview loader 不得夹带 detail 或 provider options')
}

function overview(id: string) {
  return {
    id,
    defaultRule: false,
    editable: true,
    name: id,
    enabled: true,
    priority: 1,
    scopeType: 'protocol' as const,
    protocolCode: 'openai' as const,
    action: 'observe' as const,
    updatedAt: '2026-07-23T00:00:00.000Z'
  }
}

function detail(id: string): ResponseInspectionPolicyDetail {
  const item = overview(id)
  return {
    id: item.id,
    name: item.name,
    enabled: item.enabled,
    priority: item.priority,
    scopeType: item.scopeType,
    protocolCode: item.protocolCode,
    action: item.action,
    updatedAt: item.updatedAt,
    match: { outputTextIncludes: [id] },
    notes: `${id} notes`
  }
}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}

function functionSource(source: string, signature: string): string {
  const start = source.indexOf(signature)
  assert.notEqual(start, -1, `必须找到 ${signature}`)
  const next = source.slice(start + signature.length).search(/\n(?:async\s+)?function\s+/)
  return source.slice(start, next < 0 ? undefined : start + signature.length + next)
}
