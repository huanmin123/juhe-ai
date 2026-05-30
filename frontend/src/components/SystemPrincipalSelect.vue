<template>
  <CachedSelect
    :value="value"
    :allow-clear="allowClear"
    :disabled="disabled"
    :mode="mode"
    :options="principalOptions"
    :placeholder="resolvedPlaceholder"
    :selected-ids="normalizedSelectedIds"
    :selected-options="selectedOptions"
    :cache-key="principalCacheKey"
    :preference-key="preferenceKey ?? principalCacheKey"
    :ignored-preference-values="ignoredPreferenceValues"
    v-bind="$attrs"
    @change="handleChange"
    @update:value="handleUpdateValue"
  />
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import CachedSelect from '@/components/CachedSelect.vue'
import {
  principalLabelForId,
  rememberPrincipalLabel,
  rememberPrincipalSelections,
  rememberSystemAccountPrincipals,
  rememberSystemTeamPrincipals,
  systemAccountPrincipalName,
  type PrincipalKind,
  type PrincipalSelection
} from '@/shared/principalLabelCache'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'

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
  selectedPrincipal?: PrincipalSelection
  selectedPrincipals?: Array<PrincipalSelection | undefined>
  selectedIds?: Array<string | undefined>
  selectedAccounts?: Array<SystemAccountPrincipalSummary | undefined>
  selectedTeams?: Array<SystemTeamPrincipalSummary | undefined>
  preferenceKey?: string
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
  selectedPrincipal: undefined,
  selectedPrincipals: () => [],
  selectedIds: () => [],
  selectedAccounts: () => [],
  selectedTeams: () => [],
  preferenceKey: undefined,
  mode: undefined,
  allowClear: false,
  disabled: false,
  placeholder: undefined
})

const emit = defineEmits<{
  (event: 'update:value', value: SelectValue): void
  (event: 'update:selectedPrincipal', value: PrincipalSelection | undefined): void
  (event: 'update:selectedPrincipals', value: PrincipalSelection[]): void
  (event: 'change', value: SelectValue, option: unknown): void
}>()

const principalOptions = computed(() => {
  const options: SelectOption[] = []
  const excluded = new Set(props.excludedIds)
  if (props.includeAll) {
    options.push({ label: props.allLabel, value: props.allValue })
  }
  if (props.scope === 'system_account' || props.scope === 'all') {
    options.push(...props.accounts
      .filter((account) => !excluded.has(account.id))
      .filter((account) => !props.activeOnly || account.status === 'active')
      .filter((account) => Boolean(systemAccountLabel(account).trim()))
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
const normalizedSelectedIds = computed(() => [
  ...selectedValues(props.value),
  ...props.selectedIds.map((id) => normalizedSelectedValue(id)).filter((id): id is string => Boolean(id))
])
const selectedOptions = computed<SelectOption[]>(() => [
  ...[props.selectedPrincipal, ...props.selectedPrincipals]
    .filter((selection): selection is PrincipalSelection => Boolean(selection))
    .map((selection) => principalSelectedOption(selection))
    .filter((option): option is SelectOption => Boolean(option)),
  ...props.selectedAccounts
    .filter((account): account is SystemAccountPrincipalSummary => Boolean(account))
    .filter((account) => Boolean(systemAccountLabel(account).trim()))
    .map((account) => ({ label: withStatusLabel(systemAccountLabel(account), account.status), value: principalValue('system_account', account.id) })),
  ...props.selectedTeams
    .filter((team): team is SystemTeamPrincipalSummary => Boolean(team))
    .map((team) => ({ label: withStatusLabel(teamLabel(team), team.status), value: principalValue('team', team.id) }))
])
const principalCacheKey = computed(() => `system-principal:${props.scope}`)
const ignoredPreferenceValues = computed(() => props.includeAll ? [props.allValue] : [])

watch(
  () => props.accounts,
  (accounts) => {
    rememberSystemAccountPrincipals(accounts)
    for (const account of accounts) {
      rememberPrincipalLabel('system_account', account.id, withStatusLabel(systemAccountLabel(account), account.status))
    }
  },
  { immediate: true }
)
watch(
  () => props.teams,
  (teams) => {
    rememberSystemTeamPrincipals(teams)
    for (const team of teams) {
      rememberPrincipalLabel('team', team.id, withStatusLabel(teamLabel(team), team.status))
    }
  },
  { immediate: true }
)
watch(
  () => [props.selectedPrincipal, ...props.selectedPrincipals],
  (selections) => rememberPrincipalSelections(selections),
  { immediate: true }
)

const resolvedPlaceholder = computed(() => {
  if (props.placeholder) return props.placeholder
  if (props.scope === 'team') return '请选择团队'
  if (props.scope === 'all') return '请选择系统账户或团队'
  return '请选择系统账户'
})

function handleUpdateValue(value: SelectValue) {
  emitSelectedPrincipal(value)
  emit('update:value', value)
}

function handleChange(value: SelectValue, option: unknown) {
  emitSelectedPrincipal(value)
  emit('change', value, option)
}

function principalValue(kind: PrincipalKind, id: string): string {
  return props.scope === 'all' ? `${kind}:${id}` : id
}

function normalizedSelectedValue(id: string | undefined): string | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  if (props.scope === 'all' && normalizedId.includes(':')) return normalizedId
  return normalizedId
}

function systemAccountLabel(account: SystemAccountPrincipalSummary): string {
  return systemAccountPrincipalName(account)
}

function teamLabel(team: SystemTeamPrincipalSummary): string {
  return props.scope === 'all' ? `团队：${team.name}` : team.name
}

function withStatusLabel(label: string, status: 'active' | 'disabled'): string {
  const normalizedLabel = label.trim()
  if (!normalizedLabel) return ''
  return status === 'active' ? normalizedLabel : `${normalizedLabel}（停用）`
}

function emitSelectedPrincipal(value: SelectValue): void {
  const selections = selectedValues(value)
    .map((item) => principalSelectionForValue(item))
    .filter((selection): selection is PrincipalSelection => Boolean(selection))
  emit('update:selectedPrincipals', selections)
  emit('update:selectedPrincipal', typeof value === 'string' ? selections[0] : undefined)
}

function principalSelectionForValue(value: string | undefined): PrincipalSelection | undefined {
  const parsed = parsePrincipalValue(value)
  if (!parsed) return undefined
  const options = principalOptions.value
  if (parsed.kind === 'team') {
    const team = props.teams.find((item) => item.id === parsed.id)
    if (team) return { id: team.id, name: team.name, kind: 'team' }
  } else {
    const account = props.accounts.find((item) => item.id === parsed.id)
    const accountName = account ? systemAccountLabel(account).trim() : ''
    if (account && accountName) return { id: account.id, name: accountName, kind: 'system_account' }
  }
  const option = options.find((item) => item.value === value)
  if (option) return { id: parsed.id, name: option.label, kind: parsed.kind }
  const cachedLabel = principalLabelForId(parsed.kind, parsed.id)
  return cachedLabel ? { id: parsed.id, name: cachedLabel, kind: parsed.kind } : undefined
}

function parsePrincipalValue(value: string | undefined): PrincipalSelection | undefined {
  const normalizedValue = value?.trim()
  if (!normalizedValue || normalizedValue === props.allValue) return undefined
  if (props.scope !== 'all') {
    return { id: normalizedValue, name: '', kind: props.scope }
  }
  const [kind, ...idParts] = normalizedValue.split(':')
  const id = idParts.join(':').trim()
  if ((kind === 'system_account' || kind === 'team') && id) {
    return { id, name: '', kind }
  }
  return undefined
}

function selectedValues(value: SelectValue): Array<string | undefined> {
  return Array.isArray(value) ? value : [value]
}

function principalSelectedOption(selection: PrincipalSelection): SelectOption | undefined {
  const normalizedId = selection.id.trim()
  const normalizedName = selection.name.trim()
  if (!normalizedId || !normalizedName) return undefined
  if (props.scope !== 'all' && props.scope !== selection.kind) return undefined
  return {
    label: normalizedName,
    value: principalValue(selection.kind, normalizedId)
  }
}
</script>
