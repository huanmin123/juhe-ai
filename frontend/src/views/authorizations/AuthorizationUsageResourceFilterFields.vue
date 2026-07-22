<template>
  <template v-if="variant === 'advanced'">
    <a-form-item :label="typeLabel">
      <a-select v-model:value="resourceType" :options="resourceTypeOptions" @change="emit('resourceTypeChange')" />
    </a-form-item>
    <a-form-item :label="resourceLabel">
      <GroupSelect
        v-if="resourceType === 'group'"
        v-model:value="resourceId"
        v-model:selected-group="resourceGroup"
        allow-clear
        :disabled="resourceGroupDisabled"
        option-filter-prop="label"
        :options="resourceOptions"
        :filter-option="false"
        :loading="resourceOptionsLoading"
        :placeholder="groupResourcePlaceholder"
        @change="emit('resourceChange')"
        @dropdown-visible-change="emit('resourceOptionsDropdown', $event)"
        @search="emit('resourceOptionsSearch', $event)"
      />
      <AccountSelect
        v-else
        v-model:value="resourceId"
        v-model:selected-account="resourceAccount"
        allow-clear
        cache-key="accounts"
        option-filter-prop="label"
        :options="resourceOptions"
        :disabled="resourceType === 'all'"
        :filter-option="false"
        :loading="resourceOptionsLoading"
        :placeholder="accountResourcePlaceholder"
        @change="emit('resourceChange')"
        @dropdown-visible-change="emit('resourceOptionsDropdown', $event)"
        @search="emit('resourceOptionsSearch', $event)"
      />
    </a-form-item>
  </template>
  <template v-else>
    <label class="mobile-filter-field">
      <span>{{ typeLabel }}</span>
      <a-select v-model:value="resourceType" :options="resourceTypeOptions" @change="emit('resourceTypeChange')" />
    </label>
    <slot name="between" />
    <label class="mobile-filter-field">
      <span>{{ resourceLabel }}</span>
      <GroupSelect
        v-if="resourceType === 'group'"
        v-model:value="resourceId"
        v-model:selected-group="resourceGroup"
        allow-clear
        :disabled="resourceGroupDisabled"
        option-filter-prop="label"
        :options="resourceOptions"
        :filter-option="false"
        :loading="resourceOptionsLoading"
        :placeholder="groupResourcePlaceholder"
        @change="emit('resourceChange')"
        @dropdown-visible-change="emit('resourceOptionsDropdown', $event)"
        @search="emit('resourceOptionsSearch', $event)"
      />
      <AccountSelect
        v-else
        v-model:value="resourceId"
        v-model:selected-account="resourceAccount"
        allow-clear
        cache-key="accounts"
        option-filter-prop="label"
        :options="resourceOptions"
        :disabled="resourceType === 'all'"
        :filter-option="false"
        :loading="resourceOptionsLoading"
        :placeholder="accountResourcePlaceholder"
        @change="emit('resourceChange')"
        @dropdown-visible-change="emit('resourceOptionsDropdown', $event)"
        @search="emit('resourceOptionsSearch', $event)"
      />
    </label>
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import AccountSelect from '@/components/AccountSelect.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { AuthorizationFilterResourceType } from './authorizationTableColumns'

type AuthorizationUsageResourceOption = {
  label: string
  value: string
}

const resourceType = defineModel<AuthorizationFilterResourceType>('resourceType', { required: true })
const resourceId = defineModel<string | undefined>('resourceId')
const resourceAccount = defineModel<AccountSelection | undefined>('resourceAccount')
const resourceGroup = defineModel<GroupSelection | undefined>('resourceGroup')

const props = withDefaults(defineProps<{
  variant: 'advanced' | 'mobile'
  typeLabel: string
  resourceLabel: string
  resourcePlaceholder: string
  emptyTypePlaceholder: string
  resourceTypeOptions: Array<{ label: string; value: AuthorizationFilterResourceType }>
  resourceOptions: AuthorizationUsageResourceOption[]
  resourceOptionsLoading: boolean
  resourceGroupDisabled: boolean
  disabledGroupPlaceholder?: string
}>(), {
  disabledGroupPlaceholder: '请先选择资源归属用户'
})

const emit = defineEmits<{
  (event: 'resourceTypeChange'): void
  (event: 'resourceChange'): void
  (event: 'resourceOptionsDropdown', open: boolean): void
  (event: 'resourceOptionsSearch', value: string): void
}>()

const groupResourcePlaceholder = computed(() => (
  props.resourceGroupDisabled ? props.disabledGroupPlaceholder : props.resourcePlaceholder
))
const accountResourcePlaceholder = computed(() => (
  resourceType.value === 'all' ? props.emptyTypePlaceholder : props.resourcePlaceholder
))
</script>
