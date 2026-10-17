<template>
  <section class="account-entry-section" :class="{ 'pick-mode': !editing }">
    <!-- 编辑/克隆态：维持既有结构（disabled 接入方式 segmented + 供应商徽标），不渲染卡片墙 -->
    <template v-if="editing">
      <div v-if="showAccountTypePicker" class="account-entry-head">
        <span class="entry-label">账户类型</span>
        <a-segmented
          block
          :value="selectedAccountTypeChoiceValue"
          :disabled="editing"
          :options="segmentedTypeOptions"
          @change="handleTypeChange"
        />
      </div>
      <div class="account-entry-meta">
        <a-tag color="blue">{{ selectedProvider?.name || providerCode || '未选择' }}</a-tag>
      </div>
    </template>
    <template v-else>
      <div v-if="pickerExpanded" class="provider-picker">
        <a-input
          v-model:value="searchKeyword"
          class="provider-search-input"
          allow-clear
          placeholder="搜索供应商名称或代码"
        />
        <div v-if="visibleProviders.length" class="provider-card-grid">
          <button
            v-for="provider in visibleProviders"
            :key="provider.code"
            type="button"
            class="provider-card"
            @click="handleProviderCardClick(provider.code)"
          >
            <span class="provider-card-logo">
              <ProviderIcon :name="provider.name" :provider-code="provider.code" :size="26" />
            </span>
            <span class="provider-card-body">
              <span class="provider-card-name">{{ provider.name }}</span>
              <span class="provider-card-types">
                <a-tag v-for="tag in providerAccountTypeTags(provider)" :key="tag" class="provider-card-type-tag">{{ tag }}</a-tag>
              </span>
              <span class="provider-card-count">{{ accountCountText(provider.code) }}</span>
            </span>
          </button>
        </div>
        <div v-else class="provider-card-empty">没有匹配的供应商，换个关键词试试</div>
      </div>
      <template v-else>
        <div class="provider-summary-bar">
          <ProviderIcon :name="selectedProvider?.name" :provider-code="providerCode" :size="20" />
          <span class="provider-summary-name">{{ selectedProvider?.name || providerCode || '未选择' }}</span>
          <a-button size="small" @click="handlePickerExpand">更换</a-button>
        </div>
        <div v-if="showAccountTypePicker" class="account-entry-head">
          <span class="entry-label">账户类型</span>
          <a-segmented
            block
            :value="selectedAccountTypeChoiceValue"
            :disabled="editing"
            :options="segmentedTypeOptions"
            @change="handleTypeChange"
          />
        </div>
      </template>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import ProviderIcon from '@/components/ProviderIcon.vue'
import { accountTypeText } from './accountBasicFormatters'
import type { AccountTypeChoice } from './accountEditFormDisplay'

const props = withDefaults(defineProps<{
  accountType: AccountType
  accountTypeChoices: AccountTypeChoice[]
  editing: boolean
  providerCode: string
  providers: ProviderDefinition[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
  selectedProvider?: ProviderDefinition
  /** 卡片墙账户计数（providerCode → 当前范围账户数）；缺省视为全部为 0。 */
  accountCounts?: Record<string, number>
  /** 弹窗 open 状态：每次打开时把卡片墙复位为展开。 */
  open?: boolean
}>(), {
  accountCounts: () => ({}),
  open: false
})

const emit = defineEmits<{
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type-choice', value: string): void
  (event: 'picker-expanded-change', expanded: boolean): void
}>()

const searchKeyword = ref('')
const pickerExpanded = ref(true)

// 弹窗每次打开时复位为阶段一（独立的供应商卡片选择），配置区由上层按本事件隐藏。
watch(() => props.open, (next) => {
  if (!next) return
  pickerExpanded.value = true
  searchKeyword.value = ''
  emit('picker-expanded-change', true)
})

const enabledProviders = computed(() => props.providers.filter((provider) => provider.enabled))

// 搜索按名称与 code 不区分大小写过滤；排序为有账户在前按数量降序，无账户保持传入顺序。
const visibleProviders = computed(() => {
  const keyword = searchKeyword.value.trim().toLowerCase()
  const matched = enabledProviders.value.filter((provider) => (
    !keyword
    || provider.name.toLowerCase().includes(keyword)
    || provider.code.toLowerCase().includes(keyword)
  ))
  const ranked = matched.map((provider, index) => ({
    provider,
    index,
    count: props.accountCounts[provider.code] ?? 0
  }))
  const withAccounts = ranked.filter((item) => item.count > 0).sort((a, b) => b.count - a.count || a.index - b.index)
  const withoutAccounts = ranked.filter((item) => item.count <= 0).sort((a, b) => a.index - b.index)
  return [...withAccounts, ...withoutAccounts].map((item) => item.provider)
})

// 选项只展示接入方式短码（tag），供应商名由下方供应商徽标承载，避免多类型供应商逐项重复前缀；
// 完整「供应商 + 接入方式」全称由弹框标题（typeTitle = label）呈现。
const segmentedTypeOptions = computed(() => props.accountTypeChoices.map((item) => ({
  label: item.tag,
  value: item.value
})))

const selectedAccountTypeChoiceValue = computed(() => {
  const selectedProfileId = props.selectedProtocolProfile?.id ?? ''
  return props.accountTypeChoices.find((item) => item.type === props.accountType && item.providerProtocolProfileId === selectedProfileId)?.value
    ?? props.accountTypeChoices.find((item) => item.type === props.accountType)?.value
    ?? ''
})
const showAccountTypePicker = computed(() => !isHybridProviderCode(props.providerCode) && props.accountTypeChoices.length > 1)

function handleTypeChange(value: string | number): void {
  emit('select-type-choice', String(value))
}

// 点选卡片只上报既有 select-provider 事件并进入阶段二（配置）；表单重置仍由上层 selectProvider 链完成。
function handleProviderCardClick(code: string): void {
  pickerExpanded.value = false
  emit('picker-expanded-change', false)
  emit('select-provider', code)
}

function handlePickerExpand(): void {
  pickerExpanded.value = true
  emit('picker-expanded-change', true)
}

function providerAccountTypeTags(provider: ProviderDefinition): string[] {
  const types = provider.protocolProfiles.length
    ? provider.protocolProfiles.flatMap((profile) => profile.accountTypes)
    : provider.accountTypes
  return [...new Set(types)].map((type) => accountTypeText(type))
}

function accountCountText(providerCode: string): string {
  const count = props.accountCounts[providerCode] ?? 0
  return count > 0 ? `${count} 个账户` : '还没有账户'
}
</script>

<style scoped>
.account-entry-section {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding-bottom: 14px;
}

.account-entry-section.pick-mode {
  align-items: stretch;
  flex-direction: column;
}

.account-entry-head {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.entry-label {
  color: var(--juhe-fg-soft);
  font-size: 13px;
  font-weight: 600;
}

.account-entry-meta {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  color: var(--juhe-muted);
  font-size: 12px;
}

/* ---- 新建态供应商卡片墙 ---- */

.provider-picker {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.provider-card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 10px;
  min-width: 0;
}

.provider-card {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  padding: 12px 14px;
  border: 1px solid var(--juhe-border);
  border-radius: 10px;
  background: var(--juhe-surface);
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}

.provider-card:hover {
  border-color: var(--juhe-accent-mid);
  box-shadow: 0 4px 14px rgba(34, 40, 43, 0.08);
}

.provider-card-logo {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 46px;
  height: 46px;
  border-radius: 12px;
  background: var(--juhe-accent-soft);
}

.provider-card-body {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  min-width: 0;
}

.provider-card-name {
  overflow: hidden;
  max-width: 100%;
  min-width: 0;
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-card-types {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}

.provider-card-types :deep(.ant-tag) {
  margin-inline-end: 0;
}

.provider-card-count {
  color: var(--juhe-muted);
  font-size: 12px;
}

.provider-card-empty {
  padding: 24px 0;
  color: var(--juhe-faint);
  font-size: 13px;
  text-align: center;
}

.provider-summary-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.provider-summary-name {
  overflow: hidden;
  flex: 1 1 auto;
  min-width: 0;
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 640px) {
  .account-entry-section {
    align-items: stretch;
    flex-direction: column;
  }

  .account-entry-meta {
    justify-content: flex-start;
  }

  .provider-card-grid {
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
  }

}
</style>
