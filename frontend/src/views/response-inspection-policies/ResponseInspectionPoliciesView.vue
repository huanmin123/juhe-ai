<template>
  <a-card class="page-card responsive-page-card response-policy-page">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索名称、协议、供应商或处置"
      :show-reset="Boolean(keyword.trim())"
      :refresh-loading="loading"
      @search="applySearch"
      @reset="resetSearch"
      @refresh="loadPolicies"
    >
      <template #actions>
        <a-button @click="guideOpen = true">
          <template #icon><question-circle-outlined /></template>
          配置指南
        </a-button>
        <a-button type="primary" @click="openCreate">新建策略</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponseInspectionPolicyList
      :loading="loading"
      :policies="filteredPolicies"
      :opening-policy-id="openingPolicyId"
      @delete="removePolicy"
      @edit="openEdit"
      @refresh="loadPolicies"
      @view="openView"
    />

    <ResponseInspectionPolicyFormModal
      v-model:open="modalOpen"
      :mode="modalMode"
      :policy="activePolicy"
      :saving="saving"
      :provider-options="providerOptions"
      :provider-options-loading="providerOptionsLoading"
      :provider-options-ready="providerOptionsReady"
      :default-priority="nextPriority()"
      @submit="savePolicy"
      @cancel="resetModal"
      @provider-options-context-change="handleProviderOptionsContextChange"
      @provider-options-dropdown-visible-change="loadProviderOptionsOnDropdown"
      @provider-options-search="scheduleProviderOptionsSearch"
    />

    <ResponseInspectionPolicyGuideModal
      v-model:open="guideOpen"
      title="响应检查策略配置指南"
      intro="这类策略检查上游返回的 JSON 或 SSE 流。协议层规则会作用到同协议账号；供应商层规则只作用到选中的同协议供应商。"
    />
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { api, type ResponseInspectionPolicyCreatePayload } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { extractApiErrorMessage } from '@/shared/apiError'
import { compareServerDateTime } from '@/shared/formatters'
import { stringOrFallback } from '@/shared/pageStateSanitizers'
import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyOverview,
  ResponseInspectionPolicyProviderOption
} from '@/types/domain'
import ResponseInspectionPolicyFormModal from './ResponseInspectionPolicyFormModal.vue'
import ResponseInspectionPolicyGuideModal from './ResponseInspectionPolicyGuideModal.vue'
import {
  responseInspectionPolicyActionText,
  responseInspectionPolicyProtocolText,
  responseInspectionPolicyScopeText
} from './responseInspectionPolicyDisplay'
import ResponseInspectionPolicyList from './ResponseInspectionPolicyList.vue'
import {
  createResponseInspectionPolicyLoadCoordinator,
  type ResponseInspectionPolicyModalIntent,
  type ResponseInspectionPolicyProviderOptionsQuery
} from './responseInspectionPolicyLoadCoordinator'
import {
  buildResponseInspectionPolicyPatch,
  hasResponseInspectionPolicyChanges,
  responseInspectionPolicyPayloadFromDetail
} from './responseInspectionPolicyMutation'

type PolicyModalMode = 'create' | 'edit' | 'view'

interface ResponseInspectionPoliciesPageState {
  keyword: string
}

const pageStateCache = usePageStateCache<ResponseInspectionPoliciesPageState>(undefined, defaultResponseInspectionPoliciesPageState, {
  sanitize: sanitizeResponseInspectionPoliciesPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const loading = ref(false)
const saving = ref(false)
const keyword = ref(initialPageState.keyword)
const providerOptions = ref<ResponseInspectionPolicyProviderOption[]>([])
const providerOptionsLoading = ref(false)
const providerOptionsReady = ref(false)
const defaultRules = ref<ResponseInspectionPolicyOverview[]>([])
const policies = ref<ResponseInspectionPolicyOverview[]>([])
const modalOpen = ref(false)
const modalMode = ref<PolicyModalMode>('create')
const guideOpen = ref(false)
const editingId = ref<string>()
const activePolicy = ref<ResponseInspectionPolicyDetail>()
const openingPolicyId = ref<string>()
const editingBaseline = ref<ResponseInspectionPolicyCreatePayload>()
const editingExpectedUpdatedAt = ref<string>()

const loadCoordinator = createResponseInspectionPolicyLoadCoordinator({
  list: (signal) => api.responseInspectionPolicies.list({ signal }),
  detail: (policyId, signal) => api.responseInspectionPolicies.detail(policyId, { signal }),
  providerOptions: (query, signal) => api.responseInspectionPolicies.providerOptions(query, { signal })
})
let activeListRequest: Promise<unknown> | undefined
let activeModalIntent: ResponseInspectionPolicyModalIntent | undefined
let activeProviderOptionsRequest: {
  request: Promise<ResponseInspectionPolicyProviderOption[] | undefined>
  query: ResponseInspectionPolicyProviderOptionsQuery
} | undefined
let providerOptionsSearchTimer: ReturnType<typeof setTimeout> | undefined

const allPolicies = computed(() => [...defaultRules.value, ...policies.value])
const filteredPolicies = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return allPolicies.value
  return allPolicies.value.filter((policy) => searchableText(policy).includes(text))
})

async function loadPolicies(): Promise<void> {
  const request = loadCoordinator.loadList()
  activeListRequest = request
  loading.value = true
  try {
    const result = await request
    if (!result || activeListRequest !== request) return
    defaultRules.value = result.defaultRules
    policies.value = result.policies
  } catch (error) {
    if (activeListRequest !== request) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略失败'))
  } finally {
    if (activeListRequest === request) {
      loading.value = false
      activeListRequest = undefined
    }
  }
}

function openCreate(): void {
  prepareModalIntent()
  editingId.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'create'
  modalOpen.value = true
}

async function openView(policy: ResponseInspectionPolicyOverview): Promise<void> {
  const intent = prepareModalIntent(policy.id)
  editingId.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'view'
  const detail = await loadPolicyDetailForIntent(intent, policy.id)
  if (!loadCoordinator.isCurrentModalIntent(intent, policy.id)) return
  openingPolicyId.value = undefined
  if (!detail) return
  activePolicy.value = detail
  modalOpen.value = true
}

async function openEdit(policy: ResponseInspectionPolicyOverview): Promise<void> {
  if (!policy.editable) {
    await openView(policy)
    return
  }
  const intent = prepareModalIntent(policy.id)
  editingId.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'edit'
  const detail = await loadPolicyDetailForIntent(intent, policy.id)
  if (!loadCoordinator.isCurrentModalIntent(intent, policy.id)) return
  openingPolicyId.value = undefined
  if (!detail) return
  if (!detail.updatedAt) {
    message.error('响应检查策略缺少版本，请刷新后重试')
    return
  }
  editingId.value = policy.id
  activePolicy.value = detail
  editingBaseline.value = responseInspectionPolicyPayloadFromDetail(detail)
  editingExpectedUpdatedAt.value = detail.updatedAt
  modalOpen.value = true
}

async function loadProviderOptionsOnDropdown(
  open: boolean,
  query: ResponseInspectionPolicyProviderOptionsQuery
): Promise<void> {
  clearProviderOptionsSearchTimer()
  if (!open) {
    cancelActiveProviderOptionsRequest()
    return
  }
  await loadProviderOptions(query)
}

function scheduleProviderOptionsSearch(query: ResponseInspectionPolicyProviderOptionsQuery): void {
  if (modalMode.value === 'view' || query.scopeType !== 'provider') return
  clearProviderOptionsSearchTimer()
  providerOptionsSearchTimer = setTimeout(() => {
    providerOptionsSearchTimer = undefined
    void loadProviderOptions(query)
  }, 250)
}

function handleProviderOptionsContextChange(): void {
  clearProviderOptionsSearchTimer()
  cancelActiveProviderOptionsRequest()
  clearProviderOptionsPresentation()
}

async function loadProviderOptions(query: ResponseInspectionPolicyProviderOptionsQuery): Promise<void> {
  if (modalMode.value === 'view' || query.scopeType !== 'provider') return
  const intent = activeModalIntent
  if (!intent || !loadCoordinator.isCurrentModalIntent(intent)) return
  const request = loadCoordinator.loadProviderOptions(intent, query)
  const activeRequest = { request, query }
  activeProviderOptionsRequest = activeRequest
  providerOptionsLoading.value = true
  try {
    const options = await request
    if (activeProviderOptionsRequest !== activeRequest || !loadCoordinator.isCurrentModalIntent(intent) || !options) return
    providerOptions.value = options
    providerOptionsReady.value = !query.keyword
  } catch (error) {
    if (activeProviderOptionsRequest !== activeRequest || !loadCoordinator.isCurrentModalIntent(intent)) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略供应商选项失败，请重试'))
  } finally {
    if (activeProviderOptionsRequest === activeRequest) {
      activeProviderOptionsRequest = undefined
      providerOptionsLoading.value = false
    }
  }
}

function resetModal(): void {
  loadCoordinator.cancelModalIntent()
  clearProviderOptionsSearchTimer()
  modalOpen.value = false
  openingPolicyId.value = undefined
  activeModalIntent = undefined
  activeProviderOptionsRequest = undefined
  clearProviderOptionsPresentation()
  editingId.value = undefined
  editingBaseline.value = undefined
  editingExpectedUpdatedAt.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'create'
}

async function savePolicy(payload: ResponseInspectionPolicyCreatePayload): Promise<void> {
  const targetId = editingId.value
  if (targetId && (!editingBaseline.value || !editingExpectedUpdatedAt.value)) {
    message.error('响应检查策略编辑版本已失效，请重新打开')
    return
  }
  const patch = targetId && editingBaseline.value
    ? buildResponseInspectionPolicyPatch(editingBaseline.value, payload)
    : undefined
  if (patch && !hasResponseInspectionPolicyChanges(patch)) {
    message.info('响应检查策略没有变化')
    return
  }
  saving.value = true
  try {
    let savedPolicy: ResponseInspectionPolicyOverview
    if (targetId) {
      savedPolicy = await api.responseInspectionPolicies.update(targetId, {
        expectedUpdatedAt: editingExpectedUpdatedAt.value as string,
        ...patch
      })
      message.success('响应检查策略已更新')
    } else {
      savedPolicy = await api.responseInspectionPolicies.create(payload)
      message.success('响应检查策略已创建')
    }
    upsertPolicyOverview(savedPolicy)
    resetModal()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存响应检查策略失败'))
  } finally {
    saving.value = false
  }
}

async function removePolicy(policy: ResponseInspectionPolicyOverview): Promise<void> {
  if (!policy.editable) return
  try {
    await api.responseInspectionPolicies.delete(policy.id)
    policies.value = policies.value.filter((item) => item.id !== policy.id)
    message.success('响应检查策略已删除')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除响应检查策略失败'))
  }
}

function applySearch(): void {
  keyword.value = keyword.value.trim()
}

function resetSearch(): void {
  keyword.value = ''
  pageStateCache.clear()
}

function defaultResponseInspectionPoliciesPageState(): ResponseInspectionPoliciesPageState {
  return { keyword: '' }
}

function sanitizeResponseInspectionPoliciesPageState(value: unknown, fallback: ResponseInspectionPoliciesPageState): ResponseInspectionPoliciesPageState {
  const source = value && typeof value === 'object' ? value as Partial<ResponseInspectionPoliciesPageState> : {}
  return {
    keyword: stringOrFallback(source.keyword, fallback.keyword)
  }
}

function snapshotPageState(): ResponseInspectionPoliciesPageState {
  return { keyword: keyword.value }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

function nextPriority(): number {
  const used = new Set(policies.value
    .map((policy) => policy.priority)
    .filter((priority): priority is number => Number.isInteger(priority) && priority > 0 && priority <= 9999))
  for (let priority = 1; priority <= 9999; priority += 1) {
    if (!used.has(priority)) return priority
  }
  return 9999
}

function searchableText(policy: ResponseInspectionPolicyOverview): string {
  return [
    policy.name,
    responseInspectionPolicyScopeText(policy),
    responseInspectionPolicyProtocolText(policy.protocolCode),
    policy.providerName,
    policy.providerCode,
    policy.defaultRule ? '默认' : '自定义',
    String(policy.priority),
    policy.enabled ? '启用' : '停用',
    responseInspectionPolicyActionText(policy.action)
  ].filter(Boolean).join(' ').toLowerCase()
}

async function loadPolicyDetailForIntent(
  intent: ResponseInspectionPolicyModalIntent,
  policyId: string
): Promise<ResponseInspectionPolicyDetail | undefined> {
  try {
    return await loadCoordinator.loadDetail(intent, policyId)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略详情失败，请重试'))
    return undefined
  }
}

function prepareModalIntent(policyId?: string): ResponseInspectionPolicyModalIntent {
  modalOpen.value = false
  activePolicy.value = undefined
  editingId.value = undefined
  editingBaseline.value = undefined
  editingExpectedUpdatedAt.value = undefined
  activeProviderOptionsRequest = undefined
  clearProviderOptionsSearchTimer()
  clearProviderOptionsPresentation()
  openingPolicyId.value = policyId
  activeModalIntent = loadCoordinator.beginModalIntent(policyId)
  return activeModalIntent
}

function upsertPolicyOverview(overview: ResponseInspectionPolicyOverview): void {
  if (overview.defaultRule) {
    defaultRules.value = upsertOverview(defaultRules.value, overview)
    return
  }
  policies.value = upsertOverview(policies.value, overview)
}

function upsertOverview(items: ResponseInspectionPolicyOverview[], overview: ResponseInspectionPolicyOverview): ResponseInspectionPolicyOverview[] {
  const next = items.filter((item) => item.id !== overview.id)
  next.push(overview)
  return next.sort((left, right) => left.priority - right.priority || compareServerDateTime(right.updatedAt, left.updatedAt) || left.id.localeCompare(right.id))
}

onMounted(loadPolicies)
onBeforeUnmount(() => {
  clearProviderOptionsSearchTimer()
  loadCoordinator.dispose()
})

function cancelActiveProviderOptionsRequest(): void {
  loadCoordinator.cancelProviderOptionsRequest()
  activeProviderOptionsRequest = undefined
  providerOptionsLoading.value = false
}

function clearProviderOptionsPresentation(): void {
  providerOptions.value = []
  providerOptionsLoading.value = false
  providerOptionsReady.value = false
}

function clearProviderOptionsSearchTimer(): void {
  if (!providerOptionsSearchTimer) return
  clearTimeout(providerOptionsSearchTimer)
  providerOptionsSearchTimer = undefined
}
</script>

<style scoped>
.response-policy-page {
  min-height: 0;
}
</style>
