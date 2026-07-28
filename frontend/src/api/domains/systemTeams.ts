import type { SystemTeamDetail, SystemTeamListResult, SystemTeamMutationResult } from '@/types/domain'
import type { TeamListParams } from '../contracts'
import { http, unwrap } from '../http'
import { teamListParams } from '../params'

export const systemTeamsApi = {
  list: (params?: TeamListParams) => unwrap<SystemTeamListResult>(http.get('/system-teams', { params: teamListParams(params) })),
  detail: (id: string, params?: TeamListParams) => unwrap<SystemTeamDetail>(http.get(`/system-teams/${id}`, { params: teamListParams(params) })),
  create: (payload: { name: string; description?: string; status?: 'active' | 'disabled' }) => unwrap<SystemTeamDetail>(http.post('/system-teams', payload)),
  update: (id: string, payload: { expectedUpdatedAt: string; name?: string; description?: string | null; status?: 'active' | 'disabled' }, params?: TeamListParams) => unwrap<SystemTeamMutationResult>(http.patch(`/system-teams/${id}`, payload, params ? { params: teamListParams(params) } : undefined)),
  addMembers: (id: string, payload: { systemAccountIds: string[] }) => unwrap<SystemTeamDetail>(http.post(`/system-teams/${id}/members`, payload)),
  removeMember: (id: string, memberId: string) => unwrap<SystemTeamDetail>(http.delete(`/system-teams/${id}/members/${memberId}`))
}

export const myTeamsApi = {
  list: (params?: Omit<TeamListParams, 'systemAccountId'>) => unwrap<SystemTeamListResult>(http.get('/my-teams', { params: teamListParams(params, false) })),
  detail: (id: string) => unwrap<SystemTeamDetail>(http.get(`/my-teams/${id}`))
}
