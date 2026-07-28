import { computed, reactive, type ComputedRef } from 'vue'

import { preferredDefaultProviderCode } from '@/shared/providerProtocol'
import type { GroupSchedulingPolicy, GroupSummary, GroupType, ProviderDefinition } from '@/types/domain'
import { GPT_VENDOR_CODE } from '../accounts/accountOptions'
import {
  cloneHighConcurrencySchedulingPolicy,
  defaultClientIpConcurrencyLimit,
  defaultHighConcurrencySchedulingPolicy,
  normalizeClientIpConcurrencyLimit
} from './groupSchedulingPolicy'

export type GroupEditForm = {
  name: string
  providerCode: string
  description: string
  enabled: boolean
  groupType: GroupType
  schedulingPolicy: Required<GroupSchedulingPolicy>
}

type GroupFormSeed = Pick<
  GroupSummary,
  'name' | 'providerCode' | 'description' | 'enabled' | 'groupType'
> & Partial<Pick<GroupSummary, 'schedulingPolicy'>>

export function useGroupFormModel(availableProviders: ComputedRef<ProviderDefinition[]>) {
  const form = reactive<GroupEditForm>({
    name: '',
    providerCode: GPT_VENDOR_CODE,
    description: '',
    enabled: true,
    groupType: 'personal',
    schedulingPolicy: cloneHighConcurrencySchedulingPolicy()
  })

  const formMaxQueueWaitSeconds = computed(() => Math.max(1, Math.round((form.schedulingPolicy.maxQueueWaitMs ?? defaultHighConcurrencySchedulingPolicy.maxQueueWaitMs) / 1000)))
  const clientIpLimitEnabled = computed({
    get: () => normalizeClientIpConcurrencyLimit(form.schedulingPolicy.clientIpConcurrencyLimit) > 0,
    set: (enabled: boolean) => {
      form.schedulingPolicy.clientIpConcurrencyLimit = enabled
        ? normalizeClientIpConcurrencyLimit(form.schedulingPolicy.clientIpConcurrencyLimit) || defaultClientIpConcurrencyLimit
        : 0
      form.schedulingPolicy.clientIpConcurrencyOverflowMode = form.schedulingPolicy.clientIpConcurrencyOverflowMode === 'queue' ? 'queue' : 'reject'
    }
  })
  const formClientIpConcurrencyLimit = computed(() => normalizeClientIpConcurrencyLimit(form.schedulingPolicy.clientIpConcurrencyLimit) || defaultClientIpConcurrencyLimit)

  function defaultProviderCode() {
    const providerCode = preferredDefaultProviderCode(availableProviders.value)
    if (!providerCode) throw new Error('没有可用供应商')
    return providerCode
  }

  function resetGroupFormForCreate() {
    const providerCode = defaultProviderCode()
    Object.assign(form, {
      name: '',
      providerCode,
      description: '',
      enabled: true,
      groupType: 'personal' as GroupType,
      schedulingPolicy: cloneHighConcurrencySchedulingPolicy()
    })
  }

  function applyGroupToForm(group: GroupFormSeed) {
    const schedulingPolicy = group.groupType === 'high_concurrency'
      ? cloneHighConcurrencySchedulingPolicy(group.schedulingPolicy, { requireComplete: true })
      : cloneHighConcurrencySchedulingPolicy()
    Object.assign(form, {
      name: group.name,
      providerCode: group.providerCode,
      description: group.description ?? '',
      enabled: group.enabled,
      groupType: group.groupType,
      schedulingPolicy
    })
  }

  function setFormMaxQueueWaitSeconds(value: unknown) {
    if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 3600) return
    form.schedulingPolicy.maxQueueWaitMs = value * 1000
  }

  function setFormClientIpConcurrencyLimit(value: unknown) {
    if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 1_000_000) return
    form.schedulingPolicy.clientIpConcurrencyLimit = value
  }

  function groupFormPayload(targetGroup?: Pick<GroupSummary, 'accessType'>): Record<string, unknown> {
    const schedulingPolicy = cloneHighConcurrencySchedulingPolicy(form.schedulingPolicy, { requireComplete: true })
    const localSettings = {
      enabled: form.enabled,
      groupType: form.groupType,
      schedulingPolicy: form.groupType === 'high_concurrency'
        ? {
            defaultSoftConcurrency: schedulingPolicy.defaultSoftConcurrency,
            maxQueueWaitMs: schedulingPolicy.maxQueueWaitMs,
            clientIpConcurrencyLimit: clientIpLimitEnabled.value ? formClientIpConcurrencyLimit.value : 0,
            clientIpConcurrencyOverflowMode: clientIpLimitEnabled.value
              ? schedulingPolicy.clientIpConcurrencyOverflowMode
              : 'reject',
            imageLaneMaxConcurrency: schedulingPolicy.imageLaneMaxConcurrency
          }
        : undefined
    }
    if (targetGroup?.accessType === 'authorized') {
      return localSettings
    }
    return {
      name: form.name,
      providerCode: form.providerCode,
      description: form.description,
      ...localSettings
    }
  }

  return {
    clientIpLimitEnabled,
    form,
    formClientIpConcurrencyLimit,
    formMaxQueueWaitSeconds,
    applyGroupToForm,
    groupFormPayload,
    resetGroupFormForCreate,
    setFormClientIpConcurrencyLimit,
    setFormMaxQueueWaitSeconds
  }
}
