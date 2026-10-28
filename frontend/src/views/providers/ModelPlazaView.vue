<template>
  <a-card class="page-card plaza-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="() => void reloadPlaza()">
      <template #inline-filters>
        <SystemPrincipalSelect
          v-if="isManagementView"
          :value="systemAccountFilter"
          :accounts="systemAccounts"
          :active-only="false"
          :disabled="loading"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          :selected-principal="systemAccountFilterSelection"
          class="plaza-owner-select"
          placeholder="选择模型归属用户"
          @update:value="handleSystemAccountUpdate"
          @update:selected-principal="systemAccountFilterSelection = $event"
          @change="handleSystemAccountFilterChange"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
      </template>
      <template #actions>
        <span class="plaza-status-line">
          共 {{ plazaItems.length }} 个模型 · {{ plazaProviders.length }} 家供应商
        </span>
        <span class="plaza-status-meta">{{ filteredModels.length }} / {{ vendorScopedItems.length }} 个模型 · USD 结算</span>
      </template>
    </ResponsiveListToolbar>

    <div class="vendor-tabs" role="tablist" aria-label="厂商筛选">
      <button
        v-for="tab in vendorTabs"
        :key="tab.code"
        type="button"
        class="vendor-tab"
        :class="{ 'is-active': selectedVendor === tab.code, 'is-disabled': !tab.enabled }"
        role="tab"
        :aria-selected="selectedVendor === tab.code"
        @click="selectVendor(tab.code)"
      >
        {{ tab.name }}
        <span class="vendor-tab-count">{{ tab.count }}</span>
      </button>
    </div>

    <a-tabs v-model:activeKey="selectedModelCategory" class="plaza-tabs" size="small">
      <a-tab-pane v-for="tab in modelCategoryTabs" :key="tab.key" :tab="tab.label" />
    </a-tabs>

    <div class="plaza-toolbar">
      <a-input-search v-model:value="keyword" allow-clear placeholder="搜索模型名称、用途或接口协议" class="plaza-search" />
      <a-button type="primary" :disabled="loading" @click="openCreateModel">添加模型</a-button>
    </div>

    <div class="plaza-content">
      <div v-if="loading" class="model-card-grid">
        <div v-for="index in 8" :key="index" class="model-card-skeleton">
          <a-skeleton active :paragraph="{ rows: 4 }" :title="false" />
        </div>
      </div>
      <a-empty
        v-else-if="loadError || !plazaItems.length"
        class="page-empty-card"
        :description="loadError || '暂无模型目录数据。'"
      />
      <a-empty v-else-if="!filteredModels.length" class="page-empty-card" description="没有匹配的模型" />
      <template v-else>
        <div class="model-card-grid">
          <article
            v-for="item in pagedModels"
            :key="modelRowKey(item)"
            class="model-card"
            role="button"
            tabindex="0"
            @click="handleCardClick($event, item)"
            @keydown="handleCardKeydown($event, item)"
          >
            <div class="model-card-head">
              <span class="model-card-provider">
                <ProviderIcon
                  :provider-code="item.providerCode"
                  :name="plazaProvidersMap.get(item.providerCode)?.name ?? item.providerCode"
                  :size="14"
                />
                <span class="model-card-provider-name">{{ plazaProvidersMap.get(item.providerCode)?.name ?? item.providerCode }}</span>
              </span>
              <span class="model-card-model mono-cell">{{ item.model }}</span>
              <a-button class="model-card-copy" size="small" type="text" aria-label="复制模型 ID" @click.stop="copyModelId(item)">
                <template #icon>
                  <CopyOutlined />
                </template>
              </a-button>
            </div>
            <div class="model-card-badges">
              <a-tag v-if="item.scope !== 'built_in'" :color="modelScopeColor(item.scope)">{{ formatModelScope(item.scope) }}</a-tag>
              <a-tag v-for="tier in modelServiceTierBadges(item)" :key="tier">{{ formatModelServiceTier(tier) }}</a-tag>
              <a-tag v-if="item.status !== 'active'" :color="modelStatusColor(item.status)">{{ formatModelStatus(item.status) }}</a-tag>
              <a-tag v-if="item.shutdownDate" color="orange">将停用 {{ item.shutdownDate }}</a-tag>
            </div>
            <div v-if="modelPriceSlots(item).length" class="model-card-prices">
              <div class="model-card-price-list">
                <div v-for="slot in modelPriceSlots(item)" :key="slot.label" class="model-card-price">
                  <span class="model-card-price-label">{{ slot.label }}</span>
                  <span class="model-card-price-value">${{ slot.value }}</span>
                </div>
              </div>
            </div>
            <div class="model-card-capacity">
              {{ formatModelCategory(item) }} · 上下文 {{ formatSpec(item.contextWindowTokens) }} · 输出 {{ formatSpec(item.maxOutputTokens) }}
            </div>
            <div v-if="rowActions(item).length" class="model-card-actions" @click.stop @keydown.stop>
              <RowActions :actions="rowActions(item)" @action-click="handleModelAction($event, item)" />
            </div>
          </article>
        </div>
        <div class="model-pagination">
          <a-pagination
            v-model:current="page"
            v-model:pageSize="pageSize"
            :total="filteredModels.length"
            :page-size-options="['24', '48', '96']"
            show-size-changer
          />
        </div>
      </template>
    </div>

    <a-drawer
      :open="record !== null"
      :width="'min(560px, 100vw)'"
      placement="right"
      @close="closeDrawer"
      @after-open-change="handleDrawerAfterOpenChange"
    >
      <template #title>
        <span class="plaza-drawer-title">
          <ProviderIcon
            v-if="record"
            :provider-code="record.providerCode"
            :name="plazaProvidersMap.get(record.providerCode)?.name ?? record.providerCode"
            :size="18"
          />
          <span>{{ record ? plazaProvidersMap.get(record.providerCode)?.name ?? record.providerCode : '' }}</span>
        </span>
      </template>
      <template v-if="record">
        <div class="model-drawer-badges">
          <a-tag v-if="record.scope !== 'built_in'" :color="modelScopeColor(record.scope)">{{ formatModelScope(record.scope) }}</a-tag>
          <a-tag v-for="tier in modelServiceTierBadges(record)" :key="tier">{{ formatModelServiceTier(tier) }}</a-tag>
          <a-tag v-if="record.status !== 'active'" :color="modelStatusColor(record.status)">{{ formatModelStatus(record.status) }}</a-tag>
          <a-tag v-if="record.shutdownDate" color="orange">将停用 {{ record.shutdownDate }}</a-tag>
        </div>
        <div class="model-drawer-name-row">
          <h3 class="model-drawer-model-name mono-cell">{{ record.model }}</h3>
          <button type="button" class="model-drawer-copy-btn" @click="copyModelId(record)">复制 ID</button>
        </div>
        <div class="model-drawer-meta">
          <span v-if="record.releaseDate">发布时间 {{ record.releaseDate }}</span>
          <span v-if="isManagementView && record.systemAccountId">归属 {{ record.systemAccountId }}</span>
        </div>
        <section v-if="modelPriceTable(record).rows.length" class="model-drawer-section">
          <h4 class="model-drawer-section-title">计费价格 <span class="model-drawer-section-unit">USD / 1M tokens</span></h4>
          <table class="model-drawer-table">
            <thead>
              <tr>
                <th scope="col" class="model-drawer-table-label">价格项</th>
                <th v-for="column in modelPriceTable(record).columns" :key="column.key" scope="col" class="model-drawer-table-value">{{ column.label }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in modelPriceTable(record).rows" :key="row.key">
                <td class="model-drawer-table-label">{{ row.label }}</td>
                <td v-for="column in modelPriceTable(record).columns" :key="column.key" class="model-drawer-table-value">
                  <strong v-if="row.cells[column.key]">{{ formatPriceTableCell(row.cells[column.key]!) }}</strong>
                  <template v-else>-</template>
                </td>
              </tr>
            </tbody>
          </table>
          <p v-if="modelPriceTable(record).hasOffpeak" class="model-drawer-table-note">
            高峰时段为工作日 09:00–12:00、14:00–18:00（北京时间）；时段计费未启用，当前所有请求按高峰价结算。
          </p>
        </section>
        <section v-if="modelInfoItems(record).length || hasModelAccessInfo(record)" class="model-drawer-section">
          <h4 class="model-drawer-section-title">基础信息</h4>
          <div class="model-drawer-info-grid">
            <template v-for="group in modelInfoItems(record)" :key="group.key">
              <div v-for="entry in group.items" :key="group.key + ':' + entry.key" class="model-drawer-info-item">
                <span>{{ entry.label }}</span>
                <strong>{{ formatModelCatalogDisplayValue(entry) }}</strong>
              </div>
            </template>
            <div class="model-drawer-info-item model-drawer-info-item--wide">
              <span>能力</span>
              <strong>{{ modelCapabilityLine(record) }}</strong>
            </div>
            <div class="model-drawer-info-item model-drawer-info-item--wide">
              <span>接口协议</span>
              <strong>
                <template v-if="record.supportedApiProtocols?.length">
                  {{ record.supportedApiProtocols.map(formatApiProtocol).join(' · ') }}
                </template>
                <template v-else>-</template>
              </strong>
            </div>
          </div>
        </section>
        <div v-if="rowActions(record).length" class="model-drawer-actions">
          <RowActions variant="button" :actions="rowActions(record)" @action-click="handleModelAction($event, record)" />
        </div>
      </template>
    </a-drawer>

    <a-modal
      v-model:open="providerPickerOpen"
      title="选择供应商"
      width="420px"
      ok-text="下一步"
      cancel-text="取消"
      @ok="confirmProviderPicker"
    >
      <p class="plaza-picker-hint">选择要在哪个供应商下创建个人模型：</p>
      <a-select
        v-model:value="pickerProviderCode"
        show-search
        option-filter-prop="label"
        :options="providerPickerOptions"
        placeholder="选择供应商"
        style="width: 100%"
      />
    </a-modal>

    <CustomModelFormModal
      ref="formModalRef"
      v-model:open="formModalOpen"
      :provider="formTargetProvider"
      :models="formTargetModels"
      :is-management-view="isManagementView"
      :system-accounts="systemAccounts"
      :system-account-filter="systemAccountFilter"
      :system-account-filter-selection="systemAccountFilterSelection"
      @saved="handleFormSaved"
      @cancel="formModalOpen = false"
    />
  </a-card>
</template>

<script setup lang="ts">
// 模型广场（模型广场页与供应商目录退役设计 §4）：用户 /my-models 与管理 /models 双路由
// 挂载同一组件（viewScope 既有模式）。数据为进入页面一次聚合加载 + 归属/创建/删除后重载 +
// 编辑后就地替换 + 手动刷新，不做激活自动刷新与轮询（no-auto-data-refresh 门禁）。
// 卡片/抽屉形态契约自 ProviderModelCatalogModal 迁移（该 modal 随本设计退役），仅增补供应商徽标
// （providers 摘要查表）；内置来源徽标与默认检查徽标属内部机制，不在卡面/抽屉展示。
import { CopyOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ProviderIcon from '@/components/ProviderIcon.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import { authState } from '@/composables/useAuth'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { ProviderPlazaMeta, ProviderModelPricing } from '@/types/domain'
import CustomModelFormModal from './CustomModelFormModal.vue'
import type { CustomModelFormTargetProvider, ProviderModelFormSavedPayload } from './CustomModelFormModal.vue'
import {
  findFirstModelCategory,
  formatApiProtocol,
  formatModelCatalogDisplayValue,
  formatModelCategory,
  formatModelModalities,
  formatModelScope,
  formatModelServiceTier,
  formatModelStatus,
  formatPriceTableCell,
  formatSpec,
  getModelCategory,
  modelInfoItems,
  modelPriceSlots,
  modelPriceTable,
  modelScopeColor,
  modelStatusColor,
  type ModelCategoryKey
} from './providerModelFormatters'
import { buildModelCategoryTabs } from './providerModelTableState'
import {
  buildPlazaProvidersMap,
  buildPlazaVendorTabs,
  clampPlazaPage,
  filterPlazaModels,
  isAllVendor
} from './modelPlazaState'

const route = useRoute()
const loading = ref(false)
const loadError = ref('')
const plazaProviders = ref<ProviderPlazaMeta[]>([])
const plazaItems = ref<ProviderModelPricing[]>([])
const keyword = ref('')
const selectedModelCategory = ref<ModelCategoryKey>('text')
const selectedVendor = ref('all')
const page = ref(1)
const pageSize = ref(24)
const record = ref<ProviderModelPricing | null>(null)
const systemAccountFilter = ref('')
const systemAccountFilterSelection = ref<PrincipalSelection>()
const formModalOpen = ref(false)
const formTargetProvider = ref<CustomModelFormTargetProvider | null>(null)
const providerPickerOpen = ref(false)
const pickerProviderCode = ref<string>()
const formModalRef = ref<InstanceType<typeof CustomModelFormModal>>()
let plazaRequestSequence = 0
let drawerTriggerElement: HTMLElement | null = null
let pageActive = true

const isManagementView = computed(() => route.meta.viewScope === 'admin')

const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  invalidate: invalidateSystemAccountOptions,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  errorMessage: '加载模型归属用户失败',
  selectedIds: () => [systemAccountFilter.value],
  onMissingSelectedIds: (ids) => {
    if (!ids.includes(systemAccountFilter.value)) return
    const fallback = currentUserPrincipalSelection()
    systemAccountFilter.value = fallback?.id ?? ''
    systemAccountFilterSelection.value = fallback
    void reloadPlaza()
  }
})

const plazaProvidersMap = computed(() => buildPlazaProvidersMap(plazaProviders.value))
const vendorTabs = computed(() => buildPlazaVendorTabs(plazaProviders.value, plazaItems.value))
const vendorScopedItems = computed(() => (
  isAllVendor(selectedVendor.value)
    ? plazaItems.value
    : plazaItems.value.filter((item) => item.providerCode === selectedVendor.value)
))
const modelCategoryTabs = computed(() => buildModelCategoryTabs(vendorScopedItems.value))
const filteredModels = computed(() => filterPlazaModels(plazaItems.value, {
  vendor: selectedVendor.value,
  category: selectedModelCategory.value,
  keyword: keyword.value
}))
const pagedModels = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return filteredModels.value.slice(start, start + pageSize.value)
})
const formTargetModels = computed(() => (
  formTargetProvider.value
    ? plazaItems.value.filter((item) => item.providerCode === formTargetProvider.value?.code)
    : []
))
const providerPickerOptions = computed(() => plazaProviders.value
  .filter((provider) => provider.enabled)
  .map((provider) => ({ value: provider.code, label: provider.name })))

watch([keyword, selectedModelCategory, selectedVendor, pageSize], () => {
  page.value = 1
})
watch([keyword, selectedModelCategory, selectedVendor, page, pageSize, plazaItems], () => {
  record.value = null
})
watch(plazaItems, () => {
  page.value = clampPlazaPage(page.value, pageSize.value, filteredModels.value.length)
})
// 「查看模型」跳转预选（管理面 /providers 行入口 → /models?providerCode=xxx），KeepAlive 存活期同样生效。
watch(() => route.query.providerCode, (value) => {
  const code = typeof value === 'string' ? value.trim() : ''
  if (code) selectVendor(code)
})

onMounted(() => {
  ensureSystemAccountFilter()
  const routeProviderCode = typeof route.query.providerCode === 'string' ? route.query.providerCode.trim() : ''
  if (routeProviderCode) selectedVendor.value = routeProviderCode
  void reloadPlaza()
})
onDeactivated(() => {
  deactivatePlazaPage()
})
onBeforeUnmount(() => {
  deactivatePlazaPage()
  resetPlazaPageState()
})
onActivated(() => {
  pageActive = true
})
watch(() => authState.revision.value, () => {
  resetPlazaPageState()
  invalidateSystemAccountOptions()
  systemAccountFilter.value = ''
  systemAccountFilterSelection.value = undefined
  systemAccounts.value = []
  resetSystemAccountOptionsSearch()
  formModalOpen.value = false
  providerPickerOpen.value = false
})

async function reloadPlaza() {
  const requestSequence = ++plazaRequestSequence
  const requestSignature = plazaRequestSignature()
  loading.value = true
  try {
    const result = await api.providers.modelPlaza(plazaQueryParams())
    if (!isCurrentPlazaRequest(requestSequence, requestSignature)) return
    plazaProviders.value = result.providers
    plazaItems.value = result.items
    loadError.value = ''
    reconcileSelectedCategory()
  } catch (error) {
    if (!isCurrentPlazaRequest(requestSequence, requestSignature)) return
    console.error(error)
    loadError.value = extractModelErrorMessage(error, '加载模型广场失败')
    message.error(loadError.value)
  } finally {
    if (requestSequence === plazaRequestSequence) loading.value = false
  }
}

function plazaQueryParams(): { viewScope?: 'admin'; systemAccountId?: string } | undefined {
  if (!isManagementView.value) return undefined
  const systemAccountId = systemAccountFilter.value.trim()
  return systemAccountId ? { viewScope: 'admin', systemAccountId } : { viewScope: 'admin' }
}

function plazaRequestSignature(): string {
  const viewer = authState.currentUser.value
  const query = plazaQueryParams()
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    query?.viewScope ?? 'self',
    query?.systemAccountId ?? ''
  ])
}

function isCurrentPlazaRequest(sequence: number, signature: string): boolean {
  return pageActive
    && sequence === plazaRequestSequence
    && signature === plazaRequestSignature()
}

function invalidatePlazaRequests(): void {
  plazaRequestSequence += 1
  loading.value = false
}

function deactivatePlazaPage(): void {
  pageActive = false
  invalidatePlazaRequests()
}

function resetPlazaPageState(): void {
  invalidatePlazaRequests()
  plazaProviders.value = []
  plazaItems.value = []
  record.value = null
}

function ensureSystemAccountFilter(): void {
  if (!isManagementView.value || systemAccountFilter.value.trim()) return
  const currentUser = currentUserPrincipalSelection()
  systemAccountFilter.value = currentUser?.id ?? ''
  systemAccountFilterSelection.value = currentUser
}

function currentUserPrincipalSelection(): PrincipalSelection | undefined {
  const user = authState.currentUser.value
  if (!user?.id || !user.displayName?.trim()) return undefined
  return { id: user.id, name: user.displayName.trim(), kind: 'system_account' }
}

function handleSystemAccountUpdate(value: string | string[] | undefined): void {
  systemAccountFilter.value = typeof value === 'string' ? value : ''
}

function handleSystemAccountFilterChange(): void {
  void reloadPlaza()
}

function selectVendor(code: string): void {
  if (selectedVendor.value === code) return
  selectedVendor.value = code
  reconcileSelectedCategory()
}

/** 切厂商 / 重载后用途 Tab 收敛：当前分类在厂商范围内无行时落到第一个有行分类。 */
function reconcileSelectedCategory(): void {
  const hasCurrentCategory = vendorScopedItems.value.some((item) => getModelCategoryOf(item) === selectedModelCategory.value)
  if (hasCurrentCategory) return
  selectedModelCategory.value = findFirstModelCategory(vendorScopedItems.value)
}

function getModelCategoryOf(item: ProviderModelPricing): ModelCategoryKey {
  return getModelCategory(item)
}
function openCreateModel(): void {
  const vendor = selectedVendor.value
  if (!isAllVendor(vendor)) {
    const provider = plazaProvidersMap.value.get(vendor)
    if (provider?.enabled) {
      startCreateFor(provider)
      return
    }
  }
  pickerProviderCode.value = undefined
  providerPickerOpen.value = true
}

function startCreateFor(provider: ProviderPlazaMeta): void {
  formTargetProvider.value = provider
  void formModalRef.value?.openCreate(provider, { mode: selectedModelCategory.value })
}

function confirmProviderPicker(): void {
  const provider = pickerProviderCode.value ? plazaProvidersMap.value.get(pickerProviderCode.value) : undefined
  if (!provider) return
  providerPickerOpen.value = false
  startCreateFor(provider)
}

function openEditModel(record: ProviderModelPricing): void {
  const provider = plazaProvidersMap.value.get(record.providerCode)
    ?? { code: record.providerCode, name: record.providerCode, enabled: true }
  formTargetProvider.value = provider
  void formModalRef.value?.openEdit(record)
}

function handleFormSaved(payload: ProviderModelFormSavedPayload): void {
  record.value = null
  if (payload.kind === 'updated') {
    plazaItems.value = plazaItems.value.map((item) => item.id === payload.result.id ? payload.result : item)
    return
  }
  void reloadPlaza()
}

/** 接入事实存在性（基础信息 section 门控的第二支）：无 catalogDisplay 非价格
 * 分组但有协议/模态事实的行仍显示基础信息网格。工具矩阵属 AI 问答搜索绑定的
 * 内部机制数据，不在抽屉展示。 */
function hasModelAccessInfo(record: ProviderModelPricing): boolean {
  return Boolean(
    record.supportedApiProtocols?.length
    || record.inputModalities?.length
    || record.outputModalities?.length
  )
}

/** 能力行：输入/输出模态合并去重，如「文本 · 图片」。 */
function modelCapabilityLine(record: ProviderModelPricing): string {
  const modalities = new Set(
    [formatModelModalities(record.inputModalities), formatModelModalities(record.outputModalities)]
      .flatMap((value) => value.split(' / '))
      .filter((value) => value && value !== '-')
  )
  return modalities.size ? Array.from(modalities).join(' · ') : '-'
}

function rowActions(record: ProviderModelPricing): RowActionItem[] {  if (loading.value) return []
  const actions: RowActionItem[] = []
  if (!canMutateModel(record)) return actions
  actions.push({ key: 'edit', label: record.scope === 'built_in' ? '编辑配置' : '编辑', icon: 'edit', tone: 'info' })
  if (record.scope !== 'built_in') {
    actions.push({ key: 'delete', label: '删除', icon: 'delete', danger: true })
  }
  return actions
}

function handleModelAction(key: string, record: ProviderModelPricing) {
  if (loading.value) return
  if (key === 'edit') {
    openEditModel(record)
    return
  }
  if (key === 'delete') {
    // 删除确认与 409 绑定 guard 错误透出由 CustomModelFormModal.deleteCustomModel 自含。
    void formModalRef.value?.deleteCustomModel(record)
  }
}

function canMutateModel(record: ProviderModelPricing): boolean {
  if (!record.id) return false
  if (record.scope === 'built_in') return isManagementView.value
  if (isManagementView.value) return true
  return record.systemAccountId === authState.currentUser.value?.id
}

function openModelDrawer(item: ProviderModelPricing, trigger: EventTarget | null): void {
  record.value = item
  drawerTriggerElement = trigger instanceof HTMLElement ? trigger : null
}

function closeDrawer(): void {
  record.value = null
}

function handleCardClick(event: MouseEvent, item: ProviderModelPricing): void {
  openModelDrawer(item, event.currentTarget)
}

function handleCardKeydown(event: KeyboardEvent, item: ProviderModelPricing): void {
  if (event.target !== event.currentTarget) return
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  openModelDrawer(item, event.currentTarget)
}

function handleDrawerAfterOpenChange(visible: boolean): void {
  if (!visible) drawerTriggerElement?.focus()
}

async function copyModelId(item: ProviderModelPricing): Promise<void> {
  await copyTextToClipboard(item.model)
}

function modelServiceTierBadges(item: ProviderModelPricing): string[] {
  return (item.supportedServiceTiers ?? []).filter((tier) => {
    const prices = item.serviceTierPrices?.[tier]
    if (!prices) return false
    return Object.values(prices).some((value) => typeof value === 'number')
  })
}

function modelRowKey(record: ProviderModelPricing): string {
  return record.id || [record.providerCode, record.scope, record.systemAccountId ?? '', record.model].join(':')
}

function extractModelErrorMessage(error: unknown, fallback: string): string {
  if (typeof error !== 'object' || error === null) return fallback
  const response = (error as { response?: { data?: { message?: unknown } } }).response
  const messageText = response?.data?.message
  return typeof messageText === 'string' && messageText.trim() ? messageText.trim() : fallback
}
</script>

<style scoped>
.plaza-page-card :deep(.ant-card-body) {
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.plaza-status-line {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  color: var(--juhe-muted);
  font-size: 13px;
}

.plaza-status-meta {
  margin-left: 4px;
}

.vendor-tabs {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}

.vendor-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  flex: none;
  color: var(--juhe-muted);
  font-size: 13px;
  cursor: pointer;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: 999px;
}

.vendor-tab:hover {
  color: var(--juhe-fg);
  border-color: var(--juhe-border-strong);
}

.vendor-tab.is-active {
  color: var(--juhe-surface);
  background: var(--juhe-fg);
  border-color: var(--juhe-fg);
}

.vendor-tab.is-disabled:not(.is-active) {
  color: var(--juhe-faint);
  cursor: default;
}

.vendor-tab-count {
  font-size: 11px;
  opacity: 0.65;
}

.plaza-tabs {
  flex: 0 0 auto;
  margin-bottom: 8px;
}

.plaza-tabs :deep(.ant-tabs-content-holder) {
  display: none;
}

.plaza-tabs :deep(.ant-tabs-nav) {
  margin-bottom: 0;
}

.plaza-toolbar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.plaza-search {
  width: 320px;
}

.plaza-owner-select {
  width: 260px;
}

.plaza-content {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-height: 320px;
  height: calc(100dvh - 380px);
}

.model-card-grid {
  display: grid;
  align-content: start;
  flex: 1 1 auto;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
  min-height: 0;
  overflow-y: auto;
}

.model-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  cursor: pointer;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius);
}

.model-card:hover,
.model-card:focus-visible {
  border-color: var(--juhe-border-strong);
  box-shadow: var(--juhe-shadow-card);
}

.model-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.model-card-provider {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 4px;
  padding: 0 6px;
  color: var(--juhe-muted);
  font-size: 11px;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: 999px;
}

.model-card-provider-name {
  max-width: 96px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-card-model {
  flex: 1 1 auto;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  overflow-wrap: anywhere;
  white-space: normal;
}

.model-card-copy {
  flex: none;
}

.model-card-badges,
.model-drawer-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.model-card-badges :deep(.ant-tag),
.model-drawer-badges :deep(.ant-tag) {
  margin-inline-end: 0;
}

.model-card-prices {
  display: grid;
  gap: 6px;
}

.model-card-prices-title {
  color: var(--juhe-muted);
  font-size: 11px;
  font-weight: 600;
}

.model-card-price-list {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(0, 1fr));
  gap: 6px;
}

.model-card-price {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.model-card-price-label {
  color: var(--juhe-faint);
  font-size: 11px;
  overflow-wrap: anywhere;
}

.model-card-price-value {
  color: var(--juhe-fg);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.model-card-price-unit {
  color: var(--juhe-faint);
  font-size: 10px;
  font-weight: 400;
}

.model-card-capacity {
  color: var(--juhe-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.model-card-actions {
  display: flex;
  flex: 1 0 auto;
  align-items: flex-end;
  justify-content: flex-end;
}

.model-card-skeleton {
  padding: 12px;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius);
}

.model-pagination {
  display: flex;
  flex: 0 0 auto;
  justify-content: flex-end;
  padding-top: 12px;
}

.plaza-drawer-title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.model-drawer-badges {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
}

.model-drawer-name-row {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 10px;
  margin: 10px 0 4px;
}

.model-drawer-model-name {
  min-width: 0;
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  letter-spacing: -0.2px;
  line-height: 1.3;
  overflow-wrap: anywhere;
  white-space: normal;
}

.model-drawer-copy-btn {
  border: none;
  background: transparent;
  padding: 0;
  cursor: pointer;
  color: var(--juhe-muted);
  font-size: 12px;
}

.model-drawer-copy-btn:hover {
  color: var(--juhe-fg);
}

.model-drawer-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  color: var(--juhe-faint);
  font-size: 11px;
}

.model-drawer-section {
  display: grid;
  gap: 10px;
  margin-top: 16px;
}

.model-drawer-section-title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.model-drawer-section-unit {
  margin-left: 6px;
  color: var(--juhe-faint);
  font-size: 11px;
  font-weight: 400;
}

.model-drawer-group {
  display: grid;
  gap: 6px;
}

.model-drawer-group-title {
  color: var(--juhe-muted);
  font-size: 12px;
  font-weight: 600;
}

/* 「计费价格」一张表（§6.2）：table-layout: fixed + 首列 38% 锚定列宽，
 * 档位列居中、行高舒展（对齐 juhe-ai-portal 计费价格表观感）；单元格
 * overflow-wrap 换行——禁止首列被数值列挤压塌陷成竖排单字。 */
.model-drawer-table {
  table-layout: fixed;
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.model-drawer-table th:first-child,
.model-drawer-table td:first-child {
  width: 38%;
}

.model-drawer-table th {
  padding: 10px 8px;
  color: var(--juhe-muted);
  font-weight: 500;
  text-align: left;
  background: var(--juhe-bg);
  border-bottom: 1px solid var(--juhe-border);
}

.model-drawer-table th.model-drawer-table-value {
  text-align: center;
}

.model-drawer-table td.model-drawer-table-label {
  color: var(--juhe-fg);
}

.model-drawer-table td {
  padding: 14px 8px;
  border-bottom: 1px solid var(--juhe-border);
  vertical-align: top;
  overflow-wrap: anywhere;
}

.model-drawer-table td.model-drawer-table-value {
  text-align: center;
}

/* 「基础信息」信息网格（§6.3）：portal 式浅底小卡两列网格。 */
.model-drawer-info-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.model-drawer-info-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: var(--juhe-radius);
  background: var(--juhe-surface-soft);
}

.model-drawer-info-item span {
  color: var(--juhe-faint);
  font-size: 11px;
}

.model-drawer-info-item strong {
  color: var(--juhe-fg);
  font-size: 12px;
  font-weight: 500;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

/* 接入事实宽项（接口协议 / 工具矩阵）：占满两列网格整行。 */
.model-drawer-info-item--wide {
  grid-column: 1 / -1;
}

/* 浅灰卡底会让 AntD 预设浅彩底 tag 发白，卡内 tag 提纯白底恢复轮廓。 */
.model-drawer-info-item :deep(.ant-tag) {
  background: var(--juhe-surface);
}

.model-drawer-table-label {
  color: var(--juhe-faint);
  font-weight: 500;
}

.model-drawer-table-value {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.model-drawer-table-note {
  margin: 6px 0 0;
  color: var(--juhe-faint);
  font-size: 11px;
}

.model-drawer-actions {
  margin-top: 16px;
}

.plaza-picker-hint {
  margin: 0 0 12px;
  color: var(--juhe-muted);
}

@media (max-width: 768px) {
  .plaza-status-meta {
    margin-left: 0;
  }

  .plaza-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .plaza-search {
    width: 100%;
  }

  .plaza-owner-select {
    width: 100%;
  }

  .plaza-content {
    height: auto;
    min-height: 0;
  }
}

@media (max-width: 767px) {
  .model-card-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
