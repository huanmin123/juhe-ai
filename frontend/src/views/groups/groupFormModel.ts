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
  let editBaseline: GroupEditForm | undefined
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
    editBaseline = undefined
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
    editBaseline = currentGroupFormSnapshot()
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
    const current = currentGroupFormSnapshot()
    const schedulingPolicy = writableSchedulingPolicy(current)
    const localSettings = {
      enabled: current.enabled,
      groupType: current.groupType,
      schedulingPolicy: current.groupType === 'high_concurrency'
        ? schedulingPolicy
        : undefined
    }
    if (targetGroup) {
      if (!editBaseline) throw new Error('缺少分组编辑基线，请重新打开编辑弹窗')
      const payload: Record<string, unknown> = {}
      if (current.enabled !== editBaseline.enabled) payload.enabled = current.enabled
      if (current.groupType !== editBaseline.groupType) payload.groupType = current.groupType
      if (current.groupType === 'high_concurrency'
        && (editBaseline.groupType !== 'high_concurrency'
          || !sameSchedulingPolicy(writableSchedulingPolicy(editBaseline), schedulingPolicy))) {
        payload.schedulingPolicy = schedulingPolicy
      }
      if (targetGroup.accessType !== 'authorized') {
        if (current.name !== editBaseline.name) payload.name = current.name
        if (current.providerCode !== editBaseline.providerCode) payload.providerCode = current.providerCode
        if (current.description !== editBaseline.description) payload.description = current.description
      }
      return payload
    }
    return {
      name: current.name,
      providerCode: current.providerCode,
      description: current.description,
      ...localSettings
    }
  }

  function currentGroupFormSnapshot(): GroupEditForm {
    return {
      name: form.name.trim(),
      providerCode: form.providerCode.trim(),
      description: form.description.trim(),
      enabled: form.enabled,
      groupType: form.groupType,
      schedulingPolicy: cloneHighConcurrencySchedulingPolicy(form.schedulingPolicy, { requireComplete: true })
    }
  }

  function writableSchedulingPolicy(snapshot: GroupEditForm) {
    return {
      defaultSoftConcurrency: snapshot.schedulingPolicy.defaultSoftConcurrency,
      maxQueueWaitMs: snapshot.schedulingPolicy.maxQueueWaitMs,
      clientIpConcurrencyLimit: snapshot.schedulingPolicy.clientIpConcurrencyLimit,
      clientIpConcurrencyOverflowMode: snapshot.schedulingPolicy.clientIpConcurrencyLimit > 0
        ? snapshot.schedulingPolicy.clientIpConcurrencyOverflowMode
        : 'reject' as const,
      imageLaneMaxConcurrency: snapshot.schedulingPolicy.imageLaneMaxConcurrency
    }
  }

  function sameSchedulingPolicy(
    left: ReturnType<typeof writableSchedulingPolicy>,
    right: ReturnType<typeof writableSchedulingPolicy>
  ): boolean {
    return left.defaultSoftConcurrency === right.defaultSoftConcurrency
      && left.maxQueueWaitMs === right.maxQueueWaitMs
      && left.clientIpConcurrencyLimit === right.clientIpConcurrencyLimit
      && left.clientIpConcurrencyOverflowMode === right.clientIpConcurrencyOverflowMode
      && left.imageLaneMaxConcurrency === right.imageLaneMaxConcurrency
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
