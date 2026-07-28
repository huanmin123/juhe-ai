import type {
  ResponseInspectionPolicyCreatePayload,
  ResponseInspectionPolicyPatchChanges
} from '@/api/contracts'
import type { ResponseInspectionPolicyDetail } from '@/types/domain'

const editablePolicyFields = [
  'name',
  'enabled',
  'priority',
  'scopeType',
  'protocolCode',
  'providerCode',
  'match',
  'action',
  'notes'
] as const satisfies readonly (keyof ResponseInspectionPolicyCreatePayload)[]

export function responseInspectionPolicyPayloadFromDetail(
  detail: ResponseInspectionPolicyDetail
): ResponseInspectionPolicyCreatePayload {
  return {
    name: detail.name,
    enabled: detail.enabled,
    priority: detail.priority,
    scopeType: detail.scopeType,
    protocolCode: detail.protocolCode,
    providerCode: detail.providerCode,
    match: structuredClone(detail.match),
    action: detail.action,
    notes: detail.notes
  }
}

export function buildResponseInspectionPolicyPatch(
  baseline: ResponseInspectionPolicyCreatePayload,
  current: ResponseInspectionPolicyCreatePayload
): ResponseInspectionPolicyPatchChanges {
  const patch: ResponseInspectionPolicyPatchChanges = {}
  const baselineRecord = baseline as unknown as Record<string, unknown>
  const currentRecord = current as unknown as Record<string, unknown>

  for (const field of editablePolicyFields) {
    const before = baselineRecord[field]
    const after = currentRecord[field]
    if (policyFieldEqual(before, after)) continue
    if (field === 'providerCode') {
      patch.providerCode = current.providerCode ?? null
    } else if (field === 'notes') {
      patch.notes = current.notes ?? null
    } else {
      Object.assign(patch, { [field]: structuredClone(after) })
    }
  }
  return patch
}

export function hasResponseInspectionPolicyChanges(patch: ResponseInspectionPolicyPatchChanges): boolean {
  return Object.keys(patch).length > 0
}

function policyFieldEqual(left: unknown, right: unknown): boolean {
  if (left === undefined || left === null) return right === undefined || right === null
  if (right === undefined || right === null) return false
  if (typeof left === 'object' || typeof right === 'object') return JSON.stringify(left) === JSON.stringify(right)
  return left === right
}
