import type { SystemTeamListItem } from '@/types/domain'

export type SystemTeamEditableSnapshot = Pick<SystemTeamListItem, 'name' | 'description' | 'status'>

export interface SystemTeamEditValues {
  name: string
  description: string
  statusActive: boolean
}

export function buildSystemTeamEditPatch(
  baseline: SystemTeamEditableSnapshot,
  values: SystemTeamEditValues
): Partial<{ name: string; description: string | null; status: 'active' | 'disabled' }> {
  const patch: Partial<{ name: string; description: string | null; status: 'active' | 'disabled' }> = {}
  const name = values.name.trim()
  const description = values.description.trim() || null
  const status = values.statusActive ? 'active' : 'disabled'
  if (name !== baseline.name) patch.name = name
  if (description !== (baseline.description ?? null)) patch.description = description
  if (status !== baseline.status) patch.status = status
  return patch
}
