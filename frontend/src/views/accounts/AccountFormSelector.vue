<template>
  <section class="account-entry-section">
    <div class="account-entry-head">
      <span class="entry-label">账户类型</span>
      <a-segmented
        :value="accountType"
        :disabled="editing"
        :options="segmentedTypeOptions"
        @change="handleTypeChange"
      />
    </div>
    <div class="account-entry-meta">
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
  gap: 16px;
  padding-bottom: 14px;
  border-bottom: 1px solid #eef2f7;
}

.account-entry-head {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.entry-label {
  color: #334155;
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
