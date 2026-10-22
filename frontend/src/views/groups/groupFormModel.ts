import { computed, reactive, type ComputedRef } from 'vue'

import type { GroupKind, GroupSchedulingPolicy, GroupSummary, GroupType, ProviderDefinition } from '@/types/domain'
import {
  cloneHighConcurrencySchedulingPolicy,
  defaultClientIpConcurrencyLimit,
  defaultHighConcurrencySchedulingPolicy,
  normalizeClientIpConcurrencyLimit
} from './groupSchedulingPolicy'

// 分组无供应商维度（供应商与上游协议面重构设计 §8：groups.provider_code 已
// 删除，后端 API 对该键「接受但忽略」）。表单与请求载荷不再携带 providerCode。
export type GroupEditForm = {
  name: string
  description: string
  enabled: boolean
  groupType: GroupType
  groupKind: GroupKind
  schedulingPolicy: Required<GroupSchedulingPolicy>
}

type GroupFormSeed = Pick<
  GroupSummary,
  'name' | 'description' | 'enabled' | 'groupType' | 'accessType'
> & Partial<Pick<GroupSummary, 'schedulingPolicy' | 'groupKind'>>

export function useGroupFormModel(_availableProviders: ComputedRef<ProviderDefinition[]>) {
  let editBaseline: GroupEditForm | undefined
  let editAccessType: 'owner' | 'authorized' | undefined
  const form = reactive<GroupEditForm>({
    name: '',
    description: '',
    enabled: true,
    groupType: 'personal',
    groupKind: 'standard',
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

  function resetGroupFormForCreate() {
    editBaseline = undefined
    editAccessType = undefined
    Object.assign(form, {
      name: '',
      description: '',
      enabled: true,
      groupType: 'personal' as GroupType,
      groupKind: 'standard' as GroupKind,
      schedulingPolicy: cloneHighConcurrencySchedulingPolicy()
    })
  }

  function applyGroupToForm(group: GroupFormSeed) {
    const schedulingPolicy = group.groupType === 'high_concurrency'
      ? cloneHighConcurrencySchedulingPolicy(group.schedulingPolicy, { requireComplete: true })
      : cloneHighConcurrencySchedulingPolicy()
    Object.assign(form, {
      name: group.name,
      description: group.description ?? '',
      enabled: group.enabled,
      groupType: group.groupType,
      groupKind: group.groupKind ?? 'standard',
      schedulingPolicy
    })
    editBaseline = currentGroupFormSnapshot()
    editAccessType = group.accessType === 'authorized' ? 'authorized' : 'owner'
  }

  function setFormMaxQueueWaitSeconds(value: unknown) {
    if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 3600) return
    form.schedulingPolicy.maxQueueWaitMs = value * 1000
  }

  function setFormClientIpConcurrencyLimit(value: unknown) {
    if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 1_000_000) return
    form.schedulingPolicy.clientIpConcurrencyLimit = value
  }

  function groupEditPatch(): Record<string, unknown> {
    if (!editBaseline || !editAccessType) throw new Error('缺少分组编辑基线，请重新打开编辑弹窗')
    const current = currentGroupFormSnapshot()
    const schedulingPolicy = writableSchedulingPolicy(current)
    const payload: Record<string, unknown> = {}
    if (current.enabled !== editBaseline.enabled) payload.enabled = current.enabled
    if (current.groupType !== editBaseline.groupType) payload.groupType = current.groupType
    if (current.groupType === 'high_concurrency'
      && (editBaseline.groupType !== 'high_concurrency'
        || !sameSchedulingPolicy(writableSchedulingPolicy(editBaseline), schedulingPolicy))) {
      payload.schedulingPolicy = schedulingPolicy
    }
    if (editAccessType !== 'authorized') {
      if (current.name !== editBaseline.name) payload.name = current.name
      if (current.description !== editBaseline.description) payload.description = current.description
    }
    return payload
  }

  function groupCreatePayload(): Record<string, unknown> {
    const current = currentGroupFormSnapshot()
    return {
      name: current.name,
      description: current.description,
      enabled: current.enabled,
      groupType: current.groupType,
      groupKind: current.groupKind,
      ...(current.groupType === 'high_concurrency' ? { schedulingPolicy: writableSchedulingPolicy(current) } : {})
    }
  }

  function currentGroupFormSnapshot(): GroupEditForm {
    return {
      name: form.name.trim(),
      description: form.description.trim(),
      enabled: form.enabled,
      groupType: form.groupType,
      groupKind: form.groupKind,
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
    groupCreatePayload,
    groupEditPatch,
    resetGroupFormForCreate,
    setFormClientIpConcurrencyLimit,
    setFormMaxQueueWaitSeconds
  }
}
