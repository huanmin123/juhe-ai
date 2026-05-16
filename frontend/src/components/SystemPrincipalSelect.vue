<template>
  <a-select
    :value="value"
    :allow-clear="allowClear"
    :disabled="disabled"
    :mode="mode"
    :option-filter-prop="optionFilterProp"
    :options="principalOptions"
    :placeholder="resolvedPlaceholder"
    show-search
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'

type PrincipalKind = 'system_account' | 'team'
type PrincipalScope = PrincipalKind | 'all'
type SelectValue = string | string[] | undefined
type SelectMode = 'multiple' | 'tags' | 'combobox'

defineOptions({
  inheritAttrs: false
})

const props = withDefaults(defineProps<{
  value?: SelectValue
  accounts?: SystemAccountPrincipalSummary[]
  teams?: SystemTeamPrincipalSummary[]
  scope?: PrincipalScope
  activeOnly?: boolean
  includeAll?: boolean
  allLabel?: string
  allValue?: string
  excludedIds?: string[]
  mode?: SelectMode
  allowClear?: boolean
  disabled?: boolean
  placeholder?: string
}>(), {
  accounts: () => [],
  teams: () => [],
  scope: 'system_account',
  activeOnly: true,
  includeAll: false,
  allLabel: '全部系统账户',
  allValue: 'all',
  excludedIds: () => [],
  mode: undefined,
  allowClear: false,
  disabled: false,
  placeholder: undefined
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const optionFilterProp = 'label'

const principalOptions = computed(() => {
  const options: Array<{ label: string; value: string; disabled?: boolean }> = []
  const excluded = new Set(props.excludedIds)
  if (props.includeAll) {
    options.push({ label: props.allLabel, value: props.allValue })
  }
  if (props.scope === 'system_account' || props.scope === 'all') {
    options.push(...props.accounts
      .filter((account) => !excluded.has(account.id))
      .filter((account) => !props.activeOnly || account.status === 'active')
      .map((account) => ({
        label: withStatusLabel(systemAccountLabel(account), account.status),
        value: principalValue('system_account', account.id)
      })))
  }
  if (props.scope === 'team' || props.scope === 'all') {
    options.push(...props.teams
      .filter((team) => !excluded.has(team.id))
      .filter((team) => !props.activeOnly || team.status === 'active')
      .map((team) => ({
        label: withStatusLabel(teamLabel(team), team.status),
        value: principalValue('team', team.id)
      })))
  }
  return options
})

const resolvedPlaceholder = computed(() => {
  if (props.placeholder) return props.placeholder
  if (props.scope === 'team') return '请选择团队'
  if (props.scope === 'all') return '请选择系统账户或团队'
  return '请选择系统账户'
})

function handleUpdateValue(value: SelectValue) {
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emit('change', value, option)
}

function principalValue(kind: PrincipalKind, id: string): string {
  return props.scope === 'all' ? `${kind}:${id}` : id
}

function systemAccountLabel(account: SystemAccountPrincipalSummary): string {
  const displayName = account.displayName || account.username
  return displayName === account.username ? displayName : `${displayName}（${account.username}）`
}

function teamLabel(team: SystemTeamPrincipalSummary): string {
  return props.scope === 'all' ? `团队：${team.name}` : team.name
}

function withStatusLabel(label: string, status: 'active' | 'disabled'): string {
  return status === 'active' ? label : `${label}（停用）`
}
</script>
