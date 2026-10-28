<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadProviders(true)" />
    <ResponsiveDataList table-class="page-table provider-table" :columns="providerColumns" :data-source="providers" row-key="code" :loading="loading" :scroll-x="providerScrollX" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadProviders">
      <template #emptyText>
        <a-empty class="page-empty-card" :description="providerEmptyDescription" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <span class="provider-name-cell">
            <ProviderIcon class="provider-name-icon" :name="record.name" :provider-code="record.code" :size="16" />
            <span class="provider-name-text">{{ record.name }}</span>
          </span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accountTypes'">
          <a-space wrap>
            <a-tag v-for="type in record.accountTypes" :key="type" color="processing">{{ type }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'capabilities'">
          <a-space wrap>
            <a-tag v-for="capability in visibleProviderCapabilities(record.capabilities)" :key="capability" color="blue">{{ formatProviderCapability(capability) }}</a-tag>
            <span v-if="!visibleProviderCapabilities(record.capabilities).length" class="muted-text">-</span>
          </a-space>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'baseUrl'">
          <span class="mono-cell">{{ record.baseUrl }}</span>
        </template>
        <template v-else-if="column.key === 'defaultHealthCheckModel'">
          <span class="mono-cell">{{ record.defaultHealthCheckModel }}</span>
        </template>
        <template v-else-if="column.key === 'defaultSupportedModels'">
          <a-space wrap>
            <a-tag v-for="model in visibleDefaultSupportedModels(record)" :key="model" class="mono-cell">{{ model }}</a-tag>
            <a-tag v-if="hiddenDefaultSupportedModelCount(record) > 0">+{{ hiddenDefaultSupportedModelCount(record) }}</a-tag>
            <span v-if="!record.defaultSupportedModels?.length" class="muted-text">-</span>
          </a-space>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="providerActions" @action-click="handleProviderAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">
              <ProviderIcon class="provider-name-icon" :name="record.name" :provider-code="record.code" :size="16" />
              <span class="provider-name-text">{{ record.name }}</span>
            </div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView && formatProviderAccountTypes(record.accountTypes)" class="mobile-list-meta-item">
              <span>账户类型</span>
              <strong>{{ formatProviderAccountTypes(record.accountTypes) }}</strong>
            </div>
            <div v-if="isManagementView && formatCapabilitiesSummary(record.capabilities) !== '-'" class="mobile-list-meta-item">
              <span>接口能力</span>
              <strong>{{ formatCapabilitiesSummary(record.capabilities) }}</strong>
            </div>
            <div v-if="record.description" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description }}</strong>
            </div>
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认 Base URL</span>
              <strong class="mono-cell">{{ record.baseUrl }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认检查模型</span>
              <strong class="mono-cell">{{ record.defaultHealthCheckModel }}</strong>
            </div>
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认支持模型</span>
              <strong class="mono-cell">{{ formatDefaultSupportedModels(record) }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="providerActions" @action-click="handleProviderAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </a-card>
</template>

<script setup lang="ts">
// 供应商管理页（模型广场页与供应商目录退役设计 §4.5）：模型目录弹窗与模型态接线已退役，
// 模型的浏览与维护统一到模型广场（/my-models 用户面、/models 管理面）；本页「查看模型」
// 行入口改为跳转管理面广场并预选该供应商。
import { message } from '@/lib/antd'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import ProviderIcon from '@/components/ProviderIcon.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { authState } from '@/composables/useAuth'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import type { ProviderDefinition } from '@/types/domain'
import {
  formatCapabilitiesSummary,
  formatProviderAccountTypes,
  formatProviderCapability,
  visibleProviderCapabilities
} from './providerModelFormatters'
import {
  providerActionsForScope,
  providerColumnsForScope,
  providerEmptyDescriptionForScope,
  providerScrollXForScope
} from './providerTableConfig'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const providers = ref<ProviderDefinition[]>([])
let providerListRequestSequence = 0
let pageActive = true

const isManagementView = computed(() => route.meta.viewScope === 'admin')

const providerColumns = computed(() => providerColumnsForScope(isManagementView.value))
const providerScrollX = computed(() => providerScrollXForScope(isManagementView.value))
const providerEmptyDescription = computed(() => providerEmptyDescriptionForScope(isManagementView.value))
const providerActions = computed<RowActionItem[]>(() => providerActionsForScope(isManagementView.value))

function visibleDefaultSupportedModels(provider: ProviderDefinition): string[] {
  return (provider.defaultSupportedModels ?? []).slice(0, 5)
}

function hiddenDefaultSupportedModelCount(provider: ProviderDefinition): number {
  return Math.max(0, (provider.defaultSupportedModels?.length ?? 0) - 5)
}

function formatDefaultSupportedModels(provider: ProviderDefinition): string {
  const visible = visibleDefaultSupportedModels(provider)
  if (!visible.length) return '-'
  const hiddenCount = hiddenDefaultSupportedModelCount(provider)
  return hiddenCount > 0 ? `${visible.join(' / ')} / +${hiddenCount}` : visible.join(' / ')
}

async function loadProviders(force = false) {
  const requestSequence = ++providerListRequestSequence
  const requestSignature = providerListRequestSignature()
  loading.value = true
  try {
    const providerResult = await loadProviderOptionsResource({
      force,
      includeDisabled: isManagementView.value,
      includeDefinitions: !isManagementView.value,
      listItemsOnly: true,
      viewScope: isManagementView.value ? 'admin' : 'self',
      isManagementView: isManagementView.value,
      isCurrent: () => isCurrentProviderListRequest(requestSequence, requestSignature)
    })
    if (!isCurrentProviderListRequest(requestSequence, requestSignature)) return
    providers.value = providerResult.data
  } catch (error) {
    if (!isCurrentProviderListRequest(requestSequence, requestSignature)) return
    console.error(error)
    message.error('加载供应商失败')
  } finally {
    if (requestSequence === providerListRequestSequence) loading.value = false
  }
}

function providerListRequestSignature(): string {
  const viewer = authState.currentUser.value
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    isManagementView.value ? 'admin' : 'self'
  ])
}

function isCurrentProviderListRequest(sequence: number, signature: string): boolean {
  return pageActive
    && sequence === providerListRequestSequence
    && signature === providerListRequestSignature()
}

function handleProviderAction(key: string, provider: ProviderDefinition) {
  if (key === 'models') {
    // 模型目录弹窗已退役：跳转管理面模型广场并预选该供应商（设计 §4.5）。
    void router.push({ path: '/models', query: { providerCode: provider.code } })
  }
}

function invalidateProviderPageRequests(): void {
  providerListRequestSequence += 1
  loading.value = false
}

function deactivateProviderPage(): void {
  pageActive = false
  invalidateProviderPageRequests()
}

function resetProviderPageState(): void {
  invalidateProviderPageRequests()
  providers.value = []
}

onMounted(loadProviders)
onDeactivated(() => {
  deactivateProviderPage()
})
onBeforeUnmount(() => {
  deactivateProviderPage()
  resetProviderPageState()
})
onActivated(() => {
  pageActive = true
})
watch(() => authState.revision.value, () => {
  resetProviderPageState()
})
</script>

<style scoped>
.provider-name-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
}

.provider-name-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-name-icon {
  flex: none;
}

.mobile-list-card-title {
  display: flex;
  align-items: center;
  gap: 6px;
}

.provider-table :deep(.ant-empty) {
  margin: 12px 0;
}

.provider-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.muted-text {
  color: rgba(0, 0, 0, 0.45);
}
</style>
