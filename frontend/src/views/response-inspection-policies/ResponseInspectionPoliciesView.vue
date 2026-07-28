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
      @provider-options-dropdown-visible-change="loadProviderOptionsOnDropdown"
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

import { api, type ResponseInspectionPolicyPayload } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { extractApiErrorMessage } from '@/shared/apiError'
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
  type ResponseInspectionPolicyModalIntent
} from './responseInspectionPolicyLoadCoordinator'

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

const loadCoordinator = createResponseInspectionPolicyLoadCoordinator({
  list: (signal) => api.responseInspectionPolicies.list({ signal }),
  detail: (policyId, signal) => api.responseInspectionPolicies.detail(policyId, { signal }),
  providerOptions: (signal) => api.responseInspectionPolicies.providerOptions({ signal })
})
let activeListRequest: Promise<unknown> | undefined
let activeModalIntent: ResponseInspectionPolicyModalIntent | undefined
let activeProviderOptionsRequest: Promise<ResponseInspectionPolicyProviderOption[] | undefined> | undefined

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
  editingId.value = policy.id
  activePolicy.value = detail
  modalOpen.value = true
}

async function loadProviderOptionsOnDropdown(open: boolean): Promise<void> {
  if (!open || modalMode.value === 'view' || providerOptionsReady.value || activeProviderOptionsRequest) return
  const intent = activeModalIntent
  if (!intent || !loadCoordinator.isCurrentModalIntent(intent)) return
  const request = loadCoordinator.loadProviderOptions(intent)
  activeProviderOptionsRequest = request
  providerOptionsLoading.value = true
  try {
    const options = await request
    if (activeProviderOptionsRequest !== request || !loadCoordinator.isCurrentModalIntent(intent) || !options) return
    providerOptions.value = options
    providerOptionsReady.value = true
  } catch (error) {
    if (activeProviderOptionsRequest !== request || !loadCoordinator.isCurrentModalIntent(intent)) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略供应商选项失败，请重试'))
  } finally {
    if (activeProviderOptionsRequest === request) {
      activeProviderOptionsRequest = undefined
      providerOptionsLoading.value = false
    }
  }
}

function resetModal(): void {
  loadCoordinator.cancelModalIntent()
  modalOpen.value = false
  openingPolicyId.value = undefined
  activeModalIntent = undefined
  activeProviderOptionsRequest = undefined
  providerOptions.value = []
  providerOptionsLoading.value = false
  providerOptionsReady.value = false
  editingId.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'create'
}

async function savePolicy(payload: ResponseInspectionPolicyPayload): Promise<void> {
  saving.value = true
  const targetId = editingId.value
  try {
    let savedPolicy: ResponseInspectionPolicyDetail
    if (targetId) {
      savedPolicy = await api.responseInspectionPolicies.update(targetId, payload)
      upsertPolicyOverview(savedPolicy)
      message.success('响应检查策略已更新')
    } else {
      savedPolicy = await api.responseInspectionPolicies.create(payload)
      upsertPolicyOverview(savedPolicy)
      message.success('响应检查策略已创建')
    }
    resetModal()
    void loadPolicies()
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
    void loadPolicies()
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
  activeProviderOptionsRequest = undefined
  providerOptions.value = []
  providerOptionsLoading.value = false
  providerOptionsReady.value = false
  openingPolicyId.value = policyId
  activeModalIntent = loadCoordinator.beginModalIntent(policyId)
  return activeModalIntent
}

function upsertPolicyOverview(detail: ResponseInspectionPolicyDetail): void {
  const overview = policyOverviewFromDetail(detail)
  if (overview.defaultRule) {
    defaultRules.value = upsertOverview(defaultRules.value, overview)
    return
  }
  policies.value = upsertOverview(policies.value, overview)
}

function upsertOverview(items: ResponseInspectionPolicyOverview[], overview: ResponseInspectionPolicyOverview): ResponseInspectionPolicyOverview[] {
  const next = items.filter((item) => item.id !== overview.id)
  next.push(overview)
  return next.sort((left, right) => left.priority - right.priority || String(right.updatedAt ?? '').localeCompare(String(left.updatedAt ?? '')) || left.id.localeCompare(right.id))
}

function policyOverviewFromDetail(detail: ResponseInspectionPolicyDetail): ResponseInspectionPolicyOverview {
  return {
    id: detail.id,
    defaultRule: detail.defaultRule,
    editable: detail.editable,
    name: detail.name,
    enabled: detail.enabled,
    priority: detail.priority,
    scopeType: detail.scopeType,
    protocolCode: detail.protocolCode,
    providerCode: detail.providerCode,
    providerName: detail.providerName,
    action: detail.action,
    updatedAt: detail.updatedAt
  }
}

onMounted(loadPolicies)
onBeforeUnmount(() => {
  loadCoordinator.dispose()
})
</script>

<style scoped>
.response-policy-page {
  min-height: 0;
}
</style>
