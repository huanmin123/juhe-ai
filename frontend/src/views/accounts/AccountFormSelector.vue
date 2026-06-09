<template>
  <section class="account-entry-section">
    <div class="account-entry-main">
      <div class="account-entry-head">
        <span class="entry-label">账户类型</span>
        <a-segmented
          :value="accountType"
          :disabled="editing"
          :options="segmentedTypeOptions"
          @change="handleTypeChange"
        />
      </div>
      <div class="entry-type-desc">{{ selectedTypeDescription }}</div>
    </div>
    <div class="account-entry-meta">
      <span>供应商</span>
      <a-tag color="blue">{{ selectedProvider?.name || providerCode || '未选择' }}</a-tag>
      <a-dropdown v-if="!editing && enabledProviders.length > 1" trigger="click">
        <a-button size="small">切换</a-button>
        <template #overlay>
          <a-menu @click="handleProviderMenuClick">
            <a-menu-item v-for="provider in enabledProviders" :key="provider.code">
              {{ provider.name }}（{{ providerAccountTypeCount(provider) }} 种类型）
            </a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { MenuProps } from 'ant-design-vue'
import type { AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'

interface AccountTypeChoice {
  value: AccountType
  label: string
  description: string
  tag: string
}

const props = defineProps<{
  accountType: AccountType
  accountTypeChoices: AccountTypeChoice[]
  editing: boolean
  providerCode: string
  providers: ProviderDefinition[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
  selectedProvider?: ProviderDefinition
}>()

const emit = defineEmits<{
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type', type: AccountType): void
}>()

const enabledProviders = computed(() => props.providers.filter((provider) => provider.enabled))

const segmentedTypeOptions = computed(() => props.accountTypeChoices.map((item) => ({
  label: item.label,
  value: item.value
})))

const selectedTypeDescription = computed(() => {
  const selected = props.accountTypeChoices.find((item) => item.value === props.accountType)
  return selected?.description ?? '选择账户类型后填写必要配置。'
})

function handleTypeChange(value: string | number): void {
  emit('select-type', value as AccountType)
}

const handleProviderMenuClick: MenuProps['onClick'] = ({ key }) => {
  emit('select-provider', String(key))
}

function providerAccountTypeCount(provider: ProviderDefinition): number {
  const accountTypes = provider.protocolProfiles.length
    ? provider.protocolProfiles.flatMap((profile) => profile.accountTypes)
    : provider.accountTypes
  return new Set(accountTypes).size
}
</script>

<style scoped>
.account-entry-section {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border: 1px solid #e8edf5;
  border-radius: 12px;
  background: #f8fafc;
}

.account-entry-main {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 6px;
}

.account-entry-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.entry-label {
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.entry-type-desc {
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

.account-entry-meta {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  color: #64748b;
  font-size: 12px;
}

@media (max-width: 640px) {
  .account-entry-section {
    align-items: stretch;
    flex-direction: column;
  }

  .account-entry-meta {
    justify-content: flex-start;
  }
}
</style>
