<template>
  <a-card class="page-card responsive-page-card response-policy-page">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索策略名称或匹配条件"
      :show-reset="Boolean(keyword.trim())"
      :refresh-loading="loading"
      @search="applySearch"
      @reset="resetSearch"
      @refresh="loadPageData"
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
      :providers="openAIProviders"
      @delete="removePolicy"
      @edit="openEdit"
      @refresh="loadPageData"
      @view="openView"
    />

    <ResponseInspectionPolicyFormModal
      v-model:open="modalOpen"
      :mode="modalMode"
      :policy="activePolicy"
      :saving="saving"
      :provider-options="providerSelectOptions"
      :default-priority="nextPriority()"
      :default-provider-code="defaultProviderCode()"
      @submit="savePolicy"
      @cancel="resetModal"
    />

    <ResponseInspectionPolicyGuideModal
      v-model:open="guideOpen"
      title="响应检查策略配置指南"
      intro="这类策略检查上游返回的 JSON 或 SSE 流。协议层规则会作用到所有 OpenAI v1 账号；供应商层规则只作用到选中的供应商，例如 GPT。"
    />
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onMounted, ref } from 'vue'

import { api, type ResponseInspectionPolicyPayload } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { extractApiErrorMessage } from '@/shared/apiError'
import { isOpenAIProtocolProfile, preferredDefaultProviderCode } from '@/shared/providerProtocol'
import type {
  ProviderDefinition,
  ResponseInspectionPolicySummary
} from '@/types/domain'
import ResponseInspectionPolicyFormModal from './ResponseInspectionPolicyFormModal.vue'
import ResponseInspectionPolicyGuideModal from './ResponseInspectionPolicyGuideModal.vue'
import {
  responseInspectionPolicyAccountCompatibilityText,
  responseInspectionPolicyActionText,
  responseInspectionPolicyClientProfileText,
  responseInspectionPolicyMatchSummary,
  responseInspectionPolicyProviderText,
  responseInspectionPolicyProtocolText,
  responseInspectionPolicyScopeText
} from './responseInspectionPolicyDisplay'
import ResponseInspectionPolicyList from './ResponseInspectionPolicyList.vue'

type PolicyModalMode = 'create' | 'edit' | 'view'

const loading = ref(false)
const saving = ref(false)
const keyword = ref('')
const providers = ref<ProviderDefinition[]>([])
const defaultRules = ref<ResponseInspectionPolicySummary[]>([])
const policies = ref<ResponseInspectionPolicySummary[]>([])
const modalOpen = ref(false)
const modalMode = ref<PolicyModalMode>('create')
const guideOpen = ref(false)
const editingId = ref<string>()
const activePolicy = ref<ResponseInspectionPolicySummary>()

const allPolicies = computed(() => [...defaultRules.value, ...policies.value])
const openAIProviders = computed(() => providers.value
  .filter((provider) => provider.enabled && provider.protocolProfiles.some((profile) => profile.enabled && isOpenAIProtocolProfile(profile)))
  .sort((left, right) => left.name.localeCompare(right.name, 'zh-Hans-CN') || left.code.localeCompare(right.code)))
const providerSelectOptions = computed(() => openAIProviders.value.map((provider) => ({
  label: provider.name,
  value: provider.code
})))
const filteredPolicies = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return allPolicies.value
  return allPolicies.value.filter((policy) => searchableText(policy).includes(text))
})

async function loadPolicies(): Promise<void> {
  loading.value = true
  try {
    const result = await api.responseInspectionPolicies.list()
    defaultRules.value = result.defaultRules
    policies.value = result.policies
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略失败'))
  } finally {
    loading.value = false
  }
}

async function loadPageData(): Promise<void> {
  loading.value = true
  try {
    const [policyResult, providerResult] = await Promise.all([
      api.responseInspectionPolicies.list(),
      api.providers.options()
    ])
    defaultRules.value = policyResult.defaultRules
    policies.value = policyResult.policies
    providers.value = providerResult
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略失败'))
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editingId.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'create'
  modalOpen.value = true
}

function openView(policy: ResponseInspectionPolicySummary): void {
  editingId.value = undefined
  activePolicy.value = policy
  modalMode.value = 'view'
  modalOpen.value = true
}

function openEdit(policy: ResponseInspectionPolicySummary): void {
  if (!policy.editable) {
    openView(policy)
    return
  }
  editingId.value = policy.id
  activePolicy.value = policy
  modalMode.value = 'edit'
  modalOpen.value = true
}

function resetModal(): void {
  editingId.value = undefined
  activePolicy.value = undefined
  modalMode.value = 'create'
}

async function savePolicy(payload: ResponseInspectionPolicyPayload): Promise<void> {
  saving.value = true
  try {
    if (editingId.value) {
      await api.responseInspectionPolicies.update(editingId.value, payload)
      message.success('响应检查策略已更新')
    } else {
      await api.responseInspectionPolicies.create(payload)
      message.success('响应检查策略已创建')
    }
    modalOpen.value = false
    resetModal()
    await loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存响应检查策略失败'))
  } finally {
    saving.value = false
  }
}

async function removePolicy(policy: ResponseInspectionPolicySummary): Promise<void> {
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
}

function nextPriority(): number {
  const used = new Set(policies.value
    .map((policy) => policy.priority)
    .filter((priority): priority is number => Number.isInteger(priority) && priority > 0 && priority <= 9999))
  for (let priority = 1; priority <= 9999; priority += 1) {
    if (!used.has(priority)) return priority
  }
  return 9999
}

function searchableText(policy: ResponseInspectionPolicySummary): string {
  return [
    policy.name,
    responseInspectionPolicyScopeText(policy),
    responseInspectionPolicyProtocolText(policy.protocolCode),
    responseInspectionPolicyProviderText(policy.providerCode, openAIProviders.value),
    responseInspectionPolicyClientProfileText(policy.match.clientProfiles),
    responseInspectionPolicyAccountCompatibilityText(policy.match.accountClientCompatibilities),
    responseInspectionPolicyMatchSummary(policy),
    policy.defaultRule ? '默认' : '自定义',
    String(policy.priority),
    policy.enabled ? '启用' : '停用',
    responseInspectionPolicyActionText(policy.action),
    policy.notes
  ].filter(Boolean).join(' ').toLowerCase()
}

function defaultProviderCode(): string {
  return preferredDefaultProviderCode(openAIProviders.value)
}

onMounted(loadPageData)
</script>

<style scoped>
.response-policy-page {
  min-height: 0;
}
</style>
