import { reactive, type ComputedRef } from 'vue'

import type { GroupKind, GroupSchedulingPolicy, GroupSummary, GroupType, ProviderDefinition } from '@/types/domain'
import { cloneGroupSchedulingPolicy, schedulingPolicyValueMax } from './groupSchedulingPolicy'

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
    schedulingPolicy: cloneGroupSchedulingPolicy()
  })

  function resetGroupFormForCreate() {
    editBaseline = undefined
    editAccessType = undefined
    Object.assign(form, {
      name: '',
      description: '',
      enabled: true,
      groupType: 'personal' as GroupType,
      groupKind: 'standard' as GroupKind,
      schedulingPolicy: cloneGroupSchedulingPolicy()
    })
  }

  function applyGroupToForm(group: GroupFormSeed) {
    Object.assign(form, {
      name: group.name,
      description: group.description ?? '',
      enabled: group.enabled,
      groupType: 'personal' as GroupType,
      groupKind: group.groupKind ?? 'standard',
      schedulingPolicy: cloneGroupSchedulingPolicy(group.schedulingPolicy ?? null)
    })
    editBaseline = currentGroupFormSnapshot()
    editAccessType = group.accessType === 'authorized' ? 'authorized' : 'owner'
  }

  // 阈值输入允许 -1 与 1..1000000，禁止 0（普通分组可选调度策略设计 §2.2）：
  // 非法值不写入表单，输入框失焦后回显当前合法值。
  function setFormDefaultSoftConcurrency(value: unknown) {
    if (typeof value !== 'number' || !Number.isInteger(value) || value === 0 || value < -1 || value > schedulingPolicyValueMax) return
    form.schedulingPolicy.defaultSoftConcurrency = value
  }

  function setFormClientIpConcurrencyLimit(value: unknown) {
    if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > schedulingPolicyValueMax) return
    form.schedulingPolicy.clientIpConcurrencyLimit = value
  }

  function groupEditPatch(): Record<string, unknown> {
    if (!editBaseline || !editAccessType) throw new Error('缺少分组编辑基线，请重新打开编辑弹窗')
    const current = currentGroupFormSnapshot()
    const payload: Record<string, unknown> = {}
    if (current.enabled !== editBaseline.enabled) payload.enabled = current.enabled
    if (editAccessType !== 'authorized') {
      if (current.name !== editBaseline.name) payload.name = current.name
      if (current.description !== editBaseline.description) payload.description = current.description
      if (!sameSchedulingPolicy(current.schedulingPolicy, editBaseline.schedulingPolicy)) {
        // PATCH 语义（普通分组可选调度策略设计 §2.2 规则 7）：整对象替换；
        // 两项都为默认值时显式发 null 清空为 NULL，不做键级合并。
        payload.schedulingPolicy = writableSchedulingPolicyValue(current.schedulingPolicy)
      }
    }
    return payload
  }

  function groupCreatePayload(): Record<string, unknown> {
    const current = currentGroupFormSnapshot()
    const schedulingPolicy = writableSchedulingPolicyValue(current.schedulingPolicy)
    return {
      name: current.name,
      description: current.description,
      enabled: current.enabled,
      groupType: current.groupType,
      groupKind: current.groupKind,
      // 创建语义（§2.2 规则 6）：缺省 → 存 NULL；非默认时传两键齐全的对象。
      ...(schedulingPolicy === null ? {} : { schedulingPolicy })
    }
  }

  function currentGroupFormSnapshot(): GroupEditForm {
    return {
      name: form.name.trim(),
      description: form.description.trim(),
      enabled: form.enabled,
      groupType: form.groupType,
      groupKind: form.groupKind,
      schedulingPolicy: cloneGroupSchedulingPolicy(form.schedulingPolicy)
    }
  }

  function writableSchedulingPolicyValue(policy: Required<GroupSchedulingPolicy>): GroupSchedulingPolicy | null {
    if (policy.defaultSoftConcurrency === -1 && policy.clientIpConcurrencyLimit === 0) return null
    return {
      defaultSoftConcurrency: policy.defaultSoftConcurrency,
      clientIpConcurrencyLimit: policy.clientIpConcurrencyLimit
    }
  }

  function sameSchedulingPolicy(
    left: Required<GroupSchedulingPolicy>,
    right: Required<GroupSchedulingPolicy>
  ): boolean {
    return left.defaultSoftConcurrency === right.defaultSoftConcurrency
      && left.clientIpConcurrencyLimit === right.clientIpConcurrencyLimit
  }

  return {
    form,
    applyGroupToForm,
    groupCreatePayload,
    groupEditPatch,
    resetGroupFormForCreate,
    setFormClientIpConcurrencyLimit,
    setFormDefaultSoftConcurrency
  }
}
